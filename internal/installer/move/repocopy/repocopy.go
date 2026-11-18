// Package repocopy carries the old system checkout's own commits over to the
// new one (345 step 13, L63): branches, a detached HEAD and stashes, so
// local changes to the system config are not lost with the old disk.
//
// The new checkout is a fresh clone of the official repository, made by
// root. Root never runs git in the old repository: hooks, config, alternates
// and everything else in it stay where they are. Root reads only the ref
// files, through safefs, and checks every name and hash. The commits come
// over as one pack, made by an unprivileged sandbox unit (the same
// confinement as the store export) from the old objects folder alone, read-
// only. Root's git takes the pack only with --strict, which checks every
// object, and writes refs only to the found commits. Branches the clone
// already has, and the release channels, land below refs/moved, so nothing
// from the old disk ever replaces what the new system builds from.
//
// Any failure after the pack starts is rolled back: the refs written are
// removed and the new repository repacked, so it ends as the clone was.
package repocopy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/storecopy"
	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
	"golang.org/x/sys/unix"
)

var (
	// Git is the git root and the pack unit run, pinned at build time.
	Git = "/run/current-system/sw/bin/git"
	// Shell runs the pack unit's two git steps, pinned at build time.
	Shell = "/run/current-system/sw/bin/bash"
)

// GitEnv is added to root's git environment: no config but the new
// repository's own, which root wrote, and no prompts.
var GitEnv = []string{
	"HOME=/var/empty",
	"GIT_CONFIG_NOSYSTEM=1",
	"GIT_CONFIG_GLOBAL=/dev/null",
	"GIT_TERMINAL_PROMPT=0",
	"GIT_NO_REPLACE_OBJECTS=1",
}

