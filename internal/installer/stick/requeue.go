package stick

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/calmmenu"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/oldroot"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/secureboot"
)

// "Queue the key again" (Q234, Plane 370): when the MokManager visit after
// an install was missed, shim dropped the requests and the installed
// gjallarOS no longer starts with Secure Boot on. The stick reads the
// install's public key from its root and queues the same visit again.
//
// Where the key comes from (R48, Q241): the root partition. An encrypted
// root is opened read-only with the install password or the recovery key, so
// a key swapped in by someone without either cannot be read. An unencrypted root offers no
// such check; the step says so and the user compares the fingerprint. The
// signed boot files on the ESP are not used: anyone with the computer for a
// few minutes can replace them, and parsing them would run their bytes
// through a PE and PKCS #7 parser as root (L96).

const (
	// keyFile is the db certificate sbctl keeps, inside subvolume @.
	keyFile = "var/lib/sbctl/keys/db/db.pem"
	// maxKeyFile caps what is read from the installed root.
	maxKeyFile = 16 << 10
)

// installedRoots picks the gjallarOS root partitions on internal disks from
// lsblk oldroot.LsblkArgs. Disks on USB or marked removable are left out, as
// the recovery console does: a stick someone plugged in must not supply the
// key. Disks with something mounted are left out too.
func installedRoots(raw []byte) ([]oldroot.Part, error) {
	parts, err := oldroot.Find(raw)
	if err != nil {
		return nil, err
	}
	var roots []oldroot.Part
	for _, p := range parts {
		if p.Internal {
			roots = append(roots, p)
		}
	}
	return roots, nil
}

// readKey reads the db certificate below root, an open directory, without
// following links, and returns it as DER. A FIFO, a device or a file larger
// than maxKeyFile is refused before it is read.
func readKey(root *os.File) ([]byte, error) {
	fd, err := unix.Openat2(int(root.Fd()), keyFile, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_NOCTTY,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", keyFile, err)
	}
	f := os.NewFile(uintptr(fd), keyFile)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, fmt.Errorf("check %s: %w", keyFile, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("%s is not a regular file", keyFile)
	}
	if st.Size > maxKeyFile {
		return nil, fmt.Errorf("%s is larger than %d bytes", keyFile, maxKeyFile)
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxKeyFile+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", keyFile, err)
	}
	if len(raw) > maxKeyFile {
		return nil, fmt.Errorf("%s is larger than %d bytes", keyFile, maxKeyFile)
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("%s does not hold exactly one certificate", keyFile)
	}
	return block.Bytes, nil
}

// requeueKey is "gjallar-stick requeue-key", which the menu starts.
func requeueKey(ctx context.Context, in io.Reader, out io.Writer) error {
	started, err := secureboot.ShimStarted()
	if err != nil {
		return fmt.Errorf("read shim's key list: %w", err)
	}
	if !started {
		return errors.New("this start did not go through shim, so MokManager cannot run; start the installer stick with Secure Boot on")
	}
	ui := prompt.New(in, out)
	ui.Wrap = true
	ui.UsePages(in)
	raw, err := exec.CommandContext(ctx, "lsblk", oldroot.LsblkArgs...).Output()
	if err != nil {
		return fmt.Errorf("list disks: %w", err)
	}
	roots, err := installedRoots(raw)
	if err != nil {
		return err
	}
	var root oldroot.Part
	switch len(roots) {
	case 0:
		return errors.New("no installed gjallarOS was found on this computer's disks")
	case 1:
		root = roots[0]
	default:
		fmt.Fprintln(out, "This computer has more than one gjallarOS:")
		for i, r := range roots {
			kind := "encrypted"
			if !r.Encrypted {
				kind = "not encrypted"
			}
			fmt.Fprintf(out, "  %d) %s, %s\n", i+1, r.Path, kind)
		}
		line, err := ui.Value(ctx, "Number", "")
		if err != nil {
			return err
		}
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || n < 1 || n > len(roots) {
			return errors.New("no gjallarOS chosen")
		}
		root = roots[n-1]
	}
	der, err := keyFromRoot(ctx, out, root)
	if err != nil {
		return err
	}
	if !root.Encrypted {
		fmt.Fprint(out, calmmenu.Wrap("This gjallarOS is not encrypted. Anyone who had this computer for a few minutes could have put their own key on its disk. Compare the fingerprint below with the one in the install report or the one the installer showed at the end of the install.\n"))
	}
	_, err = secureboot.RequeueKey(ctx, ui, out, der)
	return err
}

