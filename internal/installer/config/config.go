// Package config defines the typed contract between installer input,
// discovery, and generated/state.nix.
package config

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/apps"
	"github.com/JadeOpenServices/gjallarOS/internal/sshsig"
)

// User is user.config.json. Every field carries a tier tag that says how the
// settings service confirms a change after install (ROADMAP §14); see
// TierOf. Keys tagged scope:"device" describe this device rather than the
// person and never come from another device's settings file; see
// DeviceScoped.
type User struct {
	Hostname               string                 `json:"hostname" tier:"security" scope:"device"`
	Username               string                 `json:"username" tier:"security" scope:"device"`
	Timezone               string                 `json:"timezone" tier:"everyday"`
	Locale                 string                 `json:"locale" tier:"everyday"`
	KeyboardLayout         string                 `json:"keyboardLayout" tier:"everyday"`
	KeyboardVariant        string                 `json:"keyboardVariant" tier:"everyday"`
	WeatherCity            string                 `json:"weatherCity" tier:"everyday"`
	WeatherCountry         string                 `json:"weatherCountry" tier:"everyday"`
	TouchpadWorkspaceSwipe bool                   `json:"touchpadWorkspaceSwipe" tier:"everyday"`
	ClamshellEnable        bool                   `json:"clamshellEnable" tier:"everyday" scope:"device"`
	ConsoleLoginEnable     bool                   `json:"consoleLoginEnable" tier:"security"`
	USBGuardEnable         bool                   `json:"usbguardEnable" tier:"security" scope:"device"`
	USBTrustEnforce        bool                   `json:"usbTrustEnforce" tier:"security" scope:"device"`
	USBTrustTPMHandle      string                 `json:"usbTrustTpmHandle" tier:"security" scope:"device"`
	USBTrustLapseDays      int                    `json:"usbTrustLapseDays" tier:"security" scope:"device"`
	USBReviewTechnical     bool                   `json:"usbReviewTechnicalView" tier:"everyday"`
	PrintingEnable         bool                   `json:"printingEnable" tier:"everyday"`
	NetworkPrintingEnable  bool                   `json:"networkPrintingEnable" tier:"security"`
	DolphinDriveUsage      bool                   `json:"dolphinDriveUsage" tier:"everyday"`
	FirmwareFlashReadEntry bool                   `json:"firmwareFlashReadEntry" tier:"critical" scope:"device"`
	Name                   string                 `json:"name" tier:"everyday"`
	Email                  string                 `json:"email" tier:"everyday"`
	GitHubUsername         string                 `json:"githubUsername" tier:"everyday"`
	DotfilesDir            string                 `json:"dotfilesDir" tier:"security" scope:"device"`
	Shell                  string                 `json:"shell" tier:"everyday"`
	Editors                []string               `json:"editors" tier:"everyday"`
	Browsers               []string               `json:"browsers" tier:"everyday"`
	PreferredEditor        string                 `json:"preferredEditor" tier:"everyday"`
	PreferredBrowser       string                 `json:"preferredBrowser" tier:"everyday"`
	WebApplications        []WebApplicationIntent `json:"webApplications" tier:"security"`
	Tailscale              *TailscaleIntent       `json:"tailscale,omitempty" tier:"security" scope:"device"`
	// Apps are the selected catalogue app IDs (apps/<id>/meta.json).
	Apps []string `json:"apps" tier:"everyday"`
	// FirefoxAddons holds the add-on collections (apps/firefox-addons.json)
	// and each add-on's mode; an add-on only counts while its collection is on.
	FirefoxAddons map[string]apps.FirefoxCollectionChoice `json:"firefoxAddons" tier:"security"`
	// FirefoxStoreAddons are extra add-ons from addons.mozilla.org, each
	// pinned to one file.
	FirefoxStoreAddons []apps.FirefoxStoreAddon `json:"firefoxStoreAddons" tier:"security"`

	NextcloudEnable           bool   `json:"nextcloudEnable" tier:"security"`
	NextcloudHost             string `json:"nextcloudHost" tier:"security"`
	Theme                     string `json:"theme" tier:"everyday"`
	DebugFunctions            bool   `json:"debugFunctions" tier:"security"`
	OverrideAISelection       bool   `json:"overrideAiSelection" tier:"everyday"`
	OverrideModelWith         string `json:"overrideModelWith" tier:"everyday"`
	AIAgentMode               string `json:"aiAgentMode" tier:"security"`
	AIEndpoint                string `json:"aiEndpoint" tier:"security"`
	AIRemoteModel             string `json:"aiRemoteModel" tier:"everyday"`
	AIRemoteContextTokens     int    `json:"aiRemoteContextTokens" tier:"everyday"`
	LUKSTPM2Enable            bool   `json:"luksTpm2Enable" tier:"critical" scope:"device"`
	LUKSTPM2PIN               bool   `json:"luksTpm2Pin" tier:"critical" scope:"device"`
	RecoveryEnable            bool   `json:"recoveryEnable" tier:"critical" scope:"device"`
	RecoveryPartitionEnable   bool   `json:"recoveryPartitionEnable" tier:"critical" scope:"device"`
	JODSPrebootLockEnable     bool   `json:"jodsPrebootLockEnable" tier:"critical" scope:"device"`
	SecureBootEnable          bool   `json:"secureBootEnable" tier:"critical" scope:"device"`
	SecureBootPrompt          bool   `json:"secureBootPrompt" tier:"critical" scope:"device"`
	FirmwarePasswordLock      bool   `json:"firmwarePasswordLock" tier:"critical" scope:"device"`
	EndpointManagedDevice     bool   `json:"endpointManagedDevice" tier:"critical" scope:"device"`
	JODSEndpoint              string `json:"jodsEndpoint" tier:"critical" scope:"device"`
	JODSPolicySigningKey      string `json:"jodsPolicySigningPublicKey" tier:"critical" scope:"device"`
	JODSRecoverySigningKey    string `json:"jodsRecoveryCommandSigningPublicKey" tier:"critical" scope:"device"`
	JODSEnrollmentMode        string `json:"jodsEnrollmentMode" tier:"critical" scope:"device"`
	JODSAllowInsecureTLS      bool   `json:"jodsAllowInsecureTls" tier:"critical" scope:"device"`
	JODSDeviceClass           string `json:"jodsDeviceClass" tier:"critical" scope:"device"`
	JODSDesktopProfile        string `json:"jodsDesktopProfile" tier:"critical" scope:"device"`
	JODSFingerprintEnroll     bool   `json:"jodsFingerprintEnrollmentAllowed" tier:"critical" scope:"device"`
	AllowUnvalidatedODDCModel bool   `json:"allowUnvalidatedODDCModel" tier:"critical" scope:"device"`
	DeveloperMode             bool   `json:"developerMode" tier:"critical" scope:"device"`
	// SettingsSigningKey is the main user's own OpenSSH public key for
	// settings files (ROADMAP "Pairing and own keys"); empty means
	// GjallarOS manages that user's signing key.
	SettingsSigningKey string `json:"settingsSigningKey" tier:"critical" scope:"device"`
	// ReleaseChannel is the branch of the official repository that
	// rebuild --update follows (ROADMAP §11): dev, staging or stable.
	ReleaseChannel    string `json:"releaseChannel" tier:"critical" scope:"device"`
	UnattendedInstall bool   `json:"unattendedInstall" tier:"installer"`
	AutoReboot        bool   `json:"autoReboot" tier:"installer"`
	RunUpdateChecks   bool   `json:"runUpdateChecks" tier:"installer"`
	WriteConfig       bool   `json:"writeConfig" tier:"installer"`
	RunRebuild        bool   `json:"runRebuild" tier:"installer"`
	ForceRedeploy     bool   `json:"forceRedeploy" tier:"installer"`
}

