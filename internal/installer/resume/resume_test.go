package resume

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeTransaction(t *testing.T, tx Transaction) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "transaction.json")

	data, err := json.Marshal(tx)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestLoad(t *testing.T) {
	wantArgs := []string{
		"-repo",
		"/home/test/gjallarOS",
		"-accept-existing",
	}

	path := writeTransaction(t, Transaction{
		Schema:          Schema,
		State:           "waiting-for-release-reboot",
		Reason:          "nixos-release-alignment",
		ExpectedRelease: "26.05",
		Repo:            "/home/test/gjallarOS",
		Args:            wantArgs,
	})

	tx, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if tx.ExpectedRelease != "26.05" {
		t.Fatalf(
			"expected release %q, want 26.05",
			tx.ExpectedRelease,
		)
	}

	if !reflect.DeepEqual(tx.Args, wantArgs) {
		t.Fatalf("args %#v, want %#v", tx.Args, wantArgs)
	}
}

func TestLoadRejectsFailedState(t *testing.T) {
	path := writeTransaction(t, Transaction{
		Schema:          Schema,
		State:           "failed",
		ExpectedRelease: "26.05",
		Repo:            "/home/test/gjallarOS",
	})

	if _, err := Load(path); err == nil {
		t.Fatal("expected failed transaction to be rejected")
	}
}

func TestLoadRejectsRelativeRepo(t *testing.T) {
	path := writeTransaction(t, Transaction{
		Schema:          Schema,
		State:           "waiting-for-release-reboot",
		ExpectedRelease: "26.05",
		Repo:            "relative/repo",
	})

	if _, err := Load(path); err == nil {
		t.Fatal("expected relative repository to be rejected")
	}
}

func TestLoadAcceptsMaintenanceWithoutRelease(t *testing.T) {
	path := writeTransaction(t, Transaction{
		Schema: Schema,
		State:  StateMaintenanceReboot,
		Repo:   "/home/test/gjallarOS",
	})

	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}

func armedDir(t *testing.T, name string) string {
	t.Helper()

	dir := t.TempDir()
	data, err := json.Marshal(Transaction{
		Schema: Schema,
		State:  StateMaintenanceReboot,
		Repo:   "/home/test/gjallarOS",
		Args:   []string{"-accept-existing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func exists(t *testing.T, path string) bool {
	t.Helper()

	_, err := os.Stat(path)
	return err == nil
}

// The old unit moved pending.json away before the run; a reboot mid-resume
// lost the transaction (e2e-target, 2026-10-05).
func TestClaimSurvivesInterruptedRun(t *testing.T) {
	dir := armedDir(t, "pending.json")

	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		tx, err := Claim(dir)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if tx.Attempts != attempt {
			t.Fatalf("attempts %d, want %d", tx.Attempts, attempt)
		}
		if exists(t, filepath.Join(dir, "pending.json")) {
			t.Fatal("pending.json left after claim")
		}
		if err := Finish(dir, false, true); err != nil {
			t.Fatal(err)
		}
		if !exists(t, filepath.Join(dir, "active.json")) {
			t.Fatal("interrupted run dropped active.json")
		}
	}

	if _, err := Claim(dir); err == nil {
		t.Fatal("expected claim past MaxAttempts to fail")
	}
	if exists(t, filepath.Join(dir, "active.json")) || !exists(t, filepath.Join(dir, "failed.json")) {
		t.Fatal("exhausted transaction was not retired to failed.json")
	}
}

func TestFinishRetiresFailedRun(t *testing.T) {
	dir := armedDir(t, "pending.json")

	if _, err := Claim(dir); err != nil {
		t.Fatal(err)
	}
	if err := Finish(dir, false, false); err != nil {
		t.Fatal(err)
	}
	if exists(t, filepath.Join(dir, "active.json")) || !exists(t, filepath.Join(dir, "failed.json")) {
		t.Fatal("failed run was not retired to failed.json")
	}
}

func TestFinishRemovesSucceededRun(t *testing.T) {
	dir := armedDir(t, "active.json")

	if _, err := Claim(dir); err != nil {
		t.Fatal(err)
	}
	if err := Finish(dir, true, false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pending.json", "active.json", "failed.json"} {
		if exists(t, filepath.Join(dir, name)) {
			t.Fatalf("%s left after a finished run", name)
		}
	}
}

// A run that armed the next continuation already removed active.json.
func TestFinishToleratesHandedOverRun(t *testing.T) {
	dir := t.TempDir()

	for _, succeeded := range []bool{true, false} {
		if err := Finish(dir, succeeded, false); err != nil {
			t.Fatal(err)
		}
	}
	if exists(t, filepath.Join(dir, "failed.json")) {
		t.Fatal("finish created failed.json without an active transaction")
	}
}

// The store root lives until the last continuation finishes.
func TestFinishReleasesStoreRoot(t *testing.T) {
	for _, tc := range []struct {
		name      string
		succeeded bool
		handover  bool
		keep      bool
	}{
		{"succeeded", true, false, false},
		{"failed", false, false, false},
		{"handed over", true, true, true},
		{"failed after handover", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := armedDir(t, "pending.json")
			if _, err := Claim(dir); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(dir, "gcroot")
			if err := os.Symlink("/nix/store/does-not-matter", root); err != nil {
				t.Fatal(err)
			}
			if tc.handover {
				if err := os.WriteFile(filepath.Join(dir, "pending.json"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := Finish(dir, tc.succeeded, false); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(root); (err == nil) != tc.keep {
				t.Fatalf("store root kept = %v, want %v", err == nil, tc.keep)
			}
		})
	}
}

func TestStoreRoot(t *testing.T) {
	for in, want := range map[string]string{
		"/nix/store/abc-gjallar-installer/bin/gjallar-installer": "/nix/store/abc-gjallar-installer",
		"/nix/store/abc-gjallar-installer":                       "/nix/store/abc-gjallar-installer",
		"/nix/store/":                                            "",
		"/var/lib/gjallarOS/installer-resume/gjallar-installer":  "",
		"/nix/storex/abc/bin/x":                                  "",
	} {
		if got := storeRoot(in); got != want {
			t.Errorf("storeRoot(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModuleMatchesPermanentUnit(t *testing.T) {
	module := Module()

	for _, want := range []string{
		"!(options ? gjallar && options.gjallar ? installerResume)",
		`"|` + PendingPath + `"`,
		`"|` + ActivePath + `"`,
		`ConditionFileIsExecutable = "` + InstallerPath + `"`,
		InstallerPath + " --resume-transaction " + StateDir + `"`,
		`"/run/wrappers"`,
	} {
		if !strings.Contains(module, want) {
			t.Fatalf("module lacks %q:\n%s", want, module)
		}
	}

	if strings.Contains(module, "mv ") {
		t.Fatal("module still moves the transaction before the run")
	}
	for _, want := range []string{`StandardInput = "tty"`, `StandardOutput = "tty"`, `TTYPath = "/dev/tty1"`, `"greetd.service"`} {
		if !strings.Contains(module, want) {
			t.Fatalf("continuation lacks tty1 before the greeter: %q", want)
		}
	}

	permanent, err := os.ReadFile("../../../system/maintenance/installer-resume.nix")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`stateDir = "` + StateDir + `"`,
		`"|${stateDir}/pending.json"`,
		`"|${stateDir}/active.json"`,
		`ExecStart = "${installer} --resume-transaction ${stateDir}"`,
		"options.gjallar.installerResume.enable",
	} {
		if !strings.Contains(string(permanent), want) {
			t.Fatalf("permanent module lacks %q", want)
		}
	}
}
