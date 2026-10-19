package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
)

// gjallarctl settings is the first front end of gjallar-settingsd (ROADMAP
// section 14, step 3.3). It only talks to the service; root checks every
// change, shows it in the password dialog and starts the rebuild.

const settingsUsage = `usage: gjallarctl settings list [--names]   every key with its tier and value
       gjallarctl settings get KEY
       gjallarctl settings set KEY=VALUE...    ask for a change; VALUE is JSON, or
                                               plain text for a text key
       gjallarctl settings pending [--ids]     changes that wait in this session
       gjallarctl settings apply [ID]          write a change and rebuild
       gjallarctl settings cancel [ID]
       gjallarctl settings export FILE         save the settings, signed with your key
       gjallarctl settings import FILE         ask for the change a saved file makes
ID may be the start of the id; without one, the only waiting change is used.
A saved file imports only on the device and account that made it.`

var (
	// settingsConnect opens the system bus; tests replace it.
	settingsConnect = func() (settingsBus, error) {
		conn, err := dbus.ConnectSystemBus()
		if err != nil {
			return nil, err
		}
		return systemSettingsBus{conn}, nil
	}
	settingsNow   = time.Now
	settingsSleep = func(ctx context.Context, d time.Duration) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	settingsPoll = 2 * time.Second
	settingsEUID = os.Geteuid
)

const (
	// settingsCallTimeout covers a password dialog, which the service keeps
	// open for five minutes.
	settingsCallTimeout = 6 * time.Minute
	// settingsReadingWait is the longest wait the command sits out itself
	// before it applies.
	settingsReadingWait = time.Minute
	// settingsShortID is how much of an id the lists show.
	settingsShortID = 8
)

var errSettingsMissing = errors.New("the settings service is not on this system; it comes with the system checkout (/etc/gjallar/system)")

// settingsBus is what the command needs from the system bus.
type settingsBus interface {
	// Call runs one method of the settings service and returns its JSON
	// answer, or "" for methods without one.
	Call(ctx context.Context, method string, args ...any) (string, error)
	// Export returns a signed settings file of the current settings.
	Export(ctx context.Context) ([]byte, error)
	// Rebuild reports whether the rebuild unit is queued or running, and
	// its last result.
	Rebuild(ctx context.Context) (running bool, result string, err error)
	Close() error
}

func runSettings(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stderr, settingsUsage)
		return 2
	}
	command, args := args[0], args[1:]
	if command == "list" && slices.Contains(args, "--names") {
		return runSettingsNames(args, stdout, stderr)
	}
	switch command {
	case "list", "get", "set", "pending", "apply", "cancel", "export", "import":
	default:
		fmt.Fprintf(stderr, "ERROR: unknown settings command %q\n%s\n", command, settingsUsage)
		return 2
	}
	if settingsEUID() == 0 {
		fmt.Fprintln(stderr, "ERROR: run gjallarctl settings as your own user; a change belongs to the login session that asked for it")
		return 1
	}
	bus, err := settingsConnect()
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: system bus: %v\n", err)
		return 1
	}
	defer bus.Close()

	ctx, cancel := context.WithTimeout(context.Background(), settingsCallTimeout)
	defer cancel()
	switch command {
	case "list":
		err = settingsList(ctx, bus, args, stdout)
	case "get":
		err = settingsGet(ctx, bus, args, stdout)
	case "set":
		return settingsSet(ctx, bus, args, stdout, stderr)
	case "pending":
		err = settingsPending(ctx, bus, args, stdout)
	case "apply":
		return settingsApplyCommand(ctx, bus, args, stdout, stderr)
	case "cancel":
		err = settingsCancel(ctx, bus, args, stdout)
	case "export":
		err = settingsExport(ctx, bus, args, stdout)
	case "import":
		return settingsImport(ctx, bus, args, stdout, stderr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	return 0
}

// runSettingsNames prints the keys set accepts, for shell completion. It
// needs no service.
func runSettingsNames(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, settingsUsage)
		return 2
	}
	tiers, err := config.Tiers()
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	var names []string
	for key, tier := range tiers {
		if tier != config.TierInstaller {
			names = append(names, key)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		fmt.Fprintln(stdout, name)
	}
	return 0
}

func settingsList(ctx context.Context, bus settingsBus, args []string, stdout io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("list takes no arguments")
	}
	answer, err := bus.Call(ctx, "List")
	if err != nil {
		return err
	}
	var keys []settings.KeyValue
	if err := json.Unmarshal([]byte(answer), &keys); err != nil {
		return fmt.Errorf("read the service's answer: %w", err)
	}
	table := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "KEY\tTIER\tVALUE")
	for _, key := range keys {
		value := "(not set)"
		if key.Value != nil {
			value = settings.Shown(key.Value, 60)
		}
		fmt.Fprintf(table, "%s\t%s\t%s\n", key.Key, key.Tier, value)
	}
	if err := table.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Keys of tier installer change only with a new install.")
	return nil
}

