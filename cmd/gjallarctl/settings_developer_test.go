package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/devgate"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
)

// useFakeProof answers the proof prompt with kind and the EXAMPLE secret,
// and counts the prompts. The secret given out is checked to be cleared.
func useFakeProof(t *testing.T, kind, secret string, err error) (*int, *[][]byte) {
	t.Helper()
	old := settingsReadProof
	t.Cleanup(func() { settingsReadProof = old })
	asked := 0
	var given [][]byte
	settingsReadProof = func(io.Writer) (string, []byte, error) {
		asked++
		if err != nil {
			return "", nil, err
		}
		b := []byte(secret)
		given = append(given, b)
		return kind, b, nil
	}
	return &asked, &given
}

func developerKeys(bus *fakeSettingsBus) {
	bus.values["releaseChannel"] = settings.KeyValue{Key: "releaseChannel", Tier: config.TierCritical, Value: json.RawMessage(`"stable"`)}
	bus.values["developerMode"] = settings.KeyValue{Key: "developerMode", Tier: config.TierCritical, Value: json.RawMessage(`false`)}
}

func TestNeedsDeveloperProof(t *testing.T) {
	for _, tc := range []struct {
		values string
		want   bool
	}{
		{`{"releaseChannel":"dev"}`, true},
		{`{"releaseChannel":"staging"}`, true},
		{`{"releaseChannel":"stable"}`, false},
		{`{"releaseChannel":""}`, false},
		{`{"developerMode":true}`, true},
		{`{"developerMode":false}`, false},
		{`{"releaseChannel":"stable","developerMode":false}`, false},
		{`{"timezone":"UTC"}`, false},
		{`{"developerMode":"yes"}`, false},
	} {
		var values map[string]json.RawMessage
		if err := json.Unmarshal([]byte(tc.values), &values); err != nil {
			t.Fatal(err)
		}
		if got := needsDeveloperProof(values); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.values, got, tc.want)
		}
	}
}

func TestSettingsSetDevChannelSendsProof(t *testing.T) {
	bus := &fakeSettingsBus{}
	useFakeSettings(t, bus)
	developerKeys(bus)
	asked, given := useFakeProof(t, devgate.ProofPIN, "2468", nil)
	var stdout, stderr bytes.Buffer
	run([]string{"settings", "set", "releaseChannel=dev"}, &stdout, &stderr)
	if *asked != 1 || !slices.Equal(bus.proofs, []string{"RequestWithProof pin 2468"}) {
		t.Fatalf("asked %d, proofs %q, stderr %s", *asked, bus.proofs, stderr.String())
	}
	if len(bus.requests) != 1 || bus.requests[0] != `{"releaseChannel":"dev"}` {
		t.Fatalf("requests %q", bus.requests)
	}
	if !bytes.Equal((*given)[0], make([]byte, 4)) {
		t.Fatal("the PIN was not cleared after the call")
	}
}

func TestSettingsSetBackToStableAsksNothing(t *testing.T) {
	bus := &fakeSettingsBus{}
	useFakeSettings(t, bus)
	developerKeys(bus)
	asked, _ := useFakeProof(t, devgate.ProofPIN, "2468", nil)
	var stdout, stderr bytes.Buffer
	run([]string{"settings", "set", "releaseChannel=stable", "developerMode=false"}, &stdout, &stderr)
	if *asked != 0 || len(bus.proofs) != 0 || len(bus.requests) != 1 {
		t.Fatalf("asked %d, proofs %q, requests %q", *asked, bus.proofs, bus.requests)
	}
}

func TestSettingsSetProofNotTypedSendsNothing(t *testing.T) {
	bus := &fakeSettingsBus{}
	useFakeSettings(t, bus)
	developerKeys(bus)
	useFakeProof(t, "", "", errors.New("no terminal"))
	var stdout, stderr bytes.Buffer
	if code := run([]string{"settings", "set", "developerMode=true"}, &stdout, &stderr); code != 1 {
		t.Fatalf("code %d", code)
	}
	if len(bus.requests) != 0 || !strings.Contains(stderr.String(), "no terminal") {
		t.Fatalf("requests %q, stderr %s", bus.requests, stderr.String())
	}
}

