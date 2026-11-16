{
  config,
  lib,
  pkgs,
  utils,
  ...
}:
# The installer stick (Plane 344/350). The same image for everyone: built
# from the source without generated/ or user.config.json, US keyboard until
# the stick menu asks (CP-D). For now tty1 is the live login, like the
# recovery ISO.
let
  media = config.system.build.gjallarMedia;
  isoName = "gjallar-stick-${config.system.nixos.label}-${pkgs.stdenv.hostPlatform.system}.iso";

  # No gjallar.iso=: stage 1 takes the volume labelled GJALLAR_STICK, and
  # gjallar.verity= decides whether its store may run.
  uki = media.mkUki {
    name = "gjallar-stick.efi";
    prettyName = "gjallarOS installer";
    cmdline = "";
  };
in
{
  imports = [ ./live.nix ];

  # targetdisk.bootMediaLabel knows this label; the installer never offers
  # the stick as a target.
  isoImage.volumeID = "GJALLAR_STICK";
  image.baseName = lib.mkForce (lib.removeSuffix ".iso" isoName);

  networking.hostName = "gjallar-stick";

  # The installer queues the removal of the stick's hash from shim's list
  # (QueueStickHashDelete); mokutil does that.
  environment.systemPackages = lib.optional pkgs.stdenv.hostPlatform.isx86_64 pkgs.mokutil;

  # Started through shim, the stick's UKI is trusted by its hash, which the
  # user enrolled in MokManager. The installer removes that hash again after
  # the install (CP-C, Q1). It needs the UKI to compute the hash, and with
  # copytoram the stick may be gone by then, so stage 1 keeps a copy in
  # /run/gjallar, which carries into stage 2 and only root can change.
  # Only the disk stage 1 booted from is read, and only its one EFI system
  # partition. The stick is MBR, so systemd-stub reports no partition
  # (StubDevicePartUUID); StubImageIdentifier says which file it started.
  # A failure here never stops the boot: the installer then says it could
  # not find the hash.
  boot.initrd.systemd.services.gjallar-stick-uki = lib.mkIf pkgs.stdenv.hostPlatform.isx86_64 {
    description = "Keep the stick's boot file for the installer";
    wantedBy = [ "initrd.target" ];
    after = [ "gjallar-media-iso.service" ];
    before = [ "gjallar-media-store.service" ];
    unitConfig.DefaultDependencies = false;
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      StandardOutput = "journal+console";
      StandardError = "journal+console";
    };
    path = [
      pkgs.coreutils
      config.boot.initrd.systemd.package.util-linux
      config.services.lvm.package
    ];
    script = ''
      set -euo pipefail
      fail() {
        echo "$*" >&2
        exit 1
      }
      efivars=/sys/firmware/efi/efivars
      if ! findmnt -n "$efivars" > /dev/null; then
        mount -t efivarfs -o ro,nodev,nosuid,noexec efivarfs "$efivars"
      fi
      var=$efivars/StubImageIdentifier-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f
      [ -f "$var" ] || exit 0
      # efivars start with 4 attribute bytes; the value is UTF-16.
      id=$(tail -c +5 "$var" | tr -d '\000' | tr '[:upper:]' '[:lower:]')
      if [ "$id" != '\efi\boot\grubx64.efi' ]; then
        # Started directly, not through shim: no hash to remove.
        exit 0
      fi
      # The device stage 1 set up gjallar-iso from, not a fresh label lookup
      # that another stick with the same label could answer.
      deps=$(dmsetup deps -o devname gjallar-iso)
      back=''${deps##*(}
      back=''${back%)}
      [[ $back =~ ^[a-z0-9]+$ ]] || fail "The boot medium has no plain device name."
      dev=$(readlink -f "/sys/class/block/$back")
      if [ -f "$dev/partition" ]; then
        dev=$(dirname "$dev")
      fi
      esp=
      for part in "$dev"/*/partition; do
        [ -f "$part" ] || continue
        node=/dev/$(basename "$(dirname "$part")")
        if [ "$(blkid -p -o value -s PART_ENTRY_TYPE "$node")" = 0xef ] \
          && [ "$(blkid -p -o value -s TYPE "$node")" = vfat ]; then
          [ -z "$esp" ] || fail "The stick has more than one EFI system partition."
          esp=$node
        fi
      done
      [ -n "$esp" ] || fail "The stick has no EFI system partition."
      # gjallar-iso maps the whole disk and so holds it exclusively; the
      # kernel then refuses the exclusive open a mount of one of its
      # partitions needs. A read-only loop device opens the partition
      # without that claim.
      loop=
      mounted=
      cleanup() {
        [ -z "$mounted" ] || umount /run/gjallar/stick-esp || true
        [ -z "$loop" ] || losetup -d "$loop" || true
        rmdir /run/gjallar/stick-esp 2> /dev/null || true
        rm -f /run/gjallar/stick-uki.efi.new
      }
      trap cleanup EXIT
      mkdir -p -m 0755 /run/gjallar /run/gjallar/stick-esp
      loop=$(losetup -f --show -r "$esp")
      mount -t vfat -o ro,nodev,nosuid,noexec "$loop" /run/gjallar/stick-esp
      mounted=1
      file=/run/gjallar/stick-esp/EFI/BOOT/grubx64.efi
      [ -f "$file" ] || fail "The stick's boot file is missing."
      # The installer reads at most this much (stickUKIMax).
      cap=$((128 * 1024 * 1024))
      head -c $((cap + 1)) "$file" > /run/gjallar/stick-uki.efi.new
      [ "$(stat -c %s /run/gjallar/stick-uki.efi.new)" -le "$cap" ] || fail "The stick's boot file is too large."
      chmod 0444 /run/gjallar/stick-uki.efi.new
      mv -f /run/gjallar/stick-uki.efi.new /run/gjallar/stick-uki.efi
    '';
  };

  system.build.gjallarStickIso = pkgs.callPackage ../../pkgs/gjallar-stick-iso {
    inherit uki isoName;
    inherit (media) store storeVerity;
    inherit (config.isoImage) volumeID;
    inherit (pkgs.stdenv.hostPlatform) efiArch;
    # Debian's shim exists for x86_64 only.
    shim =
      if pkgs.stdenv.hostPlatform.isx86_64 then pkgs.callPackage ../../pkgs/gjallar-shim { } else null;
  };
}
