package rootfile

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestArgsReadStdinAndReplaceTheDestination(t *testing.T) {
	args := Args("0600", "/var/lib/example/record.json")
	i := slices.Index(args, "--")
	if i < 0 || !slices.Equal(args[i+1:], []string{"/dev/stdin", "/var/lib/example/record.json"}) || !slices.Contains(args[:i], "-T") {
		t.Fatalf("args %q", args)
	}
}

// installAsUser runs the root step's install as this user, without the
// owner flags, on data from stdin.
func installAsUser(t *testing.T, data, dst string) error {
	t.Helper()
	full := Args("0644", dst)
	var args []string
	for i := 0; i < len(full); i++ {
		if full[i] == "-o" || full[i] == "-g" {
			i++
			continue
		}
		args = append(args, full[i])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = strings.NewReader(data)
	if output, err := cmd.CombinedOutput(); err != nil {
		return errors.New(err.Error() + ": " + string(output))
	}
	return nil
}

// A link or pipe planted at the destination is replaced, never written
// through or blocked on.
func TestPlantedLinkAndFIFOAreReplaced(t *testing.T) {
	if _, err := exec.LookPath("install"); err != nil {
		t.Skip("install not found")
	}
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "record")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if err := installAsUser(t, "new\n", link); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(victim); string(data) != "secret\n" {
		t.Errorf("written through the link: %q", data)
	}
	info, err := os.Lstat(link)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
		t.Fatalf("destination %v, %v", info, err)
	}
	if data, _ := os.ReadFile(link); string(data) != "new\n" {
		t.Errorf("destination holds %q", data)
	}

	fifo := filepath.Join(dir, "fifo")
	if err := exec.Command("mkfifo", fifo).Run(); err != nil {
		t.Skip("mkfifo:", err)
	}
	if err := installAsUser(t, "new\n", fifo); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(fifo); err != nil || !info.Mode().IsRegular() {
		t.Errorf("fifo not replaced: %v, %v", info, err)
	}
}

// A boot unit runs as root without sudo on PATH.
func TestRootRunsInstallWithoutSudo(t *testing.T) {
	prev := geteuid
	t.Cleanup(func() { geteuid = prev })
	for euid, want := range map[int]string{0: "install", 1000: "sudo"} {
		geteuid = func() int { return euid }
		cmd := command(context.Background(), "0600", "/var/lib/example")
		if filepath.Base(cmd.Path) != want && cmd.Args[0] != want {
			t.Errorf("euid %d runs %q", euid, cmd.Args)
		}
	}
}
