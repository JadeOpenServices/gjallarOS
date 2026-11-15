package installlog_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installreport"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
)

// TestRecoveryMaterialSkipsTheLog shows a made-up Secure Boot passphrase
// through the installer's prompt the way a run does. It must reach the
// terminal and never the log file or a copy of it.
//
// Real passphrases are long hex, which redaction hides as well. The canary
// is plain words redaction keeps, so this test fails if the prompt ever
// writes recovery material into the log instead of past it.
func TestRecoveryMaterialSkipsTheLog(t *testing.T) {
	const canary = "wobble-garden-ferry-lantern-quartz"
	if installreport.Redact(canary, installreport.Known{}) != canary {
		t.Fatal("redaction hides the canary; pick one it keeps")
	}
	log, err := installlog.Open(filepath.Join(t.TempDir(), "gjallar-install"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	var term bytes.Buffer
	ui := prompt.UI{Out: log.Tee(&term)}
	if err := ui.ShowSecureBootRecovery(context.Background(), "/example/recovery.age", canary); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(term.String(), canary) {
		t.Fatal("passphrase did not reach the terminal; the test proves nothing")
	}
	exported, err := log.Export()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log.Path())
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string][]byte{"log file": raw, "export": exported} {
		if bytes.Contains(text, []byte(canary)) {
			t.Errorf("%s holds the passphrase", name)
		}
	}
}