// Host runs the programs. Pack runs systemd-run with args, Git runs root's
// git with args. Both stop when ctx ends.
type Host struct {
	Pack func(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error
	Git  func(ctx context.Context, stdin io.Reader, stdout io.Writer, args ...string) error
}

// Result is what the copy did. Refused names why nothing was tried, Failed
// why a copy that was tried was rolled back.
type Result struct {
	Branches int    `json:"branches"`
	Moved    int    `json:"moved"`
	Stashes  int    `json:"stashes"`
	Skipped  int    `json:"skipped"`
	Refused  string `json:"refused,omitempty"`
	Failed   string `json:"failed,omitempty"`
}

const (
	// OldObjects is where the pack unit sees the old objects folder.
	OldObjects = "/old"

	maxConfig     = 64 << 10
	maxHead       = 256
	maxLooseRef   = 128
	maxPackedRefs = 4 << 20
	maxStashLog   = 1 << 20
	// maxRefs caps the refs read from the old disk, maxStashes the stash
	// entries; a desktop's system checkout has a handful.
	maxRefs     = 10_000
	maxStashes  = 1000
	maxRefDepth = 10
	maxRefName  = 200
	// maxOutput caps what root's git may print for one step.
	maxOutput = 4 << 20

	// stepLimit stops one of root's git steps that runs too long.
	stepLimit = 30 * time.Minute

	movedPrefix = "refs/moved/"
)

// Channels always land below refs/moved: the new system's own channel
// branch comes from the official repository, never from the old disk.
var Channels = []string{"dev", "staging", "stable"}

var (
	shaLine   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	component = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+-]*$`)
	// objectName is a full object name; git reads it as the object, not as
	// the branch.
	objectName = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	// headName is HEAD, FETCH_HEAD, ORIG_HEAD and the like.
	headName = regexp.MustCompile(`^[A-Z_]*HEAD$`)
)

// shortFirsts are first parts that make a branch name look like another
// ref when git reads a short name (L140).
var shortFirsts = []string{"refs", "heads", "tags", "remotes", "moved", "stash", "origin"}

// packScript makes an empty repository in the unit's own /tmp and packs the
// commits named on stdin, and all they reach, from the old objects alone.
// No config, hook or ref of the old repository is read.
const packScript = `set -eu
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_TERMINAL_PROMPT=0 GIT_NO_REPLACE_OBJECTS=1
"$1" init -q --bare --template= /tmp/r
GIT_DIR=/tmp/r GIT_OBJECT_DIRECTORY=` + OldObjects + ` exec "$1" -c core.commitGraph=false -c core.multiPackIndex=false -c pack.useBitmaps=false -c pack.threads=4 pack-objects --revs --stdout -q`

// PackArgs returns systemd-run's arguments for the pack unit, with the old
// objects folder objects bound read-only.
func PackArgs(objects string) ([]string, error) {
	return storecopy.UnitArgs(storecopy.PackUnit, storecopy.PackLimit,
		storecopy.Bind{Src: objects, Dst: OldObjects}, storecopy.Bind{},
		Shell, "-c", packScript, "sh", Git)
}

// gitArgs are root's git arguments for the new repository at repo.
func gitArgs(repo string, args ...string) []string {
	return append([]string{
		"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
		"-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "pack.threads=4",
		"--git-dir=" + repo + "/.git",
	}, args...)
}

// tip is one ref to write: its name in the new repository, or "" while
// placement is open, and the old name for branches.
type tip struct {
	branch string // the name below refs/heads, for branches
	ref    string // the full ref to write once placed
	sha    string
	stash  bool
}

// Copy carries the commits of the old repository gitDir (the .git folder,
// on the old disk), whose objects folder the unit binds as objects, into
// the new repository newRepo. budget caps the pack. The error is only for a
// rollback that failed: the new repository may then hold old refs.
func Copy(ctx context.Context, host Host, gitDir *safefs.Dir, objects, newRepo string, budget int64) (Result, error) {
	var res Result
	if budget <= 0 {
		return res, errors.New("repo copy: no budget")
	}
	if word := shape(gitDir); word != "" {
		res.Refused = word
		return res, nil
	}
	tips, skipped, word := readTips(gitDir)
	res.Skipped = skipped
	if word != "" {
		res.Refused = word
		return res, nil
	}
	if len(tips) == 0 {
		return res, nil
	}
	c := copier{host: host, repo: newRepo}
	existing, err := c.refs(ctx)
	if err != nil {
		res.Failed = "refs"
		return res, nil
	}
	args, err := PackArgs(objects)
	if err != nil {
		res.Refused = "path"
		return res, nil
	}
	written, step, err := c.carry(ctx, args, tips, existing, budget, &res)
	if err == nil {
		return res, nil
	}
	if errors.Is(err, errNothing) {
		// The pack came in, but no tip was a commit with a free name:
		// drop the pack again. The skips are counted already.
		if rerr := c.rollback(ctx, nil); rerr != nil {
			return res, fmt.Errorf("repo copy: dropping the unused pack failed: %w", rerr)
		}
		return res, nil
	}
	res.Branches, res.Moved, res.Stashes = 0, 0, 0
	res.Skipped = skipped + len(tips)
	res.Failed = step
	if rerr := c.rollback(ctx, written); rerr != nil {
		return res, fmt.Errorf("repo copy failed at %s (%v), and the rollback failed: %w", step, err, rerr)
	}
	return res, nil
}

type copier struct {
	host Host
	repo string
}

func (c copier) git(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, stepLimit)
	defer cancel()
	var out capBuf
	out.left = maxOutput
	var in io.Reader
	if stdin != "" {
		in = strings.NewReader(stdin)
	}
	if err := c.host.Git(ctx, in, &out, gitArgs(c.repo, args...)...); err != nil {
		return nil, err
	}
	if out.over {
		return nil, errors.New("git printed more than the cap")
	}
	return out.Bytes(), nil
}

// refs lists the refs the new repository has.
func (c copier) refs(ctx context.Context) (refSet, error) {
	s := refSet{names: map[string]bool{}, dirs: map[string]bool{}}
	out, err := c.git(ctx, "", "for-each-ref", "--format=%(refname)")
	if err != nil {
		return s, err
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(string(out), "\n"), "\n") {
		if line != "" {
			s.add(line)
		}
	}
	return s, nil
}

// carry packs, imports, places and writes. It returns the refs written so
// far and the step that failed.
func (c copier) carry(ctx context.Context, args []string, tips []tip, existing refSet, budget int64, res *Result) ([]tip, string, error) {
	if err := c.pack(ctx, args, tips, budget); err != nil {
		return nil, "pack", err
	}
	commits, err := c.commits(ctx, tips)
	if err != nil {
		return nil, "kind", err
	}
	placed := place(tips, commits, existing, res)
	if len(placed) == 0 {
		return nil, "", errNothing
	}
	var cmds strings.Builder
	for _, t := range placed {
		fmt.Fprintf(&cmds, "create %s %s\n", t.ref, t.sha)
	}
	if _, err := c.git(ctx, cmds.String(), "update-ref", "-m", "gjallar move", "--stdin"); err != nil {
		// update-ref --stdin writes all refs or none.
		return nil, "refs", err
	}
	if _, err := c.git(ctx, "", "fsck", "--connectivity-only", "--no-dangling", "--no-progress"); err != nil {
		return placed, "check", err
	}
	if _, err := c.git(ctx, "", "repack", "-a", "-d", "-q"); err != nil {
		return placed, "repack", err
	}
	return placed, "", nil
}

var errNothing = errors.New("no commit to keep")

// pack runs the unit and root's index-pack side by side, joined by a pipe
// that stops at budget bytes.
func (c copier) pack(ctx context.Context, args []string, tips []tip, budget int64) error {
	ctx, cancel := context.WithTimeout(ctx, storecopy.PackLimit)
	defer cancel()
	var want strings.Builder
	seen := map[string]bool{}
	for _, t := range tips {
		if !seen[t.sha] {
			seen[t.sha] = true
			want.WriteString(t.sha + "\n")
		}
	}
	pr, pw := io.Pipe()
	lw := &limitWriter{w: pw, left: budget}
	packDone := make(chan error, 1)
	go func() {
		err := c.host.Pack(ctx, args, strings.NewReader(want.String()), lw)
		if err == nil && lw.over {
			err = errTooBig
		}
		pw.CloseWithError(err)
		packDone <- err
	}()
	var out capBuf
	out.left = maxOutput
	idxErr := c.host.Git(ctx, pr, &out, gitArgs(c.repo, "index-pack", "--stdin", "--strict",
		"--max-input-size="+strconv.FormatInt(budget, 10))...)
	if idxErr != nil {
		cancel()
	}
	pr.CloseWithError(errStopped)
	packErr := <-packDone
	switch {
	case packErr != nil:
		return fmt.Errorf("pack: %w", packErr)
	case idxErr != nil:
		return fmt.Errorf("index-pack: %w", idxErr)
	}
	return nil
}

var (
	errTooBig  = errors.New("the pack is over its budget")
	errStopped = errors.New("index-pack stopped reading")
)

// commits returns the tips that name commits in the new repository now.
func (c copier) commits(ctx context.Context, tips []tip) (map[string]bool, error) {
	var in strings.Builder
	for _, t := range tips {
		in.WriteString(t.sha + "\n")
	}
	out, err := c.git(ctx, in.String(), "cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}
	commits := map[string]bool{}
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[1] == "commit" && shaLine.MatchString(f[0]) {
			commits[f[0]] = true
		}
	}
	return commits, nil
}

// rollback removes the refs written and drops what nothing reaches.
func (c copier) rollback(ctx context.Context, written []tip) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stepLimit)
	defer cancel()
	if len(written) > 0 {
		var cmds strings.Builder
		for _, t := range written {
			fmt.Fprintf(&cmds, "delete %s %s\n", t.ref, t.sha)
		}
		if _, err := c.git(ctx, cmds.String(), "update-ref", "--stdin"); err != nil {
			return err
		}
	}
	if _, err := c.git(ctx, "", "repack", "-a", "-d", "-q"); err != nil {
		return err
	}
	_, err := c.git(ctx, "", "prune", "--expire=now")
	return err
}

// place picks each commit's ref: a branch keeps its name when the new
// repository has nothing in the way and keepsName allows it, else it goes
// below refs/moved/heads. Anything else is skipped.
func place(tips []tip, commits map[string]bool, existing refSet, res *Result) []tip {
	var placed []tip
	for _, t := range tips {
		if !commits[t.sha] {
			res.Skipped++
			continue
		}
		switch {
		case t.branch != "":
			own := "refs/heads/" + t.branch
			moved := movedPrefix + "heads/" + t.branch
			switch {
			case keepsName(t.branch, existing) && existing.free(own):
				t.ref = own
				res.Branches++
			case existing.free(moved):
				t.ref = moved
				res.Moved++
			default:
				res.Skipped++
				continue
			}
		case !existing.free(t.ref):
			res.Skipped++
			continue
		case t.stash:
			res.Stashes++
		default:
			res.Moved++
		}
		existing.add(t.ref)
		placed = append(placed, t)
	}
	return placed
}

// keepsName says whether an old branch may keep its name. git reads a short
// name X as refs/X, refs/tags/X, refs/heads/X, refs/remotes/X in that order,
// so an old branch "origin/dev" would win over the remote's dev wherever a
// tool names it short (L140). Such names, the channels and names git reads
// as something else go below refs/moved/heads instead.
func keepsName(branch string, existing refSet) bool {
	first, _, _ := strings.Cut(branch, "/")
	switch {
	case slices.Contains(Channels, branch),
		slices.Contains(shortFirsts, first),
		headName.MatchString(first),
		objectName.MatchString(branch),
		existing.names["refs/remotes/"+first] || existing.dirs["refs/remotes/"+first]:
		return false
	}
	return true
}

// refSet holds ref names and every folder they make, so a new ref that is
// a folder of an old one, or the other way round, is not free.
type refSet struct{ names, dirs map[string]bool }

func (s refSet) add(ref string) {
	s.names[ref] = true
	for i := range len(ref) {
		if ref[i] == '/' {
			s.dirs[ref[:i]] = true
		}
	}
}

func (s refSet) free(ref string) bool {
	if s.names[ref] || s.dirs[ref] {
		return false
	}
	for i := range len(ref) {
		if ref[i] == '/' && s.names[ref[:i]] {
			return false
		}
	}
	return true
}

// shape refuses a repository root's git could not copy as it is: one that
// borrows objects, is shallow or partial, is a worktree, keeps refs in a
// reftable or uses other hashes. Every check reads without following links,
// and anything unreadable refuses too.
func shape(d *safefs.Dir) string {
	st, err := d.Lstat("objects")
	if err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return "no-repo"
	}
	for rel, word := range map[string]string{
		"objects/info/alternates":      "alternates",
		"objects/info/http-alternates": "alternates",
		"shallow":                      "shallow",
		"commondir":                    "worktree",
		"reftable":                     "reftable",
	} {
		if _, err := d.Lstat(rel); !errors.Is(err, unix.ENOENT) {
			return word
		}
	}
	cfg, err := d.ReadFile("config", maxConfig)
	if err != nil {
		return "no-repo"
	}
	low := bytes.ToLower(cfg)
	for key, word := range map[string]string{
		"objectformat": "sha256",
		"refstorage":   "reftable",
		"partialclone": "partial",
	} {
		if bytes.Contains(low, []byte(key)) {
			return word
		}
	}
	return ""
}

// readTips reads the branches, a detached HEAD and the stashes. Loose
// branch files win over packed ones, as in git. Refs with a bad name or
// content are skipped and counted.
func readTips(d *safefs.Dir) ([]tip, int, string) {
	branches := map[string]string{}
	skipped := 0
	packed, err := d.ReadFile("packed-refs", maxPackedRefs)
	switch {
	case err == nil:
		s, ok := readPacked(packed, branches)
		if !ok {
			return nil, 0, "refs"
		}
		skipped = s
	case !errors.Is(err, unix.ENOENT):
		return nil, 0, "refs"
	}
	w := walker{d: d, branches: branches}
	if !w.walk("refs/heads", 0) {
		return nil, 0, "refs"
	}
	skipped += w.skipped

	var tips []tip
	for _, name := range slices.Sorted(maps.Keys(branches)) {
		tips = append(tips, tip{branch: name, sha: branches[name]})
	}
	if head, err := d.ReadFile("HEAD", maxHead); err == nil {
		if sha, ok := strings.CutSuffix(string(head), "\n"); ok && shaLine.MatchString(sha) {
			tips = append(tips, tip{ref: movedPrefix + "HEAD", sha: sha})
		}
	}
	stashes, s, ok := readStashes(d)
	if !ok {
		return nil, 0, "refs"
	}
	skipped += s
	return append(tips, stashes...), skipped, ""
}

// readPacked reads packed-refs into branches. It reports the refs skipped,
// and false when there are too many or a line is too long.
func readPacked(data []byte, branches map[string]string) (int, bool) {
	n, skipped := 0, 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 4096), 4096)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		if n++; n > maxRefs {
			return skipped, false
		}
		sha, ref, ok := strings.Cut(line, " ")
		name, isBranch := strings.CutPrefix(ref, "refs/heads/")
		switch {
		case ok && isBranch && shaLine.MatchString(sha) && RefName(name):
			branches[name] = sha
		case ok && !strings.HasPrefix(ref, "refs/heads/"):
			// Tags and remotes come from the official repository.
		default:
			skipped++
		}
	}
	if sc.Err() != nil {
		return skipped, false
	}
	return skipped, true
}

type walker struct {
	d        *safefs.Dir
	branches map[string]string
	seen     int
	skipped  int
}

// walk reads the loose branch files below rel. It returns false when the
// folder is too deep, too full or cannot be read.
func (w *walker) walk(rel string, depth int) bool {
	if depth > maxRefDepth {
		return false
	}
	names, err := w.d.Names(rel, maxRefs)
	if err != nil {
		return errors.Is(err, unix.ENOENT) && depth == 0
	}
	for _, n := range names {
		if w.seen++; w.seen > maxRefs {
			return false
		}
		p := rel + "/" + n
		st, err := w.d.Lstat(p)
		if err != nil {
			w.skipped++
			continue
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if !w.walk(p, depth+1) {
				return false
			}
		case unix.S_IFREG:
			name := strings.TrimPrefix(p, "refs/heads/")
			data, err := w.d.ReadFile(p, maxLooseRef)
			sha, ok := strings.CutSuffix(string(data), "\n")
			if err != nil || !ok || !shaLine.MatchString(sha) || !RefName(name) {
				// A symbolic ref, a bad name or a changed file.
				w.skipped++
				delete(w.branches, name)
				continue
			}
			w.branches[name] = sha
		default:
			w.skipped++
		}
	}
	return true
}

// readStashes reads the stash list from its log: the last line is stash 0.
func readStashes(d *safefs.Dir) ([]tip, int, bool) {
	data, err := d.ReadFile("logs/refs/stash", maxStashLog)
	if errors.Is(err, unix.ENOENT) {
		return nil, 0, true
	}
	if err != nil {
		return nil, 0, false
	}
	text, _ := strings.CutSuffix(string(data), "\n")
	if text == "" {
		return nil, 0, true
	}
	lines := strings.Split(text, "\n")
	skipped := 0
	if len(lines) > maxStashes {
		skipped = len(lines) - maxStashes
		lines = lines[len(lines)-maxStashes:]
	}
	var tips []tip
	for i := len(lines) - 1; i >= 0; i-- {
		f := strings.SplitN(lines[i], " ", 3)
		if len(f) < 3 || !shaLine.MatchString(f[0]) || !shaLine.MatchString(f[1]) {
			skipped++
			continue
		}
		n := len(lines) - 1 - i
		tips = append(tips, tip{ref: movedPrefix + "stash/" + strconv.Itoa(n), sha: f[1], stash: true})
	}
	return tips, skipped, true
}

// RefName reports whether name, below refs/heads, is a plain branch name.
// It is stricter than git: only letters, digits and ._+- , no part that
// starts with a dot or ends in .lock or a dot, at most 10 parts.
func RefName(name string) bool {
	if name == "" || len(name) > maxRefName {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) > maxRefDepth {
		return false
	}
	for _, p := range parts {
		if !component.MatchString(p) || strings.Contains(p, "..") ||
			strings.HasSuffix(p, ".") || strings.HasSuffix(p, ".lock") {
			return false
		}
	}
	return true
}

// capBuf keeps up to left bytes and notes whether more came.
type capBuf struct {
	bytes.Buffer
	left int
	over bool
}

func (b *capBuf) Write(p []byte) (int, error) {
	n := len(p)
	if n > b.left {
		b.over = true
		p = p[:b.left]
	}
	b.left -= len(p)
	b.Buffer.Write(p)
	return n, nil
}

// limitWriter passes up to left bytes on and fails after.
type limitWriter struct {
	w    io.Writer
	left int64
	over bool
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.left {
		l.over = true
		return 0, errTooBig
	}
	n, err := l.w.Write(p)
	l.left -= int64(n)
	return n, err
}
