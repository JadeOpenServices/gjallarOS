package secureboot

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// rootRig records root steps instead of running them. Nothing runs as root.
type rootRig struct {
	steps   []string
	written map[string][]byte
	inputs  [][]byte
}

func newRootRig(t *testing.T) *rootRig {
	t.Helper()
	rig := &rootRig{written: map[string][]byte{}}
	prevRun, prevInput, prevWrite, prevTest := run, runWithInput, writeRootFile, sudoTest
	t.Cleanup(func() { run, runWithInput, writeRootFile, sudoTest = prevRun, prevInput, prevWrite, prevTest })
	run = func(_ context.Context, name string, args ...string) error {
		rig.steps = append(rig.steps, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	runWithInput = func(_ context.Context, input []byte, name string, args ...string) error {
		rig.inputs = append(rig.inputs, input)
		rig.steps = append(rig.steps, strings.Join(append([]string{"stdin", name}, args...), " "))
		return nil
	}
	writeRootFile = func(_ context.Context, data []byte, mode, dst string) error {
		rig.written[dst] = data
		rig.steps = append(rig.steps, "rootfile "+mode+" "+dst)
		return nil
	}
	sudoTest = func(context.Context, ...string) (bool, error) { return false, nil }
	return rig
}

// noUserFile fails when a root step names a file the user session could
// have made.
func (r *rootRig) noUserFile(t *testing.T) {
	t.Helper()
	tmp := os.TempDir()
	for _, step := range r.steps {
		if strings.Contains(step, tmp) || strings.Contains(step, "file:") {
			t.Errorf("root step names a user file: %s", step)
		}
	}
}

// L54: the passphrase reaches openssl and the checkpoint from memory.
func TestProvisionPassesPassphraseFromMemory(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	rig := newRootRig(t)
	recovery, err := Provision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rig.noUserFile(t)
	if len(rig.inputs) != 1 || string(rig.inputs[0]) != recovery.Passphrase+"\n" {
		t.Fatalf("openssl stdin = %q", rig.inputs)
	}
	if !slices.ContainsFunc(rig.steps, func(s string) bool {
		return strings.HasPrefix(s, "stdin sudo ") && strings.HasSuffix(s, "-pass stdin")
	}) {
		t.Errorf("openssl does not read the passphrase from stdin: %v", rig.steps)
	}
	if string(rig.written[RecoveryPassphrasePath]) != recovery.Passphrase {
		t.Errorf("checkpoint not written from memory: %v", rig.steps)
	}
	if entries, _ := os.ReadDir(os.Getenv("TMPDIR")); len(entries) != 0 {
		t.Errorf("temp files left: %v", entries)
	}
}

// L54: mokutil imports a key root wrote from memory, never a user file a
// session could swap for its own key.
func TestQueueMOKImportsFromRootFile(t *testing.T) {
	f := newFakeFirmware(t)
	saved := liveFirmware
	t.Cleanup(func() { liveFirmware = saved })
	liveFirmware = f.root
	rig := newRootRig(t)
	der := []byte("EXAMPLE certificate bytes")

	_ = queueMOK(context.Background(), der, &strings.Builder{})
	rig.noUserFile(t)
	if string(rig.written[mokImportPath]) != string(der) {
		t.Fatalf("key not staged by root: %v", rig.steps)
	}
	want := []string{
		"rootfile 0600 " + mokImportPath,
		"sudo " + privilegedTool("mokutil") + " --import " + mokImportPath,
		"sudo rm -f -- " + mokImportPath,
	}
	if !slices.Equal(rig.steps, want) {
		t.Errorf("steps = %q, want %q", rig.steps, want)
	}

	rig.steps = nil
	writeRootFile = func(context.Context, []byte, string, string) error { return errors.New("no root") }
	if err := queueMOK(context.Background(), der, &strings.Builder{}); err == nil || len(rig.steps) != 0 {
		t.Errorf("mokutil ran without a staged key: %v %v", err, rig.steps)
	}
}
