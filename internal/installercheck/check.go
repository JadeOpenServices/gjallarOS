// Package installercheck validates repository inputs without changing the host.
package installercheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/apps"
	"github.com/JadeOpenServices/gjallarOS/internal/hardware/graphics"
)

type Level string

const (
	Error Level = "FAIL"
	Warn  Level = "WARN"
	OK    Level = "PASS"
)

type Finding struct {
	Level   Level
	Message string
}

type Report struct{ Findings []Finding }

func (r Report) Failed() bool {
	for _, finding := range r.Findings {
		if finding.Level == Error {
			return true
		}
	}
	return false
}

// ResolveRepository resolves a supplied directory once and requires the two
// repository markers used by the installer. This prevents checks from reading
// arbitrary parent directories after a typo or symlink traversal.
func ResolveRepository(path string) (string, error) {
	if path == "" {
		return "", errors.New("repository path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve repository symlinks: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("repository is not a directory: %s", root)
	}
	for _, marker := range []string{"flake.nix", "scripts/installation/install.sh"} {
		info, err := os.Stat(filepath.Join(root, marker))
		if err != nil || info.IsDir() {
			return "", fmt.Errorf("not a GjallarOS repository (missing %s)", marker)
		}
	}
	return root, nil
}

func Preflight(root string) Report {
	r := Report{}
	preset, presetOK := readJSONObject(&r, filepath.Join(root, "scripts/installation/user_PresetJSON/default.user.config.json"), "default installer preset")
	user, userOK := readJSONObject(&r, filepath.Join(root, "user.config.json"), "user.config.json")
	if presetOK && userOK {
		checkPresetCompatibility(&r, preset, user)
		validatePresetSchema(&r, user)
	}

	checkRequiredFiles(&r, root)
	checkRegistration(&r, root)
	checkFlakeInputWiring(&r, root)
	checkModuleWiring(&r, root)
	return r
}

func Check(ctx context.Context, root string) Report {
	r := Preflight(root)
	checkNativeGraphics(ctx, &r, root)
	return r
}

type presetValueType uint8

const (
	presetString presetValueType = iota
	presetBool
	presetStringList
	presetWebApplicationList
	presetTailscale
	presetStringMap
	presetInt
	presetFirefoxAddons
	presetFirefoxStoreAddons
)

var presetSchema = map[string]presetValueType{
	"hostname": presetString, "username": presetString,
	"timezone": presetString, "locale": presetString, "keyboardLayout": presetString, "keyboardVariant": presetString,
	"touchpadWorkspaceSwipe": presetBool, "clamshellEnable": presetBool, "consoleLoginEnable": presetBool, "usbguardEnable": presetBool, "usbTrustEnforce": presetBool, "usbTrustTpmHandle": presetString, "usbTrustLapseDays": presetInt, "snapshotsEnable": presetBool, "snapshotsKeepHours": presetInt, "snapshotsKeepDays": presetInt, "usbReviewTechnicalView": presetBool, "printingEnable": presetBool, "networkPrintingEnable": presetBool, "firewallEnable": presetBool, "virusScanEnable": presetBool,
	"dolphinDriveUsage":         presetBool,
	"firmwareFlashReadEntry":    presetBool,
	"allowUnvalidatedODDCModel": presetBool,
	"developerMode":             presetBool,
	"settingsSigningKey":        presetString,
	"unattendedInstall":         presetBool,
	"name":                      presetString, "email": presetString, "githubUsername": presetString, "dotfilesDir": presetString,
	"shell": presetString, "editors": presetStringList, "browsers": presetStringList,
	"preferredEditor": presetString, "preferredBrowser": presetString, "theme": presetString,
	"weatherCity": presetString, "weatherCountry": presetString,
	"webApplications":    presetWebApplicationList,
	"firefoxAddons":      presetFirefoxAddons,
	"firefoxStoreAddons": presetFirefoxStoreAddons,
	"tailscale":          presetTailscale,
	"planeEnable":        presetBool, "planeHost": presetString, "drawioEnable": presetBool, "drawioSelfHosted": presetBool, "drawioHost": presetString, "nextcloudEnable": presetBool, "nextcloudHost": presetString,
	"apps": presetStringList, "thunderbirdSmartCard": presetBool, "debugFunctions": presetBool,
	"overrideAiSelection": presetBool, "overrideModelWith": presetString, "aiAgentMode": presetString,
	"aiEndpoint": presetString, "aiRemoteModel": presetString, "aiRemoteContextTokens": presetInt,
	"jodsFingerprintEnrollmentAllowed": presetBool,
	"luksTpm2Enable":                   presetBool,
	"luksTpm2Pin":                      presetBool,
	"recoveryEnable":                   presetBool,
	"recoveryPartitionEnable":          presetBool, "jodsPrebootLockEnable": presetBool,
	"secureBootEnable": presetBool, "secureBootPrompt": presetBool, "firmwarePasswordLock": presetBool, "endpointManagedDevice": presetBool,
	"jodsEndpoint": presetString, "jodsPolicySigningPublicKey": presetString,
	"jodsRecoveryCommandSigningPublicKey": presetString, "jodsEnrollmentMode": presetString,
	"jodsAllowInsecureTls": presetBool, "jodsDeviceClass": presetString, "jodsDesktopProfile": presetString, "releaseChannel": presetString,
	"autoReboot": presetBool, "runUpdateChecks": presetBool, "writeConfig": presetBool, "runRebuild": presetBool, "forceRedeploy": presetBool,
}

func validatePresetSchema(r *Report, user map[string]json.RawMessage) {
	for key, raw := range user {
		if installerManagedFields[key] {
			r.Findings = append(r.Findings, Finding{Warn, fmt.Sprintf("%s is installer-managed and should be removed from user.config.json", key)})
			continue
		}
		expected, known := presetSchema[key]
		if !known {
			r.Findings = append(r.Findings, Finding{Warn, fmt.Sprintf("unknown user.config.json field: %s", key)})
			continue
		}
		if !matchesPresetType(raw, expected) {
			r.Findings = append(r.Findings, Finding{Error, fmt.Sprintf("user.config.json field %s has the wrong type", key)})
		}
	}
}

func matchesPresetType(raw json.RawMessage, expected presetValueType) bool {
	switch expected {
	case presetString:
		var value string
		return json.Unmarshal(raw, &value) == nil
	case presetBool:
		var value bool
		return json.Unmarshal(raw, &value) == nil
	case presetStringList:
		var value []string
		return json.Unmarshal(raw, &value) == nil
	case presetWebApplicationList:
		return matchesPresetWebApplicationList(raw)
	case presetTailscale:
		return matchesPresetTailscale(raw)
	case presetStringMap:
		var value map[string]string
		return json.Unmarshal(raw, &value) == nil
	case presetInt:
		var value int
		return json.Unmarshal(raw, &value) == nil
	case presetFirefoxAddons:
		var value map[string]apps.FirefoxCollectionChoice
		return json.Unmarshal(raw, &value) == nil
	case presetFirefoxStoreAddons:
		var value []apps.FirefoxStoreAddon
		return json.Unmarshal(raw, &value) == nil
	default:
		return false
	}
}

func checkNativeGraphics(ctx context.Context, r *Report, root string) {
	live, err := graphics.Detect(ctx)
	if err != nil {
		r.Findings = append(r.Findings, Finding{Warn, fmt.Sprintf("native graphics detection unavailable: %v", err)})
		return
	}
	r.Findings = append(r.Findings, Finding{OK, fmt.Sprintf("native graphics detection: %s (%s), %s", live.Vendor, live.Type, live.BusID)})

	generated, err := readGraphicsSettings(filepath.Join(root, "generated", "state.nix"))
	if err != nil {
		r.Findings = append(r.Findings, Finding{Warn, fmt.Sprintf("generated graphics topology unavailable: %v", err)})
		return
	}
	for key, actual := range map[string]string{
		"graphicsBusId":           live.BusID,
		"graphicsIntegratedBusId": live.IntegratedBusID,
	} {
		if generated[key] != actual {
			r.Findings = append(r.Findings, Finding{Warn, fmt.Sprintf("generated %s is %q; live detection is %q; rerun the installer to refresh machine topology", key, generated[key], actual)})
			return
		}
	}
	r.Findings = append(r.Findings, Finding{OK, "generated graphics topology matches native detection; portable graphics facts are ODDC-owned"})
}

var graphicsSetting = regexp.MustCompile(`(?m)^\s*(graphicsBusId|graphicsIntegratedBusId)\s*=\s*"([^"]*)";`)

func readGraphicsSettings(path string) (map[string]string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	settings := make(map[string]string)
	for _, match := range graphicsSetting.FindAllStringSubmatch(string(contents), -1) {
		settings[match[1]] = match[2]
	}
	if len(settings) != 2 {
		return nil, errors.New("required graphics fields are missing")
	}
	return settings, nil
}

func readJSONObject(r *Report, path, label string) (map[string]json.RawMessage, bool) {
	contents, err := os.ReadFile(path)
	if err != nil {
		r.Findings = append(r.Findings, Finding{Error, fmt.Sprintf("%s unreadable: %v", label, err)})
		return nil, false
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	if err := decoder.Decode(&object); err != nil || object == nil {
		if err == nil {
			err = errors.New("top-level value must be an object")
		}
		r.Findings = append(r.Findings, Finding{Error, fmt.Sprintf("%s is invalid JSON: %v", label, err)})
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = errors.New("additional JSON value")
		}
		r.Findings = append(r.Findings, Finding{Error, fmt.Sprintf("%s contains trailing JSON values", label)})
		return nil, false
	}
	r.Findings = append(r.Findings, Finding{OK, fmt.Sprintf("%s is valid JSON", label)})
	return object, true
}

func checkPresetCompatibility(r *Report, preset, user map[string]json.RawMessage) {
	missing := make([]string, 0)
	managed := make([]string, 0)
	for key := range preset {
		if _, exists := user[key]; !exists {
			if installerManagedFields[key] {
				managed = append(managed, key)
			} else {
				missing = append(missing, key)
			}
		}
	}
	sort.Strings(missing)
	sort.Strings(managed)
	if len(missing) == 0 {
		r.Findings = append(r.Findings, Finding{OK, "user.config.json explicitly sets every preset field"})
	} else {
		// Fields added after this machine was installed. Nix falls back to the
		// same defaults the installer writes, so this is not a problem.
		r.Findings = append(r.Findings, Finding{OK, fmt.Sprintf("preset fields not in user.config.json use their defaults: %s", strings.Join(missing, ", "))})
	}
	if len(managed) != 0 {
		r.Findings = append(r.Findings, Finding{OK, fmt.Sprintf("installer-managed fields are intentionally absent: %s", strings.Join(managed, ", "))})
	}
}

// These values are derived from hardware discovery or ODDC resolution. They
// are installer-owned and must never be accepted as user intent.
var installerManagedFields = map[string]bool{
	"aiModel":                 true,
	"aiAccelerationProfile":   true,
	"aiContextTokens":         true,
	"aiVramMB":                true,
	"deviceBoardName":         true,
	"deviceBoardVendor":       true,
	"deviceBoardVersion":      true,
	"deviceProductName":       true,
	"deviceProductVersion":    true,
	"deviceSysVendor":         true,
	"graphicsBusId":           true,
	"graphicsCompute":         true,
	"graphicsDeviceId":        true,
	"graphicsDriverBranch":    true,
	"graphicsIntegratedBusId": true,
	"graphicsType":            true,
	"graphicsVendor":          true,
	"oddcModel":               true,
	"orientationSensorEnable": true,
	"penTabletEnable":         true,
	"touchscreenEnable":       true,
	"wifiDriver":              true,
}

func checkRequiredFiles(r *Report, root string) {
	for _, relative := range []string{
		"scripts/installation/install.sh",
		"cmd/gjallar-installer/main.go",
		"internal/installer/app/app.go",
		"generated/state.nix",
		"generated/hardware.nix",
		"generated/install-state.nix",
		"system/default.nix",
		"apps/ai/nixos.nix",
		"system/tools/commands/default.nix",
		"user/default.nix",
	} {
		if info, err := os.Stat(filepath.Join(root, relative)); err != nil || info.IsDir() {
			r.Findings = append(r.Findings, Finding{Error, fmt.Sprintf("required file missing: %s", relative)})
		} else {
			r.Findings = append(r.Findings, Finding{OK, fmt.Sprintf("required file present: %s", relative)})
		}
	}
}

func checkRegistration(r *Report, root string) {
	for _, relative := range []string{"system/tools/commands/default.nix"} {
		contents, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			continue
		}
		if strings.Contains(string(contents), "check-installer") {
			r.Findings = append(r.Findings, Finding{OK, fmt.Sprintf("installer checker registered in %s", relative)})
		} else {
			r.Findings = append(r.Findings, Finding{Warn, fmt.Sprintf("installer checker not registered in %s", relative)})
		}
	}
}

