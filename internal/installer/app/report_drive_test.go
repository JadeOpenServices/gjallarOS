package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installreport"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/targetdisk"
)

// testDrive is a SYNTHETIC USB stick.
var testDrive = targetdisk.SaveDrive{
	Path: "/dev/sdz1", FSType: "vfat", UUID: "ABCD-1234",
	Label: "/dev/sdz1 (Example Stick, 2.0 GB)",
}

type driveRig struct {
	calls  [][]string
	mounts string
	// mktemp, when set, is what the root mktemp step reports.
	mktemp    string
	umountErr error
	checked   []string
}

// newDriveRig stands in for the drive list, its check and the root steps.
// Nothing runs as root; mktemp makes a folder below a test folder.
func newDriveRig(t *testing.T) *driveRig {
	t.Helper()
	rig := &driveRig{mounts: t.TempDir()}
	t.Setenv("HOME", t.TempDir())
	set := func(restore func()) { t.Cleanup(restore) }
	prevList, prevCheck, prevCmd, prevPrefix := listSaveDrives, checkSaveDrive, privilegedCommand, saveMountPrefix
	set(func() {
		listSaveDrives, checkSaveDrive, privilegedCommand, saveMountPrefix = prevList, prevCheck, prevCmd, prevPrefix
	})
	saveMountPrefix = filepath.Join(rig.mounts, "gjallar-save.")
	listSaveDrives = func(_ context.Context, target string) ([]targetdisk.SaveDrive, error) {
		return []targetdisk.SaveDrive{testDrive}, nil
	}
	checkSaveDrive = func(_ context.Context, target string, drive targetdisk.SaveDrive) error {
		rig.checked = append(rig.checked, target+" "+drive.Path)
		return nil
	}
	privilegedCommand = func(_ context.Context, args ...string) ([]byte, error) {
		rig.calls = append(rig.calls, args)
		switch args[0] {
		case "mktemp":
			if rig.mktemp != "" {
				return []byte(rig.mktemp + "\n"), nil
			}
			dir, err := os.MkdirTemp(rig.mounts, "gjallar-save.")
			return []byte(dir + "\n"), err
		case "umount":
			return nil, rig.umountErr
		}
		return nil, nil
	}
	return rig
}

func (r *driveRig) called(name string) [][]string {
	var out [][]string
	for _, call := range r.calls {
		if call[0] == name {
			out = append(out, call)
		}
	}
	return out
}

// testRunLog is a run log holding a SYNTHETIC personal value.
func testRunLog(t *testing.T, user *config.User) *logKeeper {
	t.Helper()
	log, err := installlog.Open(filepath.Join(t.TempDir(), "gjallar-install"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	keeper := &logKeeper{log: log, user: user}
	keeper.refreshKnown()
	fmt.Fprintln(log.Tee(io.Discard), "building for ada@example.invalid")
	return keeper
}

func driveInput(t *testing.T) reportInput {
	input := syntheticReportInput()
	input.toDrive = true
	input.targetDisk = "/dev/disk/by-id/example-target"
	input.log = testRunLog(t, &input.user)
	return input
}

func TestDriveSaveMountsWritesAndUnmounts(t *testing.T) {
	rig := newDriveRig(t)
	ui, out := reportUI("y\n1\n")
	offerInstallReport(context.Background(), ui, out, driveInput(t))

	mounts := rig.called("mount")
	if len(mounts) != 1 {
		t.Fatalf("mount calls = %v\n%s", rig.calls, out)
	}
	dir := mounts[0][len(mounts[0])-1]
	want := []string{"mount", "-t", "vfat", "-o",
		fmt.Sprintf("rw,nosuid,nodev,noexec,uid=%d,gid=%d,fmask=0177,dmask=0077", os.Getuid(), os.Getgid()),
		"--", "/dev/sdz1", dir}
	if strings.Join(mounts[0], " ") != strings.Join(want, " ") {
		t.Errorf("mount = %q, want %q", mounts[0], want)
	}
	for _, step := range []string{"umount", "rmdir"} {
		calls := rig.called(step)
		if len(calls) != 1 || strings.Join(calls[0], " ") != step+" -- "+dir {
			t.Errorf("%s calls = %v", step, calls)
		}
	}
	if len(rig.checked) != 1 || rig.checked[0] != "/dev/disk/by-id/example-target /dev/sdz1" {
		t.Errorf("drive checks = %v", rig.checked)
	}

	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("files on the drive = %v, %v\n%s", entries, err, out)
	}
	shown := out.String()
	begin := strings.Index(shown, "----- install report: begin -----\n")
	end := strings.Index(shown, "----- install report: end -----")
	var sawReport, sawLog bool
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || info.Mode() != 0o600 {
			t.Errorf("%s mode %v, %v", entry.Name(), info.Mode(), err)
		}
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), "ada@example.invalid") {
			t.Errorf("%s holds the email address", entry.Name())
		}
		switch {
		case strings.HasPrefix(entry.Name(), "gjallaros-install-report-"):
			sawReport = string(data) == shown[begin+len("----- install report: begin -----\n"):end]
		case strings.HasPrefix(entry.Name(), "gjallaros-install-") && strings.HasSuffix(entry.Name(), ".log"):
			sawLog = strings.Contains(string(data), "building for")
		}
	}
	if !sawReport || !sawLog {
		t.Errorf("report as shown %v, log %v: %v", sawReport, sawLog, entries)
	}
	if entries, _ := os.ReadDir(os.Getenv("HOME")); len(entries) != 0 {
		t.Errorf("a live run wrote to the home folder: %v", entries)
	}
	if !strings.Contains(shown, "Where should the report and the install log be saved?") {
		t.Errorf("question missing:\n%s", shown)
	}
}

