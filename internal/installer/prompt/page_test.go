package prompt

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/calmmenu"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
	"github.com/JadeOpenServices/gjallarOS/internal/tty"
)

// SYNTHETIC recovery material.
const (
	testArchive = "/run/example/secureboot-recovery.tar.age"
	testSecret  = "example-recovery-passphrase-0000"
)

// logged stands for the install log's writer: it keeps a copy of what
// passes through and has the terminal writer behind it.
type logged struct {
	copy bytes.Buffer
	term io.Writer
}

func (l *logged) Write(b []byte) (int, error) {
	l.copy.Write(b)
	return l.term.Write(b)
}

func (l *logged) Terminal() io.Writer { return l.term }

// testPTY returns both ends of a new pseudo-terminal and what the master
// side reads, complete once the terminal side is closed.
func testPTY(t *testing.T) (master, term *os.File, screen func() string) {
	t.Helper()
	master, term = openPTY(t)
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&out, master)
		close(done)
	}()
	return master, term, func() string {
		term.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("terminal output did not end")
		}
		return out.String()
	}
}

// openPTY opens a pseudo-terminal pair with echo off.
func openPTY(t *testing.T) (master, term *os.File) {
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
	term = os.NewFile(fd, "pty")
	t.Cleanup(func() { term.Close() })
	// No echo, so the master reads only what the program writes.
	tio, err := unix.IoctlGetTermios(int(term.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	tio.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(int(term.Fd()), unix.TCSETS, tio); err != nil {
		t.Fatal(err)
	}
	return master, term
}

// Item 1: the passphrase never reaches a file that is not a terminal,
// with the Pager and a logging writer around it.
func TestPagedRecoveryKeepsSecretOutOfFiles(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	log := &logged{term: newPager(file, "Install gjallarOS")}
	// Without /dev/tty (as in most test sandboxes) the call fails; with
	// one the secret goes there.
	_ = UI{Out: log}.ShowSecureBootRecovery(context.Background(), testArchive, testSecret)
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), testSecret) || strings.Contains(log.copy.String(), testSecret) {
		t.Fatalf("recovery passphrase written past the terminal check: file %q, log %q", data, log.copy.String())
	}
}

// Item 2: the passphrase stays on screen while the two confirmations are
// asked under it, and no writer on the way keeps it.
func TestPagedRecoveryStaysOnScreen(t *testing.T) {
	_, term, screen := testPTY(t)
	p := newPager(term, "Install gjallarOS")
	log := &logged{term: p}
	u := UI{Reader: bufio.NewReader(strings.NewReader("y\ny\nname\n")), Out: log, Wrap: true}
	ctx := context.Background()
	if err := u.ShowSecureBootRecovery(ctx, testArchive, testSecret); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"Saved both items?", "Absolutely sure?"} {
		if ok, err := u.Confirm(ctx, q, false); err != nil || !ok {
			t.Fatalf("Confirm(%q) = %v, %v", q, ok, err)
		}
	}
	if _, err := u.Value(ctx, "Next page", ""); err != nil {
		t.Fatal(err)
	}
	col := calmmenu.NewColumn(io.Discard, tty.Size(term)).Col
	out := screen()
	if at := fmt.Sprintf("\x1b[%dG%s", col, testSecret); !strings.Contains(out, at) {
		t.Fatalf("secret not in the page column (want %q):\n%q", at, out)
	}
	secret := strings.Index(out, testSecret)
	second := strings.Index(out, "Absolutely sure?")
	next := strings.Index(out, "Next page")
	if secret < 0 || second < secret || next < second {
		t.Fatalf("screen order wrong (secret %d, second question %d, next %d):\n%q", secret, second, next, out)
	}
	if strings.Contains(out[secret:second], calmmenu.Clear) {
		t.Fatalf("screen cleared between the secret and the second question:\n%q", out)
	}
	if !strings.Contains(out[second:next], calmmenu.Clear) {
		t.Fatalf("no new page after the second answer:\n%q", out)
	}
	if strings.Contains(log.copy.String(), testSecret) {
		t.Fatalf("a writer around the Pager kept the secret: %q", log.copy.String())
	}
}

