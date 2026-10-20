package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installercheck"
	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust"
	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/broker"
)

func runUSB(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Usage: gjallarctl usb {status|audit|policy|review|provision-key|allow-once|trust-permanent|keep-blocked|enroll-internal|accept-replacement|forget|renew|disarm|allow-media} [--runtime-id ID --connection TOKEN] [--trusted-id ID] [--role ROLE] [--portable=true|false] [--media H:C:T:L --connection TOKEN]")
		return 2
	}
	if args[0] == "review" {
		return runUSBReview(args[1:], stderr)
	}
	f := flag.NewFlagSet("usb", flag.ContinueOnError)
	f.SetOutput(stderr)
	r := broker.Request{Action: broker.Action(args[0])}
	socket := f.String("socket", broker.DefaultSocket, "USB trust broker socket")
	f.StringVar(&r.RuntimeID, "runtime-id", "", "current runtime ID from policy")
	f.StringVar(&r.Connection, "connection", "", "review connection token from policy")
	f.StringVar(&r.TrustedID, "trusted-id", "", "accepted trust record ID")
	f.StringVar(&r.Role, "role", "", "ODDC expectation role")
	f.StringVar(&r.Media, "media", "", "storage medium ID (H:C:T:L) from policy")
	portable := f.String("portable", "", "explicit true or false for permanent external trust")
	if err := f.Parse(args[1:]); err != nil {
		return 2
	}
	if f.NArg() != 0 {
		fmt.Fprintln(stderr, "FAIL: unexpected USB command arguments")
		return 2
	}
	if *portable != "" {
		value, err := strconv.ParseBool(*portable)
		if err != nil {
			fmt.Fprintln(stderr, "FAIL: --portable must be true or false")
			return 2
		}
		r.Portable = &value
	}
	if r.Action == broker.ActionDisarm {
		key, err := usbReadRecoveryKey(os.Stdin, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "FAIL: %v\n", err)
			return 2
		}
		r.RecoveryKey = key
	}
	if err := broker.ValidateRequest(r); err != nil {
		fmt.Fprintf(stderr, "FAIL: %v\n", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	response, err := broker.Call(ctx, *socket, r)
	if err != nil {
		fmt.Fprintf(stderr, "FAIL: %v\n", err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(response); err != nil {
		fmt.Fprintf(stderr, "FAIL: %v\n", err)
		return 1
	}
	return 0
}

// Review presents typed daemon decisions. GTK dialogs contain no trust logic,
// and privileged requests go back through the authenticated broker socket.
func runUSBReview(args []string, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "FAIL: review takes no arguments")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	review := newUSBReviewState()
	review.path = usbReviewStatePath()
	if err := review.load(); err != nil {
		fmt.Fprintf(stderr, "WARN: USB review: %v\n", err)
	}
	// Media and renewal notices close their cards when ctx ends; wait for
	// that on exit.
	var media sync.WaitGroup
	defer media.Wait()
	// The same failure every 2 s says nothing new: report it once, and once
	// more when the broker answers again.
	lastErr, lastSaveErr := "", ""
	for ctx.Err() == nil {
		requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		response, err := broker.Call(requestCtx, broker.DefaultSocket, broker.Request{Action: broker.ActionPolicy})
		cancel()
		if err != nil {
			if err.Error() != lastErr {
				fmt.Fprintf(stderr, "WARN: USB review: %v\n", err)
				lastErr = err.Error()
			}
		} else {
			if lastErr != "" {
				fmt.Fprintln(stderr, "USB review: broker answers again")
				lastErr = ""
			}
			review.begin()
			for _, d := range response.Policy {
				if !review.wants(d) {
					continue
				}
				review.done(d, usbReviewDecision(ctx, executable, d, response.Enforcing))
			}
			// Media notices run on their own: a card left closed must not hold
			// back the review of the next USB device.
			for _, m := range response.Media {
				if response.MediaPending || !review.wantsMedia(m) {
					continue
				}
				media.Add(1)
				go func() {
					defer media.Done()
					if usbReviewMedia(ctx, executable, m) {
						review.mediaDone(m)
					}
				}()
			}
			for _, l := range response.Renewals {
				if !review.wantsRenewal(l) {
					continue
				}
				media.Add(1)
				go func() {
					defer media.Done()
					usbReviewRenewal(ctx, executable, l)
				}()
			}
			review.prune(!response.MediaPending)
			if err := review.save(); err != nil && err.Error() != lastSaveErr {
				fmt.Fprintf(stderr, "WARN: USB review: %v\n", err)
				lastSaveErr = err.Error()
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	return 0
}

type usbReviewOutcome int

const (
	// The user kept the device blocked; it is not announced again while it
	// stays.
	usbReviewAnswered usbReviewOutcome = iota
	// The user allowed the device. Allowing once lasts only while the broker
	// runs, so if it comes back blocked after a broker restart it is asked
	// again.
	usbReviewAllowed
	// Nobody answered: the device left, its token changed or the review stops.
	usbReviewInterrupted
	// The decision failed and should be offered again.
	usbReviewRetry
)

// usbReviewKey names a device across broker restarts: what it is and where
// it is plugged in, without the per-connection token.
func usbReviewKey(d usbtrust.Decision) string {
	return d.Identity.Hash + "\x00" + d.Identity.Port
}

// usbMediaKey names a medium across broker restarts: the reader, its slot
// and the size of what is in it, without the per-insertion token.
func usbMediaKey(m usbtrust.MediaDecision) string {
	return strings.Join([]string{m.VIDPID, m.Port, m.ID, strconv.FormatUint(m.Bytes, 10)}, "\x00")
}

// usbReviewState remembers what the review loop already asked, from one
// broker poll to the next.
type usbReviewState struct {
	// Connections with an open or finished notice.
	seen map[string]bool
	// Devices the user kept blocked, by identity and port. A restarted
	// broker hands out new connection tokens; that alone must not announce a
	// device again that never left.
	answered map[string]bool
	// What the current poll listed.
	active, present map[string]bool
	// Lapse warnings shown, by record and lapse time. Renewing moves the
	// time, so a later warning for the same device shows again.
	warned map[string]bool

	// Media connections with an open or finished notice, and those the
	// current poll listed.
	mediaSeen, mediaActive map[string]bool
	// Media the user kept closed, by usbMediaKey, and those the current poll
	// listed. Notices answer from their own goroutines.
	mu                   sync.Mutex
	closed, mediaPresent map[string]bool

	// path keeps answered and closed across a restart of the review, such
	// as the one a rebuild causes; empty keeps them in memory only.
	path  string
	dirty bool
}

func newUSBReviewState() *usbReviewState {
	return &usbReviewState{
		seen: map[string]bool{}, answered: map[string]bool{}, warned: map[string]bool{},
		mediaSeen: map[string]bool{}, closed: map[string]bool{},
	}
}

// usbReviewStatePath lives in the runtime directory: answers last until
// logout, like the notices they close.
func usbReviewStatePath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	}
	return filepath.Join(dir, "gjallar", "usb-review.json")
}

type usbReviewSaved struct {
	Blocked []string `json:"blocked,omitempty"`
	Closed  []string `json:"closed,omitempty"`
}

// load restores the answers of an earlier run. The first poll forgets the
// devices and media that left in between.
func (r *usbReviewState) load() error {
	if r.path == "" {
		return nil
	}
	data, err := os.ReadFile(r.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved usbReviewSaved
	if err := json.Unmarshal(data, &saved); err != nil {
		return fmt.Errorf("%s: %w", r.path, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range saved.Blocked {
		r.answered[key] = true
	}
	for _, key := range saved.Closed {
		r.closed[key] = true
	}
	return nil
}

// save writes the answers when they changed since the last save.
func (r *usbReviewState) save() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.path == "" || !r.dirty {
		return nil
	}
	saved := usbReviewSaved{Blocked: sortedKeys(r.answered), Closed: sortedKeys(r.closed)}
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, r.path); err != nil {
		return err
	}
	r.dirty = false
	return nil
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// begin starts a poll.
func (r *usbReviewState) begin() {
	r.active, r.present = map[string]bool{}, map[string]bool{}
	r.mediaActive = map[string]bool{}
	r.mu.Lock()
	r.mediaPresent = map[string]bool{}
	r.mu.Unlock()
}

// wants reports whether d should be announced now, and marks it as asked.
func (r *usbReviewState) wants(d usbtrust.Decision) bool {
	r.active[d.Connection] = true
	r.present[usbReviewKey(d)] = true
	if r.seen[d.Connection] || r.answered[usbReviewKey(d)] || d.Target != usbtrust.TargetBlock {
		return false
	}
	// Unsupported built-in parts stay blocked; asking would offer nothing
	// but "keep blocked".
	if usbtrust.AuditCode(d.Reason) == usbtrust.CodeInternalDebug {
		return false
	}
	r.seen[d.Connection] = true
	return true
}

// done records how the review of d ended.
func (r *usbReviewState) done(d usbtrust.Decision, outcome usbReviewOutcome) {
	switch outcome {
	case usbReviewAnswered:
		r.mu.Lock()
		r.answered[usbReviewKey(d)] = true
		r.dirty = true
		r.mu.Unlock()
	case usbReviewRetry:
		delete(r.seen, d.Connection)
	}
}

// wantsMedia reports whether m should be announced now, and marks it as
// asked.
func (r *usbReviewState) wantsMedia(m usbtrust.MediaDecision) bool {
	r.mediaActive[m.Connection] = true
	r.mu.Lock()
	r.mediaPresent[usbMediaKey(m)] = true
	closed := r.closed[usbMediaKey(m)]
	r.mu.Unlock()
	if m.Allowed || closed || r.mediaSeen[m.Connection] {
		return false
	}
	r.mediaSeen[m.Connection] = true
	return true
}

// mediaDone records that the user kept m closed.
func (r *usbReviewState) mediaDone(m usbtrust.MediaDecision) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed[usbMediaKey(m)] = true
	r.dirty = true
}

// wantsRenewal reports whether the lapse warning for l should be shown now,
// and marks it as shown.
func (r *usbReviewState) wantsRenewal(l broker.TrustLapse) bool {
	key := l.TrustedID + "@" + l.LapsesAt
	if r.warned[key] {
		return false
	}
	r.warned[key] = true
	return true
}

// prune forgets connections, devices and media the current poll no longer
// listed. Media are kept while the broker has not listed them yet.
func (r *usbReviewState) prune(mediaListed bool) {
	for token := range r.seen {
		if !r.active[token] {
			delete(r.seen, token)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.answered {
		if !r.present[key] {
			delete(r.answered, key)
			r.dirty = true
		}
	}
	if !mediaListed {
		return
	}
	for token := range r.mediaSeen {
		if !r.mediaActive[token] {
			delete(r.mediaSeen, token)
		}
	}
	for key := range r.closed {
		if !r.mediaPresent[key] {
			delete(r.closed, key)
			r.dirty = true
		}
	}
}

// usbReviewDecision announces one blocked device and carries out the chosen
// action. Every prompt closes as soon as the device is unplugged.
func usbReviewDecision(ctx context.Context, executable string, d usbtrust.Decision, enforcing bool) usbReviewOutcome {
	technical := usbReviewAlwaysTechnical()
	rows, available := usbReviewChoices(d, technical)
	if len(rows) == 0 {
		return usbReviewAnswered
	}
	ctx, cancel := usbWhilePresent(ctx, d.Connection)
	defer cancel()

	// Announce the block quietly; the review dialog opens only on request.
	// Dismissing the notice keeps the device blocked.
	picked, err := showNotice(ctx, usbReviewNotice(d, enforcing, technical))
	if ctx.Err() != nil {
		return usbReviewInterrupted
	}
	if err != nil {
		return usbReviewRetry
	}
	if !usbReviewRequested(picked) {
		return usbReviewAnswered
	}
	action, ok := usbReviewDialog(ctx, d, enforcing, technical)
	if ctx.Err() != nil {
		return usbReviewInterrupted
	}
	if !ok || !available[action] {
		return usbReviewAnswered
	}
	// Rejecting a device never requires authentication. The derived policy is
	// already block; keeping it blocked only dismisses this review for the
	// current connection.
	if action == string(usbtrust.ActionKeepBlocked) {
		return usbReviewAnswered
	}
	request := []string{"usb", action, "--runtime-id", d.RuntimeID, "--connection", d.Connection}
	switch action {
	case "accept-replacement":
		request = append(request, "--trusted-id", d.TrustedID)
	case "enroll-internal":
		request = append(request, "--role", d.Role)
	case "trust-permanent":
		portability, err := exec.CommandContext(ctx, "zenity", "--list", "--radiolist", "--title=Permanent USB trust", "--text=Choose whether this device may move between ports and docks on this machine.", "--column=Choose", "--column=Portable", "--column=Meaning", "--hide-column=2", "--print-column=2", "TRUE", "false", "Only on this port", "FALSE", "true", "On any port or dock").Output()
		if ctx.Err() != nil {
			return usbReviewInterrupted
		}
		value := strings.TrimSpace(string(portability))
		if err != nil || (value != "true" && value != "false") {
			return usbReviewAnswered
		}
		request = append(request, "--portable="+value)
	}
	output, err := usbAuthorize(ctx, d.Identity.Name, append([]string{executable}, request...))
	if ctx.Err() != nil {
		return usbReviewInterrupted
	}
	if err == nil {
		return usbReviewAllowed
	}
	_, _ = exec.Command("zenity", "--error", "--title=USB decision failed", "--text="+html.EscapeString(string(output))).Output()
	return usbReviewRetry
}

// usbReviewMedia announces a medium that stays closed in an allowed reader
// and opens it after authentication. The notice closes when the medium is
// removed or swapped. It reports whether the user chose to keep the medium
// closed; an opened medium that a broker restart closes again is announced
// again.
func usbReviewMedia(ctx context.Context, executable string, m usbtrust.MediaDecision) (keptClosed bool) {
	ctx, cancel := usbWhileMediaPresent(ctx, m.Connection)
	defer cancel()
	picked, err := showNotice(ctx, usbMediaNotice(m))
	if err != nil || ctx.Err() != nil {
		return false
	}
	switch picked {
	case "open", "default":
	default:
		return true
	}
	output, err := usbAuthorize(ctx, "the storage in "+usbMediaReader(m), []string{executable, "usb", "allow-media", "--media", m.ID, "--connection", m.Connection})
	if err == nil || ctx.Err() != nil {
		return false
	}
	_, _ = exec.Command("zenity", "--error", "--title=Storage stays closed", "--text="+html.EscapeString(string(output))).Output()
	return false
}

// usbReviewRenewal warns that a device's trust lapses soon and renews it on
// request. Not renewing is fine: the device asks again on its next connection.
func usbReviewRenewal(ctx context.Context, executable string, l broker.TrustLapse) {
	picked, err := showNotice(ctx, usbRenewalNotice(l, time.Local))
	if err != nil || ctx.Err() != nil {
		return
	}
	switch picked {
	case "renew", "default":
	default:
		return
	}
	output, err := usbAuthorize(ctx, usbRenewalName(l), []string{executable, "usb", "renew", "--trusted-id", l.TrustedID})
	if err == nil || ctx.Err() != nil {
		return
	}
	_, _ = exec.Command("zenity", "--error", "--title=Trust not renewed", "--text="+html.EscapeString(string(output))).Output()
}

func usbRenewalName(l broker.TrustLapse) string {
	if l.Name == "" {
		return "a trusted USB device"
	}
	return l.Name
}

// usbRenewalNotice builds the warning for trust that lapses soon.
func usbRenewalNotice(l broker.TrustLapse, zone *time.Location) desktopNotice {
	body := usbRenewalName(l) + " has not been connected"
	if used, err := time.Parse(time.RFC3339, l.LastUsed); err == nil {
		body += " since " + used.In(zone).Format("2 Jan")
	}
	body += "."
	if at, err := time.Parse(time.RFC3339, l.LapsesAt); err == nil {
		body += "\nFrom " + at.In(zone).Format("Mon 2 Jan 15:04") + " it has to be allowed again. Connect it or renew it to keep it trusted."
	}
	return desktopNotice{
		App:     "USB Guard",
		Icon:    "drive-removable-media-usb",
		Summary: "USB device trust ends soon",
		Body:    html.EscapeString(body),
		Actions: []noticeAction{{"default", "Renew"}, {"renew", "Renew"}, {"later", "Not now"}},
	}
}

func usbMediaReader(m usbtrust.MediaDecision) string {
	if m.Reader == "" {
		return "USB storage device"
	}
	return m.Reader
}

// usbMediaNotice builds the notice for a closed medium.
func usbMediaNotice(m usbtrust.MediaDecision) desktopNotice {
	body := usbMediaReader(m)
	if m.Bytes > 0 {
		body += fmt.Sprintf(", %.1f GB", float64(m.Bytes)/1e9)
	}
	body += "\nNothing on it is read until you open it."
	return desktopNotice{
		App:     "USB Guard",
		Icon:    "media-flash",
		Summary: "Card or USB storage inserted",
		Body:    html.EscapeString(body),
		Actions: []noticeAction{{"default", "Open"}, {"open", "Open it"}, {"keep", "Keep closed"}},
	}
}

// usbWhileMediaPresent ends the context once the medium leaves the policy.
func usbWhileMediaPresent(ctx context.Context, connection string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			callCtx, done := context.WithTimeout(ctx, 2*time.Second)
			response, err := broker.Call(callCtx, broker.DefaultSocket, broker.Request{Action: broker.ActionPolicy})
			done()
			if err == nil && !response.MediaPending && !usbMediaActive(response.Media, connection) {
				cancel()
				return
			}
		}
	}()
	return ctx, cancel
}

func usbMediaActive(media []usbtrust.MediaDecision, connection string) bool {
	for _, m := range media {
		if m.Connection == connection && !m.Allowed {
			return true
		}
	}
	return false
}

// usbWhilePresent returns a context that ends once the review connection
// leaves the policy, so prompts for an unplugged device close within a second.
func usbWhilePresent(ctx context.Context, connection string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			callCtx, done := context.WithTimeout(ctx, 2*time.Second)
			response, err := broker.Call(callCtx, broker.DefaultSocket, broker.Request{Action: broker.ActionPolicy})
			done()
			// An unreachable broker is not proof of removal.
			if err == nil && !usbConnectionActive(response.Policy, connection) {
				cancel()
				return
			}
		}
	}()
	return ctx, cancel
}

func usbConnectionActive(policy []usbtrust.Decision, connection string) bool {
	for _, d := range policy {
		if d.Connection == connection {
			return true
		}
	}
	return false
}

// usbReviewNotice builds the notice announcing a device that needs a
// decision. In audit mode the device already works, so the notice
// must not claim it is blocked. Hardware IDs only appear in the techy view.
func usbReviewNotice(d usbtrust.Decision, enforcing, technical bool) desktopNotice {
	name := d.Identity.Name
	if name == "" {
		name = "Unknown device"
	}
	if technical {
		name = fmt.Sprintf("%s (%s)", name, d.Identity.VIDPID)
	}
	summary, dismiss := "USB device blocked", "Keep blocked"
	if !enforcing {
		summary, dismiss = "New USB device", "Ignore"
		name += "\nBlocking is off, so it already works."
	}
	return desktopNotice{
		App:     "USB Guard",
		Icon:    "drive-removable-media-usb",
		Summary: summary,
		// Notification bodies may carry markup; device strings are untrusted.
		Body:    html.EscapeString(name),
		Actions: []noticeAction{{"default", "Review"}, {"review", "Review…"}, {"keep", dismiss}},
	}
}

// wrapWords breaks long lines at spaces. Zenity never wraps the list
// dialog text, so one long line would set a minimum width wider than the
// screen and override the compositor's size rule; terminals cut words.
func wrapWords(text string, width int) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		current := ""
		for _, word := range strings.Fields(line) {
			if current != "" && len([]rune(current))+1+len([]rune(word)) > width {
				out = append(out, current)
				current = ""
			}
			if current != "" {
				current += " "
			}
			current += word
		}
		out = append(out, current)
	}
	return strings.Join(out, "\n")
}

