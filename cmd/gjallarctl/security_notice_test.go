package main

import (
	"html"
	"os"
	"strings"
	"testing"
)

// zbookFindings reads testdata/oddc-security-zbook-x2-g4.json: the real
// `oddc security --json` output of ODDC release 2 (a24366ae) for
// model/hp/zbook-x2-g4, with details, fwupdHsi and path removed.
func zbookFindings(t *testing.T) []securityFinding {
	t.Helper()
	data, err := os.ReadFile("testdata/oddc-security-zbook-x2-g4.json")
	if err != nil {
		t.Fatal(err)
	}
	findings, err := parseSecurityFindings(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 4 {
		t.Fatalf("got %d findings, want 4", len(findings))
	}
	for _, f := range findings {
		if f.ID == "" || f.Name == "" || f.Summary == "" || f.Modified == "" {
			t.Fatalf("finding misses a field gjallarctl uses: %+v", f)
		}
	}
	return findings
}

func TestParseSecurityFindingsEmpty(t *testing.T) {
	// What release 2 prints for a model without findings (fw13).
	findings, err := parseSecurityFindings([]byte(`{"findings": [], "flags": {}}`))
	if err != nil || len(findings) != 0 {
		t.Fatalf("got %v, %v", findings, err)
	}
	if fresh := unseenFindings(findings, map[string]string{}); len(fresh) != 0 {
		t.Fatalf("no findings must stay quiet, got %v", fresh)
	}
}

func TestSecurityNoticeSeenOnce(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	findings := zbookFindings(t)

	seen, err := readSeenFindings()
	if err != nil {
		t.Fatal(err)
	}
	if fresh := unseenFindings(findings, seen); len(fresh) != 4 {
		t.Fatalf("first login: got %d unseen, want 4", len(fresh))
	}
	if err := writeSeenFindings(findings); err != nil {
		t.Fatal(err)
	}
	seen, err = readSeenFindings()
	if err != nil {
		t.Fatal(err)
	}
	if fresh := unseenFindings(findings, seen); len(fresh) != 0 {
		t.Fatalf("next login: got %v, want none", fresh)
	}

	// ODDC updates one finding: only that one comes back.
	changed := append([]securityFinding(nil), findings...)
	changed[2].Modified = "2026-10-13"
	fresh := unseenFindings(changed, seen)
	if len(fresh) != 1 || fresh[0].ID != findings[2].ID {
		t.Fatalf("after an update: got %v, want only %s", fresh, findings[2].ID)
	}

	// A withdrawn finding is forgotten, so it shows again if it returns.
	if err := writeSeenFindings(findings[1:]); err != nil {
		t.Fatal(err)
	}
	seen, err = readSeenFindings()
	if err != nil {
		t.Fatal(err)
	}
	fresh = unseenFindings(findings, seen)
	if len(fresh) != 1 || fresh[0].ID != findings[0].ID {
		t.Fatalf("after it returns: got %v, want only %s", fresh, findings[0].ID)
	}
}

func TestSecurityNoticeBrokenSeenFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := os.MkdirAll(strings.TrimSuffix(seenFindingsPath(), "/security-findings-seen.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seenFindingsPath(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	seen, err := readSeenFindings()
	if err != nil || len(seen) != 0 {
		t.Fatalf("broken file: got %v, %v; want empty", seen, err)
	}
}

func TestSecurityNoticeTextOne(t *testing.T) {
	f := zbookFindings(t)[3]
	summary, body := securityNoticeText([]securityFinding{f})
	if summary != "Security note for this device" {
		t.Fatalf("summary %q", summary)
	}
	// ODDC's summary is shown whole: it says what to be careful with.
	if html.UnescapeString(body) != f.Summary {
		t.Fatalf("body %q, want the ODDC summary %q", body, f.Summary)
	}
}

func TestSecurityNoticeTextSeveral(t *testing.T) {
	findings := zbookFindings(t)
	summary, body := securityNoticeText(unseenFindings(findings, map[string]string{}))
	if summary != "4 security notes for this device" {
		t.Fatalf("summary %q", summary)
	}
	for _, f := range findings {
		if !strings.Contains(body, "• "+f.Name+"\n") {
			t.Fatalf("body misses %q:\n%s", f.Name, body)
		}
	}
	if !strings.HasSuffix(body, "Details says what to be careful with.") {
		t.Fatalf("body must point at Details:\n%s", body)
	}
	// ODDC's summaries ask the user to act (AMT, Thunderbolt), so the notice
	// must not claim otherwise.
	if strings.Contains(strings.ToLower(body), "nothing for you to do") {
		t.Fatalf("body contradicts ODDC:\n%s", body)
	}
}

func TestSecurityNoticeTextEscapes(t *testing.T) {
	// SYNTHETIC: markup in an ODDC string must reach the card as text.
	f := securityFinding{ID: "x", Name: "a <b>", Summary: "c & <i>d</i>"}
	_, body := securityNoticeText([]securityFinding{f})
	if body != "c &amp; &lt;i&gt;d&lt;/i&gt;" {
		t.Fatalf("one: %q", body)
	}
	_, body = securityNoticeText([]securityFinding{f, f})
	if !strings.Contains(body, "• a &lt;b&gt;\n") {
		t.Fatalf("several: %q", body)
	}
}

func TestUnseenFindingsSeverityOrder(t *testing.T) {
	// SYNTHETIC severities: release 2 only has "medium" findings.
	in := []securityFinding{
		{ID: "a", Severity: "low"},
		{ID: "b", Severity: "unrated"},
		{ID: "c", Severity: "critical"},
		{ID: ""},
		{ID: "d", Severity: "medium"},
	}
	var got []string
	for _, f := range unseenFindings(in, map[string]string{}) {
		got = append(got, f.ID)
	}
	if strings.Join(got, ",") != "c,d,a,b" {
		t.Fatalf("got %v, want c,d,a,b", got)
	}
}