// Defaults is the starting point of every interactive install and the content
// of scripts/installation/user_PresetJSON/default.user.config.json. The file
// is a template for automating many machines; it must never enable anything
// the interactive installer cannot reach. TestDefaultsMatchPresetFile keeps
// the two equal.
func Defaults() (User, error) {
	ids, err := apps.Defaults()
	if err != nil {
		return User{}, err
	}
	return User{
		Hostname:                "gjallarOS",
		Username:                "usernamehere",
		Timezone:                "Europe/Berlin",
		Locale:                  "en_US.UTF-8",
		KeyboardLayout:          "de",
		TouchpadWorkspaceSwipe:  true,
		ClamshellEnable:         true,
		Name:                    "Your Name",
		Email:                   "you@example.com",
		GitHubUsername:          "UsernameHere",
		Shell:                   "zsh",
		Editors:                 []string{"vscodium"},
		Browsers:                []string{"firefox"},
		PreferredEditor:         "vscodium",
		PreferredBrowser:        "firefox",
		WebApplications:         []WebApplicationIntent{},
		Apps:                    ids,
		FirefoxAddons:           apps.FirefoxAddonDefaults(),
		FirefoxStoreAddons:      []apps.FirefoxStoreAddon{},
		Theme:                   "noctalia",
		AIAgentMode:             "workspace",
		RecoveryPartitionEnable: true,
		SecureBootPrompt:        true,
		FirmwarePasswordLock:    true,
		JODSEnrollmentMode:      "manual",
		JODSDeviceClass:         "laptop",
		JODSDesktopProfile:      "headless",
		ReleaseChannel:          DefaultReleaseChannel,
		WriteConfig:             true,
		RunRebuild:              true,
	}, nil
}