// USB review markers. Red is reserved for known attack patterns; the
// daemon never classifies products, so nothing is claimed as proven malware.
const (
	usbFine    = "#2e9d4f"
	usbUnknown = "#8e5bd6"
	usbBad     = "#d93b3b"
)

var usbReviewLegend = fmt.Sprintf("\n<small><span foreground=%q>●</span> fine   <span foreground=%q>●</span> unknown, check it   <span foreground=%q>●</span> known attack pattern</small>\n", usbFine, usbUnknown, usbBad)

type usbFact struct {
	colour string
	text   string
}

// usbReviewFacts translates the daemon's decision into plain statements.
// Every risk finding is shown; unknown codes fall back to their detail text.
func usbReviewFacts(d usbtrust.Decision) []usbFact {
	var facts []usbFact
	add := func(colour, text string) { facts = append(facts, usbFact{colour, text}) }
	role := d.Role
	if role == "" {
		role = "part"
	}
	switch usbtrust.AuditCode(d.Reason) {
	case usbtrust.CodeInternalUnvalidated:
		add(usbFine, "Looks like this computer's built-in "+role)
		add(usbUnknown, "Not yet confirmed as the built-in "+role)
	case usbtrust.CodeInternalChanged:
		add(usbBad, "Differs from the built-in "+role+" trusted before")
		if d.Anchor == usbtrust.AnchorBluetoothAddress {
			add(usbBad, "Its Bluetooth address differs from the stored one (this catches a swapped part, not a copied one)")
		}
	case usbtrust.CodeTrustedExternalChanged:
		add(usbBad, "Claims to be a trusted device, but its details changed")
	case usbtrust.CodeTrustedExternalLapsed:
		add(usbUnknown, "Trusted before, but not connected for too long, so it asks again")
	case usbtrust.CodeUnexpectedInternal:
		add(usbBad, "Unexpected device inside this computer")
	case usbtrust.CodeInternalAmbiguous:
		add(usbUnknown, "Cannot tell which built-in part this is")
	case usbtrust.CodeInternalDebug:
		add(usbUnknown, "Built-in "+role+" is not supported on this model yet, so it stays blocked")
	case usbtrust.CodeInternalPortMatch:
		add(usbFine, "Built-in "+role+" on its known port")
	default:
		add(usbUnknown, "Never trusted on this computer")
	}
	if d.Identity.Serial == "" {
		add(usbUnknown, "No serial number, so copies look identical")
	} else {
		add(usbFine, "Has its own serial number")
	}
	codes := map[string]bool{}
	for _, risk := range d.Risks {
		codes[risk.Code] = true
	}
	if !codes[usbtrust.RiskHIDInput] {
		add(usbFine, "Cannot type or move the pointer")
	}
	if !codes[usbtrust.RiskNetwork] {
		add(usbFine, "No network connection")
	}
	for _, risk := range d.Risks {
		switch risk.Code {
		case "descriptor-only":
			// Covered by the serial number statement.
		case usbtrust.RiskHIDInput:
			add(usbUnknown, "Can type like a keyboard (normal for keyboards and passkeys)")
		case usbtrust.RiskMassStorage:
			add(usbUnknown, "Stores files")
		case usbtrust.RiskNetwork:
			add(usbUnknown, "Can act as a network adapter")
		case usbtrust.RiskWirelessController:
			add(usbUnknown, "Wireless controller, such as Bluetooth")
		case usbtrust.RiskVendorSpecific:
			add(usbUnknown, "Has a maker-specific function that cannot be inspected")
		case usbtrust.RiskHub:
			add(usbFine, "USB hub")
		case usbtrust.RiskBillboard:
			add(usbFine, "Adapter information only")
		case usbtrust.RiskComposite:
			add(usbUnknown, "Combines several functions")
		case usbtrust.RiskHIDStorageComposite:
			add(usbBad, "Stores files and can type: a known attack trick")
		case usbtrust.RiskHIDNetworkComposite:
			add(usbBad, "Can type and act as a network adapter: a known attack trick")
		default:
			colour := usbUnknown
			if risk.Severity == usbtrust.RiskHigh {
				colour = usbBad
			}
			add(colour, risk.Detail)
		}
	}
	return facts
}

