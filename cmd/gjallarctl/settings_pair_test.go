package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsPairExportWritesNewPrivateFile(t *testing.T) {
	bus := &fakeSettingsBus{pairFile: []byte("gjallaros-pairing-v1\nSYNTHETIC\n")}
	useFakeSettings(t, bus)
	path := filepath.Join(t.TempDir(), "pair.gjp")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"settings", "pair", "export", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, bus.pairFile) {
		t.Fatalf("file = %q, %v", data, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, %v", info.Mode(), err)
	}
	if !strings.Contains(stdout.String(), "gjallarctl settings pair add "+path) {
		t.Errorf("stdout %q does not say what to run on the other device", stdout.String())
	}
	if code := run([]string{"settings", "pair", "export", path}, &stdout, &stderr); code != 1 {
		t.Fatalf("export over an existing file: code %d", code)
	}
}

func writePairFile(t *testing.T) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pair.gjp")
	content := []byte("gjallaros-pairing-v1\nSYNTHETIC\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, content
}

func TestSettingsPairAddAsksBeforeAdding(t *testing.T) {
	bus := &fakeSettingsBus{}
	useFakeSettings(t, bus)
	path, content := writePairFile(t)
	settingsStdin = strings.NewReader("y\n")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"settings", "pair", "add", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	if len(bus.checked) != 1 || !bytes.Equal(bus.checked[0], content) {
		t.Fatalf("checked %q", bus.checked)
	}
	if len(bus.pairs) != 1 || bus.pairs[0].ID != fakeOffer.ID {
		t.Fatalf("pairs %+v", bus.pairs)
	}
	for _, want := range []string{
		"  11111 22222 33333 44444\n  55555 66666 77777 88888\n  99999 00000 12121 34343\n",
		`says it is "example-host"`,
		"pair remove " + fakeOffer.ID,
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
		}
	}

	stdout.Reset()
	if code := run([]string{"settings", "pair", "list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("list: code %d, stderr %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), fakeOffer.ID+`  "tester" on the device that says it is "example-host", paired 2026-10-19 12:00`) {
		t.Errorf("list = %q", stdout.String())
	}
	stdout.Reset()
	if code := run([]string{"settings", "pair", "list", "--ids"}, &stdout, &stderr); code != 0 || stdout.String() != fakeOffer.ID+"\n" {
		t.Errorf("list --ids: code %d, %q", code, stdout.String())
	}
	if code := run([]string{"settings", "pair", "remove", fakeOffer.ID}, &stdout, &stderr); code != 0 || len(bus.pairs) != 0 {
		t.Fatalf("remove: code %d, pairs %+v", code, bus.pairs)
	}
	stderr.Reset()
	if code := run([]string{"settings", "pair", "remove", fakeOffer.ID}, &stdout, &stderr); code != 1 {
		t.Errorf("removing twice: code %d", code)
	}
}

func TestSettingsPairAddDefaultsToNo(t *testing.T) {
	for name, input := range map[string]string{"enter": "\n", "no": "n\n", "end of input": ""} {
		t.Run(name, func(t *testing.T) {
			bus := &fakeSettingsBus{}
			useFakeSettings(t, bus)
			path, _ := writePairFile(t)
			settingsStdin = strings.NewReader(input)
			var stdout, stderr bytes.Buffer
			run([]string{"settings", "pair", "add", path}, &stdout, &stderr)
			if len(bus.pairs) != 0 {
				t.Fatalf("paired without a yes: %+v", bus.pairs)
			}
		})
	}
}

func TestSettingsPairRejectsBadArguments(t *testing.T) {
	bus := &fakeSettingsBus{}
	useFakeSettings(t, bus)
	big := filepath.Join(t.TempDir(), "big.gjp")
	if err := os.WriteFile(big, make([]byte, 5<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"settings", "pair"},
		{"settings", "pair", "send"},
		{"settings", "pair", "export"},
		{"settings", "pair", "add"},
		{"settings", "pair", "add", big},
		{"settings", "pair", "list", "--all"},
		{"settings", "pair", "remove"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code == 0 {
			t.Errorf("%q: code 0", args)
		}
	}
	if len(bus.checked) != 0 {
		t.Errorf("a too large file reached the service")
	}
}
