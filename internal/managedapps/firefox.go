package managedapps

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/JadeOpenServices/gjallarOS/apps"
)

// Firefox removes add-ons that Firefox installed from its enterprise policy
// in one profile.
type Firefox struct {
	Profile string
}

var profileName = regexp.MustCompile(`[^a-z0-9._-]+`)

// NewFirefox checks that profile is a real directory of the user running
// this, so a run never follows a link into someone else's files.
func NewFirefox(profile string) (*Firefox, error) {
	if !filepath.IsAbs(profile) {
		return nil, fmt.Errorf("profile path must be absolute: %q", profile)
	}
	info, err := os.Lstat(profile)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(st.Uid) != os.Geteuid() {
		return nil, fmt.Errorf("%s is not a profile directory owned by this user", profile)
	}
	return &Firefox{Profile: filepath.Clean(profile)}, nil
}

func (f *Firefox) Ledger() string {
	name := strings.Trim(profileName.ReplaceAllString(strings.ToLower(filepath.Base(f.Profile)), "-"), "-.")
	if len(name) > 40 {
		name = name[:40]
	}
	return "firefox-" + name
}

// Busy reads the profile lock: a link to "<address>:+<pid>" while Firefox
// runs. A lock left by a crash points at a process that is gone.
func (f *Firefox) Busy() (bool, error) {
	target, err := os.Readlink(filepath.Join(f.Profile, "lock"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read Firefox profile lock: %w", err)
	}
	_, pid, ok := strings.Cut(target, ":+")
	n, err := strconv.Atoi(pid)
	if !ok || err != nil || n <= 0 {
		// Unknown lock format: wait rather than pull files from under Firefox.
		return true, nil
	}
	_, err = os.Stat(filepath.Join("/proc", strconv.Itoa(n)))
	return err == nil, nil
}

type extensionsJSON struct {
	Addons []struct {
		ID        string `json:"id"`
		Location  string `json:"location"`
		Telemetry struct {
			Source string `json:"source"`
		} `json:"installTelemetryInfo"`
	} `json:"addons"`
}

// Remove deletes <profile>/extensions/<id>.xpi when Firefox lists the add-on
// in the profile as installed by enterprise policy. Firefox drops a missing
// add-on on its next start.
func (f *Firefox) Remove(id string) (bool, error) {
	if !apps.ValidStoreAddonID(id) {
		return false, fmt.Errorf("%q is not an add-on ID", id)
	}
	ours, err := f.installedByPolicy(id)
	if err != nil || !ours {
		return false, err
	}
	path := filepath.Join(f.Profile, "extensions", id+".xpi")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a plain file; left as is", path)
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}

func (f *Firefox) installedByPolicy(id string) (bool, error) {
	file, err := os.Open(filepath.Join(f.Profile, "extensions.json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	var list extensionsJSON
	if err := json.NewDecoder(io.LimitReader(file, 64<<20)).Decode(&list); err != nil {
		return false, fmt.Errorf("read Firefox extensions.json: %w", err)
	}
	for _, a := range list.Addons {
		if a.ID == id && a.Location == "app-profile" {
			return a.Telemetry.Source == "enterprise-policy", nil
		}
	}
	return false, nil
}
