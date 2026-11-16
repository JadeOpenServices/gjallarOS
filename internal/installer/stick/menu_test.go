package stick

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/calmmenu"
)

var ansi = regexp.MustCompile(`\x1b(\[[0-9;]*[A-Za-z]|[78])`)

// plain is the screen without colour codes and cursor moves.
func plain(s string) string { return ansi.ReplaceAllString(s, "\n") }

func TestMenuOrderAndStatus(t *testing.T) {
	net := &fakeNet{devices: always(cable), connectivity: "full"}
	h := newHarness(t, ModeWindow, "", net)
	h.run(t)
	out := plain(h.screen.String())
	for _, w := range []string{"gjallarOS installer", "Network \nCable, connected", "Keyboard \nde", "Channel \nstable"} {
		if !strings.Contains(out, w) {
			t.Errorf("screen lacks %q:\n%s", w, out)
		}
	}
	last := -1
	for _, item := range []string{" 1  \nInstall gjallarOS", " 2  \nRecovery", " 3  \nShell", " 4  \nNetwork", " 5  \nKeyboard layout", " 6  \nAdvanced", " 7  \nRestart", " 8  \nPower off"} {
		i := strings.Index(out, item)
		if i <= last {
			t.Fatalf("%q missing or out of order:\n%s", item, out)
		}
		last = i
	}
}

func TestMenuOfflineStatus(t *testing.T) {
	net := &fakeNet{devices: always(nothing), connectivity: "none"}
	h := newHarness(t, ModeWindow, "2\n", net) // go on without network
	h.run(t)
	if !strings.Contains(plain(h.screen.String()), "Network \nNot connected") {
		t.Errorf("no offline status:\n%s", plain(h.screen.String()))
	}
}

func TestUnknownChoiceShowsNote(t *testing.T) {
	net := &fakeNet{devices: always(cable), connectivity: "full"}
	h := newHarness(t, ModeWindow, "9\n", net)
	h.run(t)
	if !strings.Contains(h.screen.String(), "Type one of the numbers 1 to 8.") {
		t.Errorf("no note:\n%s", plain(h.screen.String()))
	}
	if r := net.ran(""); len(r) != 0 {
		t.Errorf("ran %q", r)
	}
}

// The menu clears the screen, so the installer's last words stay until
// Enter, and a failure comes back as the note on the menu.
func TestPauseAfterInstall(t *testing.T) {
	net := &fakeNet{devices: always(cable), connectivity: "full",
		interactive: func(string, []string) error { return errors.New("boom") }}
	h := newHarness(t, ModeWindow, "1\n\n", net)
	h.run(t)
	out := h.screen.String()
	pause := strings.Index(out, "Press Enter to go back to the menu.")
	if pause < 0 {
		t.Fatalf("no pause:\n%s", plain(out))
	}
	note := "The installer ended with an error (exit code -1)."
	if strings.Count(out[pause:], note) != 1 {
		t.Errorf("menu after the pause does not show the failure:\n%s", plain(out[pause:]))
	}
	if len(net.ran("gjallar-recover")) != 1 {
		t.Errorf("installer ran %q", net.ran(""))
	}
}

const tinySixel = "\x1bPq\"1;1;20;20#0!20~\x1b\\"

func TestLogoOnlyInWindowAndWhenItFits(t *testing.T) {
	logo, err := calmmenu.ParseSixel([]byte(tinySixel))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		mode             string
		cols, rows, x, y int
		want             bool
	}{
		{ModeWindow, 120, 33, 1200, 825, true},
		{ModeConsole, 120, 33, 1200, 825, false},
		{ModeWindow, 120, 33, 0, 0, false},      // no pixel size
		{ModeWindow, 120, 22, 1200, 550, false}, // menu alone fills it
	} {
		net := &fakeNet{devices: always(cable), connectivity: "full"}
		h := newHarness(t, c.mode, "", net)
		h.s.Logos = []calmmenu.Sixel{logo, logo}
		h.s.Size = func() (int, int, int, int) { return c.cols, c.rows, c.x, c.y }
		h.run(t)
		if got := strings.Contains(h.screen.String(), tinySixel); got != c.want {
			t.Errorf("%+v: logo drawn %v", c, got)
		}
	}
}

func writeLogos(t *testing.T, dir string) {
	t.Helper()
	for _, n := range logoNames {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(tinySixel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Loophole 82: the menu runs as root and reads the logo folder it is
// given. Links, FIFOs, big files and anything but one sixel are refused.
func TestLoadLogosRefuses(t *testing.T) {
	good := t.TempDir()
	writeLogos(t, good)
	if l, err := loadLogos(good); err != nil || len(l) != 2 {
		t.Fatalf("good folder: %d logos, %v", len(l), err)
	}

	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, []byte(tinySixel), 0o600)
	cases := map[string]func(dir string){
		"link": func(dir string) {
			os.Remove(filepath.Join(dir, logoNames[1]))
			os.Symlink(secret, filepath.Join(dir, logoNames[1]))
		},
		"fifo": func(dir string) {
			os.Remove(filepath.Join(dir, logoNames[0]))
			if err := syscall.Mkfifo(filepath.Join(dir, logoNames[0]), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"too big": func(dir string) {
			big := "\x1bPq\"1;1;2;2" + strings.Repeat("~", maxLogo) + "\x1b\\"
			os.WriteFile(filepath.Join(dir, logoNames[0]), []byte(big), 0o644)
		},
		"not sixel": func(dir string) {
			os.WriteFile(filepath.Join(dir, logoNames[0]), []byte("\x1b]0;title\x07"), 0o644)
		},
		"missing": func(dir string) { os.Remove(filepath.Join(dir, logoNames[1])) },
		"folder": func(dir string) {
			os.Remove(filepath.Join(dir, logoNames[0]))
			os.Mkdir(filepath.Join(dir, logoNames[0]), 0o755)
		},
	}
	for name, plant := range cases {
		dir := t.TempDir()
		writeLogos(t, dir)
		plant(dir)
		if l, err := loadLogos(dir); err == nil || l != nil {
			t.Errorf("%s: accepted (%d logos)", name, len(l))
		}
	}

	// The folder itself as a link.
	link := filepath.Join(t.TempDir(), "logo")
	os.Symlink(good, link)
	if _, err := loadLogos(link); err == nil {
		t.Error("folder link: accepted")
	}
}
