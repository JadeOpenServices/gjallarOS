package baremetalinstall

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls   [][]string
	outputs map[string][]byte
	errors  map[string]error
	onRun   func()
}

func commandKey(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), "\x00")
}

func (f *fakeRunner) Run(
	ctx context.Context,
	_ io.Reader,
	_ io.Writer,
	_ io.Writer,
	name string,
	args ...string,
) error {
	// Like exec.CommandContext: a cancelled context never starts the command.
	if err := ctx.Err(); err != nil {
		return err
	}
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)

	if f.onRun != nil {
		f.onRun()
	}

	return f.errors[commandKey(name, args...)]
}

func (f *fakeRunner) Output(
	ctx context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)

	k := commandKey(name, args...)
	if err := f.errors[k]; err != nil {
		return nil, err
	}
	if output, ok := f.outputs[k]; ok {
		return output, nil
	}

	return nil, fmt.Errorf("unexpected output command: %v", call)
}

func testRepo(t *testing.T) string {
	t.Helper()

	root := t.TempDir()

	if err := os.WriteFile(
		filepath.Join(root, "flake.nix"),
		[]byte("{ outputs = _: {}; }\n"),
		0644,
	); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(root, "generated"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "generated", "state.nix"),
		[]byte("{ hostname = \"gjallarOS\"; }\n"),
		0644,
	); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(root, "user.config.json"),
		[]byte("{\"profile\":\"laptop\"}\n"),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	return root
}

func mountedRunner() *fakeRunner {
	return &fakeRunner{
		outputs: map[string][]byte{
			commandKey(
				"findmnt",
				"-nro",
				"TARGET",
				"--mountpoint",
				"/mnt",
			): []byte("/mnt\n"),
			commandKey(
				"findmnt",
				"-nro",
				"TARGET",
				"--mountpoint",
				"/mnt/boot",
			): []byte("/mnt/boot\n"),
			commandKey("swapon", "--show=NAME", "--noheadings"):             nil,
			commandKey("findmnt", "-nro", "FSTYPE", "--mountpoint", "/mnt"): []byte("btrfs\n"),
		},
		errors: map[string]error{},
	}
}

func createInstalledProfile(t *testing.T) {
	t.Helper()

	path := "/mnt/nix/var/nix/profiles"
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Skipf("cannot create temporary /mnt test fixture: %v", err)
	}

	system := filepath.Join(path, "system")
	_ = os.Remove(system)

	if err := os.Symlink("/nix/store/test-system", system); err != nil {
		t.Skipf("cannot create /mnt system profile fixture: %v", err)
	}
	store := "/mnt/nix/store/test-system"
	if err := os.MkdirAll(store, 0755); err != nil {
		t.Skipf("cannot create /mnt store fixture: %v", err)
	}

	t.Cleanup(func() {
		_ = os.Remove(system)
		_ = os.Remove(store)
	})
}

func TestInstallUsesOnlyPreparedTarget(t *testing.T) {
	createInstalledProfile(t)

	repo := testRepo(t)
	r := mountedRunner()
	var out bytes.Buffer

	result, err := install(
		context.Background(),
		Input{
			Repo:     repo,
			Hostname: "gjallarOS",
			Out:      &out,
		},
		r,
	)
	if err != nil {
		t.Fatal(err)
	}

	if result.Stage != "os-installed" {
		t.Fatalf("stage = %q", result.Stage)
	}

	joined := callsText(r.calls)
	required := strings.Join([]string{
		"sudo",
		"nixos-install",
		"--root",
		"/mnt",
		"--flake",
		"path:" + repo + "#gjallarOS",
		"--no-root-passwd",
	}, " ")

	if !strings.Contains(joined, required) {
		t.Fatalf(
			"missing expected nixos-install invocation:\n%s",
			joined,
		)
	}

	for _, forbidden := range []string{
		"sgdisk",
		"wipefs",
		"parted",
		"resize2fs",
		"cryptsetup",
		"mkfs",
		"mount ",
		"umount",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf(
				"install stage attempted disk/filesystem operation %q:\n%s",
				forbidden,
				joined,
			)
		}
	}
}

func TestInstallRequiresRootAndBootMounts(t *testing.T) {
	repo := testRepo(t)

	tests := []struct {
		name   string
		remove string
	}{
		{name: "root", remove: "/mnt"},
		{name: "boot", remove: "/mnt/boot"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := mountedRunner()
			delete(
				r.outputs,
				commandKey(
					"findmnt",
					"-nro",
					"TARGET",
					"--mountpoint",
					tt.remove,
				),
			)
			r.errors[commandKey(
				"findmnt",
				"-nro",
				"TARGET",
				"--mountpoint",
				tt.remove,
			)] = fmt.Errorf("not mounted")

			var out bytes.Buffer
			_, err := install(
				context.Background(),
				Input{
					Repo:     repo,
					Hostname: "gjallarOS",
					Out:      &out,
				},
				r,
			)

			if err == nil || !strings.Contains(err.Error(), "not mounted") {
				t.Fatalf("unmounted target accepted: %v", err)
			}
			if strings.Contains(callsText(r.calls), "nixos-install") {
				t.Fatalf(
					"nixos-install ran before mount validation:\n%s",
					callsText(r.calls),
				)
			}
		})
	}
}

