package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
	"github.com/JadeOpenServices/gjallarOS/internal/winicon"
)

// synthIco is a SYNTHETIC .ico file with one PNG icon of side 32.
func synthIco(t *testing.T, c color.NRGBA) []byte {
	t.Helper()
	return synthIcoSide(t, c, 32)
}

// synthIcoSide is a SYNTHETIC .ico file with one PNG icon of side n.
func synthIcoSide(t *testing.T, c color.NRGBA, n int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	var p bytes.Buffer
	if err := png.Encode(&p, img); err != nil {
		t.Fatal(err)
	}
	d := []byte{0, 0, 1, 0, 1, 0, byte(n), byte(n), 0, 0, 1, 0, 32, 0}
	d = binary.LittleEndian.AppendUint32(d, uint32(p.Len()))
	d = binary.LittleEndian.AppendUint32(d, 22)
	return append(d, p.Bytes()...)
}

func iconOf(t *testing.T, f *fakeSetup, id string, s int) (color.NRGBA, bool) {
	t.Helper()
	data, err := os.ReadFile(iconFile(f.home, id, s))
	if os.IsNotExist(err) {
		return color.NRGBA{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return color.NRGBAModel.Convert(img.At(s/2, s/2)).(color.NRGBA), true
}

func TestIconKey(t *testing.T) {
	for name, want := range map[string]string{
		"lobu_online.exe":    "lobuonline",
		"LobuOnline.ico":     "lobuonline",
		"Example Tool.EXE":   "exampletool",
		"Über-Tool.ico":      "übertool",
		"LobuOnlineFile.ico": "lobuonlinefile",
		"___.exe":            "",
	} {
		if got := iconKey(name); got != want {
			t.Errorf("iconKey(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestIconOut(t *testing.T) {
	imgs := map[int]*image.NRGBA{
		16: image.NewNRGBA(image.Rect(0, 0, 16, 16)),
		48: image.NewNRGBA(image.Rect(0, 0, 48, 48)),
	}
	imgs[48].Pix[0] = 9
	got, err := parseIconOut(iconOut(imgs))
	if err != nil || len(got) != 2 || got[48].Pix[0] != 9 {
		t.Fatalf("round trip: %d images, %v", len(got), err)
	}
	frame := func(s, n int) []byte {
		return append(binary.BigEndian.AppendUint16(nil, uint16(s)), make([]byte, n)...)
	}
	for name, data := range map[string][]byte{
		"odd size":  frame(17, 4*17*17),
		"cut":       frame(16, 4*16*16-1),
		"half size": {0},
		"twice":     append(frame(16, 4*16*16), frame(16, 4*16*16)...),
		"falling":   append(frame(22, 4*22*22), frame(16, 4*16*16)...),
		"too large": frame(512, 4*512*512),
	} {
		if _, err := parseIconOut(data); err == nil {
			t.Errorf("%s: taken", name)
		}
	}
	if _, err := parseIconOut(nil); err == nil {
		t.Error("empty answer taken")
	}
	c := &capped{max: 4}
	if _, err := c.Write([]byte("SYNTHETIC")); err == nil {
		t.Error("capped took more than its limit")
	}
}

// iconApp sets up app id with its program at C:\Tool\tool_app.exe, which
// is not a program file, so only an .ico can give an icon.
func iconApp(t *testing.T, f *fakeSetup, id string) winesandbox.Paths {
	t.Helper()
	paths := winesandbox.PathsFor(f.home, id, id)
	dir := filepath.Join(paths.Prefix, "drive_c", "Tool")
	writeFile(t, filepath.Join(dir, "tool_app.exe"), []byte("SYNTHETIC program"))
	if err := os.MkdirAll(paths.Files, 0o755); err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestMenuIcon(t *testing.T) {
	f := newFakeSetup(t)
	paths := iconApp(t, f, "tool")
	a := winesandbox.App{Name: "tool", Exe: `C:\Tool\tool_app.exe`}
	dir := filepath.Join(paths.Prefix, "drive_c", "Tool")

	// No icon: Wine's.
	if err := saveApp(paths, "tool", a, io.Discard); err != nil {
		t.Fatal(err)
	}
	if entry, _ := menuOf(t, f, "tool"); !strings.Contains(entry, "\nIcon=wine\n") {
		t.Errorf("entry %q", entry)
	}

	// An .ico of another name is not the program's.
	writeFile(t, filepath.Join(dir, "ToolAppFile.ico"), synthIco(t, color.NRGBA{0, 0, 255, 255}))
	if err := saveApp(paths, "tool", a, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, ok := iconOf(t, f, "tool", 32); ok {
		t.Error("icon from an .ico of another name")
	}

	// The .ico with the program's name.
	writeFile(t, filepath.Join(dir, "ToolApp.ico"), synthIco(t, color.NRGBA{255, 0, 0, 255}))
	if err := saveApp(paths, "tool", a, io.Discard); err != nil {
		t.Fatal(err)
	}
	if entry, _ := menuOf(t, f, "tool"); !strings.Contains(entry, "\nIcon=gjallar-wine-tool\n") {
		t.Errorf("entry %q", entry)
	}
	for _, s := range []int{16, 22, 24, 32} {
		if c, ok := iconOf(t, f, "tool", s); !ok || c != (color.NRGBA{255, 0, 0, 255}) {
			t.Errorf("icon %d: %v %v", s, ok, c)
		}
	}
	if _, ok := iconOf(t, f, "tool", 48); ok {
		t.Error("icon larger than the .ico's")
	}

	if err := removeCmd([]string{"tool"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, ok := iconOf(t, f, "tool", 16); ok {
		t.Error("icon left after remove")
	}
}

// The app's files can point at files outside them; those are never read.
func TestMenuIconLinks(t *testing.T) {
	f := newFakeSetup(t)
	outside := filepath.Join(t.TempDir(), "outside.ico")
	writeFile(t, outside, synthIco(t, color.NRGBA{255, 0, 0, 255}))

	paths := iconApp(t, f, "link")
	dir := filepath.Join(paths.Prefix, "drive_c", "Tool")
	if err := os.Symlink(outside, filepath.Join(dir, "tool-app.ico")); err != nil {
		t.Fatal(err)
	}
	a := winesandbox.App{Name: "link", Exe: `C:\Tool\tool_app.exe`}
	if err := saveApp(paths, "link", a, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, ok := iconOf(t, f, "link", 32); ok {
		t.Error("icon read through a link")
	}

	// A folder that is a link to one outside.
	paths = winesandbox.PathsFor(f.home, "dirlink", "dirlink")
	writeFile(t, filepath.Join(filepath.Dir(outside), "tool_app.ico"), synthIco(t, color.NRGBA{255, 0, 0, 255}))
	if err := os.MkdirAll(filepath.Join(paths.Prefix, "drive_c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(paths.Prefix, "drive_c", "Tool")); err != nil {
		t.Fatal(err)
	}
	a = winesandbox.App{Name: "dirlink", Exe: `C:\Tool\tool_app.exe`}
	if err := saveApp(paths, "dirlink", a, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, ok := iconOf(t, f, "dirlink", 32); ok {
		t.Error("icon read through a linked folder")
	}
}

func TestMenuCmdIcons(t *testing.T) {
	f := newFakeSetup(t)
	stale := iconFile(f.home, "gone", 16)
	writeFile(t, stale, []byte("SYNTHETIC icon"))
	other := filepath.Join(filepath.Dir(stale), "someone-else.png")
	writeFile(t, other, []byte("SYNTHETIC icon"))
	if err := menuCmd(nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(stale); err == nil {
		t.Error("icon of a removed app left")
	}
	if _, err := os.Lstat(other); err != nil {
		t.Errorf("other icon removed: %v", err)
	}
}

// TestMain lets TestIconReaderLimit run the icon reader as a child of the
// test binary, the way the sandbox runs it.
func TestMain(m *testing.M) {
	if os.Getenv("GJALLAR_WINE_ICON_CHILD") == "1" {
		os.Exit(iconCmd(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// TestIconReaderLimit runs the icon reader under its memory limit on a
// 256 pixel icon, which it scales to every smaller size, so the Go runtime
// has to grow its heap below the limit.
func TestIconReaderLimit(t *testing.T) {
	src := filepath.Join(t.TempDir(), "big.ico")
	if err := os.WriteFile(src, synthIcoSide(t, color.NRGBA{R: 200, A: 255}, 256), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := exec.Command(os.Args[0], "ico")
	cmd.Env = []string{"GJALLAR_WINE_ICON_CHILD=1"}
	cmd.ExtraFiles = []*os.File{f}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("icon reader: %v: %s", err, errOut.String())
	}
	imgs, err := parseIconOut(out.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != len(winicon.Sizes) {
		t.Fatalf("got %d sizes, want %d", len(imgs), len(winicon.Sizes))
	}
}
