# A USB stick written with dd shows only some of its new partitions, and
# the disk keeps the old label (Plane 185).
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
#   test-reread-partition-table with both overlap cases. Only systemd itself
#   is rebuilt (systemd.package, not an overlay); programs that link
#   libsystemd keep the cached one. systemd.package also feeds udev and the
#   initrd. package.nix builds it, so the flake's prebuilt set can cache
#   the same derivation.
#
# Upstream
#   systemd main still has the same order. No systemd issue exists; Bluefin
#   hit the same bug (projectbluefin/server#369) and works around it with a
#   udev rule that runs partx --delete. The report waits in the
#   human-session queue.
#
# Remove when
#   - the test's stock machine shows every new partition and the new disk
#     properties after a dd: drop the fix.
#   - systemd is no longer 260.5 (the assertion in package.nix stops the
#     build): check whether reread-partition-table.c still adds before it
#     removes and still skips the disk "change". If it does, rebase the
#     patch and update the version; if not, drop the fix.
{ pkgs, ... }:

{
  systemd.package = import ./package.nix { inherit pkgs; };
}
