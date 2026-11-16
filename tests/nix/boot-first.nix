{ pkgs, script }:
# boot-first.sh inside the nixos-install chroot, which has no udev database:
# lsblk printed no PARTUUID there, so the shim entry made a moment before was
# never found and the install stopped (stick CP-C, 2026-11-16). The stubs
# below answer like that chroot: lsblk without PARTUUID, the partition table
# read by blkid -p, one existing shim entry in the firmware.
pkgs.runCommand "boot-first-chroot"
  {
    nativeBuildInputs = [
      pkgs.bash
      pkgs.coreutils
      pkgs.gnugrep
      pkgs.gnused
    ];
  }
  ''
    set -eu
    stubs=$PWD/stubs
    esp=$PWD/esp
    mkdir -p "$stubs" "$esp/EFI/nixos"
    : > "$esp/EFI/nixos/shimx64.efi"
    export calls=$PWD/calls BLKID_OUT=9F6BC3DA-7244-4FCE-8759-6A3033D20CEA

    cat > "$stubs/findmnt" <<'EOF'
    #!${pkgs.bash}/bin/bash
    echo /dev/vda1
    EOF
    cat > "$stubs/lsblk" <<'EOF'
    #!${pkgs.bash}/bin/bash
    case "$*" in
      *PKNAME*) echo vda ;;
      *) echo ;;
    esac
    EOF
    cat > "$stubs/blkid" <<'EOF'
    #!${pkgs.bash}/bin/bash
    [ "$*" = "-p -s PART_ENTRY_UUID -o value /dev/vda1" ] || exit 2
    [ -n "$BLKID_OUT" ] || exit 2
    echo "$BLKID_OUT"
    EOF
    cat > "$stubs/efibootmgr" <<'EOF'
    #!${pkgs.bash}/bin/bash
    echo "efibootmgr $*" >> "$calls"
    case "$*" in
      -v | "")
        printf '%s\n' 'BootCurrent: 0002' 'BootOrder: 0002,0001' \
          'Boot0001* UEFI QEMU HARDDISK	PciRoot(0x0)/Pci(0x4,0x0)' \
          'Boot0002* Linux Boot Manager (shim)	HD(1,GPT,9f6bc3da-7244-4fce-8759-6a3033d20cea,0x800,0x100000)/File(\EFI\nixos\shimx64.efi)'
        ;;
      *) ;;
    esac
    EOF
    chmod +x "$stubs"/*
    export PATH=$stubs:$PATH

    # The entry exists: found through blkid, nothing written.
    bash ${script} --keep-order "$esp"
    if grep -v -e '^efibootmgr -v$' -e '^efibootmgr $' "$calls"; then
      echo "FAIL: boot-first wrote to the firmware although the entry exists" >&2
      exit 1
    fi

    # No partition UUID: stop before touching the firmware.
    : > "$calls"
    if BLKID_OUT= bash ${script} --keep-order "$esp" 2> err; then
      echo "FAIL: boot-first went on without a partition UUID" >&2
      exit 1
    fi
    grep -q 'cannot read the GPT partition UUID' err
    if [ -s "$calls" ]; then
      echo "FAIL: boot-first called efibootmgr without a partition UUID" >&2
      exit 1
    fi

    touch "$out"
  ''
