package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	// timeWatchPoll is how often the watch asks chronyd.
	timeWatchPoll = 30 * time.Second
	// timeBarGrace keeps the clock icon away while chronyd syncs after boot
	// or after the network comes back.
	timeBarGrace = 5 * time.Minute
	// timeCardAfter is how long the time may go without NTS before the card
	// (ROADMAP section 18, question 61).
	timeCardAfter = 24 * time.Hour
	// timeCardOnline is how long the device must be online first, so the
	// card only comes when NTS had its chance on this network.
	timeCardOnline = 15 * time.Minute
)

var (
	readTimeStamp = func() time.Time {
		data, err := os.ReadFile(timeStampPath)
		if err != nil {
			return time.Time{}
		}
		seconds, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		if err != nil {
			return time.Time{}
		}
		return time.Unix(seconds, 0)
	}
	networkOnline = nmOnline
)

// nmOnline asks NetworkManager whether the device reaches the internet
// (NM_STATE_CONNECTED_GLOBAL).
func nmOnline() bool {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return false
	}
	defer conn.Close()
	v, err := conn.Object("org.freedesktop.NetworkManager", "/org/freedesktop/NetworkManager").GetProperty("org.freedesktop.NetworkManager.State")
	if err != nil {
		return false
	}
	state, ok := v.Value().(uint32)
	return ok && state == 70
}

// timeWatch is what the watch remembers between polls.
type timeWatch struct {
	badSince    time.Time
	onlineSince time.Time
}

// timeView is one poll: what the bar shows, and the card when one is due.
type timeView struct {
	// Kind is "hidden" (no icon), "unconfirmed" or "router".
	Kind    string
	Tooltip string
	// Card is set when the 24 hour card is due; Key names the spell.
	Card bool
	Key  string
}

// step turns one poll into what to show. lines come from timeLines; stamp
// is the last NTS confirmation gjallar-time-check recorded, zero when none.
// seen is the spell the last card was for.
func (w *timeWatch) step(lines []string, router bool, stamp time.Time, online bool, now time.Time, seen string) timeView {
	if online {
		if w.onlineSince.IsZero() {
			w.onlineSince = now
		}
	} else {
		w.onlineSince = time.Time{}
	}
	if len(lines) == 0 || strings.HasPrefix(lines[0], "Time: confirmed") {
		w.badSince = time.Time{}
		return timeView{Kind: "hidden"}
	}
	if w.badSince.IsZero() {
		w.badSince = now
	}
	// The bar tooltip cuts off near 90 characters.
	if router {
		// Question 62: router time shows the icon, never the card.
		return timeView{Kind: "router", Tooltip: "Time from the home router, not confirmed by NTS. Click for details."}
	}
	if now.Sub(w.badSince) < timeBarGrace {
		return timeView{Kind: "hidden"}
	}
	view := timeView{Kind: "unconfirmed", Tooltip: "Time not confirmed by NTS. Click for details."}
	if !stamp.IsZero() {
		view.Tooltip = "Time not confirmed by NTS since " + stamp.Local().Format("Jan 2 15:04") + ". Click for details."
	}
	// Without a stamp the clock was never confirmed on this install; the
	// watch then counts from when it saw that.
	last, key := stamp, "never"
	if stamp.IsZero() {
		last = w.badSince
	} else {
		key = strconv.FormatInt(stamp.Unix(), 10)
	}
	if online && now.Sub(w.onlineSince) >= timeCardOnline && now.Sub(last) >= timeCardAfter && key != seen {
		view.Card, view.Key = true, key
	}
	return view
}

// timeBarPath is what the bar widget reads: the kind, then the tooltip.
func timeBarPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	}
	return filepath.Join(dir, "gjallar", "time")
}

func timeNoticeSeenPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "gjallar", "time-notice-seen")
}

func writePrivateFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// runTimeWatch is the user unit gjallar-time-watch: it keeps the clock icon
// in the bar (user/wm/shells/noctalia/plugins/time) current and posts the
// card once per spell without NTS.
func runTimeWatch(args []string, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "Usage: gjallarctl system time-watch")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	barPath := timeBarPath()
	seenPath := timeNoticeSeenPath()
	var w timeWatch
	written := ""
	var cardDone chan struct{}
	var closeCard context.CancelFunc
	tick := time.NewTicker(timeWatchPoll)
	defer tick.Stop()
	for {
		seen := ""
		if data, err := os.ReadFile(seenPath); err == nil {
			seen = strings.TrimSpace(string(data))
		} else if !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
		}
		router := readTimeRouter()
		lines := timeLines(readClock(), readTimeBoot(), router, clockNow())
		stamp := readTimeStamp()
		view := w.step(lines, router != nil, stamp, networkOnline(), clockNow(), seen)

		bar := view.Kind + "\n" + view.Tooltip + "\n"
		if bar != written {
			if err := writePrivateFile(barPath, []byte(bar)); err != nil {
				fmt.Fprintf(stderr, "ERROR: %v\n", err)
			} else {
				written = bar
			}
		}

		// The card goes away by itself once NTS confirms the clock.
		if closeCard != nil && view.Kind != "unconfirmed" {
			closeCard()
		}
		if cardDone != nil {
			select {
			case <-cardDone:
				cardDone, closeCard = nil, nil
			default:
			}
		}
		if view.Card && cardDone == nil {
			if err := writePrivateFile(seenPath, []byte(view.Key+"\n")); err != nil {
				fmt.Fprintf(stderr, "ERROR: %v\n", err)
			} else {
				var cardCtx context.Context
				cardCtx, closeCard = context.WithCancel(ctx)
				cardDone = make(chan struct{})
				go func(done chan struct{}) {
					defer close(done)
					showTimeNotice(cardCtx, stamp, clockNow(), stderr)
				}(cardDone)
			}
		}

		select {
		case <-ctx.Done():
			if closeCard != nil {
				closeCard()
				<-cardDone
			}
			return 0
		case <-tick.C:
		}
	}
}

func showTimeNotice(ctx context.Context, stamp, now time.Time, stderr io.Writer) {
	summary := "Time not confirmed"
	body := "The clock has not been confirmed by NTS on this install. This network may block secure time."
	if !stamp.IsZero() {
		summary = "Time not confirmed for " + spanText(now.Sub(stamp))
		body = "The clock was last confirmed by NTS on " + html.EscapeString(stamp.Local().Format("Jan 2 15:04")) + ". This network may block secure time."
	}
	choice, err := showNotice(ctx, desktopNotice{
		App:     "GjallarOS",
		Icon:    "preferences-system-time",
		Summary: summary,
		Body:    body,
		Actions: []noticeAction{{"details", "Details"}, {"ok", "OK"}},
	})
	if err != nil {
		if ctx.Err() == nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
		}
		return
	}
	if choice == "details" {
		if err := exec.Command("systemctl", "--user", "start", "--no-block", "gjallar-time-details.service").Run(); err != nil {
			fmt.Fprintf(stderr, "ERROR: details: %v\n", err)
		}
	}
}

// systemTime prints what the system knows about the clock, for the bar
// icon's click and the card's Details.
func systemTime(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "Usage: gjallarctl system time")
		return 2
	}
	for _, line := range timeLines(readClock(), readTimeBoot(), readTimeRouter(), clockNow()) {
		fmt.Fprintln(stdout, line)
	}
	if stamp := readTimeStamp(); stamp.IsZero() {
		fmt.Fprintln(stdout, "Last confirmed by NTS: never on this install")
	} else {
		fmt.Fprintln(stdout, "Last confirmed by NTS: "+stamp.Local().Format("2006-01-02 15:04 MST"))
	}
	fmt.Fprintln(stdout, "\nThe clock is confirmed only by NTS, time with a signature. Some networks block it")
	fmt.Fprintln(stdout, "(TCP port 4460, UDP port 123); the clock then keeps running on its own.")
	return 0
}
