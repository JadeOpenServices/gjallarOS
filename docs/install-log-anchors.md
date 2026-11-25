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
| GJA-1032 | `install.nixos-install.retry` | nixos-install ended with an error during a fresh install and the person was asked whether to run it again on the same disk. | `answer` (one of try-again, stop): run nixos-install again, or stop and keep the failure |
| GJA-1040 | `install.checkout.cloned` | The system checkout was cloned into the target. | none |
| GJA-1041 | `install.fresh.finished` | The fresh install on the new disk is complete; Secure Boot steps may follow. | none |
| GJA-1050 | `install.secureboot.continue` | The Secure Boot part of the run started. | `path` (one of shim, shim-fresh, shim-sb-off, own-keys): shim on a running system, shim after a fresh install from the stick, shim while firmware Secure Boot is off, or own keys |
| GJA-1060 | `install.inplace.started` | The in-place install on the running system started building. | `path` (one of first, system-checkout, repo): first install with a new system checkout, an update of the system checkout, or a build from the installer's own checkout |
| GJA-1061 | `install.inplace.finished` | The in-place build and switch finished without an error. | none |
| GJA-1090 | `install.run.restart` | The installer restarts the machine to go on. | `into` (one of system, firmware-setup): gjallarOS or the firmware setup screen |
| GJA-1091 | `install.run.boot-next` | Before the restart, the installer pointed the next start at the new system, so a stick still plugged in does not start again. | `result` (one of set, none, failed): set: the next start goes to the new system; none: no firmware entry for it was found; failed: setting it did not work (the WARNING line says why) |
| GJA-1098 | `install.run.failed` | The run stopped with an error; the ERROR line above says why. | none |
| GJA-1099 | `install.run.finished` | The run ended. | `code` (count): exit code: 0 done, 1 failed, 2 wrong options, 130 stopped by the user |
| GJA-2001 | `stick.boot.started` | The installer stick's menu started for the first time this boot. | `secureboot` (yes/no): the firmware booted with Secure Boot on |
| GJA-2010 | `stick.display.started` | The stick's menu is on the screen. | `mode` (one of window, console): a window on the screen, or the text console |
| GJA-2020 | `stick.network.online` | The stick has a network connection. | `via` (one of ethernet, wifi): a cable or Wi-Fi |
| GJA-2021 | `stick.network.look-again` | The user asked the stick to look for a network again. | `tries` (count): how many times so far |
| GJA-2022 | `stick.network.none` | The user went on without a network connection. | none |
| GJA-2030 | `stick.menu.chosen` | The user picked an entry of the stick's menu. | `choice` (one of install, move-disk, bring-over, recovery, requeue-key, shell): install, move to a new disk, bring an old system over, recovery, or the shell |
| GJA-2031 | `stick.channel.advanced` | The user changed the release channel under Advanced. | `channel` (one of stable, staging, dev): the channel the install will follow |
| GJA-2040 | `stick.release.refused` | The stick did not start the installer: it could not show that it is still safe to install from. Nothing on the computer was changed. | `reason` (one of floor, no-key, bad, unreachable): floor: the stick is older than the oldest release still safe; no-key: built without the release key; bad: the release files did not match the signature; unreachable: the release files could not be fetched |
| GJA-2041 | `stick.release.older` | A newer release is out; the stick is still safe to install from and went on. | none |
| GJA-2050 | `stick.installed.start` | The user chose Start installed gjallarOS on the stick's menu. | `result` (one of set, none, failed): set: the stick restarts into the installed system; none: no installed system was found anymore; failed: the next start could not be set |
| GJA-2098 | `stick.flow.failed` | A step of the stick's menu failed; the ERROR line above says why. | none |
| GJA-2101 | `stick.move.started` | The disk move started on the stick. | `mode` (one of new-disk, bring-over): new-disk: the system moves to a new disk in this computer; bring-over: an old disk's system comes to this computer |
| GJA-2102 | `stick.move.prepare-found` | The opened old disk holds a record of gjallarctl move prepare made for its current encryption header. | `pin` (yes/no): the startup PIN was checked on the old computer, so the stick skips its PIN notice |
| GJA-2103 | `stick.move.prepare-missing` | The opened old disk holds no usable record of gjallarctl move prepare. The move goes on as without one. | `reason` (one of none, stale, unreadable): none: prepare was not run; stale: the encryption header changed after prepare (new PIN or chip setup); unreadable: the record is damaged or not a plain file |
| GJA-2104 | `stick.move.header-refused` | The old disk's encryption header was refused before any key was asked. Nothing was opened. | `reason` (one of reencrypt, unknown-token, kdf-memory, not-luks2, unsupported): reencrypt: a change of the encryption was not finished; unknown-token: an unlock method gjallarOS never sets up; kdf-memory: unlocking needs more memory than is free; not-luks2: no LUKS2 header or a damaged one; unsupported: encryption settings gjallarOS does not set up |
| GJA-2105 | `stick.move.layout-refused` | The old disk opened, but its contents were refused. It was closed again. | `reason` (one of not-btrfs, no-evidence, managed, bad-config): not-btrfs: no gjallarOS file system; no-evidence: not a complete gjallarOS with the current layout; managed: the organisation moves this computer; bad-config: the old settings file does not pass this stick's schema check |
| GJA-2106 | `stick.move.unlock-ok` | The old disk opened with the key the user typed. | `tries` (count): how many tries it took |
| GJA-2107 | `stick.move.unlock-failed` | No key the user typed opened the old disk; the move stopped. | `tries` (count): how many tries were made |
| GJA-2108 | `stick.move.old-refused` | The stick could not work with the old disk. | `reason` (one of busy, old-cryptsetup, not-open, too-slow): busy: an old disk is already open, nothing was opened; old-cryptsetup: the stick's disk tools are too old to open it safely, nothing was opened; not-open: a later step found the old disk no longer open; too-slow: one key try ran over its time limit, was stopped and nothing stayed open |
| GJA-2110 | `stick.move.pin-unchecked` | The old disk also unlocks with a startup PIN that the stick cannot check; the user read the warning and went on. Identity items start as "make new". | none |
| GJA-2111 | `stick.move.space-ok` | What the user chose to move fits on the new disk, with the reserve left free. | `needed` (count): GiB needed, rounded up: the chosen categories, the store import, the file cache and the reserve<br>`free` (count): GiB free on the planned root after the swap and the base system, rounded down |
| GJA-2112 | `stick.move.space-short` | What the user chose to move does not fit on the new disk. The stick asks to leave something behind or to stop; nothing is erased. | `short` (count): GiB missing, rounded up |
| GJA-2113 | `stick.move.nextcloud-warned` | The Nextcloud folders are above 50 GiB or above a quarter of the free space, and the stick asked first. | `answer` (one of copy, skip): copy: the folders are copied with their sync state; skip: they stay behind and sync again from the server after the move |
| GJA-2114 | `stick.move.identity-choice` | The user answered one identity screen. | `item` (one of wifi, wifi-enterprise, vpn, bluetooth, fingerprints, tailscale, ssh-host-keys, machine-id, nextcloud): the identity item<br>`answer` (one of keep, new): keep: it comes along to the new disk; new: it stays behind and the new system starts without it |
| GJA-2115 | `stick.move.exported` | The old system's store paths were copied into a cache on the new disk for the install, or the copy was skipped and the install downloads everything. | `paths` (count): store paths in the cache<br>`gib` (count): GiB in the cache, rounded up<br>`skipped` (yes/no): true: nothing was copied and the install went on without the cache |
| GJA-2116 | `stick.move.verified` | Every store path of the new system matches the hash its database row records. | none |
| GJA-2117 | `stick.move.verify-failed` | A store path of the new system does not match its recorded hash; the move stopped before anything was copied from the old home. | none |
| GJA-2118 | `stick.move.repo-copied` | The local branches and stashes of the old system checkout were carried into the new one, or the copy was refused or rolled back. | `branches` (count): local branches found<br>`moved` (count): branches carried over<br>`stashes` (count): stashes carried over<br>`skipped` (count): branches left behind<br>`refused` (yes/no): true: the old checkout was refused and nothing was tried<br>`failed` (yes/no): true: the copy failed and what it wrote was taken back |
| GJA-2119 | `stick.move.ended` | The old disk is closed and the new disk is no longer shown to the move helper. | none |
| GJA-2120 | `stick.move.home-copied` | The old account's home folder was copied into the new account's home, or the copy failed and what it wrote was taken back. | `files` (count): files copied<br>`folders` (count): folders copied<br>`links` (count): links copied as links<br>`left` (count): Nextcloud folders left behind on purpose<br>`skipped` (count): pipes, sockets and devices left behind<br>`foreign` (count): entries owned by another account, left behind<br>`sync` (yes/no): true: the Nextcloud folders were asked to come along<br>`failed` (yes/no): true: the copy failed and what it wrote was taken back |
| GJA-2121 | `stick.move.identity-written` | The identity items the user kept were written onto the new disk; the ones made new were left out. | `kept` (count): items the user chose to keep<br>`written` (count): kept items now on the new disk<br>`failed` (count): kept items that stay behind, told on screen<br>`sync` (count): Nextcloud folders made again, empty, to download from the server<br>`left` (count): network profiles and Bluetooth pairings of kept items that stay behind, refused by the allowlist |
| GJA-3001 | `maker.run.started` | The USB stick maker started. | `os` (one of linux, windows): the system the maker runs on |
| GJA-3002 | `maker.key.confirmed` | The user typed yes: the release key fingerprint the maker showed matches the README. | none |
| GJA-3003 | `maker.key.declined` | The user did not confirm the release key fingerprint; nothing was downloaded or written. | none |
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
