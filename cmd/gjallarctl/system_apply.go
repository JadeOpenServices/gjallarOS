package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/JadeOpenServices/gjallarOS/internal/localchanges"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
)

// localFolder is the owner's developer-mode folder; tests replace it.
var localFolder = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "GjallarOS-local"), nil
}

// binaryExtensions are the folder's file types a diff does not show.
var binaryExtensions = []string{".png", ".jpg", ".jpeg"}

// systemApply asks root to take ~/GjallarOS-local as its copy of the
// developer-mode folder and rebuild (ROADMAP section 14, checkpoint 3b). The
// folder goes to the settings service as data, so root never reads the
// home folder. The change waits like a security setting; the password
// dialog shows the files and the new hash printed here.
func systemApply(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "Usage: gjallarctl system apply")
		return 2
	}
	bus, ok := openSettings(stderr)
	if !ok {
		return 1
	}
	defer bus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), settingsCallTimeout)
	defer cancel()

	key, err := settingsKey(ctx, bus, "developerMode")
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if string(key.Value) != "true" {
		fmt.Fprintln(stderr, "ERROR: developer mode is off; turn it on with: gjallarctl settings set developerMode=true")
		return 1
	}
	dir, err := localFolder()
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	wanted, err := localchanges.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	current, err := localchanges.ReadRoot(localChangesPath)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	// The same plan the service makes, so the list and hash below are the
	// ones the password dialog shows.
	plan, err := settings.PlanLocal(current, wanted)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if err := printLocalDiff(stdout, current, wanted, plan.Local.Files); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "New folder hash: %s\n", settings.ShortHash(plan.Local.NewHash))

	files := make(map[string][]byte, len(wanted.Files))
	for _, file := range wanted.Files {
		files[file.Path] = file.Data
	}
	answer, err := bus.Call(ctx, "RequestLocal", files)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	return settingsAsked(ctx, bus, answer, stdout, stderr)
}

// printLocalDiff shows every changed file of the folder as a unified diff
// against root's copy. Characters that do not print are shown as escapes,
// so a file cannot hide lines from the person reading the diff.
func printLocalDiff(stdout io.Writer, current, wanted localchanges.Tree, changes []settings.FileChange) error {
	tmp, err := os.MkdirTemp("", "gjallarctl-apply-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	data := func(tree localchanges.Tree, rel string) []byte {
		for _, file := range tree.Files {
			if file.Path == rel {
				return file.Data
			}
		}
		return nil
	}
	for i, change := range changes {
		how := "changed"
		switch {
		case change.Old == "":
			how = "added"
		case change.New == "":
			how = "removed"
		}
		fmt.Fprintf(stdout, "%s: %s\n", change.Path, how)
		ext := strings.ToLower(path.Ext(change.Path))
		if slices.Contains(binaryExtensions, ext) {
			fmt.Fprintln(stdout, "  (binary file, not shown)")
			continue
		}
		oldFile := filepath.Join(tmp, strconv.Itoa(i)+".old")
		newFile := filepath.Join(tmp, strconv.Itoa(i)+".new")
		if err := os.WriteFile(oldFile, data(current, change.Path), 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(newFile, data(wanted, change.Path), 0o600); err != nil {
			return err
		}
		out, err := exec.Command("diff", "-u",
			"--label", "root/"+change.Path, "--label", "new/"+change.Path,
			oldFile, newFile).Output()
		var exit *exec.ExitError
		if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
			return fmt.Errorf("diff %s: %w", change.Path, err)
		}
		for _, line := range strings.SplitAfter(string(out), "\n") {
			if line != "" {
				fmt.Fprintf(stdout, "%s\n", shownDiffLine(strings.TrimSuffix(line, "\n")))
			}
		}
	}
	return nil
}

// shownDiffLine escapes what does not print, except tabs.
func shownDiffLine(line string) string {
	var b strings.Builder
	for _, r := range line {
		if r == '\t' || unicode.IsPrint(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteString(strings.Trim(strconv.QuoteRuneToASCII(r), "'"))
	}
	return b.String()
}
