package nixrender

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
)

func TestStringEscapesNixInterpolation(t *testing.T) {
	got := String(`hello ${builtins.abort "no"} \\ "world"`)
	want := `"hello \${builtins.abort \"no\"} \\\\ \"world\""`
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestRenderEscapesAllUserStrings(t *testing.T) {
	s := Settings{System: "x86_64-linux", Hostname: `${builtins.abort "bad"}`, Editors: []string{`a${b}`}}
	got := string(Render(s))
	if !strings.Contains(got, `hostname = "\${builtins.abort \"bad\"}";`) || !strings.Contains(got, `editors = [ "a\${b}" ];`) {
		t.Fatalf("unsafe or missing escaped output:\n%s", got)
	}
}

func TestStrings(t *testing.T) {
	if got := Strings([]string{"a", "b"}); got != `[ "a" "b" ]` {
		t.Fatalf("Strings() = %q", got)
	}
}

func TestRenderRecoveryPolicyDefaultsDisabled(t *testing.T) {
	got := string(Render(Settings{}))
	for _, want := range []string{
		"recoveryEnable = false;",
		"recoveryPartitionEnable = false;",
		"jodsPrebootLockEnable = false;",
		"secureBootEnable = false;",
		"usbTrustEnforce = false;",
		`usbTrustTpmHandle = "";`,
		"usbReviewTechnicalView = false;",
		"endpointManagedDevice = false;",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q from settings:\n%s", want, got)
		}
	}
}

func TestRenderAIResolvedState(t *testing.T) {
	got := string(Render(Settings{Apps: []string{"ai"}, AIModel: "qwen2.5-coder:7b", AIAccelerationProfile: "auto", AIAgentMode: "workspace", AIContextTokens: 8192}))
	for _, want := range []string{`apps = [ "ai" ];`, `aiModel = "qwen2.5-coder:7b";`, `aiAccelerationProfile = "auto";`, `aiAgentMode = "workspace";`, "aiContextTokens = 8192;"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestRenderWeatherLocation(t *testing.T) {
	got := string(Render(Settings{WeatherCity: "Berlin", WeatherCountry: "Germany"}))
	for _, want := range []string{`weatherCity = "Berlin";`, `weatherCountry = "Germany";`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q from settings:\n%s", want, got)
		}
	}
}

func TestRenderJODSSettings(t *testing.T) {
	got := string(Render(Settings{
		EndpointManagedDevice:               true,
		JODSEndpoint:                        "https://jods.example.test:1666",
		JODSPolicySigningPublicKey:          strings.Repeat("ab", 32),
		JODSRecoveryCommandSigningPublicKey: strings.Repeat("cd", 32),
		JODSEnrollmentMode:                  "jade-registry-only",
		JODSAllowInsecureTLS:                false,
		JODSDeviceClass:                     "laptop",
		JODSDesktopProfile:                  "headless",
	}))
	for _, want := range []string{
		"endpointManagedDevice = true;",
		`jodsEndpoint = "https://jods.example.test:1666";`,
		`jodsRecoveryCommandSigningPublicKey = "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd";`,
		`jodsEnrollmentMode = "jade-registry-only";`,
		"jodsAllowInsecureTls = false;",
		`jodsDeviceClass = "laptop";`,
		`jodsDesktopProfile = "headless";`,
		"jodsFingerprintEnrollmentAllowed = false;",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q from settings:\n%s", want, got)
		}
	}
}

func TestRenderJODSDoesNotContainSecretFields(t *testing.T) {
	got := string(Render(Settings{}))
	for _, forbidden := range []string{"devicePrivateKey", "apiToken", "luksRecoveryKey"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("generated settings contain secret field %q", forbidden)
		}
	}
}

func TestRenderIncludesOrientationSensorSetting(t *testing.T) {
	rendered := string(Render(Settings{
		OrientationSensorEnable: true,
	}))

	if !strings.Contains(rendered, "orientationSensorEnable = true;") {
		t.Fatalf("orientation setting missing from render: %s", rendered)
	}
}

// The bar notice reads developerMode from the generated state.
func TestRenderIncludesDeveloperMode(t *testing.T) {
	rendered := string(Render(FromUser(config.User{DeveloperMode: true})))

	if !strings.Contains(rendered, "developerMode = true;") {
		t.Fatalf("developer mode missing from render: %s", rendered)
	}
}

func TestRenderThemeDetailsUsesRepositoryThemeDirectory(t *testing.T) {
	got := string(Render(Settings{Theme: "noctalia"}))
	want := `themeDetails = import (./. + "/../themes/${theme}.nix") {inherit pkgs;};`
	if !strings.Contains(got, want) {
		t.Fatalf("theme path is not rooted above generated/:\n%s", got)
	}
}

