package resume

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/control"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	Schema        = 1
	StateDir      = "/var/lib/gjallarOS/installer-resume"
	PendingPath   = StateDir + "/pending.json"
	ActivePath    = StateDir + "/active.json"
	FailedPath    = StateDir + "/failed.json"
	InstallerPath = StateDir + "/gjallar-installer"
	ControlPath   = StateDir + "/gjallarctl"
	ModulePath    = StateDir + "/resume-module.nix"
	WrapperPath   = StateDir + "/configuration.nix"
	GCRootPath    = StateDir + "/gcroot"
	ServiceName   = "installer-resume.service"

	StateReleaseReboot     = "waiting-for-release-reboot"
	StateMaintenanceReboot = "waiting-for-maintenance-reboot"

	// MaxAttempts bounds how often a resume that never finished runs again,
	// so a transaction that crashes the boot cannot loop forever.
	MaxAttempts = 3
)

type Transaction struct {
	Schema          int
	State           string
	Reason          string
	ExpectedRelease string
	Repo            string
	Args            []string
	CreatedUTC      string
	Attempts        int
	// DeviceGate is the ODDC validation target the arming run passed.
	DeviceGate string `json:",omitempty"`
}

func Load(path string) (Transaction, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Transaction{}, fmt.Errorf("read installer resume transaction: %w", err)
	}

	var tx Transaction
	if err := json.Unmarshal(data, &tx); err != nil {
		return Transaction{}, fmt.Errorf("parse installer resume transaction: %w", err)
	}

	if tx.Schema != Schema {
		return Transaction{}, fmt.Errorf(
			"unsupported installer resume transaction schema %d",
			tx.Schema,
		)
	}

	switch tx.State {
	case StateReleaseReboot, StateMaintenanceReboot:
	default:
		return Transaction{}, fmt.Errorf(
			"installer resume transaction is not resumable: %s",
			tx.State,
		)
	}

	if tx.State == StateReleaseReboot && strings.TrimSpace(tx.ExpectedRelease) == "" {
		return Transaction{}, fmt.Errorf(
			"installer resume transaction has no expected release",
		)
	}

	if !filepath.IsAbs(tx.Repo) {
		return Transaction{}, fmt.Errorf(
			"installer resume repository is not absolute: %q",
			tx.Repo,
		)
	}

	return tx, nil
}

// Claim takes the transaction in dir for this boot. A freshly armed
// pending.json becomes active.json; an active.json left by a boot that died
// mid-resume is run again until MaxAttempts.
func Claim(dir string) (Transaction, error) {
	pending := filepath.Join(dir, "pending.json")
	active := filepath.Join(dir, "active.json")

	if err := os.Rename(pending, active); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Transaction{}, fmt.Errorf("claim installer resume transaction: %w", err)
	}

	tx, err := Load(active)
	if err != nil {
		return Transaction{}, errors.Join(err, retire(dir))
	}

	tx.Attempts++
	if tx.Attempts > MaxAttempts {
		return Transaction{}, errors.Join(
			fmt.Errorf(
				"installer resume was interrupted %d times; automatic retry disabled",
				MaxAttempts,
			),
			retire(dir),
		)
	}

	data, err := encode(tx)
	if err != nil {
		return Transaction{}, err
	}

	if err := writeDurable(active, data); err != nil {
		return Transaction{}, fmt.Errorf("record installer resume attempt: %w", err)
	}

	return tx, nil
}

// Finish ends a claimed run. An interrupted run keeps active.json for the
// next boot; a failed run is kept as failed.json and not retried.
func Finish(dir string, succeeded, interrupted bool) error {
	if interrupted {
		return nil
	}

	if succeeded {
		err := os.Remove(filepath.Join(dir, "active.json"))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove finished installer resume transaction: %w", err)
		}
		return releaseRoot(dir)
	}

	return errors.Join(retire(dir), releaseRoot(dir))
}

// releaseRoot drops the garbage collector root arm took, unless a run handed
// over to a new continuation that still needs it.
func releaseRoot(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "pending.json")); err == nil {
		return nil
	}
	err := os.Remove(filepath.Join(dir, "gcroot"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove installer resume store root: %w", err)
	}
	return nil
}

