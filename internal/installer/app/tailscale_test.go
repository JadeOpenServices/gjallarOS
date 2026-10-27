package app

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
)

func collectTailscaleAnswers(t *testing.T, answers ...string) (*config.TailscaleIntent, string) {
	t.Helper()
	var output bytes.Buffer
	user := config.User{}
	ui := prompt.New(strings.NewReader(strings.Join(answers, "\n")+"\n"), &output)
	if err := collectTailscale(context.Background(), ui, nil, &user); err != nil {
		t.Fatal(err)
	}
	return user.Tailscale, output.String()
}

func TestCollectTailscaleHomeWifisAndSiteRouter(t *testing.T) {
	got, _ := collectTailscaleAnswers(t,
		"10.20.0.0/24",
		"y",
		"home-router",
		"synthetic-5Ghz, synthetic-2.4Ghz,synthetic-guest",
		"",  // no trusted Wi-Fi uses the exit node
		"n", // router home
		"",  // site router trust
		"",  // targets: default from the home subnet
	)
	want := config.TailscaleIntent{
		HomeSubnets:       []string{"10.20.0.0/24"},
		TrustedWifis:      []string{"synthetic-5Ghz", "synthetic-2.4Ghz", "synthetic-guest"},
		ExitNode:          "home-router",
		SiteRouterTrust:   true,
		SiteRouterTargets: []string{"10.20.0.1:53"},
	}
	if got.ExitNode != want.ExitNode || !got.SiteRouterTrust ||
		!slices.Equal(got.HomeSubnets, want.HomeSubnets) ||
		!slices.Equal(got.TrustedWifis, want.TrustedWifis) ||
		!slices.Equal(got.SiteRouterTargets, want.SiteRouterTargets) {
		t.Fatalf("tailscale = %#v", got)
	}
}

func TestCollectTailscaleReasksInvalidAnswers(t *testing.T) {
	got, out := collectTailscaleAnswers(t,
		"10.0.0.0", // not a CIDR
		"10.0.0.0/24",
		"y",
		"bad name", // not a node name
		"exit-1",
		"",
		"n",
		"y",
		"router", // no port
		"10.0.0.1:443",
	)
	if !strings.Contains(out, "is not a CIDR subnet") || !strings.Contains(out, "is not host:port") {
		t.Fatalf("missing validation messages:\n%s", out)
	}
	if got.ExitNode != "exit-1" || !slices.Equal(got.HomeSubnets, []string{"10.0.0.0/24"}) ||
		!slices.Equal(got.SiteRouterTargets, []string{"10.0.0.1:443"}) || len(got.TrustedWifis) != 0 {
		t.Fatalf("tailscale = %#v", got)
	}
}

func TestCollectTailscaleQuotedWifiNames(t *testing.T) {
	got, out := collectTailscaleAnswers(t,
		"", "y", "OpenWrt",
		`"cafe, upstairs`, // unterminated quote is asked again
		`synthetic-5ghz, "cafe, upstairs", synthetic-guest`,
		"elsewhere", // not a trusted Wi-Fi: asked again
		`"cafe, upstairs"`,
		"n", "n",
	)
	if !strings.Contains(out, "missing closing") || !strings.Contains(out, `"elsewhere" is not a trusted Wi-Fi`) {
		t.Fatalf("missing validation messages:\n%s", out)
	}
	if !slices.Equal(got.TrustedWifis, []string{"synthetic-5ghz", "cafe, upstairs", "synthetic-guest"}) {
		t.Fatalf("trustedWifis = %q", got.TrustedWifis)
	}
	if len(got.WifiExitNodes) != 1 || got.WifiExitNodes["cafe, upstairs"] != "OpenWrt" {
		t.Fatalf("wifiExitNodes = %q", got.WifiExitNodes)
	}
}

func TestCollectTailscaleRouterHomeSkipsSiteTargets(t *testing.T) {
	got, _ := collectTailscaleAnswers(t, "", "y", "exit-1", "", "")
	if !got.RouterHome || got.SiteRouterTrust || len(got.SiteRouterTargets) != 0 || got.ExitNode != "exit-1" {
		t.Fatalf("tailscale = %#v", got)
	}
}

func TestCollectTailscaleWithoutVPNKeepsHomeBypassOnly(t *testing.T) {
	got, _ := collectTailscaleAnswers(t, "", "n")
	if got.ExitNode != "" || got.SiteRouterTrust || len(got.TrustedWifis) != 0 {
		t.Fatalf("tailscale = %#v", got)
	}
}

func TestCollectTailscaleExitNodeDefaultsToAuto(t *testing.T) {
	got, _ := collectTailscaleAnswers(t, "", "y", "", "home", "", "n", "n")
	if got.ExitNode != "auto" || !slices.Equal(got.TrustedWifis, []string{"home"}) || got.WifiExitNodes != nil {
		t.Fatalf("tailscale = %#v", got)
	}
}