func settingsGet(ctx context.Context, bus settingsBus, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: gjallarctl settings get KEY")
	}
	key, err := settingsKey(ctx, bus, args[0])
	if err != nil {
		return err
	}
	if key.Value == nil {
		fmt.Fprintln(stdout, "(not set)")
		return nil
	}
	value := []byte(key.Value)
	if indented, err := json.MarshalIndent(key.Value, "", "  "); err == nil {
		value = indented
	}
	for _, line := range strings.Split(string(value), "\n") {
		fmt.Fprintln(stdout, settings.Shown(json.RawMessage(line), 0))
	}
	return nil
}

func settingsKey(ctx context.Context, bus settingsBus, name string) (settings.KeyValue, error) {
	answer, err := bus.Call(ctx, "Get", name)
	if err != nil {
		return settings.KeyValue{}, err
	}
	var key settings.KeyValue
	if err := json.Unmarshal([]byte(answer), &key); err != nil {
		return settings.KeyValue{}, fmt.Errorf("read the service's answer: %w", err)
	}
	return key, nil
}

// settingsValues turns KEY=VALUE arguments into the request. A value that
// is not JSON, or any value of a key that holds text, is sent as text, so
// `set timezone=Europe/Berlin` needs no quotes.
func settingsValues(ctx context.Context, bus settingsBus, args []string) (map[string]json.RawMessage, config.Tier, error) {
	if len(args) == 0 {
		return nil, "", fmt.Errorf("usage: gjallarctl settings set KEY=VALUE...")
	}
	values := map[string]json.RawMessage{}
	tier := config.TierEveryday
	for _, arg := range args {
		name, value, ok := strings.Cut(arg, "=")
		if !ok || name == "" {
			return nil, "", fmt.Errorf("%q is not KEY=VALUE", arg)
		}
		if _, dup := values[name]; dup {
			return nil, "", fmt.Errorf("%s is given twice", name)
		}
		key, err := settingsKey(ctx, bus, name)
		if err != nil {
			return nil, "", err
		}
		if key.Tier == config.TierCritical || (key.Tier == config.TierSecurity && tier == config.TierEveryday) {
			tier = key.Tier
		}
		raw := json.RawMessage(value)
		if strings.HasPrefix(string(key.Value), `"`) || !json.Valid(raw) {
			if raw, err = json.Marshal(value); err != nil {
				return nil, "", err
			}
		}
		values[name] = raw
	}
	return values, tier, nil
}

func settingsSet(ctx context.Context, bus settingsBus, args []string, stdout, stderr io.Writer) int {
	values, tier, err := settingsValues(ctx, bus, args)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	request, err := json.Marshal(values)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if tier == config.TierCritical {
		fmt.Fprintln(stdout, "Critical change: the password is asked now, and again when the hour is over.")
	}
	answer, err := bus.Call(ctx, "Request", string(request))
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	return settingsAsked(ctx, bus, answer, stdout, stderr)
}

