package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/devgate"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/credential"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/oldroot"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/prepared"
)

const moveUsage = "usage: gjallarctl move prepare"

// rootPATH is the PATH move prepare runs its lookups with as root, so a
// findmnt or lsblk planted in the user's PATH is never run by root.
const rootPATH = "/run/wrappers/bin:/run/current-system/sw/bin"

// moveRecoveryTries is how many recovery keys move prepare takes before it
// stops. gjallar-devproof waits after each wrong one.
const moveRecoveryTries = 3

func runMove(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "prepare" {
		fmt.Fprintln(stderr, moveUsage)
		return 2
	}
	if os.Geteuid() != 0 {
		self, err := os.Executable()
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		// The fixed sudo, not the first one on the user's PATH. -k: ask now
		// and leave no timestamp behind.
		const sudo = "/run/wrappers/bin/sudo"
		err = syscall.Exec(sudo, []string{"sudo", "-k", self, "move", "prepare"}, os.Environ())
		fmt.Fprintf(stderr, "ERROR: sudo: %v\n", err)
		return 1
	}
	if err := os.Setenv("PATH", rootPATH); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	tty := &lazyTTY{}
	defer tty.close()
	return movePrepare(context.Background(), movePrepareDeps{
		rootDevice: diskcrypto.RootDevice,
		readArea:   oldroot.ReadHeaderArea,
		ask: func(ctx context.Context, kind string, secret []byte) (devgate.Reply, error) {
			return devgate.Ask(ctx, devgate.SocketPath, kind, secret)
		},
		secret: tty.secret,
		write:  func(r prepared.Record) error { return prepared.Write(prepared.Dir, r) },
		now:    time.Now,
	}, stdout, stderr)
}

// lazyTTY opens the terminal on the first question, so a disk without
// encryption needs no terminal at all.
type lazyTTY struct {
	f      *os.File
	reader *bufio.Reader
}

func (t *lazyTTY) secret(prompt string) (string, error) {
	if t.f == nil {
		f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err != nil {
			return "", fmt.Errorf("the recovery key and the startup PIN are typed at a terminal: %w", err)
		}
		t.f, t.reader = f, bufio.NewReader(f)
	}
	return credential.ReadSecret(t.f, t.reader, t.f, prompt)
}

func (t *lazyTTY) close() {
	if t.f != nil {
		t.f.Close()
	}
}

type movePrepareDeps struct {
	rootDevice func(context.Context) (string, error)
	readArea   func(path string) ([]byte, error)
	ask        func(ctx context.Context, kind string, secret []byte) (devgate.Reply, error)
	// secret asks one hidden question; "" means Enter on an empty line.
	secret func(prompt string) (string, error)
	write  func(prepared.Record) error
	now    func() time.Time
}

// movePrepare checks, on the system that is moving, what the USB stick
// cannot check itself: that the recovery key opens the disk and, when the
// disk has a startup PIN, that the PIN is right. The PIN needs this
// computer's security chip, which the stick does not use. The stick finds
// the record after the unlock and then skips its PIN notice.
func movePrepare(ctx context.Context, d movePrepareDeps, stdout, stderr io.Writer) int {
	dev, err := d.rootDevice(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: find the system disk: %v\n", err)
		return 1
	}
	if dev == "" {
		fmt.Fprintln(stdout, "The system disk is not encrypted, so the stick needs no recovery key or PIN for it. Nothing to prepare.")
		return 0
	}
	h, digest, err := moveHeader(d, dev)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "The stick asks for the recovery key of this disk. Type it here once, so you know it works before you start the move.")
	if code := moveCheckRecovery(ctx, d, stdout, stderr); code != 0 {
		return code
	}
	if h.PINToken {
		fmt.Fprintln(stdout, "This disk also has a startup PIN. The stick cannot check it, because the check needs this computer's security chip. Type it here, so the stick knows it was checked.")
		if code := moveCheckPIN(ctx, d, stdout, stderr); code != 0 {
			return code
		}
	}

	// The checks above took a while; the record must describe the header
	// they ran against, not one changed in between.
	_, again, err := moveHeader(d, dev)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if again != digest {
		fmt.Fprintln(stderr, "ERROR: the disk encryption changed while this ran (a key or PIN was changed). Run gjallarctl move prepare again.")
		return 1
	}
	rec := prepared.Record{Version: prepared.Version, Time: d.now().UTC(), PIN: h.PINToken, Header: digest}
	if err := d.write(rec); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Ready. Shut down this computer and start it from the stick.")
	return 0
}

// moveHeader reads and checks the encryption header the way the stick
// does. The memory cap is the 4 GiB limit, not this computer's free
// memory: the stick checks against its own memory when it runs.
func moveHeader(d movePrepareDeps, dev string) (oldroot.Header, string, error) {
	area, err := d.readArea(dev)
	if err != nil {
		return oldroot.Header{}, "", err
	}
	cut, h, err := oldroot.CheckArea(area, oldroot.MaxKDFMemoryKiB)
	if err != nil {
		return h, "", fmt.Errorf("the stick would refuse this disk: %w", err)
	}
	return h, oldroot.AreaDigest(cut), nil
}

func moveCheckRecovery(ctx context.Context, d movePrepareDeps, stdout, stderr io.Writer) int {
	for try := 1; try <= moveRecoveryTries; try++ {
		typed, err := d.secret("Recovery key (Enter to stop): ")
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		if typed == "" {
			fmt.Fprintln(stdout, "Stopped. Nothing was prepared.")
			return 1
		}
		key, err := normalRecoveryKey(typed)
		if err != nil {
			fmt.Fprintln(stderr, err)
			continue
		}
		secret := []byte(key)
		_, err = d.ask(ctx, devgate.ProofRecovery, secret)
		clear(secret)
		switch {
		case err == nil:
			fmt.Fprintln(stdout, "The recovery key opens this disk.")
			return 0
		case errors.Is(err, devgate.ErrWrong):
			fmt.Fprintln(stderr, "That recovery key does not open this disk.")
		default:
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
	}
	fmt.Fprintln(stderr, "No recovery key opened this disk. Find the key shown when this computer was set up, then run gjallarctl move prepare again. Nothing was prepared.")
	return 1
}

func moveCheckPIN(ctx context.Context, d movePrepareDeps, stdout, stderr io.Writer) int {
	for {
		typed, err := d.secret("Startup PIN (Enter to stop): ")
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		if typed == "" {
			fmt.Fprintln(stdout, "Stopped. Nothing was prepared; the stick will remind you to check the PIN.")
			return 1
		}
		secret := []byte(typed)
		reply, err := d.ask(ctx, devgate.ProofPIN, secret)
		clear(secret)
		switch {
		case err == nil:
			fmt.Fprintln(stdout, "The startup PIN is right.")
			return 0
		case !reply.Retry.IsZero():
			fmt.Fprintf(stderr, "No PIN tries are left until %s. Run gjallarctl move prepare again after that; if you move before, the stick will remind you to check the PIN. Nothing was prepared.\n", reply.Retry.Local().Format(time.DateTime))
			return 1
		case errors.Is(err, devgate.ErrWrong) && reply.PINLeft > 0:
			fmt.Fprintf(stderr, "Wrong startup PIN; %d tries left.\n", reply.PINLeft)
		default:
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
	}
}
