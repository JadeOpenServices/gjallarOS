package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/godbus/dbus/v5"
)

const (
	udisksName   = "org.freedesktop.UDisks2"
	udisksRoot   = dbus.ObjectPath("/org/freedesktop/UDisks2")
	udisksBlocks = dbus.ObjectPath("/org/freedesktop/UDisks2/block_devices")
	udisksBlock  = udisksName + ".Block"
	udisksFS     = udisksName + ".Filesystem"
	// mediaAuthWait is how long a remount may take, a password prompt for a
	// built-in disk included.
	mediaAuthWait = 2 * time.Minute
)

// mediaForeignTypes are the file systems system/services/foreign-media-
// readonly.nix opens read-only (Plane 286, Q181).
var mediaForeignTypes = map[string]bool{
	"vfat":    true,
	"exfat":   true,
	"ntfs":    true,
	"hfsplus": true,
}

var mediaShowNotice = showNotice

// runMedia is `gjallarctl media`.
func runMedia(args []string, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "notice" {
		fmt.Fprintln(stderr, "Usage: gjallarctl media notice")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mediaWatch(ctx, stderr); err != nil && ctx.Err() == nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	return 0
}

// mediaWatch tells the user when a disk from another system was opened
// read-only and offers "Allow writing". The file manager cannot show why a
// disk is read-only, so the reason lives in this notice. It only acts on a
// mount the user's own session made under /run/media/USER, and only through
// udisks with the user's own rights: no new privilege.
//
// Mounts that exist when the watch starts got their notice from the run
// before; only new mounts get one.
func mediaWatch(ctx context.Context, stderr io.Writer) error {
	me, err := user.Current()
	if err != nil {
		return err
	}
	mediaDir := filepath.Join("/run/media", me.Username)

	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	signals := make(chan *dbus.Signal, 64)
	conn.Signal(signals)
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.DBus.Properties"),
		dbus.WithMatchMember("PropertiesChanged"),
		dbus.WithMatchPathNamespace(udisksBlocks),
		dbus.WithMatchArg(0, udisksFS),
	); err != nil {
		return err
	}
	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(udisksRoot),
		dbus.WithMatchInterface("org.freedesktop.DBus.ObjectManager"),
		dbus.WithMatchMember("InterfacesRemoved"),
	); err != nil {
		return err
	}
	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(udisksRoot),
		dbus.WithMatchInterface("org.freedesktop.DBus.ObjectManager"),
		dbus.WithMatchMember("InterfacesAdded"),
	); err != nil {
		return err
	}
	if err := conn.AddMatchSignal(
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, udisksName),
	); err != nil {
		return err
	}

	// Only udisks itself may report mounts; any client can emit a signal
	// with the same path and interface.
	var owner string
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", 0, udisksName).Store(&owner); err != nil {
		owner = ""
	}
	mounted := map[dbus.ObjectPath]bool{}
	if owner != "" {
		var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
		if err := conn.Object(udisksName, udisksRoot).CallWithContext(ctx, "org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objects); err != nil {
			return err
		}
		for path, ifaces := range objects {
			if fs, ok := ifaces[udisksFS]; ok {
				mounted[path] = len(mediaMountPoints(fs["MountPoints"])) > 0
			}
		}
	}

	cards := &mediaCards{open: map[dbus.ObjectPath]*mediaCard{}}
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cards.closeAll()

	for {
		select {
		case <-ctx.Done():
			return nil
		case s, ok := <-signals:
			if !ok {
				return errors.New("system bus closed")
			}
			if s.Name == "org.freedesktop.DBus.NameOwnerChanged" {
				if len(s.Body) == 3 {
					owner, _ = s.Body[2].(string)
				}
				// A new udisks announces what it finds with InterfacesAdded.
				clear(mounted)
				cards.closeAll()
				continue
			}
			if owner == "" || s.Sender != owner {
				continue
			}
			switch s.Name {
			case "org.freedesktop.DBus.ObjectManager.InterfacesAdded":
				if len(s.Body) != 2 {
					continue
				}
				path, ok := s.Body[0].(dbus.ObjectPath)
				ifaces, ok2 := s.Body[1].(map[string]map[string]dbus.Variant)
				if !ok || !ok2 {
					continue
				}
				if fs, ok := ifaces[udisksFS]; ok {
					mounted[path] = len(mediaMountPoints(fs["MountPoints"])) > 0
				}
			case "org.freedesktop.DBus.ObjectManager.InterfacesRemoved":
				if len(s.Body) == 2 {
					if path, ok := s.Body[0].(dbus.ObjectPath); ok {
						delete(mounted, path)
						cards.close(path, nil)
					}
				}
			case "org.freedesktop.DBus.Properties.PropertiesChanged":
				if len(s.Body) != 3 {
					continue
				}
				changed, ok := s.Body[1].(map[string]dbus.Variant)
				if !ok {
					continue
				}
				points, ok := changed["MountPoints"]
				if !ok {
					continue
				}
				now := mediaMountPoints(points)
				was := mounted[s.Path]
				mounted[s.Path] = len(now) > 0
				// The card's own remount unmounts and mounts again; that
				// neither closes it nor opens a second one.
				if cards.remounting(s.Path) {
					continue
				}
				if len(now) == 0 {
					cards.close(s.Path, nil)
					continue
				}
				if was {
					continue
				}
				d, ok := mediaReadDisk(ctx, conn, s.Path)
				if !ok || !mediaOffer(d, now, mediaDir, readMountInfo()) {
					continue
				}
				d.MountPoint = mediaPointUnder(now, mediaDir)
				card := cards.start(ctx, s.Path)
				wg.Add(1)
				go func(path dbus.ObjectPath, d mediaDisk) {
					defer wg.Done()
					mediaOfferWriting(card, conn, path, d, stderr)
					cards.close(path, card)
				}(s.Path, d)
			}
		}
	}
}

