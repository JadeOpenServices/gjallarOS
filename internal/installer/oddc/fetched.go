package oddc

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	portable "github.com/JadeOpenServices/oddc/pkg/oddc"
)

// Remote is the git repository ODDC answers come from.
var Remote = portable.Remote

// FetchedSource asks ODDC for this machine only: on first resolve it
// fetches the matched model's answer (reference closure, evidence and
// revision) at commit Rev into Root, replacing an earlier answer, and
// resolves from it. Nothing else of the catalog reaches the machine.
type FetchedSource struct {
	Root string
	Rev  string

	fetched bool
	err     error
}

func (source *FetchedSource) answer() EmbeddedSource {
	return EmbeddedSource{
		Root:       source.Root,
		Repository: "github:" + portable.Repository,
		Revision:   portable.DirSource{Root: source.Root}.Revision(),
	}
}

func (source *FetchedSource) Metadata() SourceMetadata {
	return source.answer().Metadata()
}

// fetch asks ODDC once; later calls return the first outcome.
func (source *FetchedSource) fetch(identity Identity) error {
	if !source.fetched {
		source.err = source.ask(identity)
		source.fetched = true
	}

	return source.err
}

func (source *FetchedSource) ask(identity Identity) error {
	upstream, done, err := gitSource(source.Rev)
	if err != nil {
		return err
	}
	defer done()

	if err := os.MkdirAll(filepath.Dir(source.Root), 0o755); err != nil {
		return err
	}
	next := source.Root + ".next"
	if err := os.RemoveAll(next); err != nil {
		return err
	}

	_, err = portable.Fetch(
		upstream,
		portable.MachineIdentity{
			FormFactor:     identity.FormFactor,
			SysVendor:      identity.SysVendor,
			ProductName:    identity.ProductName,
			ProductVersion: identity.ProductVersion,
			BoardVendor:    identity.BoardVendor,
			BoardName:      identity.BoardName,
			BoardVersion:   identity.BoardVersion,
		},
		next,
	)
	if errors.Is(err, portable.ErrNoModelMatch) {
		// No model: no answer, not an earlier machine's.
		return errors.Join(os.RemoveAll(source.Root), ErrNoMatch)
	}
	if err != nil {
		return fmt.Errorf("fetch ODDC answer: %w", err)
	}

	return publish(next, source.Root)
}

// publish replaces root with the complete answer in next. The answer is
// part of the checkout its owner builds from, so its modes must not
// follow the writer's umask: the installer resumed by installer-resume
// runs with 0077 and left it unreadable to the owner's install.sh
// (e2e-fw13, 2026-10-20).
func publish(next, root string) error {
	err := filepath.WalkDir(next, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return os.Chmod(path, 0o755)
		case entry.Type().IsRegular():
			return os.Chmod(path, 0o644)
		}
		return nil
	})
	if err != nil {
		return err
	}

	if err := os.RemoveAll(root); err != nil {
		return err
	}
	return os.Rename(next, root)
}

// gitSource fetches ODDC's tree at commit rev into a scratch repository;
// done removes it.
func gitSource(rev string) (*portable.GitSource, func(), error) {
	if len(rev) != 40 || strings.Trim(rev, "0123456789abcdef") != "" {
		return nil, nil, fmt.Errorf("ask ODDC: %q is not a commit", rev)
	}

	scratch, err := os.MkdirTemp("", "gjallar-oddc-")
	if err != nil {
		return nil, nil, err
	}
	done := func() { os.RemoveAll(scratch) }

	upstream, err := portable.NewGitSource(filepath.Join(scratch, "git"), Remote, rev)
	if err != nil {
		done()
		return nil, nil, fmt.Errorf("ask ODDC: %w", err)
	}

	return upstream, done, nil
}

// Refresh moves the answer in root to commit rev: it fetches the same
// model again and replaces the answer only once the new one is complete.
// It returns the answer's earlier revision. Without an answer (no model
// matched this machine) there is nothing to refresh, and it returns "".
func Refresh(root, rev string) (string, error) {
	current := AnswerRevision(root)
	if current == "" || current == rev {
		return current, nil
	}

	model, err := AnswerModel(root)
	if err != nil {
		return current, err
	}

	upstream, done, err := gitSource(rev)
	if err != nil {
		return current, err
	}
	defer done()

	next := root + ".next"
	if err := os.RemoveAll(next); err != nil {
		return current, err
	}
	if err := portable.FetchModel(upstream, model, next); err != nil {
		return current, fmt.Errorf("fetch ODDC answer: %w", err)
	}

	return current, publish(next, root)
}

func (source *FetchedSource) Resolve(identity Identity) (Resolved, error) {
	if err := source.fetch(identity); err != nil {
		return Resolved{}, err
	}

	return source.answer().Resolve(identity)
}

func (source *FetchedSource) ResolveWithHost(
	identity Identity,
	host []HostOverlay,
) (Resolved, error) {
	if err := source.fetch(identity); err != nil {
		return Resolved{}, err
	}

	return source.answer().ResolveWithHost(identity, host)
}
