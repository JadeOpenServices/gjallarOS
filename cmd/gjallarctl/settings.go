package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/systemcheckout"
	"github.com/JadeOpenServices/gjallarOS/internal/installercheck"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
	"github.com/JadeOpenServices/gjallarOS/internal/sshsig"
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
       gjallarctl settings import FILE         ask for the change a saved file makes;
                                               with an own key, FILE.sig is its signature
       gjallarctl settings pair export FILE    save a pairing file for your other device
       gjallarctl settings pair add FILE       pair with the device that made FILE
       gjallarctl settings pair list [--ids]
       gjallarctl settings pair remove ID
ID may be the start of the id; without one, the only waiting change is used.
A saved file imports on the device and account that made it, and for the
account that paired with that one; settings of the device stay behind.`

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
	settingsPoll            = 2 * time.Second
	settingsEUID            = os.Geteuid
	settingsStdin io.Reader = os.Stdin
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
	// Bytes runs a method of the settings service that returns a file:
	// Export or PairExport.
	Bytes(ctx context.Context, method string) ([]byte, error)
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
	case "list", "get", "set", "pending", "apply", "cancel", "export", "import", "pair":
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
	case "pair":
		return settingsPair(ctx, bus, args, stdout, stderr)
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
func settingsValues(ctx context.Context, bus settingsBus, args []string) (map[string]json.RawMessage, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("usage: gjallarctl settings set KEY=VALUE...")
	}
	values := map[string]json.RawMessage{}
	for _, arg := range args {
		name, value, ok := strings.Cut(arg, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("%q is not KEY=VALUE", arg)
		}
		if _, dup := values[name]; dup {
			return nil, fmt.Errorf("%s is given twice", name)
		}
		key, err := settingsKey(ctx, bus, name)
		if err != nil {
			return nil, err
		}
		raw := json.RawMessage(value)
		if strings.HasPrefix(string(key.Value), `"`) || !json.Valid(raw) {
			if raw, err = json.Marshal(value); err != nil {
				return nil, err
			}
		}
		values[name] = raw
	}
	return values, nil
}

func settingsSet(ctx context.Context, bus settingsBus, args []string, stdout, stderr io.Writer) int {
	values, err := settingsValues(ctx, bus, args)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	return settingsRequest(ctx, bus, values, stdout, stderr)
}

// settingsRequest asks for values and follows the change until it is
// applied or has to wait.
func settingsRequest(ctx context.Context, bus settingsBus, values map[string]json.RawMessage, stdout, stderr io.Writer) int {
	request, err := json.Marshal(values)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
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
			why = "Critical changes always wait an hour, and the password is asked again then."
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
	return settingsApply(ctx, bus, change.Change, stdout, stderr)
}

// settingsExport saves a signed file of the settings. The service signs
// what it reads itself; this command only writes the file, as the user, and
// never over an existing one. For an account with its own key the file is
// left unsigned, and the user signs it with ssh-keygen.
func settingsExport(ctx context.Context, bus settingsBus, args []string, stdout io.Writer) error {
	if len(args) != 1 || args[0] == "" {
		return fmt.Errorf("usage: gjallarctl settings export FILE")
	}
	data, err := exportNewFile(ctx, bus, "Export", args[0], stdout)
	if err != nil {
		return err
	}
	if settings.IsUnsigned(data) {
		fmt.Fprintf(stdout, "Saved to %s. Sign it with your own key; KEY is its private key file (for a FIDO key, the id_ed25519_sk file):\n", args[0])
		fmt.Fprintf(stdout, "  ssh-keygen -Y sign -n %s -f KEY %s\n", settings.SSHNamespace, shellQuote(args[0]))
		fmt.Fprintf(stdout, "That writes %s.sig. Import both with: gjallarctl settings import %s\n", args[0], shellQuote(args[0]))
		return nil
	}
	fmt.Fprintf(stdout, "Saved to %s. It imports on this device, for this account: gjallarctl settings import %s\n", args[0], args[0])
	return nil
}

// exportNewFile saves what the service's method returns to path, a new file
// only the user can read, and returns it. The file is made before the
// service is asked, so an existing file fails before the password dialog.
func exportNewFile(ctx context.Context, bus settingsBus, method, path string, stdout io.Writer) ([]byte, error) {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(stdout, "The password dialog asks for your own password.")
	data, err := bus.Bytes(ctx, method)
	if err == nil {
		_, err = out.Write(data)
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return nil, err
	}
	return data, nil
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
	var answer string
	if settings.IsUnsigned(data) {
		answer, err = importSigned(ctx, bus, args[0], data)
	} else {
		answer, err = bus.Call(ctx, "Import", data)
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	return settingsAsked(ctx, bus, answer, stdout, stderr)
}

// importSigned sends a file for an own key with its signature, path.sig.
func importSigned(ctx context.Context, bus settingsBus, path string, data []byte) (string, error) {
	sig, err := readLimited(path+".sig", "an OpenSSH signature", sshsig.MaxSignatureBytes)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%s.sig is missing. This file is for your own key; sign it first: ssh-keygen -Y sign -n %s -f KEY %s", path, settings.SSHNamespace, shellQuote(path))
	}
	if err != nil {
		return "", err
	}
	return bus.Call(ctx, "ImportSigned", data, sig)
}

func readSettingsFile(path string) ([]byte, error) {
	return readLimited(path, "a settings file", settings.MaxFileBytes)
}

