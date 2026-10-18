// Package systemcheckout checks the root-owned checkout the system builds
// from (ROADMAP section 14). Root never builds what a user account can
// change: every file in the checkout and every directory above it must be
// owned by root and not writable by group or others, HEAD must be on a branch
// of the official origin, and no tracked file may differ from HEAD except
// the oddc pin in flake.lock, which must be a commit of an official ODDC
// branch.
package systemcheckout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
)

// Path is the system checkout. /etc/gjallar/repository names it when the
// system builds from it.
const Path = "/etc/gjallar/system"

// Origins are the URLs the system checkout may track.
var Origins = []string{
	"https://github.com/JadeOpenServices/gjallarOS.git",
	"https://github.com/JadeOpenServices/gjallarOS",
}

// ODDCOrigin is the official ODDC repository. Only commits reachable from
// its stable, staging and verify/* branches count: GitHub also serves
// commits that exist only in a fork under the parent's URL (ODDC 108).
var ODDCOrigin = "https://github.com/JadeOpenServices/oddc.git"

// ODDCBranches are the official ODDC branch patterns.
var ODDCBranches = []string{"refs/heads/stable", "refs/heads/staging", "refs/heads/verify"}

var (
	rootUID      uint32 = 0
	checkParents        = true
)

// Active reports whether the repository file names the system checkout.
func Active(repositoryFile string) bool {
	data, err := os.ReadFile(repositoryFile)
	return err == nil && filepath.Clean(strings.TrimSpace(string(data))) == Path
}

// CheckOwnership walks the checkout without following symlinks. It fails on
// a missing or empty checkout, on anything not owned by root or writable by
// group or others, on a directory above it that someone else could change,
// and on a symlink that leaves the checkout.
func CheckOwnership(root string) error {
	info, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("the checkout at %s is missing", root)
	}
	if err != nil {
		return fmt.Errorf("inspect checkout: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("the checkout at %s is not a directory", root)
	}
	if _, err := os.Lstat(filepath.Join(root, ".git")); err != nil {
		return fmt.Errorf("the checkout at %s is empty or not a git checkout", root)
	}
	for dir := filepath.Dir(root); checkParents; dir = filepath.Dir(dir) {
		if err := checkEntry(dir); err != nil {
			return err
		}
		if dir == "/" {
			break
		}
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := checkEntry(path); err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		resolved := filepath.Join(filepath.Dir(path), target)
		if filepath.IsAbs(target) || (resolved != root && !strings.HasPrefix(resolved, root+"/")) {
			return fmt.Errorf("%s points outside the checkout", path)
		}
		return nil
	})
}

func checkEntry(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s has no owner information", path)
	}
	if stat.Uid != rootUID {
		return fmt.Errorf("%s is owned by uid %d, not root", path, stat.Uid)
	}
	if info.Mode()&fs.ModeSymlink == 0 && info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or others", path)
	}
	return nil
}

// Source is what the checkout builds.
type Source struct {
	Revision string
	// Branches are the origin branches that contain Revision.
	Branches []string
	// ODDC is the oddc commit flake.lock pins; ODDCMoved is true when it
	// differs from the pin at HEAD.
	ODDC      string
	ODDCMoved bool
}

