package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOrientationTransform(t *testing.T) {
	cases := map[string]int{
		"normal":    0,
		"left-up":   1,
		"bottom-up": 2,
		"right-up":  3,
	}

	for input, want := range cases {
		got, ok := orientationTransform(input)
		if !ok || got != want {
			t.Fatalf("%q => %d,%v want %d,true", input, got, ok, want)
		}
	}

	if _, ok := orientationTransform("unknown"); ok {
		t.Fatal("unknown orientation unexpectedly accepted")
	}
}

func TestRunningScale(t *testing.T) {
	cases := []struct {
		width, height int
		reported      float64
		want          float64
	}{
		// ZBook x2 G4 panel at 1.875: hyprctl monitors -j said 1.88 and
		// Hyprland suggested 1.875 (ODDC report 2026-10-11).
		{3840, 2160, 1.88, 1.875},
		// e2e-book12 VM after `hyprctl keyword monitor` with 1.333333:
		// hyprctl monitors -j said 1.33.
		{1280, 800, 1.33, 160.0 / 120},
		// Framework 13 panel at scale 1 (hyprctl monitors -j on the fw13).
		{2256, 1504, 1.00, 1},
	}
	for _, c := range cases {
		if got := runningScale(c.width, c.height, c.reported); got != c.want {
			t.Errorf("runningScale(%d, %d, %v) = %v, want %v", c.width, c.height, c.reported, got, c.want)
		}
	}
}

func TestLuaString(t *testing.T) {
	cases := map[string]string{
		"eDP-1":       `"eDP-1"`,
		`a"b\c`:       `"a\"b\\c"`,
		"line\nbreak": `"line\010break"`,
	}
	for in, want := range cases {
		if got := luaString(in); got != want {
			t.Errorf("luaString(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestRotationLua(t *testing.T) {
	// SYNTHETIC monitor: Framework 13 panel size, turned to the right.
	monitor := hyprMonitor{Name: "eDP-1", Width: 2256, Height: 1504, RefreshRate: 60, Scale: 1}
	want := `hl.monitor({ output = "eDP-1", mode = "2256x1504@60.000", position = "0x0", scale = "1", transform = 3 }) ` +
		`hl.config({ input = { touchdevice = { output = "eDP-1", transform = 3 }, tablet = { output = "eDP-1", transform = 3 } } })`
	if got := rotationLua(monitor, 3); got != want {
		t.Errorf("rotationLua:\n got %s\nwant %s", got, want)
	}
}

func TestInternalDRMConnectorFindsConnectedEDP(t *testing.T) {
	root := t.TempDir()

	disconnected := filepath.Join(root, "class", "drm", "card0-eDP-1")
	connected := filepath.Join(root, "class", "drm", "card1-eDP-2")

	if err := os.MkdirAll(disconnected, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(connected, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(disconnected, "status"),
		[]byte("disconnected\n"),
		0644,
	); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(connected, "status"),
		[]byte("connected\n"),
		0644,
	); err != nil {
		t.Fatal(err)
	}

	got, err := internalDRMConnector(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != "eDP-2" {
		t.Fatalf("got %q want eDP-2", got)
	}
}
