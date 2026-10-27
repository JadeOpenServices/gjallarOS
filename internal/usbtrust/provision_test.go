package usbtrust

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Explicitly opt in: this test always uses a disposable software TPM, never
// /dev/tpm*. The production signing/provisioning commands run unchanged.
func TestTPMProvisionAndSignedPersistenceSimulator(t *testing.T) {
	if os.Getenv("GJALLAR_USBTRUST_TPM_TEST") != "1" {
		t.Skip("software TPM integration is opt-in")
	}
	root := t.TempDir()
	socket := filepath.Join(root, "tpm.sock")
	cmd := exec.Command("swtpm", "socket", "--tpm2", "--tpmstate", "dir="+root, "--server", "type=unixio,path="+socket, "--ctrl", "type=unixio,path="+socket+".ctrl", "--flags", "not-need-init,startup-clear")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Setenv("TPM2TOOLS_TCTI", "swtpm:path="+socket)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := filepath.Join(root, "trust")
	const handle = "0x81000042"
	// Production talks to /dev/tpmrm0, whose resource manager flushes
	// transient objects when each tool exits. swtpm has none, so flush them
	// between steps or the three object slots run out.
	flushTransient := func() {
		if output, err := exec.CommandContext(ctx, "tpm2_flushcontext", "-t").CombinedOutput(); err != nil {
			t.Fatalf("flush transient objects: %v: %s", err, output)
		}
	}
	if err := ProvisionTPM(ctx, dir, handle); err != nil {
		t.Fatal(err)
	}
	flushTransient()
	// The same primary left at the handle by an earlier install is adopted.
	if err := ProvisionTPM(ctx, dir, handle); err != nil {
		t.Fatalf("same key not adopted: %v", err)
	}
	flushTransient()
	const foreign = "0x81000043"
	foreignCtx := filepath.Join(root, "foreign.ctx")
	for _, args := range [][]string{
		{"tpm2_createprimary", "-Q", "-C", "o", "-G", "ecc256:ecdsa-sha256", "-g", "sha256", "-a", "fixedtpm|fixedparent|sensitivedataorigin|userwithauth|sign|noda", "-c", foreignCtx},
		{"tpm2_evictcontrol", "-Q", "-C", "o", "-c", foreignCtx, foreign},
	} {
		if output, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, output)
		}
	}
	flushTransient()
	if err := ProvisionTPM(ctx, dir, foreign); err == nil || !strings.Contains(err.Error(), "different object") {
		t.Fatal("different object at TPM handle adopted")
	}
	flushTransient()
	signer, _ := NewTPMSigner(handle)
	if err := CommitSigned(ctx, dir, sampleDocument(), signer); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadVerified(ctx, dir, signer); err != nil {
		t.Fatal(err)
	}
	other, _ := NewTPMSigner("0x81000043")
	if _, err := ReadVerified(ctx, dir, other); err == nil {
		t.Fatal("wrong key accepted")
	}
	// Provisioning pinned the key: the foreign key fails the pin even
	// before any signature is checked.
	pinned, _ := NewTPMSigner(foreign)
	pinned.PinFile = filepath.Join(dir, KeyPinFile)
	if err := ValidateSigner(ctx, pinned); err == nil || !strings.Contains(err.Error(), "not the key this system set up") {
		t.Fatalf("foreign key passed the pin: %v", err)
	}
	signer.PinFile = pinned.PinFile
	signer.key = nil
	if err := ValidateSigner(ctx, signer); err != nil {
		t.Fatalf("provisioned key failed its own pin: %v", err)
	}
}