// The review opens in the simple view. The view button switches only the
// open dialog; the "always" box sets usbReviewTechnicalView in
// user.config.json, which makes the techy view the start view until it is
// unticked. Neither changes the decision itself.
const (
	usbTechyView      = "Techy view"
	usbSimpleView     = "Simple view"
	usbAlwaysTechyOff = "☐ Always techy view"
	usbAlwaysTechyOn  = "☑ Always techy view"
)

// usbReviewDialog shows the review until an action is chosen or the dialog
// is closed. It returns the chosen action.
func usbReviewDialog(ctx context.Context, d usbtrust.Decision, enforcing, always bool) (string, bool) {
	technical := always
	for {
		rows, _ := usbReviewChoices(d, technical)
		toggle := usbTechyView
		if technical {
			toggle = usbSimpleView
		}
		box := usbAlwaysTechyOff
		if always {
			box = usbAlwaysTechyOn
		}
		summary := usbReviewSummary(d, enforcing, technical)
		dialog := []string{"--list", "--radiolist", "--title=USB device review", "--text=" + summary, fmt.Sprintf("--width=%d", usbReviewWidth), fmt.Sprintf("--height=%d", usbReviewHeight(summary, len(rows)/3)), "--extra-button=" + toggle, "--extra-button=" + box, "--column=", "--column=Action", "--column=What to do", "--hide-column=2", "--print-column=2"}
		choice, err := exec.CommandContext(ctx, "zenity", append(dialog, rows...)...).Output()
		picked := strings.TrimSpace(string(choice))
		if err == nil {
			return picked, true
		}
		if ctx.Err() != nil {
			return "", false
		}
		// Zenity reports an extra button as a non-zero exit with its label.
		switch picked {
		case toggle:
			technical = !technical
		case box:
			always = !always
			technical = always
			if err := setUSBReviewAlwaysTechnical(always); err != nil {
				// The box shows what is saved; only this dialog keeps the view.
				always = !always
				_ = exec.CommandContext(ctx, "zenity", "--error", "--title=USB device review",
					"--text="+html.EscapeString("The setting was not saved, so the next review starts as before.\n\n"+err.Error())).Run()
			}
		default:
			return "", false
		}
	}
}

