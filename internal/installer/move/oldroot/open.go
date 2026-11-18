package oldroot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// openTryLimit caps one key try (R50, L118): a header made to take hours
// to unlock, or a disk that hangs, stops the move instead of holding the
// stick. A real try takes seconds; the KDF memory cap keeps it there.
// killWait is how long cryptsetup gets to stop after it is told to before
// it is killed. Tests shorten both.
var openTryLimit, killWait = 60 * time.Second, 10 * time.Second

// mapperDir is where device-mapper names show; tests point it elsewhere.
var mapperDir = "/dev/mapper"

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
	// Digest is AreaDigest of the checked copy.
	Digest string
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
	return &Prepared{part: part, header: hdr, Header: h, Digest: AreaDigest(area)}, nil
}

// AreaDigest is the hex SHA-256 of a metadata area cut by CheckArea. Any
// change of the header, a new PIN or a new TPM enrolment included, changes
// it. The move-prepared record is bound to it.
func AreaDigest(area []byte) string {
	sum := sha256.Sum256(area)
	return hex.EncodeToString(sum[:])
}

// Open opens the root read-only as /dev/mapper/Mapping with the key read
// from key, exactly the bytes given. No token plugin is loaded.
//
// One try gets openTryLimit. Past it, or when ctx ends, cryptsetup is told
// to stop and then killed, and a mapping it may have made just before is
// closed again: Free made sure none was there before the try.
func (p *Prepared) Open(ctx context.Context, key io.Reader) error {
	try, cancel := context.WithTimeout(ctx, openTryLimit)
	defer cancel()
	open := Tool(try, Cryptsetup, "open", "--readonly", "--type", "luks2",
		"--disable-external-tokens", "--header", p.header, "--key-file=-", "--", p.part, Mapping)
	open.Cancel = func() error { return open.Process.Signal(unix.SIGTERM) }
	open.WaitDelay = killWait
	stderr := &capWriter{max: 4096}
	open.Stdin, open.Stdout, open.Stderr = key, stderr, stderr
	err := open.Run()
	if try.Err() != nil {
		// Stopped, whatever cryptsetup managed before: nothing stays open.
		left := closeMapping(context.WithoutCancel(ctx))
		if ctx.Err() != nil {
			return errors.Join(ctx.Err(), left)
		}
		return errors.Join(refuse(TooSlow, fmt.Sprintf("one key try ran over %s", openTryLimit)), left)
	}
	if err != nil {
		time.Sleep(wrongKeyWait)
		return cryptsetupFailure(err, stderr.buf.Bytes())
	}
	return nil
}

// closeMapping closes Mapping if it is there. It gets a short time of its
// own: it runs after the try's time ran out.
func closeMapping(ctx context.Context) error {
	if !mapped() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := Tool(ctx, Cryptsetup, "close", "--", Mapping).CombinedOutput()
	if err != nil {
		return fmt.Errorf("close %s: %v: %s", Mapping, err, printable(string(out)))
	}
	return nil
}

// sysBlock is where the kernel lists block devices and devDir where their
// nodes are; tests point them elsewhere.
var sysBlock, devDir = "/sys/block", "/dev"

// MappedDevice returns the device node of the opened Mapping, /dev/dm-N.
// /dev/mapper/Mapping is a link made by udev and is never followed: the
// kernel's own list names the node, and it must be a LUKS2 mapping of part
// and nothing else.
func MappedDevice(part string) (string, error) {
	entries, err := os.ReadDir(sysBlock)
	if err != nil {
		return "", fmt.Errorf("list block devices: %w", err)
	}
	for _, e := range entries {
		dm := e.Name()
		if !strings.HasPrefix(dm, "dm-") {
			continue
		}
		dir := filepath.Join(sysBlock, dm)
		name, err := os.ReadFile(filepath.Join(dir, "dm", "name"))
		if err != nil || strings.TrimSpace(string(name)) != Mapping {
			continue
		}
		uuid, err := os.ReadFile(filepath.Join(dir, "dm", "uuid"))
		if err != nil || !strings.HasPrefix(string(uuid), "CRYPT-LUKS2-") {
			return "", fmt.Errorf("%s is not a LUKS2 mapping", Mapping)
		}
		slaves, err := os.ReadDir(filepath.Join(dir, "slaves"))
		if err != nil {
			return "", fmt.Errorf("check %s: %w", Mapping, err)
		}
		if len(slaves) != 1 || slaves[0].Name() != filepath.Base(part) {
			return "", fmt.Errorf("%s does not sit on %s", Mapping, part)
		}
		var major, minor uint32
		raw, err := os.ReadFile(filepath.Join(dir, "dev"))
		if err != nil {
			return "", fmt.Errorf("check %s: %w", Mapping, err)
		}
		if n, _ := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d:%d", &major, &minor); n != 2 {
			return "", fmt.Errorf("check %s: no device number", Mapping)
		}
		node := filepath.Join(devDir, dm)
		var st unix.Stat_t
		if err := unix.Lstat(node, &st); err != nil {
			return "", fmt.Errorf("check %s: %w", node, err)
		}
		if st.Mode&unix.S_IFMT != unix.S_IFBLK || st.Rdev != unix.Mkdev(major, minor) {
			return "", fmt.Errorf("%s is not the device of %s", node, Mapping)
		}
		return node, nil
	}
	return "", fmt.Errorf("%s is not open", Mapping)
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
	if mapped() {
		out, err := Tool(ctx, Cryptsetup, "close", "--", Mapping).CombinedOutput()
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

// Opened refuses unless the old disk is mounted at target, as open left it.
func Opened(target string) error {
	mounted, err := isMountPoint(target)
	if errors.Is(err, os.ErrNotExist) {
		return refuse(NotOpen, target+" is not there")
	}
	if err != nil {
		return err
	}
	if !mounted {
		return refuse(NotOpen, target+" is not a mount point")
	}
	return nil
}

func mapperFree() error {
	if mapped() {
		return refuse(Busy, Mapping+" is open")
	}
	return nil
}

// mapped tells whether Mapping is there: as a node in mapperDir, or in the
// kernel's list before udev made the node, as right after a stopped open.
func mapped() bool {
	if _, err := os.Lstat(filepath.Join(mapperDir, Mapping)); err == nil {
		return true
	}
	entries, _ := os.ReadDir(sysBlock)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "dm-") {
			continue
		}
		name, err := os.ReadFile(filepath.Join(sysBlock, e.Name(), "dm", "name"))
		if err == nil && strings.TrimSpace(string(name)) == Mapping {
			return true
		}
	}
	return false
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
	cmd := Tool(ctx, Cryptsetup, "--version")
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
