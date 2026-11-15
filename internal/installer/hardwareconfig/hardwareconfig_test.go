package hardwareconfig

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	calls   [][]string
	outputs map[string][]byte
	errors  map[string]error
	content []byte
}

func commandKey(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), "\x00")
}

func (f *fakeRunner) Run(
	_ context.Context,
	stdout io.Writer,
	_ io.Writer,
	name string,
	args ...string,
) error {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)

	if err := f.errors[commandKey(name, args...)]; err != nil {
		return err
	}

	_, err := stdout.Write(f.content)
	return err
}

func (f *fakeRunner) Output(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)

	k := commandKey(name, args...)
	if err := f.errors[k]; err != nil {
		return nil, err
	}
	if out, ok := f.outputs[k]; ok {
		return out, nil
	}
	return nil, fmt.Errorf("unexpected output command: %v", call)
}

func nixModule() []byte {
	return []byte(`{ config, lib, pkgs, modulesPath, ... }:

{
  boot.initrd.availableKernelModules = [ "nvme" ];
  fileSystems."/" = {
    device = "/dev/disk/by-uuid/example";
    fsType = "ext4";
  };
}
`)
}

func TestValidateTarget(t *testing.T) {
	root := t.TempDir()
	valid := Target(root)

	if got, err := ValidateTarget(root, valid); err != nil || got != valid {
		t.Fatalf("got %q, %v", got, err)
	}

	for _, invalid := range []string{
		filepath.Join(root, "hardware-configuration.nix"),
		filepath.Join(root, "..", "hardware-configuration.nix"),
		filepath.Join(root, "profiles", "x", "other.nix"),
	} {
		if _, err := ValidateTarget(root, invalid); err == nil {
			t.Fatalf("accepted invalid target %s", invalid)
		}
	}
}

func TestGenerateTargetUsesMountedMnt(t *testing.T) {
	root := t.TempDir()
	target := Target(root)

	r := &fakeRunner{
		outputs: map[string][]byte{
			commandKey(
				"findmnt",
				"-nro",
				"TARGET",
				"--mountpoint",
				"/mnt",
			): []byte("/mnt\n"),
		},
		errors:  map[string]error{},
		content: nixModule(),
	}

	generated, err := generate(
		context.Background(),
		root,
		target,
		time.Unix(0, 0),
		"/mnt",
		r,
	)
	if err != nil {
		t.Fatal(err)
	}
	if generated.Backup != "" || len(generated.DroppedSwap) != 0 {
		t.Fatalf("unexpected result %+v", generated)
	}

	joined := callsText(r.calls)
	if !strings.Contains(
		joined,
		"sudo nixos-generate-config --root /mnt --show-hardware-config",
	) {
		t.Fatalf(
			"fresh target generation did not use --root /mnt:\n%s",
			joined,
		)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, nixModule()) {
		t.Fatalf("unexpected generated content:\n%s", data)
	}
}

func TestGenerateCurrentMachinePreservesExistingBehavior(t *testing.T) {
	root := t.TempDir()
	target := Target(root)

	r := &fakeRunner{
		outputs: map[string][]byte{},
		errors:  map[string]error{},
		content: nixModule(),
	}

	_, err := generate(
		context.Background(),
		root,
		target,
		time.Unix(0, 0),
		"",
		r,
	)
	if err != nil {
		t.Fatal(err)
	}

	joined := callsText(r.calls)
	if !strings.Contains(
		joined,
		"sudo nixos-generate-config --show-hardware-config",
	) {
		t.Fatalf(
			"existing generation behavior changed:\n%s",
			joined,
		)
	}
	if strings.Contains(joined, "--root") {
		t.Fatalf(
			"current-machine generation unexpectedly uses --root:\n%s",
			joined,
		)
	}
}

func TestGenerateTargetRequiresMountedMnt(t *testing.T) {
	root := t.TempDir()
	target := Target(root)

	r := &fakeRunner{
		outputs: map[string][]byte{},
		errors: map[string]error{
			commandKey(
				"findmnt",
				"-nro",
				"TARGET",
				"--mountpoint",
				"/mnt",
			): fmt.Errorf("not mounted"),
		},
		content: nixModule(),
	}

	_, err := generate(
		context.Background(),
		root,
		target,
		time.Unix(0, 0),
		"/mnt",
		r,
	)
	if err == nil || !strings.Contains(err.Error(), "not mounted") {
		t.Fatalf("unmounted /mnt accepted: %v", err)
	}

	if strings.Contains(callsText(r.calls), "nixos-generate-config") {
		t.Fatalf(
			"generation ran before /mnt validation:\n%s",
			callsText(r.calls),
		)
	}
}

