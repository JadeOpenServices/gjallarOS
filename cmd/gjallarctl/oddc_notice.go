package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// buildSourcePath is written by system/security/build-source.nix from the
// facts the rebuild recorded (flakesource.BuildSource).
var buildSourcePath = "/etc/gjallar/build-source.json"

type builtSource struct {
	Revision   string `json:"revision"`
	ODDC       string `json:"oddc"`
	ODDCPinned string `json:"oddcPinned"`
}

// runODDCNotice keeps a notice up while the system runs an ODDC commit
// given with --override-input oddc instead of the one flake.lock pins. It
// runs at login and after every switch, so it comes back until a plain
// rebuild returns to the pin; OK only closes it for now.
func runODDCNotice(stderr io.Writer) int {
	data, err := os.ReadFile(buildSourcePath)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	var built builtSource
	if err := json.Unmarshal(data, &built); err != nil {
		fmt.Fprintf(stderr, "ERROR: %s: %v\n", buildSourcePath, err)
		return 1
	}
	summary, body, ok := oddcNoticeText(built)
	if !ok {
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if _, err := showNotice(ctx, desktopNotice{
		App:     "GjallarOS",
		Icon:    "dialog-warning",
		Summary: summary,
		Body:    body,
		Actions: []noticeAction{{"ok", "OK"}},
	}); err != nil && ctx.Err() == nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	return 0
}

// oddcNoticeText words the notice, or reports false when ODDC is the pinned
// commit (or the build did not record both).
func oddcNoticeText(built builtSource) (summary, body string, ok bool) {
	if len(built.ODDC) < 12 || len(built.ODDCPinned) < 12 || built.ODDC == built.ODDCPinned {
		return "", "", false
	}
	return "ODDC is not the pinned release",
		fmt.Sprintf("ODDC commit %s is in use. It is not a release.\n"+
			"GjallarOS pins %s. A plain rebuild goes back to it.",
			html.EscapeString(built.ODDC[:12]), html.EscapeString(built.ODDCPinned[:12])),
		true
}
