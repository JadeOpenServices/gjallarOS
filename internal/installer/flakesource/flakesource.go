// Package flakesource stages the GjallarOS repository as a clean flake source.
//
// The flake must be referenced as path: because it imports untracked,
// machine-local generated/*.nix files that a git+file: reference hides. A
// path: reference copies the whole directory into the Nix store, though,
// including .git (gigabytes of history) and ignored scratch directories such
// as .vm. On live media the store is RAM, so that copy alone can exhaust
// memory. Stage copies only what evaluation needs into a temporary directory.
package flakesource

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// BuildContextName marks sources prepared by the supported deployment entrypoints.
// This is a workflow guard, not an authorization boundary against root.
const BuildContextName = ".gjallar-build-context.json"

// LocalName is where a developer-mode rebuild stages root's copy of the
// local folder. A checkout's own entry of that name is never staged.
const LocalName = ".gjallar-local"

// excluded reports whether a repository entry is left out of the flake
// source. .git is skipped at any depth (submodules carry a .git file).
func excluded(rel string, entry fs.DirEntry) bool {
	name := entry.Name()
	if name == ".git" {
		return true
	}
	if strings.ContainsRune(rel, filepath.Separator) {
		return false
	}
	return Scratch(name)
}

// Scratch reports whether a top-level checkout entry is local scratch (VM
// disks, agent settings, build links) that never leaves the machine.
func Scratch(name string) bool {
	switch {
	case name == ".vm", name == ".claude", name == ".direnv", name == BuildContextName, name == LocalName:
		return true
	case name == "result", strings.HasPrefix(name, "result-"):
		return true
	}
	return false
}

type Source struct {
	Dir string
}

// Ref returns the path: flake reference for attr in the staged source.
func (s Source) Ref(attr string) string {
	if attr == "" {
		return "path:" + s.Dir
	}
	return "path:" + s.Dir + "#" + attr
}

// Close removes the staged copy.
func (s Source) Close() error {
	return os.RemoveAll(filepath.Dir(s.Dir))
}

// BuildSource is what a rebuild knows about the source it builds: Revision
// is the checkout's HEAD commit (with -dirty when tracked files differ), ODDC
// the oddc commit the build uses and ODDCPinned the one flake.lock pins.
// The system records them; they differ only under an oddc override. Local
// is the developer-mode folder staged at LocalName, if any.
type BuildSource struct {
	Revision   string       `json:"revision,omitempty"`
	ODDC       string       `json:"oddc,omitempty"`
	ODDCPinned string       `json:"oddcPinned,omitempty"`
	Local      *LocalSource `json:"local,omitempty"`
}

// LocalSource is the manifest hash and the file list of a staged local
// folder.
type LocalSource struct {
	Hash  string   `json:"hash"`
	Files []string `json:"files"`
}

type buildContext struct {
	SchemaVersion int          `json:"schemaVersion"`
	Entrypoint    string       `json:"entrypoint"`
	Source        *BuildSource `json:"source,omitempty"`
}

// Stage copies repo (which must contain flake.nix) into a new temporary
// directory under tmpRoot ("" means os.TempDir) and returns it.
func Stage(repo, tmpRoot string) (Source, error) {
	return StageSource(repo, tmpRoot, nil)
}

// StageSource is Stage that also records facts about the source in the build
// context; nil records none.
func StageSource(repo, tmpRoot string, facts *BuildSource) (Source, error) {
	root, err := filepath.Abs(repo)
	if err != nil {
		return Source{}, fmt.Errorf("resolve repository: %w", err)
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return Source{}, fmt.Errorf("resolve repository: %w", err)
	}
	if info, err := os.Stat(filepath.Join(root, "flake.nix")); err != nil || !info.Mode().IsRegular() {
		return Source{}, fmt.Errorf("flake.nix not found in %s", root)
	}

	parent, err := os.MkdirTemp(tmpRoot, "gjallar-flake-")
	if err != nil {
		return Source{}, fmt.Errorf("create flake staging directory: %w", err)
	}
	source := Source{Dir: filepath.Join(parent, "source")}
	if err := copyTree(root, source.Dir); err != nil {
		_ = source.Close()
		return Source{}, fmt.Errorf("stage flake source: %w", err)
	}
	// Never reuse a checkout marker (including symlinks); create it only in the
	// disposable source, after staging has succeeded.
	data, err := json.Marshal(buildContext{SchemaVersion: 1, Entrypoint: "gjallarctl", Source: facts})
	if err != nil {
		_ = source.Close()
		return Source{}, fmt.Errorf("write build context: %w", err)
	}
	if err := os.WriteFile(filepath.Join(source.Dir, BuildContextName), append(data, '\n'), 0o600); err != nil {
		_ = source.Close()
		return Source{}, fmt.Errorf("write build context: %w", err)
	}
	return source, nil
}

func copyTree(root, destination string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel != "." && excluded(rel, entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch mode := info.Mode(); {
		case mode.IsDir():
			return os.MkdirAll(target, mode.Perm()|0o700)
		case mode&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case mode.IsRegular():
			return copyFile(path, target, mode.Perm())
		default:
			return nil // sockets, fifos and devices are not flake source
		}
	})
}

func copyFile(source, target string, perm fs.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
