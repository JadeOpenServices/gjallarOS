package prompt

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// echoPTY is a pseudo-terminal with echo on, as a console starts.
func echoPTY(t *testing.T) (master, term *os.File, screen func() string) {
	t.Helper()
	master, term, screen = testPTY(t)
	tio, err := unix.IoctlGetTermios(int(term.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	tio.Lflag |= unix.ECHO
	if err := unix.IoctlSetTermios(int(term.Fd()), unix.TCSETS, tio); err != nil {
		t.Fatal(err)
	}
	return master, term, screen
}

func echoOn(t *testing.T, f *os.File) bool {
	t.Helper()
	tio, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	return tio.Lflag&unix.ECHO != 0
}

func TestSecretHidesTheAnswer(t *testing.T) {
	master, term, screen := echoPTY(t)
	var out bytes.Buffer
	u := New(term, &out)
	got := make(chan string, 1)
	go func() {
		line, err := u.Secret(context.Background(), "Recovery key", term)
		if err != nil {
			t.Error(err)
		}
		got <- line
	}()
	// Type only once echo is off, as a person would after the question.
	deadline := time.Now().Add(5 * time.Second)
	for echoOn(t, term) {
		if time.Now().After(deadline) {
			t.Fatal("echo never went off")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := master.WriteString("not-shown-123\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-got:
		if line != "not-shown-123" {
			t.Fatalf("answer = %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer read")
	}
	if !echoOn(t, term) {
		t.Error("echo stayed off after the answer")
	}
	if out.String() != "Recovery key: " {
		t.Errorf("out = %q", out.String())
	}
	echoed := screen()
	if strings.Contains(echoed, "not-shown") {
		t.Errorf("the terminal showed the answer: %q", echoed)
	}
	if !strings.Contains(echoed, "\n") {
		t.Errorf("Enter did not move to the next line: %q", echoed)
	}
}

func TestSecretGivenUpRestoresEcho(t *testing.T) {
	_, term, _ := echoPTY(t)
	u := New(term, &bytes.Buffer{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := u.Secret(ctx, "Recovery key", term)
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for echoOn(t, term) {
		if time.Now().After(deadline) {
			t.Fatal("echo never went off")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("a question given up on returned no error")
	}
	if !echoOn(t, term) {
		t.Error("echo stayed off after the question was given up on")
	}
}

func TestSecretRefusesNonTerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	u := New(r, &bytes.Buffer{})
	if _, err := u.Secret(context.Background(), "Recovery key", r); err == nil {
		t.Fatal("a pipe was taken as a terminal")
	}
}