// Inspect checks that HEAD is on a branch of the official origin and that
// no tracked file differs from HEAD apart from the oddc node of flake.lock.
func Inspect(ctx context.Context, root string) (Source, error) {
	origin, err := git(ctx, root, "config", "--get", "remote.origin.url")
	if err != nil {
		return Source{}, fmt.Errorf("the checkout has no origin: %w", err)
	}
	if !official(origin) {
		return Source{}, fmt.Errorf("origin is %s, not the official GjallarOS repository", origin)
	}
	revision, err := git(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return Source{}, fmt.Errorf("read HEAD: %w", err)
	}
	contains, err := git(ctx, root, "for-each-ref", "--contains", revision, "--format=%(refname)", "refs/remotes/origin")
	if err != nil {
		return Source{}, fmt.Errorf("find origin branches: %w", err)
	}
	source := Source{Revision: revision}
	for _, ref := range strings.Fields(contains) {
		if ref != "refs/remotes/origin/HEAD" {
			source.Branches = append(source.Branches, strings.TrimPrefix(ref, "refs/remotes/origin/"))
		}
	}
	if len(source.Branches) == 0 {
		return Source{}, fmt.Errorf("HEAD %s is on no branch of the official origin", short(revision))
	}

	status, err := gitRaw(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=no", "--ignore-submodules=none")
	if err != nil {
		return Source{}, fmt.Errorf("read checkout status: %w", err)
	}
	lockChanged := false
	for _, entry := range strings.Split(status, "\x00") {
		if entry == "" {
			continue
		}
		if len(entry) > 3 && entry[3:] == "flake.lock" && strings.TrimSpace(entry[:2]) == "M" {
			lockChanged = true
			continue
		}
		return Source{}, fmt.Errorf("tracked file differs from HEAD: %s", strings.TrimSpace(entry))
	}

	headLock, err := git(ctx, root, "show", "HEAD:flake.lock")
	if err != nil {
		return Source{}, fmt.Errorf("read flake.lock at HEAD: %w", err)
	}
	headRev, headRest, err := splitODDC([]byte(headLock))
	if err != nil {
		return Source{}, fmt.Errorf("flake.lock at HEAD: %w", err)
	}
	source.ODDC = headRev
	if !lockChanged {
		return source, nil
	}
	current, err := os.ReadFile(filepath.Join(root, "flake.lock"))
	if err != nil {
		return Source{}, err
	}
	rev, rest, err := splitODDC(current)
	if err != nil {
		return Source{}, fmt.Errorf("flake.lock: %w", err)
	}
	if !reflect.DeepEqual(rest, headRest) {
		return Source{}, errors.New("flake.lock differs from HEAD beyond the oddc pin")
	}
	source.ODDC, source.ODDCMoved = rev, rev != headRev
	return source, nil
}

// splitODDC returns the oddc commit of a flake.lock and the lock without
// its oddc node.
func splitODDC(data []byte) (string, map[string]any, error) {
	var lock map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&lock); err != nil {
		return "", nil, err
	}
	nodes, _ := lock["nodes"].(map[string]any)
	node, _ := nodes["oddc"].(map[string]any)
	locked, _ := node["locked"].(map[string]any)
	rev, _ := locked["rev"].(string)
	if rev == "" {
		return "", nil, errors.New("no oddc pin")
	}
	delete(nodes, "oddc")
	return rev, lock, nil
}

// OfficialODDC fails unless rev is reachable from an official ODDC branch.
// cache is a bare partial clone (commits only) kept between runs; only the
// branches the official repository advertises are fetched into it.
func OfficialODDC(ctx context.Context, cache, rev string) error {
	if len(rev) != 40 || strings.Trim(rev, "0123456789abcdef") != "" {
		return fmt.Errorf("oddc %q is not a full commit", rev)
	}
	if !filepath.IsAbs(cache) {
		return fmt.Errorf("no cache directory for the official ODDC branches")
	}
	if _, err := os.Stat(filepath.Join(cache, "HEAD")); err != nil {
		if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
			return err
		}
		if _, err := git(ctx, "", "clone", "--quiet", "--bare", "--filter=tree:0", "--no-tags", ODDCOrigin, cache); err != nil {
			return fmt.Errorf("fetch official ODDC branches: %w", err)
		}
	} else if _, err := git(ctx, cache, "fetch", "--quiet", "--prune", "--no-tags", "origin", "+refs/heads/*:refs/heads/*"); err != nil {
		return fmt.Errorf("fetch official ODDC branches: %w", err)
	}
	args := append([]string{"for-each-ref", "--contains", rev, "--format=%(refname)"}, ODDCBranches...)
	out, err := git(ctx, cache, args...)
	if err != nil || strings.TrimSpace(out) == "" {
		return fmt.Errorf("oddc %s is on no official ODDC branch (stable, staging, verify/*)", short(rev))
	}
	return nil
}

func official(origin string) bool {
	for _, url := range Origins {
		if origin == url {
			return true
		}
	}
	return false
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitRaw(ctx, dir, args...)
	return strings.TrimSpace(out), err
}

func gitRaw(ctx context.Context, dir string, args ...string) (string, error) {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	// A missing commit must not be fetched on demand from the promisor:
	// GitHub would hand out a fork commit.
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return "", fmt.Errorf("%w: %s", err, message)
		}
		return "", err
	}
	return string(out), nil
}
