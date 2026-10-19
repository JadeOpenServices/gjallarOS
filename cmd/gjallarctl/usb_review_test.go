package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust"
	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/broker"
)

func TestUSBReviewUsesOnlyDomainActions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decision usbtrust.Decision
	}{
		{
			name: "ambiguous internal expectation",
			decision: usbtrust.Decision{
				Reason: string(usbtrust.CodeInternalAmbiguous),
				Role:   "camera",
			},
		},
		{
			name: "unexpected hardwired device",
			decision: usbtrust.Decision{
				ObservedDevice: usbtrust.ObservedDevice{
					Identity: usbtrust.Identity{ConnectType: "hardwired"},
				},
			},
		},
		{
			name: "unchanged accepted internal device kept blocked",
			decision: usbtrust.Decision{
				Reason:    "blocked-for-connection",
				Role:      "camera",
				TrustedID: "device:camera",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.decision.Actions = []usbtrust.Action{usbtrust.ActionKeepBlocked, usbtrust.ActionAllowOnce}
			rows, available := usbReviewChoices(tc.decision, false)
			wantRows := []string{
				"TRUE", "keep-blocked", "Keep blocked",
				"FALSE", "allow-once", "Allow until unplugged",
			}
			if !reflect.DeepEqual(rows, wantRows) {
				t.Fatalf("review invented or mislabeled an action: got %q, want %q", rows, wantRows)
			}
			wantAvailable := map[string]bool{"keep-blocked": true, "allow-once": true}
			if !reflect.DeepEqual(available, wantAvailable) {
				t.Fatalf("review accepts actions absent from domain decision: %v", available)
			}
		})
	}
}

func TestUSBReviewLabelsReplacementExplicitly(t *testing.T) {
	d := usbtrust.Decision{
		Role:      "camera",
		TrustedID: "device:accepted-camera",
		Actions:   []usbtrust.Action{usbtrust.ActionAcceptReplacement},
	}
	rows, available := usbReviewChoices(d, true)
	wantRows := []string{"FALSE", "accept-replacement", "Accept replacement for device:accepted-camera"}
	if !reflect.DeepEqual(rows, wantRows) {
		t.Fatalf("techy view must identify the record being replaced: got %q, want %q", rows, wantRows)
	}
	rows, _ = usbReviewChoices(d, false)
	if strings.Contains(strings.Join(rows, " "), "device:accepted-camera") {
		t.Fatalf("simple view shows the record ID: %q", rows)
	}
	if !reflect.DeepEqual(available, map[string]bool{"accept-replacement": true}) {
		t.Fatalf("unexpected available actions: %v", available)
	}
}

func TestUSBReviewOffersRenewForLapsedTrust(t *testing.T) {
	d := usbtrust.Decision{
		Reason:    string(usbtrust.CodeTrustedExternalLapsed),
		TrustedID: "device:synthetic-stick",
		Actions:   []usbtrust.Action{usbtrust.ActionKeepBlocked, usbtrust.ActionAllowOnce, usbtrust.ActionAcceptReplacement},
	}
	rows, _ := usbReviewChoices(d, false)
	if rows[len(rows)-1] != "Always allow again" {
		t.Fatalf("lapsed renew label: %q", rows)
	}
	if facts := usbReviewFacts(d); !strings.Contains(facts[0].text, "not connected for too long") {
		t.Fatalf("lapse not explained: %+v", facts)
	}
}

func TestUSBReviewNamesBluetoothAddressCheck(t *testing.T) {
	d := usbtrust.Decision{Reason: string(usbtrust.CodeInternalChanged), Role: "Bluetooth"}
	without := usbReviewFacts(d)
	d.Anchor = usbtrust.AnchorBluetoothAddress
	with := usbReviewFacts(d)
	if len(with) != len(without)+1 || !strings.Contains(with[1].text, "Bluetooth address differs") {
		t.Fatalf("address check not named: %+v", with)
	}
}

func TestUSBReviewIgnoresUnknownActions(t *testing.T) {
	for _, actions := range [][]usbtrust.Action{
		{"future-action"},
		{"future-action", usbtrust.ActionKeepBlocked, "another-future-action"},
	} {
		d := usbtrust.Decision{Actions: actions}
		rows, available := usbReviewChoices(d, false)
		if available["future-action"] || available["another-future-action"] {
			t.Fatalf("unknown action became requestable: %v", available)
		}
		if len(actions) == 1 {
			if len(rows) != 0 || len(available) != 0 {
				t.Fatalf("unknown action produced review choices: %q, %v", rows, available)
			}
			continue
		}
		if !reflect.DeepEqual(rows, []string{"TRUE", "keep-blocked", "Keep blocked"}) ||
			!reflect.DeepEqual(available, map[string]bool{"keep-blocked": true}) {
			t.Fatalf("unknown action altered known choices: %q, %v", rows, available)
		}
	}
}