// Channel is the release channel the install follows; empty means the
// default.
func (u User) Channel() string {
	if u.ReleaseChannel == "" {
		return DefaultReleaseChannel
	}
	return u.ReleaseChannel
}

// HasApp reports whether the catalogue app id is selected.
func (u User) HasApp(id string) bool {
	return slices.Contains(u.Apps, id)
}

// WriteAtomic persists the confirmed machine-local installer input.
func WriteAtomic(path string, user User) error {
	data, err := json.MarshalIndent(user, "", "  ")
	if err != nil {
		return fmt.Errorf("encode user configuration: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".user.config.json-*")
	if err != nil {
		return fmt.Errorf("create temporary user configuration: %w", err)
	}

	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	// A new file is private; an existing one keeps its mode, so the
	// root-owned system checkout's copy stays readable for the desktop
	// tools. It holds no secrets.
	mode := fs.FileMode(0600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm() &^ 0o022
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("set user configuration permissions: %w", err)
	}

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write user configuration: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync user configuration: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close user configuration: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace user configuration: %w", err)
	}

	return nil
}

var usernamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

// hostnamePattern is the networking.hostName rule of NixOS: one DNS label of
// at most 63 characters, underscores allowed as NixOS does.
var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_-]{0,61}[A-Za-z0-9])?$`)
var usbTrustTPMHandlePattern = regexp.MustCompile(`^0x810[0-9a-fA-F]{5}$`)

// timezonePattern is the shape of an IANA time zone name such as
// Europe/Berlin or Etc/GMT+5: parts of letters, digits, _, + and -, joined
// by /. No part can be . or .., so the name stays inside the zoneinfo tree.
var timezonePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+)*$`)

// localePattern is the shape of a glibc locale name such as en_US.UTF-8,
// sr_RS@latin or C.UTF-8.
var localePattern = regexp.MustCompile(`^(C|POSIX|[a-z]{2,3}(_[A-Z]{2})?)(\.[A-Za-z0-9-]+)?(@[a-z]+)?$`)

// DefaultUSBTrustTPMHandle is where the USB trust broker creates its signing
// key on first boot. It stays clear of the handles systemd and Windows use
// (0x81000001, 0x81000002) and the endorsement range (0x8101xxxx).
const DefaultUSBTrustTPMHandle = "0x81000042"

// MaxUSBTrustLapseDays bounds usbTrustLapseDays to what the Nix option
// accepts. 0 means permanent USB trust never lapses.
const MaxUSBTrustLapseDays = 36500

// DefaultReleaseChannel is the channel of new installs and of configurations
// written before channels existed.
const DefaultReleaseChannel = "stable"

// ReleaseChannels are the branches an install may follow, newest code first.
var ReleaseChannels = []string{"dev", "staging", "stable"}

func Load(path string) (User, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return User{}, fmt.Errorf("read user configuration: %w", err)
	}
	return Parse(contents)
}

