<div align="center">

<img src="docs/branding/gjallaros-logo.svg" alt="GjallarOS logo: Gjallarhorn held in a fist" width="128">

# GjallarOS

**A secure, modular NixOS workstation on Hyprland.**

NixOS 26.05 · Hyprland · Noctalia · ODDC hardware policy · TPM2 disk encryption · reviewable installer

<br>

<a href="#-quick-start"><kbd> <br> Quick start <br> </kbd></a>&ensp;
<a href="#-screenshots"><kbd> <br> Screenshots <br> </kbd></a>&ensp;
<a href="#features"><kbd> <br> Features <br> </kbd></a>&ensp;
<a href="#%EF%B8%8F-keybindings"><kbd> <br> Keybindings <br> </kbd></a>&ensp;
<a href="#-configuration"><kbd> <br> Configuration <br> </kbd></a>&ensp;
<a href="#-license"><kbd> <br> License <br> </kbd></a>

<br><br>

<img src="docs/screenshots/desktop.webp" alt="GjallarOS desktop with the Noctalia bar" width="100%">

</div>

<br>

## 🚀 Quick start

Needs a machine booted in UEFI mode (legacy BIOS/CSM is refused). On NixOS,
or the NixOS minimal ISO:

```bash
curl -fsSL https://raw.githubusercontent.com/JadeOpenServices/gjallarOS/stable/scripts/installation/install.sh | bash
```

The script clones the repository with its submodules into
`~/Documents/gjallarOS` (set `GJALLAR_DIR` to change it) and starts the
interactive installer from that checkout. Re-running it reuses the checkout.
`GJALLAR_CLONE_ONLY=1` stops after the clone. From an existing checkout,
run `./scripts/installation/install.sh` directly.

That checkout only runs the installer. The installed system gets its own
clone of the official repository in `/etc/gjallar/system`, owned by root,
at the commit the installer was built from, with this machine's
`generated/` and `user.config.json` copied in. An installer built from a
commit that is not pushed to `main`, or with local changes, stops before
the clone.

For automated non-secret answers:

```bash
cp scripts/installation/user_PresetJSON/default.user.config.json user.config.json
```

Edit the copy, then run the installer. Secrets, LUKS passphrases, recovery
settings, generated hardware data and backups are excluded from Git.

