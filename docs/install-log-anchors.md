# Install log anchors

<!-- Generated from internal/installer/installlog/anchors.go. Update with: GJALLAR_UPDATE_DOCS=1 go test ./internal/installer/installlog -->

The installer, the installer stick's menu and the USB stick makers write
a log of each run.
Fixed points of the run carry an anchor: a code that never changes and a
name that says what happened, for example
`GJA-1011 install.disk.only-one-confirmed`. Search a log for the code to
find where a run got to. Codes GJA-1000 to GJA-1999 come from the
installer, GJA-2000 to GJA-2999 from the stick's menu, GJA-3000 to
GJA-3999 from the USB stick makers. Anchor fields only hold numbers,
yes/no or one of the words listed here, never text a user typed.

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
| GJA-3001 | `maker.run.started` | The USB stick maker started. | `os` (one of linux, windows): the system the maker runs on |
| GJA-3010 | `maker.download.finished` | The stick image and its checksum files are downloaded. | none |
| GJA-3011 | `maker.signature.verified` | The signature and every checksum the maker needs are correct. | none |
| GJA-3012 | `maker.signature.bad` | The signature or a checksum did not match; the download was deleted. | none |
| GJA-3013 | `maker.release.refused` | The signed release is older than one this computer or the maker already knows; the download was deleted and nothing was written. | `reason` (one of floor, rollback): floor: below the oldest release still safe; rollback: offered as newest but older than one used before |
| GJA-3014 | `maker.release.older` | The user went on with an older release after a warning. | `reason` (one of seen, date): seen: picked by name though a newer one was used before; date: signed more than 90 days ago and the user typed OLD |
| GJA-3020 | `maker.disk.chosen` | The user picked the USB stick to write. | `disks` (count): how many USB sticks the maker offered |
| GJA-3021 | `maker.elevation.declined` | Admin rights for the write were refused or not available; nothing was written. | none |
| GJA-3022 | `maker.disk.busy` | Windows could not lock a volume on the stick because a program has it open; nothing was written and the maker offers to try again. | none |
| GJA-3023 | `maker.disk.external` | The chosen USB disk looks like an external hard disk or SSD (not marked removable, or bigger than sticks usually are), and the user typed EXTERNAL to erase it anyway. | none |
| GJA-3030 | `maker.write.finished` | The image is written to the stick. | none |
| GJA-3031 | `maker.readback.verified` | The stick reads back the same bytes that were signed. | none |
| GJA-3032 | `maker.readback.mismatch` | The stick read back different bytes; its first MiB was wiped so it does not boot. | none |
| GJA-3033 | `maker.write.ended` | The admin process for the write ended without one of its own result codes, for example because Windows Security stopped it; the maker does not count the stick as written. | `code` (count): the exit code Windows reported |
| GJA-3040 | `maker.logs.created` | The GJLOGS partition for install logs is on the stick. | none |
| GJA-3041 | `maker.logs.skipped` | The stick got no GJLOGS partition; the installer offers to save logs to another drive instead. | `reason` (one of too-small, layout): the stick has no room after the image, or the image's partition table leaves no free slot |
| GJA-3098 | `maker.run.failed` | A step of the maker failed; the ERROR line above says why. | none |
| GJA-3099 | `maker.run.finished` | The maker ended. | `code` (count): exit code: 0 done, 1 failed, 2 wrong options, 130 stopped by the user |
