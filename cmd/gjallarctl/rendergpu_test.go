package main

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

func TestScreenEvent(t *testing.T) {
	for line, want := range map[string]bool{
		"monitoraddedv2>>2,DP-4,Dell U2720Q": true,
		"monitoradded>>DP-4":                 true,
		"monitorremovedv2>>2,DP-4,Dell":      true,
		"workspace>>2":                       false,
		"configreloaded>>":                   false,
	} {
		if got := screenEvent(line); got != want {
			t.Errorf("%q: %v, want %v", line, got, want)
		}
	}
}

// fakeCards counts the cards renderGPUWatch shows and which are still open.
type fakeCards struct {
	mu    sync.Mutex
	shown int
	open  int
}

func (f *fakeCards) ask(ctx context.Context) bool {
	f.mu.Lock()
	f.shown++
	f.open++
	f.mu.Unlock()
	<-ctx.Done()
	f.mu.Lock()
	f.open--
	f.mu.Unlock()
	return false
}

// waitFor gives the card goroutines up to a second to reach the counts.
func (f *fakeCards) waitFor(shown, open int) (int, int) {
	deadline := time.Now().Add(time.Second)
	for {
		f.mu.Lock()
		s, o := f.shown, f.open
		f.mu.Unlock()
		if s == shown && o == open || time.Now().After(deadline) {
			return s, o
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRenderGPUWatchAsksOncePerChoice(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cards := &fakeCards{}
	want, ok := "", true
	w := renderGPUWatch{
		current: "/dev/dri/card1",
		wanted:  func() (string, bool, error) { return want, ok, nil },
		ask:     cards.ask,
	}
	step := func(next string, nextOK bool, shown, open int) {
		t.Helper()
		want, ok = next, nextOK
		w.check(ctx, io.Discard)
		if s, o := cards.waitFor(shown, open); s != shown || o != open {
			t.Fatalf("after %q: shown %d open %d, want %d %d", next, s, o, shown, open)
		}
	}
	step("/dev/dri/card1", true, 0, 0)                // same GPU as at login
	step("/dev/dri/card0:/dev/dri/card1", true, 1, 1) // screen on the other GPU
	step("/dev/dri/card0:/dev/dri/card1", true, 1, 1) // same choice again: no second card
	step("/dev/dri/card1", true, 1, 0)                // unplugged: card goes
	step("/dev/dri/card0:/dev/dri/card1", true, 2, 1) // plugged again: asks again
	step("", false, 2, 0)                             // battery saver: card goes
	step("/dev/dri/card0:/dev/dri/card1", true, 3, 1) // back on mains: asks again

	// The service stops: the card must be gone before close returns.
	w.close()
	cards.mu.Lock()
	defer cards.mu.Unlock()
	if cards.open != 0 {
		t.Fatalf("after close: %d cards open", cards.open)
	}
}