// Parse is Load for configuration text that is already read.
func Parse(contents []byte) (User, error) {
	var err error
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(contents, &fields); err == nil {
		for key, id := range map[string]string{"aiEnable": "ai", "containersEnable": "containers", "nemuEnable": "nemu"} {
			if _, old := fields[key]; old {
				return User{}, fmt.Errorf("user configuration: %s was replaced by apps; remove it and list %q in apps when wanted", key, id)
			}
		}
		// Noctalia owns the wallpaper now; older configs still carry the key.
		_, oldBackground := fields["backgroundNormal"]
		delete(fields, "backgroundNormal")
		oldWebApplications := legacyWebApplications(fields)
		if oldWebApplications {
			if err := migrateLegacyWebApplications(fields); err != nil {
				return User{}, fmt.Errorf("user configuration: %w", err)
			}
		}
		if oldBackground || oldWebApplications {
			if contents, err = json.Marshal(fields); err != nil {
				return User{}, fmt.Errorf("parse user configuration: %w", err)
			}
		}
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	var user User
	if err := decoder.Decode(&user); err != nil {
		return User{}, fmt.Errorf("parse user configuration: %w", err)
	}
	// Without apps, the catalogue defaults apply.
	if user.Apps == nil {
		if user.Apps, err = apps.Defaults(); err != nil {
			return User{}, err
		}
	}
	// Missing collections get their defaults, missing add-ons are off.
	user.FirefoxAddons = apps.CompleteFirefoxAddons(user.FirefoxAddons)
	if user.FirefoxStoreAddons == nil {
		user.FirefoxStoreAddons = []apps.FirefoxStoreAddon{}
	}
	if user.ReleaseChannel == "" {
		user.ReleaseChannel = DefaultReleaseChannel
	}
	if err := NormalizeProjectTools(&user); err != nil {
		return User{}, err
	}
	if err := Validate(user); err != nil {
		return User{}, err
	}
	return user, nil
}

func Validate(user User) error {
	if !usernamePattern.MatchString(user.Username) {
		return fmt.Errorf("invalid username: %q", user.Username)
	}
	if user.Hostname == "" || user.Theme == "" || user.Shell == "" {
		return fmt.Errorf("hostname, shell, and theme are required")
	}
	if !hostnamePattern.MatchString(user.Hostname) {
		return fmt.Errorf("invalid hostname: %q; use letters, digits and - (at most 63), starting and ending with a letter or digit", user.Hostname)
	}
	if user.Timezone != "" && (len(user.Timezone) > 64 || !timezonePattern.MatchString(user.Timezone)) {
		return fmt.Errorf("invalid timezone: %q; use an IANA name such as Europe/Berlin or Etc/UTC", user.Timezone)
	}
	if user.Locale != "" && (len(user.Locale) > 64 || !localePattern.MatchString(user.Locale)) {
		return fmt.Errorf("invalid locale: %q; use a locale name such as en_US.UTF-8", user.Locale)
	}
	if len(user.Editors) == 0 || len(user.Browsers) == 0 {
		return fmt.Errorf("at least one editor and browser are required")
	}
	if user.ReleaseChannel != "" && !slices.Contains(ReleaseChannels, user.ReleaseChannel) {
		return fmt.Errorf("invalid releaseChannel: %q; use %s", user.ReleaseChannel, strings.Join(ReleaseChannels, ", "))
	}
	if user.SettingsSigningKey != "" {
		if _, err := sshsig.ParseKey(user.SettingsSigningKey); err != nil {
			return fmt.Errorf("invalid settingsSigningKey: %w", err)
		}
	}
	if user.USBTrustTPMHandle != "" && !usbTrustTPMHandlePattern.MatchString(user.USBTrustTPMHandle) {
		return fmt.Errorf("invalid usbTrustTpmHandle: %q", user.USBTrustTPMHandle)
	}
	if user.USBTrustLapseDays < 0 || user.USBTrustLapseDays > MaxUSBTrustLapseDays {
		return fmt.Errorf("invalid usbTrustLapseDays: %d; use 0 (never) or 1 to %d", user.USBTrustLapseDays, MaxUSBTrustLapseDays)
	}
	if user.USBTrustEnforce {
		if !user.USBGuardEnable {
			return fmt.Errorf("usbTrustEnforce requires usbguardEnable")
		}
		if user.USBTrustTPMHandle == "" {
			return fmt.Errorf("usbTrustEnforce requires usbTrustTpmHandle")
		}
	}
	if err := apps.Validate(user.Apps); err != nil {
		return err
	}
	if err := apps.ValidateFirefoxAddons(user.FirefoxAddons, user.FirefoxStoreAddons); err != nil {
		return err
	}
	if user.HasApp("ai") {
		if user.OverrideAISelection && strings.TrimSpace(user.OverrideModelWith) == "" {
			return fmt.Errorf("overrideModelWith is required when overrideAiSelection is true")
		}
		if !oneOf(user.AIAgentMode, "workspace", "owner-conservative", "owner-full-local") {
			return fmt.Errorf("invalid aiAgentMode: %q", user.AIAgentMode)
		}
		if err := ValidateAIEndpoint(user); err != nil {
			return err
		}
	}
	if err := ValidateJODS(user); err != nil {
		return err
	}
	if err := ValidateProjectTools(user); err != nil {
		return err
	}
	if err := ValidateTailscale(user.Tailscale); err != nil {
		return err
	}

	return nil
}

func NormalizeExternalServiceEndpoint(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("external service endpoint is empty")
	}

	if !strings.Contains(value, "://") {
		value = "http://" + value
	}

	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("malformed external service endpoint %q", raw)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported external service endpoint scheme %q", parsed.Scheme)
	}

	// Project-tool endpoints are canonical base URLs. Keep an explicit
	// scheme and port unchanged, while removing redundant trailing slashes.
	// Remove redundant trailing path slashes for ordinary base URLs.
	// Keep the root slash when a query/fragment is present so endpoints such as
	// http://host:8080/?offline=1&https=0 remain intact.
	if parsed.RawQuery == "" && parsed.Fragment == "" {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
	} else if parsed.Path != "/" {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
	}

	return strings.TrimRight(parsed.String(), "/"), nil
}

