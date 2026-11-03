package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

// EXAMPLE mountinfo lines; device numbers and paths are made up.
const mediaExampleMountInfo = `22 1 0:21 / / rw,relatime shared:1 - btrfs /dev/mapper/root rw
40 22 8:1 / /run/media/alex/EXAMPLE ro,nosuid,nodev,relatime shared:20 - vfat /dev/sda1 ro,uid=1000
41 22 8:17 / /run/media/alex/MY\040DISK rw,nosuid,nodev,relatime shared:21 - ntfs3 /dev/sdb1 rw,uid=1000
42 22 8:33 / /run/media/alex/OLD rw,nosuid shared:22 - exfat /dev/sdc1 rw
43 22 8:33 / /run/media/alex/OLD ro,nosuid shared:23 - exfat /dev/sdc1 ro
`

func TestMediaMountReadOnly(t *testing.T) {
	cases := map[string]bool{
		"/run/media/alex/EXAMPLE": true,
		"/run/media/alex/MY DISK": false,
		"/run/media/alex/OLD":     true, // the later mount covers the earlier one
		"/run/media/alex/GONE":    false,
		"/":                       false,
	}
	for point, want := range cases {
		if got := mediaMountReadOnly(mediaExampleMountInfo, point); got != want {
			t.Errorf("%q: got %v, want %v", point, got, want)
		}
	}
}

func TestMediaUnescape(t *testing.T) {
	cases := map[string]string{
		`MY\040DISK`: "MY DISK",
		`a\011b`:     "a\tb",
		`back\134`:   `back\`,
		`plain`:      "plain",
		`short\04`:   `short\04`,
		`bad\089`:    `bad\089`,
	}
	for in, want := range cases {
		if got := mediaUnescape(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestMediaMountPoints(t *testing.T) {
	v := dbus.MakeVariant([][]byte{[]byte("/run/media/alex/EXAMPLE\x00"), []byte("\x00")})
	got := mediaMountPoints(v)
	if len(got) != 1 || got[0] != "/run/media/alex/EXAMPLE" {
		t.Fatalf("got %q", got)
	}
	if mediaMountPoints(dbus.MakeVariant("not aay")) != nil {
		t.Fatal("wrong type gives points")
	}
}

func TestMediaPointUnder(t *testing.T) {
	dir := "/run/media/alex"
	if p := mediaPointUnder([]string{"/mnt/x", "/run/media/alex/EXAMPLE"}, dir); p != "/run/media/alex/EXAMPLE" {
		t.Fatalf("got %q", p)
	}
	for _, points := range [][]string{
		{"/run/media/other/EXAMPLE"},
		{"/run/media/alex"},
		{"/run/media/alex/a/b"},
		{"/run/media/alexander/EXAMPLE"},
		{"/run/media/alex/../other/EXAMPLE"},
	} {
		if p := mediaPointUnder(points, dir); p != "" {
			t.Errorf("%q: got %q", points, p)
		}
	}
}

func TestMediaOffer(t *testing.T) {
	dir := "/run/media/alex"
	points := []string{"/run/media/alex/EXAMPLE"}
	ok := mediaDisk{Type: "vfat"}
	if !mediaOffer(ok, points, dir, mediaExampleMountInfo) {
		t.Fatal("read-only FAT under the media dir gets no offer")
	}
	for name, d := range map[string]mediaDisk{
		"ext4":            {Type: "ext4"},
		"read-only stick": {Type: "vfat", ReadOnly: true},
	} {
		if mediaOffer(d, points, dir, mediaExampleMountInfo) {
			t.Errorf("%s gets an offer", name)
		}
	}
	if mediaOffer(mediaDisk{Type: "ntfs"}, []string{"/run/media/alex/MY DISK"}, dir, mediaExampleMountInfo) {
		t.Error("writable mount gets an offer")
	}
	if mediaOffer(ok, []string{"/mnt/EXAMPLE"}, dir, mediaExampleMountInfo) {
		t.Error("mount outside the media dir gets an offer")
	}
	for _, fs := range []string{"vfat", "exfat", "ntfs", "hfsplus"} {
		if !mediaForeignTypes[fs] {
			t.Errorf("%s missing; it must match system/services/foreign-media-readonly.nix", fs)
		}
	}
}

func TestMediaName(t *testing.T) {
	if got := mediaName("  "); got != "Disk" {
		t.Errorf("empty label: %q", got)
	}
	if got := mediaName("A\x1b[31mB\nC"); strings.ContainsAny(got, "\x1b\n") {
		t.Errorf("control characters kept: %q", got)
	}
	long := strings.Repeat("ä", 40)
	if got := []rune(mediaName(long)); len(got) != 33 || got[32] != '…' {
		t.Errorf("long label: %q", string(got))
	}
}

func TestMediaBusy(t *testing.T) {
	if !mediaBusy(dbus.Error{Name: "org.freedesktop.UDisks2.Error.DeviceBusy"}) {
		t.Error("DeviceBusy not busy")
	}
	if !mediaBusy(errors.New("Error unmounting /dev/sda1: target is busy")) {
		t.Error("target is busy not busy")
	}
	if mediaBusy(dbus.Error{Name: "org.freedesktop.UDisks2.Error.NotAuthorizedDismissed"}) {
		t.Error("dismissed password is busy")
	}
}

func TestMediaCardsKeepNewerCard(t *testing.T) {
	cards := &mediaCards{open: map[dbus.ObjectPath]*mediaCard{}}
	path := dbus.ObjectPath("/org/freedesktop/UDisks2/block_devices/sda1")
	old := cards.start(t.Context(), path)
	newer := cards.start(t.Context(), path)
	if old.ctx.Err() == nil {
		t.Fatal("older card still open")
	}
	cards.close(path, old)
	if newer.ctx.Err() != nil {
		t.Fatal("ending the older card closed the newer one")
	}
	newer.setRemounting(true)
	if !cards.remounting(path) {
		t.Fatal("remount not seen")
	}
	cards.close(path, nil)
	if newer.ctx.Err() == nil || cards.remounting(path) {
		t.Fatal("unplug did not close the card")
	}
}
