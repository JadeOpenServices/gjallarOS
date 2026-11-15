package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestInitKeepsMenuBuilderOff reads the base setup script: it runs outside
// the sandbox, so Wine's menu builder must stay off there too (L51), and
// nothing later in the script may set the overrides again.
func TestInitKeepsMenuBuilderOff(t *testing.T) {
	data, err := os.ReadFile("../../apps/wine/home.nix")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(data), `writeShellScriptBin "gjallar-wine-init"`)
	if start < 0 {
		t.Fatal("gjallar-wine-init not found in apps/wine/home.nix")
	}
	script := string(data[start:])
	sets := regexp.MustCompile(`(?m)^\s*(export\s+|unset\s+)?WINEDLLOVERRIDES\b.*$`).FindAllString(script, -1)
	if len(sets) != 1 || strings.TrimSpace(sets[0]) != `export WINEDLLOVERRIDES="winemenubuilder.exe=d"` {
		t.Errorf("gjallar-wine-init sets WINEDLLOVERRIDES as %q, want one export with winemenubuilder.exe=d", sets)
	}
}