func TestDriveSaveDefaultsToNothing(t *testing.T) {
	rig := newDriveRig(t)
	ui, out := reportUI("y\n\n")
	offerInstallReport(context.Background(), ui, out, driveInput(t))
	if len(rig.calls) != 0 || !strings.Contains(out.String(), "Install report not saved.") {
		t.Fatalf("root steps without a choice: %v\n%s", rig.calls, out)
	}
}

// Root reports the folder it made; a name outside our prefix is never
// mounted on.
func TestDriveSaveRefusesOddMountFolder(t *testing.T) {
	for _, odd := range []string{"/etc", "PREFIXx/../../etc", "PREFIX", "PREFIXa\nb", "/run/user/1000/gjallar-save.x"} {
		rig := newDriveRig(t)
		rig.mktemp = strings.ReplaceAll(odd, "PREFIX", saveMountPrefix)
		ui, out := reportUI("y\n1\n\n")
		offerInstallReport(context.Background(), ui, out, driveInput(t))
		if len(rig.called("mount")) != 0 {
			t.Errorf("%q: mounted on it", odd)
		}
		if !strings.Contains(out.String(), "is not below") {
			t.Errorf("%q: no refusal shown:\n%s", odd, out)
		}
	}
}

func TestDriveSaveTellsWhenUnmountFails(t *testing.T) {
	rig := newDriveRig(t)
	rig.umountErr = errors.New("target is busy")
	ui, out := reportUI("y\n1\n")
	offerInstallReport(context.Background(), ui, out, driveInput(t))
	if !strings.Contains(out.String(), "run sudo umount "+saveMountPrefix) {
		t.Fatalf("no unmount hint:\n%s", out)
	}
	if len(rig.called("rmdir")) != 0 {
		t.Errorf("removed a folder that is still mounted")
	}
}

func TestDriveSaveRefusesChangedDrive(t *testing.T) {
	rig := newDriveRig(t)
	checkSaveDrive = func(context.Context, string, targetdisk.SaveDrive) error {
		return errors.New("/dev/sdz1 changed since it was listed; nothing written")
	}
	ui, out := reportUI("y\n1\n\n")
	offerInstallReport(context.Background(), ui, out, driveInput(t))
	if len(rig.calls) != 0 || !strings.Contains(out.String(), "changed since it was listed") {
		t.Fatalf("root steps on a changed drive: %v\n%s", rig.calls, out)
	}
}

// The desktop mounted the stick for this user: the files go there with no
// root step.
func TestDriveSaveUsesDesktopMount(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root never writes to a desktop mount")
	}
	rig := newDriveRig(t)
	mounted := testDrive
	mounted.MountPoint = t.TempDir()
	listSaveDrives = func(context.Context, string) ([]targetdisk.SaveDrive, error) {
		return []targetdisk.SaveDrive{mounted}, nil
	}
	ui, out := reportUI("y\n1\n")
	offerInstallReport(context.Background(), ui, out, driveInput(t))
	if len(rig.calls) != 0 {
		t.Fatalf("root steps for a desktop mount: %v", rig.calls)
	}
	if entries, _ := os.ReadDir(mounted.MountPoint); len(entries) != 2 {
		t.Fatalf("files = %v\n%s", entries, out)
	}
}

func TestUnknownModelSavesReportOnly(t *testing.T) {
	rig := newDriveRig(t)
	input := driveInput(t)
	input.reason = installreport.ReasonUnknown
	input.failure = nil
	ui, out := reportUI("y\n1\n")
	offerInstallReport(context.Background(), ui, out, input)
	mounts := rig.called("mount")
	if len(mounts) != 1 || !strings.Contains(out.String(), "Where should the report be saved?") {
		t.Fatalf("mounts %v:\n%s", mounts, out)
	}
	entries, _ := os.ReadDir(mounts[0][len(mounts[0])-1])
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "gjallaros-install-report-") {
		t.Fatalf("unknown model saved %v", entries)
	}
}

func TestInstalledHostSavesBothToHome(t *testing.T) {
	newDriveRig(t)
	input := driveInput(t)
	input.toDrive = false
	ui, out := reportUI("y\ny\n")
	offerInstallReport(context.Background(), ui, out, input)
	entries, err := os.ReadDir(os.Getenv("HOME"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("home files = %v, %v\n%s", entries, err, out)
	}
}
