package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestStartupPINProblem(t *testing.T) {
	for pin, ok := range map[string]bool{
		"123456":                 true,
		"Fjord-7!":               true,
		"12345":                  false,
		"":                       false,
		"123 456":                false,
		"päßwort1":               false,
		"tab\there1":             false,
		string(make([]byte, 65)): false,
	} {
		if got := startupPINProblem(pin) == ""; got != ok {
			t.Errorf("%q: accepted=%t, want %t", pin, got, ok)
		}
	}
	long := bytes.Repeat([]byte("a"), 64)
	if p := startupPINProblem(string(long)); p != "" {
		t.Errorf("64 characters refused: %s", p)
	}
}

func TestTPM2MetadataPIN(t *testing.T) {
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		header string
		want   string
	}{
		"pin":    {`{"keyslots":{"0":{},"1":{}},"tokens":{"0":{"type":"systemd-tpm2","keyslots":["1"],"tpm2-pin":true}}}`, "yes\n"},
		"no pin": {`{"keyslots":{"0":{},"1":{}},"tokens":{"0":{"type":"systemd-tpm2","keyslots":["1"],"tpm2-pin":false}}}`, "no\n"},
		"no tpm": {`{"keyslots":{"0":{}},"tokens":{}}`, "no\n"},
	} {
		path := filepath.Join(dir, "m.json")
		if err := os.WriteFile(path, []byte(tc.header), 0o600); err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer
		if code := runTPM2MetadataPIN([]string{path}, &out, &errOut); code != 0 || out.String() != tc.want {
			t.Errorf("%s: code %d out %q err %q", name, code, out.String(), errOut.String())
		}
	}
	var out, errOut bytes.Buffer
	if code := runTPM2MetadataPIN([]string{filepath.Join(dir, "missing")}, &out, &errOut); code != 1 {
		t.Errorf("missing file: code %d", code)
	}
}

func TestLUKSArgsValid(t *testing.T) {
	for _, args := range [][]string{{"status"}, {"recovery-key"}, {"setup"}, {"pin"}, {"pin", "--remove"}, {"record"}} {
		if !luksArgsValid(args) {
			t.Errorf("%q refused", args)
		}
	}
	for _, args := range [][]string{nil, {"pin", "--add"}, {"status", "--remove"}, {"pin", "--remove", "x"}, {"new-pin", "x"}} {
		if luksArgsValid(args) {
			t.Errorf("%q accepted", args)
		}
	}
}
