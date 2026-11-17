package stick

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/sys/unix"

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
	// keyMapping is the device-mapper name of the root opened here.
	keyMapping = "gjallar-key-root"
	// keyFile is the db certificate sbctl keeps, inside subvolume @.
	keyFile = "var/lib/sbctl/keys/db/db.pem"
	// maxKeyFile caps what is read from the installed root.
	maxKeyFile = 16 << 10
	// rootLabel is the PARTLABEL the installer gives the root partition.
	rootLabel = "GJALLAROS"
)

// rootPart is one root partition of an installed gjallarOS.
type rootPart struct {
	Path      string
	Encrypted bool
}

// installedRoots picks the gjallarOS root partitions on internal disks from
// lsblk -J -p -o PATH,TYPE,FSTYPE,PARTLABEL,PKNAME,TRAN,RM. Disks on USB or
// marked removable are left out, as the recovery console does: a stick
// someone plugged in must not supply the key.
func installedRoots(raw []byte) ([]rootPart, error) {
	if len(raw) > 1<<20 {
		return nil, errors.New("lsblk printed too much")
	}
	var out struct {
		BlockDevices []struct {
			Path      string `json:"path"`
			Type      string `json:"type"`
			FSType    string `json:"fstype"`
			PartLabel string `json:"partlabel"`
			PKName    string `json:"pkname"`
			Tran      string `json:"tran"`
			RM        any    `json:"rm"`
		} `json:"blockdevices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode lsblk: %w", err)
	}
	internal := map[string]bool{}
	for _, d := range out.BlockDevices {
		if d.Type == "disk" && d.Tran != "usb" && !removable(d.RM) {
			internal[d.Path] = true
		}
	}
	var roots []rootPart
	for _, d := range out.BlockDevices {
		if d.Type != "part" || d.PartLabel != rootLabel || !internal[d.PKName] {
			continue
		}
		switch d.FSType {
		case "crypto_LUKS":
			roots = append(roots, rootPart{Path: d.Path, Encrypted: true})
		case "btrfs":
			roots = append(roots, rootPart{Path: d.Path})
		}
	}
	return roots, nil
}

// removable reads lsblk's RM column, a bool in newer util-linux and "0" or
// "1" in older ones. Anything else counts as removable.
func removable(v any) bool {
	switch rm := v.(type) {
	case bool:
		return rm
	case string:
		return rm != "0"
	case float64:
		return rm != 0
	}
	return true
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
	raw, err := exec.CommandContext(ctx, "lsblk", "-J", "-l", "-p", "-o", "PATH,TYPE,FSTYPE,PARTLABEL,PKNAME,TRAN,RM").Output()
	if err != nil {
		return fmt.Errorf("list disks: %w", err)
	}
	roots, err := installedRoots(raw)
	if err != nil {
		return err
	}
	var root rootPart
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
		fmt.Fprintln(out, "This gjallarOS is not encrypted. Anyone who had this computer for a few minutes could have")
		fmt.Fprintln(out, "put their own key on its disk. Compare the fingerprint below with the one in the install")
		fmt.Fprintln(out, "report or the one the installer showed at the end of the install.")
	}
	_, err = secureboot.RequeueKey(ctx, ui, out, der)
	return err
}

// keyFromRoot opens the root read-only, reads the key and closes the root
// again before anything is queued.
func keyFromRoot(ctx context.Context, out io.Writer, root rootPart) (der []byte, err error) {
	device := root.Path
	if root.Encrypted {
		if _, err := os.Lstat("/dev/mapper/" + keyMapping); err == nil {
			return nil, fmt.Errorf("/dev/mapper/%s is already open; restart the stick", keyMapping)
		}
		if err := openLUKS(ctx, out, root.Path); err != nil {
			return nil, err
		}
		defer func() {
			if cerr := exec.CommandContext(context.WithoutCancel(ctx), "cryptsetup", "close", keyMapping).Run(); cerr != nil && err == nil {
				der, err = nil, fmt.Errorf("close the installed root: %w", cerr)
			}
		}()
		device = "/dev/mapper/" + keyMapping
	}
	dir, err := os.MkdirTemp("/run", "gjallar-key-root-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(dir)
	// The top level holds @, where / lives; nologreplay keeps a read-only
	// mount from needing to write. No device files, no programs, no links.
	flags := uintptr(unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC | unix.MS_NOSYMFOLLOW)
	if err := unix.Mount(device, dir, "btrfs", flags, "subvolid=5,rescue=nologreplay"); err != nil {
		return nil, fmt.Errorf("mount the installed root read-only: %w", err)
	}
	defer func() {
		if uerr := unix.Unmount(dir, 0); uerr != nil && err == nil {
			der, err = nil, fmt.Errorf("unmount the installed root: %w", uerr)
		}
	}()
	top, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer top.Close()
	sub, err := openSubvolume(top)
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

// openLUKS asks for the install password or the recovery key and opens the
// root read-only. It goes from systemd-ask-password to cryptsetup through a pipe, never
// through this process or a command line.
func openLUKS(ctx context.Context, out io.Writer, device string) error {
	fmt.Fprintln(out, "The installed gjallarOS is encrypted. Its key is read from the encrypted disk, read-only.")
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	ask := exec.CommandContext(ctx, "systemd-ask-password", "--timeout=0", "--emoji=no", "-n",
		"Install password or recovery key of the installed gjallarOS:")
	ask.Stdin, ask.Stdout, ask.Stderr = os.Stdin, w, os.Stderr
	open := exec.CommandContext(ctx, "cryptsetup", "open", "--readonly", "--type", "luks",
		"--key-file=-", "--", device, keyMapping)
	// cryptsetup's own words for a wrong key are replaced by ours below.
	stderr := &capWriter{max: 4096}
	open.Stdin, open.Stdout, open.Stderr = r, os.Stdout, stderr
	if err := open.Start(); err != nil {
		r.Close()
		w.Close()
		return fmt.Errorf("start cryptsetup: %w", err)
	}
	r.Close()
	askErr := ask.Run()
	w.Close()
	openErr := open.Wait()
	switch {
	case askErr != nil:
		return fmt.Errorf("read the install password or recovery key: %w", askErr)
	case openErr != nil:
		// The same wait as the recovery console, so guessing stays slow.
		time.Sleep(5 * time.Second)
		return cryptsetupFailure(openErr, stderr.buf.Bytes())
	}
	return nil
}

// cryptsetupFailure turns a failed cryptsetup open into one line. Exit
// code 2 means no key slot took the key (cryptsetup(8), RETURN CODES);
// any other failure keeps cryptsetup's own text, cleaned of terminal codes
// because it can quote the disk's header.
func cryptsetupFailure(err error, stderr []byte) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 2 {
		return errors.New("that password or recovery key did not open the disk")
	}
	var lines []string
	for _, line := range strings.Split(string(stderr), "\n") {
		var b strings.Builder
		for _, r := range line {
			if unicode.IsPrint(r) {
				b.WriteRune(r)
			}
		}
		if text := strings.TrimSpace(b.String()); text != "" {
			lines = append(lines, text)
		}
	}
	text := []rune(strings.Join(lines, "; "))
	if len(text) > 200 {
		text = append(text[:200], '…')
	}
	if len(text) == 0 {
		return fmt.Errorf("cryptsetup could not open the disk: %w", err)
	}
	return fmt.Errorf("cryptsetup could not open the disk: %s", string(text))
}

// capWriter keeps the first max bytes written to it and drops the rest.
type capWriter struct {
	buf bytes.Buffer
	max int
}

func (c *capWriter) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
