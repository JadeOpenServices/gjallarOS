package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"
	"unicode"

	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
	"github.com/JadeOpenServices/gjallarOS/internal/winicon"
)

// Each app's menu entry shows the app's own icon (Plane 290): an .ico file
// beside the program with the program's name, else the program's own icon,
// else Wine's. Both files come from the app, so they are read in a sandbox
// that has no files, no network and no other process, and that hands back
// only pixels. This side checks the pixels and writes the PNG files itself.

// iconName is the name of app id's icon in the icon theme.
func iconName(id string) string { return "gjallar-wine-" + id }

// iconFile is app id's icon of size s.
func iconFile(home, id string, s int) string {
	return filepath.Join(dataDir(home), "icons", "hicolor", fmt.Sprintf("%dx%d", s, s), "apps", iconName(id)+".png")
}

// iconTimeout bounds one icon read.
const iconTimeout = 20 * time.Second

// iconOutMax bounds what the sandbox may send back: one image per size.
var iconOutMax = func() int {
	n := 0
	for _, s := range winicon.Sizes {
		n += 2 + 4*s*s
	}
	return n
}()

// readIcon reads the icon in src, an .ico file if kind is "ico" or a
// program if "exe". Tests replace it.
var readIcon = sandboxedIcon

// setIcon writes app id's icon files and gives the icon name for its menu
// entry: its own, or "wine" if it has none. Files of sizes the new icon
// does not have are removed. An app without an icon gets no error.
func setIcon(paths winesandbox.Paths, id string, a winesandbox.App) (string, error) {
	imgs, err := appIcon(paths, a)
	if err != nil {
		removeIcons(paths.Home, id)
		if errors.Is(err, winicon.ErrNoIcon) {
			return "wine", nil
		}
		return "wine", err
	}
	for _, s := range winicon.Sizes {
		img, ok := imgs[s]
		if !ok {
			removeIcon(iconFile(paths.Home, id, s))
			continue
		}
		if err := writePNG(iconFile(paths.Home, id, s), img); err != nil {
			removeIcons(paths.Home, id)
			return "wine", err
		}
	}
	return iconName(id), nil
}

// appIcon finds and reads app a's icon. The program's folder and the files
// in it are opened below the drive's root without following any symbolic
// link, so the app cannot point the read at a file outside it.
func appIcon(paths winesandbox.Paths, a winesandbox.App) (map[int]*image.NRGBA, error) {
	if a.Exe == "" {
		return nil, winicon.ErrNoIcon
	}
	host, err := winesandbox.HostPath(paths, a.Exe)
	if err != nil {
		return nil, err
	}
	root := paths.Files
	if a.Exe[0] == 'c' || a.Exe[0] == 'C' {
		root = filepath.Join(paths.Prefix, "drive_c")
	}
	if err := winesandbox.RealDir(root); err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, host)
	if err != nil {
		return nil, err
	}
	rootFD, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", root, err)
	}
	defer unix.Close(rootFD)
	dirFD, err := openBeneath(rootFD, filepath.Dir(rel), unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return nil, winicon.ErrNoIcon
	}
	dir := os.NewFile(uintptr(dirFD), filepath.Dir(host))
	defer dir.Close()

	exe := filepath.Base(rel)
	if ico := besideICO(dir, exe); ico != "" {
		if imgs, err := readFrom(dirFD, ico, "ico"); err == nil {
			return imgs, nil
		}
	}
	return readFrom(dirFD, exe, "exe")
}

// openBeneath opens name below dirFD, refusing symbolic links on the way
// and paths that leave dirFD.
func openBeneath(dirFD int, name string, flags int) (int, error) {
	return unix.Openat2(dirFD, name, &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC | unix.O_NOCTTY),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
	})
}

// iconKey is a file name's stem, lowercase, with only letters and digits:
// lobu_online.exe and LobuOnline.ico both give lobuonline.
func iconKey(name string) string {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	var b strings.Builder
	for _, r := range strings.ToLower(stem) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// maxDirNames bounds the names read from the program's folder.
const maxDirNames = 4096

// besideICO is the .ico file in dir with the program's name, or "".
func besideICO(dir *os.File, exe string) string {
	key := iconKey(exe)
	if key == "" {
		return ""
	}
	names, _ := dir.Readdirnames(maxDirNames)
	slices.Sort(names)
	for _, name := range names {
		if strings.EqualFold(filepath.Ext(name), ".ico") && iconKey(name) == key {
			return name
		}
	}
	return ""
}

// readFrom opens name in dirFD as a plain file and reads its icon.
func readFrom(dirFD int, name, kind string) (map[int]*image.NRGBA, error) {
	fd, err := openBeneath(dirFD, name, unix.O_RDONLY|unix.O_NONBLOCK)
	if err != nil {
		return nil, winicon.ErrNoIcon
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, winicon.ErrNoIcon
	}
	return readIcon(kind, f)
}

// sandboxedIcon runs "gjallar-wine icon KIND" in bubblewrap with src as
// its fd 3. The sandbox holds only this binary: no files, no devices, no
// network, its own user, process and IPC namespaces, no capabilities, an
// empty environment, and it ends with this process. It must answer within
// iconTimeout and send no more than iconOutMax bytes.
func sandboxedIcon(kind string, src *os.File) (map[int]*image.NRGBA, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(self, "/nix/store/") {
		return nil, fmt.Errorf("%s is not in the Nix store; start the installed gjallar-wine", self)
	}
	ctx, cancel := context.WithTimeout(context.Background(), iconTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bwrapPath,
		"--unshare-all", "--die-with-parent", "--new-session", "--cap-drop", "ALL", "--clearenv",
		"--ro-bind", self, self,
		"--", self, "icon", kind)
	cmd.ExtraFiles = []*os.File{src}
	out := &capped{max: iconOutMax}
	errOut := &capped{max: 4 << 10}
	cmd.Stdout, cmd.Stderr = out, errOut
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() == iconNone:
		return nil, winicon.ErrNoIcon
	case err != nil:
		return nil, fmt.Errorf("icon reader: %w: %s", err, strings.TrimSpace(errOut.buf.String()))
	}
	return parseIconOut(out.buf.Bytes())
}

