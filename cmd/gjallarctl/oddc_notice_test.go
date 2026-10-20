package main

import (
	"strings"
	"testing"
)

func TestODDCNoticeOnlyOffPin(t *testing.T) {
	pinned := strings.Repeat("a", 40)
	commit := strings.Repeat("b", 40)
	for _, built := range []builtSource{
		{},
		{ODDC: pinned, ODDCPinned: pinned},
		{ODDC: commit},
		{ODDCPinned: pinned},
	} {
		if _, _, ok := oddcNoticeText(built); ok {
			t.Errorf("notice for %+v", built)
		}
	}
	summary, body, ok := oddcNoticeText(builtSource{ODDC: commit, ODDCPinned: pinned})
	if !ok || summary == "" {
		t.Fatal("no notice while ODDC is off its pin")
	}
	for _, want := range []string{commit[:12], pinned[:12], "not a release", "plain rebuild"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q lacks %q", body, want)
		}
	}
}
