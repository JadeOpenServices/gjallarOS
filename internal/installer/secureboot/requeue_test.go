package secureboot

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeMok stands in for root mokutil the way shim's variables change: the
// import and delete requests append to MokNew and MokDel, the revokes drop
// them and --timeout -1 makes MokManager wait.
func fakeMok(t *testing.T, rig *rootRig, fw fakeFirmware) {
	t.Helper()
	appendVar := func(name string, list []byte) {
		raw, _, _ := fw.root.efivar(name, shimLockGUID)
		fw.set(name, shimLockGUID, append(raw, list...))
	}
	run = func(_ context.Context, name string, args ...string) error {
		rig.steps = append(rig.steps, strings.Join(append([]string{name}, args...), " "))
		if len(args) < 2 || args[0] != privilegedTool("mokutil") {
			return nil
		}
		switch args[1] {
		case "--import":
			appendVar("MokNew", x509SignatureList(rig.written[args[2]]))
		case "--delete":
			for _, p := range args[2:] {
				appendVar("MokDel", x509SignatureList(rig.written[p]))
			}
		case "--delete-hash":
			appendVar("MokDel", digestList(t, sha256SignatureType, args[2]))
		case "--revoke-import":
			os.Remove(filepath.Join(fw.root.efivars, "MokNew-"+shimLockGUID))
		case "--revoke-delete":
			os.Remove(filepath.Join(fw.root.efivars, "MokDel-"+shimLockGUID))
		case "--timeout":
			fw.set("MokTimeout", shimLockGUID, []byte{0xff, 0xff, 0xff, 0xff})
		}
		return nil
	}
}

// L97: MokManager enrolls everything in MokNew at once.
func TestClearForeignMokNew(t *testing.T) {
	t.Run("only the gjallarOS key", func(t *testing.T) {
		f := newOldKeysFixture(t)
		f.stick.fw.set("MokNew", shimLockGUID, x509SignatureList(f.current))
		rig := newRootRig(t)
		ui := &scriptedUI{}
		if err := clearForeignMokNew(context.Background(), ui, &strings.Builder{}, f.stick.fw.root, f.current); err != nil {
			t.Fatal(err)
		}
		if len(ui.asked) != 0 || len(rig.steps) != 0 {
			t.Fatalf("asked %q steps %q", ui.asked, rig.steps)
		}
	})

	foreign := map[string][]byte{
		"vendor key": nil,
		"file hash":  digestList(t, sha256SignatureType, strings.Repeat("ab", 32)),
		"TBS hash":   digestList(t, x509SHA256SignatureType, strings.Repeat("cd", 32)),
	}
	for name, entry := range foreign {
		t.Run(name, func(t *testing.T) {
			f := newOldKeysFixture(t)
			if entry == nil {
				entry = x509SignatureList(f.other)
			}
			f.stick.fw.set("MokNew", shimLockGUID, slices.Concat(x509SignatureList(f.current), entry))
			rig := newRootRig(t)
			fakeMok(t, rig, f.stick.fw)
			var out strings.Builder
			if err := clearForeignMokNew(context.Background(), &scriptedUI{answers: []bool{false}}, &out, f.stick.fw.root, f.current); err == nil || len(rig.steps) != 0 {
				t.Fatalf("kept: %v %q", err, rig.steps)
			}
			if strings.Contains(out.String(), ourKeyName+" 2026-11-17") {
				t.Errorf("the gjallarOS key listed as foreign: %q", out.String())
			}
			if err := clearForeignMokNew(context.Background(), &scriptedUI{answers: []bool{true}}, &strings.Builder{}, f.stick.fw.root, f.current); err != nil ||
				!slices.Equal(rig.steps, []string{"sudo " + privilegedTool("mokutil") + " --revoke-import"}) {
				t.Fatalf("dropped: %v %q", err, rig.steps)
			}
			if _, present, _ := f.stick.fw.root.shimRequest("MokNew"); present {
				t.Fatal("MokNew still there")
			}
		})
	}
}

// missedVisit is SYNTHETIC firmware after a missed MokManager visit (Q234):
// shim dropped the requests, so MokListRT still lacks the new key and holds
// the older keys and the stick's hash; MokNew holds a key another system
// queued since.
func missedVisit(t *testing.T) (oldKeysFixture, *rootRig) {
	t.Helper()
	f := newOldKeysFixture(t)
	list := slices.Concat(x509SignatureList(f.other), x509SignatureList(f.sbctl), x509SignatureList(f.older), digestList(t, sha256SignatureType, f.stick.sum))
	if err := os.WriteFile(filepath.Join(f.stick.fw.root.mokvars, "MokListRT"), list, 0o644); err != nil {
		t.Fatal(err)
	}
	planted, _ := synthCert(t, "SYNTHETIC Planted", nil, nil)
	f.stick.fw.set("MokNew", shimLockGUID, x509SignatureList(planted.Raw))
	savedFw, savedCopy := liveFirmware, liveStickCopy
	t.Cleanup(func() { liveFirmware, liveStickCopy = savedFw, savedCopy })
	liveFirmware, liveStickCopy = f.stick.fw.root, f.stick.copy
	rig := newRootRig(t)
	fakeMok(t, rig, f.stick.fw)
	return f, rig
}

