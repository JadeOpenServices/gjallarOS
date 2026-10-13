// Package nixrender emits the small Nix literal subset used by installer
// generated machine state. It deliberately has no evaluation capability.
package nixrender

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/apps"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
)

// String returns a Nix double-quoted string literal. In addition to ordinary
// quoting it escapes interpolation markers, so a value from user.config.json
// can never become executable Nix during generated-state rendering.
func String(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	value = strings.ReplaceAll(value, "${", "\\${")
	value = strings.ReplaceAll(value, "\n", "\\n")
	value = strings.ReplaceAll(value, "\r", "\\r")
	return "\"" + value + "\""
}

func Strings(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, String(value))
	}
	return "[ " + strings.Join(quoted, " ") + " ]"
}

func WebApplications(values []config.WebApplicationIntent) string {
	rendered := make([]string, 0, len(values))
	for _, application := range values {
		rendered = append(
			rendered,
			fmt.Sprintf(
				"{ id = %s; endpoint = %s; }",
				String(application.ID),
				String(application.Endpoint),
			),
		)
	}
	return "[ " + strings.Join(rendered, " ") + " ]"
}

// FirefoxStoreAddons renders the pinned store add-ons the build installs;
// name, version and permissions stay in user.config.json.
func FirefoxStoreAddons(values []apps.FirefoxStoreAddon) string {
	rendered := make([]string, 0, len(values))
	for _, a := range values {
		rendered = append(
			rendered,
			fmt.Sprintf(
				"{ id = %s; url = %s; sha256 = %s; mode = %s; }",
				String(a.ID),
				String(a.URL),
				String(a.SHA256),
				String(a.Mode),
			),
		)
	}
	return "[ " + strings.Join(rendered, " ") + " ]"
}

// StringAttrs renders an attribute set of strings, sorted by name.
func StringAttrs(attrs map[string]string) string {
	var b strings.Builder
	b.WriteString("{ ")
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		b.WriteString(String(name) + " = " + String(attrs[name]) + "; ")
	}
	b.WriteString("}")
	return b.String()
}

// Tailscale renders the intent on one line so SyncUserIntent can replace
// it; null keeps the pre-installer defaults.
func Tailscale(t *config.TailscaleIntent) string {
	if t == nil {
		return "null"
	}
	return fmt.Sprintf(
		"{ homeSubnets = %s; trustedWifis = %s; exitNode = %s; wifiExitNodes = %s; siteRouterTrust = %t; siteRouterTargets = %s; }",
		Strings(t.HomeSubnets),
		Strings(t.TrustedWifis),
		String(t.ExitNode),
		StringAttrs(t.WifiExitNodes),
		t.SiteRouterTrust,
		Strings(t.SiteRouterTargets),
	)
}

