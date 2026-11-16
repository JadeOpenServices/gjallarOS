{ pkgs, script }:
# esp-check.sh inside the nixos-enter chroot, which has no udev database:
# sbctl verify found no ESP there (stick CP-C, 2026-11-16). The stubs answer
# from mountinfo and the disk, as findmnt and blkid -p do in that chroot.
pkgs.runCommand "esp-check-chroot"
  {
    nativeBuildInputs = [
      pkgs.bash
      pkgs.coreutils
    ];
  }
  ''
    set -eu
    stubs=$PWD/stubs
    mkdir -p "$stubs"
    cat > "$stubs/findmnt" <<'EOF'
    #!${pkgs.bash}/bin/bash
    [ "$4" = /boot ] || exit 1
    [ -n "$MNT_SOURCE" ] || exit 1
    case "$2" in
      SOURCE) echo "$MNT_SOURCE" ;;
      FSTYPE) echo "$MNT_FSTYPE" ;;
      *) exit 2 ;;
    esac
    EOF
    cat > "$stubs/blkid" <<'EOF'
    #!${pkgs.bash}/bin/bash
    [ "$1 $2 $4 $5 $6" = "-p -s -o value /dev/vda1" ] || exit 2
    case "$3" in
      PART_ENTRY_TYPE) echo "$PART_TYPE" ;;
      PART_ENTRY_SCHEME) echo "$PART_SCHEME" ;;
      *) exit 2 ;;
    esac
    EOF
    chmod +x "$stubs"/*
    export PATH=$stubs:$PATH
    esp=C12A7328-F81F-11D2-BA4B-00A0C93EC93B

    MNT_SOURCE=/dev/vda1 MNT_FSTYPE=vfat PART_TYPE=$esp PART_SCHEME=gpt \
      bash ${script} /boot

    refused() {
      if env "$@" bash ${script} /boot 2> err; then
        echo "FAIL: esp-check took $*" >&2
        exit 1
      fi
      grep -q ERROR err
    }
    refused MNT_SOURCE= MNT_FSTYPE= PART_TYPE=$esp PART_SCHEME=gpt
    refused MNT_SOURCE=/dev/vda1 MNT_FSTYPE=ext4 PART_TYPE=$esp PART_SCHEME=gpt
    refused MNT_SOURCE=/dev/vda1 MNT_FSTYPE=vfat PART_TYPE=0fc63daf-8483-4772-8e79-3d69d8477de4 PART_SCHEME=gpt
    refused MNT_SOURCE=/dev/vda1 MNT_FSTYPE=vfat PART_TYPE=0xef PART_SCHEME=dos
    refused MNT_SOURCE=/dev/vda1 MNT_FSTYPE=vfat PART_TYPE= PART_SCHEME=
    if bash ${script} 2> /dev/null; then
      echo "FAIL: esp-check ran without a mount point" >&2
      exit 1
    fi

    touch "$out"
  ''