func TestGenerateTargetBacksUpExistingHardwareState(t *testing.T) {
	root := t.TempDir()
	target := Target(root)

	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}

	original := []byte("ORIGINAL HARDWARE CONFIG\n")
	if err := os.WriteFile(target, original, 0644); err != nil {
		t.Fatal(err)
	}

	r := &fakeRunner{
		outputs: map[string][]byte{
			commandKey(
				"findmnt",
				"-nro",
				"TARGET",
				"--mountpoint",
				"/mnt",
			): []byte("/mnt\n"),
		},
		errors:  map[string]error{},
		content: nixModule(),
	}

	now := time.Date(
		2026, 9, 7,
		21, 30, 45,
		0,
		time.UTC,
	)

	generated, err := generate(
		context.Background(),
		root,
		target,
		now,
		"/mnt",
		r,
	)
	if err != nil {
		t.Fatal(err)
	}
	backup := generated.Backup

	expectedBackup := target + ".bak.20260907213045"
	if backup != expectedBackup {
		t.Fatalf(
			"backup = %q, expected %q",
			backup,
			expectedBackup,
		)
	}

	gotBackup, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBackup, original) {
		t.Fatalf("backup changed:\n%s", gotBackup)
	}
}

func TestInvalidGeneratedContentNeverReplacesExistingFile(t *testing.T) {
	root := t.TempDir()
	target := Target(root)

	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}

	original := []byte("ORIGINAL\n")
	if err := os.WriteFile(target, original, 0644); err != nil {
		t.Fatal(err)
	}

	r := &fakeRunner{
		outputs: map[string][]byte{
			commandKey(
				"findmnt",
				"-nro",
				"TARGET",
				"--mountpoint",
				"/mnt",
			): []byte("/mnt\n"),
		},
		errors:  map[string]error{},
		content: []byte("garbage"),
	}

	_, err := generate(
		context.Background(),
		root,
		target,
		time.Unix(0, 0),
		"/mnt",
		r,
	)
	if err == nil || !strings.Contains(err.Error(), "does not look like") {
		t.Fatalf("invalid generated output accepted: %v", err)
	}

	current, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, original) {
		t.Fatalf(
			"existing hardware state was overwritten:\n%s",
			current,
		)
	}
}

func TestFreshTargetGenerationDoesNotMutateFlake(t *testing.T) {
	root := t.TempDir()

	flake := filepath.Join(root, "flake.nix")
	original := []byte("{ outputs = _: {}; }\n")
	if err := os.WriteFile(flake, original, 0644); err != nil {
		t.Fatal(err)
	}

	target := Target(root)

	r := &fakeRunner{
		outputs: map[string][]byte{
			commandKey(
				"findmnt",
				"-nro",
				"TARGET",
				"--mountpoint",
				"/mnt",
			): []byte("/mnt\n"),
		},
		errors:  map[string]error{},
		content: nixModule(),
	}

	if _, err := generate(
		context.Background(),
		root,
		target,
		time.Unix(0, 0),
		"/mnt",
		r,
	); err != nil {
		t.Fatal(err)
	}

	current, err := os.ReadFile(flake)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, original) {
		t.Fatalf("flake.nix was modified:\n%s", current)
	}
}

func callsText(calls [][]string) string {
	var lines []string
	for _, call := range calls {
		lines = append(lines, strings.Join(call, " "))
	}
	return strings.Join(lines, "\n")
}