// keyFromRoot opens the root read-only, reads the key and closes the root
// again before anything is queued.
func keyFromRoot(ctx context.Context, out io.Writer, root oldroot.Part) (der []byte, err error) {
	// base holds the checked header copy and the mount point; only root can
	// enter it.
	base, err := os.MkdirTemp("/run", "gjallar-key-root-")
	if err != nil {
		return nil, err
	}
	// Plain removes: if the unmount failed, nothing walks into the root.
	defer os.Remove(base)
	top := filepath.Join(base, "top")
	if err := os.Mkdir(top, 0o700); err != nil {
		return nil, err
	}
	defer os.Remove(top)
	// Close below must only ever close what this step opened.
	if err := oldroot.Free(top); err != nil {
		return nil, fmt.Errorf("%w; restart the stick", err)
	}
	device := root.Path
	if root.Encrypted {
		if err := openLUKS(ctx, out, base, root.Path); err != nil {
			return nil, err
		}
	}
	defer func() {
		if cerr := oldroot.Close(context.WithoutCancel(ctx), top); cerr != nil && err == nil {
			der, err = nil, fmt.Errorf("close the installed root: %w", cerr)
		}
	}()
	if root.Encrypted {
		if device, err = oldroot.MappedDevice(root.Path); err != nil {
			return nil, err
		}
	}
	if err := oldroot.Mount(device, top); err != nil {
		return nil, fmt.Errorf("mount the installed root read-only: %w", err)
	}
	dir, err := os.Open(top)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	sub, err := openSubvolume(dir)
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	return readKey(sub)
}

// openSubvolume opens @ below the top level without following links.
func openSubvolume(top *os.File) (*os.File, error) {
	fd, err := unix.Openat2(int(top.Fd()), "@", &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_DIRECTORY | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, fmt.Errorf("open subvolume @ of the installed root: %w", err)
	}
	return os.NewFile(uintptr(fd), "@"), nil
}

// openLUKS checks the root's LUKS metadata (L60), asks for the install
// password or the recovery key and opens the root read-only from the checked
// header copy in base. The key goes from systemd-ask-password to cryptsetup
// through a pipe, never through this process or a command line.
func openLUKS(ctx context.Context, out io.Writer, base, device string) error {
	cap, err := oldroot.MemoryCap()
	if err != nil {
		return err
	}
	prep, err := oldroot.Prepare(ctx, base, device, cap)
	if err != nil {
		return openFailure(err)
	}
	defer prep.Remove()
	fmt.Fprint(out, calmmenu.Wrap("The installed gjallarOS is encrypted. Its key is read from the encrypted disk, read-only.\n"))
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	ask := exec.CommandContext(ctx, "systemd-ask-password", "--timeout=0", "--emoji=no", "-n",
		"Install password or recovery key of the installed gjallarOS:")
	ask.Stdin, ask.Stdout, ask.Stderr = os.Stdin, w, os.Stderr
	if err := ask.Start(); err != nil {
		r.Close()
		w.Close()
		return fmt.Errorf("start systemd-ask-password: %w", err)
	}
	w.Close()
	// The key waits in the pipe until it is typed in full, so the time limit
	// of one try (R50) counts the open only, not the typing. Input longer
	// than the pipe buffer (64 KiB) blocks ask here; only the person at the
	// keyboard can do that, to their own session, and Ctrl+C ends it. Keep
	// it so: reading the key into this process would put it in our memory.
	if err := ask.Wait(); err != nil {
		r.Close()
		return fmt.Errorf("read the install password or recovery key: %w", err)
	}
	err = prep.Open(ctx, r)
	r.Close()
	if err != nil {
		return openFailure(err)
	}
	return nil
}

// openFailure words a refusal from oldroot for this step; its own texts
// speak of the move's old disk.
func openFailure(err error) error {
	r, ok := oldroot.AsRefusal(err)
	if !ok {
		return err
	}
	switch r.Reason {
	case oldroot.WrongKey:
		return errors.New("that password or recovery key did not open the disk")
	case oldroot.TooSlow:
		return errors.New("the installed gjallarOS took too long to open, so the try was stopped. Restart from the stick and try again")
	}
	msg := "the installed gjallarOS failed the safety check of its encryption header, so its key is not read: " + string(r.Reason)
	if r.Detail != "" {
		msg += " (" + r.Detail + ")"
	}
	return errors.New(msg)
}
