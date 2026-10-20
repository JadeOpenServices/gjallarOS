package settings

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
)

func configText(t *testing.T, user config.User) []byte {
	t.Helper()
	data, err := json.MarshalIndent(user, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func defaults(t *testing.T) config.User {
	t.Helper()
	user, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func raw(values map[string]string) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		out[key] = json.RawMessage(value)
	}
	return out
}

func TestPlanTakesStrictestTierAndDropsNoOps(t *testing.T) {
	user := defaults(t)
	data := configText(t, user)
	change, err := Plan(data, raw(map[string]string{
		"weatherCity": `"Example City"`,
		"hostname":    `"desk-07"`,
		"theme":       `"` + user.Theme + `"`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if change.Tier != config.TierSecurity {
		t.Fatalf("tier = %s, want security", change.Tier)
	}
	if len(change.Keys) != 2 || change.Keys[0].Key != "hostname" || change.Keys[1].Key != "weatherCity" {
		t.Fatalf("keys = %+v, want hostname and weatherCity", change.Keys)
	}
	if len(change.ID) != 32 {
		t.Fatalf("id %q is not 16 random bytes in hex", change.ID)
	}
	if _, err := Plan(data, raw(map[string]string{"theme": `"` + user.Theme + `"`})); err == nil {
		t.Fatal("accepted a request that changes nothing")
	}
}

func TestPlanRefuses(t *testing.T) {
	data := configText(t, defaults(t))
	for name, values := range map[string]map[string]string{
		"unknown key":   {"backgroundNormal": `"x"`},
		"installer key": {"runRebuild": `false`},
		"broken value":  {"weatherCity": `"open`},
		"wrong type":    {"theme": `7`},
		"too large":     {"weatherCity": `"` + strings.Repeat("a", MaxRequestBytes) + `"`},
	} {
		if _, err := Plan(data, raw(values)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPlanOnManagedDeviceAllowsEverydayOnly(t *testing.T) {
	user := defaults(t)
	user.EndpointManagedDevice = true
	user.JODSEndpoint = "https://jods.example.test:1666"
	user.JODSPolicySigningKey = strings.Repeat("ab", 32)
	user.JODSRecoverySigningKey = strings.Repeat("cd", 32)
	user.JODSEnrollmentMode = "manual"
	user.JODSDeviceClass = "laptop"
	user.JODSDesktopProfile = "headless"
	data := configText(t, user)
	if _, err := Plan(data, raw(map[string]string{"weatherCity": `"Example City"`})); err != nil {
		t.Fatalf("everyday change refused: %v", err)
	}
	if _, err := Plan(data, raw(map[string]string{"hostname": `"desk-07"`})); err == nil {
		t.Fatal("accepted a security change on a managed device")
	}
}

func TestPlanLapseWaitsOnlyWhenLoosened(t *testing.T) {
	user := defaults(t)
	cases := []struct {
		old, new int
		tier     config.Tier
	}{
		{0, 30, config.TierEveryday},
		{30, 7, config.TierEveryday},
		{30, 60, config.TierSecurity},
		{30, 0, config.TierSecurity},
	}
	for _, c := range cases {
		user.USBTrustLapseDays = c.old
		change, err := Plan(configText(t, user), raw(map[string]string{"usbTrustLapseDays": strconv.Itoa(c.new)}))
		if err != nil {
			t.Fatalf("%d → %d: %v", c.old, c.new, err)
		}
		if change.Tier != c.tier {
			t.Errorf("%d → %d: tier %s, want %s", c.old, c.new, change.Tier, c.tier)
		}
	}
	user.USBTrustLapseDays = 0
	change, err := Plan(configText(t, user), raw(map[string]string{"usbTrustLapseDays": `30`, "hostname": `"desk-07"`}))
	if err != nil || change.Tier != config.TierSecurity {
		t.Fatalf("lapse on beside a security key: %s %v", change.Tier, err)
	}
	if _, err := Plan(configText(t, user), raw(map[string]string{"usbTrustLapseDays": `-1`})); err == nil {
		t.Fatal("accepted a negative lapse")
	}
}

func TestPlanDeveloperModeWaitsOnlyWhenTurnedOn(t *testing.T) {
	user := defaults(t)
	change, err := Plan(configText(t, user), raw(map[string]string{"developerMode": `true`}))
	if err != nil || change.Tier != config.TierCritical {
		t.Fatalf("developer mode on: %s %v", change.Tier, err)
	}
	user.DeveloperMode = true
	change, err = Plan(configText(t, user), raw(map[string]string{"developerMode": `false`}))
	if err != nil || change.Tier != config.TierEveryday {
		t.Fatalf("developer mode off: %s %v", change.Tier, err)
	}
}

func TestPlanOnManagedDeviceRefusesTighteningToo(t *testing.T) {
	user := defaults(t)
	user.EndpointManagedDevice = true
	user.JODSEndpoint = "https://jods.example.test:1666"
	user.JODSPolicySigningKey = strings.Repeat("ab", 32)
	user.JODSRecoverySigningKey = strings.Repeat("cd", 32)
	user.JODSEnrollmentMode = "manual"
	user.JODSDeviceClass = "laptop"
	user.JODSDesktopProfile = "headless"
	if _, err := Plan(configText(t, user), raw(map[string]string{"usbTrustLapseDays": `30`})); err == nil {
		t.Fatal("JODS-managed lapse changed locally")
	}
}

func TestReadyAt(t *testing.T) {
	asked := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		tier config.Tier
		home bool
		wait time.Duration
	}{
		{config.TierEveryday, false, 0},
		{config.TierSecurity, true, ReadingDelay},
		{config.TierSecurity, false, SecurityDelay},
		{config.TierCritical, true, SecurityDelay},
		{config.TierCritical, false, SecurityDelay},
	} {
		got := Change{Tier: tc.tier, Asked: asked}.ReadyAt(tc.home)
		if want := asked.Add(tc.wait); !got.Equal(want) {
			t.Errorf("%s home=%v: ready at %s, want %s", tc.tier, tc.home, got, want)
		}
	}
}

func TestApplyChecksOldValues(t *testing.T) {
	data := configText(t, defaults(t))
	change, err := Plan(data, raw(map[string]string{"hostname": `"desk-07"`}))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := change.Apply(data)
	if err != nil {
		t.Fatal(err)
	}
	user, err := config.Parse(updated)
	if err != nil {
		t.Fatal(err)
	}
	if user.Hostname != "desk-07" {
		t.Fatalf("hostname = %q after apply", user.Hostname)
	}
	moved, _, err := config.ChangeText(data, raw(map[string]string{"hostname": `"other-01"`}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := change.Apply(moved); err == nil {
		t.Fatal("applied over a value that changed after the request")
	}
}

func TestMessageShowsHiddenCharacters(t *testing.T) {
	change := Change{Tier: config.TierSecurity, Keys: []KeyChange{
		{Key: "hostname", Old: json.RawMessage(`"desk-07"`), New: json.RawMessage("\"desk\u202e70-\"")},
		{Key: "aiEndpoint", New: json.RawMessage(`"` + strings.Repeat("a", 200) + `"`)},
	}}
	message := change.Message()
	if strings.ContainsRune(message, '\u202e') || !strings.Contains(message, `\u202e`) {
		t.Fatalf("message %q hides a direction mark", message)
	}
	if !strings.Contains(message, "aiEndpoint: (not set) -> \"aaa") || !strings.Contains(message, "...") {
		t.Fatalf("message %q does not mark the unset or shortened value", message)
	}
	if change.KeyNames() != "hostname,aiEndpoint" {
		t.Fatalf("key names = %q", change.KeyNames())
	}
}

func TestLinesShowWhatChangesInsideLongValues(t *testing.T) {
	long := strings.Repeat("x", 200)
	key := KeyChange{
		Key: "firefoxAddons",
		Old: json.RawMessage(`{"comfort":{"note":"` + long + `"},"passwords":{"enable":false,"addons":{"passbolt":"off"}},"list":[1,2]}`),
		New: json.RawMessage(`{"comfort":{"note":"` + long + `"},"passwords":{"enable":true,"addons":{"passbolt":"on"}},"list":[1,3,4],"new\u202e":1}`),
	}
	got := key.Lines()
	want := []string{
		"firefoxAddons.list[1]: 2 -> 3",
		"firefoxAddons.list[2]: (not set) -> 4",
		"firefoxAddons.new\\u202e: (not set) -> 1",
		"firefoxAddons.passwords.addons.passbolt: \"off\" -> \"on\"",
		"firefoxAddons.passwords.enable: false -> true",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	if m := (Change{Keys: []KeyChange{key}}).Message(); strings.Contains(m, long) || !strings.Contains(m, "passbolt") {
		t.Fatalf("message %q", m)
	}
}
