{
  config,
  lib,
  pkgs,
  utils,
  ...
}:
# Stage 1 for every gjallarOS boot medium (recovery ISO, recovery
# partition, later the installer stick). systemd stage 1, because the
# scripted one goes away in NixOS 26.11.
#
# Two devices are set up before anything is mounted:
# - /dev/mapper/gjallar-iso: the ISO volume. On the recovery partition
#   (gjallar.iso= on the signed UKI command line) only the UKI's own
#   partition (StubDevicePartUUID) is read, and only if it is the vfat
#   GJALLAR-REC partition; a stick or second disk with the same label is
#   never used (Plane 306). From a USB stick or DVD it is the volume with
#   the ISO's label, like upstream.
# - /dev/mapper/gjallar-store: the store squashfs. With gjallar.verity= it
#   goes through dm-verity, so a changed block fails to read instead of
#   running (loophole 9). Without it the store is used as is, like upstream.
#
# GRUB findiso= (loopback.cfg) is gone: upstream drops it under systemd
# stage 1, and scanning every device as root is attack surface.
#
# A failure ends in emergency mode, which has no shell here.
let
  util-linux = config.boot.initrd.systemd.package.util-linux;
  storeMount = "${utils.escapeSystemdPath "/sysroot/nix/.ro-store"}.mount";
  isoMount = "${utils.escapeSystemdPath "/sysroot/iso"}.mount";

  cmdline = ''
    iso=
    verity=
    read -ra args < /proc/cmdline
    for o in "''${args[@]}"; do
      case $o in
        gjallar.iso=*) iso=''${o#gjallar.iso=} ;;
        gjallar.verity=*) verity=''${o#gjallar.verity=} ;;
      esac
    done
    fail() {
      echo "$*" >&2
      exit 1
    }
  '';
in
{
  boot.initrd.systemd = {
    enable = true;
    # installation-cd-base turns this on for the live ISO; the recovery
    # partition boots even with USB boot locked, so stage 1 never opens a
    # root shell.
    emergencyAccess = lib.mkForce false;
    services.gjallar-media-iso = {
      description = "Find the gjallarOS boot medium";
      requiredBy = [ isoMount ];
      before = [ isoMount ];
      after = [
        "systemd-udevd.service"
        "systemd-udev-trigger.service"
        "systemd-modules-load.service"
      ];
      unitConfig.DefaultDependencies = false;
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        StandardOutput = "journal+console";
        StandardError = "journal+console";
      };
      path = [
        pkgs.coreutils
        util-linux
        config.services.lvm.package
        config.boot.initrd.systemd.package
      ];
      script = ''
        set -euo pipefail
        ${cmdline}
        if [ -n "$iso" ]; then
          [ -n "$verity" ] || fail "gjallar.iso= without gjallar.verity="
          [[ $iso =~ ^/[A-Za-z0-9_-][A-Za-z0-9._-]*$ ]] || fail "gjallar.iso=$iso is not a plain file name"
          # The UKI's own partition, as systemd-stub reports it. efivars
          # start with 4 attribute bytes; the value is UTF-16.
          efivars=/sys/firmware/efi/efivars
          if ! findmnt -n "$efivars" > /dev/null; then
            mount -t efivarfs -o ro,nodev,nosuid,noexec efivarfs "$efivars"
          fi
          var=$efivars/StubDevicePartUUID-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f
          [ -f "$var" ] || fail "The boot stub did not report its partition (StubDevicePartUUID)."
          uuid=$(tail -c +5 "$var" | tr -d '\000' | tr '[:upper:]' '[:lower:]')
          [[ $uuid =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] \
            || fail "The boot stub reported no usable partition id."
          # udevadm wait returns once udev has set up the link, or at the
          # deadline; no polling.
          udevadm wait --timeout=90 "/dev/disk/by-partuuid/$uuid" \
            || fail "The recovery partition $uuid did not appear."
          dev=$(readlink -f "/dev/disk/by-partuuid/$uuid")
          if [ "$(blkid -p -o value -s PART_ENTRY_NAME "$dev")" != GJALLAR-RECOVERY ] \
            || [ "$(blkid -p -o value -s TYPE "$dev")" != vfat ] \
            || [ "$(blkid -p -o value -s LABEL "$dev")" != GJALLAR-REC ]; then
            fail "$dev is not the gjallarOS recovery partition."
          fi
          # Under /run the mount carries into stage 2: the image stays
          # readable and the installer sees this disk as in use.
          mkdir -p /run/gjallar/src
          mount -t vfat -o ro,nodev,nosuid,noexec "$dev" /run/gjallar/src
          [ -f "/run/gjallar/src$iso" ] || fail "$dev: no recovery image matching this boot entry."
          src=$(losetup -f --show -r "/run/gjallar/src$iso")
        else
          label=/dev/disk/by-label/${config.isoImage.volumeID}
          udevadm wait --timeout=90 "$label" || fail "No medium labelled ${config.isoImage.volumeID} appeared."
          src=$(readlink -f "$label")
        fi
        dmsetup create gjallar-iso --readonly --table "0 $(blockdev --getsz "$src") linear $src 0"
      '';
    };

    services.gjallar-media-store = {
      description = "Check the gjallarOS store image";
      requiredBy = [ storeMount ];
      before = [ storeMount ];
      after = [
        isoMount
        "copytoram.service"
      ];
      unitConfig = {
        DefaultDependencies = false;
        RequiresMountsFor = "/sysroot/iso";
      };
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        StandardOutput = "journal+console";
        StandardError = "journal+console";
      };
      path = [
        pkgs.coreutils
        util-linux
        config.services.lvm.package
      ];
      script = ''
        set -euo pipefail
        ${cmdline}
        data=$(losetup -f --show -r /sysroot/iso/nix-store.squashfs)
        size=$(blockdev --getsz "$data")
        if [ -n "$verity" ]; then
          IFS=, read -r blocks salt root rest <<< "$verity"
          [[ $blocks =~ ^[0-9]{1,12}$ && $salt =~ ^[0-9a-f]{64}$ && $root =~ ^[0-9a-f]{64}$ && -z $rest ]] \
            || fail "gjallar.verity= is malformed."
          [ "$size" -eq $((blocks * 8)) ] || fail "The store image does not match this boot entry."
          hash=$(losetup -f --show -r /sysroot/iso/nix-store.squashfs.verity)
          dmsetup create gjallar-store --readonly \
            --table "0 $size verity 1 $data $hash 4096 4096 $blocks 0 sha256 $root $salt"
          # The first block is checked against the root hash here, so a
          # wrong image stops now with a clear message.
          dd if=/dev/mapper/gjallar-store of=/dev/null bs=4096 count=1 status=none \
            || fail "The store image does not match this boot entry."
          echo "gjallarOS store verified"
        else
          dmsetup create gjallar-store --readonly --table "0 $size linear $data 0"
        fi
      '';
    };
  };

  # dmsetup and the udev rules that turn dm devices into systemd devices.
  boot.initrd.services.lvm.enable = true;
  boot.initrd.kernelModules = [
    "dm_mod"
    "dm_verity"
    "loop"
  ];
  boot.initrd.availableKernelModules = [
    "vfat"
    "nls_cp437"
    "nls_iso8859-1"
  ];
  # The initrd's gpt-auto generator never mounts a partition of the disk
  # this medium repairs. Stage 2 masks it already.
  boot.kernelParams = [ "rd.systemd.gpt_auto=0" ];

  # installation-cd-base sets the whole fileSystems set with
  # mkImageMediaOverride and iso-image.nix wraps each entry the same way, so
  # both levels need it or this definition is dropped.
  fileSystems = lib.mkImageMediaOverride {
    "/iso" = lib.mkImageMediaOverride {
      device = lib.mkForce "/dev/mapper/gjallar-iso";
    };
    "/nix/.ro-store" = lib.mkImageMediaOverride {
      device = lib.mkForce "/dev/mapper/gjallar-store";
      noCheck = true;
      # Drops "loop": the device is a dm device over a loop already. The
      # force also drops the x-initrd.mount that neededForBoot adds, so it
      # is listed again.
      options = lib.mkForce (
        [ "x-initrd.mount" ]
        ++ lib.optional (config.boot.kernelPackages.kernel.kernelAtLeast "6.2") "threads=multi"
      );
    };
  };
}
