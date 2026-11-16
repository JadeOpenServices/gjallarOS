package region

import (
	"errors"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/stickkeyboard"
)

func TestPreferStick(t *testing.T) {
	old := stickLayout
	t.Cleanup(func() { stickLayout = old })
	session := Session{Timezone: "Etc/UTC", Locale: "en_US.UTF-8", KeyboardLayout: "us"}

	stickLayout = func() (string, error) { return "de", nil }
	if got := preferStick(session); got.KeyboardLayout != "de" || got.Timezone != "Etc/UTC" || got.Locale != "en_US.UTF-8" {
		t.Fatalf("stick layout: got %+v", got)
	}
	stickLayout = func() (string, error) { return "", stickkeyboard.ErrNone }
	if got := preferStick(session); got != session {
		t.Fatalf("no stick file: got %+v", got)
	}
	stickLayout = func() (string, error) { return "fr", errors.New("planted link") }
	if got := preferStick(session); got != session {
		t.Fatalf("refused stick file still used: got %+v", got)
	}
}