// settingsAsked shows the change the service keeps and applies it once it
// may be.
func settingsAsked(ctx context.Context, bus settingsBus, answer string, stdout, stderr io.Writer) int {
	var change settings.Pending
	if err := json.Unmarshal([]byte(answer), &change); err != nil {
		fmt.Fprintf(stderr, "ERROR: read the service's answer: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Asked for change %s (%s):\n", shortID(change.ID), change.Tier)
	printKeyChanges(stdout, change.Change)
	for _, note := range change.Notes {
		fmt.Fprintf(stdout, "Note: %s\n", note)
	}

	wait := change.ReadyAt.Sub(settingsNow())
	if wait > settingsReadingWait {
		why := "Security changes wait an hour while this device is not on its home network."
		if change.Tier == config.TierCritical {
			why = "Critical changes always wait an hour."
		}
		fmt.Fprintf(stdout, "%s\nFrom %s, run: gjallarctl settings apply %s\nLogging out or rebooting drops it; gjallarctl settings cancel drops it now.\n",
			why, change.ReadyAt.Local().Format("15:04"), shortID(change.ID))
		return 0
	}
	if wait > 0 {
		fmt.Fprintf(stdout, "Read it once more; it can be applied in %d s. Ctrl+C keeps it waiting.\n", int(wait.Round(time.Second).Seconds()))
		interrupt, stop := signal.NotifyContext(ctx, os.Interrupt)
		err := settingsSleep(interrupt, wait)
		stop()
		if err != nil {
			fmt.Fprintf(stdout, "\nNot applied. It waits: gjallarctl settings apply %s\n", shortID(change.ID))
			return 0
		}
	}
	return settingsApply(ctx, bus, change.ID, stdout, stderr)
}

// settingsExport saves a signed file of the settings. The service signs
// what it reads itself; this command only writes the file, as the user, and
// never over an existing one.
func settingsExport(ctx context.Context, bus settingsBus, args []string, stdout io.Writer) error {
	if len(args) != 1 || args[0] == "" {
		return fmt.Errorf("usage: gjallarctl settings export FILE")
	}
	fmt.Fprintln(stdout, "The password dialog asks for your own password.")
	data, err := bus.Export(ctx)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(args[0], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := out.Write(data); err != nil {
		out.Close()
		os.Remove(args[0])
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(args[0])
		return err
	}
	fmt.Fprintf(stdout, "Saved to %s. It imports on this device, for this account: gjallarctl settings import %s\n", args[0], args[0])
	return nil
}

func settingsImport(ctx context.Context, bus settingsBus, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(stderr, "ERROR: usage: gjallarctl settings import FILE")
		return 2
	}
	data, err := readSettingsFile(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	answer, err := bus.Call(ctx, "Import", data)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	return settingsAsked(ctx, bus, answer, stdout, stderr)
}

func readSettingsFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, settings.MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > settings.MaxFileBytes {
		return nil, fmt.Errorf("%s is larger than a settings file can be (%d bytes)", path, settings.MaxFileBytes)
	}
	return data, nil
}

func printKeyChanges(stdout io.Writer, change settings.Change) {
	for _, key := range change.Keys {
		old := "(not set)"
		if key.Old != nil {
			old = settings.Shown(key.Old, settings.MaxShownValue)
		}
		fmt.Fprintf(stdout, "  %s: %s -> %s\n", key.Key, old, settings.Shown(key.New, settings.MaxShownValue))
	}
}

func shortID(id string) string {
	if len(id) > settingsShortID {
		return id[:settingsShortID]
	}
	return id
}

func settingsPendingList(ctx context.Context, bus settingsBus) ([]settings.Pending, error) {
	answer, err := bus.Call(ctx, "Pending")
	if err != nil {
		return nil, err
	}
	var list []settings.Pending
	if err := json.Unmarshal([]byte(answer), &list); err != nil {
		return nil, fmt.Errorf("read the service's answer: %w", err)
	}
	return list, nil
}

func settingsPending(ctx context.Context, bus settingsBus, args []string, stdout io.Writer) error {
	ids := len(args) == 1 && args[0] == "--ids"
	if len(args) != 0 && !ids {
		return fmt.Errorf("usage: gjallarctl settings pending [--ids]")
	}
	list, err := settingsPendingList(ctx, bus)
	if err != nil {
		return err
	}
	if ids {
		for _, change := range list {
			fmt.Fprintln(stdout, shortID(change.ID))
		}
		return nil
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "No change waits in this session.")
		return nil
	}
	now := settingsNow()
	for _, change := range list {
		ready := "ready now"
		if change.ReadyAt.After(now) {
			ready = "ready at " + change.ReadyAt.Local().Format("15:04")
		}
		fmt.Fprintf(stdout, "%s  %s, %s\n", shortID(change.ID), change.Tier, ready)
		printKeyChanges(stdout, change.Change)
	}
	return nil
}

