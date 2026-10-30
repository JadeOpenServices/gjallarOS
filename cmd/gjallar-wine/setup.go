package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/godbus/dbus/v5"

	"github.com/JadeOpenServices/gjallarOS/internal/settings"
	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

// add, install, program and remove set an app up and take it away. They
// run outside the sandbox, but an installer or program only ever runs
// through launch, inside it. Each holds the app's lock, so one prefix has
// one Wine at a time.

// Set by the package: Wine's one-time setup and the copy tool.
var (
	initPath = ""
	cpPath   = "cp"
)

// Replaced by the tests.
var (
	currentUser = func() (name, home string, err error) {
		u, err := user.Current()
		if err != nil {
			return "", "", err
		}
		home, err = filepath.EvalSymlinks(u.HomeDir)
		return u.Username, home, err
	}
	launchApp = launch
	setWine   = setWineOnBus
)

// lockApp takes app id's lock. It is let go when the file closes, also
// when the process dies.
func lockApp(id string) (*os.File, error) {
	if !winesandbox.ValidID(id) {
		return nil, fmt.Errorf("%q is not an app id; gjallarctl wine list shows them", id)
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(runtimeDir) {
		return nil, errors.New("XDG_RUNTIME_DIR is not set; start the app from your desktop session")
	}
	parent, err := runParent(runtimeDir, os.Getuid())
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(parent, id+".lock"), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("app %s is open or being set up; close it first", id)
		}
		return nil, err
	}
	return f, nil
}

// setWineOnBus sets app id's switches in the settings service. Turning
// them off never asks a password.
func setWineOnBus(id string, internet, home bool) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	call := conn.Object(settings.BusName, dbus.ObjectPath(settings.ObjectPath)).
		CallWithContext(ctx, settings.Interface+".SetWine", 0, id, internet, home)
	return busError(call.Err)
}

// clearSwitches turns app id's switches off. Without the settings service
// there are none.
func clearSwitches(id string) error {
	if err := setWine(id, false, false); err != nil && !errors.Is(err, errSettingsMissing) {
		return fmt.Errorf("turn off the network switches of %s: %w", id, err)
	}
	return nil
}

