#!/usr/bin/env bash
# usage: esp-check.sh MOUNTPOINT
#
# Exits 0 when MOUNTPOINT is a mounted FAT EFI system partition on a GPT
# disk, read from the disk itself. sbctl verify finds the ESP with lsblk,
# whose partition and file system types come from the udev database; the
# installer runs the verifier in the nixos-enter chroot, which has none, and
# sbctl stopped with "failed to find EFI system partition" (stick CP-C,
# 2026-11-16). The verifier checks the ESP here and names it to sbctl.
set -euo pipefail

[ "$#" -eq 1 ] || { echo "usage: $0 MOUNTPOINT" >&2; exit 2; }
mountpoint=$1

# --mountpoint matches the mount point exactly; a folder on another file
# system gives nothing.
source=$(findmnt -nro SOURCE --mountpoint "$mountpoint") || source=
fstype=$(findmnt -nro FSTYPE --mountpoint "$mountpoint") || fstype=
if [ -z "$source" ] || [ "$fstype" != vfat ]; then
  echo "ERROR: $mountpoint is not a mounted FAT file system" >&2
  exit 1
fi
# blkid -p reads the partition table from the disk, without udev.
parttype=$(blkid -p -s PART_ENTRY_TYPE -o value "$source" | tr '[:upper:]' '[:lower:]') || parttype=
scheme=$(blkid -p -s PART_ENTRY_SCHEME -o value "$source") || scheme=
if [ "$scheme" != gpt ] || [ "$parttype" != c12a7328-f81f-11d2-ba4b-00a0c93ec93b ]; then
  echo "ERROR: $source at $mountpoint is not an EFI system partition on a GPT disk" >&2
  exit 1
fi
