package move

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/oldroot"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/movehelper"
	"golang.org/x/sys/unix"
)

// fakeHelper answers each operation from a list, in order. An answer is a
// result value or an error.
type fakeHelper struct {
	answers map[string][]any
	calls   []movehelper.Request
	keys    []string
}

func (h *fakeHelper) Run(_ context.Context, req movehelper.Request, result any) error {
	h.keys = append(h.keys, string(req.Key))
	req.Key = nil
	h.calls = append(h.calls, req)
	list := h.answers[req.Op]
	if len(list) == 0 {
		return errors.New("no answer for " + req.Op)
	}
	answer := list[0]
	if len(list) > 1 {
		h.answers[req.Op] = list[1:]
	}
	if err, ok := answer.(error); ok {
		return err
	}
	if result == nil {
		return nil
	}
	raw, err := json.Marshal(answer)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, result)
}

func (h *fakeHelper) ops() string {
	var ops []string
	for _, c := range h.calls {
		ops = append(ops, c.Op)
	}
	return strings.Join(ops, " ")
}

func refusal(reason oldroot.Reason) error {
	return &oldroot.Refusal{Reason: reason, Text: "text for " + string(reason)}
}

var (
	encDisk   = oldroot.Part{Path: "/dev/nvme0n1p2", Disk: "/dev/nvme0n1", Encrypted: true, Internal: true, Size: 512110190592, Model: "Example SSD 512"}
	plainDisk = oldroot.Part{Path: "/dev/sdb2", Disk: "/dev/sdb", Size: 1000204886016, Model: "Example USB disk"}
	opened    = movehelper.OpenResult{Info: oldroot.Info{Channel: "stable", LastUpdated: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)}}
)

// pty gives a terminal whose echo Secret can switch; the answers come from
// the UI's own reader.
func pty(t *testing.T) *os.File {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals here: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	fd, _, errno := unix.Syscall(unix.SYS_IOCTL, master.Fd(), unix.TIOCGPTPEER, uintptr(unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC))
	if errno != 0 {
		t.Skipf("no TIOCGPTPEER here: %v", errno)
	}
	term := os.NewFile(fd, "pty")
	t.Cleanup(func() { term.Close() })
	return term
}

// marks returns the anchor lines written while the test ran.
func marks(t *testing.T) func() string {
	t.Helper()
	l, err := installlog.OpenStick(filepath.Join(t.TempDir(), "log"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	installlog.SetCurrent(l)
	t.Cleanup(func() {
		installlog.SetCurrent(nil)
		l.Close()
	})
	return func() string {
		raw, err := os.ReadFile(l.Path())
		if err != nil {
			t.Fatal(err)
		}
		var lines []string
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, "GJA-") {
				lines = append(lines, line[strings.Index(line, "GJA-"):])
			}
		}
		return strings.Join(lines, "\n")
	}
}

func run(t *testing.T, h *fakeHelper, input string) (*Old, string, error) {
	t.Helper()
	var out bytes.Buffer
	f := Flow{UI: prompt.New(strings.NewReader(input), &out), Term: pty(t), Helper: h, Mode: NewDisk}
	old, err := f.OpenOld(context.Background())
	return old, out.String(), err
}

