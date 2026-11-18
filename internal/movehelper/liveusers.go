package movehelper

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// maxPasswd caps the account list CheckLiveUsers reads.
const maxPasswd = 1 << 20

// CheckLiveUsers refuses to work while an account of the running system has
// a user id in minUserID-maxUserID (Q254). Those ids belong to the users of
// the disks the helper copies to: a live login with one of them would own
// what the helper writes for that user (L58). passwd must be root's plain
// file, written by nobody else.
func CheckLiveUsers(passwd string) error {
	fd, err := unix.Open(passwd, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("read %s: %w", passwd, err)
	}
	f := os.NewFile(uintptr(fd), passwd)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("read %s: %w", passwd, err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		return fmt.Errorf("%s is not a plain file", passwd)
	case st.Uid != rootUID:
		return fmt.Errorf("%s is not root's", passwd)
	case st.Mode&0o022 != 0:
		return fmt.Errorf("%s can be written by others than root", passwd)
	case st.Size > maxPasswd:
		return fmt.Errorf("%s is over %d bytes", passwd, maxPasswd)
	}
	data := make([]byte, maxPasswd+1)
	n, err := f.ReadAt(data, 0)
	if n > maxPasswd {
		return fmt.Errorf("%s is over %d bytes", passwd, maxPasswd)
	}
	if err != nil && n == 0 && st.Size > 0 {
		return fmt.Errorf("read %s: %w", passwd, err)
	}
	sc := bufio.NewScanner(bytes.NewReader(data[:n]))
	sc.Buffer(make([]byte, 4096), maxPasswd)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) != 7 {
			return fmt.Errorf("%s has a line that is not an account", passwd)
		}
		uid, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return fmt.Errorf("%s: account %q has no plain user id", passwd, fields[0])
		}
		if uid >= minUserID && uid <= maxUserID {
			return fmt.Errorf("account %q has user id %d, which belongs to the users of an installed system; the move runs only from the gjallarOS stick", fields[0], uid)
		}
	}
	return sc.Err()
}
