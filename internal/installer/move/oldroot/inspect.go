package oldroot

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
	"golang.org/x/sys/unix"
)

// Info is what step 5 shows about the old system.
type Info struct {
	// Channel is the release channel the old system follows.
	Channel string `json:"channel"`
	// LastUpdated is when the old system last switched to a new build.
	LastUpdated time.Time `json:"lastUpdated"`
	// Username is the account of the old system. It stays inside the
	// helper: the identity inventory needs it to find the home folder.
	Username string `json:"-"`
}

// subvolumes are the btrfs subvolumes a gjallarOS install creates on the
// top level (rootprovision): / , /home, /nix and /etc/gjallar/system.
var subvolumes = []string{"@", "@home", "@nix", "@config"}

// maxUserConfig caps user.config.json.
const maxUserConfig = 1 << 20

// Inspect checks that the mounted top level holds a complete gjallarOS with
// the current layout and reads what the screen shows. A managed install is
// refused: its organisation moves it. Nothing on the old disk is run or
// followed; links are only looked at.
func Inspect(top *safefs.Dir) (Info, error) {
	for _, sv := range subvolumes {
		st, err := top.Lstat(sv)
		if err != nil {
			return Info{}, refuse(NoEvidence, "subvolume "+sv+" is missing")
		}
		// 256 is the inode number of the root of every btrfs subvolume.
		if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Ino != 256 {
			return Info{}, refuse(NoEvidence, sv+" is not a subvolume")
		}
	}
	return inspectFiles(top)
}

// inspectFiles is Inspect after the subvolume check. Managed means
// management.enable in user.config.json, nothing else: installation-complete
// is also written by the Secure Boot setup of an unmanaged install.
func inspectFiles(top *safefs.Dir) (Info, error) {
	var info Info
	for _, p := range []string{"@/etc/NIXOS", "@/etc/gjallar/repository"} {
		if _, err := top.Lstat(p); err != nil {
			return info, refuse(NoEvidence, p+" is missing")
		}
	}
	raw, err := top.ReadFile("@config/user.config.json", maxUserConfig)
	if err != nil {
		return info, refuse(NoEvidence, "user.config.json cannot be read")
	}
	var cfg struct {
		Username       string `json:"username"`
		ReleaseChannel string `json:"releaseChannel"`
		Management     struct {
			Enable bool `json:"enable"`
		} `json:"management"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return info, refuse(NoEvidence, "user.config.json is not valid")
	}
	if cfg.Management.Enable {
		return info, refuse(Managed, "management.enable is on")
	}
	if !config.ValidUsername(cfg.Username) {
		return info, refuse(NoEvidence, "user.config.json has no valid username")
	}
	info.Username = cfg.Username
	info.Channel = config.DefaultReleaseChannel
	if cfg.ReleaseChannel != "" {
		if !slices.Contains(config.ReleaseChannels, cfg.ReleaseChannel) {
			return info, refuse(NoEvidence, "unknown release channel")
		}
		info.Channel = cfg.ReleaseChannel
	}
	st, err := top.Lstat("@nix/var/nix/profiles/system")
	if err != nil {
		return info, refuse(NoEvidence, "no system profile")
	}
	info.LastUpdated = time.Unix(st.Mtim.Unix()).UTC()
	return info, nil
}
