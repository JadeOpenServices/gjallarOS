package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
)

// testKeeper opens a real log in a temp folder and records the commands the
// keeper runs instead of running them.
func testKeeper(t *testing.T) (*logKeeper, *[]string, *[]bool) {
	t.Helper()
	log, err := installlog.Open(filepath.Join(t.TempDir(), "gjallar-install"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	installlog.SetCurrent(log)
	t.Cleanup(func() {
		installlog.SetCurrent(nil)
		log.Close()
	})

	var copies []string
	previousWrite := writeRootFile
	writeRootFile = func(_ context.Context, data []byte, dst string) error {
		copies = append(copies, dst)
		if strings.Contains(string(data), "exampleuser") {
			t.Errorf("copy holds the user name:\n%s", data)
		}
		return nil
	}
	var reboots []bool
	previousReboot := reboot
	reboot = func(_ context.Context, firmwareSetup bool) error {
		reboots = append(reboots, firmwareSetup)
		return nil
	}
	t.Cleanup(func() {
		writeRootFile = previousWrite
		reboot = previousReboot
	})
	user := config.User{Username: "exampleuser"}
	return &logKeeper{log: log, target: "/", user: &user}, &copies, &reboots
}

func TestLogKeeperCopiesRootOnlyAndRedacted(t *testing.T) {
	k, copies, _ := testKeeper(t)
	k.log.Tee(&bytes.Buffer{}).Write([]byte("Creating user exampleuser\n"))
	var errOut bytes.Buffer
	k.keep(context.Background(), &errOut)
	if errOut.Len() != 0 {
		t.Fatalf("keep warned: %s", errOut.String())
	}
	if len(*copies) != 1 {
		t.Fatalf("%d copies, want 1", len(*copies))
	}
	if got := (*copies)[0]; got != "/var/log/gjallar-install.log" {
		t.Errorf("copied to %q", got)
	}
}

// realWriteRootFile is set before TestMain stubs the writer.
var realWriteRootFile = writeRootFile

// TestWriteRootFileUsesAPipe runs the real writer against a stand-in sudo:
// root must get the data on stdin, never a path in the user's folders.
func TestWriteRootFileUsesAPipe(t *testing.T) {
	bin := t.TempDir()
	got := filepath.Join(t.TempDir(), "got")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GOT.args\"\ncat > \"$GOT\"\n"
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOT", got)
	if err := realWriteRootFile(context.Background(), []byte("line one\n"), "/var/log/gjallar-install.log"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(got)
	args, _ := os.ReadFile(got + ".args")
	want := "install\n-D\n-m\n0600\n-o\nroot\n-g\nroot\n-T\n--\n/dev/stdin\n/var/log/gjallar-install.log\n"
	if string(data) != "line one\n" || string(args) != want {
		t.Errorf("sudo got args %q and stdin %q", args, data)
	}
}

func TestNoLinkedFolders(t *testing.T) {
	root := t.TempDir()
	if err := noLinkedFolders(root, "/var/log"); err != nil {
		t.Errorf("missing folders refused: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "var"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(root, "var", "log")); err != nil {
		t.Fatal(err)
	}
	if err := noLinkedFolders(root, "/var/log"); err == nil {
		t.Error("linked var/log accepted")
	}
}

func TestLogKeeperSkipsCopyAfterRestart(t *testing.T) {
	k, copies, reboots := testKeeper(t)
	var errOut bytes.Buffer
	if err := k.restart(context.Background(), &errOut, true); err != nil {
		t.Fatal(err)
	}
	k.finish(context.Background(), &errOut, 0)
	if len(*copies) != 1 {
		t.Errorf("%d copies, want only the one before the restart", len(*copies))
	}
	if len(*reboots) != 1 || !(*reboots)[0] {
		t.Errorf("reboots %v, want one into firmware setup", *reboots)
	}
}

func TestLogKeeperNeedsMountedTarget(t *testing.T) {
	k, copies, _ := testKeeper(t)
	k.target = t.TempDir()
	var errOut bytes.Buffer
	k.keep(context.Background(), &errOut)
	if len(*copies) != 0 || !strings.Contains(errOut.String(), "is not mounted") {
		t.Errorf("copied into a folder that is not a mount: copies %v, warning %q", *copies, errOut.String())
	}
}

func TestLogKeeperWithoutLogDoesNothing(t *testing.T) {
	k := &logKeeper{target: "/"}
	var errOut bytes.Buffer
	k.keep(context.Background(), &errOut)
	k.finish(context.Background(), &errOut, 1)
	if errOut.Len() != 0 {
		t.Errorf("no-log keeper wrote %q", errOut.String())
	}
}

// bootRunner answers the boot-entry reads with one installed disk.
type bootRunner struct {
	next  string
	calls []string
}

func (r *bootRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, key)
	switch {
	case name == "lsblk":
		return []byte(`{"blockdevices":[
 {"path":"/dev/vda1","type":"part","partuuid":"9f6bc3da-7244-4fce-8759-6a3033d20cea","partlabel":"EFI","parttype":"c12a7328-f81f-11d2-ba4b-00a0c93ec93b","pkname":"/dev/vda"},
 {"path":"/dev/vda2","type":"part","partuuid":"11111111-0000-4000-8000-000000000001","partlabel":"GJALLAROS","parttype":"ca7d7ccb-63ed-4c53-861c-1742536059cc","pkname":"/dev/vda"}]}`), nil
	case key == "efibootmgr -v":
		return []byte("BootOrder: 0001,0002\nBoot0001* UEFI USB\tPciRoot(0x0)/Pci(0x5,0x0)\nBoot0002* Linux Boot Manager\tHD(1,GPT,9f6bc3da-7244-4fce-8759-6a3033d20cea,0x800,0x100000)/File(\\EFI\\systemd\\systemd-bootx64.efi)\n"), nil
	case key == "efibootmgr --bootnext 0002":
		r.next = "0002"
		return nil, nil
	case key == "efibootmgr":
		return []byte("BootNext: " + r.next + "\n"), nil
	}
	return nil, os.ErrInvalid
}

func useBootRunner(t *testing.T) *bootRunner {
	r := &bootRunner{}
	prev := bootNextRunner
	bootNextRunner = r
	t.Cleanup(func() { bootNextRunner = prev })
	return r
}

func TestRestartSetsBootNextOnceForTheNewDisk(t *testing.T) {
	k, _, _ := testKeeper(t)
	r := useBootRunner(t)
	k.bootDisk = "/dev/vda"
	var errOut bytes.Buffer
	if err := k.restart(context.Background(), &errOut, false); err != nil {
		t.Fatal(err)
	}
	k.finish(context.Background(), &errOut, 0)
	if errOut.Len() != 0 {
		t.Fatalf("warned: %s", errOut.String())
	}
	if r.next != "0002" || len(r.calls) != 4 {
		t.Fatalf("next %q, calls %v", r.next, r.calls)
	}
}

func TestFinishWithoutRestartSetsBootNext(t *testing.T) {
	k, _, _ := testKeeper(t)
	r := useBootRunner(t)
	k.bootDisk = "/dev/vda"
	k.finish(context.Background(), &bytes.Buffer{}, 0)
	if r.next != "0002" {
		t.Fatalf("calls %v", r.calls)
	}
}

func TestBootNextOnlyForAFreshInstallThatWorked(t *testing.T) {
	k, _, _ := testKeeper(t)
	r := useBootRunner(t)
	if err := k.restart(context.Background(), &bytes.Buffer{}, false); err != nil {
		t.Fatal(err)
	}
	k2, _, _ := testKeeper(t)
	k2.bootDisk = "/dev/vda"
	k2.finish(context.Background(), &bytes.Buffer{}, 1)
	if len(r.calls) != 0 {
		t.Fatalf("calls %v", r.calls)
	}
}

func TestBootNextOtherDiskWarns(t *testing.T) {
	k, _, _ := testKeeper(t)
	r := useBootRunner(t)
	k.bootDisk = "/dev/vdb"
	var errOut bytes.Buffer
	if err := k.restart(context.Background(), &errOut, false); err != nil {
		t.Fatal(err)
	}
	if r.next != "" || !strings.Contains(errOut.String(), "Start installed gjallarOS") {
		t.Fatalf("next %q, warned %q", r.next, errOut.String())
	}
}

func TestBootNextFailureStillRestarts(t *testing.T) {
	k, _, reboots := testKeeper(t)
	k.bootDisk = "/dev/vda"
	var errOut bytes.Buffer
	if err := k.restart(context.Background(), &errOut, false); err != nil {
		t.Fatal(err)
	}
	if len(*reboots) != 1 || !strings.Contains(errOut.String(), "could not point the next start") {
		t.Fatalf("reboots %v, warned %q", *reboots, errOut.String())
	}
}
