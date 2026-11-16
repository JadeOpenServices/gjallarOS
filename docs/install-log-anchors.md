# Install log anchors

<!-- Generated from internal/installer/installlog/anchors.go. Update with: GJALLAR_UPDATE_DOCS=1 go test ./internal/installer/installlog -->

The installer and the installer stick's menu write a log of each run.
Fixed points of the run carry an anchor: a code that never changes and a
name that says what happened, for example
`GJA-1011 install.disk.only-one-confirmed`. Search a log for the code to
find where a run got to. Codes GJA-1000 to GJA-1999 come from the
installer, GJA-2000 to GJA-2999 from the stick's menu. Anchor fields only
hold numbers, yes/no or one of the words listed here, never text a user
typed.

| Code | Name | Meaning | Fields |
|---|---|---|---|
| GJA-1001 | `install.run.started` | The installer started and the log opened. | none |
| GJA-1002 | `install.run.host` | The installer found out what kind of system it runs on. | `kind` (one of live, installed, recovery): live media, an installed gjallarOS, or the recovery system |
| GJA-1010 | `install.disk.given` | The target disk came from --target-disk; nothing was asked. | none |
| GJA-1011 | `install.disk.only-one-confirmed` | Only one disk qualified and the user said yes to it. | none |
| GJA-1012 | `install.disk.chosen` | The user picked the target from several disks. | `disks` (count): how many disks qualified |
| GJA-1020 | `install.target.validated` | The target disk passed the safety checks; the disk plan is next. | none |
| GJA-1021 | `install.target.root-ready` | The new root file system is mounted at /mnt. | `encrypted` (yes/no): root is inside LUKS |
| GJA-1025 | `install.hardware.disk-swap-dropped` | Swap partitions that were on while the hardware configuration was made are left out; gjallarOS swaps to zram only. The partitions are not changed. | `swaps` (count): how many swap partitions were left out |
| GJA-1030 | `install.nixos-install.started` | nixos-install started building and copying the system. | none |
| GJA-1031 | `install.nixos-install.finished` | nixos-install finished without an error. | none |
| GJA-1040 | `install.checkout.cloned` | The system checkout was cloned into the target. | none |
| GJA-1041 | `install.fresh.finished` | The fresh install on the new disk is complete; Secure Boot steps may follow. | none |
| GJA-1050 | `install.secureboot.continue` | The Secure Boot part of the run started. | `path` (one of shim, shim-fresh, shim-sb-off, own-keys): shim on a running system, shim after a fresh install from the stick, shim while firmware Secure Boot is off, or own keys |
| GJA-1060 | `install.inplace.started` | The in-place install on the running system started building. | `path` (one of first, system-checkout, repo): first install with a new system checkout, an update of the system checkout, or a build from the installer's own checkout |
| GJA-1061 | `install.inplace.finished` | The in-place build and switch finished without an error. | none |
| GJA-1090 | `install.run.restart` | The installer restarts the machine to go on. | `into` (one of system, firmware-setup): gjallarOS or the firmware setup screen |
| GJA-1098 | `install.run.failed` | The run stopped with an error; the ERROR line above says why. | none |
| GJA-1099 | `install.run.finished` | The run ended. | `code` (count): exit code: 0 done, 1 failed, 2 wrong options, 130 stopped by the user |
| GJA-2001 | `stick.boot.started` | The installer stick's menu started for the first time this boot. | `secureboot` (yes/no): the firmware booted with Secure Boot on |
| GJA-2010 | `stick.display.started` | The stick's menu is on the screen. | `mode` (one of window, console): a window on the screen, or the text console |
| GJA-2020 | `stick.network.online` | The stick has a network connection. | `via` (one of ethernet, wifi): a cable or Wi-Fi |
| GJA-2021 | `stick.network.look-again` | The user asked the stick to look for a network again. | `tries` (count): how many times so far |
| GJA-2022 | `stick.network.none` | The user went on without a network connection. | none |
| GJA-2030 | `stick.menu.chosen` | The user picked an entry of the stick's menu. | `choice` (one of install, move-disk, bring-over, recovery, shell): install, move to a new disk, bring an old system over, recovery, or the shell |
| GJA-2031 | `stick.channel.advanced` | The user changed the release channel under Advanced. | `channel` (one of stable, staging, dev): the channel the install will follow |
| GJA-2098 | `stick.flow.failed` | A step of the stick's menu failed; the ERROR line above says why. | none |
