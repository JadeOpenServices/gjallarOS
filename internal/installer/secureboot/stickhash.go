package secureboot

import (
	"bytes"
	"context"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/calmmenu"
)

// The installer stick boots under Secure Boot through shim with its UKI
// enrolled in shim's MOK list as a single file hash (installer stick, Q1).
// That entry would keep trusting the stick after the install, so the
// installer queues its deletion for the same MokManager visit that enrolls
// the gjallarOS key. The hash comes from the UKI the firmware loaded: stage
// 1 copies it from the stick's ESP to stickUKI before anything of the user
// session runs (system/installer-media/stick.nix, gjallar-stick-uki).

// stubLoaderGUID is systemd-stub's vendor GUID (LoaderDevicePartUUID,
// StubImageIdentifier).
const stubLoaderGUID = "4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"

// stickBootPath is where shim on the stick finds the UKI; systemd-stub
// reports it as StubImageIdentifier.
const stickBootPath = `\EFI\BOOT\grubx64.efi`

// stickUKIMax caps the copy; the stick UKI is about 42 MiB. Stage 1 uses
// the same cap.
const stickUKIMax = 128 << 20

type stickCopy struct {
	dir, name string
	// owner must own dir and the file; root on a real system.
	owner int
	// cmdline is the running kernel's command line.
	cmdline string
}

var liveStickCopy = stickCopy{
	dir:     "/run/gjallar",
	name:    "stick-uki.efi",
	owner:   0,
	cmdline: "/proc/cmdline",
}

// errNoStickCopy: this start came from the stick, but stage 1 left no copy
// of its UKI.
var errNoStickCopy = errors.New("the installer stick's boot file was not copied at start (gjallar-stick-uki.service); restart from the stick and run the installer again")

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// StickHash returns the Authenticode SHA-256 of the installer stick's UKI
// when this system started from it; ok is false for any other start.
func StickHash() (sum string, ok bool, err error) {
	return liveFirmware.stickHash(liveStickCopy)
}

func (f firmwareFiles) stickHash(c stickCopy) (string, bool, error) {
	raw, present, err := f.efivar("StubImageIdentifier", stubLoaderGUID)
	if errors.Is(err, fs.ErrPermission) {
		raw, present, err = f.rootEfivar("StubImageIdentifier", stubLoaderGUID)
	}
	if err != nil {
		return "", false, fmt.Errorf("read the boot file systemd-stub reports: %w", err)
	}
	if !present || !strings.EqualFold(utf16String(raw), stickBootPath) {
		return "", false, nil
	}
	data, err := c.read()
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, errNoStickCopy
	}
	if err != nil {
		return "", false, err
	}
	if err := c.sameVerity(data); err != nil {
		return "", false, err
	}
	sum, err := AuthenticodeSHA256(data)
	if err != nil {
		return "", false, fmt.Errorf("hash the installer stick's boot file: %w", err)
	}
	return hex.EncodeToString(sum[:]), true, nil
}

// utf16String decodes a NUL-terminated UTF-16LE EFI string.
func utf16String(raw []byte) string {
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		u := binary.LittleEndian.Uint16(raw[i:])
		if u == 0 {
			break
		}
		units = append(units, u)
	}
	return string(utf16.Decode(units))
}

// read opens the copy without following links and without blocking on a
// FIFO, and takes it only when owner holds the folder and the file and no
// one else may write either.
func (c stickCopy) read() ([]byte, error) {
	dirFD, err := unix.Open(c.dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: c.dir, Err: err}
	}
	defer unix.Close(dirFD)
	if err := c.trusted(dirFD, c.dir, unix.S_IFDIR); err != nil {
		return nil, err
	}
	path := c.dir + "/" + c.name
	fd, err := unix.Openat(dirFD, c.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	if err := c.trusted(fd, path, unix.S_IFREG); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, stickUKIMax+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > stickUKIMax {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, stickUKIMax)
	}
	return data, nil
}