// mediaCard is one open "opened read-only" notice.
type mediaCard struct {
	ctx    context.Context
	cancel context.CancelFunc
	cards  *mediaCards
	busy   bool
}

// setRemounting marks the card while it unmounts and mounts its disk again.
// Once the disk was unmounted the mark stays until the card ends: the mount
// signals can reach the watch after the calls returned.
func (c *mediaCard) setRemounting(on bool) {
	c.cards.mu.Lock()
	defer c.cards.mu.Unlock()
	c.busy = on
}

type mediaCards struct {
	mu   sync.Mutex
	open map[dbus.ObjectPath]*mediaCard
}

func (m *mediaCards) start(ctx context.Context, path dbus.ObjectPath) *mediaCard {
	m.close(path, nil)
	c := &mediaCard{cards: m}
	c.ctx, c.cancel = context.WithCancel(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.open[path] = c
	return c
}

// close ends the card for path. With only set it ends that card and no
// newer one.
func (m *mediaCards) close(path dbus.ObjectPath, only *mediaCard) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.open[path]
	if !ok || (only != nil && c != only) {
		return
	}
	c.cancel()
	delete(m.open, path)
}

func (m *mediaCards) remounting(path dbus.ObjectPath) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.open[path]
	return ok && c.busy
}

func (m *mediaCards) closeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for path, c := range m.open {
		c.cancel()
		delete(m.open, path)
	}
}

// mediaDisk is what the watch knows about one file system.
type mediaDisk struct {
	Type, Label, UUID string
	Size              uint64
	ReadOnly          bool
	MountPoint        string
}

func mediaReadDisk(ctx context.Context, conn *dbus.Conn, path dbus.ObjectPath) (mediaDisk, bool) {
	var props map[string]dbus.Variant
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := conn.Object(udisksName, path).CallWithContext(callCtx, "org.freedesktop.DBus.Properties.GetAll", 0, udisksBlock).Store(&props); err != nil {
		return mediaDisk{}, false
	}
	var d mediaDisk
	d.Type, _ = props["IdType"].Value().(string)
	d.Label, _ = props["IdLabel"].Value().(string)
	d.UUID, _ = props["IdUUID"].Value().(string)
	d.Size, _ = props["Size"].Value().(uint64)
	d.ReadOnly, _ = props["ReadOnly"].Value().(bool)
	return d, true
}

// mediaOffer says whether a new mount gets the notice: a file system from
// another system, opened by this user, mounted read-only, on a device that
// can be written at all.
func mediaOffer(d mediaDisk, points []string, mediaDir string, mountInfo string) bool {
	if !mediaForeignTypes[d.Type] || d.ReadOnly {
		return false
	}
	point := mediaPointUnder(points, mediaDir)
	return point != "" && mediaMountReadOnly(mountInfo, point)
}

// mediaMountPoints turns the udisks MountPoints value (aay, each with a
// trailing NUL) into paths.
func mediaMountPoints(v dbus.Variant) []string {
	raw, ok := v.Value().([][]byte)
	if !ok {
		return nil
	}
	var out []string
	for _, p := range raw {
		s := strings.TrimRight(string(p), "\x00")
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// mediaPointUnder returns the mount point directly under mediaDir, the place
// udisks mounts for this user.
func mediaPointUnder(points []string, mediaDir string) string {
	for _, p := range points {
		if filepath.Dir(filepath.Clean(p)) == mediaDir {
			return p
		}
	}
	return ""
}

func readMountInfo() string {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	return string(data)
}

// mediaMountReadOnly reads the per-mount options of point from
// /proc/self/mountinfo text. The last line for a point wins, as a later
// mount covers an earlier one.
func mediaMountReadOnly(mountInfo, point string) bool {
	ro, found := false, false
	sc := bufio.NewScanner(strings.NewReader(mountInfo))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 6 || mediaUnescape(f[4]) != point {
			continue
		}
		found = true
		ro = false
		for _, o := range strings.Split(f[5], ",") {
			if o == "ro" {
				ro = true
			}
		}
	}
	return found && ro
}

// mediaUnescape undoes the octal escapes mountinfo uses for space, tab,
// newline and backslash.
func mediaUnescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

// mediaName is the disk name for the notice: its label, cleaned of control
// characters and kept short, as anyone who formats a stick picks it.
func mediaName(label string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(label))
	if clean == "" {
		return "Disk"
	}
	if r := []rune(clean); len(r) > 32 {
		clean = string(r[:32]) + "…"
	}
	return clean
}

