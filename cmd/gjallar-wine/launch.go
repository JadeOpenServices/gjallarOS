package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/JadeOpenServices/gjallarOS/internal/nettrust"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

// trustPath is root's verdict on the current network, as in
// gjallar-settingsd and gjallarctl vpn.
const trustPath = "/run/gjallar/vpn-trust.json"

// callTimeout bounds a read of the switches; it never asks a password.
const callTimeout = 10 * time.Second

var errSettingsMissing = errors.New("the settings service is not on this system")

// Start order, so the app is never online without its rules:
//
//  1. a holder process makes the app's user and network namespaces;
//  2. the rules go into its network namespace;
//  3. pasta connects that namespace to the device's network;
//  4. bwrap starts the app inside both.
//
// While the app runs, the rules are built again whenever the device's
// network, root's home verdict or the app's switches change, and when the
// verdict runs out. If they cannot be, the app is stopped.

func runApp(id string, args []string, stderr io.Writer) int {
	lock, err := lockApp(id)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	defer lock.Close()
	code, err := launch(id, nil, args, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	return code
}

// launch starts program with args in app id's sandbox, or the app's own
// program when program is nil. The caller holds the app's lock.
func launch(id string, program, args []string, stderr io.Writer) (int, error) {
	u, err := user.Current()
	if err != nil {
		return 0, err
	}
	uid, gid := os.Getuid(), os.Getgid()
	home, err := filepath.EvalSymlinks(u.HomeDir)
	if err != nil {
		return 0, err
	}
	a, paths, err := winesandbox.LoadApp(home, id)
	if err != nil {
		return 0, err
	}
	if program == nil {
		if err := a.Ready(id); err != nil {
			return 0, err
		}
		program = []string{a.Exe}
	}
	self, err := os.Executable()
	if err != nil {
		return 0, err
	}
	// The sandbox sees only the store, so the lock step must come from it.
	if !strings.HasPrefix(self, "/nix/store/") {
		return 0, fmt.Errorf("%s is not in the Nix store; start the installed gjallar-wine", self)
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(runtimeDir) {
		return 0, errors.New("XDG_RUNTIME_DIR is not set; start the app from your desktop session")
	}
	display := os.Getenv("WAYLAND_DISPLAY")
	if display == "" {
		return 0, errors.New("WAYLAND_DISPLAY is not set; start the app from your desktop session")
	}
	if !filepath.IsAbs(display) {
		display = filepath.Join(runtimeDir, display)
	}

	instance := id + "-" + strconv.Itoa(os.Getpid())
	runDir, err := makeRunDir(runtimeDir, instance, uid)
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(runDir)
	resolvConf := filepath.Join(runDir, "resolv.conf")
	if err := os.WriteFile(resolvConf, []byte("nameserver "+winesandbox.DNSAddr+"\n"), 0o644); err != nil {
		return 0, err
	}
	wayland := filepath.Join(runDir, "wayland.sock")
	sc, err := winesandbox.NewSecurityContext(display, wayland, "org.gjallaros.wine."+id, instance)
	if err != nil {
		return 0, err
	}
	defer sc.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sw := openSwitches(ctx, id, stderr)
	defer sw.Close()

	holder := exec.Command(self, "hold")
	holder.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: uid, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: gid, Size: 1}},
		GidMappingsEnableSetgroups: false,
		Pdeathsig:                  syscall.SIGKILL,
	}
	holdIn, err := holder.StdinPipe()
	if err != nil {
		return 0, err
	}
	if err := holder.Start(); err != nil {
		return 0, fmt.Errorf("make the app's namespaces: %w", err)
	}
	// The holder ends when its stdin closes; pasta ends with the namespace.
	defer func() {
		holdIn.Close()
		holder.Wait()
	}()
	ns := nsenter(holder.Process.Pid)

	expiry, err := refill(ctx, ns, sw.flags)
	if err != nil {
		return 0, err
	}
	pasta := exec.Command(pastaPath, "-q",
		"-t", "none", "-u", "none", "-T", "none", "-U", "none",
		"--map-host-loopback", "none",
		"--dns-forward", winesandbox.DNSAddr,
		"--config-net", strconv.Itoa(holder.Process.Pid))
	// pasta goes to the background once the namespace is connected. Its
	// stderr is a file, not a pipe, so its exit is all Run waits for.
	pasta.Stderr = os.Stderr
	if err := pasta.Run(); err != nil {
		return 0, fmt.Errorf("connect the app's network: %w", err)
	}

	// Without the service the switches stay off, and the rules then do
	// not depend on the network.
	var changed <-chan struct{}
	var watchErrs <-chan error
	if sw.present {
		if changed, watchErrs, err = winesandbox.Watch(ctx, trustPath); err != nil {
			return 0, fmt.Errorf("watch the network: %w", err)
		}
	}

	prog := exec.CommandContext(ctx, nsenterPath, append(ns, winesandbox.Sandbox{
		Bwrap:      bwrapPath,
		Self:       self,
		Wine:       winePath,
		UID:        uid,
		GID:        gid,
		User:       u.Username,
		Home:       home,
		RuntimeDir: runtimeDir,
		Wayland:    wayland,
		ResolvConf: resolvConf,
		Prefix:     paths.Prefix,
		Files:      paths.Files,
		ID:         id,
	}.Args(append(slices.Clone(program), args...))...)...)
	prog.Stdout, prog.Stderr = os.Stdout, os.Stderr
	prog.Cancel = func() error { return prog.Process.Signal(syscall.SIGTERM) }
	prog.WaitDelay = 10 * time.Second
	if err := prog.Start(); err != nil {
		return 0, fmt.Errorf("start the app: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- prog.Wait() }()

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	if !expiry.IsZero() {
		timer.Reset(time.Until(expiry))
	}
	defer timer.Stop()
	var failure error
	fail := func(err error) {
		if failure == nil {
			failure = err
			fmt.Fprintf(stderr, "ERROR: %v; stopping the app\n", err)
			cancel()
		}
	}
	again := func() {
		if failure != nil {
			return
		}
		next, err := refill(ctx, ns, sw.flags)
		if err != nil {
			fail(err)
			return
		}
		timer.Stop()
		if !next.IsZero() {
			timer.Reset(time.Until(next))
		}
	}
	for {
		select {
		case err := <-done:
			if failure != nil {
				return 1, nil
			}
			return exitCode(err)
		case <-changed:
			again()
		case err := <-watchErrs:
			fail(fmt.Errorf("the network watch stopped: %w", err))
		case <-sw.signals:
			sw.reload(ctx)
			again()
		case <-timer.C:
			again()
		}
	}
}

// makeRunDir makes the private folder for one start of the app, below a
// gjallar-wine folder in the runtime dir that only uid can open.
func makeRunDir(runtimeDir, instance string, uid int) (string, error) {
	parent, err := runParent(runtimeDir, uid)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(parent, instance)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// runParent makes the gjallar-wine folder in the runtime dir, which only
// uid can open.
func runParent(runtimeDir string, uid int) (string, error) {
	parent := filepath.Join(runtimeDir, "gjallar-wine")
	if err := os.Mkdir(parent, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return "", err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(st.Uid) != uid || info.Mode().Perm() != 0o700 {
		return "", fmt.Errorf("%s must be a folder only you can open", parent)
	}
	return parent, nil
}

// nsenter is the command prefix that runs a tool in the holder's
// namespaces as the same user.
func nsenter(pid int) []string {
	return []string{"-t", strconv.Itoa(pid), "-U", "-n", "--preserve-credentials", "--"}
}

// refill builds the rules for the network as it is now and loads them in
// one step. It returns when root's home verdict runs out, or zero when
// there is none.
func refill(ctx context.Context, ns []string, flags winesandbox.Flags) (time.Time, error) {
	n, err := winesandbox.ReadNet(ctx, ipPath)
	if err != nil {
		return time.Time{}, fmt.Errorf("read the network: %w", err)
	}
	verdict, expiry, ok := nettrust.ReadVerdictExpiry(trustPath, time.Now())
	var homeLAN []netip.Prefix
	if ok && verdict.Trust == nettrust.TrustHome {
		homeLAN = winesandbox.HomeLAN(verdict.Bypass, n.LAN)
	}
	nft := exec.CommandContext(ctx, nsenterPath, append(ns, nftPath, "-f", "-")...)
	nft.Stdin = strings.NewReader(winesandbox.Ruleset(n, flags, homeLAN))
	if out, err := nft.CombinedOutput(); err != nil {
		return time.Time{}, fmt.Errorf("load the network rules: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if !ok {
		return time.Time{}, nil
	}
	return expiry, nil
}

func exitCode(err error) (int, error) {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code >= 0 {
			return code, nil
		}
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	return 0, nil
}

// switches are the app's network switches in the settings service. Any
// failure to read them leaves both off.
type switches struct {
	id      string
	stderr  io.Writer
	conn    *dbus.Conn
	present bool
	flags   winesandbox.Flags
	// signals carries WineChanged. Another program could send a look-alike
	// signal; it only makes the launcher read the switches again.
	signals chan *dbus.Signal
}

func openSwitches(ctx context.Context, id string, stderr io.Writer) *switches {
	sw := &switches{id: id, stderr: stderr}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		fmt.Fprintf(stderr, "could not reach the system bus (%v), so the app stays offline\n", err)
		return sw
	}
	sw.conn = conn
	// Listen first, so a change during the first read is not missed.
	if err := conn.AddMatchSignal(
		dbus.WithMatchSender(settings.BusName),
		dbus.WithMatchObjectPath(dbus.ObjectPath(settings.ObjectPath)),
		dbus.WithMatchInterface(settings.Interface),
		dbus.WithMatchMember("WineChanged"),
	); err != nil {
		fmt.Fprintf(stderr, "could not listen to the settings service (%v), so the app stays offline\n", err)
		return sw
	}
	raw := make(chan *dbus.Signal, 8)
	conn.Signal(raw)
	sw.signals = make(chan *dbus.Signal, 1)
	go func() {
		for sig := range raw {
			if sig.Path != dbus.ObjectPath(settings.ObjectPath) || sig.Name != settings.Interface+".WineChanged" {
				continue
			}
			select {
			case sw.signals <- sig:
			default:
			}
		}
	}()
	sw.present = true
	sw.reload(ctx)
	if !sw.present {
		conn.RemoveSignal(raw)
		close(raw)
		sw.signals = nil
	}
	return sw
}

// reload reads the switches again.
func (sw *switches) reload(ctx context.Context) {
	flags, err := sw.read(ctx)
	switch {
	case errors.Is(err, errSettingsMissing):
		fmt.Fprintln(sw.stderr, "the settings service is not on this system, so the app stays offline")
		sw.present = false
	case err != nil:
		fmt.Fprintf(sw.stderr, "could not read the app's network switches (%v), so the app is offline\n", err)
	}
	sw.flags = flags
}

func (sw *switches) read(ctx context.Context) (winesandbox.Flags, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	call := sw.conn.Object(settings.BusName, dbus.ObjectPath(settings.ObjectPath)).
		CallWithContext(ctx, settings.Interface+".Wine", 0)
	if call.Err != nil {
		return winesandbox.Flags{}, busError(call.Err)
	}
	var answer string
	if err := call.Store(&answer); err != nil {
		return winesandbox.Flags{}, err
	}
	var programs map[string]winesandbox.Flags
	if err := json.Unmarshal([]byte(answer), &programs); err != nil {
		return winesandbox.Flags{}, fmt.Errorf("read the service's answer: %w", err)
	}
	return programs[sw.id], nil
}

// busError gives errSettingsMissing when the settings service is not
// there, and err otherwise.
func busError(err error) error {
	var busErr dbus.Error
	if errors.As(err, &busErr) && busErr.Name == "org.freedesktop.DBus.Error.ServiceUnknown" {
		return errSettingsMissing
	}
	return err
}

func (sw *switches) Close() {
	if sw.conn != nil {
		sw.conn.Close()
	}
}

// hold keeps the app's namespaces open until the launcher closes stdin.
func hold() int {
	io.Copy(io.Discard, os.Stdin)
	return 0
}

// lock is the last step inside the sandbox: it turns Landlock on and
// starts the program.
func lock(args []string, stderr io.Writer) int {
	sym, refer, argv, err := parseLock(args)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 2
	}
	err = winesandbox.ExecLocked(sym, refer, argv, os.Environ())
	fmt.Fprintf(stderr, "ERROR: %v\n", err)
	return 1
}

// stay runs Wine as the sandbox's first program. The sandbox ends when
// that program does, so stay waits until wineserver is gone too: until
// then other programs of the app (an installer's children, a launcher's
// game) still run, and wineserver has not yet saved the registry. A
// signal goes on to Wine, and then stops the app's other programs too.
func stay(args []string, signals <-chan os.Signal, stderr io.Writer) int {
	if len(args) == 0 || !filepath.IsAbs(args[0]) {
		fmt.Fprintln(stderr, "ERROR: stay needs Wine, as a full path")
		return 2
	}
	wine := exec.Command(args[0], args[1:]...)
	wine.Stdin, wine.Stdout, wine.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := wine.Start(); err != nil {
		fmt.Fprintf(stderr, "ERROR: start Wine: %v\n", err)
		return 1
	}
	done := make(chan error, 1)
	go func() { done <- wine.Wait() }()
	stopped := false
	var waitErr error
wait:
	for {
		select {
		case sig := <-signals:
			stopped = true
			wine.Process.Signal(sig)
		case waitErr = <-done:
			break wait
		}
	}
	code, err := exitCode(waitErr)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		code = 1
	}

	server := filepath.Join(filepath.Dir(args[0]), "wineserver")
	mode := "-w"
	if stopped {
		mode = "-k"
	}
	for {
		cmd := exec.Command(server, mode)
		cmd.Stdout, cmd.Stderr = stderr, stderr
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(stderr, "ERROR: start wineserver: %v\n", err)
			return code
		}
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
			return code
		case <-signals:
			// Waiting gave way to stopping.
			cmd.Process.Kill()
			<-done
			mode = "-k"
		}
	}
}

func parseLock(args []string) (sym, refer, argv []string, err error) {
	for len(args) > 0 {
		switch args[0] {
		case "--sym", "--refer":
			if len(args) < 2 || !filepath.IsAbs(args[1]) {
				return nil, nil, nil, fmt.Errorf("%s needs a full path", args[0])
			}
			if args[0] == "--sym" {
				sym = append(sym, args[1])
			} else {
				refer = append(refer, args[1])
			}
			args = args[2:]
		case "--":
			if len(args) < 2 || !filepath.IsAbs(args[1]) {
				return nil, nil, nil, errors.New("lock needs a program, as a full path, after --")
			}
			return sym, refer, args[1:], nil
		default:
			return nil, nil, nil, fmt.Errorf("lock: unknown option %q", args[0])
		}
	}
	return nil, nil, nil, errors.New("lock needs a program after --")
}
