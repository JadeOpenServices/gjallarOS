#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 PARTITION IMAGE MANIFEST SIGNATURE PINNED_PUBLIC_KEY" >&2
  echo "       $0 --local-build PARTITION IMAGE" >&2
  echo "PARTITION must be a dedicated, unmounted exactly-12-GiB partition." >&2
  echo "This command never accepts, creates, shrinks, or formats a whole disk." >&2
  echo "--local-build installs an image this host just built with Nix; it has" >&2
  echo "no release signature, and the endpoint Secure Boot key signs its boot chain." >&2
  exit 2
}

local_build=false
if [ "${1:-}" = --local-build ]; then
  local_build=true
  shift
  [ "$#" -eq 2 ] || usage
else
  [ "$#" -eq 5 ] || usage
fi
[ "$(id -u)" -eq 0 ] || { echo "ERROR: run as root" >&2; exit 1; }
partition=$(realpath "$1")
image=$(realpath "$2")
if ! $local_build; then
  manifest=$(realpath "$3")
  signature=$(realpath "$4")
  public_key=$(realpath "$5")
fi

[ "$(lsblk -dnro TYPE "$partition")" = part ] || {
  echo "ERROR: target must be a partition, never a whole disk" >&2
  exit 1
}
part_label=$(lsblk -dnro PARTLABEL "$partition")
[ "$part_label" = JODS-RECOVERY ] || {
  echo "ERROR: target partition must have GPT label JODS-RECOVERY" >&2
  exit 1
}
part_type=$(lsblk -dnro PARTTYPE "$partition" | tr '[:upper:]' '[:lower:]')
[ "$part_type" = bc13c2ff-59e6-4262-a352-b275fd6f7172 ] || {
  echo "ERROR: JODS-RECOVERY must use the XBOOTLDR partition type" >&2
  exit 1
}
[ -z "$(lsblk -nro MOUNTPOINTS "$partition" | tr -d '[:space:]')" ] || {
  echo "ERROR: target partition is mounted" >&2
  exit 1
}
size=$(blockdev --getsize64 "$partition")
[ "$size" -eq 12884901888 ] || {
  echo "ERROR: recovery partition must be exactly 12 GiB" >&2
  exit 1
}