func TestInstallerConfigRemainsUnchanged(t *testing.T) {
	createInstalledProfile(t)

	repo := testRepo(t)

	settingsBefore, err := os.ReadFile(
		filepath.Join(repo, "generated", "state.nix"),
	)
	if err != nil {
		t.Fatal(err)
	}
	userBefore, err := os.ReadFile(
		filepath.Join(repo, "user.config.json"),
	)
	if err != nil {
		t.Fatal(err)
	}

	r := mountedRunner()
	var out bytes.Buffer

	if _, err := install(
		context.Background(),
		Input{
			Repo:     repo,
			Hostname: "gjallarOS",
			Out:      &out,
		},
		r,
	); err != nil {
		t.Fatal(err)
	}

	settingsAfter, err := os.ReadFile(
		filepath.Join(repo, "generated", "state.nix"),
	)
	if err != nil {
		t.Fatal(err)
	}
	userAfter, err := os.ReadFile(
		filepath.Join(repo, "user.config.json"),
	)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(settingsBefore, settingsAfter) {
		t.Fatal("generated/state.nix changed during installation")
	}
	if !bytes.Equal(userBefore, userAfter) {
		t.Fatal("user.config.json changed during installation")
	}
}

func TestConfigMutationDuringInstallFailsClosed(t *testing.T) {
	createInstalledProfile(t)

	repo := testRepo(t)
	r := mountedRunner()

	r.onRun = func() {
		if err := os.MkdirAll(filepath.Join(repo, "generated"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(repo, "generated", "state.nix"),
			[]byte("MUTATED\n"),
			0644,
		); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	_, err := install(
		context.Background(),
		Input{
			Repo:     repo,
			Hostname: "gjallarOS",
			Out:      &out,
		},
		r,
	)

	if err == nil ||
		!strings.Contains(err.Error(), "installer config changed") {
		t.Fatalf("config mutation was not detected: %v", err)
	}
}

func TestInstallHasNoJODSEnrollmentOrPhoneHome(t *testing.T) {
	createInstalledProfile(t)

	repo := testRepo(t)
	r := mountedRunner()
	var out bytes.Buffer

	if _, err := install(
		context.Background(),
		Input{
			Repo:     repo,
			Hostname: "gjallarOS",
			Out:      &out,
		},
		r,
	); err != nil {
		t.Fatal(err)
	}

	joined := strings.ToLower(callsText(r.calls))

	for _, forbidden := range []string{
		"mdm-agent",
		"jods-endpoint",
		"enroll.service",
		"enroll.timer",
		"installation-complete",
		"curl",
		"wget",
		"systemctl",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf(
				"install stage performed forbidden management action %q:\n%s",
				forbidden,
				joined,
			)
		}
	}
}

func TestFailureDoesNotReportOSInstalled(t *testing.T) {
	repo := testRepo(t)
	r := mountedRunner()

	r.errors[commandKey(
		"sudo",
		"nixos-install",
		"--root",
		"/mnt",
		"--flake",
		"path:"+repo+"#gjallarOS",
		"--no-root-passwd",
	)] = fmt.Errorf("install failed")

	var out bytes.Buffer
	_, err := install(
		context.Background(),
		Input{
			Repo:     repo,
			Hostname: "gjallarOS",
			Out:      &out,
		},
		r,
	)

	if err == nil {
		t.Fatal("failed nixos-install reported success")
	}
	if strings.Contains(out.String(), "STAGE: os-installed") {
		t.Fatalf(
			"failed installation reported os-installed:\n%s",
			out.String(),
		)
	}
}

func callsText(calls [][]string) string {
	var lines []string
	for _, call := range calls {
		lines = append(lines, strings.Join(call, " "))
	}
	return strings.Join(lines, "\n")
}

func TestInstallEvaluatesStagedSourceAndCleansUp(t *testing.T) {
	createInstalledProfile(t)
	repo := testRepo(t)
	staged := t.TempDir()
	cleaned := false
	previous := stageFlake
	stageFlake = func(got string) (string, func(), error) {
		if got != repo {
			t.Fatalf("staged %q, want %q", got, repo)
		}
		return staged, func() { cleaned = true }, nil
	}
	t.Cleanup(func() { stageFlake = previous })

	r := mountedRunner()
	var out bytes.Buffer
	if _, err := install(context.Background(), Input{Repo: repo, Hostname: "gjallarOS", Out: &out}, r); err != nil {
		t.Fatal(err)
	}
	joined := callsText(r.calls)
	if !strings.Contains(joined, "--flake path:"+staged+"#gjallarOS") {
		t.Fatalf("nixos-install did not use the staged source:\n%s", joined)
	}
	if strings.Contains(joined, "path:"+repo+"#") {
		t.Fatalf("nixos-install evaluated the checkout directly (copies .git):\n%s", joined)
	}
	if !cleaned {
		t.Fatal("staged source not removed")
	}
}

func callIndex(calls [][]string, prefix string) int {
	for i, call := range calls {
		if strings.HasPrefix(strings.Join(call, " "), prefix) {
			return i
		}
	}
	return -1
}

func TestInstallUsesTemporaryEncryptedTargetSwap(t *testing.T) {
	createInstalledProfile(t)
	r := mountedRunner()
	var out bytes.Buffer
	if _, err := install(context.Background(), Input{Repo: testRepo(t), Hostname: "gjallarOS", Out: &out}, r); err != nil {
		t.Fatal(err)
	}
	mk := callIndex(r.calls, "sudo btrfs filesystem mkswapfile --size 8g /mnt/.gjallar-install.swap")
	on := callIndex(r.calls, "sudo swapon /mnt/.gjallar-install.swap")
	install := callIndex(r.calls, "sudo nixos-install")
	off := callIndex(r.calls, "sudo swapoff /mnt/.gjallar-install.swap")
	if mk < 0 || on < mk || install < on || off < install {
		t.Fatalf("swap must exist before nixos-install and be removed after:\n%s", callsText(r.calls))
	}
	last := strings.Join(r.calls[len(r.calls)-1], " ")
	if last != "sudo rm -f -- /mnt/.gjallar-install.swap" {
		t.Fatalf("swapfile left on target, last call %q", last)
	}
}

func TestInstallRemovesSwapWhenNixosInstallFails(t *testing.T) {
	repo := testRepo(t)
	r := mountedRunner()
	r.errors[commandKey("sudo", "nixos-install", "--root", "/mnt", "--flake", "path:"+repo+"#gjallarOS", "--no-root-passwd")] = fmt.Errorf("killed")
	var out bytes.Buffer
	if _, err := install(context.Background(), Input{Repo: repo, Hostname: "gjallarOS", Out: &out}, r); err == nil {
		t.Fatal("nixos-install failure ignored")
	}
	if callIndex(r.calls, "sudo swapoff /mnt/.gjallar-install.swap") < 0 {
		t.Fatalf("swap not disabled after failure:\n%s", callsText(r.calls))
	}
}

func TestInstallKeepsExistingSwap(t *testing.T) {
	createInstalledProfile(t)
	r := mountedRunner()
	r.outputs[commandKey("swapon", "--show=NAME", "--noheadings")] = []byte("/dev/zram0\n")
	var out bytes.Buffer
	if _, err := install(context.Background(), Input{Repo: testRepo(t), Hostname: "gjallarOS", Out: &out}, r); err != nil {
		t.Fatal(err)
	}
	if callIndex(r.calls, "sudo btrfs filesystem mkswapfile") >= 0 {
		t.Fatal("created installation swap although swap is active")
	}
}

// Live media: the profile links name /nix/store paths that exist only in the
// target's store, never in the host's.
func TestVerifySystemProfileResolvesInsideTarget(t *testing.T) {
	root := t.TempDir()
	profiles := filepath.Join(root, "nix", "var", "nix", "profiles")
	store := filepath.Join(root, "nix", "store", "abc-nixos-system-test")
	for _, dir := range []string{profiles, store} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, name string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(profiles, name)); err != nil {
			t.Fatal(err)
		}
	}
	link("/nix/store/abc-nixos-system-test", "system-1-link")
	link("system-1-link", "system")

	if err := verifySystemProfile(root); err != nil {
		t.Fatalf("installed profile rejected: %v", err)
	}

	if err := os.Remove(store); err != nil {
		t.Fatal(err)
	}
	if err := verifySystemProfile(root); err == nil {
		t.Fatal("profile pointing at a missing target store path accepted")
	}
}

func TestVerifySystemProfileRejectsMissingOrPlainProfile(t *testing.T) {
	root := t.TempDir()
	if err := verifySystemProfile(root); err == nil {
		t.Fatal("missing profile accepted")
	}
	profiles := filepath.Join(root, "nix", "var", "nix", "profiles")
	if err := os.MkdirAll(filepath.Join(profiles, "system"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifySystemProfile(root); err == nil {
		t.Fatal("plain directory accepted as system profile")
	}
}

// An interrupted installer (Ctrl-C, dropped SSH) cancels the context; the
// swapfile must still be switched off and removed.
func TestInstallSwapCleanupRunsAfterCancel(t *testing.T) {
	r := mountedRunner()
	ctx, cancel := context.WithCancel(context.Background())
	release, err := enableInstallSwap(ctx, r, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	r.calls = nil
	release()
	got := callsText(r.calls)
	for _, want := range []string{"sudo swapoff " + installSwapPath, "sudo rm -f -- " + installSwapPath} {
		if !strings.Contains(got, want) {
			t.Fatalf("cleanup after cancel missing %q:\n%s", want, got)
		}
	}
}
