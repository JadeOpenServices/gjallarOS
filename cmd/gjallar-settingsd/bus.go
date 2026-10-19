package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/JadeOpenServices/gjallarOS/internal/nettrust"
)

const (
	busName       = "org.gjallaros.Settings1"
	objectPath    = dbus.ObjectPath("/org/gjallaros/Settings1")
	interfaceName = "org.gjallaros.Settings1"

	rebuildUnit = "gjallar-settings-rebuild.service"
	rebuildEnv  = "/run/gjallar/settings/rebuild.env"

	// passwordTimeout is how long a password dialog may stay open.
	passwordTimeout = 5 * time.Minute
	// verdictMaxAge is older than the trust timer's five minutes, so a
	// verdict that stopped being renewed no longer counts as home.
	verdictMaxAge = 15 * time.Minute
)

var vpnTrustPath = "/run/gjallar/vpn-trust.json"

type busSystem struct{ conn *dbus.Conn }

func (b busSystem) Caller(ctx context.Context, sender string) (caller, error) {
	bus := b.conn.BusObject()
	var uid, pid uint32
	if err := bus.CallWithContext(ctx, "org.freedesktop.DBus.GetConnectionUnixUser", 0, sender).Store(&uid); err != nil {
		return caller{}, fmt.Errorf("identify caller: %w", err)
	}
	if err := bus.CallWithContext(ctx, "org.freedesktop.DBus.GetConnectionUnixProcessID", 0, sender).Store(&pid); err != nil {
		return caller{}, fmt.Errorf("identify caller: %w", err)
	}
	path, err := b.session(ctx, uid, pid)
	if err != nil {
		return caller{}, err
	}
	session := b.conn.Object("org.freedesktop.login1", path)
	id, err := session.GetProperty("org.freedesktop.login1.Session.Id")
	if err != nil {
		return caller{}, fmt.Errorf("read login session: %w", err)
	}
	user, err := session.GetProperty("org.freedesktop.login1.Session.User")
	if err != nil {
		return caller{}, fmt.Errorf("read login session: %w", err)
	}
	var owner struct {
		UID  uint32
		Path dbus.ObjectPath
	}
	if err := dbus.Store([]any{user.Value()}, &owner); err != nil || owner.UID != uid {
		return caller{}, fmt.Errorf("%w: the caller's process is not in its own login session", errRefused)
	}
	// The process id is checked last: if the connection is gone, its id
	// may already belong to another process.
	var again uint32
	if err := bus.CallWithContext(ctx, "org.freedesktop.DBus.GetConnectionUnixProcessID", 0, sender).Store(&again); err != nil || again != pid {
		return caller{}, fmt.Errorf("%w: the caller went away", errRefused)
	}
	name, ok := id.Value().(string)
	if !ok || name == "" {
		return caller{}, fmt.Errorf("read login session: no id")
	}
	return caller{UID: uid, Session: name}, nil
}

// session finds the login session of a process. Apps the desktop starts as
// user units run outside the session scope; like polkit, those count for
// the user's display session.
func (b busSystem) session(ctx context.Context, uid, pid uint32) (dbus.ObjectPath, error) {
	login := b.conn.Object("org.freedesktop.login1", "/org/freedesktop/login1")
	var path dbus.ObjectPath
	if err := login.CallWithContext(ctx, "org.freedesktop.login1.Manager.GetSessionByPID", 0, pid).Store(&path); err == nil {
		return path, nil
	}
	var user dbus.ObjectPath
	if err := login.CallWithContext(ctx, "org.freedesktop.login1.Manager.GetUser", 0, uid).Store(&user); err != nil {
		return "", fmt.Errorf("%w: the caller is not in a login session", errRefused)
	}
	display, err := b.conn.Object("org.freedesktop.login1", user).GetProperty("org.freedesktop.login1.User.Display")
	if err != nil {
		return "", fmt.Errorf("read login user: %w", err)
	}
	var value struct {
		ID   string
		Path dbus.ObjectPath
	}
	if err := dbus.Store([]any{display.Value()}, &value); err != nil || value.ID == "" {
		return "", fmt.Errorf("%w: the caller is not in a login session", errRefused)
	}
	return value.Path, nil
}