// mediaOfferWriting shows the notice for one read-only mount and, when the
// user picks "Allow writing", mounts it again writable.
func mediaOfferWriting(card *mediaCard, conn *dbus.Conn, path dbus.ObjectPath, d mediaDisk, stderr io.Writer) {
	ctx := card.ctx
	name := html.EscapeString(mediaName(d.Label))
	notice := desktopNotice{
		App:     "GjallarOS",
		Icon:    "drive-removable-media",
		Summary: name + " opened read-only",
		Body: "Its file system comes from another system, so gjallarOS does not change anything on it. " +
			"Allow writing only if you want to save files on it. " +
			"If the other system went to sleep on it or did not shut down fully, shut it down there first.",
		Actions: []noticeAction{{"allow", "Allow writing"}, {"keep", "Keep read-only"}},
	}
	for {
		choice, err := mediaShowNotice(ctx, notice)
		if ctx.Err() != nil || err != nil || choice != "allow" {
			if err != nil && ctx.Err() == nil {
				fmt.Fprintf(stderr, "media notice: %v\n", err)
			}
			return
		}
		// The card may have been open for a while: act only on the same
		// file system, still mounted read-only in the same place.
		now, ok := mediaReadDisk(ctx, conn, path)
		if !ok || now.Type != d.Type || now.UUID != d.UUID || now.Size != d.Size || now.Label != d.Label {
			return
		}
		if !mediaMountReadOnly(readMountInfo(), d.MountPoint) {
			return
		}
		card.setRemounting(true)
		next, retry := mediaRemount(ctx, conn, path, name, stderr)
		if !retry {
			if next.Summary != "" {
				mediaShowNotice(ctx, next)
			}
			return
		}
		// In use: nothing changed on the disk.
		card.setRemounting(false)
		notice = next
	}
}

// mediaRemount unmounts the file system and mounts it again writable. It
// returns the notice to show next; retry says whether that notice offers
// "Allow writing" again.
func mediaRemount(ctx context.Context, conn *dbus.Conn, path dbus.ObjectPath, name string, stderr io.Writer) (desktopNotice, bool) {
	fs := conn.Object(udisksName, path)
	call := func(method string, options map[string]dbus.Variant) error {
		callCtx, cancel := context.WithTimeout(ctx, mediaAuthWait)
		defer cancel()
		return fs.CallWithContext(callCtx, udisksFS+"."+method, dbus.FlagAllowInteractiveAuthorization, options).Err
	}
	if err := call("Unmount", map[string]dbus.Variant{}); err != nil {
		fmt.Fprintf(stderr, "media: unmount %s: %v\n", path, err)
		if mediaBusy(err) {
			return desktopNotice{
				App:     "GjallarOS",
				Icon:    "drive-removable-media",
				Summary: name + " is in use",
				Body:    "It is still open read-only. Close the files and windows that use it, then try again.",
				Actions: []noticeAction{{"allow", "Try again"}, {"keep", "Keep read-only"}},
			}, true
		}
		return mediaFailed(name, "It is still open read-only."), false
	}
	if err := call("Mount", map[string]dbus.Variant{"options": dbus.MakeVariant("rw")}); err == nil {
		return desktopNotice{}, false
	} else {
		fmt.Fprintf(stderr, "media: mount %s rw: %v\n", path, err)
	}
	// Writable did not work: open it read-only again, as it was.
	if err := call("Mount", map[string]dbus.Variant{}); err != nil {
		fmt.Fprintf(stderr, "media: mount %s again: %v\n", path, err)
		return mediaFailed(name, "It could not be opened again. Unplug it and plug it in again."), false
	}
	return desktopNotice{
		App:     "GjallarOS",
		Icon:    "drive-removable-media",
		Summary: "Writing to " + name + " not turned on",
		Body: "It is open read-only again. Most often the other system went to sleep on it or did not shut down fully, " +
			"or its file system has errors. Shut it down fully there, or check the disk there, then plug it in again.",
		Actions: []noticeAction{{"ok", "OK"}},
	}, false
}

func mediaFailed(name, body string) desktopNotice {
	return desktopNotice{
		App:     "GjallarOS",
		Icon:    "drive-removable-media",
		Summary: "Writing to " + name + " not turned on",
		Body:    body,
		Actions: []noticeAction{{"ok", "OK"}},
	}
}

// mediaBusy says whether udisks refused the unmount because files are open.
func mediaBusy(err error) bool {
	var e dbus.Error
	if errors.As(err, &e) && e.Name == udisksName+".Error.DeviceBusy" {
		return true
	}
	return strings.Contains(err.Error(), "target is busy")
}
