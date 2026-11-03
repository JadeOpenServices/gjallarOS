package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/winefix"
	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

// gjallarctl wine fix (Plane 277, wiki 931 step 5): puts one fix from the
// table (internal/winefix) into an app. Its files are downloaded and
// checked on the host first; then winetricks runs in the app's own
// sandbox without any network, with the checked files laid out where it
// looks for them. A snapshot comes first, so undo takes the fix out again.

var (
	loadFixes = winefix.Load
	// launchTool runs a fix's step in the app's sandbox (launch.go).
	launchTool = launchOffline
	fixClient  = &http.Client{Transport: fixTransport()}
)

func fixTransport() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.ResponseHeaderTimeout = time.Minute
	return t
}

// seedDir is where the checked files lie during a fix, in the app's C:.
// winetricks reaches into its cache through the drive, so it has to be in
// the prefix.
const seedDir = "drive_c/gjallar-fix-files"

const dllOverrides = `HKCU\Software\Wine\DllOverrides`

func fixCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) != 2 || strings.HasPrefix(args[0], "-") || strings.HasPrefix(args[1], "-") {
		return errUsage
	}
	tab, err := loadFixes()
	if err != nil {
		return err
	}
	fix, refused := tab.Find(strings.ToLower(args[1]))
	switch {
	case refused != nil:
		return fmt.Errorf("%s is not offered: %s", refused.ID, refused.Reason)
	case fix == nil:
		return fmt.Errorf("there is no fix %q; gjallarctl wine fixes lists them", args[1])
	}
	id, a, paths, lock, err := doctorApp(args[:1])
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := winesandbox.RealDir(paths.Prefix); err != nil {
		return err
	}
	if slices.Contains(a.Fixes, fix.ID) {
		fmt.Fprintf(stdout, "App %s already has %s.\n", id, fix.ID)
		return nil
	}
	for _, have := range a.Fixes {
		other, _ := tab.Find(have)
		if slices.Contains(fix.Conflicts, have) || other != nil && slices.Contains(other.Conflicts, fix.ID) {
			return fmt.Errorf("%s does not go together with %s, which app %s already has", fix.ID, have, id)
		}
	}
	if len(a.Fixes) >= winesandbox.MaxFixes {
		return fmt.Errorf("app %s already has %d fixes", id, len(a.Fixes))
	}

	fmt.Fprintf(stdout, "Putting %s into app %s: %s\n", fix.ID, id, fix.Why)
	if fix.Kind == winefix.KindOffer {
		fmt.Fprintln(stdout, "It replaces some of Wine's own libraries; use it only for an app that asks for them.")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	_, home, err := currentUser()
	if err != nil {
		return err
	}
	// Downloads come first: if one fails, nothing has changed.
	local, err := fetchFixes(ctx, home, []*winefix.Fix{fix}, stdout)
	if err != nil {
		return err
	}
	if _, err := winesandbox.TakeSnapshot(paths, id, a, copyFill(paths, stderr)); err != nil {
		return fmt.Errorf("keep the app's state: %w", err)
	}
	undo := fmt.Sprintf("gjallarctl wine undo %s puts the state from before back", id)
	if err := runFixes(id, paths, []*winefix.Fix{fix}, local, false, stderr); err != nil {
		return fmt.Errorf("%w; %s", err, undo)
	}
	a.Fixes = append(a.Fixes, fix.ID)
	if err := saveApp(paths, id, a, stderr); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "App %s has %s now. %s.\n", id, fix.ID, undo)
	return nil
}

// copyFill fills a snapshot with a copy of the prefix, sharing blocks where
// the file system can.
func copyFill(paths winesandbox.Paths, stderr io.Writer) func(dst string) error {
	return func(dst string) error {
		cp := exec.Command(cpPath, "-a", "--reflink=auto", "--", paths.Prefix, dst)
		cp.Stderr = stderr
		return cp.Run()
	}
}

// downloadDir is where checked downloads are kept, by sha256.
func downloadDir(home string) (string, error) {
	cache := os.Getenv("XDG_CACHE_HOME")
	if !filepath.IsAbs(cache) {
		cache = filepath.Join(home, ".cache")
	}
	dir := filepath.Join(cache, "gjallar-wine", "downloads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := winesandbox.RealDir(dir); err != nil {
		return "", err
	}
	return dir, os.Chmod(dir, 0o700)
}