func readLimited(path, what string, limit int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("%s is larger than %s can be (%d bytes)", path, what, limit)
	}
	return data, nil
}

func printKeyChanges(stdout io.Writer, change settings.Change) {
	for _, line := range change.Lines() {
		fmt.Fprintf(stdout, "  %s\n", line)
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
	return settingsApply(ctx, bus, change.Change, stdout, stderr)
}

// settingsApply hands the change and this session's colors to root, then
// follows the rebuild it starts.
func settingsApply(ctx context.Context, bus settingsBus, change settings.Change, stdout, stderr io.Writer) int {
	id := change.ID
	written := "user.config.json"
	if change.Local != nil {
		written = localChangesPath
	}
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
				fmt.Fprintf(stderr, "ERROR: the rebuild failed (%s); the change stays in %s. Its log: journalctl -b -u %s\n", result, written, settings.RebuildUnit)
				return 1
			}
			live := "the change is live"
			if inRecoveryEntry() {
				live = "the change starts with the next boot"
			}
			fmt.Fprintf(stdout, "Rebuild finished after %s; %s.\n", settingsNow().Sub(started).Round(time.Second), live)
			offerLocalFolder(change, stdout, stderr)
			return 0
		}
		if settingsSleep(watch, settingsPoll) != nil {
			continue
		}
	}
}

// offerLocalFolder creates ~/GjallarOS-local from the template once
// developer mode is on, so the owner has a place to start. A folder that
// already exists is left alone.
func offerLocalFolder(change settings.Change, stdout, stderr io.Writer) {
	on := false
	for _, key := range change.Keys {
		on = on || (key.Key == "developerMode" && string(key.New) == "true")
	}
	if !on {
		return
	}
	dir, err := localFolder()
	if err != nil {
		fmt.Fprintf(stderr, "WARNING: %v\n", err)
		return
	}
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err := writeTemplate(dir); err != nil {
		fmt.Fprintf(stderr, "WARNING: %s not created: %v\n", dir, err)
		return
	}
	fmt.Fprintf(stdout, "Created %s from the template; put modules in system/ and home/, then run gjallarctl system apply.\n", dir)
}

// onSystemCheckout reports whether configPath is the user.config.json of
// the system checkout the device builds from. Root owns that file, so the
// commands that change it ask gjallar-settingsd (ROADMAP section 14, step
// 3.5); a development checkout is still written directly. Tests replace it.
var onSystemCheckout = func(configPath string) bool {
	return filepath.Dir(filepath.Clean(configPath)) == systemcheckout.Path &&
		systemcheckout.Active(installercheck.SystemRepositoryFile)
}

// errShown means the command already printed why it stopped.
var errShown = errors.New("stopped")

// openSettings connects to the settings service as the user, the way
// gjallarctl settings does.
func openSettings(stderr io.Writer) (settingsBus, bool) {
	if settingsEUID() == 0 {
		fmt.Fprintln(stderr, "ERROR: run this as your own user; the settings service takes changes from your login session")
		return nil, false
	}
	bus, err := settingsConnect()
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: system bus: %v\n", err)
		return nil, false
	}
	return bus, true
}

// askSystemSettings asks the settings service for values on the system
// checkout, with the waits and password of gjallarctl settings set, and
// applies the change when it may be.
func askSystemSettings(values map[string]any, stdout, stderr io.Writer) int {
	bus, ok := openSettings(stderr)
	if !ok {
		return 1
	}
	defer bus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), settingsCallTimeout)
	defer cancel()

	request := map[string]json.RawMessage{}
	for name, value := range values {
		if _, err := settingsKey(ctx, bus, name); err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		raw, err := json.Marshal(value)
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		request[name] = raw
	}
	return settingsRequest(ctx, bus, request, stdout, stderr)
}

// applySystemSettingNow writes an everyday setting on the system checkout
// without following the rebuild, for a choice made in a dialog. The
// password dialog still shows the change.
func applySystemSettingNow(name string, value any) error {
	var stderr strings.Builder
	bus, ok := openSettings(&stderr)
	if !ok {
		return errors.New(strings.TrimPrefix(strings.TrimSpace(stderr.String()), "ERROR: "))
	}
	defer bus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), settingsCallTimeout)
	defer cancel()

	request, err := json.Marshal(map[string]any{name: value})
	if err != nil {
		return err
	}
	answer, err := bus.Call(ctx, "Request", string(request))
	if err != nil {
		return err
	}
	var change settings.Pending
	if err := json.Unmarshal([]byte(answer), &change); err != nil {
		return fmt.Errorf("read the service's answer: %w", err)
	}
	if change.ReadyAt.After(settingsNow()) {
		return fmt.Errorf("the change waits until %s; apply it then with gjallarctl settings apply %s",
			change.ReadyAt.Local().Format("15:04"), shortID(change.ID))
	}
	if _, err = bus.Call(ctx, "Apply", change.ID, rebuildColors(io.Discard)); err != nil {
		// The caller shows the old value again, so the request goes too;
		// left pending it could be applied later when nobody remembers it.
		dropCtx, dropCancel := context.WithTimeout(context.Background(), settingsCallTimeout)
		defer dropCancel()
		_, _ = bus.Call(dropCtx, "Cancel", change.ID)
	}
	return err
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

func (b systemSettingsBus) Bytes(ctx context.Context, method string) ([]byte, error) {
	call := b.conn.Object(settings.BusName, dbus.ObjectPath(settings.ObjectPath)).
		CallWithContext(ctx, settings.Interface+"."+method, 0)
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