// Settings contains only values written to generated/state.nix. Values are rendered
// as literals; none are evaluated as Nix source.
type Settings struct {
	System                                                                                  string
	Hostname, Username, Timezone, Locale                                                    string
	KeyboardLayout, KeyboardVariant                                                         string
	WeatherCity, WeatherCountry                                                             string
	TouchpadWorkspaceSwipe, TouchscreenEnable                                               bool
	PenTabletEnable, OrientationSensorEnable                                                bool
	ClamshellEnable, USBGuardEnable, USBTrustEnforce, PrintingEnable, NetworkPrintingEnable bool
	ConsoleLoginEnable, USBReviewTechnical, DolphinDriveUsage, FirmwareFlashReadEntry       bool
	USBTrustTPMHandle                                                                       string
	Name, Email, GitHubUsername, DotfilesDir                                                string
	RootPasswordFile, UserPasswordFile                                                      string
	DebugFunctions                                                                          bool
	Shell                                                                                   string
	Editors, Browsers                                                                       []string
	PreferredEditor, PreferredBrowser                                                       string
	WebApplications                                                                         []config.WebApplicationIntent
	Tailscale                                                                               *config.TailscaleIntent
	Apps                                                                                    []string
	FirefoxAddons                                                                           map[string]string
	FirefoxStoreAddons                                                                      []apps.FirefoxStoreAddon
	NextcloudHost                                                                           string
	NextcloudEnable                                                                         bool
	ODDCModel                                                                               string
	GraphicsBusID, GraphicsIntegratedBusID                                                  string
	AIModel, AIAccelerationProfile                                                          string
	AIAgentMode                                                                             string
	AIContextTokens, AIVRAMMB                                                               int
	AIEndpoint, AIRemoteModel                                                               string
	AIRemoteContextTokens                                                                   int
	LUKSTPM2Enable, LUKSTPM2PIN                                                             bool
	RecoveryEnable, RecoveryPartitionEnable, JODSPrebootLockEnable                          bool
	SecureBootEnable                                                                        bool
	EndpointManagedDevice                                                                   bool
	JODSEndpoint, JODSPolicySigningPublicKey                                                string
	JODSRecoveryCommandSigningPublicKey                                                     string
	JODSEnrollmentMode                                                                      string
	JODSAllowInsecureTLS                                                                    bool
	JODSDeviceClass, JODSDesktopProfile                                                     string
	JODSFingerprintEnrollmentAllowed                                                        bool
	WMs                                                                                     []string
	Theme                                                                                   string
}

// FromUser maps direct user intent into generated-state fields. Hardware,
// discovery, installer-owned credentials and other derived facts are filled by
// the installer after this mapping. Keeping this map here gives rebuild and
// installation one owner for the user -> generated/state.nix contract.
func FromUser(user config.User) Settings {
	return Settings{
		Hostname:                            user.Hostname,
		Username:                            user.Username,
		Timezone:                            user.Timezone,
		Locale:                              user.Locale,
		KeyboardLayout:                      user.KeyboardLayout,
		KeyboardVariant:                     user.KeyboardVariant,
		WeatherCity:                         user.WeatherCity,
		WeatherCountry:                      user.WeatherCountry,
		TouchpadWorkspaceSwipe:              user.TouchpadWorkspaceSwipe,
		ClamshellEnable:                     user.ClamshellEnable,
		ConsoleLoginEnable:                  user.ConsoleLoginEnable,
		USBGuardEnable:                      user.USBGuardEnable,
		USBTrustEnforce:                     user.USBTrustEnforce,
		USBTrustTPMHandle:                   user.USBTrustTPMHandle,
		USBReviewTechnical:                  user.USBReviewTechnical,
		PrintingEnable:                      user.PrintingEnable,
		NetworkPrintingEnable:               user.NetworkPrintingEnable,
		DolphinDriveUsage:                   user.DolphinDriveUsage,
		FirmwareFlashReadEntry:              user.FirmwareFlashReadEntry,
		Name:                                user.Name,
		Email:                               user.Email,
		GitHubUsername:                      user.GitHubUsername,
		DotfilesDir:                         user.DotfilesDir,
		DebugFunctions:                      user.DebugFunctions,
		Shell:                               user.Shell,
		Editors:                             user.Editors,
		Browsers:                            user.Browsers,
		PreferredEditor:                     user.PreferredEditor,
		PreferredBrowser:                    user.PreferredBrowser,
		WebApplications:                     user.WebApplications,
		Tailscale:                           user.Tailscale,
		Apps:                                user.Apps,
		FirefoxAddons:                       apps.ActiveFirefoxAddons(user.FirefoxAddons),
		FirefoxStoreAddons:                  apps.ActiveStoreAddons(user.FirefoxStoreAddons),
		NextcloudEnable:                     user.NextcloudEnable,
		NextcloudHost:                       user.NextcloudHost,
		AIAgentMode:                         user.AIAgentMode,
		AIEndpoint:                          user.AIEndpoint,
		AIRemoteModel:                       user.AIRemoteModel,
		AIRemoteContextTokens:               user.AIRemoteContextTokens,
		LUKSTPM2Enable:                      user.LUKSTPM2Enable,
		LUKSTPM2PIN:                         user.LUKSTPM2PIN,
		RecoveryEnable:                      user.RecoveryEnable,
		RecoveryPartitionEnable:             user.RecoveryPartitionEnable,
		JODSPrebootLockEnable:               user.JODSPrebootLockEnable,
		SecureBootEnable:                    user.SecureBootEnable,
		EndpointManagedDevice:               user.EndpointManagedDevice,
		JODSEndpoint:                        user.JODSEndpoint,
		JODSPolicySigningPublicKey:          user.JODSPolicySigningKey,
		JODSRecoveryCommandSigningPublicKey: user.JODSRecoverySigningKey,
		JODSEnrollmentMode:                  user.JODSEnrollmentMode,
		JODSAllowInsecureTLS:                user.JODSAllowInsecureTLS,
		JODSDeviceClass:                     user.JODSDeviceClass,
		JODSDesktopProfile:                  user.JODSDesktopProfile,
		JODSFingerprintEnrollmentAllowed:    user.JODSFingerprintEnroll,
		Theme:                               user.Theme,
	}
}