func addApp(args []string, stdout, stderr io.Writer) error {
	if len(args) < 1 || len(args) > 2 {
		return errUsage
	}
	name := args[0]
	if !winesandbox.ValidName(name) {
		return fmt.Errorf("%q cannot be a folder name: no slashes, not hidden, at most %d characters", name, winesandbox.MaxNameLen)
	}
	id := winesandbox.IDFromName(name)
	var installer string
	if len(args) == 2 {
		var err error
		// Checked now, before the long setup.
		if installer, err = checkInstaller(args[1]); err != nil {
			return err
		}
	}
	userName, home, err := currentUser()
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
		return fmt.Errorf("app %s exists already; gjallar-wine install %s INSTALLER adds to it", id, id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Switches left by an app of the same id that is gone must not carry
	// over to this one.
	if err := clearSwitches(id); err != nil {
		return err
	}
	// A files folder kept from an earlier app of this name is used again.
	for _, dir := range []string{filepath.Dir(paths.Files), paths.Files} {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := winesandbox.RealDir(dir); err != nil {
			return err
		}
	}
	base, err := ensureBase(home, stderr)
	if err != nil {
		return err
	}
	if err := copyPrefix(base, paths, id, userName, stderr); err != nil {
		return err
	}
	if err := winesandbox.WriteRecord(paths, winesandbox.App{Name: name}); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Added app %s. Its files folder is %s, which the app sees as D:.\n", id, paths.Files)
	fmt.Fprintf(stdout, "It has no network: gjallarctl wine set %s --internet on lets it reach the internet.\n", id)
	if installer == "" {
		fmt.Fprintf(stdout, "Next: gjallar-wine install %s INSTALLER, or gjallar-wine program %s D:\\PROGRAM.exe for a program in the files folder.\n", id, id)
		return nil
	}
	return installApp(id, installer, os.Stdin, stdinIsTerminal(), stdout, stderr)
}

// setupLog is where Wine's one-time setup writes what it did.
func setupLog(home string) string {
	state := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(state) {
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "wine-win11", "setup.log")
}

// ensureBase makes the base prefix once, outside any sandbox, with Wine's
// setup step. Its downloads are pinned by checksum in winetricks and in
// the step. A failed setup leaves no marker and is tried again next time.
func ensureBase(home string, stderr io.Writer) (string, error) {
	base := winesandbox.BasePrefix(home)
	dir := filepath.Dir(base)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := winesandbox.RealDir(dir); err != nil {
		return "", err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "base.lock"), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); errors.Is(err, syscall.EWOULDBLOCK) {
		fmt.Fprintln(stderr, "Waiting for Wine's setup that another app started.")
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
		if err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	marker := filepath.Join(base, winesandbox.InitMarker)
	if _, err := os.Lstat(marker); err == nil {
		return base, nil
	}
	if initPath == "" {
		return "", errors.New("this gjallar-wine came without Wine's setup step; use the one the system installs")
	}

	env := append(os.Environ(), "WINEPREFIX="+base)
	setup := exec.Command(initPath)
	setup.Env = env
	setup.Stdout, setup.Stderr = os.Stdout, os.Stderr
	setupErr := setup.Run()
	// Wine's server outlives the setup and saves the registry on its way
	// out; the copy must not start before.
	server := exec.Command(filepath.Join(filepath.Dir(winePath), "wineserver"), "-w")
	server.Env = env
	server.Stderr = stderr
	serverErr := server.Run()
	if _, err := os.Lstat(marker); err != nil {
		why := "it did not finish"
		if setupErr != nil {
			why = setupErr.Error()
		}
		return "", fmt.Errorf("Wine's setup is not complete (%s); %s says why, and the next gjallar-wine add tries again", why, setupLog(home))
	}
	if serverErr != nil {
		return "", fmt.Errorf("wait for Wine's setup to save: %w", serverErr)
	}
	return base, nil
}

// copyPrefix copies the base prefix for app id and makes the copy its own.
// It is built under a hidden name and renamed when complete.
func copyPrefix(base string, paths winesandbox.Paths, id, userName string, stderr io.Writer) error {
	prefixes := filepath.Dir(paths.Prefix)
	if err := os.MkdirAll(prefixes, 0o700); err != nil {
		return err
	}
	if err := winesandbox.RealDir(prefixes); err != nil {
		return err
	}
	if _, err := os.Lstat(paths.Prefix); err == nil {
		return fmt.Errorf("a prefix for %s is left over; gjallar-wine remove %s clears it", id, id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := filepath.Join(prefixes, "."+id+".new")
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	cp := exec.Command(cpPath, "-a", "--reflink=auto", "--", base, tmp)
	cp.Stderr = stderr
	if err := cp.Run(); err != nil {
		os.RemoveAll(tmp)
		return fmt.Errorf("copy the base prefix: %w", err)
	}
	if err := winesandbox.FixPrefix(tmp, paths.Files, userName); err != nil {
		os.RemoveAll(tmp)
		return fmt.Errorf("set up the app's prefix: %w", err)
	}
	if err := os.Rename(tmp, paths.Prefix); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	return nil
}

// checkInstaller resolves an installer's path and checks it is a plain
// .exe or .msi file.
func checkInstaller(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if ext := strings.ToLower(filepath.Ext(resolved)); !info.Mode().IsRegular() || ext != ".exe" && ext != ".msi" {
		return "", fmt.Errorf("%s is not an .exe or .msi file", path)
	}
	return resolved, nil
}

func installCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) != 2 {
		return errUsage
	}
	installer, err := checkInstaller(args[1])
	if err != nil {
		return err
	}
	lock, err := lockApp(args[0])
	if err != nil {
		return err
	}
	defer lock.Close()
	return installApp(args[0], installer, os.Stdin, stdinIsTerminal(), stdout, stderr)
}

// installApp runs installer in app id's sandbox and picks the program it
// installed. The caller holds the app's lock.
func installApp(id, installer string, in io.Reader, interactive bool, stdout, stderr io.Writer) error {
	_, home, err := currentUser()
	if err != nil {
		return err
	}
	a, paths, err := winesandbox.LoadApp(home, id)
	if err != nil {
		return err
	}
	// The sandbox sees only the app's folders: an installer elsewhere is
	// copied into C: for the run.
	win, err := winesandbox.WinPath(paths, installer)
	if back, berr := winesandbox.HostPath(paths, win); err != nil || berr != nil || back != installer {
		driveC := filepath.Join(paths.Prefix, "drive_c")
		setup := filepath.Join(driveC, "gjallar-setup")
		if err := winesandbox.RealDir(driveC); err != nil {
			return err
		}
		if err := os.RemoveAll(setup); err != nil {
			return err
		}
		if err := os.Mkdir(setup, 0o700); err != nil {
			return err
		}
		defer func() {
			if winesandbox.RealDir(driveC) == nil {
				os.RemoveAll(setup)
			}
		}()
		name := filepath.Base(installer)
		if strings.ContainsAny(name, `\:`) {
			name = "setup" + strings.ToLower(filepath.Ext(name))
		}
		if err := copyFile(installer, filepath.Join(setup, name)); err != nil {
			return fmt.Errorf("copy the installer into the app: %w", err)
		}
		win = `C:\gjallar-setup\` + name
	}

	before, err := winesandbox.Shortcuts(paths.Prefix)
	if err != nil {
		return err
	}
	program := []string{win}
	if strings.EqualFold(filepath.Ext(win), ".msi") {
		program = []string{"msiexec", "/i", win}
	}
	fmt.Fprintf(stdout, "Running the installer in app %s.\n", id)
	code, err := launchApp(id, program, nil, stderr)
	if err != nil {
		return err
	}
	if code != 0 {
		// Some installers end with a code for "restart needed"; what they
		// installed still counts.
		fmt.Fprintf(stderr, "The installer ended with code %d.\n", code)
	}

	found, err := winesandbox.Programs(paths, before)
	if err != nil {
		return err
	}
	if a.Exe == "" && len(found) == 1 {
		a.Exe = found[0]
		if err := winesandbox.WriteRecord(paths, a); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "App %s starts %s. gjallar-wine run %s starts it.\n", id, a.Exe, id)
		return nil
	}
	return chooseProgram(id, "", in, interactive, stdout)
}

// copyFile copies a plain file to a new file dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func programCmd(args []string, stdout io.Writer) error {
	if len(args) < 1 || len(args) > 2 {
		return errUsage
	}
	lock, err := lockApp(args[0])
	if err != nil {
		return err
	}
	defer lock.Close()
	choice := ""
	if len(args) == 2 {
		choice = args[1]
	}
	return chooseProgram(args[0], choice, os.Stdin, stdinIsTerminal(), stdout)
}

// chooseProgram sets the program app id starts. choice is a number from
// the list of programs its shortcuts start, a path on C: or D:, or a host
// path in the app's folders. Without a choice it lists the programs, and
// asks for one when interactive. The caller holds the app's lock.
func chooseProgram(id, choice string, in io.Reader, interactive bool, stdout io.Writer) error {
	_, home, err := currentUser()
	if err != nil {
		return err
	}
	a, paths, err := winesandbox.LoadApp(home, id)
	if err != nil {
		return err
	}
	found, err := winesandbox.Programs(paths, nil)
	if err != nil {
		return err
	}
	if choice == "" {
		if len(found) == 0 {
			fmt.Fprintf(stdout, "No shortcut in app %s starts a program. gjallar-wine program %s D:\\PROGRAM.exe picks one by its path.\n", id, id)
			return nil
		}
		fmt.Fprintf(stdout, "Programs in app %s:\n", id)
		for i, p := range found {
			mark := " "
			if strings.EqualFold(p, a.Exe) {
				mark = "*"
			}
			fmt.Fprintf(stdout, "%s %d  %s\n", mark, i+1, p)
		}
		if !interactive {
			fmt.Fprintf(stdout, "gjallar-wine program %s NUMBER picks one.\n", id)
			return nil
		}
		fmt.Fprint(stdout, "Number of the program to start, or Enter to keep the current one: ")
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if choice = strings.TrimSpace(line); choice == "" {
			return nil
		}
	}

	var win string
	if n, err := strconv.Atoi(choice); err == nil {
		if n < 1 || n > len(found) {
			return fmt.Errorf("%d is not on the list; gjallar-wine program %s shows it", n, id)
		}
		win = found[n-1]
	} else if filepath.IsAbs(choice) {
		if win, err = winesandbox.WinPath(paths, choice); err != nil {
			return err
		}
	} else {
		win = choice
	}
	host, err := winesandbox.HostPath(paths, win)
	if err != nil {
		return err
	}
	// The path must stay in the app's folders, also through links.
	if back, err := winesandbox.WinPath(paths, host); err != nil || !strings.EqualFold(back, win) {
		return fmt.Errorf("%s leads out of the app's folders", win)
	}
	if info, err := os.Stat(host); err != nil {
		return err
	} else if !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(host), ".exe") {
		return fmt.Errorf("%s is not an .exe file", win)
	}
	a.Exe = win
	if err := winesandbox.WriteRecord(paths, a); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "App %s starts %s.\n", id, win)
	return nil
}

func removeCmd(args []string, stdout io.Writer) error {
	files := false
	switch {
	case len(args) == 2 && args[1] == "--delete-files":
		files = true
	case len(args) != 1:
		return errUsage
	}
	id := args[0]
	_, home, err := currentUser()
	if err != nil {
		return err
	}
	lock, err := lockApp(id)
	if err != nil {
		return err
	}
	defer lock.Close()
	var kept string
	if !files {
		if _, paths, err := winesandbox.LoadApp(home, id); err == nil {
			kept = paths.Files
		}
	}
	if err := clearSwitches(id); err != nil {
		return err
	}
	if err := winesandbox.RemoveApp(home, id, files); err != nil {
		return err
	}
	switch {
	case files:
		fmt.Fprintf(stdout, "Removed app %s and its files folder.\n", id)
	case kept != "":
		fmt.Fprintf(stdout, "Removed app %s. Its files stay in %s.\n", id, kept)
	default:
		fmt.Fprintf(stdout, "Removed app %s.\n", id)
	}
	return nil
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
