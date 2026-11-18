package app

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procRoot and mapperDir are where mappingUsers looks; tests point them at
// a synthetic tree.
var (
	procRoot  = "/proc"
	mapperDir = "/dev/mapper"
)

const (
	maxProcMountinfo = 8 << 20
	maxComm          = 64
)

// mappingUser is one process whose mount namespace still mounts the new
// system disk.
type mappingUser struct {
	pid  int
	comm string
}

func (u mappingUser) String() string { return fmt.Sprintf("%s (pid %d)", u.comm, u.pid) }

// mappingUsers finds a process in any mount namespace that still has the
// LUKS mapping mounted (L151). umount -R reaches only the installer's own
// namespace: a copy made in another one, by a service started meanwhile or
// a move helper that did not detach, keeps the file system open after the
// mapping looks free here. Each namespace is read once; a process whose
// tables cannot be read is skipped.
func mappingUsers(mapping string) []mappingUser {
	sources := map[string]bool{"/dev/mapper/" + mapping: true}
	if real, err := filepath.EvalSymlinks(filepath.Join(mapperDir, mapping)); err == nil {
		sources[real] = true
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var users []mappingUser
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		dir := filepath.Join(procRoot, e.Name())
		if ns, err := os.Readlink(filepath.Join(dir, "ns", "mnt")); err == nil {
			if seen[ns] {
				continue
			}
			seen[ns] = true
		}
		data, err := readCappedProc(filepath.Join(dir, "mountinfo"), maxProcMountinfo)
		if err != nil || !mountsSource(data, sources) {
			continue
		}
		comm, _ := readCappedProc(filepath.Join(dir, "comm"), maxComm)
		users = append(users, mappingUser{pid: pid, comm: oneLine(string(comm))})
	}
	return users
}

// blockFS are the file system types the installer puts on the mapping and
// their kin. None can be mounted in an unprivileged user namespace, so a
// line with one of them and the mapping as source was mounted by root. A
// tmpfs, FUSE or overlay mount names any source it likes: the live session
// could fake a user and keep the installer from closing the mapping.
var blockFS = map[string]bool{"btrfs": true, "ext2": true, "ext3": true, "ext4": true, "xfs": true, "f2fs": true}

// mountsSource is true when a line of the mount table mounts one of
// sources with a block file system. After the "-" separator come the type
// and the source.
func mountsSource(data []byte, sources map[string]bool) bool {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 4096), maxProcMountinfo)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		for i := 6; i+2 < len(fields); i++ {
			if fields[i] == "-" {
				if blockFS[fields[i+1]] && sources[unescapeMountField(fields[i+2])] {
					return true
				}
				break
			}
		}
	}
	return false
}

func readCappedProc(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return data, nil
}

// unescapeMountField undoes the octal escapes the kernel writes for space,
// tab, newline and backslash in mount table fields.
func unescapeMountField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