if $local_build; then
  case "$image" in
    /nix/store/*) ;;
    *) echo "ERROR: --local-build image must be a Nix store path" >&2; exit 1 ;;
  esac
else
  "$(dirname "$0")/verify-image.sh" "$image" "$manifest" "$signature" "$public_key"
fi
# Endpoints without an ODDC Secure Boot policy (generic profiles) have no
# per-device keys and boot with Secure Boot off; their recovery UKI stays
# unsigned (e2e-genluks, 2026-10-07). With Secure Boot on, an unsigned UKI
# would not start, so the keys are required there.
sign=false
if [ -r /var/lib/sbctl/keys/db/db.key ] && [ -r /var/lib/sbctl/keys/db/db.pem ]; then
  sign=true
else
  sb_var=/sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d0-aa5d-00a0c94e5b8c
  if [ -r "$sb_var" ] && [ "$(od -An -tu1 -j4 -N1 "$sb_var" | tr -d ' ')" = 1 ]; then
    echo "ERROR: Secure Boot is on but per-device signing keys are not installed" >&2
    exit 1
  fi
  echo "Secure Boot is off and no per-device keys exist; the recovery UKI stays unsigned."
fi
for command in mkfs.vfat xorriso findmnt efibootmgr; do
  command -v "$command" >/dev/null || {
    echo "ERROR: required command not found: $command" >&2
    exit 1
  }
done
! $sign || command -v sbctl >/dev/null || {
  echo "ERROR: required command not found: sbctl" >&2
  exit 1
}
parent_name=$(lsblk -dnro PKNAME "$partition")
part_number=$(lsblk -dnro PARTN "$partition")
[ -n "$parent_name" ] && [ -n "$part_number" ] || {
  echo "ERROR: cannot resolve parent disk and partition number" >&2
  exit 1
}
parent="/dev/$parent_name"
[ "$(lsblk -dnro PTTYPE "$parent")" = gpt ] || {
  echo "ERROR: recovery partition requires a GPT disk" >&2
  exit 1
}
# systemd-boot only lists XBOOTLDR entries on the disk it started from.
esp=$(findmnt -nro SOURCE --mountpoint /boot || true)
if [ -n "$esp" ] && [ "$(lsblk -dnro PKNAME "$esp")" != "$parent_name" ]; then
  echo "ERROR: recovery partition must be on the same disk as the ESP ($esp)" >&2
  exit 1
fi
echo "Target: $partition ($(numfmt --to=iec "$size"))"
# The installer creates JODS-RECOVERY empty or as vfat JODSRECOV and asks
# before it gets here; anything else on the partition needs typed consent.
# blkid -p exits 2 when it finds no signature. Ambiguous signatures (8) or a
# failed read are not an empty partition and need consent like any other
# content.
probe_rc=0
probe=$(blkid -p -o export "$partition" 2>/dev/null) || probe_rc=$?
case $probe_rc in
  0)
    fs_type=$(sed -n 's/^TYPE=//p' <<<"$probe")
    fs_label=$(sed -n 's/^LABEL=//p' <<<"$probe")
    ;;
  2) fs_type='' fs_label='' ;;
  *)
    echo "Cannot identify the content of $partition (blkid exit $probe_rc)." >&2
    fs_type=unknown fs_label=''
    ;;
esac
if ! $local_build || { [ -n "$fs_type" ] && [ "$fs_type:$fs_label" != vfat:JODSRECOV ]; }; then
  printf 'Type FORMAT-JODS-RECOVERY to erase that partition: '
  read -r answer
  [ "$answer" = FORMAT-JODS-RECOVERY ] || { echo "Cancelled." >&2; exit 1; }
fi

# Preserve the XBOOTLDR GUID validated above. Changing it to an ESP here would
# make discovery inconsistent and could cause firmware/bootloader ambiguity.
mkfs.vfat -F 32 -n JODSRECOV "$partition"
mount_dir=$(mktemp -d)
cleanup() {
  mountpoint -q "$mount_dir" && umount "$mount_dir"
  rmdir "$mount_dir"
}
trap cleanup EXIT
mount -o nodev,nosuid,noexec "$partition" "$mount_dir"
# The partition boots the image's UKI, which mounts the ISO file itself
# (gjallar.iso=) and checks its store against the hash in its cmdline
# (gjallar.verity=). The ISO's own GRUB needs shim under Secure Boot, and its
# files unpacked onto vfat have no iso9660 root to mount.
[ "$(stat -c '%s' "$image")" -lt 4294967296 ] || {
  echo "ERROR: recovery image does not fit a FAT32 file (4 GiB)" >&2
  exit 1
}
# systemd-boot lists Type #2 entries from EFI/Linux on the XBOOTLDR
# partition next to its own. Never use the removable-media path
# \EFI\BOOT\BOOTX64.EFI here: firmware that drops or ignores BootOrder
# starts any loader it finds there, and every boot landed in recovery (real
# HP 2026-10-08, book1 2026-10-10).
install -d "$mount_dir/EFI/Linux"
xorriso -osirrox on -indev "$image" \
  -extract /EFI/gjallar/recovery-partition.efi "$mount_dir/EFI/Linux/gjallar-recovery.efi" >/dev/null 2>&1
[ -f "$mount_dir/EFI/Linux/gjallar-recovery.efi" ] || {
  echo "ERROR: recovery image has no recovery-partition UKI" >&2
  exit 1
}
cp "$image" "$mount_dir/gjallar-recovery.iso"

# Sign the boot executable with this endpoint's key so firmware starts it
# under Secure Boot. No --save: sbctl would keep the temporary mount path in
# its file database, and every later `sbctl verify` reported it missing. Each
# recovery install signs again with the current key.
if $sign; then
  while IFS= read -r -d '' executable; do
    sbctl sign "$executable" >/dev/null
    sbctl verify "$executable" >/dev/null
  done < <(find "$mount_dir/EFI" -type f -iname '*.efi' -print0)
fi

install -d -m 0700 "$mount_dir/.gjallar-release"
if $local_build; then
  printf 'schema=1\nimage=%s\nsha256=%s\nsize=%s\nsource=local-nix-build\nstore-path=%s\n' \
    "$(basename "$image")" "$(sha256sum "$image" | cut -d' ' -f1)" \
    "$(stat -c '%s' "$image")" "$image" > "$mount_dir/.gjallar-release/manifest"
  chmod 0600 "$mount_dir/.gjallar-release/manifest"
else
  install -m 0600 "$manifest" "$mount_dir/.gjallar-release/manifest"
  install -m 0600 "$signature" "$mount_dir/.gjallar-release/manifest.sig"
  install -m 0644 "$public_key" "$mount_dir/.gjallar-release/recovery-signing-public.pem"
fi
sync -f "$mount_dir/EFI/Linux/gjallar-recovery.efi"

# Recovery is a systemd-boot menu entry, never a firmware boot entry of its
# own; boot-first.sh removes the one older installs created and puts the
# installed systemd-boot entry first.
if findmnt -rn --mountpoint /boot >/dev/null; then
  "$(dirname "$(realpath "${BASH_SOURCE[0]}")")/boot-first.sh" /boot
else
  echo "WARNING: /boot is not mounted; BootOrder left unchanged" >&2
fi

if $sign; then
  echo "Installed verified, endpoint-signed recovery environment (boot menu entry)."
else
  echo "Installed verified recovery environment (boot menu entry; unsigned, Secure Boot off)."
fi
