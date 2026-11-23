package diskcrypto

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func withPCRLock(t *testing.T, bin string, run func(context.Context, string) ([]byte, int, error)) {
	t.Helper()
	oldBin, oldRun := PCRLock, pcrlockRun
	PCRLock, pcrlockRun = bin, run
	t.Cleanup(func() { PCRLock, pcrlockRun = oldBin, oldRun })
}

func TestCheckPCRLockSupport(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		code    int
		runErr  error
		want    PCRLockSupport
		wantErr bool
	}{
		{name: "yes", out: "yes\n", code: 0, want: PCRLockSupported},
		{name: "obsolete T480s", out: "obsolete\n", code: 1, want: PCRLockObsolete},
		{name: "partial", out: "partial\n", code: 1, want: PCRLockPartial},
		{name: "no", out: "no\n", code: 1, want: PCRLockNoTPM},
		{name: "yes with failure exit", out: "yes\n", code: 1, wantErr: true},
		{name: "unknown word", out: "maybe\n", code: 0, wantErr: true},
		{name: "empty", out: "", code: 1, wantErr: true},
		{name: "run error", runErr: errors.New("exec failed"), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withPCRLock(t, "/nix/store/x/lib/systemd/systemd-pcrlock", func(_ context.Context, bin string) ([]byte, int, error) {
				if bin != "/nix/store/x/lib/systemd/systemd-pcrlock" {
					t.Fatalf("ran %q", bin)
				}
				return []byte(tc.out), tc.code, tc.runErr
			})
			got, err := CheckPCRLockSupport(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %q, want error", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestCheckPCRLockSupportNeedsLinkedPath(t *testing.T) {
	withPCRLock(t, "", func(context.Context, string) ([]byte, int, error) {
		t.Fatal("ran without a linked path")
		return nil, 0, nil
	})
	if _, err := CheckPCRLockSupport(context.Background()); err == nil {
		t.Fatal("empty PCRLock path gave no error")
	}
}

func TestReadChipCheckNeedsRootCheck(t *testing.T) {
	dir := t.TempDir()
	if ReadChipCheck(filepath.Join(dir, "missing.json")) == "" {
		t.Fatal("a missing boot check counts as a supported chip")
	}
	path := filepath.Join(dir, "chip-check.json")
	if err := os.WriteFile(path, []byte(`{"chipReason":""}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadChipCheck(path); (os.Geteuid() == 0) != (got == "") {
		t.Fatalf("reason %q from a file owned by uid %d", got, os.Geteuid())
	}
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if ReadChipCheck(path) == "" {
		t.Fatal("a check without a reason field counts as a supported chip")
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if ReadChipCheck(link) == "" {
		t.Fatal("a planted link counts as root's check")
	}
}

// The TPM is root-only: as a user the check must ask sudo, or every chip
// looks unusable (live user on a non-root installer run, 2026-11-23).
func TestPCRLockArgv(t *testing.T) {
	bin := "/nix/store/x/lib/systemd/systemd-pcrlock"
	root := pcrlockArgv(bin, 0)
	if len(root) != 2 || root[0] != bin || root[1] != "is-supported" {
		t.Fatalf("root argv %q", root)
	}
	user := pcrlockArgv(bin, 60600)
	want := []string{"sudo", "--", bin, "is-supported"}
	if len(user) != len(want) {
		t.Fatalf("user argv %q, want %q", user, want)
	}
	for i := range want {
		if user[i] != want[i] {
			t.Fatalf("user argv %q, want %q", user, want)
		}
	}
}
