package secureboot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// scriptedUI answers Confirm from a list and keeps the questions.
type scriptedUI struct {
	UI
	answers []bool
	asked   []string
}

func (u *scriptedUI) Confirm(_ context.Context, message string, _ bool) (bool, error) {
	u.asked = append(u.asked, message)
	if len(u.answers) == 0 {
		return false, nil
	}
	a := u.answers[0]
	u.answers = u.answers[1:]
	return a, nil
}

func TestSbctlConfig(t *testing.T) {
	raw, err := sbctlConfig("2026-11-17")
	if err != nil {
		t.Fatal(err)
	}
	var conf struct {
		Landlock bool
		Keydir   string
		Keys     map[string]struct{ Privkey, Pubkey, Type, Description string }
	}
	if err := json.Unmarshal(raw, &conf); err != nil {
		t.Fatal(err)
	}
	if !conf.Landlock || conf.Keydir != "/var/lib/sbctl/keys" || len(conf.Keys) != 3 {
		t.Fatalf("%s", raw)
	}
	db := conf.Keys["db"]
	if db.Description != "gjallarOS Secure Boot key 2026-11-17" || db.Privkey != "/var/lib/sbctl/keys/db/db.key" ||
		db.Pubkey != "/var/lib/sbctl/keys/db/db.pem" || db.Type != "file" {
		t.Fatalf("db %+v", db)
	}
	for _, k := range conf.Keys {
		if !strings.HasPrefix(k.Privkey, "/var/lib/sbctl/keys/") {
			t.Errorf("key outside the sbctl folder: %+v", k)
		}
	}
}

// oldKeysFixture is SYNTHETIC: MokListRT with the current key, an older
// gjallarOS key, a key with sbctl's default name and a vendor key.
type oldKeysFixture struct {
	stick                        stickFixture
	current, older, sbctl, other []byte
}

