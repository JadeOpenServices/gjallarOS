#!/usr/bin/env bash
# usage: boot-first.sh [--keep-order] ESP_MOUNTPOINT
#
# Make the systemd-boot entry of the ESP mounted at ESP_MOUNTPOINT the first
# UEFI boot target, recreating it when missing, with every other entry after.
# When lzbt installed shim (secureBootShim, firmware with fixed keys, Plane
# 293), the entry starts EFI/nixos/shimx64.efi, which loads systemd-boot:
# that firmware refuses systemd-boot itself. lzbt removes shim again when the
# setting is off, so the file tells which one this ESP boots.
# On the HP (2026-10-08) BootOrder held neither Linux Boot Manager entry, so
# every boot landed in recovery, and bootctl install appended the restored
# entry behind USB, PXE and recovery.
#
# --keep-order is for every bootloader install (lanzaboote.nix): it writes
# nothing when the entry already starts the right file and no old entry is
# left, so a plain rebuild leaves NVRAM and the user's boot order alone.
# Switching secureBootShim on or off is a rebuild; the entry for the other
# file then gets replaced in its place in BootOrder. Without this the firmware kept starting
# systemd-boot directly, refused it under fixed keys and only booted
# through the removable-media fallback, which not every firmware tries
# (e2e-fw13 with maker-only keys, 2026-11-02).
#
# Deletes the "GjallarOS Recovery" entries older installs created: firmware
# that rebuilds or ignores BootOrder started them ahead of the installed
# system (book1, 2026-10-10). Recovery is a systemd-boot menu entry now.
# Deletes the entries on this ESP that start the other of our two loader
# files. Never deletes any other entry.
set -euo pipefail

keep_order=
if [ "${1-}" = --keep-order ]; then
  keep_order=1
  shift
fi
[ "$#" -eq 1 ] || { echo "usage: $0 [--keep-order] ESP_MOUNTPOINT" >&2; exit 2; }
mountpoint=$1
systemd_boot='\EFI\systemd\systemd-bootx64.efi'
shim='\EFI\nixos\shimx64.efi'
file=EFI/systemd/systemd-bootx64.efi
loader=$systemd_boot
other=$shim
label='Linux Boot Manager'
if [ -f "$mountpoint/EFI/nixos/shimx64.efi" ]; then
  file=EFI/nixos/shimx64.efi
  loader=$shim
  other=$systemd_boot
  label='Linux Boot Manager (shim)'
fi

esp=$(findmnt -nro SOURCE --mountpoint "$mountpoint" || true)
[ -n "$esp" ] && [ -f "$mountpoint/$file" ] || {
  echo "ERROR: $mountpoint is not a mounted ESP with $file" >&2
  exit 1
}
lower() { tr '[:upper:]' '[:lower:]'; }
esp_partuuid=$(lsblk -dnro PARTUUID "$esp" | lower)

# Entry numbers on this ESP that start the loader file $1.
boot_entries() {
  local list match
  match=$(printf '%s' "$1" | lower)
  # A failed read must not look like a missing entry, or a duplicate gets
  # created.
  list=$(efibootmgr -v) || return
  printf '%s\n' "$list" | lower |
    grep -F "gpt,$esp_partuuid," |
    grep -v '^boot[0-9a-f]\{4\}[* ] gjallaros recovery' |
    grep -F "$match" |
    sed -n 's/^boot\([0-9a-f]\{4\}\).*/\1/p'
  true
}
in_list() { case ",$2," in *",$1,"*) return 0 ;; esac; return 1; }

esp_entries=$(boot_entries "$loader" | paste -sd, -)
stale_entries=$(boot_entries "$other" | paste -sd, -)
recovery_entries=$(efibootmgr | lower |
  sed -n 's/^boot\([0-9a-f]\{4\}\)[* ] gjallaros recovery\(\t.*\)\{0,1\}$/\1/p' |
  paste -sd, -)
if [ -n "$keep_order" ] && [ -n "$esp_entries" ] && [ -z "$stale_entries$recovery_entries" ]; then
  exit 0
fi
if [ -z "$esp_entries" ]; then
  efibootmgr \
    --create-only \
    --disk "/dev/$(lsblk -dnro PKNAME "$esp")" \
    --part "$(cat "/sys/class/block/$(basename "$(realpath "$esp")")/partition")" \
    --label "$label" \
    --loader "$loader" >/dev/null
  esp_entries=$(boot_entries "$loader" | paste -sd, -)
  [ -n "$esp_entries" ] || {
    echo "ERROR: UEFI boot entry for $file on $esp was not created" >&2
    exit 1
  }
fi

# Read before the deletes: the replaced entry's place is where ours goes.
old_order=$(efibootmgr | sed -n 's/^BootOrder: //p' | lower)
for entry in ${stale_entries//,/ }; do
  efibootmgr --quiet --bootnum "$entry" --delete-bootnum
  echo "Removed firmware entry Boot$entry ($other)"
done
for entry in ${recovery_entries//,/ }; do
  efibootmgr --quiet --bootnum "$entry" --delete-bootnum
  echo "Removed firmware entry Boot$entry (GjallarOS Recovery)"
done

new_order=()
placed=
if [ -z "$keep_order" ]; then
  new_order=(${esp_entries//,/ })
  placed=1
fi
for entry in ${old_order//,/ }; do
  if in_list "$entry" "$esp_entries" || in_list "$entry" "$stale_entries"; then
    if [ -z "$placed" ]; then
      new_order+=(${esp_entries//,/ })
      placed=1
    fi
    continue
  fi
  in_list "$entry" "$recovery_entries" && continue
  new_order+=("$entry")
done
[ -n "$placed" ] || new_order=(${esp_entries//,/ } "${new_order[@]}")
efibootmgr --bootorder "$(IFS=,; printf '%s' "${new_order[*]}")" >/dev/null

boot_order=$(efibootmgr | sed -n 's/^BootOrder: //p' | lower)
first=$(printf '%s\n' "$boot_order" | cut -d, -f1)
for entry in ${esp_entries//,/ }; do
  in_list "$entry" "$boot_order" || {
    echo "ERROR: firmware did not keep $file in BootOrder" >&2
    exit 1
  }
done
if [ -z "$keep_order" ] && ! in_list "$first" "$esp_entries"; then
  echo "ERROR: firmware did not keep $file first in BootOrder" >&2
  exit 1
fi
echo "PASS: BootOrder $(efibootmgr | sed -n 's/^BootOrder: //p')"