var flakeInputDeclaration = regexp.MustCompile(`(?m)^    ([A-Za-z0-9][A-Za-z0-9_-]*)(?:\.url)?\s*=`)
var nixLiteralPath = regexp.MustCompile(`(?:^|[[:space:]\[\(\{=])((?:\./|\.\./)[A-Za-z0-9_./-]+)`)

func checkFlakeInputWiring(r *Report, root string) {
	path := filepath.Join(root, "flake.nix")
	contents, err := os.ReadFile(path)
	if err != nil {
		r.Findings = append(r.Findings, Finding{Error, fmt.Sprintf("flake.nix unreadable: %v", err)})
		return
	}

	parts := strings.SplitN(string(contents), "outputs =", 2)
	if len(parts) != 2 {
		r.Findings = append(r.Findings, Finding{Error, "flake.nix has no outputs declaration"})
		return
	}

	seen := make(map[string]bool)
	inputs := make([]string, 0)
	for _, match := range flakeInputDeclaration.FindAllStringSubmatch(parts[0], -1) {
		name := match[1]
		if name == "inputs" || seen[name] {
			continue
		}
		seen[name] = true
		inputs = append(inputs, name)
	}
	sort.Strings(inputs)

	usedByInputs := make(map[string]bool)
	for _, name := range inputs {
		if strings.Contains(parts[1], name) {
			usedByInputs[name] = true
		}
	}

	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			base := entry.Name()
			if base == ".git" || base == ".direnv" || base == "node_modules" {
				return filepath.SkipDir
			}
			if path == filepath.Join(root, "pkgs", "monique") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".nix" || path == filepath.Join(root, "flake.nix") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(data)
		for _, name := range inputs {
			if strings.Contains(text, "inputs."+name) {
				usedByInputs[name] = true
			}
		}
		return nil
	})

	unused := make([]string, 0)
	for _, name := range inputs {
		if !usedByInputs[name] {
			unused = append(unused, name)
		}
	}

	if len(unused) == 0 {
		r.Findings = append(r.Findings, Finding{OK, fmt.Sprintf("flake input wiring: %d inputs are referenced", len(inputs))})
		return
	}
	for _, name := range unused {
		r.Findings = append(r.Findings, Finding{Warn, fmt.Sprintf("unused flake input: %s", name)})
	}
}