func TestOneEncryptedDisk(t *testing.T) {
	got := marks(t)
	h := &fakeHelper{answers: map[string][]any{
		"find":  {movehelper.FindResult{Disks: []oldroot.Part{encDisk}}},
		"check": {movehelper.CheckResult{Encrypted: true}},
		"open":  {opened},
	}}
	old, out, err := run(t, h, "\n\nright-key\n")
	if err != nil {
		t.Fatal(err)
	}
	if old.Disk != encDisk || old.Info.Channel != "stable" || old.PINUnchecked {
		t.Fatalf("old = %+v", old)
	}
	if h.ops() != "find check open" {
		t.Fatalf("ops = %q", h.ops())
	}
	if h.calls[1].Disk != "/dev/nvme0n1" || h.calls[2].Disk != "/dev/nvme0n1" {
		t.Fatalf("disk sent = %+v", h.calls)
	}
	// The empty answer was asked again, not sent.
	if h.keys[2] != "right-key" {
		t.Fatalf("key sent = %q", h.keys[2])
	}
	for _, want := range []string{
		"/dev/nvme0n1, Example SSD 512, 512.1 GB. Use this disk?",
		"Checking the old disk...",
		"Found gjallarOS (stable channel, last updated 2 November 2026).",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("screen lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "right-key") {
		t.Error("the key reached the screen text")
	}
	if m := got(); m != "GJA-2101 stick.move.started mode=new-disk\nGJA-2106 stick.move.unlock-ok tries=1" {
		t.Errorf("marks:\n%s", m)
	}
}

func TestSeveralDisksPickByNumber(t *testing.T) {
	h := &fakeHelper{answers: map[string][]any{
		"find":  {movehelper.FindResult{Disks: []oldroot.Part{encDisk, plainDisk}}},
		"check": {movehelper.CheckResult{}},
		"open":  {opened},
	}}
	old, out, err := run(t, h, "3\nx\n2\n")
	if err != nil {
		t.Fatal(err)
	}
	if old.Disk != plainDisk {
		t.Fatalf("picked %+v", old.Disk)
	}
	if !strings.Contains(out, "  2) /dev/sdb, Example USB disk, 1.0 TB, external\n") {
		t.Errorf("list:\n%s", out)
	}
	if strings.Count(out, "Type a number from 1 to 2.") != 2 {
		t.Errorf("wrong numbers not asked again:\n%s", out)
	}
	if h.keys[2] != "" {
		t.Error("a key went with an unencrypted disk")
	}
}

func TestNoDiskLookAgain(t *testing.T) {
	h := &fakeHelper{answers: map[string][]any{
		"find":  {movehelper.FindResult{}, movehelper.FindResult{Disks: []oldroot.Part{plainDisk}}},
		"check": {movehelper.CheckResult{}},
		"open":  {opened},
	}}
	if _, out, err := run(t, h, "y\ny\n"); err != nil || !strings.Contains(out, "No disk with gjallarOS was found.") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if h.ops() != "find find check open" {
		t.Fatalf("ops = %q", h.ops())
	}
}

func TestNoDiskGiveUp(t *testing.T) {
	h := &fakeHelper{answers: map[string][]any{"find": {movehelper.FindResult{}}}}
	if _, _, err := run(t, h, "n\n"); !errors.Is(err, ErrStopped) {
		t.Fatalf("err = %v", err)
	}
}

func TestRefusalsShowTextAndMark(t *testing.T) {
	cases := []struct {
		reason oldroot.Reason
		op     string
		mark   string
	}{
		{oldroot.Reencrypt, "check", "GJA-2104 stick.move.header-refused reason=reencrypt"},
		{oldroot.UnknownToken, "check", "GJA-2104 stick.move.header-refused reason=unknown-token"},
		{oldroot.KDFMemory, "check", "GJA-2104 stick.move.header-refused reason=kdf-memory"},
		{oldroot.NotLUKS2, "check", "GJA-2104 stick.move.header-refused reason=not-luks2"},
		{oldroot.Unsupported, "check", "GJA-2104 stick.move.header-refused reason=unsupported"},
		{oldroot.OldCryptsetup, "check", "GJA-2108 stick.move.old-refused reason=old-cryptsetup"},
		{oldroot.Busy, "open", "GJA-2108 stick.move.old-refused reason=busy"},
		{oldroot.NotBtrfs, "open", "GJA-2105 stick.move.layout-refused reason=not-btrfs"},
		{oldroot.NoEvidence, "open", "GJA-2105 stick.move.layout-refused reason=no-evidence"},
		{oldroot.Managed, "open", "GJA-2105 stick.move.layout-refused reason=managed"},
	}
	for _, c := range cases {
		t.Run(string(c.reason), func(t *testing.T) {
			got := marks(t)
			h := &fakeHelper{answers: map[string][]any{
				"find":  {movehelper.FindResult{Disks: []oldroot.Part{plainDisk}}},
				"check": {movehelper.CheckResult{}},
				"open":  {opened},
			}}
			h.answers[c.op] = []any{refusal(c.reason)}
			_, out, err := run(t, h, "y\n")
			if !errors.Is(err, ErrStopped) {
				t.Fatalf("err = %v", err)
			}
			if !strings.Contains(out, "text for "+string(c.reason)) {
				t.Errorf("text not shown:\n%s", out)
			}
			if m := got(); !strings.HasSuffix(m, c.mark) {
				t.Errorf("marks:\n%s", m)
			}
		})
	}
}

func TestUnknownRefusalIsAnError(t *testing.T) {
	h := &fakeHelper{answers: map[string][]any{
		"find":  {movehelper.FindResult{Disks: []oldroot.Part{plainDisk}}},
		"check": {refusal("made-up")},
	}}
	if _, _, err := run(t, h, "y\n"); err == nil || errors.Is(err, ErrStopped) {
		t.Fatalf("err = %v", err)
	}
}

func TestWrongKeyThreeTimes(t *testing.T) {
	got := marks(t)
	h := &fakeHelper{answers: map[string][]any{
		"find":  {movehelper.FindResult{Disks: []oldroot.Part{encDisk}}},
		"check": {movehelper.CheckResult{Encrypted: true}},
		"open":  {refusal(oldroot.WrongKey)},
	}}
	_, out, err := run(t, h, "y\na\nb\nc\nd\n")
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("err = %v", err)
	}
	if h.ops() != "find check open open open" {
		t.Fatalf("ops = %q", h.ops())
	}
	for _, want := range []string{"2 tries left.", "One try left.", "The old disk stays locked."} {
		if !strings.Contains(out, want) {
			t.Errorf("screen lacks %q:\n%s", want, out)
		}
	}
	if m := got(); !strings.HasSuffix(m, "GJA-2107 stick.move.unlock-failed tries=3") {
		t.Errorf("marks:\n%s", m)
	}
}

