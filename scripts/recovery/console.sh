# GjallarOS recovery console. The recovery partition starts this on tty1
# instead of a login prompt. Firmware boots that partition even when USB boot
# is locked, so a shell here first needs the disk's recovery key or
# passphrase. Without it, the only way on is to erase the disk and reinstall.

# The menu ignores Ctrl-C; the recovery shell gets it back.
trap '' INT QUIT TSTP

mapping=gjallar-recovery-root

# LUKS partitions on internal disks. An encrypted USB backup drive plugged in
# during recovery is neither unlocked nor erased.
internal_luks() {
  lsblk -rpno PATH,FSTYPE | while read -r path fstype; do
    [ "$fstype" = crypto_LUKS ] || continue
    disk=$(lsblk -rpndo PKNAME "$path")
    [ -n "$disk" ] || disk=$path
    [ "$(lsblk -rndo TRAN "$disk")" != usb ] || continue
    [ "$(lsblk -rndo RM "$disk")" = 0 ] || continue
    printf '%s\n' "$path"
  done
}

pick_device() {
  mapfile -t devices < <(internal_luks)
  case ${#devices[@]} in
    0) return 1 ;;
    1)
      device=${devices[0]}
      return 0
      ;;
  esac
  printf '%s\n' 'Encrypted partitions:'
  for i in "${!devices[@]}"; do
    printf '  %d) %s\n' $((i + 1)) "${devices[$i]}"
  done
  printf 'Number: '
  read -r choice || return 1
  case $choice in
    '' | *[!0-9]*) return 1 ;;
  esac
  [ "$choice" -ge 1 ] && [ "$choice" -le ${#devices[@]} ] || return 1
  device=${devices[$((choice - 1))]}
}

# Keys typed while no prompt is up are neither shown nor taken as the next
# answer: a recovery key typed after its prompt had gone stayed on the screen
# and went to the menu (e2e-fw13, 2026-10-28).
drop_typeahead() {
  while read -r -s -t 0.05 -n 4096 _; do :; done
  stty echo
}

close_system() {
  if findmnt -rn --mountpoint /mnt >/dev/null 2>&1; then
    umount -R /mnt || return
  fi
  if [ -e "/dev/mapper/$mapping" ]; then
    cryptsetup close "$mapping"
  fi
}

open_system() {
  udevadm settle
  if [ -z "$(internal_luks)" ]; then
    # A disk that has not shown up yet must not open the shell unchecked.
    printf '%s\n' 'No encrypted disk found, so there is nothing to sign in with.' \
      'Boot installer media to repair an unencrypted install.'
    return
  fi
  pick_device || {
    printf '%s\n' 'No partition chosen.'
    return
  }
  stty -echo
  if ! gjallar-recover open-root "$device" "$mapping"; then
    # Each wrong guess costs time on top of the LUKS key derivation.
    sleep 5
    drop_typeahead
    return
  fi
  drop_typeahead
  printf '%s\n' '' \
    'Recovery shell as root. The installed system is at /mnt.' \
    'gjallar-recover lists the repair commands. exit returns to the menu.' ''
  (
    trap - INT QUIT TSTP
    cd /root && HOME=/root exec bash --login
  )
  close_system
}

erase_and_reinstall() {
  udevadm settle
  mapfile -t devices < <(internal_luks)
  printf '%s\n' '' \
    'This erases the encrypted disk of this device. Its files cannot be' \
    'recovered afterwards, not even with the recovery key.'
  [ ${#devices[@]} -eq 0 ] || printf '  %s\n' "${devices[@]}"
  printf '%s\n' 'The GjallarOS installer starts afterwards.'
  printf 'Type ERASE-THIS-DEVICE to continue: '
  read -r answer || return
  [ "$answer" = ERASE-THIS-DEVICE ] || {
    printf '%s\n' 'Cancelled.'
    return
  }
  close_system || return
  for device in "${devices[@]}"; do
    # erase drops every key slot and token; wipefs then removes the header so
    # the installer sees an empty partition.
    if ! cryptsetup erase --batch-mode "$device" || ! wipefs -a "$device" >/dev/null; then
      printf '%s\n' "ERROR: could not erase $device. Nothing else was changed."
      return
    fi
    printf '%s\n' "Erased $device."
  done
  runuser -l nixos -c 'gjallar-recover install'
}

while true; do
  printf '%s\n' '' 'GjallarOS recovery' \
    '  1) Unlock the installed system and open a recovery shell' \
    '  2) Erase this device and reinstall GjallarOS' \
    '  3) Restart' \
    '  4) Power off'
  drop_typeahead
  printf 'Choice: '
  read -r choice || exit 1
  case $choice in
    1) open_system ;;
    2) erase_and_reinstall ;;
    3) systemctl reboot ;;
    4) systemctl poweroff ;;
  esac
done
