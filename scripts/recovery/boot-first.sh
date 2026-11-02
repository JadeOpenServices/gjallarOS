#!/usr/bin/env bash
# usage: boot-first.sh ESP_MOUNTPOINT
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
# Deletes the "GjallarOS Recovery" entries older installs created: firmware
# that rebuilds or ignores BootOrder started them ahead of the installed
# system (book1, 2026-10-10). Recovery is a systemd-boot menu entry now.
# Never deletes any other entry.
set -euo pipefail

[ "$#" -eq 1 ] || { echo "usage: $0 ESP_MOUNTPOINT" >&2; exit 2; }
mountpoint=$1
file=EFI/systemd/systemd-bootx64.efi
loader='\EFI\systemd\systemd-bootx64.efi'
label='Linux Boot Manager'
if [ -f "$mountpoint/EFI/nixos/shimx64.efi" ]; then
  file=EFI/nixos/shimx64.efi
  loader='\EFI\nixos\shimx64.efi'
  label='Linux Boot Manager (shim)'
fi

esp=$(findmnt -nro SOURCE --mountpoint "$mountpoint" || true)
[ -n "$esp" ] && [ -f "$mountpoint/$file" ] || {
  echo "ERROR: $mountpoint is not a mounted ESP with $file" >&2
  exit 1
}
loader_match=$(printf '%s' "$loader" | tr '[:upper:]' '[:lower:]')
esp_partuuid=$(lsblk -dnro PARTUUID "$esp" | tr '[:upper:]' '[:lower:]')

boot_entries() {
  local list
  # A failed read must not look like a missing entry, or a duplicate gets
  # created.
  list=$(efibootmgr -v) || return
  printf '%s\n' "$list" | tr '[:upper:]' '[:lower:]' |
    grep -F "gpt,$esp_partuuid," |
    grep -F "$loader_match" |
    sed -n 's/^boot\([0-9a-f]\{4\}\).*/\1/p'
  true
}

esp_entries=$(boot_entries | paste -sd, -)
if [ -z "$esp_entries" ]; then
  efibootmgr \
    --create-only \
    --disk "/dev/$(lsblk -dnro PKNAME "$esp")" \
    --part "$(cat "/sys/class/block/$(basename "$(realpath "$esp")")/partition")" \
    --label "$label" \
    --loader "$loader" >/dev/null
  esp_entries=$(boot_entries | paste -sd, -)
  [ -n "$esp_entries" ] || {
    echo "ERROR: UEFI boot entry for $file on $esp was not created" >&2
    exit 1
  }
fi

for entry in $(efibootmgr | tr '[:upper:]' '[:lower:]' |
  sed -n 's/^boot\([0-9a-f]\{4\}\)[* ] gjallaros recovery\(\t.*\)\{0,1\}$/\1/p'); do
  efibootmgr --quiet --bootnum "$entry" --delete-bootnum
  echo "Removed firmware entry Boot$entry (GjallarOS Recovery)"
done
# A failed read here would write a BootOrder with only our entries. grep exits
# 1 when every entry is ours, which is fine.
boot_order=$(efibootmgr | sed -n 's/^BootOrder: //p')
rest=$(printf '%s\n' "$boot_order" | tr '[:upper:]' '[:lower:]' | tr , '\n' |
  { grep -vxF -f <(printf '%s\n' "$esp_entries" | tr , '\n') || [ $? = 1 ]; } |
  paste -sd, -)
new_order=$(printf '%s\n' "$esp_entries" "$rest" | grep . | paste -sd, -)
efibootmgr --bootorder "$new_order" >/dev/null

first=$(efibootmgr | sed -n 's/^BootOrder: //p' | cut -d, -f1 | tr '[:upper:]' '[:lower:]')
case ",$esp_entries," in
  *",$first,"*) ;;
  *)
    echo "ERROR: firmware did not keep $file first in BootOrder" >&2
    exit 1
    ;;
esac
echo "PASS: BootOrder $(efibootmgr | sed -n 's/^BootOrder: //p')"