func TestSettingsConfirmDeveloper(t *testing.T) {
	bus := &fakeSettingsBus{}
	useFakeSettings(t, bus)
	_, given := useFakeProof(t, devgate.ProofRecovery, "example-recovery", nil)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"settings", "confirm-developer"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	if !slices.Equal(bus.proofs, []string{"ConfirmDeveloper recovery example-recovery"}) || !strings.Contains(stdout.String(), "Confirmed") {
		t.Fatalf("proofs %q, stdout %s", bus.proofs, stdout.String())
	}
	if !bytes.Equal((*given)[0], make([]byte, len("example-recovery"))) {
		t.Fatal("the recovery key was not cleared")
	}
	if code := run([]string{"settings", "confirm-developer", "extra"}, &stdout, &stderr); code != 2 {
		t.Fatalf("extra argument: code %d", code)
	}
}

// unencrypted makes the EMULATED system disk unencrypted and answers the
// stable offer with answer.
func unencrypted(bus *fakeSettingsBus, channel string, answer string) {
	developerKeys(bus)
	bus.values["releaseChannel"] = settings.KeyValue{Key: "releaseChannel", Tier: config.TierCritical, Value: json.RawMessage(`"` + channel + `"`)}
	settingsDiskEncrypted = func(context.Context) (bool, error) { return false, nil }
	settingsStdin = strings.NewReader(answer)
}

func TestSettingsUnencryptedRefusesWithoutProof(t *testing.T) {
	bus := &fakeSettingsBus{}
	useFakeSettings(t, bus)
	unencrypted(bus, "stable", "")
	asked, _ := useFakeProof(t, devgate.ProofPIN, "2468", nil)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"settings", "set", "releaseChannel=staging"}, &stdout, &stderr); code != 1 {
		t.Fatalf("code %d", code)
	}
	if *asked != 0 || len(bus.requests) != 0 || !strings.Contains(stderr.String(), devgate.Unencrypted) || !strings.Contains(stdout.String(), "Nothing changed") {
		t.Fatalf("asked %d, requests %q, stdout %s, stderr %s", *asked, bus.requests, stdout.String(), stderr.String())
	}
}

func TestSettingsUnencryptedOffersStable(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		answer string
		want   []string
	}{
		{[]string{"settings", "confirm-developer"}, "y\n", []string{`{"developerMode":false,"releaseChannel":"stable"}`}},
		{[]string{"settings", "set", "developerMode=true", "timezone=UTC"}, "y\n", []string{`{"developerMode":false,"releaseChannel":"stable","timezone":"UTC"}`}},
		{[]string{"settings", "confirm-developer"}, "n\n", nil},
	} {
		bus := &fakeSettingsBus{}
		useFakeSettings(t, bus)
		unencrypted(bus, "staging", tc.answer)
		asked, _ := useFakeProof(t, devgate.ProofPIN, "2468", nil)
		var stdout, stderr bytes.Buffer
		run(tc.args, &stdout, &stderr)
		if *asked != 0 || len(bus.proofs) != 0 || !slices.Equal(bus.requests, tc.want) {
			t.Fatalf("%v %q: asked %d, proofs %q, requests %q, stderr %s", tc.args, tc.answer, *asked, bus.proofs, bus.requests, stderr.String())
		}
	}
}

func TestNormalRecoveryKey(t *testing.T) {
	// EXAMPLE key in the systemd alphabet.
	want := strings.TrimSuffix(strings.Repeat("cbdefghi-", 8), "-")
	for _, in := range []string{
		want,
		strings.ToUpper(want),
		strings.ReplaceAll(want, "-", ""),
		strings.ReplaceAll(want, "-", " "),
		" " + want + "\n",
	} {
		got, err := normalRecoveryKey(in)
		if err != nil || got != want {
			t.Errorf("%q: %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "2468", strings.Repeat("a", 64), want + "-cbdefghi"} {
		if _, err := normalRecoveryKey(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}
