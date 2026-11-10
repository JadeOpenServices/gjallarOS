package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/JadeOpenServices/gjallarOS/internal/tty"
	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

// import makes a Wine prefix set up without gjallar-wine, such as one the
// old unsandboxed launcher made, into an app. The prefix is copied, so the
// old one stays as it was until the user deletes it. The copy gets the
// same treatment as a new app's prefix, and links that lead out of it are
// taken away.

func importCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) != 2 {
		return errUsage
	}
	name := args[0]
	if !winesandbox.ValidName(name) {
		return fmt.Errorf("%q cannot be a folder name: no slashes, not hidden, at most %d characters", name, winesandbox.MaxNameLen)
	}
	id := winesandbox.IDFromName(name)
	userName, home, err := currentUser()
	if err != nil {
		return err
	}
	source, err := checkPrefix(home, args[1])
	if err != nil {
		return err
	}
	lock, err := lockApp(id)
	if err != nil {
		return err
	}
	defer lock.Close()

	paths := winesandbox.PathsFor(home, id, name)
	if _, err := os.Lstat(paths.Record); err == nil {
		return fmt.Errorf("app %s exists already; pick another name", id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := clearSwitches(id); err != nil {
		return err
	}
	if err := winesandbox.DropSnapshots(home, id); err != nil {
		return err
	}
	for _, dir := range []string{filepath.Dir(paths.Files), paths.Files} {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := winesandbox.RealDir(dir); err != nil {
			return err
		}
	}

	// A program still open in the old prefix keeps writing to it; Wine's
	// server ends when the last one closes and saves the registry then.
	fmt.Fprintf(stdout, "Waiting until no program runs in %s.\n", source)
	server := exec.Command(filepath.Join(filepath.Dir(winePath), "wineserver"), "-w")
	server.Env = append(os.Environ(), "WINEPREFIX="+source)
	server.Stderr = stderr
	if err := server.Run(); err != nil {
		return fmt.Errorf("wait for the programs in %s: %w", source, err)
	}

	fmt.Fprintf(stdout, "Copying %s into app %s.\n", source, id)
	if err := importPrefix(source, paths, id, userName, stderr); err != nil {
		return err
	}
	if err := winesandbox.WriteRecord(paths, winesandbox.App{Name: name}); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Imported app %s. Its files folder is %s, which the app sees as D:.\n", id, paths.Files)
	if err := setPlace("add", paths.Files, name, placeIcon); err != nil {
		fmt.Fprintf(stderr, "WARNING: %s is not in the file manager's Places: %v\n", paths.Files, err)
	}
	fmt.Fprintf(stdout, "Files the app kept in its user folders, such as Documents, were in your home's folders and stay there. Copy what it needs into %s.\n", paths.Files)
	fmt.Fprintf(stdout, "It has no network: gjallarctl wine set %s --internet on lets it reach the internet.\n", id)
	fmt.Fprintf(stdout, "%s stays as it was. Delete it once the app works.\n", source)

	a, paths, err := winesandbox.LoadApp(home, id)
	if err != nil {
		return err
	}
	found, err := winesandbox.Programs(paths, nil)
	if err != nil {
		return err
	}
	if len(found) == 1 {
		a.Exe = found[0]
		if err := saveApp(paths, id, a, stderr); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "App %s starts %s. It is in the app menu, and gjallarctl wine run %s starts it.\n", id, a.Exe, id)
		return nil
	}
	return chooseProgram(id, "", os.Stdin, tty.Is(os.Stdin), stdout, stderr)
}

// checkPrefix resolves the prefix to import and checks it is one: a folder
// of the user's with a C: drive and a registry, and no app's own prefix.
func checkPrefix(home, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	source, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a folder", path)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return "", fmt.Errorf("%s is not yours", path)
	}
	if err := winesandbox.RealDir(filepath.Join(source, "drive_c")); err != nil {
		return "", fmt.Errorf("%s is not a Wine prefix: %w", path, err)
	}
	if reg, err := os.Lstat(filepath.Join(source, "system.reg")); err != nil || !reg.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a Wine prefix: it has no system.reg", path)
	}
	data := filepath.Dir(winesandbox.BasePrefix(home))
	if rel, err := filepath.Rel(data, source); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%s is the prefix of a Wine app already", path)
	}
	return source, nil
}

// importPrefix copies source for app id and makes the copy its own. It is
// built under a hidden name and renamed when complete.
func importPrefix(source string, paths winesandbox.Paths, id, userName string, stderr io.Writer) error {
	prefixes := filepath.Dir(paths.Prefix)
	if err := os.MkdirAll(prefixes, 0o700); err != nil {
		return err
	}
	if err := winesandbox.RealDir(prefixes); err != nil {
		return err
	}
	if _, err := os.Lstat(paths.Prefix); err == nil {
		return fmt.Errorf("a prefix for %s is left over; gjallarctl wine remove %s clears it", id, id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := filepath.Join(prefixes, "."+id+".new")
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	fail := func(what string, err error) error {
		os.RemoveAll(tmp)
		return fmt.Errorf("%s: %w", what, err)
	}
	cp := exec.Command(cpPath, "-a", "--reflink=auto", "--", source, tmp)
	cp.Stderr = stderr
	if err := cp.Run(); err != nil {
		return fail("copy the prefix", err)
	}
	if err := os.Chmod(tmp, 0o700); err != nil {
		return fail("set up the app's prefix", err)
	}
	if err := dropOutwardLinks(tmp); err != nil {
		return fail("set up the app's prefix", err)
	}
	if err := winesandbox.FixPrefix(tmp, paths.Files, userName); err != nil {
		return fail("set up the app's prefix", err)
	}
	if err := os.Rename(tmp, paths.Prefix); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	return nil
}

// dropOutwardLinks removes the links in prefix that lead out of it: an
// absolute link, or a relative one that climbs above the prefix. In the
// sandbox they would lead nowhere or into the store; outside it, the
// tools that read the prefix should not follow them either. dosdevices is
// set anew by FixPrefix.
func dropOutwardLinks(prefix string) error {
	return filepath.WalkDir(prefix, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path == filepath.Join(prefix, "dosdevices") {
			return fs.SkipDir
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(target) {
			rel, err := filepath.Rel(prefix, filepath.Join(filepath.Dir(path), target))
			if err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
				return nil
			}
		}
		return os.Remove(path)
	})
}