// usbReviewHeight sizes the review so every action row shows. Zenity gives
// the list only what the text leaves of its default height, which fit two
// rows and hid "Always allow" behind a scrollbar. Measured at scale 1: about
// 380 px of title and buttons, 19 px per text line, 30 px per list row plus
// the header, and a spare row's worth. The Hyprland rule for this title sets
// no size, or it would override this one.
func usbReviewHeight(summary string, rows int) int {
	return 380 + 19*strings.Count(summary, "\n") + 30*(rows+2)
}

// usbReviewWidth keeps the review as wide as the old fixed window rule made
// it on a 1280 px screen; the text wraps to it.
const usbReviewWidth = 520

// usbReviewConfigKey is the user.config.json switch behind the "always" box.
// The box edits the checkout's file (through the settings service on the
// system checkout), so the choice is part of the system configuration and
// stays until someone unticks it.
const usbReviewConfigKey = "usbReviewTechnicalView"

func usbReviewConfigPath() (string, error) {
	repo, err := installercheck.DiscoverRepository("")
	if err != nil {
		return "", err
	}
	return filepath.Join(repo, "user.config.json"), nil
}

// usbReviewAlwaysTechnical reports whether the review starts in the techy
// view. user.config.json is read at every review so a tick applies at once;
// if it cannot be read the review starts simple. The value stays out of the
// service unit: a unit that changed with the tick was restarted by the
// rebuild the tick starts, which closed the review that was open.
func usbReviewAlwaysTechnical() bool {
	if path, err := usbReviewConfigPath(); err == nil {
		if on, ok := usbReviewConfigValue(path); ok {
			return on
		}
	}
	return false
}