func TestUSBReviewNoticeEscapesDeviceStrings(t *testing.T) {
	d := usbtrust.Decision{ObservedDevice: usbtrust.ObservedDevice{
		Identity: usbtrust.Identity{Name: `<b>Keyboard</b>`, VIDPID: "dead:beef"},
	}}
	body := usbReviewNotice(d, true, true).Body
	if body != "&lt;b&gt;Keyboard&lt;/b&gt; (dead:beef)" {
		t.Fatalf("device name reached notification markup: %q", body)
	}
	if body := usbReviewNotice(d, true, false).Body; body != "&lt;b&gt;Keyboard&lt;/b&gt;" {
		t.Fatalf("simple notice shows hardware IDs: %q", body)
	}
}

func TestUSBReviewOpensOnlyOnRequest(t *testing.T) {
	for output, want := range map[string]bool{
		"review\n":  true,
		"default\n": true,
		"keep\n":    false,
		"":          false, // dismissed or expired: stays blocked
	} {
		if got := usbReviewRequested(output); got != want {
			t.Fatalf("usbReviewRequested(%q) = %v, want %v", output, got, want)
		}
	}
}

func TestUSBReviewNoticeDoesNotClaimBlockInAuditMode(t *testing.T) {
	n := usbReviewNotice(usbtrust.Decision{}, false, false)
	if n.Summary == "USB device blocked" {
		t.Fatalf("audit-mode notice claims the device is blocked: %+v", n)
	}
	for _, a := range n.Actions {
		if a.Label == "Keep blocked" {
			t.Fatalf("audit-mode notice claims the device is blocked: %+v", n)
		}
	}
}

func TestUSBReviewTextWrapsLongLines(t *testing.T) {
	long := strings.Repeat("word ", 40) + "\n\nshort"
	for _, line := range strings.Split(wrapWords(long, 30), "\n") {
		if len(line) > 30 {
			t.Fatalf("line exceeds width: %q", line)
		}
	}
	if !strings.HasSuffix(wrapWords(long, 30), "\n\nshort") {
		t.Fatal("wrapping dropped paragraph breaks")
	}
}

func TestUSBReviewSummaryFlagsAttackPatternsFirst(t *testing.T) {
	d := usbtrust.Decision{ObservedDevice: usbtrust.ObservedDevice{
		Identity: usbtrust.Identity{Name: "<i>stick</i>", VIDPID: "dead:beef", Serial: "S1", Interfaces: []string{"03:01:01", "08:06:50"}},
	}, Reason: string(usbtrust.CodeUnknownExternal)}
	var err error
	if d.Risks, err = usbtrust.AssessIdentityRisk(d.Identity); err != nil {
		t.Fatal(err)
	}
	summary := usbReviewSummary(d, true, true)
	if strings.Contains(summary, "<i>stick") {
		t.Fatalf("device name reached dialog markup: %q", summary)
	}
	bad := strings.Index(summary, usbBad+`">●</span> Stores files and can type`)
	fine := strings.Index(summary, usbFine+`">●</span> Has its own serial number`)
	if bad < 0 || fine < 0 || bad > fine {
		t.Fatalf("attack pattern missing or listed after reassurance:\n%s", summary)
	}
}

func TestUSBReviewSimpleViewStaysPlain(t *testing.T) {
	d := usbtrust.Decision{ObservedDevice: usbtrust.ObservedDevice{
		Identity: usbtrust.Identity{Name: "stick", VIDPID: "dead:beef", Interfaces: []string{"03:01:01", "08:06:50"}},
	}, Reason: string(usbtrust.CodeUnknownExternal)}
	var err error
	if d.Risks, err = usbtrust.AssessIdentityRisk(d.Identity); err != nil {
		t.Fatal(err)
	}
	summary := usbReviewSummary(d, true, false)
	if !strings.Contains(summary, usbBad+`">●</span> This device may be unsafe`) {
		t.Fatalf("simple view hides the attack pattern:\n%s", summary)
	}
	if strings.Contains(summary, "dead:beef") || strings.Contains(summary, "Can type") {
		t.Fatalf("simple view is not plain:\n%s", summary)
	}
}

