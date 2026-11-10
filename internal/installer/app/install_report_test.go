package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installreport"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
)

func TestFailRecordsErrorForReport(t *testing.T) {
	recorder := &failureRecorder{Writer: &bytes.Buffer{}}
	if fail(recorder, context.Canceled); recorder.err != nil {
		t.Fatalf("a stop was recorded as failure: %v", recorder.err)
	}
	failure := errors.New("disk is gone")
	if code := fail(recorder, failure); code != 1 || recorder.err != failure {
		t.Fatalf("fail = %d, recorded %v", code, recorder.err)
	}
}

func TestReportNeedsAPersonAtATerminal(t *testing.T) {
	t.Setenv("GJALLAR_INSTALLER_RESUMED", "")
	if reportAllowed(strings.NewReader("y\n"), config.User{}) {
		t.Fatal("a piped answer stream may be offered a report")
	}
	pipeRead, pipeWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipeRead.Close()
	defer pipeWrite.Close()
	if reportAllowed(pipeRead, config.User{}) {
		t.Fatal("a pipe counts as a terminal")
	}

	tty, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer tty.Close()
	// /dev/null is a character device, as a terminal is.
	if !reportAllowed(tty, config.User{}) {
		t.Fatal("character device not accepted")
	}
	if reportAllowed(tty, config.User{UnattendedInstall: true}) {
		t.Fatal("unattended install offered a report")
	}
	if reportAllowed(tty, config.User{Management: config.ManagementIntent{Enable: true}}) {
		t.Fatal("managed install offered a report")
	}
	t.Setenv("GJALLAR_INSTALLER_RESUMED", "1")
	if reportAllowed(tty, config.User{}) {
		t.Fatal("resumed run offered a report")
	}
}

func reportUI(answers string) (prompt.UI, *bytes.Buffer) {
	var out bytes.Buffer
	return prompt.UI{Reader: bufio.NewReader(strings.NewReader(answers)), Out: &out}, &out
}

// SYNTHETIC user values.
func syntheticReportInput() reportInput {
	return reportInput{
		reason: installreport.ReasonFailed,
		user: config.User{
			Username: "ada", Name: "Ada Example", Hostname: "example-host",
			Email: "ada@example.invalid",
		},
		failure: errors.New("nixos-rebuild failed for ada@example.invalid on example-host"),
	}
}

func TestInstallReportDefaultsToNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ui, out := reportUI("\n")
	offerInstallReport(context.Background(), ui, out, syntheticReportInput())
	if strings.Contains(out.String(), "install report: begin") {
		t.Fatal("report shown without a yes")
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("files written without a yes: %v", entries)
	}

	ui, out = reportUI("y\n\n")
	offerInstallReport(context.Background(), ui, out, syntheticReportInput())
	if !strings.Contains(out.String(), "Install report not saved.") {
		t.Fatal("no answer to save did not keep the report unsaved")
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("files written without a second yes: %v", entries)
	}
}

func TestInstallReportSavesWhatWasShown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ui, out := reportUI("y\ny\n")
	offerInstallReport(context.Background(), ui, out, syntheticReportInput())

	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 1 {
		t.Fatalf("saved files = %v, %v", entries, err)
	}
	saved, err := os.ReadFile(filepath.Join(home, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	shown := out.String()
	begin := strings.Index(shown, "----- install report: begin -----\n")
	end := strings.Index(shown, "----- install report: end -----")
	if begin < 0 || end < begin {
		t.Fatal("report markers missing")
	}
	if shown[begin+len("----- install report: begin -----\n"):end] != string(saved) {
		t.Fatal("saved bytes differ from the shown report")
	}
	for _, personal := range []string{"ada@example.invalid", "example-host", " ada "} {
		if strings.Contains(string(saved), personal) {
			t.Errorf("report holds %q", personal)
		}
	}
}