func usbReviewConfigValue(path string) (bool, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return false, false
	}
	var on bool
	if raw, found := fields[usbReviewConfigKey]; !found || json.Unmarshal(raw, &on) != nil {
		return false, false
	}
	return on, true
}

func setUSBReviewAlwaysTechnical(on bool) error {
	path, err := usbReviewConfigPath()
	if err != nil {
		return fmt.Errorf("find the GjallarOS checkout: %w", err)
	}
	if onSystemCheckout(path) {
		return applySystemSettingNow(usbReviewConfigKey, on)
	}
	return config.SetBool(path, usbReviewConfigKey, on)
}

// usbReviewVerdict condenses the facts into one plain statement for the
// simple view. Any attack pattern wins over everything else.
func usbReviewVerdict(d usbtrust.Decision) (colour, headline, advice string) {
	for _, fact := range usbReviewFacts(d) {
		if fact.colour == usbBad {
			return usbBad, "This device may be unsafe", "Keep it blocked unless you know exactly what it is."
		}
	}
	switch usbtrust.AuditCode(d.Reason) {
	case usbtrust.CodeInternalUnvalidated:
		return usbFine, "Built-in part of this computer", "Trust it once and you will not be asked again."
	case usbtrust.CodeTrustedExternalLapsed:
		return usbUnknown, "Allowed before, but not used for a long time", "Allow it again if you just plugged it in yourself."
	}
	return usbUnknown, "New device", "Allow it if you just plugged it in yourself."
}

