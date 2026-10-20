package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTPM2ReenrollArgvUsesSudoForUsers(t *testing.T) {
	if got := rootToolArgv(tpm2ReenrollTool, 0); !reflect.DeepEqual(got, []string{"gjallar-tpm2-reenroll"}) {
		t.Fatalf("root argv = %q", got)
	}
	if got := rootToolArgv(tpm2ReenrollTool, 1000); !reflect.DeepEqual(got, []string{"sudo", "-k", "gjallar-tpm2-reenroll"}) {
		t.Fatalf("user argv = %q", got)
	}
}

func TestTPM2RejectsUnknownSubcommand(t *testing.T) {
	for _, args := range [][]string{nil, {"enroll"}, {"reenroll", "--force"}} {
		var stderr bytes.Buffer
		if code := runTPM2Reenroll(args, &stderr); code != 2 {
			t.Fatalf("runTPM2Reenroll(%q) = %d, want 2", args, code)
		}
		if !strings.Contains(stderr.String(), "Usage: gjallarctl tpm2 reenroll") {
			t.Fatalf("runTPM2Reenroll(%q) stderr = %q", args, stderr.String())
		}
	}
}

func TestTPM2ReenrollReportsMissingTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stderr bytes.Buffer
	if code := runTPM2Reenroll([]string{"reenroll"}, &stderr); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "TPM2 unlock is not enabled") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// installer tpm2 takes the Startup unlock answer and asks nothing: no
// terminal is open here, and a request without a LUKS mapping stays off.
func TestInstallerTPM2FollowsTheRequest(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{nil, "luks_tpm2_enable=false\n"},
		{[]string{"--requested=false"}, "luks_tpm2_enable=false\n"},
		{[]string{"--requested=true"}, "luks_tpm2_enable=false\n[NOTE] No LUKS mapping found for TPM2 enrollment.\n"},
	} {
		repo := t.TempDir()
		hardware := filepath.Join(repo, "generated", "hardware.nix")
		if err := os.MkdirAll(filepath.Dir(hardware), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hardware, []byte("{ ... }:\n{\n}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if code := runTPM2(append([]string{"--repo", repo, "--hardware", hardware}, test.args...), &stdout, &stderr); code != 0 {
			t.Fatalf("%q: code = %d, stderr = %q", test.args, code, stderr.String())
		}
		if stdout.String() != test.want {
			t.Errorf("%q: stdout = %q, want %q", test.args, stdout.String(), test.want)
		}
		if data, _ := os.ReadFile(hardware); string(data) != "{ ... }:\n{\n}\n" {
			t.Errorf("%q: hardware.nix changed:\n%s", test.args, data)
		}
	}
}
