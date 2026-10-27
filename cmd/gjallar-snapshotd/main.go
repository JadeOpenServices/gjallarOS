// Command gjallar-snapshotd lets users restore their own files from the
// hourly /home snapshots (ROADMAP section 15, step 3). It owns
// org.gjallaros.Snapshots1 on the system bus, mounts @snapshots read-only
// in its own mount namespace, asks the caller's password for every
// restore, and hands back a read-only descriptor of the old file. It never
// writes: gjallarctl makes the copy as the user.
//
// It is apart from gjallar-settingsd so that service keeps no
// capabilities; this one needs CAP_SYS_ADMIN for the mount and
// CAP_DAC_READ_SEARCH to read the snapshots.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/snapshots"
)

const (
	// passwordTimeout is how long a password dialog may stay open.
	passwordTimeout = 5 * time.Minute
	// idle is how long the service waits for another call; D-Bus starts
	// it again when one comes.
	idle = 2 * time.Minute
)

const introspection = `<node>
  <interface name="` + snapshots.Interface + `">
    <method name="List"><arg name="snapshots" type="s" direction="out"/></method>
    <method name="Restore">
      <arg name="path" type="s" direction="in"/>
      <arg name="from" type="s" direction="in"/>
      <arg name="file" type="h" direction="out"/>
      <arg name="snapshot" type="s" direction="out"/>
    </method>
  </interface>` + introspect.IntrospectDataString + `</node>`

func main() {
	log.SetFlags(0)
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "Usage: gjallar-snapshotd DEVICE MOUNTPOINT (started by D-Bus)")
		os.Exit(2)
	}
	if os.Geteuid() != 0 {
		log.Fatal("gjallar-snapshotd runs as root")
	}
	device, root := os.Args[1], os.Args[2]
	// The unit's own mount namespace (PrivateMounts) keeps this mount out
	// of everyone else's sight.
	if err := os.MkdirAll(root, 0o700); err != nil {
		log.Fatal(err)
	}
	if err := unix.Mount(device, root, "btrfs", unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "subvol=@snapshots"); err != nil {
		log.Fatalf("mount @snapshots: %v", err)
	}

	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		log.Fatalf("connect to the system bus: %v", err)
	}
	defer conn.Close()
	if !conn.SupportsUnixFDs() {
		log.Fatal("the system bus does not pass file descriptors")
	}

	svc := &service{sys: busSystem{conn}, root: root, home: lookupHome}
	obj := &object{svc: svc}
	obj.timer = time.AfterFunc(idle, obj.exitIfIdle)
	if err := conn.Export(obj, snapshots.ObjectPath, snapshots.Interface); err != nil {
		log.Fatal(err)
	}
	if err := conn.Export(introspect.Introspectable(introspection), snapshots.ObjectPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		log.Fatal(err)
	}
	reply, err := conn.RequestName(snapshots.BusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		log.Fatalf("request %s: %v", snapshots.BusName, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		log.Fatalf("%s is already owned", snapshots.BusName)
	}
	select {}
}

type busSystem struct{ conn *dbus.Conn }

func (b busSystem) UID(ctx context.Context, sender string) (int, error) {
	var uid uint32
	if err := b.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetConnectionUnixUser", 0, sender).Store(&uid); err != nil {
		return 0, fmt.Errorf("identify caller: %w", err)
	}
	return int(uid), nil
}

func (b busSystem) Authorize(ctx context.Context, sender, message string) error {
	subject := struct {
		Kind    string
		Details map[string]dbus.Variant
	}{"system-bus-name", map[string]dbus.Variant{"name": dbus.MakeVariant(sender)}}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	cancelID := "gjallar-snapshots-" + hex.EncodeToString(nonce)
	authority := b.conn.Object("org.freedesktop.PolicyKit1", "/org/freedesktop/PolicyKit1/Authority")

	ctx, cancel := context.WithTimeout(ctx, passwordTimeout)
	defer cancel()
	var result struct {
		Authorized bool
		Challenge  bool
		Details    map[string]string
	}
	const allowUserInteraction = uint32(1)
	err := authority.CallWithContext(ctx, "org.freedesktop.PolicyKit1.Authority.CheckAuthorization", 0,
		subject, snapshots.RestoreAction, map[string]string{"polkit.message": message}, allowUserInteraction, cancelID).Store(&result)
	if ctx.Err() != nil {
		authority.Call("org.freedesktop.PolicyKit1.Authority.CancelCheckAuthorization", 0, cancelID)
		return fmt.Errorf("%w: the password was not given in time", errDenied)
	}
	if err != nil {
		return fmt.Errorf("%w: %v", errDenied, err)
	}
	if !result.Authorized {
		return errDenied
	}
	return nil
}

// object is what the bus sees. Every call puts off the idle exit.
type object struct {
	svc   *service
	mu    sync.Mutex
	busy  int
	timer *time.Timer
	// held are the files handed out (Restore).
	held []*os.File
}

func (o *object) begin() {
	o.mu.Lock()
	o.busy++
	o.timer.Stop()
	o.mu.Unlock()
}

// exitIfIdle ends the service unless a call came in meanwhile. The mount
// goes with the mount namespace.
func (o *object) exitIfIdle() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.busy == 0 {
		os.Exit(0)
	}
}

func (o *object) end() {
	o.mu.Lock()
	o.busy--
	if o.busy == 0 {
		o.timer.Reset(idle)
	}
	o.mu.Unlock()
}

func (o *object) List() (string, *dbus.Error) {
	o.begin()
	defer o.end()
	names, err := o.svc.List()
	if err != nil {
		return "", busError(err)
	}
	if names == nil {
		names = []string{}
	}
	data, err := json.Marshal(names)
	if err != nil {
		return "", busError(err)
	}
	return string(data), nil
}

func (o *object) Restore(sender dbus.Sender, path, from string) (dbus.UnixFD, string, *dbus.Error) {
	o.begin()
	defer o.end()
	file, name, err := o.svc.Restore(context.Background(), string(sender), filepath.Clean(path), from)
	if err != nil {
		return -1, "", busError(err)
	}
	// godbus sends the descriptor after this returns, and nothing says
	// when. Closing it on a timer could let a later file take its number
	// before the reply is out, so it stays open until the service exits.
	o.mu.Lock()
	o.held = append(o.held, file)
	o.mu.Unlock()
	return dbus.UnixFD(file.Fd()), name, nil
}

func busError(err error) *dbus.Error {
	switch {
	case errors.Is(err, errDenied):
		return dbus.NewError(snapshots.Interface+".Error.NotAuthorized", []any{err.Error()})
	case errors.Is(err, snapshots.ErrRefused):
		return dbus.NewError(snapshots.Interface+".Error.Refused", []any{err.Error()})
	case errors.Is(err, snapshots.ErrNotFound):
		return dbus.NewError(snapshots.Interface+".Error.NotFound", []any{err.Error()})
	default:
		return dbus.NewError(snapshots.Interface+".Error.Failed", []any{err.Error()})
	}
}