var retiredGeneratedKeys = map[string]bool{
	// Selected by apps since the app catalogue.
	"aiEnable":             true,
	"containersEnable":     true,
	"nemuEnable":           true,
	"backgroundWork":       true,
	"deviceBoardName":      true,
	"deviceBoardVendor":    true,
	"deviceBoardVersion":   true,
	"deviceProductName":    true,
	"deviceProductVersion": true,
	"deviceSysVendor":      true,
	"frameworkEnable":      true,
	"frameworkModel":       true,
	"planeEnable":          true,
	"planeHost":            true,
	"drawioEnable":         true,
	"drawioSelfHosted":     true,
	"drawioHost":           true,
	"graphicsCompute":      true,
	"graphicsDeviceId":     true,
	"graphicsDriverBranch": true,
	"graphicsType":         true,
	"graphicsVendor":       true,
	"wifiDriver":           true,
	"workUserEnable":       true,
	"workUserPasswordFile": true,
	"workUsername":         true,
}

var userIntentKeys = []string{
	"hostname",
	"username",
	"timezone",
	"locale",
	"keyboardLayout",
	"keyboardVariant",
	"weatherCity",
	"weatherCountry",
	"touchpadWorkspaceSwipe",
	"clamshellEnable",
	"consoleLoginEnable",
	"usbguardEnable",
	"usbTrustEnforce",
	"usbTrustTpmHandle",
	"usbReviewTechnicalView",
	"printingEnable",
	"networkPrintingEnable",
	"dolphinDriveUsage",
	"firmwareFlashReadEntry",
	"name",
	"email",
	"githubUsername",
	"dotfilesDir",
	"debugFunctions",
	"shell",
	"editors",
	"browsers",
	"preferredEditor",
	"preferredBrowser",
	"webApplications",
	"tailscale",
	"nextcloudEnable",
	"nextcloudHost",
	"aiAgentMode",
	"aiEndpoint",
	"aiRemoteModel",
	"aiRemoteContextTokens",
	"luksTpm2Enable",
	"luksTpm2Pin",
	"recoveryEnable",
	"recoveryPartitionEnable",
	"jodsPrebootLockEnable",
	"secureBootEnable",
	"endpointManagedDevice",
	"jodsEndpoint",
	"jodsPolicySigningPublicKey",
	"jodsRecoveryCommandSigningPublicKey",
	"jodsEnrollmentMode",
	"jodsAllowInsecureTls",
	"jodsDeviceClass",
	"jodsDesktopProfile",
	"jodsFingerprintEnrollmentAllowed",
	"theme",
	"apps",
	"firefoxAddons",
	"firefoxStoreAddons",
}

