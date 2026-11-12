# gjallarOS installer

Run this from the repository root on an existing NixOS installation, or on
the NixOS minimal ISO started from a USB stick to install onto a whole disk
(see "New machine: from a USB stick" in the main README):

```bash
./scripts/installation/install.sh
```

The short bootstrapper requires only Nix. It builds and starts
`gjallar-installer` without changing the current shell environment. After one
approval, the Go preflight persistently adds Go, PCI/firmware tools, Git,
HTTPS/GTK helpers, SOPS/Age, password hashing, and disk-encryption tooling to
`/etc/nixos/configuration.nix`, rebuilds, then resumes the installation.

Clone with `git clone --recurse-submodules`. The bootstrapper initializes a
missing Monique submodule before making system changes. When copying a checkout,
include `pkgs/monique`; a Git archive alone omits submodule contents.

On the first run, the installer creates `generated/install-state.nix`, preserving
the existing NixOS and configured Home Manager compatibility versions. A fresh
target starts at the pinned release. Reruns leave these historical baselines intact.

## Preset automation

Copy the preset with:

```bash
cp scripts/installation/user_PresetJSON/default.user.config.json user.config.json
```

Then edit `user.config.json` and run the installer.
The preset answers the non-secret configuration automatically. Hardware
profiling remains dynamic and GPU passthrough IDs are detected automatically.
Remove or rename the file to return to fully interactive mode.

The installer estimates the weather location from the current public network
address and asks the user to confirm it. If rejected or unavailable, it asks
for city and country explicitly. The detected value is never accepted silently.

In the preset, `username` is the Linux account and the single identity input.
`dotfilesDir` is derived automatically as `/home/<username>/Documents/gjallarOS`.
The `name` field is the human-readable Git commit author name; it is not the
Linux username or GitHub username. `githubUsername` is a separate optional
account label.

The wallpaper is picked in the Noctalia control centre and kept in Noctalia's
own state. The repository ships no wallpapers; until one is picked the
background is the theme's base colour.

The installer targets NixOS 26.05 and stops before making changes if another
release is detected. The release/channel policy is kept in
`deployment/release-policy.json` for future centrally managed updates. The root
flake and stable shell flakes track its `nixpkgsInput`; only the explicitly
listed development shells (currently ComfyUI) intentionally use
`unstableNixpkgsInput` for fast-moving GPU/build tooling. Flake input URLs must
remain literal by Nix design, so the installer validates the running release
against this policy before proceeding.

Every run offers an explicit firmware-update check. If accepted, it refreshes
fwupd metadata and applies available updates before continuing; declining it or
an unavailable update is non-fatal.
The bootstrap step enables `services.fwupd` in `/etc/nixos/configuration.nix`
as well as installing the command, so firmware updates work before the first
gjallarOS rebuild when possible.

The wizard collects human and administrator intent, normalizes it, writes the
machine-local generated state, and can generate hardware configuration before
running `nixos-rebuild`.
`generated/state.nix` is rendered and atomically replaced by `gjallarctl`; all
string values escape Nix interpolation markers as well as quotes and backslashes.
Machine-local Git protection is also owned by `gjallarctl`, with repository
containment checks and fixed-argument Git invocations.
Hardware generation writes `generated/hardware.nix` using a Go plan/apply action
with timestamped backup, fixed `nixos-generate-config` arguments, and atomic
replacement.
Deployment uses a Go plan/apply action: `nixos-rebuild dry-build` must pass before
the next boot generation is installed, and both privileged commands use fixed
arguments.
Generated machine-local state remains outside reusable repository configuration.
Hardware identity, capabilities, validated quirks, and exceptional hardware
policy are resolved through ODDC rather than through selectable machine profiles.

When run inside a graphical session, the installer uses GTK dialogs through
Zenity. Zenity is bootstrapped into the NixOS configuration automatically;
TTY, SSH, live-media, and headless runs continue using the terminal interface.
On an existing install, a missing Zenity package also triggers the small
prerequisite bootstrap so the next installer prompts can use GTK.
The same bootstrap sets `environment.variables.GTK_THEME` to `Adwaita:dark`,
a predictable GTK engine for installer dialogs. The active Noctalia/Stylix
palette owns normal desktop theming.
Closing or pressing Escape in a GTK prompt opens a quit confirmation; choosing
No returns to the interrupted prompt.

Tailscale is enabled by default. The installer asks for the home LAN subnets
that stay local at home and, if Tailscale should act as VPN, for the exit node
(`auto` finds the home router's once the tailnet is reachable), the trusted
Wi-Fi names, which of those still route the internet through the exit node,
and whether a nearby tailnet subnet router proves a trusted site. Quote Wi-Fi
names that contain commas. See "Tailscale VPN and network trust" in the
top-level README for changing these later.

Local AI is optional. If disabled, Ollama, OpenCode, the AI wrapper commands,
and AI profiling are all omitted from the generated system.

Before rebuilding, make sure your SOPS age key exists and that
`secrets/default.yaml` is encrypted for it. The installer checks this and
prints a warning when the secrets cannot be decrypted; it does not create or
guess service credentials.
