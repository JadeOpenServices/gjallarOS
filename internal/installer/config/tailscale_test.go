package config

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestValidateTailscale(t *testing.T) {
	good := TailscaleIntent{
		HomeSubnets:       []string{"192.168.8.0/24", "fd00::/64"},
		TrustedWifis:      []string{"synthetic-5Ghz", "synthetic-guest"},
		ExitNode:          "home-router",
		WifiExitNodes:     map[string]string{"synthetic-guest": "auto"},
		SiteRouterTrust:   true,
		SiteRouterTargets: []string{"192.168.8.1:53", "[fd00::1]:22", "nas.lan:445"},
	}
	if err := ValidateTailscale(&good); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTailscale(nil); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*TailscaleIntent){
		"default route":                        func(t *TailscaleIntent) { t.HomeSubnets = []string{"0.0.0.0/0"} },
		"bare address":                         func(t *TailscaleIntent) { t.HomeSubnets = []string{"192.168.8.1"} },
		"long ssid":                            func(t *TailscaleIntent) { t.TrustedWifis = []string{strings.Repeat("x", 33)} },
		"exit node space":                      func(t *TailscaleIntent) { t.ExitNode = "home router" },
		"wifi exit node for an untrusted wifi": func(t *TailscaleIntent) { t.WifiExitNodes = map[string]string{"cafe": "auto"} },
		"empty wifi exit node":                 func(t *TailscaleIntent) { t.WifiExitNodes = map[string]string{"synthetic-guest": ""} },
		"target no port":                       func(t *TailscaleIntent) { t.SiteRouterTargets = []string{"192.168.8.1"} },
		"target port 0":                        func(t *TailscaleIntent) { t.SiteRouterTargets = []string{"192.168.8.1:0"} },
		"no targets":                           func(t *TailscaleIntent) { t.SiteRouterTargets = nil },
	} {
		bad := good
		mutate(&bad)
		if err := ValidateTailscale(&bad); err == nil {
			t.Errorf("%s: accepted %#v", name, bad)
		}
	}
}

func TestTailscaleIntentAbsentStaysNil(t *testing.T) {
	var user User
	if err := json.Unmarshal([]byte(`{"hostname":"x"}`), &user); err != nil || user.Tailscale != nil {
		t.Fatalf("tailscale = %#v, err %v", user.Tailscale, err)
	}
	data, _ := json.Marshal(User{})
	if strings.Contains(string(data), "tailscale") {
		t.Fatalf("nil intent serialized: %s", data)
	}
}

func TestDefaultSiteRouterTarget(t *testing.T) {
	if got := DefaultSiteRouterTarget([]string{"fd00::/64", "10.20.0.7/24"}); got != "10.20.0.1:53" {
		t.Fatalf("got %q", got)
	}
}

func TestSplitListQuotedNames(t *testing.T) {
	for raw, want := range map[string][]string{
		"":         {},
		"a, b ,,c": {"a", "b", "c"},
		`synthetic-5Ghz, "cafe, upstairs", 'Mum's'`: nil,
		`"cafe, upstairs", 'say "hi"' , " pad "`:    {"cafe, upstairs", `say "hi"`, " pad "},
		`it's-wifi`:                                 {"it's-wifi"},
	} {
		got, err := SplitList(raw)
		if want == nil {
			if err == nil {
				t.Errorf("SplitList(%q) = %q, want error", raw, got)
			}
			continue
		}
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("SplitList(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if _, err := SplitList(`"open`); err == nil {
		t.Fatal("unterminated quote accepted")
	}
}
