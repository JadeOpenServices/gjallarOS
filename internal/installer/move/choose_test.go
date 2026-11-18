package move

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/freespace"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/identity"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/oldroot"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/movehelper"
)

const gb = 1 << 30

// target2T is a 2 TB disk with a recovery partition; the closure numbers
// are the fixture's own.
var target2T = Target{DiskBytes: 2000398934016, Recovery: true, BaseClosure: 30 * gb, StoreImport: 40 * gb, FileCache: 40 * gb}

func choose(t *testing.T, h *fakeHelper, mode string, old *Old, tg Target, input string) (Choices, string, error) {
	t.Helper()
	var out bytes.Buffer
	f := Flow{UI: prompt.New(strings.NewReader(input), &out), Term: pty(t), Helper: h, Mode: mode}
	c, err := f.Choose(context.Background(), old, tg)
	return c, out.String(), err
}

func sizesOf(s freespace.Sizes) movehelper.SizesResult { return movehelper.SizesResult{Sizes: s} }
func invOf(i identity.Inventory) movehelper.InventoryResult {
	return movehelper.InventoryResult{Inventory: i}
}

// spaceOK is the space-ok line the check writes for cats on tg.
func spaceOK(tg Target, cats ...freespace.Category) string {
	r := freespace.Plan(freespace.Input{DiskBytes: tg.DiskBytes, Recovery: tg.Recovery, BaseClosure: tg.BaseClosure,
		StoreImport: tg.StoreImport, FileCache: tg.FileCache, Categories: cats})
	return fmt.Sprintf("GJA-2111 stick.move.space-ok needed=%d free=%d", gib(r.Needed, true), gib(r.Available, false))
}

// Everything fits; every screen answered with Enter keeps the defaults.
// Pairings with another adapter and unread files are named, never offered.
func TestChooseFits(t *testing.T) {
	got := marks(t)
	h := &fakeHelper{answers: map[string][]any{
		"sizes":     {sizesOf(freespace.Sizes{Home: 300 * gb, Branches: 1 * gb})},
		"inventory": {invOf(identity.Inventory{WiFi: 3, VPN: 1, Bluetooth: 2, BluetoothOther: 1, Fingerprints: 1, MachineID: true, Skipped: 2})},
	}}
	c, out, err := choose(t, h, NewDisk, &Old{}, target2T, strings.Repeat("\n", 5))
	if err != nil {
		t.Fatal(err)
	}
	if h.ops() != "sizes inventory" {
		t.Fatalf("ops %q", h.ops())
	}
	if !c.Chosen(freespace.Home) || !c.Chosen(freespace.Branches) || c.Chosen(freespace.Nextcloud) || !c.Space.Fits || c.NextcloudSkipped {
		t.Fatalf("choices %+v", c)
	}
	if len(c.Identity) != 5 {
		t.Fatalf("identity %+v", c.Identity)
	}
	for _, ch := range c.Identity {
		if !ch.Keep {
			t.Fatalf("identity %+v", c.Identity)
		}
	}
	want := strings.Join([]string{
		spaceOK(target2T, freespace.Category{Name: freespace.Home, Bytes: 300 * gb, Chosen: true}, freespace.Category{Name: freespace.Branches, Bytes: gb, Chosen: true}),
		"GJA-2114 stick.move.identity-choice item=wifi answer=keep",
		"GJA-2114 stick.move.identity-choice item=vpn answer=keep",
		"GJA-2114 stick.move.identity-choice item=bluetooth answer=keep",
		"GJA-2114 stick.move.identity-choice item=fingerprints answer=keep",
		"GJA-2114 stick.move.identity-choice item=machine-id answer=keep",
	}, "\n")
	if m := got(); m != want {
		t.Fatalf("marks\n%s\nwant\n%s", m, want)
	}
	for _, s := range []string{"Your files (home folder): 322.1 GB", "Saved Wi-Fi networks (3)", "1 Bluetooth pairings were made with an adapter", "2 settings files could not be read safely", identity.RiskLine} {
		if !strings.Contains(out, s) {
			t.Errorf("screen lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "Nextcloud") {
		t.Errorf("Nextcloud shown with none on the disk:\n%s", out)
	}
}

// An unchecked startup PIN makes the risk items default to new; Enter
// takes that default, a typed number overrides it either way.
func TestChoosePINUnchecked(t *testing.T) {
	got := marks(t)
	h := &fakeHelper{answers: map[string][]any{
		"sizes":     {sizesOf(freespace.Sizes{Home: 10 * gb})},
		"inventory": {invOf(identity.Inventory{WiFi: 1, Tailscale: true, SSHHostKeys: 3, MachineID: true})},
	}}
	c, _, err := choose(t, h, NewDisk, &Old{PINUnchecked: true}, target2T, "2\n\n1\n\n")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		spaceOK(target2T, freespace.Category{Name: freespace.Home, Bytes: 10 * gb, Chosen: true}),
		"GJA-2114 stick.move.identity-choice item=wifi answer=new",
		"GJA-2114 stick.move.identity-choice item=tailscale answer=new",
		"GJA-2114 stick.move.identity-choice item=ssh-host-keys answer=keep",
		"GJA-2114 stick.move.identity-choice item=machine-id answer=new",
	}, "\n")
	if m := got(); m != want {
		t.Fatalf("marks\n%s\nwant\n%s", m, want)
	}
	if len(c.Identity) != 4 || c.Identity[2].Keep != true || c.Identity[1].Keep {
		t.Fatalf("identity %+v", c.Identity)
	}
}