// SyncUserIntent updates only direct user-owned assignments in an existing
// generated state file. Derived hardware/AI facts and installer-owned values
// remain byte-for-byte untouched.
func SyncUserIntent(path string, user config.User) error {
	rendered := strings.Split(string(Render(FromUser(user))), "\n")
	desired := make(map[string]string, len(userIntentKeys))
	for _, line := range rendered {
		trimmed := strings.TrimSpace(line)
		for _, key := range userIntentKeys {
			if strings.HasPrefix(trimmed, key+" = ") {
				desired[key] = line
				break
			}
		}
	}
	if len(desired) != len(userIntentKeys) {
		return fmt.Errorf("render user intent: expected %d fields, got %d", len(userIntentKeys), len(desired))
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	lines := strings.Split(string(data), "\n")
	seen := make(map[string]bool, len(userIntentKeys))
	result := make([]string, 0, len(lines)+len(userIntentKeys))
	insertedMissing := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		assignmentKey := strings.TrimSpace(strings.SplitN(trimmed, "=", 2)[0])
		if retiredGeneratedKeys[assignmentKey] {
			continue
		}

		if !insertedMissing && strings.HasPrefix(trimmed, "themeDetails = ") {
			for _, key := range userIntentKeys {
				if !seen[key] {
					result = append(result, desired[key])
					seen[key] = true
				}
			}
			insertedMissing = true
		}

		replaced := false
		for _, key := range userIntentKeys {
			if !strings.HasPrefix(trimmed, key+" = ") {
				continue
			}
			if seen[key] {
				return fmt.Errorf("sync user intent %s: duplicate generated assignment", key)
			}
			result = append(result, desired[key])
			seen[key] = true
			replaced = true
			break
		}
		if !replaced {
			result = append(result, line)
		}
	}

	for _, key := range userIntentKeys {
		if !seen[key] {
			return fmt.Errorf("sync user intent %s: themeDetails insertion anchor not found", key)
		}
	}

	updated := strings.Join(result, "\n")
	if updated == string(data) {
		return nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-user-intent-*")
	if err != nil {
		return fmt.Errorf("create temporary generated state: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return fmt.Errorf("preserve generated state permissions: %w", err)
	}
	if _, err := tmp.WriteString(updated); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary generated state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temporary generated state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary generated state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func Render(s Settings) []byte {
	var b bytes.Buffer
	fmt.Fprintln(&b, "{pkgs, inputs, ...}:")
	fmt.Fprintln(&b, "rec {")
	fmt.Fprintln(&b, "    # Generated by gjallarctl; do not edit by hand.")
	str := func(k, v string) { fmt.Fprintf(&b, "    %s = %s;\n", k, String(v)) }
	boolean := func(k string, v bool) { fmt.Fprintf(&b, "    %s = %t;\n", k, v) }
	integer := func(k string, v int) { fmt.Fprintf(&b, "    %s = %d;\n", k, v) }
	list := func(k string, v []string) { fmt.Fprintf(&b, "    %s = %s;\n", k, Strings(v)) }
	str("system", s.System)
	str("hostname", s.Hostname)
	str("username", s.Username)
	str("timezone", s.Timezone)
	str("locale", s.Locale)
	str("keyboardLayout", s.KeyboardLayout)
	str("keyboardVariant", s.KeyboardVariant)
	str("weatherCity", s.WeatherCity)
	str("weatherCountry", s.WeatherCountry)
	boolean("touchpadWorkspaceSwipe", s.TouchpadWorkspaceSwipe)
	boolean("touchscreenEnable", s.TouchscreenEnable)
	boolean("penTabletEnable", s.PenTabletEnable)
	boolean("orientationSensorEnable", s.OrientationSensorEnable)
	boolean("clamshellEnable", s.ClamshellEnable)
	boolean("consoleLoginEnable", s.ConsoleLoginEnable)
	boolean("usbguardEnable", s.USBGuardEnable)
	boolean("usbTrustEnforce", s.USBTrustEnforce)
	str("usbTrustTpmHandle", s.USBTrustTPMHandle)
	boolean("usbReviewTechnicalView", s.USBReviewTechnical)
	boolean("printingEnable", s.PrintingEnable)
	boolean("networkPrintingEnable", s.NetworkPrintingEnable)
	boolean("dolphinDriveUsage", s.DolphinDriveUsage)
	boolean("firmwareFlashReadEntry", s.FirmwareFlashReadEntry)
	str("name", s.Name)
	str("email", s.Email)
	str("githubUsername", s.GitHubUsername)
	str("dotfilesDir", s.DotfilesDir)
	str("rootPasswordFile", s.RootPasswordFile)
	str("userPasswordFile", s.UserPasswordFile)
	boolean("debugFunctions", s.DebugFunctions)
	str("shell", s.Shell)
	list("editors", s.Editors)
	list("browsers", s.Browsers)
	str("preferredEditor", s.PreferredEditor)
	str("preferredBrowser", s.PreferredBrowser)
	fmt.Fprintf(&b, "    webApplications = %s;\n", WebApplications(s.WebApplications))
	fmt.Fprintf(&b, "    tailscale = %s;\n", Tailscale(s.Tailscale))
	list("apps", s.Apps)
	fmt.Fprintf(&b, "    firefoxAddons = %s;\n", StringAttrs(s.FirefoxAddons))
	fmt.Fprintf(&b, "    firefoxStoreAddons = %s;\n", FirefoxStoreAddons(s.FirefoxStoreAddons))
	boolean("nextcloudEnable", s.NextcloudEnable)
	str("nextcloudHost", s.NextcloudHost)
	str("oddcModel", s.ODDCModel)
	str("graphicsBusId", s.GraphicsBusID)
	str("graphicsIntegratedBusId", s.GraphicsIntegratedBusID)
	str("aiModel", s.AIModel)
	str("aiAccelerationProfile", s.AIAccelerationProfile)
	str("aiAgentMode", s.AIAgentMode)
	integer("aiContextTokens", s.AIContextTokens)
	integer("aiVramMB", s.AIVRAMMB)
	str("aiEndpoint", s.AIEndpoint)
	str("aiRemoteModel", s.AIRemoteModel)
	integer("aiRemoteContextTokens", s.AIRemoteContextTokens)
	boolean("luksTpm2Enable", s.LUKSTPM2Enable)
	boolean("luksTpm2Pin", s.LUKSTPM2Enable && s.LUKSTPM2PIN)
	boolean("recoveryEnable", s.RecoveryEnable)
	boolean("recoveryPartitionEnable", s.RecoveryPartitionEnable)
	boolean("jodsPrebootLockEnable", s.JODSPrebootLockEnable)
	boolean("secureBootEnable", s.SecureBootEnable)
	boolean("endpointManagedDevice", s.EndpointManagedDevice)
	str("jodsEndpoint", s.JODSEndpoint)
	str("jodsPolicySigningPublicKey", s.JODSPolicySigningPublicKey)
	str("jodsRecoveryCommandSigningPublicKey", s.JODSRecoveryCommandSigningPublicKey)
	str("jodsEnrollmentMode", s.JODSEnrollmentMode)
	boolean("jodsAllowInsecureTls", s.JODSAllowInsecureTLS)
	str("jodsDeviceClass", s.JODSDeviceClass)
	str("jodsDesktopProfile", s.JODSDesktopProfile)
	boolean("jodsFingerprintEnrollmentAllowed", s.JODSFingerprintEnrollmentAllowed)
	list("wms", s.WMs)
	str("theme", s.Theme)
	fmt.Fprintln(&b, "    themeDetails = import (./. + \"/../themes/${theme}.nix\") {inherit pkgs;};")
	fmt.Fprintln(&b, "}")
	return b.Bytes()
}

// WriteAtomic writes through a sibling temporary file then renames it.
func WriteAtomic(path string, s Settings) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create generated state directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".state.nix-*")
	if err != nil {
		return fmt.Errorf("create temporary settings: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return fmt.Errorf("set settings permissions: %w", err)
	}
	if _, err := tmp.Write(Render(s)); err != nil {
		tmp.Close()
		return fmt.Errorf("write settings: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync settings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close settings: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	return nil
}
