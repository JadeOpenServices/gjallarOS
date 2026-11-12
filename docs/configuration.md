# Configuration

How gjallarOS keeps its settings, the settings that are off unless you turn
them on, how the hardware catalogue and rebuilds work. Everyday changes are
in [Changing settings](../README.md#changing-settings).

## Where the settings live

Your answers to the installer are kept in `user.config.json`. The installer
turns them into `generated/state.nix`, which the system build reads.
`generated/hardware.nix` describes this machine's hardware, and
`generated/install-state.nix` keeps the NixOS and Home Manager versions the
machine was first installed with, so later releases keep its old data
formats. `deployment/release-policy.json` names the supported NixOS
release: the root flake, Home Manager and Stylix follow it, and only the
ComfyUI development shell uses unstable.

On a system that builds from `/etc/gjallar/system`, root owns
`user.config.json`. Change it with `gjallarctl settings`.

## Saving settings to a file

To keep your settings, save them to a file and bring them back later:

```sh
gjallarctl settings export ~/settings.gjallar
gjallarctl settings import ~/settings.gjallar
```

Export asks for your password and signs the file with a key that only
the settings service holds. Import takes only files made on this device by
your account, or by an account you paired with (below), and refuses a file
that was changed after it was made. It asks for the change the way `set`
does, with the same tiers and waits, and says how old the file is. A file
made on another device brings only your personal settings. Settings that
belong to that device (hostname, disk, boot and the like) stay behind.

### Pairing two devices

Pairing lets a settings file made on your other device import here. Each
device makes a pairing file, and each adds the other one's:

```sh
gjallarctl settings pair export ~/pair.gjallar
gjallarctl settings pair add ~/pair-from-laptop.gjallar
```

`pair add` shows a code. The other device shows its own code when it adds
your file. Compare the two side by side or read them out on a call, never
over the way the files came. When they match, confirm and type your
password. `gjallarctl settings pair list` shows the pairings, and
`gjallarctl settings pair remove ID` ends one without a password.

### Signing with an OpenSSH key

To sign these files with your OpenSSH key instead (an `ssh-ed25519` key
file or a FIDO `sk-ssh-ed25519` key), answer No to "Let gjallarOS sign your
settings files for you?" in the installer, or set `settingsSigningKey` to
your public key line later (critical tier). Export then writes a file you
sign yourself, and import needs that signature next to the file:

```sh
gjallarctl settings export ~/settings.gjallar
ssh-keygen -Y sign -n gjallaros-settings -f ~/.ssh/id_ed25519 ~/settings.gjallar
gjallarctl settings import ~/settings.gjallar
```

The second line writes `~/settings.gjallar.sig`. With an OpenSSH key, only
that key counts for your account: files gjallarOS signed are refused, and
pairing is off.

## How a new hostname is checked

A new hostname must be free. The settings service refuses a name that a
machine on your tailnet already uses, and, on a home, site or trusted Wi-Fi
network, a name another device there answers for as `<name>.local`. Letter
case does not count. It checks again before it writes the change. When
Tailscale is off or the network is not trusted, `set` says that part was
not checked. To hear the answers, the firewall lets in UDP from port 5353 to
port 1015, only from private and link-local addresses; nothing listens there
outside a check.

## Web apps

Web apps are listed in `webApplications`, for example:

```json
"webApplications": [{"id": "teams"}, {"id": "plane", "endpoint": "https://plane.example"}]
```

## App catalogue

Optional applications live in `apps/<id>/`: `meta.json` (name, description,
category, `default`, optional `installer` question, optional `package`, the
nixpkgs attribute of its main program), and a `home.nix` and/or
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
and the Home Manager side (`user/apps/default.nix`).

## Optional settings

These are off by default.

`"dolphinDriveUsage": true` shows used and total space next to each drive in
the Dolphin places panel. It patches KIO, so KIO, Dolphin and the file dialogs
are compiled on the machine instead of downloaded. That adds a lot of time to
a rebuild.

`"aarch64Emulation": true` lets this device build NixOS for ARM devices such as
phones. The kernel then hands every aarch64 program to qemu-user, which runs it
as the user who started it; set-user-ID bits on such programs are ignored.
Nix also lists `aarch64-linux` (and `i686-linux`) in `extra-platforms`. It is a
setting for one device; turning it off applies at once.

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
into the normal one first. Turn the setting off again once the dump is done.

## Hardware catalogue (ODDC)

[ODDC](https://github.com/JadeOpenServices/oddc) is a separate project,
used like an API. The installer reads the machine's DMI identity and asks ODDC
for its model; only that model's answer (referenced entities, evidence and the
ODDC revision) lands in `generated/oddc`. Nothing else of the catalogue reaches
this repository or the machine. The `oddc` flake input supplies module code
only, and the system configuration reads the answer as `oddc.catalog`. The
installed system receives that model under `/etc/oddc`.

When no ODDC model matches, the installer says the machine runs best effort.
It lists the parts it found, checked against the ODDC catalogue built into the
installer, and names the parts ODDC marks as not working on Linux, with ODDC's
reason. The machine gets the hardware-neutral base; nothing optional is
switched on, and no part from that list becomes a built-in USB device.

The answer comes from the ODDC commit `flake.lock` pins for the `oddc` input.
Moving the ODDC pin on a device is one operation:

```bash
gjallarctl rebuild --hardware-update
```

<details>
<summary><b>How ODDC updates work</b></summary>
<br>

`gjallarctl rebuild --hardware-update [--channel stable|staging] [--switch 0|1]`
runs `oddc update --flake <checkout> [--channel CHANNEL]` on the system
checkout first and stops with `nothing rebuilt` if that fails. `--channel`
moves the oddc input to the newest commit of that ODDC channel (`stable` when
left out); no commit hash is needed. `--switch 1` (the default) rebuilds and
switches right away; `--switch 0` only moves the pin, and the next
`gjallarctl rebuild` applies it. It needs an `oddc` that knows
`update --flake`. When the pinned commit differs from
`generated/oddc/revision`, the rebuild fetches the same model again at the new
commit and swaps it in only once complete. If that fetch fails, for example
offline, the rebuild stops with the error and changes nothing.
`gjallarctl oddc doctor` shows the ODDC revision the installed model came
from. `gjallarctl oddc update` is refused and points here, because
plain `oddc update` moves only the pin of `/etc/nixos` and leaves the system
checkout as it is. Always apply with `gjallarctl rebuild`. A plain
`sudo nixos-rebuild switch --flake` misses `generated/`: it is untracked, so
a plain git flake cannot see it.

A plain `gjallarctl rebuild` never moves the pin. It asks GitHub whether the
branch the oddc input follows has a newer commit and, if so, prints
`ODDC update available: OLD -> NEW. Apply with: gjallarctl rebuild --hardware-update`.
Offline or on any error it says nothing. Managed endpoints skip the check:
JODS moves their pin.

`gjallarctl oddc COMMAND` passes ODDC commands to `oddc` unchanged, for example
`gjallarctl oddc resolve` or `gjallarctl oddc explain --path PATH`. On the
installed system these default to its model. `gjallarctl oddc validate-device`
validates this checkout on the device. Validation evidence goes to ODDC through
`oddc contribute` and never into this repository.

</details>

## Rebuilds

`gjallarctl rebuild` builds only the system checkout `/etc/gjallar/system`,
from any directory. Root owns that checkout and builds it as it is on the
official origin; with developer mode on it also builds local changes in it.
A checkout anywhere else is never rebuilt: an install that still builds from
a folder in your home is moved with `gjallarctl system move` (see
[Upgrading](upgrading.md#moving-an-older-install-to-etcgjallarsystem)).
Other commands find their checkout through `--repo PATH`, `GJALLAROS_REPO`,
`/etc/gjallar/repository` (`dotfilesDir`) or the current directory, in that
order. Direct host flake builds fail unless the
source was staged by `gjallarctl`; installer and recovery deployment also use
that staging path. This prevents skipping the normal preparation workflow. It
is not an authorisation boundary against root or someone who can change the
source. Package builds and recovery ISO builds remain available directly.

To bring an install up to date with upstream, run:

```bash
gjallarctl rebuild --update
```

It fast-forwards that checkout to its release channel and then rebuilds.
The channel is `releaseChannel` in `user.config.json`: `stable` (the
default), `staging` or `dev`. Change it with
`gjallarctl settings set releaseChannel=staging`. A move to `stable` applies
at once; any other move waits like other critical settings.
Moving to `staging` or `dev`, or turning on `developerMode`, also asks for
the startup PIN or the recovery key once, after the password. Without a PIN,
press Enter and type the recovery key. Moving back to `stable` and turning
developer mode off ask for neither, and the next move away asks again.
A device that was on `staging` or `dev` before this check refuses changes
and rebuilds until you run `gjallarctl settings confirm-developer` once.
Local commits or uncommitted edits stop the update, and nothing is rebuilt.
A checkout that is ahead of its channel stays where it is until the channel
catches up; it never goes back to an older commit.
`gjallarctl rebuild --help` lists every option.

`--repo`, `--host`, `--update-inputs` and other `nixos-rebuild` arguments are
refused; only `-- --override-input oddc COMMIT` may follow, and the oddc pin
may move only to a commit of an official ODDC branch (ROADMAP §14).
