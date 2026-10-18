package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	installerresume "github.com/JadeOpenServices/gjallarOS/internal/installer/resume"
	"github.com/JadeOpenServices/gjallarOS/internal/installercheck"
)

// stubInstalledSystemEvidence keeps the detection tests off the live host.
func stubInstalledSystemEvidence(t *testing.T, found bool) {
	original := installedSystemEvidence
	t.Cleanup(func() { installedSystemEvidence = original })
	installedSystemEvidence = func(context.Context) bool { return found }
}

func TestVanillaPersistentRootIsFresh(t *testing.T) {
	original := inspectCurrentRoot
	t.Cleanup(func() { inspectCurrentRoot = original })
	stubInstalledSystemEvidence(t, false)

	inspectCurrentRoot = func(
		context.Context,
	) (string, string, error) {
		return "/dev/mapper/cryptroot", "ext4", nil
	}

	existing, err := detectExistingInstalledSystem(
		context.Background(),
		t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if existing {
		t.Fatal("vanilla persistent NixOS root was treated as existing GjallarOS")
	}
}

func TestLiveMediaRootIsNotExistingInstalledSystem(t *testing.T) {
	original := inspectCurrentRoot
	t.Cleanup(func() { inspectCurrentRoot = original })
	stubInstalledSystemEvidence(t, true)

	inspectCurrentRoot = func(
		context.Context,
	) (string, string, error) {
		return "overlay", "overlay", nil
	}

	existing, err := detectExistingInstalledSystem(
		context.Background(),
		t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if existing {
		t.Fatal("live-media root was treated as an installed system")
	}
}

func TestUnmanagedInstallIsExisting(t *testing.T) {
	original := inspectCurrentRoot
	t.Cleanup(func() { inspectCurrentRoot = original })
	stubInstalledSystemEvidence(t, true)

	inspectCurrentRoot = func(
		context.Context,
	) (string, string, error) {
		return "/dev/mapper/cryptroot[/@root]", "btrfs", nil
	}

	existing, err := detectExistingInstalledSystem(
		context.Background(),
		t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !existing {
		t.Fatal("installed GjallarOS without JODS was treated as fresh")
	}
}

func TestSystemRepositoryFileIsEvidence(t *testing.T) {
	original := installercheck.SystemRepositoryFile
	t.Cleanup(func() { installercheck.SystemRepositoryFile = original })
	installercheck.SystemRepositoryFile = filepath.Join(t.TempDir(), "repository")
	if err := os.WriteFile(installercheck.SystemRepositoryFile, []byte("/home/u/gjallarOS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !installedSystemEvidence(context.Background()) {
		t.Fatal("/etc/gjallar/repository not taken as an installed system")
	}
}

func TestBtrfsExistingRecoveryContinuesUnchanged(t *testing.T) {
	original := detectCurrentRootFilesystem
	t.Cleanup(func() { detectCurrentRootFilesystem = original })

	detectCurrentRootFilesystem = func(
		context.Context,
	) (string, error) {
		return "btrfs", nil
	}

	root := t.TempDir()
	preset := filepath.Join(root, "user.config.json")

	user := config.User{
		RecoveryEnable:          true,
		RecoveryPartitionEnable: true,
	}
	if err := config.WriteAtomic(preset, user); err != nil {
		t.Fatal(err)
	}

	s := state{user: user}
	var out bytes.Buffer

	continued, err := handleExistingRecoveryFilesystem(
		context.Background(),
		prompt.New(strings.NewReader(""), &out),
		preset,
		&s,
		&out,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !continued {
		t.Fatal("Btrfs recovery compatibility was rejected")
	}
	if !s.user.RecoveryEnable ||
		!s.user.RecoveryPartitionEnable {
		t.Fatal("Btrfs recovery settings were changed")
	}
}

func TestExt4CanContinueWithoutRecoveryAndPersistsChoice(t *testing.T) {
	original := detectCurrentRootFilesystem
	t.Cleanup(func() { detectCurrentRootFilesystem = original })

	detectCurrentRootFilesystem = func(
		context.Context,
	) (string, error) {
		return "ext4", nil
	}

	root := t.TempDir()
	preset := filepath.Join(root, "user.config.json")

	user := config.User{
		RecoveryEnable:          true,
		RecoveryPartitionEnable: true,
	}
	if err := config.WriteAtomic(preset, user); err != nil {
		t.Fatal(err)
	}

	s := state{
		user:                     user,
		recoveryDisk:             "/dev/nvme0n1",
		recoveryPartition:        "/dev/nvme0n1p3",
		recoverySigningKey:       "/tmp/private",
		recoverySigningPublicKey: "/tmp/public",
	}

	var out bytes.Buffer

	continued, err := handleExistingRecoveryFilesystem(
		context.Background(),
		prompt.New(strings.NewReader("y\n"), &out),
		preset,
		&s,
		&out,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !continued {
		t.Fatal("ext4 fallback was not accepted")
	}

	if s.user.RecoveryEnable ||
		s.user.RecoveryPartitionEnable {
		t.Fatal("recovery remained enabled after accepted ext4 fallback")
	}

	if s.recoveryDisk != "" ||
		s.recoveryPartition != "" ||
		s.recoverySigningKey != "" ||
		s.recoverySigningPublicKey != "" {
		t.Fatal("recovery runtime state was not cleared")
	}

	data, err := os.ReadFile(preset)
	if err != nil {
		t.Fatal(err)
	}

	var persisted config.User
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}

	if persisted.RecoveryEnable ||
		persisted.RecoveryPartitionEnable {
		t.Fatal("persisted user configuration still enables recovery")
	}

	if !strings.Contains(
		out.String(),
		"Current root filesystem: ext4",
	) {
		t.Fatal("ext4 incompatibility explanation was not shown")
	}
}

func TestExt4DeclineStopsWithoutChangingPreset(t *testing.T) {
	original := detectCurrentRootFilesystem
	t.Cleanup(func() { detectCurrentRootFilesystem = original })

	detectCurrentRootFilesystem = func(
		context.Context,
	) (string, error) {
		return "ext4", nil
	}

	root := t.TempDir()
	preset := filepath.Join(root, "user.config.json")

	user := config.User{
		RecoveryEnable:          true,
		RecoveryPartitionEnable: true,
	}
	if err := config.WriteAtomic(preset, user); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(preset)
	if err != nil {
		t.Fatal(err)
	}

	s := state{user: user}
	var out bytes.Buffer

	continued, err := handleExistingRecoveryFilesystem(
		context.Background(),
		prompt.New(strings.NewReader("n\n"), &out),
		preset,
		&s,
		&out,
	)
	if err != nil {
		t.Fatal(err)
	}
	if continued {
		t.Fatal("declined ext4 fallback continued installation")
	}

	after, err := os.ReadFile(preset)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(before, after) {
		t.Fatal("declined fallback modified user.config.json")
	}
}

func TestUnattendedExt4FailsClosed(t *testing.T) {
	original := detectCurrentRootFilesystem
	t.Cleanup(func() { detectCurrentRootFilesystem = original })

	detectCurrentRootFilesystem = func(
		context.Context,
	) (string, error) {
		return "ext4", nil
	}

	s := state{
		user: config.User{
			RecoveryEnable:          true,
			RecoveryPartitionEnable: true,
			UnattendedInstall:       true,
		},
	}

	var out bytes.Buffer

	_, err := handleExistingRecoveryFilesystem(
		context.Background(),
		prompt.New(strings.NewReader(""), &out),
		filepath.Join(t.TempDir(), "user.config.json"),
		&s,
		&out,
	)
	if err == nil {
		t.Fatal("unattended ext4 recovery incompatibility was accepted")
	}
	if !strings.Contains(err.Error(), "recovery requires Btrfs") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRecoveryInstalledRootRequiresAuthenticatedMapper(t *testing.T) {
	original := inspectRecoveryRoot
	t.Cleanup(func() { inspectRecoveryRoot = original })

	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "etc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "etc", "NIXOS"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	inspectRecoveryRoot = func(context.Context, string) ([]byte, error) {
		return []byte("/dev/nvme0n1p2 rw,relatime\n"), nil
	}

	if _, err := detectRecoveryInstalledRoot(context.Background(), target); err == nil ||
		!strings.Contains(err.Error(), "authenticated LUKS mapping") {
		t.Fatalf("error=%v", err)
	}
}

func TestRecoveryInstalledRootAcceptsAuthenticatedMapper(t *testing.T) {
	original := inspectRecoveryRoot
	t.Cleanup(func() { inspectRecoveryRoot = original })

	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "etc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "etc", "NIXOS"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	inspectRecoveryRoot = func(context.Context, string) ([]byte, error) {
		return []byte("/dev/mapper/gjallar-recovery-root rw,relatime\n"), nil
	}

	existing, err := detectRecoveryInstalledRoot(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if !existing {
		t.Fatal("authenticated mounted recovery root was not accepted")
	}
}

func TestRecoveryAfterMaintenanceNeedsNoAnswers(t *testing.T) {
	original := findRecoveryPartition
	t.Cleanup(func() { findRecoveryPartition = original })
	findRecoveryPartition = func(context.Context) string { return "/dev/vda3" }

	for _, tc := range []struct {
		name, state, input, want string
	}{
		{"maintenance resume", installerresume.StateMaintenanceReboot, "", "/dev/vda3"},
		{"rerun, default no", "", "\n", ""},
		{"rerun, yes", "", "y\n", "/dev/vda3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GJALLAR_INSTALLER_RESUME_STATE", tc.state)
			s := state{
				existing: true,
				user: config.User{
					RecoveryEnable:          true,
					RecoveryPartitionEnable: true,
					SecureBootEnable:        true,
				},
			}
			var out bytes.Buffer
			err := configureRecoveryProvisioning(
				context.Background(),
				prompt.New(strings.NewReader(tc.input), &out),
				options{},
				&s,
			)
			if err != nil {
				t.Fatal(err)
			}
			if s.recoveryPartition != tc.want {
				t.Fatalf("recovery partition %q; want %q", s.recoveryPartition, tc.want)
			}
			if strings.Contains(out.String(), "signing key") {
				t.Fatalf("asked for release signing keys:\n%s", out.String())
			}
		})
	}
}