// Item 3: the install log of a paged flow holds the text and no escape
// codes.
func TestPagedInstallLogHasNoEscapes(t *testing.T) {
	_, term, screen := testPTY(t)
	log, err := installlog.Open(filepath.Join(t.TempDir(), "log"), time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	u := New(strings.NewReader("example-host\ny\n"), log.Tee(newPager(term, "Install gjallarOS")))
	u.Wrap = true
	ctx := context.Background()
	if _, err := u.Value(ctx, "Computer name", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Confirm(ctx, "Go on with the install?", false); err != nil {
		t.Fatal(err)
	}
	if out := screen(); strings.Count(out, calmmenu.Clear) < 2 {
		t.Fatalf("flow drew no pages:\n%q", out)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Go on with the install?") {
		t.Fatalf("log misses the question:\n%s", data)
	}
	if bytes.IndexByte(data, 0x1b) >= 0 {
		t.Fatalf("install log holds escape codes:\n%q", data)
	}
}

// Item 5: in page mode keys typed while no question was up are dropped
// before every question.
func TestPagedFlushBeforeEveryQuestion(t *testing.T) {
	master, term, _ := testPTY(t)
	u := New(term, &logged{term: newPager(term, "Install gjallarOS")})
	u.UsePages(term)
	if u.Flush == nil {
		t.Fatal("UsePages did not set up page mode")
	}
	flush := u.Flush
	asked := 0
	// Each flush first drops a line typed early, then the real answer
	// comes, as on the stick.
	u.Flush = func(reset bool) {
		asked++
		flush(reset)
		if _, err := master.Write([]byte("answer\n")); err != nil {
			t.Error(err)
		}
	}
	if _, err := master.Write([]byte("typed early\n")); err != nil {
		t.Fatal(err)
	}
	// Wait until the early line sits in the terminal's input.
	for i := 0; ; i++ {
		n, err := unix.IoctlGetInt(int(term.Fd()), unix.TIOCINQ)
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			break
		}
		if i > 500 {
			t.Fatal("typed line never arrived")
		}
		time.Sleep(time.Millisecond)
	}
	ctx := context.Background()
	for _, q := range []string{"First", "Second"} {
		got, err := u.Value(ctx, q, "")
		if err != nil {
			t.Fatal(err)
		}
		if got != "answer" {
			t.Fatalf("%s question took %q, want the line typed after it came up", q, got)
		}
		if _, err := master.Write([]byte("typed early\n")); err != nil {
			t.Fatal(err)
		}
	}
	if asked != 2 {
		t.Fatalf("flushed %d times for 2 questions", asked)
	}
}

// Item 6: page mode is opt-in, needs a terminal, and is never passed on.
func TestPagesOptIn(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	errOut := io.Discard

	t.Setenv(PageEnv, "")
	os.Unsetenv(PageEnv)
	if out, e := Pages(os.Stdout, errOut); out != io.Writer(os.Stdout) || e != errOut {
		t.Fatal("Pages changed the output without the variable")
	}

	t.Setenv(PageEnv, "1")
	t.Setenv(PageTitleEnv, "Install gjallarOS")
	if out, e := Pages(file, errOut); out != io.Writer(file) || e != errOut || Paged(out) {
		t.Fatal("Pages drew pages on a file that is not a terminal")
	}
	if _, set := os.LookupEnv(PageEnv); set {
		t.Fatal("Pages left the variable for programs it starts")
	}

	_, term, _ := testPTY(t)
	t.Setenv(PageEnv, "1")
	t.Setenv(PageTitleEnv, "Install gjallarOS")
	t.Setenv(PageFrameEnv, "5 112")
	out, e := Pages(term, term)
	if !Paged(out) || !Paged(e) {
		t.Fatal("Pages without pages on a terminal with the variable set")
	}
	if pagerOf(e).file != term || terminal(e) != io.Writer(term) {
		t.Fatal("errOut on the same terminal does not share the page")
	}
	for _, v := range []string{PageEnv, PageTitleEnv, PageFrameEnv} {
		if _, set := os.LookupEnv(v); set {
			t.Fatalf("Pages left %s for programs it starts", v)
		}
	}
}

func TestPageEnviron(t *testing.T) {
	env := []string{"PATH=/bin", PageEnv + "=1", PageTitleEnv + "=old", PageFrameEnv + "=1 66", "LANG=C"}
	sz := calmmenu.Size{Cols: 120, Rows: 33}
	got := PageEnviron(context.Background(), env, sz)
	if strings.Join(got, " ") != "PATH=/bin LANG=C" {
		t.Fatalf("plain ctx: %q", got)
	}
	got = PageEnviron(WithPage(context.Background(), "Install gjallarOS"), env, sz)
	// The frame of 120x33 (see TestFrameFor): column 5, 112 wide.
	want := "PATH=/bin LANG=C " + PageEnv + "=1 " + PageTitleEnv + "=Install gjallarOS " + PageFrameEnv + "=5 112"
	if strings.Join(got, " ") != want {
		t.Fatalf("page ctx: %q", got)
	}
}

func TestPageTitleNamesAPageWithoutOne(t *testing.T) {
	if got := pageTitle(""); got != "gjallarOS" {
		t.Fatalf("empty title = %q", got)
	}
}

// A question given up on (Ctrl-C) leaves its read running. The next
// question takes over that read instead of resetting the Reader under it,
// and in page mode a line typed in between does not answer it.
func TestAbandonedReadIsTakenOver(t *testing.T) {
	in, typed := io.Pipe()
	defer typed.Close()
	u := UI{Reader: bufio.NewReader(in), Out: io.Discard}
	flushed, resets := 0, 0
	u.Flush = func(reset bool) {
		flushed++
		if reset {
			resets++
			u.Reader.Reset(in)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := u.Value(ctx, "First", ""); err == nil {
		t.Fatal("cancelled question returned an answer")
	}
	// Still no question up: this line is typed early.
	go func() { _, _ = typed.Write([]byte("typed early\n")) }()
	for i := 0; ; i++ {
		abandoned.Lock()
		done := abandoned.reads[u.Reader]
		abandoned.Unlock()
		if done != nil && len(done) == 1 {
			break
		}
		if i > 500 {
			t.Fatal("early line never reached the left read")
		}
		time.Sleep(time.Millisecond)
	}
	go func() { _, _ = typed.Write([]byte("answer\n")) }()
	got, err := u.Value(context.Background(), "Second", "")
	if err != nil || got != "answer" {
		t.Fatalf("second question = %q, %v; want the line typed after it came up", got, err)
	}
	if flushed != 2 || resets != 2 {
		t.Fatalf("flushed %d times with %d resets, want 2 and 2 (first question, and after the early line was dropped)", flushed, resets)
	}

	// Without a line in between the next question takes the left read
	// as it is: Flush runs without resetting the Reader under it.
	ctx, cancel = context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := u.Value(ctx, "Third", ""); err == nil {
		t.Fatal("cancelled question returned an answer")
	}
	// Typed once the question is up; a line there earlier would be
	// dropped as typed early.
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = typed.Write([]byte("later\n"))
	}()
	got, err = u.Value(context.Background(), "Fourth", "")
	if err != nil || got != "later" || flushed != 4 || resets != 3 {
		t.Fatalf("fourth question = %q, %v, flushed %d with %d resets; want \"later\", 4 and 3: no reset under the left read", got, err, flushed, resets)
	}
}

// Keys typed without Enter while no question was up do not end up in
// front of the answer when the next question takes over a left read.
func TestTakeoverDropsHalfTypedKeys(t *testing.T) {
	master, term := openPTY(t)
	var mu sync.Mutex
	var out bytes.Buffer
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			mu.Lock()
			out.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	shown := func(s string) bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(out.String(), s)
	}
	u := New(term, &logged{term: newPager(term, "Install gjallarOS")})
	u.UsePages(term)
	if u.Flush == nil {
		t.Fatal("UsePages did not set up page mode")
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := u.Value(ctx, "First", ""); err == nil {
		t.Fatal("cancelled question returned an answer")
	}
	// Echo shows when the terminal has taken the keys into its line.
	tio, err := unix.IoctlGetTermios(int(term.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	tio.Lflag |= unix.ECHO
	if err := unix.IoctlSetTermios(int(term.Fd()), unix.TCSETS, tio); err != nil {
		t.Fatal(err)
	}
	if _, err := master.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	for i := 0; !shown("abc"); i++ {
		if i > 500 {
			t.Fatal("typed keys never arrived")
		}
		time.Sleep(time.Millisecond)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = master.Write([]byte("yes\n"))
	}()
	got, err := u.Value(context.Background(), "Second", "")
	if err != nil || got != "yes" {
		t.Fatalf("second question = %q, %v; want \"yes\" without the keys typed before it", got, err)
	}
}
