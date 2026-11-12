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