func newOldKeysFixture(t *testing.T) oldKeysFixture {
	t.Helper()
	cert := func(name string) []byte {
		c, _ := synthCert(t, name, nil, nil)
		return c.Raw
	}
	f := oldKeysFixture{
		stick:   newStickFixture(t),
		current: cert(ourKeyName + " 2026-11-17"),
		older:   cert(ourKeyName + " 2026-11-01"),
		sbctl:   cert(sbctlDefaultDBName),
		other:   cert("SYNTHETIC Vendor CA"),
	}
	list := slices.Concat(x509SignatureList(f.other), x509SignatureList(f.sbctl), x509SignatureList(f.current), x509SignatureList(f.older))
	if err := os.WriteFile(filepath.Join(f.stick.fw.root.mokvars, "MokListRT"), list, 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestOldMokKeys(t *testing.T) {
	f := newOldKeysFixture(t)
	keys, err := oldMokKeys(f.stick.fw.root, f.current)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || string(keys[0].der) != string(f.sbctl) || keys[0].ours ||
		string(keys[1].der) != string(f.older) || !keys[1].ours {
		t.Fatalf("%+v", keys)
	}
}

// fakeMokDelete stands in for "sudo mokutil --delete": it reads the keys
// root staged and appends them to MokDel the way mokutil does.
func fakeMokDelete(t *testing.T, rig *rootRig, fw fakeFirmware) {
	t.Helper()
	run = func(_ context.Context, name string, args ...string) error {
		rig.steps = append(rig.steps, strings.Join(append([]string{name}, args...), " "))
		if len(args) > 1 && args[1] == "--delete" {
			raw, _, _ := fw.root.efivar("MokDel", shimLockGUID)
			for _, p := range args[2:] {
				raw = append(raw, x509SignatureList(rig.written[p])...)
			}
			fw.set("MokDel", shimLockGUID, raw)
		}
		if len(args) > 1 && args[1] == "--revoke-delete" {
			os.Remove(filepath.Join(fw.root.efivars, "MokDel-"+shimLockGUID))
		}
		return nil
	}
}

func TestQueueOldKeys(t *testing.T) {
	t.Run("ours always, sbctl default when asked", func(t *testing.T) {
		f := newOldKeysFixture(t)
		rig := newRootRig(t)
		fakeMokDelete(t, rig, f.stick.fw)
		ui := &scriptedUI{answers: []bool{true}}
		var out strings.Builder
		queued, err := queueOldKeys(context.Background(), ui, &out, f.stick.fw.root, f.current, "PASSWORD TEXT")
		if err != nil || !queued {
			t.Fatalf("%v %v", queued, err)
		}
		rig.noUserFile(t)
		if len(ui.asked) != 1 || !strings.Contains(out.String(), sha1Fingerprint(f.sbctl)) || !strings.Contains(out.String(), "PASSWORD TEXT") {
			t.Fatalf("asked %q out %q", ui.asked, out.String())
		}
		want := []string{
			"rootfile 0600 " + mokDeletePath(0),
			"rootfile 0600 " + mokDeletePath(1),
			"sudo " + privilegedTool("mokutil") + " --delete " + mokDeletePath(0) + " " + mokDeletePath(1),
			"sudo rm -f -- " + mokDeletePath(0) + " " + mokDeletePath(1),
		}
		if !slices.Equal(rig.steps, want) {
			t.Fatalf("steps %q", rig.steps)
		}
		lists, err := readMokDel(f.stick.fw.root)
		if err != nil || len(lists) != 2 || string(lists[0].Certs[0].der) != string(f.sbctl) || string(lists[1].Certs[0].der) != string(f.older) {
			t.Fatalf("MokDel %v %v", lists, err)
		}

		// A rerun finds both queued and writes nothing.
		rig.steps = nil
		queued, err = queueOldKeys(context.Background(), &scriptedUI{}, &strings.Builder{}, f.stick.fw.root, f.current, "")
		if err != nil || queued || len(rig.steps) != 0 {
			t.Fatalf("rerun: %v %v %q", queued, err, rig.steps)
		}
	})

	t.Run("keeps the sbctl default key on no", func(t *testing.T) {
		f := newOldKeysFixture(t)
		rig := newRootRig(t)
		fakeMokDelete(t, rig, f.stick.fw)
		if _, err := queueOldKeys(context.Background(), &scriptedUI{answers: []bool{false}}, &strings.Builder{}, f.stick.fw.root, f.current, ""); err != nil {
			t.Fatal(err)
		}
		if len(rig.written) != 1 || string(rig.written[mokDeletePath(0)]) != string(f.older) {
			t.Fatalf("staged %d keys", len(rig.written))
		}
	})

	t.Run("mokutil wrote nothing", func(t *testing.T) {
		f := newOldKeysFixture(t)
		newRootRig(t)
		if queued, err := queueOldKeys(context.Background(), &scriptedUI{}, &strings.Builder{}, f.stick.fw.root, f.current, ""); err == nil {
			t.Fatalf("no request written, but %v", queued)
		}
	})
}

func TestClearForeignMokDel(t *testing.T) {
	t.Run("only gjallarOS requests", func(t *testing.T) {
		f := newOldKeysFixture(t)
		f.stick.fw.set("MokDel", shimLockGUID, slices.Concat(x509SignatureList(f.older), digestList(t, sha256SignatureType, f.stick.sum)))
		rig := newRootRig(t)
		ui := &scriptedUI{}
		if err := clearForeignMokDel(context.Background(), ui, &strings.Builder{}, f.stick.fw.root, f.stick.copy, f.current); err != nil {
			t.Fatal(err)
		}
		if len(ui.asked) != 0 || len(rig.steps) != 0 {
			t.Fatalf("asked %q steps %q", ui.asked, rig.steps)
		}
	})

	t.Run("older stick shim lists (L99)", func(t *testing.T) {
		f := newOldKeysFixture(t)
		// SYNTHETIC hash of an older stick.
		older := strings.Repeat("ab", 32)
		path := filepath.Join(f.stick.fw.root.mokvars, "MokListRT")
		list, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, slices.Concat(list, digestList(t, sha256SignatureType, older)), 0o644); err != nil {
			t.Fatal(err)
		}
		f.stick.fw.set("MokDel", shimLockGUID, digestList(t, sha256SignatureType, older))
		rig := newRootRig(t)
		ui := &scriptedUI{}
		if err := clearForeignMokDel(context.Background(), ui, &strings.Builder{}, f.stick.fw.root, f.stick.copy, f.current); err != nil || len(ui.asked) != 0 || len(rig.steps) != 0 {
			t.Fatalf("%v asked %q steps %q", err, ui.asked, rig.steps)
		}
		// Not started from the stick: nothing lists it again, so it counts as foreign.
		os.Remove(filepath.Join(f.stick.fw.root.efivars, "StubImageIdentifier-"+stubLoaderGUID))
		if err := clearForeignMokDel(context.Background(), &scriptedUI{answers: []bool{false}}, &strings.Builder{}, f.stick.fw.root, f.stick.copy, f.current); err == nil {
			t.Fatal("kept without asking")
		}
	})

	foreign := map[string][]byte{
		"vendor key": x509SignatureList(nil),
		"file hash":  digestList(t, sha256SignatureType, strings.Repeat("ab", 32)),
		"TBS hash":   digestList(t, x509SHA256SignatureType, strings.Repeat("cd", 32)),
	}
	for name, entry := range foreign {
		t.Run(name, func(t *testing.T) {
			f := newOldKeysFixture(t)
			if name == "vendor key" {
				entry = x509SignatureList(f.other)
			}
			f.stick.fw.set("MokDel", shimLockGUID, slices.Concat(x509SignatureList(f.older), entry))
			rig := newRootRig(t)
			fakeMokDelete(t, rig, f.stick.fw)
			if err := clearForeignMokDel(context.Background(), &scriptedUI{answers: []bool{false}}, &strings.Builder{}, f.stick.fw.root, f.stick.copy, f.current); err == nil || len(rig.steps) != 0 {
				t.Fatalf("kept: %v %q", err, rig.steps)
			}
			if err := clearForeignMokDel(context.Background(), &scriptedUI{answers: []bool{true}}, &strings.Builder{}, f.stick.fw.root, f.stick.copy, f.current); err != nil ||
				!slices.Equal(rig.steps, []string{"sudo " + privilegedTool("mokutil") + " --revoke-delete"}) {
				t.Fatalf("dropped: %v %q", err, rig.steps)
			}
		})
	}
}

func TestSignatureLists(t *testing.T) {
	a, _ := synthCert(t, "SYNTHETIC A", nil, nil)
	sum := strings.Repeat("0f", 32)
	lists, err := signatureLists(slices.Concat(digestList(t, sha256SignatureType, sum), x509SignatureList(a.Raw)))
	if err != nil || len(lists) != 2 || lists[0].FileSums[0] != sum || string(lists[1].Certs[0].der) != string(a.Raw) {
		t.Fatalf("%+v %v", lists, err)
	}
	for _, bad := range [][]byte{make([]byte, 27), x509SignatureList(a.Raw)[:40]} {
		if _, err := signatureLists(bad); err == nil {
			t.Errorf("parsed %d bad bytes", len(bad))
		}
	}
}
