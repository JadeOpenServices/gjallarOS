# Our patched systemd. Only systemd itself is rebuilt (systemd.package, not
# an overlay); programs that link libsystemd keep the cached one.
# systemd.package also feeds udev and the initrd. package.nix builds it, so
# the flake's prebuilt set can cache the same derivation. Each patch below
# has its own test and its own "Remove when"; when the last one goes, drop
# this folder.
#
# ----------------------------------------------------------------------
#
# remove-before-add.patch: a USB stick written with dd shows only some of
# its new partitions, and the disk keeps the old label (Plane 185).
#
# Linked from: system/services/removable-media.nix. Writing images to sticks
# is what hits it; any disk written as a whole can.
# Written 2026-10-18 against nixpkgs 5dfba623, systemd 260.2.
# Checked 2026-10-20 for nixpkgs 8ce73368, systemd 260.5:
# reread-partition-table.c, udev-watch.c, blockdev-util.c and the test are
# unchanged since 260.2.
# Test: tests/nix/systemd-partition-reread-fix.nix (flake check
# systemd-partition-reread-fix).
#
# What goes wrong
#   1. When a program closes a whole disk it wrote to, udevd re-reads the
#      partition table (udev-watch.c: synthesize_change() calls
#      reread_partition_table() with REREADPT_FORCE_UEVENT). For GPT this
#      does not ask the kernel to re-read; it walks the new table in order
#      and removes, resizes or adds each partition with BLKPG
#      (src/shared/reread-partition-table.c: process_partition()).
#   2. The kernel refuses to add or grow a partition that overlaps one it
#      still knows. Partitions the new table drops, or that now carry a
#      different number, are only removed after the walk, and not at all
#      once an add failed. Writing Home Assistant OS over a NixOS ISO: the
#      new partition 1 overlaps the old partition 2, the add fails with
#      EBUSY, partition 1 never appears and the stale ones stay. The error
#      is only logged at debug level.
#   3. After changes, or after an error, no "change" uevent is sent for the
#      disk itself. udev keeps its old properties (ID_FS_LABEL,
#      ID_PART_TABLE_TYPE from the ISO), so lsblk and file managers show the
#      old label while blkid -p reads the new table.
#
# What this does
#   remove-before-add.patch reads the whole table first, then removes every
#   kernel partition that is gone or moved, then resizes, then adds. With
#   REREADPT_FORCE_UEVENT it always sends "change" for the disk, then for
#   the partitions it left alone. The patch extends systemd's own
#   test-reread-partition-table with both overlap cases.
#
# Upstream
#   systemd main still has the same order. No systemd issue exists; Bluefin
#   hit the same bug (projectbluefin/server#369) and works around it with a
#   udev rule that runs partx --delete. The report waits in the
#   human-session queue.
#
# Remove when
#   - the test's stock machine shows every new partition and the new disk
#     properties after a dd: drop the patch.
#   - systemd is no longer 260.5 (the assertion in package.nix stops the
#     build): check whether reread-partition-table.c still adds before it
#     removes and still skips the disk "change". If it does, rebase the
#     patch; if not, drop it.
#
# ----------------------------------------------------------------------
#
# tpm2-setup-lockout.patch: the TPM's dictionary attack lockout fails
# systemd-tpm2-setup.service, and the next rebuild reports it as a failed
# unit (Plane 333).
#
# Linked from: system/security/secure-boot/measured-boot.nix. Any machine
# with a TPM runs tpm2-setup at boot; the PIN on the disk unlock is what
# usually puts the chip in lockout.
# Written 2026-11-25 against systemd 260.5.
# Test: tests/nix/systemd-tpm2-lockout-fix.nix (flake check
# systemd-tpm2-lockout-fix).
#
# What goes wrong
#   After too many wrong PINs the security chip (TPM2) refuses PIN-protected
#   use for a while (dictionary attack lockout). systemd-tpm2-setup.service
#   anchors each NvPCR at boot with an extend that counts as such use. In
#   lockout the TPM answers TPM2_RC_LOCKOUT, tpm2-setup logs "Failed to
#   extend NvPCR index with anchor secret", exits 1 and the unit fails. The
#   next rebuild then ends with "the following units failed:
#   systemd-tpm2-setup.service" (seen on the T480s, 2026-11-23).
#
# What this does
#   tpm2-setup-lockout.patch turns TPM2_RC_LOCKOUT on the extend and on the
#   seal of a new anchor secret into ENOLCK. At boot the anchor secret comes
#   from the copy on the ESP; stock systemd skips that copy when the TPM
#   refuses to decrypt it, makes up a new secret and then fails to seal it
#   ("Failed to acquire anchor secret: State not recoverable", seen on the
#   e2e-fw13 VM, 2026-11-26). Had the seal worked, the new secret would
#   have replaced the stored copies. The patch stops there with ENOLCK when
#   any passed copy failed for the lockout. tpm2-setup then skips the NvPCR
#   with a notice when the extend, the seal or the unseal of the stored
#   anchor secret (already ENOLCK in stock systemd) hits the lockout, exits
#   EX_TEMPFAIL (75) and lets both tpm2-setup units accept that status. The NvPCR stays unanchored and refuses measurements
#   until a later boot anchors it, exactly as after the failure today; only
#   the unit result changes. Measuring into an NvPCR (systemd-pcrextend,
#   systemd-cryptsetup) still fails in lockout, now with ENOLCK instead of
#   ENOTRECOVERABLE; neither skips on ENOLCK. The units that run after
#   tpm2-setup only order After= it and never needed it to succeed. In the
#   test VM the lockout already stops the unseal; the T480s got one step
#   further, to the extend. The VM test covers the unseal, the copy passed
#   in as from the ESP, and the seal of a first secret; not the extend.
#
# Upstream
#   systemd v262 dropped the anchor secret (PR #42796, commit f0f36877,
#   merged 2026-07-28): NvPCRs are now set up only in the initrd, with a
#   signed write policy, and there is no unseal or anchor extend left to
#   hit the lockout. 260.x and 261.x still have the old code. The patch and
#   a report for the stable branches wait in the human-session queue
#   (book 753).
#
# Remove when
#   - nixpkgs moves to systemd 262 or later (the assertion in package.nix
#     stops the build): drop the patch and its test, then check once on a
#     TPM in lockout that systemd-tpm2-setup.service still passes.
#   - the test's stock run stops failing in lockout (it says so): drop the
#     patch.
{ pkgs, ... }:

{
  systemd.package = import ./package.nix { inherit pkgs; };
}
