package winesandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Every app's prefix starts as a copy of one base prefix that Wine's setup
// made once. The copy then sees only its own C: drive and its files folder
// as D:, and its user folders lead into the files folder.

// BasePrefix is the prefix new apps are copied from.
func BasePrefix(home string) string {
	return filepath.Join(home, ".local", "share", "gjallar-wine", "base")
}

// InitMarker is the file Wine's setup leaves in a prefix it finished.
const InitMarker = ".gjallar-wine-initialized"

// userFolders are the user's folders that Wine links to the home's own
// folders. In an app they lead into its files folder instead.
var userFolders = []string{"Documents", "Downloads", "Music", "Pictures", "Videos"}

// FixPrefix turns a copied base prefix into app prefix's own: drives C:
// and D: only, and the user folders of user in the files folder. Desktop
// becomes a plain folder, so shortcuts an installer puts there stay in
// the prefix.
func FixPrefix(prefix, files, user string) error {
	devices := filepath.Join(prefix, "dosdevices")
	entries, err := os.ReadDir(devices)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(devices, entry.Name())); err != nil {
			return err
		}
	}
	if err := os.Symlink("../drive_c", filepath.Join(devices, "c:")); err != nil {
		return err
	}
	if err := os.Symlink(files, filepath.Join(devices, "d:")); err != nil {
		return err
	}

	userDir := filepath.Join(prefix, "drive_c", "users", user)
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		return err
	}
	for _, name := range append(userFolders, "Desktop") {
		path := filepath.Join(userDir, name)
		info, err := os.Lstat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return err
		case info.Mode()&os.ModeSymlink != 0:
			if err := os.Remove(path); err != nil {
				return err
			}
		case info.IsDir():
			// A folder Wine made itself: keep what is in it.
			continue
		default:
			return fmt.Errorf("%s is not a folder", path)
		}
		if name == "Desktop" {
			if err := os.Mkdir(path, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.Symlink(files, path); err != nil {
			return err
		}
	}
	return nil
}

// WinPath gives the Wine path of host file path, which must lie in the
// prefix's C: drive or in the files folder (D:). Both roots are without
// symbolic links, and path is resolved first.
func WinPath(paths Paths, path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	for _, root := range []struct{ dir, drive string }{
		{filepath.Join(paths.Prefix, "drive_c"), `C:\`},
		{paths.Files, `D:\`},
	} {
		rel, err := filepath.Rel(root.dir, resolved)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		return root.drive + strings.ReplaceAll(rel, "/", `\`), nil
	}
	return "", fmt.Errorf("%s is neither in the app's C: drive nor in %s", path, paths.Files)
}

// HostPath gives the host file a Wine path on C: or D: names. It refuses
// other drives and paths that climb out with "..".
func HostPath(paths Paths, win string) (string, error) {
	if len(win) < 3 || win[1] != ':' || win[2] != '\\' {
		return "", fmt.Errorf("%q is not a path on C: or D:", win)
	}
	var root string
	switch win[0] {
	case 'c', 'C':
		root = filepath.Join(paths.Prefix, "drive_c")
	case 'd', 'D':
		root = paths.Files
	default:
		return "", fmt.Errorf("%q is not a path on C: or D:", win)
	}
	parts := strings.Split(win[3:], `\`)
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "/\x00") {
			return "", fmt.Errorf("%q is not a plain path", win)
		}
	}
	return filepath.Join(append([]string{root}, parts...)...), nil
}