// fetchFixes downloads and checks every file of fixes, and gives where
// each one lies.
func fetchFixes(ctx context.Context, home string, fixes []*winefix.Fix, stdout io.Writer) (map[winefix.File]string, error) {
	local := map[winefix.File]string{}
	var dir string
	for _, fix := range fixes {
		for _, f := range fix.Files {
			if _, ok := local[f]; ok {
				continue
			}
			if dir == "" {
				var err error
				if dir, err = downloadDir(home); err != nil {
					return nil, err
				}
			}
			fmt.Fprintf(stdout, "Getting %s (%s), checked against its sha256.\n", f.Name(), megabytes(f.Size))
			p, err := winefix.Fetch(ctx, fixClient, dir, f)
			if err != nil {
				return nil, err
			}
			local[f] = p
		}
	}
	return local, nil
}

// runFixes puts fixes into app id's prefix, each step offline in the app's
// sandbox. force runs winetricks steps it has run before again, for
// repair.
func runFixes(id string, paths winesandbox.Paths, fixes []*winefix.Fix, local map[winefix.File]string, force bool, stderr io.Writer) error {
	done, err := seedFiles(paths.Prefix, fixes, local)
	if err != nil {
		return fmt.Errorf("lay out the fix files: %w", err)
	}
	defer done()
	tool := &winesandbox.Tool{
		Path: winetricksPath,
		Env: []string{
			"WINE=" + winePath,
			"WINE_BIN=" + wineBinPath,
			"WINESERVER=" + filepath.Join(filepath.Dir(winePath), "wineserver"),
			"W_CACHE=" + filepath.Join(paths.Prefix, seedDir),
			"WINETRICKS_LATEST_VERSION_CHECK=disabled",
		},
	}
	for _, fix := range fixes {
		for _, act := range fix.Actions {
			var t *winesandbox.Tool
			var args []string
			switch act.Do {
			case winefix.DoVerb:
				t, args = tool, []string{"-q", act.Verb}
				if force {
					args = []string{"-q", "--force", act.Verb}
				}
			case winefix.DoDLLOverride:
				args = []string{"reg", "add", dllOverrides, "/v", act.DLL, "/d", act.Mode, "/f"}
			default:
				return fmt.Errorf("%s has a step this version does not know: %s", fix.ID, act.Do)
			}
			code, err := launchTool(id, t, args, stderr)
			if err != nil {
				return fmt.Errorf("%s: %w", fix.ID, err)
			}
			if code != 0 {
				return fmt.Errorf("%s ended with code %d", fix.ID, code)
			}
		}
	}
	return nil
}

// seedFiles copies the checked files of fixes to where winetricks looks
// for them in its cache: seedDir/VERB/NAME. It works through an os.Root on
// the prefix, so a link in the prefix cannot send a copy elsewhere. The
// returned func takes the files away again.
func seedFiles(prefix string, fixes []*winefix.Fix, local map[winefix.File]string) (func(), error) {
	root, err := os.OpenRoot(prefix)
	if err != nil {
		return nil, err
	}
	done := func() {
		root.RemoveAll(seedDir)
		root.Close()
	}
	if err := root.RemoveAll(seedDir); err != nil {
		root.Close()
		return nil, err
	}
	if err := root.Mkdir(seedDir, 0o700); err != nil {
		root.Close()
		return nil, err
	}
	for _, fix := range fixes {
		for _, f := range fix.Files {
			verbDir := filepath.Join(seedDir, f.Verb)
			if err := root.Mkdir(verbDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				done()
				return nil, err
			}
			if err := seedFile(root, filepath.Join(verbDir, f.Name()), local[f]); err != nil {
				done()
				return nil, err
			}
		}
	}
	return done, nil
}

func seedFile(root *os.Root, name, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		// Two fixes share the file.
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// recordedFixes gets an app's recorded fixes ready to be put in again
// after a repair: their files are downloaded and checked. A fix this
// version no longer has stays in the record, and the user hears of it.
func recordedFixes(id string, a winesandbox.App, stdout io.Writer) ([]*winefix.Fix, map[winefix.File]string, error) {
	if len(a.Fixes) == 0 {
		return nil, nil, nil
	}
	tab, err := loadFixes()
	if err != nil {
		return nil, nil, err
	}
	var fixes []*winefix.Fix
	for _, name := range a.Fixes {
		fix, _ := tab.Find(name)
		if fix == nil {
			fmt.Fprintf(stdout, "This version has no fix %s any more; app %s keeps what it put in.\n", name, id)
			continue
		}
		fixes = append(fixes, fix)
	}
	if len(fixes) == 0 {
		return nil, nil, nil
	}
	_, home, err := currentUser()
	if err != nil {
		return nil, nil, err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	local, err := fetchFixes(ctx, home, fixes, stdout)
	if err != nil {
		return nil, nil, err
	}
	return fixes, local, nil
}
