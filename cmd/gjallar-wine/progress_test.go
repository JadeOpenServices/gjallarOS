package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/wineprogress"
)

// progressPipe gives the fd number a window would pass and a reader of
// the lines. The command closes its end.
func progressPipe(t *testing.T) (string, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Dup(int(w.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	t.Cleanup(func() { r.Close() })
	return strconv.Itoa(fd), r
}

type stepState struct {
	step  wineprogress.Step
	state wineprogress.State
}

func readProgress(t *testing.T, r *os.File) ([]stepState, []wineprogress.Event) {
	t.Helper()
	var steps []stepState
	var events []wineprogress.Event
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		var e wineprogress.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("%q: %v", sc.Text(), err)
		}
		steps = append(steps, stepState{e.Step, e.State})
		events = append(events, e)
	}
	return steps, events
}

func sameSteps(t *testing.T, name string, got, want []stepState) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: lines %v, want %v", name, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s: line %d = %v, want %v", name, i, got[i], want[i])
		}
	}
}

func TestProgressAdd(t *testing.T) {
	f := newFakeSetup(t)
	installer := filepath.Join(t.TempDir(), "setup.exe")
	writeFile(t, installer, []byte("SYNTHETIC installer"))
	f.installs(t, "tool", "viewer")

	fd, r := progressPipe(t)
	if err := addApp([]string{"--progress-fd", fd, "Synthetic", installer}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	steps, events := readProgress(t, r)
	sameSteps(t, "first add", steps, []stepState{
		{wineprogress.StepBase, wineprogress.Running},
		{wineprogress.StepBase, wineprogress.Done},
		{wineprogress.StepCopy, wineprogress.Running},
		{wineprogress.StepCopy, wineprogress.Done},
		{wineprogress.StepInstaller, wineprogress.Running},
		{wineprogress.StepInstaller, wineprogress.Done},
		{wineprogress.StepChoose, wineprogress.Ask},
	})
	if events[0].App != "Synthetic" {
		t.Errorf("app = %q", events[0].App)
	}
	if c := events[len(events)-1].Choices; len(c) != 2 || c[1].N != 2 || c[1].Path != `C:\Program Files\Synthetic\viewer.exe` {
		t.Errorf("choices %+v", c)
	}

	// The base exists now: the window sees only the copy.
	fd, r = progressPipe(t)
	if err := addApp([]string{"--progress-fd=" + fd, "Second"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	steps, _ = readProgress(t, r)
	sameSteps(t, "second add", steps, []stepState{
		{wineprogress.StepCopy, wineprogress.Running},
		{wineprogress.StepCopy, wineprogress.Done},
	})

	fd, r = progressPipe(t)
	if err := addApp([]string{"--progress-fd", fd, "Second"}, io.Discard, io.Discard); err == nil {
		t.Fatal("same app added twice")
	}
	steps, events = readProgress(t, r)
	if len(steps) != 1 || events[0].State != wineprogress.Failed || events[0].Code != wineprogress.CodeAppExists {
		t.Errorf("app exists: %+v", events)
	}

	// An installer into an app that starts a program already asks with
	// that program marked.
	fd, r = progressPipe(t)
	if err := installCmd([]string{"--progress-fd", fd, "synthetic", installer}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	steps, _ = readProgress(t, r)
	sameSteps(t, "install", steps, []stepState{
		{wineprogress.StepInstaller, wineprogress.Running},
		{wineprogress.StepInstaller, wineprogress.Done},
		{wineprogress.StepChoose, wineprogress.Ask},
	})
}

func TestProgressSetupFails(t *testing.T) {
	f := newFakeSetup(t)
	if err := os.WriteFile(initPath, []byte("#!/bin/sh\necho run >> \""+f.count+"\"\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	fd, r := progressPipe(t)
	if err := addApp([]string{"--progress-fd", fd, "Synthetic"}, io.Discard, io.Discard); err == nil {
		t.Fatal("failed setup accepted")
	}
	_, events := readProgress(t, r)
	if len(events) != 2 || events[1].Step != wineprogress.StepBase || events[1].Code != wineprogress.CodeBaseIncomplete {
		t.Errorf("lines %+v", events)
	}
}

func TestProgressArgs(t *testing.T) {
	w, rest, err := progressArgs([]string{"Synthetic"})
	if w != nil || err != nil || len(rest) != 1 {
		t.Errorf("no flag: %v %v %v", w, rest, err)
	}
	for _, bad := range [][]string{{"--progress-fd"}, {"--progress-fd=x", "a"}, {"--progress-fd", "2", "a"}, {"--progress-fd=-1", "a"}} {
		if _, _, err := progressArgs(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// The flag goes first; later it is an app name and add refuses it.
	newFakeSetup(t)
	if err := addApp([]string{"Synthetic", "--progress-fd"}, io.Discard, io.Discard); err == nil {
		t.Error("flag taken as the installer")
	}
}
