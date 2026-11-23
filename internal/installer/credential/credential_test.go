package credential

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/termout"
)

func TestChpasswdLine(t *testing.T) {
	got, err := chpasswdLine("root", "$y$j9T$salt$hash")
	if err != nil || got != "root:$y$j9T$salt$hash\n" {
		t.Fatalf("%q %v", got, err)
	}
	for _, c := range [][2]string{
		{"../root", "$6$a$b"},
		{"root", ""},
		{"root", "plaintext"},
		{"root", "$6$a$b\nother:$6$c$d"},
		{"root", "$6$a:b"},
	} {
		if _, err := chpasswdLine(c[0], c[1]); err == nil {
			t.Fatalf("accepted %q", c)
		}
	}
}

func TestPath(t *testing.T) {
	got, err := Path("testuser-corp")
	if err != nil || got != "/var/lib/gjallarOS/passwords/testuser-corp.hash" {
		t.Fatalf("%q %v", got, err)
	}
	for _, v := range []string{"", "../root", "Bad User"} {
		if _, err := Path(v); err == nil {
			t.Fatalf("accepted %q", v)
		}
	}
}

func TestStoreCommandsKeepPasswordDirectoryRootOnly(t *testing.T) {
	got := storeDirCommand("/var/lib/gjallarOS/passwords/root.hash")
	want := []string{"install", "-d", "-m", "0700", "-o", "root", "-g", "root", "--", "/var/lib/gjallarOS/passwords"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("storeDirCommand = %q, want %q", got, want)
	}
}

func TestPasswordUsable(t *testing.T) {
	for status, want := range map[string]bool{
		"tester P 2026-10-09 0 99999 7 -1\n": true,
		"tester L 1970-01-02 -1 -1 -1 -1\n":  false,
		"tester NP 2026-10-09 0 99999 7 -1":  false,
		"":                                   false,
	} {
		if got := passwordUsable(status); got != want {
			t.Fatalf("passwordUsable(%q) = %v, want %v", status, got, want)
		}
	}
}

// An empty password is turned down before the confirm prompt, not after.
func TestReadConfirmedPasswordEmptyBeforeConfirm(t *testing.T) {
	master, slave, err := termout.OpenPTY()
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	if _, err := master.WriteString("\nexample-pass\nexample-pass\n"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	got, err := ReadConfirmedPassword(slave, &out, "alice")
	if err != nil || got != "example-pass" {
		t.Fatalf("got %q %v", got, err)
	}
	text := out.String()
	empty := strings.Index(text, "Password must not be empty.")
	confirm := strings.Index(text, "Confirm password: ")
	if empty < 0 || confirm < empty || strings.Count(text, "Confirm password: ") != 1 {
		t.Fatalf("prompt order: %q", text)
	}
}
