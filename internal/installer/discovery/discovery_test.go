package discovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectHardware(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "class", "dmi", "id")
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(path, "chassis_type"), []byte("32\n"), 0644)
	os.WriteFile(filepath.Join(path, "product_name"), []byte("Framework Laptop 13\n"), 0644)
	got := DetectHardware(root)
	if got.FormFactor != "laptop" {
		t.Fatalf("%+v", got)
	}
}

func TestDetectHardwareFindsDirectMultitouchScreen(t *testing.T) {
	root := t.TempDir()
	device := filepath.Join(root, "class", "input", "event4", "device")
	if err := os.MkdirAll(filepath.Join(device, "capabilities"), 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(device, "name"), []byte("ELAN Touch Device\n"), 0644)
	os.WriteFile(filepath.Join(device, "properties"), []byte("2\n"), 0644)
	os.WriteFile(filepath.Join(device, "capabilities", "abs"), []byte("60000000000000\n"), 0644)
	if got := DetectHardware(root); !got.Touchscreen {
		t.Fatalf("touchscreen not detected: %+v", got)
	}
}

func TestDetectHardwareDoesNotTreatTouchpadAsTouchscreen(t *testing.T) {
	root := t.TempDir()
	device := filepath.Join(root, "class", "input", "event7", "device")
	if err := os.MkdirAll(filepath.Join(device, "capabilities"), 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(device, "name"), []byte("Touchpad\n"), 0644)
	os.WriteFile(filepath.Join(device, "properties"), []byte("5\n"), 0644)
	os.WriteFile(filepath.Join(device, "capabilities", "abs"), []byte("60000000000000\n"), 0644)
	if got := DetectHardware(root); got.Touchscreen {
		t.Fatalf("touchpad detected as touchscreen: %+v", got)
	}
}

func TestDetectHardwareFindsPenTablet(t *testing.T) {
	root := t.TempDir()
	device := filepath.Join(root, "class", "input", "event9", "device")
	if err := os.MkdirAll(filepath.Join(device, "capabilities"), 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(device, "name"), []byte("Wacom Tablet\n"), 0644)
	os.WriteFile(filepath.Join(device, "capabilities", "key"), []byte("1"+strings.Repeat("0", 80)+"\n"), 0644)
	os.WriteFile(filepath.Join(device, "capabilities", "abs"), []byte("3\n"), 0644)
	if got := DetectHardware(root); !got.PenTablet {
		t.Fatalf("pen tablet not detected: %+v", got)
	}
}

func TestDetectHardwareReadsStructuredDMIIdentity(t *testing.T) {
	root := t.TempDir()
	dmi := filepath.Join(root, "class", "dmi", "id")

	if err := os.MkdirAll(dmi, 0755); err != nil {
		t.Fatal(err)
	}

	values := map[string]string{
		"chassis_type":    "32\n",
		"sys_vendor":      "HP\n",
		"product_name":    "HP ZBook x2 G4\n",
		"product_version": "A\n",
		"board_vendor":    "HP\n",
		"board_name":      "824C\n",
		"board_version":   "KBC Version 43.72\n",
	}

	for name, value := range values {
		if err := os.WriteFile(
			filepath.Join(dmi, name),
			[]byte(value),
			0644,
		); err != nil {
			t.Fatal(err)
		}
	}

	got := DetectHardware(root)

	if got.FormFactor != "laptop" {
		t.Fatalf("FormFactor=%q", got.FormFactor)
	}
	if got.SysVendor != "HP" {
		t.Fatalf("SysVendor=%q", got.SysVendor)
	}
	if got.ProductName != "HP ZBook x2 G4" {
		t.Fatalf("ProductName=%q", got.ProductName)
	}
	if got.ProductVersion != "A" {
		t.Fatalf("ProductVersion=%q", got.ProductVersion)
	}
	if got.BoardVendor != "HP" {
		t.Fatalf("BoardVendor=%q", got.BoardVendor)
	}
	if got.BoardName != "824C" {
		t.Fatalf("BoardName=%q", got.BoardName)
	}
	if got.BoardVersion != "KBC Version 43.72" {
		t.Fatalf("BoardVersion=%q", got.BoardVersion)
	}
}

func TestDetectHardwareFindsOrientationSensor(t *testing.T) {
	root := t.TempDir()
	device := filepath.Join(root, "bus", "iio", "devices", "iio:device4")

	if err := os.MkdirAll(device, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(device, "name"),
		[]byte("accel_3d\n"),
		0644,
	); err != nil {
		t.Fatal(err)
	}

	got := DetectHardware(root)
	if !got.OrientationSensor {
		t.Fatalf("orientation sensor not detected: %+v", got)
	}
}

func TestDetectHardwareDoesNotTreatALSAsOrientationSensor(t *testing.T) {
	root := t.TempDir()
	device := filepath.Join(root, "bus", "iio", "devices", "iio:device0")

	if err := os.MkdirAll(device, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(device, "name"),
		[]byte("als\n"),
		0644,
	); err != nil {
		t.Fatal(err)
	}

	got := DetectHardware(root)
	if got.OrientationSensor {
		t.Fatalf("ALS incorrectly detected as orientation sensor: %+v", got)
	}
}

func TestDiscoverSkipsRetiredBrowsers(t *testing.T) {
	repo := t.TempDir()
	for _, dir := range []string{"user/shells", "user/editors/vscodium", "user/browsers", "themes"} {
		if err := os.MkdirAll(filepath.Join(repo, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"firefox", "librewolf"} {
		if err := os.WriteFile(filepath.Join(repo, "user/browsers", name+".nix"), []byte("{ }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	o, err := Discover(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Browsers) != 1 || o.Browsers[0] != "firefox" {
		t.Fatalf("Browsers = %v, want [firefox]", o.Browsers)
	}
	if len(o.RetiredBrowsers) != 1 || o.RetiredBrowsers[0] != "librewolf" {
		t.Fatalf("RetiredBrowsers = %v, want [librewolf]", o.RetiredBrowsers)
	}
}