// usbReviewSummary renders the review text as Pango markup for zenity.
// Device strings are untrusted and always escaped.
func usbReviewSummary(d usbtrust.Decision, enforcing, technical bool) string {
	var b strings.Builder
	name := d.Identity.Name
	if name == "" {
		name = "Unknown device"
	}
	fmt.Fprintf(&b, "<b>%s</b>\n", html.EscapeString(name))
	if !technical {
		colour, headline, advice := usbReviewVerdict(d)
		fmt.Fprintf(&b, "\n<span foreground=%q>●</span> %s\n%s\n", colour, headline, advice)
		b.WriteString(usbReviewLegend)
		if !enforcing {
			b.WriteString("<small>Blocking is off right now, so it already works.</small>")
		}
		return b.String()
	}
	if !enforcing {
		b.WriteString(html.EscapeString(wrapWords("Audit mode: this device already works. Your choice is saved for when blocking is on.", 72)) + "\n")
	}
	b.WriteString("\n")
	facts := usbReviewFacts(d)
	// Warnings first, reassurance last.
	rank := map[string]int{usbBad: 0, usbUnknown: 1, usbFine: 2}
	sort.SliceStable(facts, func(i, j int) bool { return rank[facts[i].colour] < rank[facts[j].colour] })
	for _, fact := range facts {
		fmt.Fprintf(&b, "<span foreground=%q>●</span> %s\n", fact.colour, html.EscapeString(fact.text))
	}
	b.WriteString(usbReviewLegend)
	details := fmt.Sprintf("ID %s · serial %s · port %s · interfaces %s", d.Identity.VIDPID, usbOrNone(d.Identity.Serial), usbOrNone(d.Identity.Port), usbOrNone(strings.Join(d.Identity.Interfaces, ", ")))
	if d.TrustedID != "" {
		details += " · record " + d.TrustedID
	}
	fmt.Fprintf(&b, "<small><span foreground=\"gray\">%s</span></small>", html.EscapeString(wrapWords(details, 90)))
	return b.String()
}

func usbOrNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

// usbAuthorize runs one USB trust mutation behind fresh authentication. The
// fingerprint is tried first while a notice offers the password instead; a
// failed or skipped scan opens the password prompt at once. The credential
// cached for the command is dropped again afterwards.
// usbAuthMu keeps two prompts from sharing or dropping one sudo timestamp.
var usbAuthMu sync.Mutex

func usbAuthorize(ctx context.Context, name string, command []string) ([]byte, error) {
	usbAuthMu.Lock()
	defer usbAuthMu.Unlock()
	const sudo = "/run/wrappers/bin/sudo"
	authHelper, err := helperExecutable("gjallar-sudo-auth")
	if err != nil {
		return []byte(err.Error()), err
	}
	_ = exec.CommandContext(ctx, sudo, "-k").Run()
	defer func() { _ = exec.Command(sudo, "-k").Run() }()

	if probeFingerprint(ctx).State != fingerprintReady || !usbFingerprintAuth(ctx, sudo, authHelper, name) {
		// A scan that finished after "Use password" may have cached a
		// timestamp; drop it so the password prompt really appears.
		_ = exec.CommandContext(ctx, sudo, "-k").Run()
		// pam_askpass_service limits this phase to the password. The askpass
		// dialog is a child of sudo, so cancelling kills the whole group.
		password := exec.CommandContext(ctx, sudo, "-A", authHelper)
		password.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		password.Cancel = func() error { return syscall.Kill(-password.Process.Pid, syscall.SIGKILL) }
		// sudo and the askpass dialog report to the journal; their toolkit
		// warnings mean nothing in the error shown to the user.
		password.Stderr = os.Stderr
		if err := password.Run(); err != nil {
			return []byte("Authentication failed or was cancelled."), err
		}
	}
	return exec.CommandContext(ctx, sudo, append([]string{"-n", "--"}, command...)...).CombinedOutput()
}