func checkModuleWiring(r *Report, root string) {
	wired := make(map[string]bool)
	sources := make([]string, 0)

	for _, relative := range []string{"flake.nix", "system", "user"} {
		path := filepath.Join(root, relative)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			sources = append(sources, path)
			continue
		}
		_ = filepath.WalkDir(path, func(source string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			if filepath.Ext(source) == ".nix" {
				sources = append(sources, source)
			}
			return nil
		})
	}

	for _, source := range sources {
		contents, err := os.ReadFile(source)
		if err != nil {
			continue
		}
		text := string(contents)

		// Dynamic sibling imports such as (./. + "/${settings.theme}.nix")
		// intentionally select one module from the current directory.
		if strings.Contains(text, `./. + "/${`) {
			entries, err := os.ReadDir(filepath.Dir(source))
			if err == nil {
				for _, entry := range entries {
					if !entry.IsDir() && filepath.Ext(entry.Name()) == ".nix" {
						wired[filepath.Join(filepath.Dir(source), entry.Name())] = true
					}
				}
			}
		}

		for _, match := range nixLiteralPath.FindAllStringSubmatch(text, -1) {
			resolved := filepath.Clean(filepath.Join(filepath.Dir(source), match[1]))
			info, err := os.Stat(resolved)
			if err != nil {
				continue
			}
			if !info.IsDir() {
				if filepath.Ext(resolved) == ".nix" {
					wired[resolved] = true
				}
				continue
			}

			defaultModule := filepath.Join(resolved, "default.nix")
			if info, err := os.Stat(defaultModule); err == nil && !info.IsDir() {
				wired[defaultModule] = true
				continue
			}

			// Only recurse through a literal directory when the source explicitly
			// consumes it with listFilesRecursive. Dynamic imports such as
			// ./wm/${wm} must not accidentally mark an entire tree as wired.
			if !strings.Contains(text, "listFilesRecursive "+match[1]) {
				continue
			}
			_ = filepath.WalkDir(resolved, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr == nil && !entry.IsDir() && filepath.Ext(path) == ".nix" {
					wired[path] = true
				}
				return nil
			})
		}
	}

	candidateRoots := []string{
		"system/compat",
		"system/maintenance",
		"system/management",
		"system/security",
		"system/services",
		"user/apps",
		"user/services",
	}

	unwired := make([]string, 0)
	for _, relativeRoot := range candidateRoots {
		candidateRoot := filepath.Join(root, relativeRoot)
		_ = filepath.WalkDir(candidateRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.IsDir() || filepath.Ext(path) != ".nix" || wired[path] {
				return nil
			}

			data, err := os.ReadFile(path)
			if err == nil && strings.Contains(string(data), "# gjallar: dormant-module") {
				return nil
			}

			relative, err := filepath.Rel(root, path)
			if err == nil {
				unwired = append(unwired, filepath.ToSlash(relative))
			}
			return nil
		})
	}
	sort.Strings(unwired)

	if len(unwired) == 0 {
		r.Findings = append(r.Findings, Finding{OK, "module wiring: no orphan candidates in owned module roots"})
		return
	}
	for _, relative := range unwired {
		r.Findings = append(r.Findings, Finding{Warn, fmt.Sprintf("likely unwired Nix module: %s", relative)})
	}
}
