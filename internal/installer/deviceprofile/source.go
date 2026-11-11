package deviceprofile

import (
	"fmt"
	"path/filepath"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
)

// AnswerDir is where a gjallarOS tree keeps the ODDC answer for its machine;
// the system configuration reads it as oddc.catalog.
const AnswerDir = "generated/oddc"

// CurrentSource asks ODDC, at the commit repo's flake.lock pins, for this
// machine's model and keeps the answer in repo's generated/oddc.
func CurrentSource(repo string) (*oddc.FetchedSource, error) {
	rev, err := oddc.LockedRevision(repo)
	if err != nil {
		return nil, fmt.Errorf("find the ODDC commit to ask: %w", err)
	}

	return &oddc.FetchedSource{
		Root: filepath.Join(repo, filepath.FromSlash(AnswerDir)),
		Rev:  rev,
	}, nil
}

// RefreshAnswer moves repo's ODDC answer to the commit its flake.lock pins
// and returns the answer's earlier and current revisions.
func RefreshAnswer(repo string) (string, string, error) {
	rev, err := oddc.LockedRevision(repo)
	if err != nil {
		return "", "", fmt.Errorf("find the ODDC commit to ask: %w", err)
	}
	return RefreshAnswerAt(repo, rev)
}

// RefreshAnswerAt moves repo's ODDC answer to commit rev, for a rebuild that
// overrides the oddc input: the answer has to come from the same commit as
// the catalogue code the build evaluates.
func RefreshAnswerAt(repo, rev string) (string, string, error) {
	before, err := oddc.Refresh(
		filepath.Join(repo, filepath.FromSlash(AnswerDir)),
		rev,
	)
	return before, rev, err
}
