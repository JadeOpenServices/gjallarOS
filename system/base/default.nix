{
  config,
  lib,
  pkgs,
  inputs,
  gjallarctlPackage,
  ...
}:
# The base system of the two-stage install (docs/two-stage-install.md,
# Plane 385). Stage 1 copies it from the stick store to the new disk; it
# boots once and runs stage 2, which switches to the full system. The same
# for every machine: no hardware scan, no partition IDs, no user.
{
  imports = [
    inputs.lanzaboote.nixosModules.lanzaboote
    ../security/secure-boot
    ../hardware/networking.nix
    ../maintenance/binary-cache.nix
  ];

  boot.loader.efi.canTouchEfiVariables = true;
  # Same as the full system (hardware/boot.nix): no boot menu editor.
  boot.loader.systemd-boot.editor = false;
  boot.loader.timeout = 0;

  # Root by partition type on the boot disk (Discoverable Partitions
  # Specification). The generator opens the LUKS root as /dev/mapper/root
  # and mounts the btrfs default subvolume (@) as /.
  boot.initrd.systemd.enable = true;
  boot.initrd.systemd.root = "gpt-auto";
  boot.initrd.luks.forceLuksSupportInInitrd = true;
  boot.initrd.supportedFilesystems = [ "btrfs" ];
  boot.initrd.availableKernelModules = [
    "nvme"
    "ahci"
    "sd_mod"
    "xhci_pci"
    "usb_storage"
    "virtio_pci"
    "virtio_blk"
    "dm_crypt"
  ];
  # NixOS puts only a few of systemd's udev rules in the initrd. The
  # /dev/gpt-auto-root* links that the generator's units wait for come from
  # 90-image-dissect.rules.
  boot.initrd.services.udev.packages = [
    (pkgs.runCommand "gjallar-base-image-dissect-rules" { } ''
      mkdir -p $out/lib/udev/rules.d
      ln -s ${config.boot.initrd.systemd.package}/lib/udev/rules.d/90-image-dissect.rules $out/lib/udev/rules.d/
    '')
  ];

  # The other subvolumes that rootprovision makes. gpt-auto mounts only @;
  # /dev/gpt-auto-root is the opened root, whatever the partition's UUID.
  fileSystems =
    lib.mapAttrs
      (_: subvol: {
        device = "/dev/gpt-auto-root";
        fsType = "btrfs";
        options = [ "subvol=${subvol}" ];
        neededForBoot = subvol == "@nix";
      })
      {
        "/nix" = "@nix";
        "/home" = "@home";
        "/etc/gjallar/system" = "@config";
      };

  # Stage 2 drives its steps through gjallarctl (secure-boot sets the arg).
  environment.systemPackages = [ gjallarctlPackage ];

  nix.settings.experimental-features = [
    "nix-command"
    "flakes"
  ];

  networking.hostName = "gjallaros";
  networking.firewall.enable = true;
  users.mutableUsers = false;
  # No account has a password or key on the base: there is no shell (Q265).
  # The account is made in stage 2 (identity step).
  users.allowNoPasswordLogin = true;

  # Kept off the stick (size check in docs/two-stage-install.md). The fat
  # installer's resume after Secure Boot setup (zenity dialogs, GTK) never
  # runs here: stage 2 has its own Secure Boot check. With no shell there is
  # no one to read manuals, and stage 2 builds the full system from the
  # checkout's flake.lock, so no nixpkgs copy for the registry or NIX_PATH.
  systemd.services.installer-post-secure-boot.enable = false;
  documentation.enable = false;
  nixpkgs.flake.setFlakeRegistry = false;
  nixpkgs.flake.setNixPath = false;

  # Stage 2 reads it for the install state; never advance it.
  system.stateVersion = lib.trivial.release;
}