func (c stickCopy) trusted(fd int, path string, kind uint32) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return &fs.PathError{Op: "stat", Path: path, Err: err}
	}
	switch {
	case st.Mode&unix.S_IFMT != kind:
		return fmt.Errorf("%s is not a plain %s", path, map[uint32]string{unix.S_IFDIR: "folder", unix.S_IFREG: "file"}[kind])
	case int(st.Uid) != c.owner:
		return fmt.Errorf("%s belongs to uid %d, not %d", path, st.Uid, c.owner)
	case st.Mode&0o022 != 0:
		return fmt.Errorf("%s may be changed by others (mode %04o)", path, st.Mode&0o7777)
	case kind == unix.S_IFREG && st.Size > stickUKIMax:
		return fmt.Errorf("%s is larger than %d bytes", path, stickUKIMax)
	}
	return nil
}

// sameVerity ties the copy to this start: its signed command line must pin
// the same verity root as the running kernel's. A copy of another stick
// image never matches.
func (c stickCopy) sameVerity(uki []byte) error {
	image, err := pe.NewFile(bytes.NewReader(uki))
	if err != nil {
		return fmt.Errorf("read the installer stick's boot file: %w", err)
	}
	section := image.Section(".cmdline")
	if section == nil {
		return errors.New("the installer stick's boot file has no command line")
	}
	embedded, err := section.Data()
	if err != nil {
		return fmt.Errorf("read the installer stick's command line: %w", err)
	}
	// Section data is padded to the file alignment.
	embedded = bytes.TrimRight(embedded, "\x00")
	running, err := os.ReadFile(c.cmdline)
	if err != nil {
		return err
	}
	want, err := verityToken(string(embedded))
	if err != nil {
		return fmt.Errorf("installer stick's boot file: %w", err)
	}
	got, err := verityToken(string(running))
	if err != nil {
		return fmt.Errorf("running kernel: %w", err)
	}
	if want != got {
		return errors.New("the copied boot file belongs to another stick image than the one running")
	}
	return nil
}

func verityToken(cmdline string) (string, error) {
	var found []string
	for _, field := range strings.Fields(cmdline) {
		if value, ok := strings.CutPrefix(field, "gjallar.verity="); ok {
			found = append(found, value)
		}
	}
	if len(found) != 1 || found[0] == "" {
		return "", fmt.Errorf("the command line carries %d gjallar.verity= values, want one", len(found))
	}
	return found[0], nil
}

// hashDelete is a queued deletion of file hashes from shim's list.
type hashDelete struct {
	// sums are the hashes MokManager is asked to delete; stick is the
	// running stick's own among them, or "".
	sums  []string
	stick string
	// reused: every request waited already, so the password chosen when
	// it was made still stands.
	reused bool
}

// againPassword is printed before each further mokutil call of one visit:
// MokManager checks the password of the last call.
const againPassword = "Type the same password again: MokManager asks for it once more to confirm the deletions."

// mokFileSums lists the SHA-256 file hashes in the shim variable name.
func mokFileSums(f firmwareFiles, name string) ([]string, error) {
	raw, present, err := f.shimRequest(name)
	if err != nil || !present {
		return nil, err
	}
	list := parseSignatureList(raw, true)
	if list.Malformed {
		return nil, fmt.Errorf("%s does not parse", name)
	}
	return list.FileSums, nil
}

// mokHash tells whether the shim variable name lists the file hash sum.
func mokHash(f firmwareFiles, name, sum string) (bool, error) {
	sums, err := mokFileSums(f, name)
	return slices.Contains(sums, sum), err
}

