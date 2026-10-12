package main

import (
	"bytes"
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
