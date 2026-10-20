package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/settings"
	"github.com/JadeOpenServices/gjallarOS/internal/sshsig"
)

// sshKey points sshsig at the ssh-keygen on PATH and makes a SYNTHETIC
// Ed25519 key without a passphrase. It returns the key file and its
// public key line.
func sshKey(t *testing.T, name string) (string, string) {
	t.Helper()
	path, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen is not on PATH")
	}
	old := sshsig.Keygen
	sshsig.Keygen = path
	t.Cleanup(func() { sshsig.Keygen = old })
	key := filepath.Join(t.TempDir(), name)
	if out, err := exec.Command(path, "-q", "-t", "ed25519", "-N", "", "-C", "SYNTHETIC test key", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	public, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	return key, strings.TrimSpace(string(public))
}

func sshSign(t *testing.T, key, namespace string, data []byte) []byte {
	t.Helper()
	cmd := exec.Command(sshsig.Keygen, "-q", "-Y", "sign", "-n", namespace, "-f", key)
	cmd.Stdin = bytes.NewReader(data)
	sig, err := cmd.Output()
	if err != nil {
		t.Fatalf("ssh-keygen -Y sign: %v", err)
	}
	return sig
}

// withOwnKey makes tester (1000) the main account with line as its own key.
func withOwnKey(t *testing.T, f *fixture, line string) {
	t.Helper()
	setValue(t, f, "username", `"tester"`)
	setValue(t, f, "settingsSigningKey", strconv.Quote(line))
}

func TestOwnKeyExportImport(t *testing.T) {
	f := newFixture(t)
	withKeys(t, f)
	key, line := sshKey(t, "own")
	withOwnKey(t, f, line)
	f.svc.keys.hostKey = nil
	ctx := context.Background()
	before := f.value(t, "timezone")

	data, err := f.svc.Export(ctx, ":1.10")
	if err != nil {
		t.Fatal(err)
	}
	if !settings.IsUnsigned(data) {
		t.Fatalf("export for an own key wrote %.40q", data)
	}
	if len(f.sys.asked) != 1 || f.sys.asked[0] != exportAction+" " {
		t.Fatalf("password checks = %v", f.sys.asked)
	}
	sig := sshSign(t, key, settings.SSHNamespace, data)

	changeTimezone(t, f)
	pending, err := f.svc.ImportSigned(ctx, ":1.10", data, sig)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.Keys) != 1 || pending.Keys[0].Key != "timezone" || string(pending.Keys[0].New) != before {
		t.Fatalf("import asks for %+v, want timezone back to %s", pending.Keys, before)
	}
	parsed, _ := sshsig.ParseKey(line)
	if len(pending.Notes) != 1 || !strings.Contains(pending.Notes[0], parsed.Fingerprint()) ||
		!strings.Contains(pending.Notes[0], "made on this device") {
		t.Errorf("notes = %q", pending.Notes)
	}
	if err := f.svc.Apply(ctx, ":1.10", pending.ID, testColors); err != nil {
		t.Fatal(err)
	}
	if got := f.value(t, "timezone"); got != before {
		t.Errorf("timezone after import = %s, want %s", got, before)
	}

	f.svc.device = otherDevice
	changeTimezone(t, f)
	pending, err = f.svc.ImportSigned(ctx, ":1.10", data, sig)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range pending.Keys {
		if k.Key == "settingsSigningKey" || k.Key == "hostname" {
			t.Errorf("a file of another device brings %s", k.Key)
		}
	}
	if len(pending.Notes) != 1 || !strings.Contains(pending.Notes[0], "another device") ||
		!strings.Contains(pending.Notes[0], "settings of that device were left out") {
		t.Errorf("notes = %q", pending.Notes)
	}
}

func TestOwnKeyRefuses(t *testing.T) {
	f := newFixture(t)
	withKeys(t, f)
	ctx := context.Background()
	managed, err := f.svc.Export(ctx, ":1.10")
	if err != nil {
		t.Fatal(err)
	}
	pairFile, err := f.svc.PairExport(ctx, ":1.12")
	if err != nil {
		t.Fatal(err)
	}
	key, line := sshKey(t, "own")
	otherKey, _ := sshKey(t, "other")
	withOwnKey(t, f, line)
	changeTimezone(t, f)
	data, err := f.svc.Export(ctx, ":1.10")
	if err != nil {
		t.Fatal(err)
	}
	sig := sshSign(t, key, settings.SSHNamespace, data)

	refused := map[string]error{}
	_, refused["managed file of an own-key account"] = f.svc.Import(ctx, ":1.10", managed)
	_, refused["own-key file without its signature"] = f.svc.Import(ctx, ":1.10", data)
	_, refused["changed file"] = f.svc.ImportSigned(ctx, ":1.10", bytes.Replace(data, []byte(`"uid": 1000`), []byte(`"uid": 1001`), 1), sig)
	_, refused["other key"] = f.svc.ImportSigned(ctx, ":1.10", data, sshSign(t, otherKey, settings.SSHNamespace, data))
	_, refused["git namespace"] = f.svc.ImportSigned(ctx, ":1.10", data, sshSign(t, key, "git", data))
	_, refused["managed file with a signature"] = f.svc.ImportSigned(ctx, ":1.10", managed, sshSign(t, key, settings.SSHNamespace, managed))
	_, refused["account without own key"] = f.svc.ImportSigned(ctx, ":1.12", data, sig)
	_, refused["pairing file"] = f.svc.PairExport(ctx, ":1.10")
	_, refused["pairing check"] = f.svc.PairCheck(ctx, ":1.10", pairFile)
	for name, err := range refused {
		if !errors.Is(err, errRefused) {
			t.Errorf("%s: %v, want refused", name, err)
		}
	}

	// Other accounts keep the key GjallarOS manages.
	if other, err := f.svc.Export(ctx, ":1.12"); err != nil || settings.IsUnsigned(other) {
		t.Errorf("export of another account: %v", err)
	}

	sshsig.Keygen = ""
	if _, err := f.svc.ImportSigned(ctx, ":1.10", data, sig); !errors.Is(err, sshsig.ErrNoKeygen) || errors.Is(err, errRefused) {
		t.Errorf("without ssh-keygen: %v", err)
	}
}

func TestSigningKeyChangeSaysWhatItDoes(t *testing.T) {
	f := newFixture(t)
	withKeys(t, f)
	_, line := sshKey(t, "own")
	setValue(t, f, "username", `"tester"`)
	pending, err := f.svc.Request(context.Background(), ":1.10", `{"settingsSigningKey":`+strconv.Quote(line)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.Notes) != 1 || !strings.Contains(pending.Notes[0], "only that key signs") {
		t.Errorf("notes = %q", pending.Notes)
	}
	if len(f.sys.asked) != 1 {
		t.Errorf("an own key was set without the password: %v", f.sys.asked)
	}
}