func TestBackupFileNumbersSameSecondReruns(t *testing.T) {
	target := filepath.Join(t.TempDir(), "hardware.nix")
	if err := os.WriteFile(target, []byte("A\n"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 8, 12, 41, 29, 0, time.UTC)

	first, err := backupFile(target, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := backupFile(target, now)
	if err != nil {
		t.Fatal(err)
	}
	if first != target+".bak.20261008124129" || second != first+".1" {
		t.Fatalf("backups %s, %s", first, second)
	}
}

// The shape nixos-generate-config prints for the stock NixOS installer's
// encrypted root plus encrypted swap (multiLineList in
// nixos-generate-config.pl): only the root's LUKS mapping, and the swap
// as a device that appears only after an unlock gjallarOS never does.
func calamaresRootAndSwap() []byte {
	return []byte(`{ config, lib, pkgs, modulesPath, ... }:

{
  boot.initrd.availableKernelModules = [ "nvme" "xhci_pci" ];
  boot.initrd.luks.devices."luks-root-example".device = "/dev/disk/by-uuid/root-example";

  fileSystems."/" =
    { device = "/dev/mapper/luks-root-example";
      fsType = "ext4";
    };

  swapDevices =
    [ { device = "/dev/disk/by-uuid/swap-example"; }
    ];

  networking.useDHCP = lib.mkDefault true;
}
`)
}

func TestDropDiskSwapLeavesOnlyZram(t *testing.T) {
	cleaned, dropped, err := dropDiskSwap(calamaresRootAndSwap())
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 1 || dropped[0] != "/dev/disk/by-uuid/swap-example" {
		t.Fatalf("dropped = %v", dropped)
	}
	text := string(cleaned)
	if !strings.Contains(text, "  swapDevices = [ ];\n") || strings.Contains(text, "swap-example") {
		t.Fatalf("swap left in:\n%s", text)
	}
	for _, kept := range []string{
		`boot.initrd.luks.devices."luks-root-example".device = "/dev/disk/by-uuid/root-example";`,
		`{ device = "/dev/mapper/luks-root-example";`,
		"networking.useDHCP = lib.mkDefault true;",
	} {
		if !strings.Contains(text, kept) {
			t.Fatalf("lost %q:\n%s", kept, text)
		}
	}
}

func TestDropDiskSwapKeepsEmptyAndMissingLists(t *testing.T) {
	for name, module := range map[string][]byte{
		"empty":   []byte("{ ... }:\n{\n  swapDevices = [ ];\n}\n"),
		"missing": nixModule(),
	} {
		cleaned, dropped, err := dropDiskSwap(module)
		if err != nil || len(dropped) != 0 || string(cleaned) != string(module) {
			t.Fatalf("%s: changed to %q, dropped %v, err %v", name, cleaned, dropped, err)
		}
	}
}

func TestDropDiskSwapRefusesUnknownShapes(t *testing.T) {
	for name, module := range map[string]string{
		"twice":        "{ ... }:\n{\n  swapDevices = [ ];\n  swapDevices = [ ];\n}\n",
		"unterminated": "{ ... }:\n{\n  swapDevices =\n    [ { device = \"/dev/sda2\"; }\n}\n",
	} {
		if _, _, err := dropDiskSwap([]byte(module)); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func TestGenerateDropsDiskSwap(t *testing.T) {
	root := t.TempDir()
	target := Target(root)
	r := &fakeRunner{
		outputs: map[string][]byte{
			commandKey("findmnt", "-nro", "TARGET", "--mountpoint", "/mnt"): []byte("/mnt\n"),
		},
		errors:  map[string]error{},
		content: calamaresRootAndSwap(),
	}
	generated, err := generate(context.Background(), root, target, time.Unix(0, 0), "/mnt", r)
	if err != nil {
		t.Fatal(err)
	}
	if len(generated.DroppedSwap) != 1 || generated.DroppedSwap[0] != "/dev/disk/by-uuid/swap-example" {
		t.Fatalf("dropped = %v", generated.DroppedSwap)
	}
	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), "swap-example") || !strings.Contains(string(written), "swapDevices = [ ];") {
		t.Fatalf("written hardware configuration keeps the swap:\n%s", written)
	}
	notice := DroppedSwapNotice(generated.DroppedSwap)
	if !strings.Contains(notice, "/dev/disk/by-uuid/swap-example") || !strings.Contains(notice, "nothing on it was changed") {
		t.Fatalf("notice %q", notice)
	}
	if DroppedSwapNotice(nil) != "" {
		t.Fatal("notice without dropped swap")
	}
}