func (b busSystem) Authorize(ctx context.Context, sender, action string, details map[string]string) error {
	subject := struct {
		Kind    string
		Details map[string]dbus.Variant
	}{"system-bus-name", map[string]dbus.Variant{"name": dbus.MakeVariant(sender)}}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	cancelID := "gjallar-settings-" + hex.EncodeToString(nonce)
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
		subject, action, details, allowUserInteraction, cancelID).Store(&result)
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

// Home reads root's own network verdict. Only a fresh verdict in a file
// root wrote counts; anything else is not home.
func (busSystem) Home() bool {
	return readHome(vpnTrustPath, time.Now())
}

func readHome(path string, now time.Time) bool {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return false
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != 0 {
		return false
	}
	if now.Sub(info.ModTime()) > verdictMaxAge {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(file, 64<<10))
	if err != nil {
		return false
	}
	var verdict nettrust.Verdict
	if err := json.Unmarshal(data, &verdict); err != nil {
		return false
	}
	return verdict.Trust == nettrust.TrustHome
}

func (b busSystem) unit(ctx context.Context) (dbus.BusObject, error) {
	manager := b.conn.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1")
	var path dbus.ObjectPath
	if err := manager.CallWithContext(ctx, "org.freedesktop.systemd1.Manager.LoadUnit", 0, rebuildUnit).Store(&path); err != nil {
		return nil, fmt.Errorf("load %s: %w", rebuildUnit, err)
	}
	return b.conn.Object("org.freedesktop.systemd1", path), nil
}

func (b busSystem) Rebuilding(ctx context.Context) (bool, error) {
	unit, err := b.unit(ctx)
	if err != nil {
		return false, err
	}
	state, err := unit.GetProperty("org.freedesktop.systemd1.Unit.ActiveState")
	if err != nil {
		return false, fmt.Errorf("read %s state: %w", rebuildUnit, err)
	}
	value, _ := state.Value().(string)
	return value == "activating" || value == "deactivating" || value == "reloading", nil
}

// StartRebuild hands the checked colors to the rebuild unit through its
// environment file and starts it. The colors are hex digits, = and , only,
// so the line needs no quoting.
func (b busSystem) StartRebuild(ctx context.Context, colors string) error {
	if err := os.MkdirAll("/run/gjallar/settings", 0o700); err != nil {
		return err
	}
	if err := writeRoot(rebuildEnv, []byte("GJALLAR_NOCTALIA_COLORS="+colors+"\n")); err != nil {
		return err
	}
	manager := b.conn.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1")
	var job dbus.ObjectPath
	return manager.CallWithContext(ctx, "org.freedesktop.systemd1.Manager.StartUnit", 0, rebuildUnit, "replace").Store(&job)
}

func writeRoot(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// object is what the bus sees. Each method answers with JSON text, so
// clients need no D-Bus struct types.
type object struct{ svc *service }

func (o object) List() (string, *dbus.Error) {
	list, err := o.svc.List()
	return answer(list, err)
}

func (o object) Get(key string) (string, *dbus.Error) {
	value, err := o.svc.Get(key)
	return answer(value, err)
}

func (o object) Request(sender dbus.Sender, values string) (string, *dbus.Error) {
	change, err := o.svc.Request(context.Background(), string(sender), values)
	return answer(change, err)
}

func (o object) Apply(sender dbus.Sender, id, colors string) *dbus.Error {
	return busError(o.svc.Apply(context.Background(), string(sender), id, colors))
}

func (o object) Pending(sender dbus.Sender) (string, *dbus.Error) {
	list, err := o.svc.Pending(context.Background(), string(sender))
	if list == nil {
		list = []pendingChange{}
	}
	return answer(list, err)
}

func (o object) Cancel(sender dbus.Sender, id string) *dbus.Error {
	return busError(o.svc.Cancel(context.Background(), string(sender), id))
}

func answer(value any, err error) (string, *dbus.Error) {
	if err != nil {
		return "", busError(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", busError(err)
	}
	return string(data), nil
}

func busError(err error) *dbus.Error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errDenied):
		return dbus.NewError(interfaceName+".Error.NotAuthorized", []any{err.Error()})
	case errors.Is(err, errRefused):
		return dbus.NewError(interfaceName+".Error.Refused", []any{err.Error()})
	default:
		return dbus.NewError(interfaceName+".Error.Failed", []any{err.Error()})
	}
}
