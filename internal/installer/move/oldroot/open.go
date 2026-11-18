package oldroot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"golang.org/x/sys/unix"
)

// Mapping is the device-mapper name of an opened old root: the move's old
// disk, or the installed root "queue the key again" reads.
const Mapping = "gjallar-old-root"

// headerFile is the checked copy of the old disk's LUKS metadata, inside
// the caller's root-only folder.
const headerFile = "old-header"

// wrongKeyWait is how long a wrong key holds the next try back, the same
// wait as the recovery console, so guessing stays slow.
var wrongKeyWait = 5 * time.Second

// CheckEncrypted reads and checks the LUKS metadata of part without a key.
func CheckEncrypted(ctx context.Context, part string, kdfCapKiB int64) (Header, error) {
	if err := cryptsetupVersion(ctx); err != nil {
		return Header{}, err
	}
	area, err := ReadHeaderArea(part)
	if err != nil {
		return Header{}, err
	}
	_, h, err := CheckArea(area, kdfCapKiB)
	return h, err
}

// Prepared is an encrypted root whose metadata was checked and copied, ready
// to be opened with a key.
type Prepared struct {
	part, header string
	Header       Header
}

// Prepare checks the LUKS metadata of the encrypted root part and writes the
// checked copy into base, a root-only folder. cryptsetup reads that copy,
// never the disk's own header, so the disk cannot show it other bytes than
// the checked ones. Remove the copy with Remove when done.
func Prepare(ctx context.Context, base, part string, kdfCapKiB int64) (*Prepared, error) {
	if err := mapperFree(); err != nil {
		return nil, err
	}
	if err := cryptsetupVersion(ctx); err != nil {
		return nil, err
	}
	area, err := ReadHeaderArea(part)
	if err != nil {
		return nil, err
	}
	area, h, err := CheckArea(area, kdfCapKiB)
	if err != nil {
		return nil, err
	}
	hdr := filepath.Join(base, headerFile)
	if err := writeNew(hdr, area); err != nil {
		return nil, err
	}
	return &Prepared{part: part, header: hdr, Header: h}, nil
}

// Open opens the root read-only as /dev/mapper/Mapping with the key read
// from key, exactly the bytes given. No token plugin is loaded.
func (p *Prepared) Open(ctx context.Context, key io.Reader) error {
	open := exec.CommandContext(ctx, "cryptsetup", "open", "--readonly", "--type", "luks2",
		"--disable-external-tokens", "--header", p.header, "--key-file=-", "--", p.part, Mapping)
	stderr := &capWriter{max: 4096}
	open.Stdin, open.Stdout, open.Stderr = key, stderr, stderr
	if err := open.Run(); err != nil {
		time.Sleep(wrongKeyWait)
		return cryptsetupFailure(err, stderr.buf.Bytes())
	}
	return nil
}

// Remove deletes the header copy.
func (p *Prepared) Remove() { os.Remove(p.header) }

// MemoryCap is the Argon2 memory cap for this computer now (KDFCap).
func MemoryCap() (int64, error) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	return KDFCap(raw)
}

// writeNew writes data to a file that must not exist yet, so nothing
// planted at the path is followed or reused.
func writeNew(path string, data []byte) error {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Mount mounts the top level of the btrfs on dev read-only at target. No
// device files, no programs, no links, and no log replay, which would
// write.
func Mount(dev, target string) error {
	if err := mountFree(target); err != nil {
		return err
	}
	flags := uintptr(unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC | unix.MS_NOSYMFOLLOW)
	if err := unix.Mount(dev, target, "btrfs", flags, "subvolid=5,rescue=nologreplay"); err != nil {
		return refuse(NotBtrfs, "mount: "+err.Error())
	}
	return nil
}

// Close unmounts target if the old disk is mounted there and closes the
// mapping if it is open. Both are fine to find already gone.
func Close(ctx context.Context, target string) error {
	var errs []error
	if mounted, err := isMountPoint(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	} else if mounted {
		if err := unix.Unmount(target, unix.UMOUNT_NOFOLLOW); err != nil {
			errs = append(errs, fmt.Errorf("unmount %s: %w", target, err))
		}
	}
	if _, err := os.Lstat("/dev/mapper/" + Mapping); err == nil {
		out, err := exec.CommandContext(ctx, "cryptsetup", "close", "--", Mapping).CombinedOutput()
		if err != nil {
			errs = append(errs, fmt.Errorf("close %s: %v: %s", Mapping, err, printable(string(out))))
		}
	}
	return errors.Join(errs...)
}

// Free refuses when an old disk is already open: mounted at target or
// mapped. Close it first.
func Free(target string) error {
	if err := mapperFree(); err != nil {
		return err
	}
	return mountFree(target)
}

func mapperFree() error {
	if _, err := os.Lstat("/dev/mapper/" + Mapping); err == nil {
		return refuse(Busy, "/dev/mapper/"+Mapping+" exists")
	}
	return nil
}

func mountFree(target string) error {
	if mounted, err := isMountPoint(target); err != nil {
		return err
	} else if mounted {
		return refuse(Busy, target+" is already a mount point")
	}
	return nil
}

// isMountPoint tells whether something is mounted on the folder p: a mount
// gives it another device than its parent.
func isMountPoint(p string) (bool, error) {
	var st, parent unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		return false, fmt.Errorf("check %s: %w", p, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return false, fmt.Errorf("%s is not a folder", p)
	}
	if err := unix.Lstat(filepath.Dir(p), &parent); err != nil {
		return false, fmt.Errorf("check %s: %w", filepath.Dir(p), err)
	}
	return st.Dev != parent.Dev, nil
}

func cryptsetupVersion(ctx context.Context) error {
	out := &capWriter{max: 256}
	cmd := exec.CommandContext(ctx, "cryptsetup", "--version")
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run cryptsetup --version: %w", err)
	}
	return CheckCryptsetup(out.buf.Bytes())
}

// cryptsetupFailure turns a failed cryptsetup open into a refusal or one
// line. Exit code 2 means no key slot took the key (cryptsetup(8), RETURN
// CODES); any other failure keeps cryptsetup's own text, cleaned of
// terminal codes because it can quote the disk's header.
func cryptsetupFailure(err error, stderr []byte) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 2 {
		return refuse(WrongKey, "")
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
