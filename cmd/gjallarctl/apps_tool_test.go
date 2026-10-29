package main

import (
	"io"
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