// storeRoot returns the /nix/store entry path lives in, or "" outside it.
func storeRoot(path string) string {
	rest, ok := strings.CutPrefix(filepath.Clean(path), "/nix/store/")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, "/")
	if name == "" {
		return ""
	}
	return "/nix/store/" + name
}

func retire(dir string) error {
	err := os.Rename(filepath.Join(dir, "active.json"), filepath.Join(dir, "failed.json"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("retire failed installer resume transaction: %w", err)
	}
	return nil
}

func encode(tx Transaction) ([]byte, error) {
	data, err := json.MarshalIndent(tx, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode installer resume transaction: %w", err)
	}
	return append(data, '\n'), nil
}

// writeDurable replaces path so that a power loss leaves either the old or
// the new transaction, never a truncated one.
func writeDurable(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".transaction-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())

	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return err
	}

	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

// ArmMaintenance arms a continuation for after the one-shot recovery-storage
// maintenance boot. The installed GjallarOS configuration already carries
// the installer-resume unit.
func ArmMaintenance(
	ctx context.Context,
	executable string,
	repo string,
	args []string,
	deviceGate string,
) error {
	return arm(ctx, executable, Transaction{
		Schema:     Schema,
		State:      StateMaintenanceReboot,
		Reason:     "recovery-storage-maintenance",
		Repo:       repo,
		Args:       append([]string(nil), args...),
		DeviceGate: deviceGate,
	})
}

// Arm arms a continuation for after the staged NixOS release boot. The
// staged generation is built from WrapperPath, which adds the resume unit
// to a configuration that is not GjallarOS yet.
func Arm(
	ctx context.Context,
	executable string,
	repo string,
	args []string,
	expectedRelease string,
) error {
	if strings.TrimSpace(expectedRelease) == "" {
		return fmt.Errorf("expected release must not be empty")
	}

	if err := arm(ctx, executable, Transaction{
		Schema:          Schema,
		State:           StateReleaseReboot,
		Reason:          "nixos-release-alignment",
		ExpectedRelease: expectedRelease,
		Repo:            repo,
		Args:            append([]string(nil), args...),
	}); err != nil {
		return err
	}

	if _, err := os.Stat("/etc/nixos/configuration.nix"); err != nil {
		return fmt.Errorf(
			"current NixOS configuration is unavailable at /etc/nixos/configuration.nix: %w",
			err,
		)
	}

	wrapper :=
		"{ ... }:\n" +
			"{\n" +
			"  imports = [\n" +
			"    /etc/nixos/configuration.nix\n" +
			"    " + ModulePath + "\n" +
			"  ];\n" +
			"}\n"

	if err := sudoWrite(ctx, []byte(Module()), "0644", ModulePath); err != nil {
		return fmt.Errorf("install resume NixOS module: %w", err)
	}

	if err := sudoWrite(ctx, []byte(wrapper), "0644", WrapperPath); err != nil {
		return fmt.Errorf("install resume NixOS wrapper: %w", err)
	}

	return nil
}

// Module is the unit the release-alignment wrapper adds. It matches
// system/maintenance/installer-resume.nix and steps aside when that module
// is already part of the configuration.
func Module() string {
	return "{ lib, options, pkgs, ... }:\n" +
		"{\n" +
		"  config = lib.mkIf (!(options ? gjallar && options.gjallar ? installerResume)) {\n" +
		"    systemd.services.installer-resume = {\n" +
		"      description = \"Resume the GjallarOS installer after a planned or interrupted reboot\";\n" +
		"      wantedBy = [ \"multi-user.target\" ];\n" +
		"      wants = [ \"network-online.target\" ];\n" +
		"      after = [ \"local-fs.target\" \"network-online.target\" ];\n" +
		"      before = [ \"display-manager.service\" \"greetd.service\" ];\n" +
		"      path = [ \"/run/wrappers\" \"/run/current-system/sw\" pkgs.sbctl pkgs.efibootmgr pkgs.gptfdisk pkgs.parted ];\n" +
		"      unitConfig = {\n" +
		"        ConditionPathExists = [ \"|" + PendingPath + "\" \"|" + ActivePath + "\" ];\n" +
		"        ConditionFileIsExecutable = \"" + InstallerPath + "\";\n" +
		"      };\n" +
		"      serviceConfig = {\n" +
		"        Type = \"oneshot\";\n" +
		"        UMask = \"0077\";\n" +
		"        TimeoutStartSec = 0;\n" +
		"        StandardInput = \"tty\";\n" +
		"        StandardOutput = \"tty\";\n" +
		"        StandardError = \"journal+console\";\n" +
		"        TTYPath = \"/dev/tty1\";\n" +
		"        TTYReset = true;\n" +
		"        TTYVHangup = true;\n" +
		"        ExecStartPre = [ \"-${pkgs.plymouth}/bin/plymouth quit\" \"-${pkgs.kbd}/bin/chvt 1\" ];\n" +
		"        ExecStart = \"" + InstallerPath + " --resume-transaction " + StateDir + "\";\n" +
		"      };\n" +
		"    };\n" +
		"  };\n" +
		"}\n"
}

func arm(ctx context.Context, executable string, tx Transaction) error {
	if !filepath.IsAbs(executable) {
		return fmt.Errorf("installer executable must be absolute")
	}

	if !filepath.IsAbs(tx.Repo) {
		return fmt.Errorf("installer repository must be absolute")
	}

	tx.CreatedUTC = time.Now().UTC().Format(time.RFC3339)

	data, err := encode(tx)
	if err != nil {
		return err
	}

	if err := sudo(ctx, "install", "-d", "-m", "0700", StateDir); err != nil {
		return err
	}

	if err := sudo(
		ctx,
		"install",
		"-m", "0755",
		executable,
		InstallerPath,
	); err != nil {
		return fmt.Errorf("install resumable installer binary: %w", err)
	}

	ctl := control.Path
	if ctl == "" {
		ctl = filepath.Join(filepath.Dir(executable), "gjallarctl")
	}
	if info, statErr := os.Stat(ctl); statErr == nil && !info.IsDir() {
		if err := sudo(
			ctx,
			"install",
			"-m", "0755",
			ctl,
			ControlPath,
		); err != nil {
			return fmt.Errorf("install resumable gjallarctl: %w", err)
		}
	}

	// The copied installer still names gjallarctl and the ODDC catalog by
	// store path, and the weekly garbage collection can run right at boot.
	// A root on the store entry the installer started from keeps its whole
	// closure until Finish. A resumed copy is not in the store; the root its
	// first run took stays in place.
	if store := storeRoot(executable); store != "" {
		cmd := exec.CommandContext(ctx, "sudo", "nix-store", "--add-root", GCRootPath, "--realise", store)
		cmd.Stdin = os.Stdin
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("keep installer store paths for the resume: %w", err)
		}
	}

	if err := sudoWrite(ctx, data, "0600", PendingPath); err != nil {
		return fmt.Errorf("install resume transaction: %w", err)
	}

	// A resumed run that arms the next continuation hands over to it;
	// otherwise the reboot it schedules would also retry the old one.
	if err := sudo(ctx, "rm", "-f", ActivePath); err != nil {
		return fmt.Errorf("hand over installer resume transaction: %w", err)
	}

	if err := sudo(ctx, "sync"); err != nil {
		return fmt.Errorf("flush installer resume transaction: %w", err)
	}

	return nil
}

func sudoWrite(ctx context.Context, data []byte, mode, path string) error {
	tempDir, err := os.MkdirTemp("", "gjallar-installer-resume-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	temp := filepath.Join(tempDir, filepath.Base(path))
	if err := os.WriteFile(temp, data, 0600); err != nil {
		return err
	}

	return sudo(ctx, "install", "-m", mode, temp, path)
}

func sudo(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "sudo", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf(
			"sudo %s: %w",
			strings.Join(args, " "),
			err,
		)
	}

	return nil
}