func NormalizeProjectTools(user *User) error {
	if user.NextcloudEnable {
		normalized, err := NormalizeExternalServiceEndpoint(
			user.NextcloudHost,
		)
		if err != nil {
			return fmt.Errorf("nextcloudHost: %w", err)
		}

		if !strings.HasPrefix(normalized, "https://") {
			return fmt.Errorf(
				"nextcloudHost: HTTPS is required",
			)
		}

		user.NextcloudHost = normalized
	} else {
		user.NextcloudHost = ""
	}

	return NormalizeWebApplications(user)
}

func ValidateProjectTools(user User) error {
	return NormalizeProjectTools(&user)
}

var jodsPublicKeyPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
var jodsDesktopProfilePattern = regexp.MustCompile(`^[A-Za-z0-9._+:-]{1,64}$`)

func ValidateJODS(user User) error {
	if !user.EndpointManagedDevice {
		if user.JODSFingerprintEnroll {
			return fmt.Errorf("JODS fingerprint enrollment policy requires endpoint management enrollment")
		}
		if user.JODSPrebootLockEnable {
			return fmt.Errorf("JODS preboot locking requires explicit JODS endpoint management enrollment")
		}
		return nil
	}
	if user.DeveloperMode {
		return fmt.Errorf("developerMode builds local Nix code; it is not available on JODS-managed devices")
	}
	parsed, err := url.Parse(user.JODSEndpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("JODS endpoint must be an HTTPS URL without embedded credentials")
	}
	if !jodsPublicKeyPattern.MatchString(user.JODSPolicySigningKey) {
		return fmt.Errorf("JODS policy-signing public key must be exactly 64 hexadecimal characters")
	}
	if !jodsPublicKeyPattern.MatchString(user.JODSRecoverySigningKey) {
		return fmt.Errorf("JODS recovery-command signing public key must be exactly 64 hexadecimal characters")
	}
	if strings.EqualFold(user.JODSPolicySigningKey, user.JODSRecoverySigningKey) {
		return fmt.Errorf("JODS policy and recovery-command signing keys must differ")
	}
	if !oneOf(user.JODSEnrollmentMode, "auto", "manual", "jade-registry-only") {
		return fmt.Errorf("invalid JODS enrollment mode: %q", user.JODSEnrollmentMode)
	}
	if !oneOf(user.JODSDeviceClass, "pc", "vm", "laptop", "kiosk", "workstation") {
		return fmt.Errorf("invalid JODS device class: %q", user.JODSDeviceClass)
	}
	if !jodsDesktopProfilePattern.MatchString(strings.TrimSpace(user.JODSDesktopProfile)) {
		return fmt.Errorf("invalid JODS desktop profile: %q", user.JODSDesktopProfile)
	}
	host := parsed.Hostname()
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return fmt.Errorf("JODS endpoint must not use a loopback address")
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

// ValidateAIEndpoint checks the central Ollama server settings. An empty
// aiEndpoint keeps inference on this device.
func ValidateAIEndpoint(user User) error {
	if user.AIEndpoint == "" {
		return nil
	}
	parsed, err := url.Parse(user.AIEndpoint)
	// The system logs in with a bearer token; over plain http the token and
	// every prompt would cross the network in clear.
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return fmt.Errorf("aiEndpoint must be an https URL with a host (the server needs a token and TLS): %q", user.AIEndpoint)
	}
	if parsed.User != nil || strings.Trim(parsed.Path, "/") != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("aiEndpoint must be scheme://host[:port] only: %q", user.AIEndpoint)
	}
	if !ollamaModelPattern.MatchString(user.AIRemoteModel) {
		return fmt.Errorf("aiRemoteModel is not a valid Ollama model identifier: %q", user.AIRemoteModel)
	}
	if user.AIRemoteContextTokens < 2048 || user.AIRemoteContextTokens > 262144 {
		return fmt.Errorf("aiRemoteContextTokens must be between 2048 and 262144")
	}
	return nil
}

var ollamaModelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*(:[A-Za-z0-9._-]+)?$`)
