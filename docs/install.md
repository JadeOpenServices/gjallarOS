# Install details

The [Quick start](../README.md#quick-start) covers a normal install. This
page covers release channels, preset answers and where the installed system
keeps its checkout.

## Release channels

`stable` is the default. For `staging` or `dev`, add `--channel` (or set
`GJALLAR_CHANNEL`):

```bash
curl -fsSL https://raw.githubusercontent.com/JadeOpenServices/gjallarOS/staging/scripts/installation/install.sh | bash -s -- --channel staging
```

The installed system follows the same channel. When the script runs again,
it moves its checkout forward to the newest commit of the channel and
resumes where the last run stopped. Local edits or local commits on that
branch stop it before anything changes.

Choosing `staging` or `dev` needs one confirmation with the recovery key
or the startup PIN. Rebuilds wait until it is done. With security chip
(TPM2) unlock this happens at the first start, when you set the PIN.
Without it, run `gjallarctl luks recovery-key`, then
`gjallarctl settings confirm-developer` with that key. The disk passphrase
does not count.

## Preset answers

The installer can take its non-secret answers from a file:

```bash
cp scripts/installation/user_PresetJSON/default.user.config.json user.config.json
```

Edit the copy, then run the installer. A preset with a different
`releaseChannel` than the chosen channel stops the installer. Secrets, LUKS
passphrases, recovery settings, generated hardware data and backups stay out
of Git. [scripts/installation/README.md](../scripts/installation/README.md)
lists what the preset fields mean.

## System checkout

The checkout in `~/Documents` only runs the installer. The installed system
gets a clone of the official repository in `/etc/gjallar/system`, owned by
root, at the commit the installer was built from. This machine's
`generated/` and `user.config.json` are copied in. An installer with local
changes, or built from a commit outside `stable`, `staging` and `dev`,
stops before this step.

> [!TIP]
> After the install, apply changes with `gjallarctl rebuild`. A plain
> `sudo nixos-rebuild switch --flake` cannot see the untracked
> `generated/` folder, see [Rebuilds](configuration.md#rebuilds).

## Run log

Each installer run keeps a log in RAM while it runs. When the run ends, or
before it restarts the machine, the installed system gets a copy at
`/var/log/gjallar-install.log`, readable by root only. A later run on the
same system replaces it. A live run that never reached a target disk keeps
its log in RAM only.

The log is the terminal text, cleaned of colour codes and progress lines.
The Nix build steps (`nixos-install`, `nixos-rebuild`, `nix-channel`) keep
their progress bar on screen; the log gets only the last state of each bar
line. Only their output is logged, never what you type while they run.
Before a line is written, URL credentials, tokens and key-like strings are
hidden, and so are your user name, name, host name, email, GitHub name and
disk serials. Passwords are typed with echo off and never reach it. The
Secure Boot recovery passphrase is shown on the terminal only, never
written to the log.

Fixed points of the run carry a code such as `GJA-1012`. Search the log for
a code to see how far a run got; [Install log anchors](install-log-anchors.md)
lists them all.

## When an install fails

A failed run shows its install report on screen first. One question then
offers to save the report and the install log, with the same personal values
hidden, to a USB drive. The list shows only writable USB drives with a FAT
or exFAT partition. It never shows the disk the run installs to or a drive
the system uses. On the boot stick made by the USB maker, its `GJLOGS`
partition comes first; a stick written with plain `dd` has no such
partition, so plug in a second stick. Another gjallarOS stick shows only
its `GJLOGS` partition, further down the list, never its boot partition.
The default answer saves nothing.

The installer mounts the chosen partition for one moment, writes both files
readable by their owner only, and unmounts it again. Read the files on
another computer before you send them on. An installed system that runs the
installer again offers its home folder instead of a drive.

When the system install stops because Nix gave up on a download, the error
line names the server the download came from, for example "nixos-install
stopped because a download from github.com failed". The Nix error just above
it gives the reason. On the release cache, which lets downloads resume, Nix
picks a broken download up where it stopped and only gives up after five
tries in a row that got no data at all (Plane 431). So this error means the
network or the server stopped answering, not that the disk was slow.

On a fresh install, when `nixos-install` itself ends with an error, the
installer asks "What next?" before the report, with "Try again" as the
default. Try again runs `nixos-install` once more on the disk that is still
open from this run. Nix keeps every path it already has, so only the missing
ones download. Choose Stop when the same error comes back; the run then ends
with the report as before.

Limits of Try again:

- It is offered only when a person sits at the install: not on a resumed,
  unattended or managed run.
- It is offered only for a failure of `nixos-install` itself. A failed check
  before it, such as a target that is not mounted, ends the run as before.
- It works only in the same stick session. After a restart the next install
  erases the disk again, because the installer cannot prove a half-written
  disk is the one it set up.
- The question waits until Nix gives up. When a server stops answering in the
  middle of a download, Nix waits up to five minutes per try and tries five
  times, so the screen can sit still for about 25 minutes before "What
  next?" shows. Nothing is stuck; leave the machine running.