func TestFromUserMapsRoutedIntent(t *testing.T) {
	user := config.User{
		Hostname:              "gjallarOS",
		Username:              "baka",
		USBGuardEnable:        true,
		USBTrustEnforce:       true,
		USBTrustTPMHandle:     "0x81000042",
		PrintingEnable:        true,
		NetworkPrintingEnable: true,
		Apps:                  []string{"containers"},
		WebApplications: []config.WebApplicationIntent{
			{
				ID:       config.WebApplicationPlane,
				Endpoint: "https://plane.example.test",
			},
		},
		EndpointManagedDevice:  true,
		JODSEndpoint:           "https://jods.example.test",
		JODSPolicySigningKey:   strings.Repeat("ab", 32),
		JODSRecoverySigningKey: strings.Repeat("cd", 32),
		JODSEnrollmentMode:     "manual",
		JODSDeviceClass:        "laptop",
		JODSDesktopProfile:     "headless",
		JODSFingerprintEnroll:  true,
		Theme:                  "noctalia",
		Editors:                []string{"vscodium"},
		Browsers:               []string{"firefox"},
		PreferredEditor:        "vscodium",
		PreferredBrowser:       "firefox",
		Shell:                  "zsh",
		AIAgentMode:            "workspace",
	}

	got := FromUser(user)

	if !got.PrintingEnable ||
		!got.NetworkPrintingEnable ||
		!slices.Equal(got.Apps, []string{"containers"}) ||
		!got.USBGuardEnable ||
		!got.USBTrustEnforce {
		t.Fatalf(
			"routed booleans were not mapped: %#v",
			got,
		)
	}

	if got.USBTrustTPMHandle != user.USBTrustTPMHandle {
		t.Fatalf(
			"USB trust TPM handle was not mapped: %#v",
			got,
		)
	}

	if got.JODSEndpoint != user.JODSEndpoint ||
		got.Theme != user.Theme {
		t.Fatalf(
			"routed strings were not mapped: %#v",
			got,
		)
	}

	if len(got.WebApplications) != 1 ||
		got.WebApplications[0] != user.WebApplications[0] {
		t.Fatalf(
			"canonical web application intent was not mapped: %#v",
			got.WebApplications,
		)
	}

	if got.ODDCModel != "" || got.AIModel != "" {
		t.Fatalf(
			"derived fields leaked into FromUser: %#v",
			got,
		)
	}
}

func TestSyncUserIntentPreservesDerivedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.nix")

	before := `{pkgs, inputs, ...}:
rec {
    hostname = "old";
    printingEnable = false;
    planeEnable = false;
    planeHost = "";
    graphicsVendor = "amd";
    graphicsDeviceId = "15bf";
    graphicsType = "integrated";
    graphicsCompute = true;
    graphicsDriverBranch = "legacy_580";
    frameworkEnable = true;
    workUsername = "legacy-work";
    deviceSysVendor = "Legacy Vendor";
    wifiDriver = "legacy-driver";
    aiModel = "derived-model";
    theme = "old-theme";
    themeDetails = import (./. + "/../themes/${theme}.nix") {inherit pkgs;};
}
`

	if err := os.WriteFile(path, []byte(before), 0o640); err != nil {
		t.Fatal(err)
	}

	user := config.User{
		Hostname:       "gjallarOS",
		PrintingEnable: true,
		WebApplications: []config.WebApplicationIntent{
			{
				ID:       config.WebApplicationPlane,
				Endpoint: "https://plane.example.test",
			},
		},
		Theme:              "noctalia",
		AIAgentMode:        "workspace",
		JODSEnrollmentMode: "manual",
	}

	if err := SyncUserIntent(path, user); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	got := string(data)

	for _, want := range []string{
		`hostname = "gjallarOS";`,
		`printingEnable = true;`,
		`webApplications = [ { id = "plane"; endpoint = "https://plane.example.test"; } ];`,
		`aiModel = "derived-model";`,
		`theme = "noctalia";`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q after sync:\n%s", want, got)
		}
	}

	for _, retired := range []string{
		"planeEnable",
		"planeHost",
		"graphicsVendor",
		"graphicsDeviceId",
		"graphicsType",
		"graphicsCompute",
		"graphicsDriverBranch",
		"frameworkEnable",
		"workUsername",
		"deviceSysVendor",
		"wifiDriver",
	} {
		if strings.Contains(got, retired+" = ") {
			t.Fatalf(
				"retired generated field %q survived sync:\n%s",
				retired,
				got,
			)
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed to %o", info.Mode().Perm())
	}
}

func TestRenderAICentralServer(t *testing.T) {
	got := string(Render(Settings{AIEndpoint: "https://192.168.8.205", AIRemoteModel: "qwen3-coder:30b", AIRemoteContextTokens: 32768}))
	for _, want := range []string{`aiEndpoint = "https://192.168.8.205";`, `aiRemoteModel = "qwen3-coder:30b";`, "aiRemoteContextTokens = 32768;"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q", want)
		}
	}
}
