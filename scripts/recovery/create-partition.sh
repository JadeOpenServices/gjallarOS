#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 GPT_DISK [SIZE_GIB]" >&2
  echo "Creates the GJALLAR-RECOVERY partition only inside existing unallocated space." >&2
  echo "It never shrinks or reformats an existing partition." >&2
  exit 2
}

[ "$#" -ge 1 ] && [ "$#" -le 2 ] || usage
[ "$(id -u)" -eq 0 ] || { echo "ERROR: run as root" >&2; exit 1; }
disk=$(realpath "$1")
size_gib=${2:-12}

[ "$(lsblk -dnro TYPE "$disk")" = disk ] || {
  echo "ERROR: target must be a whole disk for partition-table inspection" >&2
  exit 1
}
[ "$(lsblk -dnro PTTYPE "$disk")" = gpt ] || {
  echo "ERROR: target must already use GPT" >&2
  exit 1
}
[ "$size_gib" = 12 ] || {
  echo "ERROR: the recovery partition size must be exactly 12 GiB" >&2
  exit 1
}
command -v sgdisk >/dev/null || { echo "ERROR: sgdisk is required" >&2; exit 1; }

existing=$(lsblk -pnro PATH,PARTLABEL "$disk" | awk '$2 == "GJALLAR-RECOVERY" { print $1; exit }')
if [ -n "$existing" ]; then
  echo "Existing recovery partition: $existing"
  exit 0
fi

sector_size=$(blockdev --getss "$disk")
free_start=$(sgdisk -F "$disk")
free_end=$(sgdisk -E "$disk")
required_sectors=$((size_gib * 1024 * 1024 * 1024 / sector_size))
available_sectors=$((free_end - free_start + 1))
[ "$available_sectors" -ge "$required_sectors" ] || {
  echo "ERROR: no unallocated block large enough for ${size_gib} GiB" >&2
  echo "Existing filesystems will not be shrunk automatically." >&2
  exit 1
}

echo "Disk: $disk"
echo "Plan: allocate ${size_gib} GiB from unallocated sectors; preserve all existing partitions"
printf 'Type CREATE-GJALLAR-RECOVERY to modify the GPT: '
read -r answer
[ "$answer" = CREATE-GJALLAR-RECOVERY ] || { echo "Cancelled." >&2; exit 1; }

# Freedesktop XBOOTLDR: a dedicated boot payload partition, not a generic ESP.
sgdisk --new="0:${free_start}:+${size_gib}G" \
  --typecode=0:bc13c2ff-59e6-4262-a352-b275fd6f7172 \
  --change-name=0:GJALLAR-RECOVERY \
  "$disk"
partprobe "$disk"
udevadm settle

partition=$(lsblk -pnro PATH,PARTLABEL "$disk" | awk '$2 == "GJALLAR-RECOVERY" { print $1; exit }')
[ -n "$partition" ] || {
  echo "ERROR: partition was created but its device node was not found" >&2
  exit 1
}
echo "Created: $partition"
