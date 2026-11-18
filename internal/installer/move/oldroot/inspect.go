package oldroot

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
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
	// User is the old system's user.config.json after the installer's own
	// schema check, with the installer keys of a fresh install (L65). It
	// leaves the helper only through the config operation.
	User config.User `json:"-"`
}

// subvolumes are the btrfs subvolumes a gjallarOS install creates on the
// top level (rootprovision): / , /home, /nix and /etc/gjallar/system.
var subvolumes = []string{"@", "@home", "@nix", "@config"}

// maxUserConfig caps user.config.json. Real ones are a few KiB; the
// installer reads the helper's answer with a 64 KiB cap.
const maxUserConfig = 32 << 10

// maxConfigString caps every key and string value in user.config.json.
// Validate checks the shape of most strings, not all of their lengths.
const maxConfigString = 4096

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
	user, err := UserConfig(top)
	if err != nil {
		return info, err
	}
	info.User = user
	info.Username = user.Username
	info.Channel = user.Channel()
	st, err := top.Lstat("@nix/var/nix/profiles/system")
	if err != nil {
		return info, refuse(NoEvidence, "no system profile")
	}
	info.LastUpdated = time.Unix(st.Mtim.Unix()).UTC()
	return info, nil
}

var errLongString = errors.New("a key or string is too long")

// UserConfig reads @config/user.config.json from the old disk's top level
// and checks it like installer input (L65): size-capped, no link, pipe or
// device, every string capped, then config.Parse (unknown and renamed keys
// refused, Validate). The installer-tier keys come from config.Defaults, not
// from the old disk. A managed install is refused as Managed even when the
// rest of its file would not pass.
func UserConfig(top *safefs.Dir) (config.User, error) {
	raw, err := top.ReadFile("@config/user.config.json", maxUserConfig)
	if err != nil {
		return config.User{}, refuse(NoEvidence, "user.config.json cannot be read")
	}
	if !json.Valid(raw) {
		return config.User{}, refuse(NoEvidence, "user.config.json is not valid")
	}
	if err := checkStrings(raw); err != nil {
		return config.User{}, refuse(BadConfig, "user.config.json: "+err.Error())
	}
	var managed struct {
		Management struct {
			Enable bool `json:"enable"`
		} `json:"management"`
	}
	// A wrong type here is left to Parse.
	if json.Unmarshal(raw, &managed) == nil && managed.Management.Enable {
		return config.User{}, refuse(Managed, "management.enable is on")
	}
	user, err := config.Parse(raw)
	if err != nil {
		// The parse error can quote values from the file; the log on the
		// stick gets a fixed text.
		if !config.ValidUsername(usernameOf(raw)) {
			return config.User{}, refuse(NoEvidence, "user.config.json has no valid username")
		}
		return config.User{}, refuse(BadConfig, "user.config.json does not pass the schema check")
	}
	if user.Management.Enable {
		return config.User{}, refuse(Managed, "management.enable is on")
	}
	defaults, err := config.Defaults()
	if err != nil {
		return config.User{}, err
	}
	return config.WithInstallerKeysFrom(user, defaults), nil
}

// usernameOf is the username in raw, or "" when there is none.
func usernameOf(raw []byte) string {
	var u struct {
		Username string `json:"username"`
	}
	if json.Unmarshal(raw, &u) != nil {
		return ""
	}
	return u.Username
}

// checkStrings refuses a key or string value longer than maxConfigString.
func checkStrings(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return errors.New("not valid JSON")
		}
		if s, ok := tok.(string); ok && len(s) > maxConfigString {
			return errLongString
		}
	}
}
