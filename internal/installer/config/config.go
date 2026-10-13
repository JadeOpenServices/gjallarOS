// Package config defines the typed contract between installer input,
// discovery, and generated/state.nix.
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/apps"
)

type User struct {
	Hostname               string                 `json:"hostname"`
	Username               string                 `json:"username"`
	Timezone               string                 `json:"timezone"`
	Locale                 string                 `json:"locale"`
	KeyboardLayout         string                 `json:"keyboardLayout"`
	KeyboardVariant        string                 `json:"keyboardVariant"`
	WeatherCity            string                 `json:"weatherCity"`
	WeatherCountry         string                 `json:"weatherCountry"`
	TouchpadWorkspaceSwipe bool                   `json:"touchpadWorkspaceSwipe"`
	ClamshellEnable        bool                   `json:"clamshellEnable"`
	ConsoleLoginEnable     bool                   `json:"consoleLoginEnable"`
	USBGuardEnable         bool                   `json:"usbguardEnable"`
	USBTrustEnforce        bool                   `json:"usbTrustEnforce"`
	USBTrustTPMHandle      string                 `json:"usbTrustTpmHandle"`
	USBReviewTechnical     bool                   `json:"usbReviewTechnicalView"`
	PrintingEnable         bool                   `json:"printingEnable"`
	NetworkPrintingEnable  bool                   `json:"networkPrintingEnable"`
	DolphinDriveUsage      bool                   `json:"dolphinDriveUsage"`
	FirmwareFlashReadEntry bool                   `json:"firmwareFlashReadEntry"`
	Name                   string                 `json:"name"`
	Email                  string                 `json:"email"`
	GitHubUsername         string                 `json:"githubUsername"`
	DotfilesDir            string                 `json:"dotfilesDir"`
	Shell                  string                 `json:"shell"`
	Editors                []string               `json:"editors"`
	Browsers               []string               `json:"browsers"`
	PreferredEditor        string                 `json:"preferredEditor"`
	PreferredBrowser       string                 `json:"preferredBrowser"`
	WebApplications        []WebApplicationIntent `json:"webApplications"`
	Tailscale              *TailscaleIntent       `json:"tailscale,omitempty"`
	// Apps are the selected catalogue app IDs (apps/<id>/meta.json).
	Apps []string `json:"apps"`
	// FirefoxAddons holds the add-on collections (apps/firefox-addons.json)
	// and each add-on's mode; an add-on only counts while its collection is on.
	FirefoxAddons map[string]apps.FirefoxCollectionChoice `json:"firefoxAddons"`
	// FirefoxStoreAddons are extra add-ons from addons.mozilla.org, each
	// pinned to one file.
	FirefoxStoreAddons []apps.FirefoxStoreAddon `json:"firefoxStoreAddons"`

	// Compatibility fields retained while the installer prompt and generated
	// Nix settings migrate to the generic webApplications contract.
	PlaneEnable               bool   `json:"planeEnable"`
	PlaneHost                 string `json:"planeHost"`
	DrawioEnable              bool   `json:"drawioEnable"`
	DrawioSelfHosted          bool   `json:"drawioSelfHosted"`
	DrawioHost                string `json:"drawioHost"`
	NextcloudEnable           bool   `json:"nextcloudEnable"`
	NextcloudHost             string `json:"nextcloudHost"`
	Theme                     string `json:"theme"`
	DebugFunctions            bool   `json:"debugFunctions"`
	OverrideAISelection       bool   `json:"overrideAiSelection"`
	OverrideModelWith         string `json:"overrideModelWith"`
	AIAgentMode               string `json:"aiAgentMode"`
	AIEndpoint                string `json:"aiEndpoint"`
	AIRemoteModel             string `json:"aiRemoteModel"`
	AIRemoteContextTokens     int    `json:"aiRemoteContextTokens"`
	LUKSTPM2Enable            bool   `json:"luksTpm2Enable"`
	LUKSTPM2PIN               bool   `json:"luksTpm2Pin"`
	RecoveryEnable            bool   `json:"recoveryEnable"`
	RecoveryPartitionEnable   bool   `json:"recoveryPartitionEnable"`
	JODSPrebootLockEnable     bool   `json:"jodsPrebootLockEnable"`
	SecureBootEnable          bool   `json:"secureBootEnable"`
	SecureBootPrompt          bool   `json:"secureBootPrompt"`
	FirmwarePasswordLock      bool   `json:"firmwarePasswordLock"`
	EndpointManagedDevice     bool   `json:"endpointManagedDevice"`
	JODSEndpoint              string `json:"jodsEndpoint"`
	JODSPolicySigningKey      string `json:"jodsPolicySigningPublicKey"`
	JODSRecoverySigningKey    string `json:"jodsRecoveryCommandSigningPublicKey"`
	JODSEnrollmentMode        string `json:"jodsEnrollmentMode"`
	JODSAllowInsecureTLS      bool   `json:"jodsAllowInsecureTls"`
	JODSDeviceClass           string `json:"jodsDeviceClass"`
	JODSDesktopProfile        string `json:"jodsDesktopProfile"`
	JODSFingerprintEnroll     bool   `json:"jodsFingerprintEnrollmentAllowed"`
	AllowUnvalidatedODDCModel bool   `json:"allowUnvalidatedODDCModel"`
	UnattendedInstall         bool   `json:"unattendedInstall"`
	AutoReboot                bool   `json:"autoReboot"`
	RunUpdateChecks           bool   `json:"runUpdateChecks"`
	WriteConfig               bool   `json:"writeConfig"`
	RunRebuild                bool   `json:"runRebuild"`
	ForceRedeploy             bool   `json:"forceRedeploy"`
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
		WriteConfig:             true,
		RunRebuild:              true,
	}, nil
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

	if err := tmp.Chmod(0600); err != nil {
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
var usbTrustTPMHandlePattern = regexp.MustCompile(`^0x810[0-9a-fA-F]{5}$`)

// DefaultUSBTrustTPMHandle is where the USB trust broker creates its signing
// key on first boot. It stays clear of the handles systemd and Windows use
// (0x81000001, 0x81000002) and the endorsement range (0x8101xxxx).
const DefaultUSBTrustTPMHandle = "0x81000042"

func Load(path string) (User, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return User{}, fmt.Errorf("read user configuration: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(contents, &fields); err == nil {
		for key, id := range map[string]string{"aiEnable": "ai", "containersEnable": "containers", "nemuEnable": "nemu"} {
			if _, old := fields[key]; old {
				return User{}, fmt.Errorf("user configuration: %s was replaced by apps; remove it and list %q in apps when wanted", key, id)
			}
		}
		// Noctalia owns the wallpaper now; older configs still carry the key.
		if _, old := fields["backgroundNormal"]; old {
			delete(fields, "backgroundNormal")
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
	if len(user.Editors) == 0 || len(user.Browsers) == 0 {
		return fmt.Errorf("at least one editor and browser are required")
	}
	if user.USBTrustTPMHandle != "" && !usbTrustTPMHandlePattern.MatchString(user.USBTrustTPMHandle) {
		return fmt.Errorf("invalid usbTrustTpmHandle: %q", user.USBTrustTPMHandle)
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
