package movehelper

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const livePasswd = "root:x:0:0:System administrator:/root:/bin/sh\n" +
	"nixos:x:60600:100::/home/nixos:/bin/sh\n" +
	"nobody:x:65534:65534:Unprivileged account:/var/empty:/bin/false\n"

func passwdFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Q254: the helper runs only where no account has an installed user's id.
func TestCheckLiveUsers(t *testing.T) {
	asRoot(t)
	if err := CheckLiveUsers(passwdFile(t, livePasswd)); err != nil {
		t.Fatalf("live accounts refused: %v", err)
	}
	for name, extra := range map[string]string{
		"lowest":    "x:x:1000:100::/home/x:/bin/sh\n",
		"highest":   "x:x:59999:100::/home/x:/bin/sh\n",
		"bad id":    "x:x:+1000:100::/home/x:/bin/sh\n",
		"short":     "x:x:1000\n",
		"nis entry": "+::::::\n",
	} {
		if err := CheckLiveUsers(passwdFile(t, livePasswd+extra)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// The account list is read only from root's own plain file.
func TestCheckLiveUsersPlanted(t *testing.T) {
	asRoot(t)
	real := passwdFile(t, livePasswd)
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	writable := passwdFile(t, livePasswd)
	if err := os.Chmod(writable, 0o666); err != nil {
		t.Fatal(err)
	}
	large := passwdFile(t, livePasswd+strings.Repeat("y:x:70000:100::/:/bin/sh\n", maxPasswd/24))
	for name, p := range map[string]string{"link": link, "fifo": fifo, "writable": writable, "large": large, "missing": filepath.Join(dir, "none")} {
		if err := CheckLiveUsers(p); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	rootUID = uint32(os.Getuid()) + 1
	if err := CheckLiveUsers(real); err == nil {
		t.Error("someone else's file accepted")
	}
}

// realNixbld is the Nix build accounts of the machine running the test, as
// NixOS writes them. The test skips where there are none.
func realNixbld(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		t.Skipf("no /etc/passwd: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "nixbld") {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		t.Skip("this machine has no Nix build accounts")
	}
	return lines
}

// L152: the live stick's Nix build accounts pass, exactly as NixOS makes
// them; any change to one is refused.
func TestCheckLiveUsersNixbld(t *testing.T) {
	asRoot(t)
	lines := realNixbld(t)
	if err := CheckLiveUsers(passwdFile(t, livePasswd+strings.Join(lines, "\n")+"\n")); err != nil {
		t.Fatalf("real Nix build accounts refused: %v", err)
	}
	f := strings.Split(lines[0], ":")
	n := strings.TrimPrefix(f[0], "nixbld")
	with := func(i int, v string) string {
		g := slices.Clone(f)
		g[i] = v
		return strings.Join(g, ":") + "\n"
	}
	for name, line := range map[string]string{
		"other name":   with(0, "nixbldx"),
		"other uid":    with(2, "30040"),
		"uid of other": with(0, "nixbld"+n+"0"),
		"leading zero": with(0, "nixbld0"+n),
		"number 33":    "nixbld33:x:30033:" + strings.Join(f[3:], ":") + "\n",
		"number 0":     "nixbld0:x:30000:" + strings.Join(f[3:], ":") + "\n",
		"other group":  with(3, "100"),
		"login shell":  with(6, "/bin/sh"),
		"home":         with(5, "/home/"+f[0]),
		"password":     with(1, ""),
		"prefix only":  with(0, "nixbld"),
		"lookalike":    with(0, "nixbId"+n),
	} {
		if err := CheckLiveUsers(passwdFile(t, livePasswd+line)); err == nil {
			t.Errorf("%s accepted: %q", name, line)
		}
	}
}

// L152: the helper never copies to an id a Nix build account has.
func TestNormalIDSkipsNixbld(t *testing.T) {
	for id, want := range map[int]bool{
		999: false, 1000: true, 29999: true, 30000: false, 30001: false,
		30032: false, 30033: true, 59999: true, 60000: false, 60600: false,
	} {
		if normalID(id) != want {
			t.Errorf("normalID(%d) = %v", id, !want)
		}
	}
}
