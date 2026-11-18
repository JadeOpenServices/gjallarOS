package oddchost

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/oddc/pkg/oddc"
)

func TestLoadPrivilegedMissingIsAbsent(t *testing.T) {
	reader := func(
		context.Context,
		string,
	) ([]byte, bool, error) {
		return nil, false, nil
	}

	_, exists, err := loadPrivileged(
		context.Background(),
		"/protected/host-overlay.json",
		testModel,
		reader,
	)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("missing protected overlay reported as present")
	}
}

func TestLoadPrivilegedDecodesAndBindsModel(t *testing.T) {
	reader := func(
		_ context.Context,
		path string,
	) ([]byte, bool, error) {
		if path != "/protected/host-overlay.json" {
			t.Fatalf("unexpected path %q", path)
		}

		return []byte(`{
  "apiVersion": "oddc.openjade.de/v2",
  "id": "host/machine-local",
  "kind": "host",
  "targetModel": "model/test/laptop",
  "overrides": {
    "hardware": {
      "security": {
        "fingerprint": {
          "primary": {
            "$delete": true
          }
        }
      }
    }
  }
}`), true, nil
	}

	overlay, exists, err := loadPrivileged(
		context.Background(),
		"/protected/host-overlay.json",
		testModel,
		reader,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("protected overlay reported as absent")
	}
	if overlay.TargetModel != testModel {
		t.Fatalf("target model = %q", overlay.TargetModel)
	}
}

func TestLoadPrivilegedRejectsDifferentModel(t *testing.T) {
	reader := func(
		context.Context,
		string,
	) ([]byte, bool, error) {
		return []byte(`{
  "apiVersion": "oddc.openjade.de/v2",
  "id": "host/machine-local",
  "kind": "host",
  "targetModel": "model/test/other",
  "overrides": {}
}`), true, nil
	}

	_, _, err := loadPrivileged(
		context.Background(),
		"/protected/host-overlay.json",
		testModel,
		reader,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "targets") {
		t.Fatalf("wrong-model overlay error = %v", err)
	}
}

func TestLoadPrivilegedReadFailureIsNotTreatedAsMissing(t *testing.T) {
	reader := func(
		context.Context,
		string,
	) ([]byte, bool, error) {
		return nil, false, errors.New("sudo authorization failed")
	}

	_, exists, err := loadPrivileged(
		context.Background(),
		"/protected/host-overlay.json",
		testModel,
		reader,
	)
	if err == nil {
		t.Fatal("protected read failure was silently ignored")
	}
	if exists {
		t.Fatal("failed read reported overlay as present")
	}
}

func TestSavePrivilegedUsesProtectedAtomicReplacement(t *testing.T) {
	overlay, err := New(testModel)
	if err != nil {
		t.Fatal(err)
	}

	var calls [][]string
	var stagedBody []byte

	run := func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		call := append([]string{name}, args...)
		calls = append(calls, call)

		return nil, nil
	}
	prev := writeRootFile
	t.Cleanup(func() { writeRootFile = prev })
	writeRootFile = func(_ context.Context, data []byte, mode, dst string) error {
		calls = append(calls, []string{"rootfile", mode, dst})
		stagedBody = data
		return nil
	}

	path := "/mnt/var/lib/gjallarOS/oddc/host-overlay.json"

	if err := savePrivileged(
		context.Background(),
		path,
		overlay,
		run,
	); err != nil {
		t.Fatal(err)
	}

	if len(stagedBody) == 0 {
		t.Fatal("privileged save did not stage serialized overlay")
	}

	decoded, err := oddc.DecodeOverlay(
		strings.NewReader(string(stagedBody)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.TargetModel != testModel {
		t.Fatalf(
			"staged target model = %q",
			decoded.TargetModel,
		)
	}

	all := make([]string, 0, len(calls))
	for _, call := range calls {
		all = append(all, strings.Join(call, " "))
	}
	joined := strings.Join(all, "\n")

	if !strings.Contains(
		joined,
		"sudo install -d -m 0700 /mnt/var/lib/gjallarOS/oddc",
	) {
		t.Fatalf(
			"protected directory creation missing:\n%s",
			joined,
		)
	}

	if !strings.Contains(
		joined,
		"rootfile 0600 /mnt/var/lib/gjallarOS/oddc/.host-overlay.json.tmp",
	) || strings.Contains(joined, "sudo install -m") {
		t.Fatalf(
			"protected file staging missing:\n%s",
			joined,
		)
	}

	if !strings.Contains(
		joined,
		"sudo mv -f -- /mnt/var/lib/gjallarOS/oddc/.host-overlay.json.tmp /mnt/var/lib/gjallarOS/oddc/host-overlay.json",
	) {
		t.Fatalf(
			"atomic activation missing:\n%s",
			joined,
		)
	}
}

func TestSavePrivilegedDoesNotActivateAfterInstallFailure(t *testing.T) {
	overlay, err := New(testModel)
	if err != nil {
		t.Fatal(err)
	}

	moved := false
	prev := writeRootFile
	t.Cleanup(func() { writeRootFile = prev })
	writeRootFile = func(context.Context, []byte, string, string) error {
		return errors.New("install failed")
	}

	run := func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		call := strings.Join(
			append([]string{name}, args...),
			" ",
		)

		if strings.Contains(call, " mv ") {
			moved = true
		}

		return nil, nil
	}

	err = savePrivileged(
		context.Background(),
		"/var/lib/gjallarOS/oddc/host-overlay.json",
		overlay,
		run,
	)
	if err == nil {
		t.Fatal("temporary protected install failure was accepted")
	}
	if moved {
		t.Fatal("host overlay activated after staging failure")
	}
}

func TestSavePrivilegedRejectsInvalidTarget(t *testing.T) {
	overlay, err := New(testModel)
	if err != nil {
		t.Fatal(err)
	}

	called := false
	run := func(
		context.Context,
		string,
		...string,
	) ([]byte, error) {
		called = true
		return nil, nil
	}

	if err := savePrivileged(
		context.Background(),
		"relative/host-overlay.json",
		overlay,
		run,
	); err == nil {
		t.Fatal("relative protected host-overlay path accepted")
	}

	if called {
		t.Fatal("privileged command ran for invalid path")
	}
}

// Regression: "test -e -- PATH" exits 2 (syntax error) instead of 0/1, which
// broke every install that resolved an ODDC device model.
func TestProtectedExistsArgsRunWithRealTest(t *testing.T) {
	testBin, err := exec.LookPath("test")
	if err != nil {
		t.Skip("test(1) not available")
	}
	dir := t.TempDir()
	present := filepath.Join(dir, "host-overlay.json")
	if err := os.WriteFile(present, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{
		present:                            0,
		filepath.Join(dir, "missing.json"): 1,
	} {
		args, err := protectedExistsArgs(path)
		if err != nil {
			t.Fatal(err)
		}
		code := 0
		if err := exec.Command(testBin, args[1:]...).Run(); err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatal(err)
			}
			code = exit.ExitCode()
		}
		if code != want {
			t.Errorf("test %v exit = %d, want %d", args[1:], code, want)
		}
	}
	if _, err := protectedExistsArgs("relative/host-overlay.json"); err == nil {
		t.Error("accepted relative path")
	}
}