func TestWrongKeyThenRight(t *testing.T) {
	got := marks(t)
	h := &fakeHelper{answers: map[string][]any{
		"find":  {movehelper.FindResult{Disks: []oldroot.Part{encDisk}}},
		"check": {movehelper.CheckResult{Encrypted: true}},
		"open":  {refusal(oldroot.WrongKey), opened},
	}}
	if _, _, err := run(t, h, "y\nwrong\n"+strings.Repeat("k", maxKey+1)+"\nright\n"); err != nil {
		t.Fatal(err)
	}
	// The over-long answer was not sent and not counted.
	if len(h.keys) != 4 || h.keys[2] != "wrong" || h.keys[3] != "right" {
		t.Fatalf("keys sent = %d", len(h.keys))
	}
	if m := got(); !strings.HasSuffix(m, "GJA-2106 stick.move.unlock-ok tries=2") {
		t.Errorf("marks:\n%s", m)
	}
}

func TestPINGoBackCloses(t *testing.T) {
	got := marks(t)
	withPIN := opened
	withPIN.PINToken = true
	h := &fakeHelper{answers: map[string][]any{
		"find":  {movehelper.FindResult{Disks: []oldroot.Part{encDisk}}},
		"check": {movehelper.CheckResult{Encrypted: true, PINToken: true}},
		"open":  {withPIN},
		"close": {struct{}{}},
	}}
	_, out, err := run(t, h, "y\nkey\n\n")
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("err = %v", err)
	}
	if h.ops() != "find check open close" {
		t.Fatalf("ops = %q", h.ops())
	}
	if !strings.Contains(out, "gjallarctl move prepare") {
		t.Errorf("notice:\n%s", out)
	}
	if strings.Contains(got(), "GJA-2110") {
		t.Error("going back marked the PIN as accepted")
	}
}

func TestPINContinue(t *testing.T) {
	got := marks(t)
	withPIN := opened
	withPIN.PINToken = true
	h := &fakeHelper{answers: map[string][]any{
		"find":  {movehelper.FindResult{Disks: []oldroot.Part{encDisk}}},
		"check": {movehelper.CheckResult{Encrypted: true, PINToken: true}},
		"open":  {withPIN},
	}}
	old, _, err := run(t, h, "y\nkey\n2\n")
	if err != nil {
		t.Fatal(err)
	}
	if !old.PINUnchecked {
		t.Error("PINUnchecked not set")
	}
	if m := got(); !strings.HasSuffix(m, "GJA-2110 stick.move.pin-unchecked") {
		t.Errorf("marks:\n%s", m)
	}
}

func TestEncryptionChangedBetweenFindAndCheck(t *testing.T) {
	h := &fakeHelper{answers: map[string][]any{
		"find":  {movehelper.FindResult{Disks: []oldroot.Part{plainDisk}}},
		"check": {movehelper.CheckResult{Encrypted: true}},
	}}
	if _, _, err := run(t, h, "y\n"); err == nil || errors.Is(err, ErrStopped) {
		t.Fatalf("err = %v", err)
	}
	if h.ops() != "find check" {
		t.Fatalf("ops = %q", h.ops())
	}
}

func TestUnknownMode(t *testing.T) {
	f := Flow{UI: prompt.New(strings.NewReader(""), &bytes.Buffer{}), Helper: &fakeHelper{}, Mode: "other"}
	if _, err := f.OpenOld(context.Background()); err == nil {
		t.Fatal("unknown mode accepted")
	}
}
