package oldroot

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"

	"golang.org/x/sys/unix"
)

// openBlock opens a block device for reading without following a link. Any
// other kind of file is refused before it is read.
func openBlock(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		f.Close()
		return nil, fmt.Errorf("check %s: %w", path, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFBLK {
		f.Close()
		return nil, fmt.Errorf("%s is not a disk", path)
	}
	return f, nil
}

// SetReadOnly marks each block device read-only in the kernel, as
// blockdev --setro does, and checks that it took. Nothing on the stick can
// write to the old disk afterwards, a mount included.
func SetReadOnly(paths ...string) error {
	for _, p := range paths {
		f, err := openBlock(p)
		if err != nil {
			return err
		}
		fd := int(f.Fd())
		err = unix.IoctlSetPointerInt(fd, unix.BLKROSET, 1)
		if err == nil {
			var ro int
			ro, err = unix.IoctlGetInt(fd, unix.BLKROGET)
			if err == nil && ro != 1 {
				err = errors.New("the kernel kept it writable")
			}
		}
		f.Close()
		if err != nil {
			return fmt.Errorf("set %s read-only: %w", p, err)
		}
	}
	return nil
}

// maxHeaderArea caps the copy of the LUKS metadata: both header copies and
// the key slot area. gjallarOS installs use 16 MiB.
const maxHeaderArea = 64 << 20

// ReadHeaderArea reads the LUKS metadata of the partition at path: the
// bytes before the data segment. CheckArea checks it, and cryptsetup gets
// the same bytes as a detached header (--header), so the disk cannot show
// cryptsetup a header other than the one that was checked.
func ReadHeaderArea(path string) ([]byte, error) {
	f, err := openBlock(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// The end of a block device is its size.
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("size of %s: %w", path, err)
	}
	n := min(size, maxHeaderArea)
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, 0); err != nil {
		return nil, fmt.Errorf("read the header of %s: %w", path, err)
	}
	return buf, nil
}

// CheckArea checks the metadata area read by ReadHeaderArea and returns it
// cut at the start of the data segment, which is what cryptsetup gets.
func CheckArea(area []byte, kdfCapKiB int64) ([]byte, Header, error) {
	h, err := CheckHeader(bytes.NewReader(area), int64(len(area)), kdfCapKiB)
	if err != nil {
		return nil, h, err
	}
	if h.DataOffset > int64(len(area)) {
		return nil, h, refuse(Unsupported, fmt.Sprintf("data starts at %d MiB", h.DataOffset>>20))
	}
	area = area[:h.DataOffset]
	// The cut can drop a copy that sat beyond the data start; check again
	// what is left, so the checked bytes are exactly the ones handed on.
	h2, err := CheckHeader(bytes.NewReader(area), int64(len(area)), kdfCapKiB)
	if err != nil {
		return nil, h, err
	}
	return area, h2, nil
}

// MaxKDFMemoryKiB is the most memory an Argon2 key slot may ask for, the
// limit cryptsetup itself sets (4 GiB).
const MaxKDFMemoryKiB = 4 << 20

// KDFCap is the Argon2 memory cap for this computer: three quarters of the
// memory that is free now, and never over 4 GiB. A header asking for more
// would make the stick run out of memory while unlocking.
func KDFCap(meminfo []byte) (int64, error) {
	sc := bufio.NewScanner(bytes.NewReader(meminfo))
	for sc.Scan() {
		var kib int64
		if n, _ := fmt.Sscanf(sc.Text(), "MemAvailable: %d kB", &kib); n == 1 {
			if kib <= 0 {
				break
			}
			return min(kib/4*3, MaxKDFMemoryKiB), nil
		}
	}
	return 0, errors.New("/proc/meminfo has no MemAvailable line")
}

// minCryptsetup is the first cryptsetup that checks LUKS2 metadata before it
// trusts it (2.4.3, CVE-2021-4122).
var minCryptsetup = [3]int{2, 4, 3}

var versionLine = regexp.MustCompile(`^cryptsetup (\d+)\.(\d+)\.(\d+)`)

// CheckCryptsetup reads the output of cryptsetup --version.
func CheckCryptsetup(out []byte) error {
	m := versionLine.FindSubmatch(bytes.TrimSpace(out))
	if m == nil {
		return refuse(OldCryptsetup, "no version found")
	}
	for i := range 3 {
		n, err := strconv.Atoi(string(m[i+1]))
		if err != nil {
			return refuse(OldCryptsetup, "no version found")
		}
		if n != minCryptsetup[i] {
			if n < minCryptsetup[i] {
				return refuse(OldCryptsetup, "cryptsetup "+printable(string(m[0])))
			}
			return nil
		}
	}
	return nil
}