// Bringing an old disk to another computer: the fingerprints belong to
// the old reader and get no screen.
func TestChooseBringOverNoFingerprints(t *testing.T) {
	marks(t)
	h := &fakeHelper{answers: map[string][]any{
		"sizes":     {sizesOf(freespace.Sizes{Home: 10 * gb})},
		"inventory": {invOf(identity.Inventory{Fingerprints: 2, WiFi: 1})},
	}}
	c, out, err := choose(t, h, BringOver, &Old{}, target2T, "\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Identity) != 1 || c.Identity[0].Item != identity.WiFi || strings.Contains(out, "fingerprints") {
		t.Fatalf("identity %+v\n%s", c.Identity, out)
	}
}

// A big Nextcloud: the note asks first. Copy keeps it, skip drops it from
// the move and from the space check.
func TestChooseNextcloudNote(t *testing.T) {
	for _, c := range []struct {
		input, answer string
		chosen        bool
	}{
		{"\n\n", "copy", true},
		{"2\n\n", "skip", false},
	} {
		got := marks(t)
		h := &fakeHelper{answers: map[string][]any{
			"sizes":     {sizesOf(freespace.Sizes{Home: 100 * gb, Nextcloud: 60 * gb, SyncFolders: 2})},
			"inventory": {invOf(identity.Inventory{Nextcloud: true})},
		}}
		ch, out, err := choose(t, h, NewDisk, &Old{}, target2T, c.input)
		if err != nil {
			t.Fatal(err)
		}
		if ch.Chosen(freespace.Nextcloud) != c.chosen || ch.NextcloudSkipped == c.chosen {
			t.Fatalf("%s: choices %+v", c.answer, ch)
		}
		cats := []freespace.Category{{Name: freespace.Home, Bytes: 100 * gb, Chosen: true}, {Name: freespace.Nextcloud, Bytes: 60 * gb, Chosen: c.chosen}}
		want := strings.Join([]string{
			"GJA-2113 stick.move.nextcloud-warned answer=" + c.answer,
			spaceOK(target2T, cats...),
			"GJA-2114 stick.move.identity-choice item=nextcloud answer=keep",
		}, "\n")
		if m := got(); m != want {
			t.Fatalf("%s: marks\n%s\nwant\n%s", c.answer, m, want)
		}
		if !strings.Contains(out, "Your Nextcloud folders take 64.4 GB") {
			t.Fatalf("%s: screen\n%s", c.answer, out)
		}
	}
}

// small is a 512 GB disk without a recovery partition.
var small = Target{DiskBytes: 512110190592, BaseClosure: 30 * gb}

