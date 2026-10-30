package winesandbox

import (
	"path/filepath"
	"strconv"
)

// Sandbox describes one program's start inside bubblewrap.
type Sandbox struct {
	// Bwrap is the bubblewrap binary.
	Bwrap string
	// Self is this launcher's binary; it runs inside the sandbox to turn
	// Landlock on before Wine starts, and keeps the sandbox until Wine
	// has ended.
	Self string
	// Wine is the wine binary.
	Wine string

	UID, GID int
	User     string
	Home     string
	// RuntimeDir is XDG_RUNTIME_DIR, inside and out.
	RuntimeDir string
	// Wayland is the security context socket on the host. Inside it is
	// the only Wayland socket, named wayland-0.
	Wayland string
	// ResolvConf is a file naming the namespace's DNS address.
	ResolvConf string

	// Prefix is the program's own Wine prefix.
	Prefix string
	// Files is the program's files folder, the only place it shares with
	// the rest of the device.
	Files string

	// ID names the program to the compositor and in the window class.
	ID string
}

// wineDLLOverrides matches apps/wine/home.nix, and stops Wine from writing
// menu entries.
const wineDLLOverrides = "winhttp=n,b;d3dcompiler_47=n,b;d3dcompiler_43=n,b;dxgi=n,b;d3d11=n,b;d3d10core=n,b;d3d9=n,b;winemenubuilder.exe=d"

// Args is the bwrap command line that starts args (a Windows program and
// its arguments) under Wine. The root holds only the Nix store, the
// system's shared files, the prefix and the files folder. No X11, D-Bus,
// sound or other sockets are bound; the network is the namespace bwrap is
// started in.
func (s Sandbox) Args(args []string) []string {
	wayland := filepath.Join(s.RuntimeDir, "wayland-0")
	out := []string{
		s.Bwrap,
		"--ro-bind", "/nix/store", "/nix/store",
		"--ro-bind", "/run/current-system", "/run/current-system",
		"--ro-bind-try", "/run/opengl-driver", "/run/opengl-driver",
		"--ro-bind", "/etc/static", "/etc/static",
		"--ro-bind", "/etc/fonts", "/etc/fonts",
		"--ro-bind", "/etc/ssl", "/etc/ssl",
		"--ro-bind-try", "/etc/localtime", "/etc/localtime",
		"--ro-bind-try", "/etc/zoneinfo", "/etc/zoneinfo",
		"--ro-bind", s.ResolvConf, "/etc/resolv.conf",
		"--dev", "/dev",
		"--dev-bind-try", "/dev/dri", "/dev/dri",
		// Mesa finds the GPU through these, as in Flatpak.
		"--ro-bind-try", "/sys/dev/char", "/sys/dev/char",
		"--ro-bind-try", "/sys/devices", "/sys/devices",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--dir", s.Home,
		"--perms", "0700", "--dir", s.RuntimeDir,
		"--ro-bind", s.Wayland, wayland,
		"--bind", s.Prefix, s.Prefix,
		"--bind", s.Files, s.Files,
		"--chdir", s.Files,

		"--clearenv",
		"--setenv", "HOME", s.Home,
		"--setenv", "USER", s.User,
		"--setenv", "PATH", "/run/current-system/sw/bin",
		"--setenv", "LANG", "C.UTF-8",
		"--setenv", "XDG_RUNTIME_DIR", s.RuntimeDir,
		"--setenv", "WAYLAND_DISPLAY", "wayland-0",
		"--setenv", "WINEPREFIX", s.Prefix,
		"--setenv", "WINEARCH", "win64",
		"--setenv", "WINEESYNC", "1",
		"--setenv", "WINEFSYNC", "1",
		"--setenv", "DXVK_LOG_LEVEL", "none",
		"--setenv", "DXVK_ASYNC", "0",
		"--setenv", "DXVK_ENABLE_NVAPI", "0",
		"--setenv", "DXVK_FRAME_RATE", "0",
		"--setenv", "WINE_FULLSCREEN_FSR", "0",
		"--setenv", "DXVK_STATE_CACHE", "1",
		"--setenv", "DXVK_STATE_CACHE_PATH", filepath.Join(s.Prefix, "dxvk-cache"),
		"--setenv", "STAGING_SHARED_MEMORY", "1",
		"--setenv", "WINE_LARGE_ADDRESS_AWARE", "1",
		"--setenv", "VKD3D_FEATURE_LEVEL", "12_1",
		"--setenv", "WPF_DISABLE_HARDWARE_ACCELERATION", "1",
		"--setenv", "WINEDEBUG", "-fixme",
		"--setenv", "WINEDLLOVERRIDES", wineDLLOverrides,

		"--unshare-user", "--uid", strconv.Itoa(s.UID), "--gid", strconv.Itoa(s.GID),
		"--disable-userns",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try",
		"--hostname", s.ID,
		"--new-session", "--die-with-parent",
		"--cap-drop", "ALL",
		"--",
		// Symbolic links only in the prefix and /tmp: one in the files
		// folder could point other programs at files outside.
		s.Self, "lock", "--sym", s.Prefix, "--sym", "/tmp", "--refer", "/", "--",
		// The sandbox ends with this first program, so it waits for all of
		// Wine to end.
		s.Self, "stay", s.Wine,
	}
	return append(out, args...)
}
