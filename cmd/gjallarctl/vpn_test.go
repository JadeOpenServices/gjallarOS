package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/nettrust"
)

func fakeVPNPaths(t *testing.T) (reapplied *int) {
	t.Helper()
	dir := t.TempDir()
	oldStatus, oldPolicy, oldOverride, oldReapply, oldEUID := vpnStatusPath, vpnPolicyPath, vpnOverridePath, vpnReapply, vpnEUID
	t.Cleanup(func() {
		vpnStatusPath, vpnPolicyPath, vpnOverridePath, vpnReapply, vpnEUID = oldStatus, oldPolicy, oldOverride, oldReapply, oldEUID
	})
	vpnStatusPath = filepath.Join(dir, "status.json")
	vpnPolicyPath = filepath.Join(dir, "policy.json")
	vpnOverridePath = filepath.Join(dir, "state", "override.json")
	vpnEUID = func() int { return 0 }
	n := 0
	vpnReapply = func() error { n++; return nil }
	write := func(path, data string) {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(vpnPolicyPath, `{"homeSubnets":["192.168.8.0/24"],"trustedWifis":["home"],"exitNode":"OpenWrt","siteRouterTrust":false,"siteRouterTargets":[]}`)
	write(vpnStatusPath, `{"trust":"untrusted","reason":"r","bypass":[],"exitNode":"OpenWrt","manageExitNode":true}`)
	return &n
}

func TestVPNOverrideCommands(t *testing.T) {
	reapplied := fakeVPNPaths(t)
	var out, errOut bytes.Buffer
	for _, args := range [][]string{
		{"trust-wifi", "Synthetic 2,4"},
		{"untrust-wifi", "home"},
		{"exit-node", "auto"},
	} {
		if code := runVPN(args, &out, &errOut); code != 0 {
			t.Fatalf("%q: code %d: %s", args, code, errOut.String())
		}
	}
	if *reapplied != 3 {
		t.Fatalf("reapplied %d times", *reapplied)
	}
	if !strings.Contains(out.String(), `override:  trust "Synthetic 2,4", untrust "home", exit node auto`) {
		t.Fatalf("status:\n%s", out.String())
	}

	out.Reset()
	if code := runVPN([]string{"export"}, &out, &errOut); code != 0 {
		t.Fatalf("export: %s", errOut.String())
	}
	want := `tailscale = { enable = true; homeSubnets = [ "192.168.8.0/24" ]; trustedWifis = [ "Synthetic 2,4" ]; exitNode = "auto"; wifiExitNodes = { }; routerHome = false; siteRouterTrust = false; siteRouterTargets = [ ]; };`
	if strings.TrimSpace(out.String()) != want {
		t.Fatalf("export = %s", out.String())
	}

	for _, args := range [][]string{{"trust-wifi", "home"}, {"untrust-wifi", "Synthetic 2,4"}, {"exit-node", "default"}} {
		if code := runVPN(args, &out, &errOut); code != 0 {
			t.Fatalf("%q: %s", args, errOut.String())
		}
	}
	if _, err := os.Stat(vpnOverridePath); !os.IsNotExist(err) {
		t.Fatalf("override left behind after undoing every change: %v", err)
	}
}

func TestVPNTrustWifiWithExitNode(t *testing.T) {
	fakeVPNPaths(t)
	var out, errOut bytes.Buffer
	for _, args := range [][]string{
		{"trust-wifi", "--exit-node", "OpenWrt", "Synthetic 2,4"},
		{"trust-wifi", "home", "--exit-node=auto"},
	} {
		if code := runVPN(args, &out, &errOut); code != 0 {
			t.Fatalf("%q: code %d: %s", args, code, errOut.String())
		}
	}
	if !strings.Contains(out.String(), `exit node OpenWrt on "Synthetic 2,4", exit node auto on "home"`) {
		t.Fatalf("status:\n%s", out.String())
	}
	out.Reset()
	if code := runVPN([]string{"export"}, &out, &errOut); code != 0 {
		t.Fatalf("export: %s", errOut.String())
	}
	if !strings.Contains(out.String(), `trustedWifis = [ "home" "Synthetic 2,4" ]; exitNode = "OpenWrt"; wifiExitNodes = { "Synthetic 2,4" = "OpenWrt"; "home" = "auto"; };`) {
		t.Fatalf("export = %s", out.String())
	}

	for _, args := range [][]string{
		{"trust-wifi", "home", "--exit-node", "default"},
		{"untrust-wifi", "Synthetic 2,4"},
	} {
		if code := runVPN(args, &out, &errOut); code != 0 {
			t.Fatalf("%q: %s", args, errOut.String())
		}
	}
	if _, err := os.Stat(vpnOverridePath); !os.IsNotExist(err) {
		t.Fatalf("override left behind: %v", err)
	}

	for _, args := range [][]string{
		{"trust-wifi", "home", "--exit-node"},
		{"trust-wifi", "home", "--exit-node", "a", "--exit-node", "b"},
		{"trust-wifi", "home", "--bogus"},
		{"untrust-wifi", "home", "--exit-node", "a"},
	} {
		if code := runVPN(args, &out, &errOut); code != 2 {
			t.Fatalf("%q: code %d, want 2", args, code)
		}
	}
}

func TestVPNOverrideNeedsRoot(t *testing.T) {
	reapplied := fakeVPNPaths(t)
	vpnEUID = func() int { return 1000 }
	var out, errOut bytes.Buffer
	if code := runVPN([]string{"exit-node", "off"}, &out, &errOut); code != 1 || *reapplied != 0 {
		t.Fatalf("code %d, reapplied %d", code, *reapplied)
	}
	if _, err := os.Stat(vpnOverridePath); !os.IsNotExist(err) {
		t.Fatal("non-root wrote an override")
	}
}

func TestVPNPauseAsksTheService(t *testing.T) {
	fakeVPNPaths(t)
	bus := &fakeSettingsBus{}
	useFakeSettings(t, bus)
	vpnEUID = func() int { return 1000 }
	var out, errOut bytes.Buffer
	if code := runVPN([]string{"pause"}, &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if len(bus.asked) != 1 || bus.asked[0] != "PauseVPN" {
		t.Fatalf("asked %v", bus.asked)
	}
	if !strings.Contains(out.String(), "without the tunnel") {
		t.Fatalf("output %q", out.String())
	}
	vpnEUID = func() int { return 0 }
	if code := runVPN([]string{"pause"}, &out, &errOut); code != 1 || len(bus.asked) != 1 {
		t.Fatalf("root pause: code %d, asked %v", code, bus.asked)
	}
}

func TestVPNResume(t *testing.T) {
	reapplied := fakeVPNPaths(t)
	oldPause := nettrust.PausePath
	t.Cleanup(func() { nettrust.PausePath = oldPause })
	nettrust.PausePath = filepath.Join(t.TempDir(), "pause.json")
	if err := os.WriteFile(nettrust.PausePath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runVPN([]string{"resume"}, &out, &errOut); code != 0 || *reapplied != 1 {
		t.Fatalf("root resume: code %d, reapplied %d: %s", code, *reapplied, errOut.String())
	}
	if _, err := os.Stat(nettrust.PausePath); !os.IsNotExist(err) {
		t.Fatalf("pause file still there: %v", err)
	}
	if code := runVPN([]string{"resume"}, &out, &errOut); code != 0 || *reapplied != 1 {
		t.Fatalf("resume without a pause: code %d, reapplied %d", code, *reapplied)
	}

	bus := &fakeSettingsBus{}
	useFakeSettings(t, bus)
	vpnEUID = func() int { return 1000 }
	if code := runVPN([]string{"resume"}, &out, &errOut); code != 0 || len(bus.asked) != 1 || bus.asked[0] != "ResumeVPN" {
		t.Fatalf("user resume: code %d, asked %v", code, bus.asked)
	}
	if *reapplied != 1 {
		t.Fatal("a user's resume ran the decision itself instead of the service")
	}
}

func TestVPNBootBlock(t *testing.T) {
	fakeVPNPaths(t)
	old := vpnBootBlock
	t.Cleanup(func() { vpnBootBlock = old })
	blocked := 0
	vpnBootBlock = func(context.Context) error { blocked++; return nil }
	run := func(policy string) string {
		t.Helper()
		var out, errOut bytes.Buffer
		args := []string{"boot-block", "--policy", policy, "--override", vpnOverridePath}
		if code := runVPN(args, &out, &errOut); code != 0 {
			t.Fatalf("code %d: %s", code, errOut.String())
		}
		return out.String() + errOut.String()
	}

	if out := run(vpnPolicyPath); blocked != 0 || !strings.Contains(out, "already checked") {
		t.Fatalf("with a verdict: blocked %d, %q", blocked, out)
	}
	os.Remove(vpnStatusPath)
	if out := run(vpnPolicyPath); blocked != 1 || !strings.Contains(out, nettrust.BootBlockText) {
		t.Fatalf("exit node wanted: blocked %d, %q", blocked, out)
	}

	off := filepath.Join(t.TempDir(), "off.json")
	os.WriteFile(off, []byte(`{"homeSubnets":[],"trustedWifis":["home"],"exitNode":"off"}`), 0o644)
	if out := run(off); blocked != 1 || !strings.Contains(out, "no boot block") {
		t.Fatalf("exit node off: blocked %d, %q", blocked, out)
	}

	// The override turns the exit node off, and a broken one is ignored.
	os.MkdirAll(filepath.Dir(vpnOverridePath), 0o755)
	os.WriteFile(vpnOverridePath, []byte(`{"exitNode":"off"}`), 0o644)
	if out := run(vpnPolicyPath); blocked != 1 || !strings.Contains(out, "no boot block") {
		t.Fatalf("override off: blocked %d, %q", blocked, out)
	}
	os.WriteFile(vpnOverridePath, []byte(`{`), 0o644)
	if out := run(vpnPolicyPath); blocked != 2 || !strings.Contains(out, "ignoring override") {
		t.Fatalf("broken override: blocked %d, %q", blocked, out)
	}

	// A policy that cannot be read may want an exit node.
	if out := run(filepath.Join(t.TempDir(), "missing.json")); blocked != 3 || !strings.Contains(out, "blocking anyway") {
		t.Fatalf("no policy: blocked %d, %q", blocked, out)
	}

	vpnEUID = func() int { return 1000 }
	var out, errOut bytes.Buffer
	if code := runVPN([]string{"boot-block", "--policy", vpnPolicyPath}, &out, &errOut); code != 1 || blocked != 3 {
		t.Fatalf("as a user: code %d, blocked %d", code, blocked)
	}
}

func TestVPNStatusReportsTheBootBlock(t *testing.T) {
	fakeVPNPaths(t)
	old := nettrust.BootBlockPath
	t.Cleanup(func() { nettrust.BootBlockPath = old })
	nettrust.BootBlockPath = filepath.Join(t.TempDir(), "vpn-boot-block")
	status := func() (int, string) {
		var out, errOut bytes.Buffer
		code := runVPN([]string{"status"}, &out, &errOut)
		return code, out.String() + errOut.String()
	}

	if _, out := status(); strings.Contains(out, nettrust.BootBlockText) {
		t.Fatalf("boot block without a marker:\n%s", out)
	}
	os.WriteFile(nettrust.BootBlockPath, nil, 0o644)
	if code, out := status(); code != 0 || !strings.Contains(out, "network:   untrusted") || !strings.Contains(out, nettrust.BootBlockText) {
		t.Fatalf("offline verdict, boot table left:\n%s", out)
	}
	os.Remove(vpnStatusPath)
	if code, out := status(); code != 0 || !strings.Contains(out, "not checked yet") || !strings.Contains(out, nettrust.BootBlockText) {
		t.Fatalf("no verdict yet:\n%s", out)
	}
	os.Remove(nettrust.BootBlockPath)
	if code, _ := status(); code != 1 {
		t.Fatal("no verdict and no boot block is not an error")
	}
}