// Too much for the disk: the stick names the biggest choices. Leaving the
// home behind also leaves the Nextcloud folders inside it, and the
// Nextcloud account screen goes away.
func TestChooseShortLeaveHome(t *testing.T) {
	got := marks(t)
	h := &fakeHelper{answers: map[string][]any{
		"sizes":     {sizesOf(freespace.Sizes{Home: 900 * gb, Nextcloud: 5 * gb, Branches: 1 * gb, SyncFolders: 1})},
		"inventory": {invOf(identity.Inventory{WiFi: 1, Nextcloud: true})},
	}}
	c, out, err := choose(t, h, NewDisk, &Old{}, small, "1\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.Chosen(freespace.Home) || c.Chosen(freespace.Nextcloud) || !c.Chosen(freespace.Branches) || !c.Space.Fits {
		t.Fatalf("choices %+v", c)
	}
	short := freespace.Plan(freespace.Input{DiskBytes: small.DiskBytes, BaseClosure: small.BaseClosure, Categories: []freespace.Category{
		{Name: freespace.Home, Bytes: 900 * gb, Chosen: true}, {Name: freespace.Nextcloud, Bytes: 5 * gb, Chosen: true}, {Name: freespace.Branches, Bytes: gb, Chosen: true},
	}}).Short
	want := strings.Join([]string{
		fmt.Sprintf("GJA-2112 stick.move.space-short short=%d", gib(short, true)),
		spaceOK(small, freespace.Category{Name: freespace.Branches, Bytes: gb, Chosen: true}),
		"GJA-2114 stick.move.identity-choice item=wifi answer=keep",
	}, "\n")
	if m := got(); m != want {
		t.Fatalf("marks\n%s\nwant\n%s", m, want)
	}
	if !strings.Contains(out, "Leave behind: Your files (home folder) (966.4 GB), with the Nextcloud folders inside it") || strings.Contains(out, "Nextcloud account") {
		t.Fatalf("screen\n%s", out)
	}
	if h.ops() != "sizes inventory" {
		t.Fatalf("ops %q", h.ops())
	}
}

// Stop at the space check closes the old disk.
func TestChooseShortStop(t *testing.T) {
	marks(t)
	h := &fakeHelper{answers: map[string][]any{
		"sizes":     {sizesOf(freespace.Sizes{Home: 900 * gb})},
		"inventory": {invOf(identity.Inventory{WiFi: 1})},
		"close":     {struct{}{}},
	}}
	_, _, err := choose(t, h, NewDisk, &Old{}, small, "2\n")
	if !errors.Is(err, ErrStopped) || h.ops() != "sizes inventory close" {
		t.Fatalf("%v, ops %q", err, h.ops())
	}
}

// A disk too small for gjallarOS itself: nothing to leave behind, stop.
func TestChooseDiskTooSmall(t *testing.T) {
	got := marks(t)
	h := &fakeHelper{answers: map[string][]any{
		"sizes":     {sizesOf(freespace.Sizes{})},
		"inventory": {invOf(identity.Inventory{})},
		"close":     {struct{}{}},
	}}
	_, out, err := choose(t, h, NewDisk, &Old{}, Target{DiskBytes: 32 * gb, BaseClosure: 30 * gb}, "")
	if !errors.Is(err, ErrStopped) || h.ops() != "sizes inventory close" || !strings.Contains(out, "too small even for gjallarOS itself") {
		t.Fatalf("%v, ops %q\n%s", err, h.ops(), out)
	}
	if m := got(); !strings.HasPrefix(m, "GJA-2112 stick.move.space-short short=") {
		t.Fatalf("marks %q", m)
	}
}

// The old disk went away under the stick: the refusal is logged and
// passed on; a plain helper error passes through.
func TestChooseRefused(t *testing.T) {
	got := marks(t)
	h := &fakeHelper{answers: map[string][]any{"sizes": {refusal(oldroot.NotOpen)}}}
	_, _, err := choose(t, h, NewDisk, &Old{}, target2T, "")
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("err %v", err)
	}
	if m := got(); m != "GJA-2108 stick.move.old-refused reason=not-open" {
		t.Fatalf("marks %q", m)
	}

	broken := errors.New("helper broke")
	h = &fakeHelper{answers: map[string][]any{"sizes": {sizesOf(freespace.Sizes{})}, "inventory": {broken}}}
	if _, _, err := choose(t, h, NewDisk, &Old{}, target2T, ""); !errors.Is(err, broken) {
		t.Fatalf("err %v", err)
	}
}

// A crafted size that would not fit an int64 shows as the largest, and the
// GiB counts never wrap.
func TestChooseHugeSizes(t *testing.T) {
	if s := usize(^uint64(0)); s != size(1<<63-1) {
		t.Fatalf("usize %q", s)
	}
	if gib(^uint64(0), true) != 1<<31-1 || gib(gb+1, true) != 2 || gib(2*gb-1, false) != 1 {
		t.Fatal("gib")
	}
}