// settingsPick finds the change an argument names: its id or the start of
// it, or the only waiting change when there is no argument.
func settingsPick(ctx context.Context, bus settingsBus, args []string) (settings.Pending, error) {
	if len(args) > 1 {
		return settings.Pending{}, fmt.Errorf("name one change")
	}
	list, err := settingsPendingList(ctx, bus)
	if err != nil {
		return settings.Pending{}, err
	}
	if len(list) == 0 {
		return settings.Pending{}, fmt.Errorf("no change waits in this session; ask with gjallarctl settings set")
	}
	if len(args) == 0 {
		if len(list) > 1 {
			return settings.Pending{}, fmt.Errorf("%d changes wait; name one (gjallarctl settings pending lists them)", len(list))
		}
		return list[0], nil
	}
	var found []settings.Pending
	for _, change := range list {
		if args[0] != "" && strings.HasPrefix(change.ID, args[0]) {
			found = append(found, change)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return settings.Pending{}, fmt.Errorf("no change %s waits in this session", args[0])
	default:
		return settings.Pending{}, fmt.Errorf("%s fits %d changes; give more of the id", args[0], len(found))
	}
}

func settingsCancel(ctx context.Context, bus settingsBus, args []string, stdout io.Writer) error {
	change, err := settingsPick(ctx, bus, args)
	if err != nil {
		return err
	}
	if _, err := bus.Call(ctx, "Cancel", change.ID); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Change %s dropped.\n", shortID(change.ID))
	return nil
}

func settingsApplyCommand(ctx context.Context, bus settingsBus, args []string, stdout, stderr io.Writer) int {
	change, err := settingsPick(ctx, bus, args)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	return settingsApply(ctx, bus, change.ID, stdout, stderr)
}

// settingsApply hands the change and this session's colors to root, then
// follows the rebuild it starts.
func settingsApply(ctx context.Context, bus settingsBus, id string, stdout, stderr io.Writer) int {
	colors := rebuildColors(stderr)
	fmt.Fprintln(stdout, "The password dialog shows the change root will write.")
	if _, err := bus.Call(ctx, "Apply", id, colors); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Written. Rebuilding; Ctrl+C stops watching, the rebuild goes on.")

	watch, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	started := settingsNow()
	for {
		running, result, err := bus.Rebuild(watch)
		if watch.Err() != nil {
			fmt.Fprintf(stdout, "\nStopped watching. Follow it with: journalctl -b -f -u %s\n", settings.RebuildUnit)
			return 0
		}
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: %v; follow it with: journalctl -b -f -u %s\n", err, settings.RebuildUnit)
			return 1
		}
		if !running {
			if result != "success" {
				fmt.Fprintf(stderr, "ERROR: the rebuild failed (%s); the change stays in user.config.json. Its log: journalctl -b -u %s\n", result, settings.RebuildUnit)
				return 1
			}
			fmt.Fprintf(stdout, "Rebuild finished after %s; the change is live.\n", settingsNow().Sub(started).Round(time.Second))
			return 0
		}
		if settingsSleep(watch, settingsPoll) != nil {
			continue
		}
	}
}

type systemSettingsBus struct{ conn *dbus.Conn }

func (b systemSettingsBus) Close() error { return b.conn.Close() }

func (b systemSettingsBus) Call(ctx context.Context, method string, args ...any) (string, error) {
	call := b.conn.Object(settings.BusName, dbus.ObjectPath(settings.ObjectPath)).
		CallWithContext(ctx, settings.Interface+"."+method, 0, args...)
	if call.Err != nil {
		var busErr dbus.Error
		if errors.As(call.Err, &busErr) && busErr.Name == "org.freedesktop.DBus.Error.ServiceUnknown" {
			return "", errSettingsMissing
		}
		return "", call.Err
	}
	var answer string
	if len(call.Body) > 0 {
		if err := call.Store(&answer); err != nil {
			return "", err
		}
	}
	return answer, nil
}

func (b systemSettingsBus) Export(ctx context.Context) ([]byte, error) {
	call := b.conn.Object(settings.BusName, dbus.ObjectPath(settings.ObjectPath)).
		CallWithContext(ctx, settings.Interface+".Export", 0)
	if call.Err != nil {
		var busErr dbus.Error
		if errors.As(call.Err, &busErr) && busErr.Name == "org.freedesktop.DBus.Error.ServiceUnknown" {
			return nil, errSettingsMissing
		}
		return nil, call.Err
	}
	var data []byte
	if err := call.Store(&data); err != nil {
		return nil, err
	}
	return data, nil
}

func (b systemSettingsBus) Rebuild(ctx context.Context) (bool, string, error) {
	systemd := b.conn.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1")
	var path dbus.ObjectPath
	if err := systemd.CallWithContext(ctx, "org.freedesktop.systemd1.Manager.LoadUnit", 0, settings.RebuildUnit).Store(&path); err != nil {
		return false, "", fmt.Errorf("read %s: %w", settings.RebuildUnit, err)
	}
	unit := b.conn.Object("org.freedesktop.systemd1", path)
	property := func(iface, name string) (dbus.Variant, error) {
		var value dbus.Variant
		err := unit.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, iface, name).Store(&value)
		if err != nil {
			err = fmt.Errorf("read %s: %w", settings.RebuildUnit, err)
		}
		return value, err
	}
	state, err := property("org.freedesktop.systemd1.Unit", "ActiveState")
	if err != nil {
		return false, "", err
	}
	job, err := property("org.freedesktop.systemd1.Unit", "Job")
	if err != nil {
		return false, "", err
	}
	result, err := property("org.freedesktop.systemd1.Service", "Result")
	if err != nil {
		return false, "", err
	}
	var queued struct {
		ID   uint32
		Path dbus.ObjectPath
	}
	if err := dbus.Store([]any{job.Value()}, &queued); err != nil {
		return false, "", fmt.Errorf("read %s: %w", settings.RebuildUnit, err)
	}
	active, _ := state.Value().(string)
	running := queued.ID != 0 || active == "activating" || active == "deactivating" || active == "reloading"
	text, _ := result.Value().(string)
	return running, text, nil
}
