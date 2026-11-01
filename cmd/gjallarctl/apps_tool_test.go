package main

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAppsWindowRunsTheAppsCommand(t *testing.T) {
	var got []string
	old := runInTerminal
	runInTerminal = func(title string, command []string, stderr io.Writer) int {
		got = command
		return 0
	}
	defer func() { runInTerminal = old }()

	for _, action := range []string{"add", "remove"} {
		if code := runApps([]string{action, "teams", "--window"}, io.Discard, io.Discard); code != 0 {
			t.Fatalf("%s --window: exit %d", action, code)
		}
		want := []string{"apps", action, "teams"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s --window runs %q, want %q", action, got, want)
		}
	}
}

func TestBuiltApps(t *testing.T) {
	old := appsBuiltPath
	t.Cleanup(func() { appsBuiltPath = old })
	appsBuiltPath = filepath.Join(t.TempDir(), "apps.json")
	if _, known := builtApps(); known {
		t.Fatal("a system without the record must not claim to know its apps")
	}
	if err := os.WriteFile(appsBuiltPath, []byte(`["git","vlc"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if built, known := builtApps(); !known || len(built) != 2 || built[1] != "vlc" {
		t.Fatalf("builtApps = %v, %v", built, known)
	}
}

func TestAppWaits(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		installed, built *bool
		rebuilding       bool
		want             string
	}{
		{&yes, &yes, false, ""},
		{nil, &yes, false, ""},
		{&yes, nil, false, ""},
		{&yes, &no, false, " (installed with the next rebuild)"},
		{&yes, &no, true, " (installing now)"},
		{&no, &yes, false, " (removed with the next rebuild)"},
		{&no, &yes, true, " (removing now)"},
	}
	for _, c := range cases {
		if got := appWaits(catalogueEntry{Installed: c.installed, Built: c.built}, c.rebuilding); got != c.want {
			t.Errorf("appWaits(%v, %v, %v) = %q, want %q", c.installed, c.built, c.rebuilding, got, c.want)
		}
	}
}