func TestUSBReviewAlwaysTechnicalPreference(t *testing.T) {
	repo := t.TempDir()
	for _, marker := range []string{"flake.nix", "scripts/installation/install.sh"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, marker)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, marker), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	userConfig := filepath.Join(repo, "user.config.json")
	if err := os.WriteFile(userConfig, []byte("{\n  \"hostname\": \"box\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GJALLAROS_REPO", repo)

	t.Setenv(usbReviewViewEnv, "")
	if usbReviewAlwaysTechnical() {
		t.Fatal("review must start in the simple view by default")
	}
	t.Setenv(usbReviewViewEnv, usbReviewTechnicalV)
	if !usbReviewAlwaysTechnical() {
		t.Fatal("built default was ignored while the key is missing")
	}
	if err := setUSBReviewAlwaysTechnical(false); err != nil {
		t.Fatal(err)
	}
	if usbReviewAlwaysTechnical() {
		t.Fatal("unticking the box must override the built default")
	}
	t.Setenv(usbReviewViewEnv, "")
	if err := setUSBReviewAlwaysTechnical(true); err != nil {
		t.Fatal(err)
	}
	if !usbReviewAlwaysTechnical() {
		t.Fatal("ticked box was not remembered")
	}
	data, err := os.ReadFile(userConfig)
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"usbReviewTechnicalView\": true,\n  \"hostname\": \"box\"\n}\n"; string(data) != want {
		t.Fatalf("user.config.json =\n%s\nwant\n%s", data, want)
	}
}

func TestUnpluggedConnectionIsInactive(t *testing.T) {
	policy := []usbtrust.Decision{{Connection: "a"}, {Connection: "b"}}
	if !usbConnectionActive(policy, "b") {
		t.Fatal("present connection reported inactive")
	}
	if usbConnectionActive(policy, "c") || usbConnectionActive(nil, "a") {
		t.Fatal("unplugged connection reported active")
	}
}

func TestUSBMediaNotice(t *testing.T) {
	body := usbMediaNotice(usbtrust.MediaDecision{ID: "6:0:0:0", Reader: "<b>Reader</b>", Bytes: 31_900_000_000}).Body
	if !strings.Contains(body, "&lt;b&gt;Reader&lt;/b&gt;, 31.9 GB") {
		t.Fatalf("body = %q", body)
	}
	if usbMediaActive([]usbtrust.MediaDecision{{Connection: "a", Allowed: true}}, "a") {
		t.Fatal("an opened medium keeps its notice")
	}
	if !usbMediaActive([]usbtrust.MediaDecision{{Connection: "a"}}, "a") {
		t.Fatal("closed medium not active")
	}
}

// poll runs one broker poll through r and returns the connections it would
// announce.
func poll(r *usbReviewState, policy ...usbtrust.Decision) []string {
	r.begin()
	var asked []string
	for _, d := range policy {
		if r.wants(d) {
			asked = append(asked, d.Connection)
		}
	}
	r.prune()
	return asked
}

func TestUSBReviewAfterBrokerRestart(t *testing.T) {
	device := func(connection string, target usbtrust.Target) usbtrust.Decision {
		return usbtrust.Decision{
			Connection:     connection,
			Target:         target,
			ObservedDevice: usbtrust.ObservedDevice{Identity: usbtrust.Identity{Hash: "SYNTHETIC-hash", Port: "2-2"}},
		}
	}
	r := newUSBReviewState()
	if got := poll(r, device("a", usbtrust.TargetBlock)); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("new device announced %v, want [a]", got)
	}
	r.done(device("a", usbtrust.TargetBlock), usbReviewAllowed)
	if got := poll(r, device("a", usbtrust.TargetAllow)); got != nil {
		t.Fatalf("allowed device announced %v", got)
	}
	// The restarted broker blocks it again under a new token: the allow
	// is gone, so ask again.
	if got := poll(r, device("b", usbtrust.TargetBlock)); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("device blocked again after restart announced %v, want [b]", got)
	}
	r.done(device("b", usbtrust.TargetBlock), usbReviewAnswered)
	if got := poll(r, device("c", usbtrust.TargetBlock)); got != nil {
		t.Fatalf("device kept blocked announced again after restart: %v", got)
	}
	// Unplugged and back: ask again.
	poll(r)
	if got := poll(r, device("d", usbtrust.TargetBlock)); !reflect.DeepEqual(got, []string{"d"}) {
		t.Fatalf("replugged device announced %v, want [d]", got)
	}
}

func TestUSBRenewalNotice(t *testing.T) {
	// SYNTHETIC record name and times.
	l := broker.TrustLapse{TrustedID: "device:synthetic", Name: "Synthetic <Dock>", LastUsed: "2026-09-21T10:00:00Z", LapsesAt: "2026-10-21T10:00:00Z"}
	n := usbRenewalNotice(l, time.UTC)
	if !strings.Contains(n.Body, "Synthetic &lt;Dock&gt; has not been connected since 21 Sep.") || !strings.Contains(n.Body, "From Wed 21 Oct 10:00") {
		t.Fatalf("renewal body: %q", n.Body)
	}
	r := newUSBReviewState()
	if !r.wantsRenewal(l) || r.wantsRenewal(l) {
		t.Fatal("warning must show once per lapse time")
	}
	l.LapsesAt = "2026-11-20T10:00:00Z"
	if !r.wantsRenewal(l) {
		t.Fatal("new lapse time not warned")
	}
}