> [!TIP]
> After the install, apply every change with `rebuild`, not
> `sudo nixos-rebuild switch --flake`. See
> [Rebuilds and removable disks](#-rebuilds-and-removable-disks).

<div align="right"><a href="#gjallaros">↑ back to top</a></div>

## 📸 Screenshots

<div align="center">
<table>
  <tr>
    <td colspan="2"><img src="docs/screenshots/tiling.webp" alt="Tiled Ghostty with fastfetch, Dolphin and btop"></td>
  </tr>
  <tr>
    <td width="50%"><img src="docs/screenshots/launcher.webp" alt="Application launcher"></td>
    <td width="50%"><img src="docs/screenshots/control-center.webp" alt="Noctalia control center"></td>
  </tr>
  <tr>
    <td width="50%"><img src="docs/screenshots/greeter.webp" alt="Login greeter"></td>
    <td width="50%"><img src="docs/screenshots/lock-screen.webp" alt="Lock screen with weather"></td>
  </tr>
  <tr>
    <td colspan="2" align="center"><img src="docs/screenshots/boot-unlock.webp" alt="Boot splash asking for the disk PIN" width="50%"></td>
  </tr>
</table>

<sub>Tiling · launcher · control center · greeter · lock screen · TPM2+PIN disk unlock at boot.<br>
Taken in a QEMU test VM at 1920×1080.</sub>
</div>

<div align="right"><a href="#gjallaros">↑ back to top</a></div>

## Features

| | |
| --- | --- |
| **Desktop** | Hyprland-only, Home Manager user configuration, Noctalia shell, Stylix theming propagated to system apps. |
| **Disk encryption** | LUKS2 with TPM2 enrollment, optional TPM+PIN, recovery-key handling, SOPS/age secrets. |
| **Hardware policy** | [ODDC](https://github.com/JadeOpenServices/oddc)-backed device identity and capabilities; graphics, Wi-Fi, firmware, touchscreen, battery and clamshell detection. |
| **USB trust** | Review new USB devices, TPM-signed permanent trust, opt-in enforcement. |
| **VPN and network trust** | Tailscale with per-network trust levels and automatic exit nodes. |
| **App catalogue** | One list in `user.config.json` picks the apps; each app owns its whole module. |
| **Local AI** | Optional localhost-only Ollama with hardware-aware model selection, or a central HTTPS server with a sealed token. |
| **Recovery** | Trusted recovery boot entry, recovery ISO, generation rollback without deleting anything newer. |
| **Apps** | VSCodium, Thunderbird, web apps (Teams, Plane), system-wide Wine, optional Nemu. |
| **Maintenance** | Helpers for rebuilding, cleanup, thermal status, and updates. |

<details>
<summary><b>Local AI details</b></summary>
<br>

AI is optional and localhost-only, with hardware-aware model selection. On AMD
integrated graphics it runs on Vulkan instead of ROCm, and it is confined
below the desktop (memory capped at 65% of RAM, low CPU/IO priority, killed
first under memory pressure) so a model load cannot take Hyprland down. Below
8 GiB RAM no local model fits: the installer offers a central server or no
AI, and local AI then runs only a model you name. From 8 GiB it picks a 7b
model, from 24 GiB a 14b one, and with 32 GiB plus a 12 GiB graphics card a
30b one.
To turn local AI on after the install, run `gjallarctl ai enable`; the
rebuild then picks the model the same way. A changed `overrideModelWith`
takes effect with the next rebuild too.

Instead of a local model, AI can use a central server: set `aiEndpoint`
(`https://host[:port]` only), `aiRemoteModel` and `aiRemoteContextTokens`.
The server must sit behind HTTPS and check a bearer token (Ollama itself has
no login); http endpoints and missing tokens are refused. The installer asks
for the token hidden; on an installed system run `gjallarctl ai set-token`.
The token never enters the Nix store or `user.config.json`: it is encrypted
with `systemd-creds` (TPM2-bound when present) into
`/var/lib/gjallarOS/ai/endpoint-token.cred`.

</details>

<div align="right"><a href="#gjallaros">↑ back to top</a></div>

## ⌨️ Keybindings

`$mod` is the Super key. <kbd>Super</kbd> + <kbd>F1</kbd> shows every
shortcut and gesture of the running session, searchable, also as
"Keyboard shortcuts" in the launcher and as `gjallar-keybinds --print` in a
terminal. The binds live in
[`user/wm/hyprland/keybinds.json`](user/wm/hyprland/keybinds.json); each one
needs a `desc`, which is what the list shows.

| Keys | Action |
| --- | --- |
| <kbd>Super</kbd> + <kbd>D</kbd> | Application launcher |
| <kbd>Super</kbd> + <kbd>T</kbd> | Terminal (Ghostty) |
| <kbd>Super</kbd> + <kbd>F</kbd> | File manager (Dolphin) |
| <kbd>Super</kbd> + <kbd>E</kbd> | Editor |
| <kbd>Super</kbd> + <kbd>B</kbd> | Browser |
| <kbd>Super</kbd> + <kbd>N</kbd> | LibreOffice (when installed) |
| <kbd>Super</kbd> + <kbd>Q</kbd> | Close window |
| <kbd>Super</kbd> + <kbd>W</kbd> | Fullscreen |
| <kbd>Super</kbd> + <kbd>H</kbd> <kbd>J</kbd> <kbd>K</kbd> <kbd>L</kbd> | Move focus |
| <kbd>Super</kbd> + <kbd>Shift</kbd> + <kbd>H</kbd> <kbd>J</kbd> <kbd>K</kbd> <kbd>L</kbd> | Move window |
| <kbd>Super</kbd> + <kbd>Ctrl</kbd> + arrow keys | Resize window (hold to repeat) |
| <kbd>Super</kbd> + left-drag | Move window |
| Drag the gap between windows | Resize window |
| <kbd>Super</kbd> + right-drag or <kbd>Super</kbd> + <kbd>Shift</kbd> + left-drag | Resize window (the second is easier on a touchpad) |
| <kbd>Super</kbd> + <kbd>1</kbd>…<kbd>0</kbd> | Switch workspace |
| <kbd>Super</kbd> + <kbd>Shift</kbd> + <kbd>1</kbd>…<kbd>0</kbd> | Move window to workspace |
| <kbd>Super</kbd> + <kbd>S</kbd> | Scratchpad workspace |
| <kbd>Super</kbd> + <kbd>Shift</kbd> + <kbd>Z</kbd> or <kbd>Print</kbd> | Screenshot region |
| <kbd>Super</kbd> + <kbd>Escape</kbd> | Lock screen |
| <kbd>Super</kbd> + <kbd>F1</kbd> | Show all shortcuts |
| <kbd>Super</kbd> + <kbd>Ctrl</kbd> + <kbd>K</kbd> | On-screen keyboard (touch devices) |

On touch devices the on-screen keyboard also opens when the device folds
into tablet mode and on the keyboard key some convertibles have. With no
keyboard attached, the login screen and the lock screen show their own
touch keyboard; with one attached, touch the password field to open it, and
a key on the keyboard closes it again. The disk unlock prompt shows its touch
keyboard on every touch device. These keyboards
have letters, numbers and symbols only: no clipboard, emoji or word
suggestions. The HP ZBook
x2 bezel Quick Keys have three presets, switched with the third key from the
top. GjallarOS maps none of them: every key sends a macro key
(`XF86Macro1`–`15`) that no other key sends, so you can bind it in Hyprland,
a drawing app or any other program. The bar shows the active preset number
next to the network widget after the first key press.

<div align="right"><a href="#gjallaros">↑ back to top</a></div>

## 🔧 Configuration

`user.config.json` contains machine-local human intent. The installer normalizes
that intent into `generated/state.nix`; machine hardware lives in
`generated/hardware.nix`, and historical compatibility baselines live in
`generated/install-state.nix`. `deployment/release-policy.json` records the supported release. The
root flake, Home Manager, and Stylix follow 26.05; only the ComfyUI
development shell intentionally uses unstable.

Web apps are listed in `webApplications`, for example
`[{"id": "teams"}, {"id": "plane", "endpoint": "https://plane.example"}]`.
An older config with `planeEnable`/`planeHost` is moved into the list once on
the next rebuild, and the rebuild says so. draw.io was removed; a rebuild drops
it from older configs once and says so. Its browser profile in
`~/.config/gjallarOS/chromium-drawio` stays until you delete it.

`"dolphinDriveUsage": true` shows used and total space next to each drive in
the Dolphin places panel. It patches KIO, so KIO, Dolphin and the file dialogs
are compiled on the machine instead of downloaded. That adds a lot of time to
a rebuild, so it is off by default.

### Changing settings

On a system that builds from `/etc/gjallar/system`, root owns
`user.config.json`. Change it with `gjallarctl settings`, as your own user:

```sh
gjallarctl settings list
gjallarctl settings set touchpadWorkspaceSwipe=false timezone=Europe/Berlin
gjallarctl settings pending
gjallarctl settings apply
```

VALUE is JSON; a key that holds text takes plain text. Each key has a tier,
and the tier decides how long a change waits before it can be written:

- **everyday** (looks, input, apps): no wait.
- **security** (network, services, add-ons, hostname, user name): one hour,
  or 10 s while a router of your tailnet proves this is your home network.
- **critical** (disk unlock, recovery, firmware): one hour always, and the
  password is asked when you ask for the change and again when you apply it.
- **installer**: changes only with a new install.

The password dialog lists every key with its old and new value. Apply
writes the file and starts the rebuild, and `set` follows it until the
change is live. A waiting change lives only in your login session, so
logging out or rebooting drops it.

A new hostname must be free. The service refuses a name that a machine on
your tailnet already uses, and, on a home, site or trusted Wi-Fi network, a
name another device there answers for as `<name>.local`. Letter case does
not count. It checks again before it writes the change. When Tailscale is
off or the network is not trusted, `set` says that part was not checked. To
hear the answers, the firewall lets in UDP from port 5353 to port 1015, only
from private and link-local addresses; nothing listens there outside a
check.

To keep your settings, save them to a file and bring them back later:

```sh
gjallarctl settings export ~/settings.gjallar
gjallarctl settings import ~/settings.gjallar
```

Export asks for your own password and signs the file with a key that only
the settings service holds. Import takes only files made on this device by
your account, and refuses a file that was changed after it was made. It
asks for the change the way `set` does, with the same tiers and waits, and
says how old the file is.

To sign these files with your own OpenSSH key instead (an `ssh-ed25519` key
file or a FIDO `sk-ssh-ed25519` key), answer No to "Let GjallarOS sign your
settings files for you?" in the installer, or set `settingsSigningKey` to
your public key line later (critical tier). Export then writes a file you
sign yourself, and import needs that signature next to the file:

```sh
gjallarctl settings export ~/settings.gjallar
ssh-keygen -Y sign -n gjallaros-settings -f ~/.ssh/id_ed25519 ~/settings.gjallar
gjallarctl settings import ~/settings.gjallar
```

The second line writes `~/settings.gjallar.sig`. With an own key, only that
key counts for your account: files GjallarOS signed are refused, and pairing
is off. A file made on another device brings your personal settings, not
that device's (hostname, disk, boot and the like).

### App catalogue

Optional applications live in `apps/<id>/`: `meta.json` (name, description,
category, `default`, optional `installer` question), and a `home.nix` and/or
`nixos.nix` module. Each app owns its whole module, so removing an app removes
everything it brought in.

`user.config.json` selects apps with one list:

```json
"apps": ["ai", "btop", "containers", "dolphin", "tailscale"]
```

Without `apps`, every app with `"default": true` is installed. The installer
asks each app's `installer` question (today `ai`, `containers`, `nemu`,
`tailscale`, `thunderbird`) and uses `default` for the rest. `apps` drives
`gjallar.apps.<id>.enable` on both the NixOS side (`apps/nixos-options.nix`)
and the Home Manager side (`user/apps/default.nix`). The old `aiEnable`,
`containersEnable` and `nemuEnable` keys are rejected; list `ai`,
`containers` or `nemu` in `apps` instead.

### Firefox add-ons

Add-ons come in collections, listed in `apps/firefox-addons.json`: Privacy,
Password managers, Comfort and Cookie banners. New installs start with uBlock
Origin on and everything else off. Each add-on has one of three modes:

- `off`: not installed. If GjallarOS installed it earlier, it is removed.
- `on`: installed, and you can turn it off in about:addons.
- `locked`: installed and always on.

The installer asks which collections you want, then the mode of each add-on in
them. Afterwards `gjallarctl addons` changes them:

```sh
gjallarctl addons list
gjallarctl addons set privacy.clearurls on
gjallarctl addons set comfort off
gjallarctl rebuild
```

In `user.config.json` this looks like:

```json
"firefoxAddons": {
  "privacy": { "enable": true, "addons": { "ublock-origin": "on", "clearurls": "locked" } }
}
```

Add-ons of a collection are only installed while its `enable` is `true`.
Missing add-ons count as `off`.

Add-ons from addons.mozilla.org are pinned to one reviewed file.
`gjallarctl addons pin ID` downloads the newest version, shows its name and
permissions and asks before it saves the URL and SHA-256 to
`firefoxStoreAddons`. The build fetches exactly that file, so a changed file
on addons.mozilla.org breaks the build instead of reaching Firefox.
`gjallarctl addons update` moves pins to the newest version and lists the
permissions the new version adds. About:support shows the ID of each
installed add-on.

Firefox installs all of these through its enterprise policy, from the Nix
store, and does not update them by itself; new versions arrive with
`gjallarctl rebuild`. The rebuild, or the next login if Firefox was open,
removes add-ons that left the list, but only those Firefox itself installed
from the policy. GjallarOS
keeps a list of them in `~/.local/state/gjallaros/managed/`. Add-ons you
installed by hand stay untouched. JODS and unattended installs set both keys
in the same way.

Upgrading from a version that linked add-ons into the profile: close Firefox
before `gjallarctl rebuild`. On the next start Firefox keeps uBlock Origin
with its settings, at the version it had. The Stylix theme comes back on the
second start. A rollback never downgrades an add-on; Firefox refuses that.

### Nextcloud files

The Nextcloud client is in the default apps. To connect it, set the server in
`user.config.json` and rebuild:

```json
"nextcloudEnable": true,
"nextcloudHost": "https://cloud.example.org"
```

Only `https` addresses are accepted. Start Nextcloud from the app menu, or run
`nextcloud-enroll`, and log in. Files appear in `~/Nextcloud` and in Dolphin.

Everything starts as a placeholder: you see every file, but nothing is
downloaded until you open it. Right after the login a "Keep on this device"
window lists your top-level folders with nothing ticked. Tick the folders you
want offline; they download and stay. "Not now" or closing the window keeps
everything online-only. New top-level folders that show up later are
placeholders too.

Change it later in Dolphin: right-click a file or folder and pick "Make always
available locally" or "Free up local space". The folder settings in the
Nextcloud window have the same two actions. Your picks stay on this device
only; another device of yours starts with everything online-only again.

If the client crashes, it starts again after ten seconds. After five starts in
five minutes it stays off, and Quit in the tray stops it as well; open
Nextcloud from the app menu or log in again to bring it back.

### Thunderbird

Thunderbird (latest release) is in the default apps; the installer asks
before it installs it. You set up every account yourself. On the first start a
local page walks through the account, the Primary Password, your OpenPGP key
and its backup, and Nextcloud calendar and contacts.

GjallarOS sets these through Thunderbird's enterprise policy:

- A Primary Password is asked before the first saved password, and it cannot
  be removed. It protects saved passwords, login tokens and OpenPGP keys.
- Remote content in mail stays blocked; you allow it per sender. Scam
  warnings stay on. Account autoconfig uses HTTPS only, and connections need
  TLS 1.2 or newer.
- Mail shows as simple HTML (View, Message Body As changes it). Chat,
  telemetry, donation and survey messages are off.
- Only dictionaries, languages and themes can be added, no add-ons with code.
- Key servers are searched only when you ask for a key.

Every `mailto` link opens Thunderbird, Firefox ones included.

Smart cards are off by default. The installer asks after Thunderbird, or set
it later (security tier):

```sh
gjallarctl settings set thunderbirdSmartCard=true
gjallarctl settings apply
```

This starts `pcscd` and `gpg-agent`, lets Thunderbird use a card key through
GnuPG, and adds OpenSC for S/MIME cards. Approve the card reader when
GjallarOS asks about the new USB device.

<div align="right"><a href="#gjallaros">↑ back to top</a></div>

## 🧬 Hardware catalog (ODDC)

[ODDC](https://github.com/JadeOpenServices/oddc) is a separate project,
used like an API. The installer reads the machine's DMI identity and asks ODDC
for its model; only that model's answer (referenced entities, evidence and the
ODDC revision) lands in `generated/oddc`. Nothing else of the catalog reaches
this repository or the machine. The `oddc` flake input supplies module code
only, and the system configuration reads the answer as `oddc.catalog`. The
installed system receives that model under `/etc/oddc`.

The answer comes from the ODDC commit `flake.lock` pins for the `oddc` input.
Updating ODDC on a device is one operation:

```bash
gjallarctl oddc update --rebuild
```

<details>
<summary><b>How ODDC updates work</b></summary>
<br>

`gjallarctl oddc update --rebuild` runs `nix flake update oddc` on the
system's checkout and rebuilds, then says whether ODDC moved
(`ODDC: OLD -> NEW` or `ODDC: already at REV`) and where this machine's answer
stands. `--simple` prints only that as one line; `--debug` lists the lock and
answer revisions before and after, the model, and whether the answer was
fetched, kept or left unchanged. When the pinned commit differs from
`generated/oddc/revision`, the rebuild fetches the same model again at the new
commit and swaps it in only once complete. If that fetch fails, for example
offline, the rebuild stops with the error and changes nothing. Without `--rebuild`, the next `rebuild` does the
fetch. `gjallarctl oddc doctor` shows the ODDC revision the installed model
came from.

`rebuild --hardware-update [--channel stable|staging] [--switch 0|1]` does
the same move as part of a rebuild: it runs `oddc update --flake <checkout>
[--channel CHANNEL]` first and stops with `nothing rebuilt` if that fails.
`--channel` moves the oddc input to the newest commit of that ODDC channel
(`stable` when left out); no commit hash is needed. `--switch 1` (the default) rebuilds and switches right away;
`--switch 0` only moves the pin, and the next `rebuild` applies it. It needs an
`oddc` that knows `update --flake`. Always apply with `rebuild`, not `sudo
nixos-rebuild switch --flake`: `generated/` is untracked, so a plain git flake
cannot see it.

A plain `rebuild` never moves the pin. It asks GitHub whether the branch the
oddc input follows has a newer commit and, if so, prints `ODDC update
available: OLD -> NEW. Apply with: rebuild --hardware-update`. Offline or on
any error it says nothing. Managed endpoints skip the check: JODS moves their
pin.

`gjallarctl oddc COMMAND` passes ODDC commands to `oddc` unchanged, for example
`gjallarctl oddc resolve` or `gjallarctl oddc explain --path PATH`. On the
installed system these default to its model. `gjallarctl oddc validate-device`
validates this checkout on the device. Validation evidence goes to ODDC through
`oddc contribute`, not into this repository.

</details>

<div align="right"><a href="#gjallaros">↑ back to top</a></div>

## 💾 Rebuilds and removable disks

Every GjallarOS system names its checkout in `/etc/gjallar/repository`
(`dotfilesDir`), so `rebuild`, `gjallarctl oddc update` and the other commands
find it from any directory. `--repo PATH`, `GJALLAROS_REPO` and the checkout you
last rebuilt from take precedence. Direct host flake builds fail unless the
source was staged by `gjallarctl`; installer and recovery deployment also use
that staging path. This prevents skipping the normal preparation workflow. It
is not an authorization boundary against root or someone who can change the
source. Package builds and recovery ISO builds remain available directly.

To bring an install up to date with upstream, run:

```bash
rebuild --update
```

It fast-forwards that checkout to its release channel and then rebuilds.
The channel is `releaseChannel` in `user.config.json`: `stable` (the
default), `staging` or `dev`. Change it with
`gjallarctl settings set releaseChannel=staging`. A move to `stable` applies
at once; any other move waits like other critical settings.
Local commits or uncommitted edits stop the update, and nothing is rebuilt.
A checkout that is ahead of its channel stays where it is until the channel
catches up; it never goes back to an older commit.
`rebuild --update-inputs` moves every flake input first (for development
checkouts). `rebuild --help` lists every option.

When `dotfilesDir` is `/etc/gjallar/system`, root owns the checkout and
builds it only as it is on the official origin. `--repo`, `--host`,
`--update-inputs` and other `nixos-rebuild` arguments are refused there; the
oddc pin may move only to a commit of an official ODDC branch (ROADMAP §14).

### Moving an older install to `/etc/gjallar/system`

Installs made before the system checkout build from a checkout in your home
folder. To move one over, run as your own user:

```sh
gjallarctl system move
```

It shows the commit the system runs, and lists local commits and changes in
your checkout that the move leaves behind. Your checkout keeps them; local
`.nix` changes go into the developer-mode folder instead (see
`gjallarctl system apply`). The running commit must be on an official
release channel (`dev`, `staging` or `stable`); a system built with local
changes refuses until you rebuild from a pushed commit. Set `releaseChannel`
first: the new checkout follows that channel. Then, as root:

```sh
sudo gjallarctl system clone --from ~/Documents/gjallarOS
sudo gjallar-system-rebuild
```

`system clone` clones the official repository at the running commit and
copies `generated/` and `user.config.json` from the old checkout as files.
It never runs git in the old checkout. The rebuild switches
`/etc/gjallar/repository` to the new checkout; plain `rebuild` uses it from
then on. The old checkout stays where it is.

When `/etc/gjallar/system` is missing or broken, `rebuild` says so and names
the same `system clone` step.

To try a `gjallarctl` change before rebuilding, run it from the checkout with
`go run ./cmd/gjallarctl rebuild` (or any other command). It uses the sudo
helpers of the installed system, so this works only on a machine that already
runs GjallarOS. If you need the package itself, build it with
`nix build "git+file://$PWD#gjallarctl"`. A `path:` flake reference copies the
whole directory into the Nix store, untracked files and VM disks included.

### Release cache

GjallarOS patches a few packages (Noctalia, its greeter, the Nextcloud client
with OpenVFS, OpenCode, Hyprland, Ghostty, Dolphin and the KDE apps that
come with it, and others), so cache.nixos.org does not have them and every
machine compiled them itself. For each `v*` release tag,
`.github/workflows/cache.yml` builds them (flake package `prebuilt`), signs
them and uploads them as files of the `nix-cache` release. Those files are a
plain Nix binary cache. Each tag adds to it, so a machine on `main` downloads a
package whenever it matches one built for any tag. Anything missing still
builds locally.

`deployment/binary-cache.json` names the cache and its public key:

```json
{
  "url": "https://github.com/JadeOpenServices/gjallarOS/releases/download/nix-cache",
  "publicKey": "gjallaros-cache-1:..."
}
```

Machines add it to `nix.conf`, and `rebuild` and the installer pass it on the
command line too, so the first install uses it as well. Nix only accepts
files signed with that key.

The workflow reads the signing key (secret `NIX_SIGNING_KEY`) only through the
`release-cache` environment, which should allow `v*` tags only and require a
reviewer. Create the key on your own machine; it never needs network:

```sh
nix key generate-secret --key-name gjallaros-cache-1 > cache.sec
nix key convert-secret-to-public < cache.sec
```

Paste `cache.sec` into the secret, put the public key into
`deployment/binary-cache.json`, then delete `cache.sec` or keep it offline.
The key signs cache files and nothing else. Forks keep
`deployment/binary-cache.json` empty or point it at their own cache.

### Recovery and rollback

With `recoveryEnable`, the boot menu lists only the newest generation and its
recovery entries. To boot an older generation, start trusted recovery (or the
recovery ISO, with the root mounted at `/mnt`) and run:

```bash
sudo gjallar-recover generations /
sudo gjallar-recover rollback / N
```

This adds a new generation with that system; nothing newer is deleted.

The recovery partition shows up in the same menu as "GjallarOS recovery
(partition)". It has no firmware boot entry of its own and no loader at the
removable-media path, so firmware that ignores the boot order still starts
GjallarOS. If the ESP itself is broken, boot the recovery ISO from USB.

> [!NOTE]
> The installed system does not carry the installer. `gjallar-installer` and
> `gjallar-recovery-maintenance` (package `pkgs/gjallar-installer`) can
> repartition and reinstall a disk, so they exist only in the trusted-recovery
> boot entry, the recovery-maintenance initrd and the recovery ISO. A reinstall
> or reset therefore needs a deliberate boot into recovery.

### Snapshots

Installs made with the subvolume layout keep read-only Btrfs snapshots of
`/home` (hourly) and of the system settings in `/etc/gjallar/system` (daily,
and before and after each rebuild). `/`, `/nix` and `/var/lib` never get
snapshots: generations bring the system back, and the machine keys in
`/var/lib` must not live on in old copies. Older installs get no snapshots
until they are reinstalled.

| Key | Default | Meaning |
| --- | --- | --- |
| `snapshotsEnable` | `true` | turn the snapshot timers on or off |
| `snapshotsKeepHours` | `24` | hours of hourly `/home` snapshots to keep (0 to 168) |
| `snapshotsKeepDays` | `7` | days of daily snapshots to keep (1 to 365) |

Only root can reach the snapshots; they are mounted nowhere on the running
system. When the disk is nearly full, no new snapshot is made and the
journal says so (`journalctl -u gjallar-snapshot-home`).

To get an older version of one of your files back:

```bash
gjallarctl snapshots list
gjallarctl snapshots restore ~/Documents/notes.txt
```

`restore` asks your password and takes the newest snapshot that has the
file, or the one you name with `--from SNAPSHOT`. The copy goes next to the
file, as `notes.restored-20261021T030000.txt`; your current file is never
touched. If the folder is gone too, add `--to FOLDER`. Only plain files in
your own home can be restored, one at a time.

To put all of `/home` or the settings back, start the trusted recovery boot
entry and run the lines below. From the recovery partition, unlock with
`gjallar-recover open-root DEVICE` first and use `/mnt` instead of `/`.

```bash
sudo gjallar-recover snapshots /
sudo gjallar-recover snapshot-rollback / home.20261021T030000+0200
```

It asks you to type `ROLLBACK-HOME` (or `ROLLBACK-CONFIG` for a `config.`
snapshot). The snapshot itself stays as it was; the current `/home` is kept
as a read-only copy in the list under "replaced by a rollback". When `/home`
is in use, the change takes effect at the next start. After a settings
rollback, run `rebuild` once the system is up so the system matches the
settings again. `sudo gjallar-recover snapshot-drop / NAME` deletes a kept
copy once you no longer need it.

> [!WARNING]
> A file you delete from `/home` stays in older snapshots until they age
> out. Snapshots live on the same disk, so they are not a backup.

### Secure Boot

The installer offers Secure Boot on models whose ODDC entry has a Secure Boot
setup, with yes as the default. It asks this before the recovery questions:
the recovery partition is signed with this machine's Secure Boot key, so
without Secure Boot there is no recovery partition and the disk is not shrunk
for one. Saying no, or a model without an ODDC Secure Boot setup, shows what
the install goes without: nothing checks the startup files, the disk
passphrase is typed at every startup, repairs start from a USB stick, and JODS
cannot manage the machine. A system installed without it gets it with:

```bash
gjallarctl secure-boot setup
```

It takes the installer's steps: it checks the firmware state, creates the keys
and shows their recovery archive, turns on `secureBootEnable`, rebuilds and
offers to restart into firmware setup. After the restart the boot services
enroll the keys and verify them. Run the command again after a step was
interrupted; it picks up where it stopped. `gjallarctl secure-boot status`
shows the current state.

Some firmware empties its revocation list (dbx) along with its keys. When the
ODDC setup wants dbx kept, the firmware's own default list is written back
before enrolling, and enrollment stops if that fails. Firmware that only knows
Microsoft's 2011 KEK (expired June 2026) also gets the 2023 one, so revocation
updates signed with it still apply once GjallarOS owns the keys.

Revocation updates do not arrive on their own. Once Secure Boot is on, `setup`
offers one, and you can check any time with:

```bash
gjallarctl secure-boot update-dbx
```

The new list takes effect at the next restart. With TPM disk unlock, the TPM
accepts the current and the new state until then, so a rebuild in between
still unlocks.

### Reading the firmware flash chip

`"firmwareFlashReadEntry": true` adds a boot menu entry tagged
`firmware-flash-read`. It is for reading the firmware chip on machines whose
firmware hides the SPI controller from Linux. Only that entry allows
`flashrom`'s `internal` programmer (`iomem=relaxed`):

```bash
sudo flashrom -p internal -r flash.bin
```

Only read with it. With TPM disk unlock, this entry asks for the disk recovery
key or disk passphrase, never the startup PIN: the TPM never unlocks the disk
in a boot with these relaxed limits. Rebuilds stop in this entry; restart
into the normal one first. The setting is off by default; turn it off again
once the dump is done.

### USB trust

The installer asks for USB trust review on machines with an ODDC model. On a
machine with TPM2 it sets the signing handle (`usbTrustTpmHandle`, default
`0x81000042`), and the broker creates the TPM signing key on its first start.
A key left at that handle by an earlier install on the same TPM is reused; any
other object there is never overwritten, and permanent trust stays off until
you pick a free handle. `sudo gjallarctl usb provision-key` does the same step
by hand. Creating the key trusts no devices; inspect `gjallarctl usb status`
and review devices first.

With an encrypted root, the installer also asks "Block unknown USB devices from
the first boot?" (default no). Say no on a desktop with a USB keyboard: it is
not built in, so it stays blocked until approved. Enforcement can be turned on
later with `"usbTrustEnforce": true` in `user.config.json` and a `rebuild`. To
turn it off, run `sudo gjallarctl usb disarm` first and enter the disk
encryption passphrase, then set it back to `false` and `rebuild`. Disarm alone
is temporary: the broker re-arms from the config on its next restart, or right
away with `sudo gjallarctl usb arm`. The other
order is refused by `gjallarctl settings`; done by hand, USBGuard keeps
blocking unknown devices at boot while the stored state still says armed.

Turning USB trust off altogether (`"usbguardEnable": false`) takes effect for
new USB devices at the next reboot. Until then a newly plugged device stays
off: USBGuard does not hand the ports back to the kernel's default when it
stops, because anything that stops it would then let every device in.

On a machine with TPM2 the installer also asks "Let approved USB devices expire
when unused?" (default no). With it on, an approved external device that was
not connected for `usbTrustLapseDays` days (30 by default, any number from 1)
asks for approval again. The days count from the last connection, and from the
moment the setting was turned on, so old devices do not all ask at once. Three
days before a device expires, a notice offers to renew it. Built-in devices
never expire. Without a built-in USB keyboard, the keyboard and mouse used last
never expire either. `gjallarctl settings` turns it on or shortens it at once;
turning it off (`0`) or lengthening it waits out the security delay.

The review dialog opens in a simple view. "Techy view" shows IDs, ports and
interfaces for that dialog only. The "Always techy view" box writes
`"usbReviewTechnicalView": true` or `false` into `user.config.json`, so the
choice stays until someone unticks it. Someone debugging the laptop can tick
it, work, and untick it to give the user the simple view back. Editing the key
by hand does the same.

Unformatted USB disks offer an explicit exFAT setup prompt. Cancelling leaves the
disk unchanged. Existing partitions, recognized filesystems, mounted devices,
and readers with no media are excluded. Formatting uses UDisks/Polkit and
requires confirmation; lack of a recognized filesystem does not prove a disk
contains no valuable data.

<div align="right"><a href="#gjallaros">↑ back to top</a></div>

## 🛡️ Tailscale VPN and network trust

The installer can enable Tailscale and use a tailnet exit node as VPN.
`gjallar-vpn-trust` then decides on every network change, tailnet change, and
every five minutes how far to trust the current network:

| Network | Recognized by | Local subnets | Exit node |
| --- | --- | --- | --- |
| home | a home subnet, proven by a trusted Wi-Fi name or your tailnet router answering on it | home subnets | off |
| home | `routerHome`: any subnet router of your tailnet answering on this LAN, yours or a friend's | this LAN | off |
| site | `siteRouterTrust`: a tailnet subnet router on this LAN that reaches every `siteRouterTargets` host | this LAN | off |
| trusted-wifi | a `trustedWifis` name on a network that needs a key | this LAN | off, or its `wifiExitNodes` entry |
| untrusted | anything else | none | `exitNode` |
| offline | no default route | none | unchanged |

> [!WARNING]
> A trusted name on an open network only produces a warning, since anyone can
> clone it.

> [!WARNING]
> With `routerHome`, every LAN behind a router of your tailnet is home. A travel
> router in a hotel is one too, so the hotel network sees your traffic unless
> that router sends it through its own VPN.

Router proof is a signed Tailscale ping answered from this LAN or by its
gateway, so copying a subnet or a Wi-Fi name is not enough. Every member of the
tailnet counts, tagged routers too, because the tailnet admin approves each
subnet route. A node shared in from another tailnet never counts as a router:
Tailscale does not pass on its subnet routes. It can still be your exit node
when its owner allows that, so a friend's exit node works too.

`exitNode` is a tailnet host name or 100.x address; `auto` picks an
online exit node, preferring one that routes a home subnet; `off` keeps it off
everywhere; empty leaves the exit node to you unless `wifiExitNodes` is set.
On an untrusted network the VPN fails closed: when the exit node is offline,
none can be set or Tailscale is not running, the internet is blocked outside
the tunnel instead of going around it. The local network and DHCP still work;
DNS does not, so the network cannot see what you look up. A notice says which
side failed, and the shield in the bar shows the block.

To get online anyway, for example to sign in to a captive portal or to
Tailscale again, pause the VPN on that network from the notice, the bar or
`gjallarctl vpn pause`. It asks for an administrator's password every time and
ends after one hour, on a network change, before sleep or on a restart, or
when you click the shield again (`gjallarctl vpn resume`). While paused, the
network sees where apps connect. `gjallarctl vpn status` shows the block or
the pause.

LAN access stays on while an exit node is active so captive portals and
printers keep working. DNS then goes to the exit node, so a home router's
filtering also applies away from home unless the tailnet's admin console
overrides DNS. `gjallarctl vpn status` shows the current decision.

<details>
<summary><b>Change trust at runtime</b></summary>
<br>

As root:

```bash
gjallarctl vpn trust-wifi                       # the current Wi-Fi
gjallarctl vpn trust-wifi "Home WiFi" --exit-node OpenWrt   # trusted LAN, internet via OpenWrt
gjallarctl vpn trust-wifi "Home WiFi" --exit-node default   # trusted LAN, exit node off
gjallarctl vpn untrust-wifi "Home WiFi"
gjallarctl vpn exit-node auto                   # or off, NAME, default
```

Changes apply at once and are kept in `/var/lib/gjallar/vpn-trust-override.json`,
layered over the built-in policy in `/etc/gjallar/vpn-trust.json`, so they
survive rebuilds. To make them permanent, replace the `tailscale = { ... };`
line in `generated/state.nix` with the output of `gjallarctl vpn export`,
rebuild, then delete the override file.

</details>

### Time

The clock is set only over NTS, which signs the time so nobody on the
network can change it: Cloudflare, Netnod, PTB and time.nl, one server each.
At least two of them have to agree before chrony moves the clock. Plain NTP,
the NixOS pool and time servers handed out by DHCP are not used, on any
network. When no NTS server answers, for example on a network that blocks
TCP port 4460, the clock keeps running from where it was.
`gjallarctl system status` shows `Time: confirmed by NTS at ...`, or
`Time: not confirmed since ...` when no server has confirmed it in the last
two hours. While the time is not confirmed, a clock shows in the bar; click
it for details, or run `gjallarctl system time`. When it stays unconfirmed
for 24 hours while you are online, a card says so once. The design and the
next steps are in section 18 of
[ROADMAP.md](ROADMAP.md).

<div align="right"><a href="#gjallaros">↑ back to top</a></div>

## 💜 Credits

GjallarOS builds on the ideas and groundwork of
[AlfheimOS](https://github.com/Serpentian/AlfheimOS) and the
[hyprdots](https://github.com/prasanthrangan/hyprdots) project.
[v3rm1n's dotfiles](https://codeberg.org/v3rm1n/dotfiles) inspired it
and got me started on this idea in the first place, together with my
loving hate for Windows and what it has become, as an ex-Windows user
since 2021. Please send some love their way as well.

The desktop shell, greeter and default owl wallpaper come from
[Noctalia](https://github.com/noctalia-dev/noctalia). The README layout
takes inspiration from [HyDE](https://github.com/HyDE-Project/HyDE).

## 📜 License

GjallarOS is licensed under the GNU Affero General Public License v3.0 or
later; see [LICENSE](LICENSE). Bundled parts keep their own licenses:

- [`pkgs/monique`](https://github.com/vardstein/monique): GPL-3.0.
- [`apps/nemu/module.nix`](apps/nemu/module.nix): nemu's upstream NixOS
  module, BSD-2-Clause, license text in the file header.
- [`themes/brand/openjade`](themes/brand/openjade/README.md): the openJade
  logo is a trademark and not covered by the AGPL.

<div align="right"><a href="#gjallaros">↑ back to top</a></div>