// usbFingerprintAuth waits for the fingerprint-only sudo phase while a notice
// explains what is happening. Choosing "Use password" cancels the scan.
func usbFingerprintAuth(ctx context.Context, sudo, authHelper, name string) bool {
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// sudo ignores SIGTERM while pam_fprintd waits for a finger, so the
	// scan has to be killed for "Use password" to take effect at once.
	scan := exec.CommandContext(scanCtx, sudo, authHelper)
	if err := scan.Start(); err != nil {
		return false
	}
	scanned := make(chan error, 1)
	go func() { scanned <- scan.Wait() }()

	picked := make(chan string, 1)
	var notice sync.WaitGroup
	notice.Add(1)
	go func() {
		defer notice.Done()
		key, _ := showNotice(scanCtx, usbFingerprintNotice(name))
		picked <- key
	}()
	// The notice closes once the scan is over, however it ended.
	defer func() {
		cancel()
		notice.Wait()
	}()

	select {
	case err := <-scanned:
		return err == nil
	case key := <-picked:
		if key == "password" {
			cancel()
			<-scanned
			return false
		}
		// A dismissed notice leaves the scan running.
		return <-scanned == nil
	}
}

// usbFingerprintNotice builds the notice for the scan prompt.
func usbFingerprintNotice(name string) desktopNotice {
	if name == "" {
		name = "this USB device"
	}
	return desktopNotice{
		App:     "USB Guard",
		Icon:    "fingerprint",
		Summary: "Touch the fingerprint reader",
		Body:    html.EscapeString("to authorize " + name),
		Actions: []noticeAction{{"password", "Use password"}},
	}
}

// usbReviewRequested reports whether the notice was answered with Review or a
// click on its body.
func usbReviewRequested(output string) bool {
	switch strings.TrimSpace(output) {
	case "review", "default":
		return true
	}
	return false
}

// The domain supplies available actions; this adapter only gives them labels.
// Record IDs and role names only appear in the techy view.
func usbReviewChoices(d usbtrust.Decision, technical bool) ([]string, map[string]bool) {
	var rows []string
	available := map[string]bool{}
	for _, action := range d.Actions {
		label, selected := "", "FALSE"
		switch action {
		case usbtrust.ActionKeepBlocked:
			label, selected = "Keep blocked", "TRUE"
		case usbtrust.ActionAllowOnce:
			label = "Allow until unplugged"
		case usbtrust.ActionTrustPermanent:
			label = "Always allow on this computer"
		case usbtrust.ActionEnrollInternal:
			label = "Trust as a built-in part of this computer"
			if technical {
				label = "Enroll as expected internal role " + d.Role
			}
		case usbtrust.ActionAcceptReplacement:
			label = "Accept as replacement for a trusted device"
			if technical {
				label = "Accept replacement for " + d.TrustedID
			}
			if usbtrust.AuditCode(d.Reason) == usbtrust.CodeTrustedExternalLapsed {
				label = "Always allow again"
				if technical {
					label = "Renew trust for " + d.TrustedID
				}
			}
		default:
			continue
		}
		rows = append(rows, selected, string(action), label)
		available[string(action)] = true
	}
	return rows, available
}
