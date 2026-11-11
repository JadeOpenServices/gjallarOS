package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/systemcheckout"
)

func TestRequireSystemCheckoutRun(t *testing.T) {
	dir := t.TempDir()
	system := filepath.Join(dir, "system")
	user := filepath.Join(dir, "user")
	// SYNTHETIC /etc/gjallar/repository files.
	if err := os.WriteFile(system, []byte(systemcheckout.Path+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(user, []byte("/home/example/gjallarOS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, file, root string
		euid             int
		want             string
	}{
		{"system checkout as root", system, systemcheckout.Path, 0, ""},
		{"system checkout, trailing slash", system, systemcheckout.Path + "/", 0, ""},
		{"user checkout on a system-checkout host", system, "/home/example/gjallarOS", 0, "builds from"},
		{"system checkout as a user", system, systemcheckout.Path, 1000, "belongs to root"},
		{"standalone install as a user", user, "/home/example/gjallarOS", 1000, ""},
		{"no repository file (live media)", filepath.Join(dir, "missing"), "/home/example/gjallarOS", 1000, ""},
	}
	for _, c := range cases {
		err := requireSystemCheckoutRun(c.file, c.root, c.euid)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
}