func TestQueueVisitAfterMissedVisit(t *testing.T) {
	f, rig := missedVisit(t)
	ui := &scriptedUI{answers: []bool{true, false}}
	visit, err := queueVisit(context.Background(), ui, &strings.Builder{}, f.current)
	if err != nil {
		t.Fatal(err)
	}
	rig.noUserFile(t)
	mok := "sudo " + privilegedTool("mokutil")
	want := []string{
		mok + " --revoke-import",
		"rootfile 0600 " + mokImportPath,
		mok + " --import " + mokImportPath,
		"sudo rm -f -- " + mokImportPath,
		"rootfile 0600 " + mokDeletePath(0),
		mok + " --delete " + mokDeletePath(0),
		"sudo rm -f -- " + mokDeletePath(0),
		mok + " --delete-hash " + f.stick.sum,
		mok + " --timeout -1",
	}
	if !slices.Equal(rig.steps, want) {
		t.Fatalf("steps %q", rig.steps)
	}
	if len(ui.asked) != 2 {
		t.Fatalf("asked %q", ui.asked)
	}
	raw, _, _ := f.stick.fw.root.shimRequest("MokNew")
	if lists, err := signatureLists(raw); err != nil || len(lists) != 1 || len(lists[0].Certs) != 1 || string(lists[0].Certs[0].der) != string(f.current) {
		t.Fatalf("MokNew holds more than the gjallarOS key: %v", err)
	}
	if string(visit.key) != string(f.current) || visit.keyReused || len(visit.del) != 2 || visit.delReused {
		t.Fatalf("visit %+v", visit)
	}
}

func TestQueueVisitKeepsForeignRequest(t *testing.T) {
	f, rig := missedVisit(t)
	if _, err := queueVisit(context.Background(), &scriptedUI{answers: []bool{false}}, &strings.Builder{}, f.current); err == nil || len(rig.steps) != 0 {
		t.Fatalf("queued next to a foreign request: %v %q", err, rig.steps)
	}
}

func TestRequeueKey(t *testing.T) {
	t.Run("refuses a key gjallarOS did not name", func(t *testing.T) {
		f, rig := missedVisit(t)
		if _, err := RequeueKey(context.Background(), &scriptedUI{}, &strings.Builder{}, f.sbctl); err == nil || len(rig.steps) != 0 {
			t.Fatalf("%v %q", err, rig.steps)
		}
		if _, err := RequeueKey(context.Background(), &scriptedUI{}, &strings.Builder{}, []byte("EXAMPLE not a certificate")); err == nil {
			t.Fatal("queued bytes that do not parse")
		}
	})
	t.Run("refuses a start without shim", func(t *testing.T) {
		f, rig := missedVisit(t)
		os.Remove(filepath.Join(f.stick.fw.root.mokvars, "MokListRT"))
		if _, err := RequeueKey(context.Background(), &scriptedUI{}, &strings.Builder{}, f.current); err == nil || len(rig.steps) != 0 {
			t.Fatalf("%v %q", err, rig.steps)
		}
	})
	t.Run("queues and prints the steps", func(t *testing.T) {
		f, _ := missedVisit(t)
		var out strings.Builder
		queued, err := RequeueKey(context.Background(), &scriptedUI{answers: []bool{true, false}}, &out, f.current)
		if err != nil || !queued {
			t.Fatalf("%v %v", queued, err)
		}
		s := out.String()
		for _, want := range []string{"fingerprint\n" + sha1Fingerprint(f.current, "  ") + "\n", sha1Fingerprint(f.current, stepIndent), "Enroll MOK", "Delete MOK", "pick Queue the key again"} {
			if !strings.Contains(s, want) {
				t.Errorf("output lacks %q:\n%s", want, s)
			}
		}
	})
	t.Run("nothing to do once shim lists the key", func(t *testing.T) {
		f, rig := missedVisit(t)
		list := slices.Concat(x509SignatureList(f.other), x509SignatureList(f.current))
		if err := os.WriteFile(filepath.Join(f.stick.fw.root.mokvars, "MokListRT"), list, 0o644); err != nil {
			t.Fatal(err)
		}
		os.Remove(filepath.Join(f.stick.fw.root.efivars, "MokNew-"+shimLockGUID))
		queued, err := RequeueKey(context.Background(), &scriptedUI{}, &strings.Builder{}, f.current)
		if err != nil || queued || len(rig.steps) != 0 {
			t.Fatalf("%v %v %q", queued, err, rig.steps)
		}
	})
}

// A request that lands in MokNew between the check and mokutil --import
// stops the visit before MokManager is held.
func TestQueueVisitRefusesRacedMokNew(t *testing.T) {
	f, rig := missedVisit(t)
	planted, _ := synthCert(t, "SYNTHETIC Raced", nil, nil)
	fake := run
	run = func(ctx context.Context, name string, args ...string) error {
		err := fake(ctx, name, args...)
		if len(args) > 1 && args[1] == "--import" {
			raw, _, _ := f.stick.fw.root.efivar("MokNew", shimLockGUID)
			f.stick.fw.set("MokNew", shimLockGUID, append(raw, x509SignatureList(planted.Raw)...))
		}
		return err
	}
	if _, err := queueVisit(context.Background(), &scriptedUI{answers: []bool{true, false}}, &strings.Builder{}, f.current); err == nil {
		t.Fatal("queued next to a raced request")
	}
	if slices.ContainsFunc(rig.steps, func(s string) bool { return strings.HasSuffix(s, "--timeout -1") }) {
		t.Fatalf("held MokManager: %q", rig.steps)
	}
	if rig.steps[len(rig.steps)-1] != "sudo "+privilegedTool("mokutil")+" --revoke-import" {
		t.Fatalf("request kept: %q", rig.steps)
	}
	if _, present, _ := f.stick.fw.root.shimRequest("MokNew"); present {
		t.Fatal("MokNew still there")
	}
}