// capped keeps up to max bytes and fails once more come.
type capped struct {
	buf bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.max {
		return 0, errors.New("icon reader sent too much")
	}
	return c.buf.Write(p)
}

// parseIconOut reads the sandbox's answer: for each size, in rising order
// and each once, the size as two bytes and then its pixels, four bytes
// each, row by row.
func parseIconOut(data []byte) (map[int]*image.NRGBA, error) {
	imgs := map[int]*image.NRGBA{}
	last := 0
	for len(data) > 0 {
		if len(data) < 2 {
			return nil, errors.New("icon reader sent a broken answer")
		}
		s := int(binary.BigEndian.Uint16(data))
		if !slices.Contains(winicon.Sizes, s) || s <= last || len(data) < 2+4*s*s {
			return nil, errors.New("icon reader sent a broken answer")
		}
		img := image.NewNRGBA(image.Rect(0, 0, s, s))
		copy(img.Pix, data[2:2+4*s*s])
		imgs[s] = img
		data = data[2+4*s*s:]
		last = s
	}
	if len(imgs) == 0 {
		return nil, winicon.ErrNoIcon
	}
	return imgs, nil
}

// iconMem is the icon reader's memory limit.
const iconMem = 512 << 20

// iconNone is the icon reader's exit code for a file without an icon.
const iconNone = 3

// iconCmd is the icon reader inside the sandbox: it reads fd 3 and writes
// the answer parseIconOut reads.
func iconCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || (args[0] != "ico" && args[0] != "exe") {
		fmt.Fprintln(stderr, "usage: gjallar-wine icon ico|exe, with the file as fd 3")
		return 2
	}
	// Memory for the decoder, well above what the limits in winicon allow.
	// RLIMIT_DATA counts the memory the Go runtime maps for use; it leaves
	// out the address space the runtime only reserves, which RLIMIT_AS
	// counts, so that limit stopped the runtime growing its heap at all.
	limit := &unix.Rlimit{Cur: iconMem, Max: iconMem}
	if err := unix.Setrlimit(unix.RLIMIT_DATA, limit); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	debug.SetMemoryLimit(iconMem / 2)
	imgs, err := decodeIcon(args[0], os.NewFile(3, "icon source"))
	if errors.Is(err, winicon.ErrNoIcon) {
		return iconNone
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := stdout.Write(iconOut(imgs)); err != nil {
		return 1
	}
	return 0
}

// decodeIcon reads the icon in src, an .ico file or a program.
func decodeIcon(kind string, src *os.File) (map[int]*image.NRGBA, error) {
	info, err := src.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("the icon source is not a plain file")
	}
	var entries []winicon.Entry
	if kind == "ico" {
		var data []byte
		if data, err = winicon.ReadAll(src, winicon.MaxICOBytes); err == nil {
			entries, err = winicon.ParseICO(data)
		}
	} else {
		entries, err = winicon.ProgramIcon(src, info.Size())
	}
	if err != nil {
		return nil, err
	}
	return winicon.Pick(entries)
}

// iconOut is the answer parseIconOut reads.
func iconOut(imgs map[int]*image.NRGBA) []byte {
	var out bytes.Buffer
	for _, s := range winicon.Sizes {
		img, ok := imgs[s]
		if !ok || img.Rect.Dx() != s || img.Rect.Dy() != s {
			continue
		}
		out.Write(binary.BigEndian.AppendUint16(nil, uint16(s)))
		for y := range s {
			out.Write(img.Pix[y*img.Stride : y*img.Stride+4*s])
		}
	}
	return out.Bytes()
}

// writePNG writes img to path in one step.
func writePNG(path string, img *image.NRGBA) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".gjallar-wine-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := png.Encode(tmp, img); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// removeIcon removes path if it is a plain file.
func removeIcon(path string) {
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
		os.Remove(path)
	}
}

// removeIcons removes all of app id's icon files.
func removeIcons(home, id string) {
	for _, s := range winicon.Sizes {
		removeIcon(iconFile(home, id, s))
	}
}

// iconIDs lists the app ids that have icon files.
func iconIDs(home string) []string {
	var ids []string
	for _, s := range winicon.Sizes {
		entries, _ := os.ReadDir(filepath.Dir(iconFile(home, "x", s)))
		for _, entry := range entries {
			rest, ok := strings.CutPrefix(entry.Name(), "gjallar-wine-")
			id, ok2 := strings.CutSuffix(rest, ".png")
			if ok && ok2 && winesandbox.ValidID(id) && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids
}