// queueHashDeletes asks MokManager to delete file hashes from shim's list
// at the next start: the running stick's own entry, and on confirmation
// every other file hash (L99). gjallarOS enrolls file hashes only for its
// installer sticks, so the others are most likely older sticks whose
// visit after the install was missed or that were used with another
// install; each would keep starting under Secure Boot. It does nothing
// when this start did not come from the stick. Root sees only checked hex
// hashes; the stick file stays with this process. password is printed
// before the first mokutil call.
func queueHashDeletes(ctx context.Context, ui UI, f firmwareFiles, c stickCopy, out io.Writer, password string) (*hashDelete, error) {
	stick, ok, err := f.stickHash(c)
	if err != nil || !ok {
		return nil, err
	}
	listed, err := mokFileSums(f, "MokListRT")
	if err != nil || len(listed) == 0 {
		return nil, err
	}
	del := &hashDelete{}
	var others []string
	for _, sum := range listed {
		if !sha256Hex.MatchString(sum) {
			return nil, fmt.Errorf("unexpected hash %q in shim's list", sum)
		}
		switch {
		case sum == stick:
			del.stick = sum
			del.sums = append(del.sums, sum)
		case !slices.Contains(others, sum):
			others = append(others, sum)
		}
	}
	queued, err := mokFileSums(f, "MokDel")
	if err != nil {
		return nil, err
	}
	if len(others) > 0 {
		fmt.Fprint(out, calmmenu.WrapFor(out, "Shim also trusts these boot files, most likely older gjallarOS installer sticks:\n"))
		for _, sum := range others {
			fmt.Fprintln(out, "  SHA256 "+spacedHex(sum[:16]))
		}
		fmt.Fprintln(out, "Each of them keeps starting with Secure Boot on until it is deleted.")
		yes, err := ui.Confirm(ctx, "Delete them too? Keep them only if another system on this computer starts with one of them.", true)
		if err != nil {
			return nil, err
		}
		if yes {
			del.sums = append(del.sums, others...)
		} else if slices.ContainsFunc(others, func(sum string) bool { return slices.Contains(queued, sum) }) {
			return nil, errors.New("MokManager already holds a request to delete some of them; drop it with mokutil --revoke-delete, or answer yes")
		}
	}
	if len(del.sums) == 0 {
		return nil, nil
	}
	var todo []string
	for _, sum := range del.sums {
		if !slices.Contains(queued, sum) {
			todo = append(todo, sum)
		}
	}
	if len(todo) == 0 {
		fmt.Fprintln(out, "These entries already wait for MokManager to delete them.")
		del.reused = true
		return del, nil
	}
	// At most one EFI_SIGNATURE_LIST with one SHA-256 entry each.
	if err := efiSpaceFor(f.efivars, len(todo)*(28+16+32)); err != nil {
		return nil, err
	}
	for i, sum := range todo {
		if i == 0 {
			fmt.Fprint(out, calmmenu.WrapFor(out, password+"\n"))
		} else {
			fmt.Fprint(out, calmmenu.WrapFor(out, againPassword+"\n"))
		}
		if err := run(ctx, "sudo", privilegedTool("mokutil"), "--delete-hash", sum); err != nil {
			return nil, fmt.Errorf("queue a file hash for deletion: %w", err)
		}
	}
	// mokutil prints "Skip hash" and succeeds when it finds nothing to do.
	if queued, err = mokFileSums(f, "MokDel"); err != nil {
		return nil, err
	}
	for _, sum := range del.sums {
		if !slices.Contains(queued, sum) {
			return nil, fmt.Errorf("mokutil returned, but the firmware holds no request to delete the file hash %s", spacedHex(sum[:16]))
		}
	}
	return del, nil
}

// QueueStickDelete queues the deletion of the installer stick's MOK entry,
// and of older sticks' entries, on its own (installs that do not go
// through MokManager for a key). It returns the lines for the summary.
func QueueStickDelete(ctx context.Context, ui UI, out io.Writer) (string, error) {
	del, err := queueHashDeletes(ctx, ui, liveFirmware, liveStickCopy, out,
		"Choose a one-time password for MokManager. MokManager reads the keyboard as US layout; letters and digits are safe.")
	if err != nil || del == nil {
		return "", err
	}
	if err := holdMokManager(ctx, liveFirmware); err != nil {
		return "", err
	}
	return hashDeleteNotice(del), nil
}

func hashDeleteNotice(del *hashDelete) string {
	password := "the password you just chose"
	if del.reused {
		password = "the password chosen when the request was made"
	}
	starts := make([]string, len(del.sums))
	for i, sum := range del.sums {
		starts[i] = "  " + spacedHex(sum[:16])
	}
	return fmt.Sprintf(`The installer stick stays trusted by shim until MokManager deletes its entry, and so do older sticks. When shim next starts, MokManager shows a blue screen and waits until you choose. Choose Delete MOK, check that the hashes start with
%s
confirm and type %s. Continue boot drops the request.
`, strings.Join(starts, "\n"), password)
}
