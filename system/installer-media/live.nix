{
  config,
  lib,
  pkgs,
  modulesPath,
  releaseVersion,
  repoSource,
  sourceRevision,
  ...
}:
# What every gjallarOS live medium shares: the recovery ISO, the recovery
# partition and the installer stick. Each image sets its own volume label,
# host name and keyboard on top.
let
  tools = import ../recovery/tools.nix { inherit config pkgs sourceRevision; };
in
{
  imports = [
    "${modulesPath}/installer/cd-dvd/installation-cd-minimal.nix"
    ./base.nix
  ];

  services.usbguard.enable = lib.mkForce false;

  isoImage.squashfsCompression = "zstd -Xcompression-level 15";
  # base.nix builds the store with its verity hash; isoImage's own copy
  # would be a second, unchecked one.
  isoImage.storeContents = lib.mkForce [ ];
  boot.zfs.forceImportRoot = false;

  networking = {
    networkmanager.enable = true;
    firewall.enable = true;
  };
  services.openssh.enable = lib.mkForce false;
  programs.ssh.startAgent = false;

  services.fprintd.enable = true;

  # Emergency mode would otherwise open a root shell with the empty password.
  users.users.root.initialHashedPassword = lib.mkForce "!";

  systemd.services.installer-repository = {
    description = "Prepare writable installer repository";
    wantedBy = [ "multi-user.target" ];
    before = [
      "getty@tty1.service"
      "serial-getty@ttyS0.service"
    ];

    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
    };

    script = ''
      set -eu

      target=/run/gjallarOS/repo

      ${pkgs.coreutils}/bin/mkdir -p "$target"
      ${pkgs.coreutils}/bin/cp -a ${repoSource}/. "$target"/

      ${pkgs.coreutils}/bin/rm -f "$target/user.config.json"
      ${pkgs.coreutils}/bin/chown -R nixos:users "$target"
      ${pkgs.coreutils}/bin/chmod -R u+rwX "$target"

      test -f "$target/flake.nix"
      test -f "$target/scripts/installation/install.sh"
      test -f "$target/cmd/gjallar-installer/main.go"
    '';
  };

  systemd.services.installer-device-probe = {
    description = "Collect installer hardware state";
    wantedBy = [ "multi-user.target" ];

    after = [ "installer-repository.service" ];
    requires = [ "installer-repository.service" ];

    path = with pkgs; [
      pciutils
      usbutils
      util-linux
      libinput
      iio-sensor-proxy
      fprintd
      bolt
      alsa-utils
      pipewire
      fwupd
      ethtool
      iw
      systemd
    ];

    serviceConfig = {
      Type = "oneshot";
      NoNewPrivileges = true;
      PrivateTmp = true;
      ProtectHome = true;
      ProtectSystem = "strict";
      ReadWritePaths = [ "/run/gjallarOS" ];
      UMask = "0022";
    };

    script = ''
      set -eu

      ${tools.gjallarctl}/bin/gjallarctl         device-probe refresh         --output /run/gjallarOS/device-probe.json
    '';
  };

  services.udev.extraRules = ''
    ACTION=="add|remove|change", SUBSYSTEM=="pci", RUN+="${pkgs.systemd}/bin/systemctl --no-block start installer-device-probe.service"
    ACTION=="add|remove|change", SUBSYSTEM=="usb", RUN+="${pkgs.systemd}/bin/systemctl --no-block start installer-device-probe.service"
    ACTION=="add|remove|change", SUBSYSTEM=="input", RUN+="${pkgs.systemd}/bin/systemctl --no-block start installer-device-probe.service"
    ACTION=="add|remove|change", SUBSYSTEM=="iio", RUN+="${pkgs.systemd}/bin/systemctl --no-block start installer-device-probe.service"
    ACTION=="add|remove|change", SUBSYSTEM=="thunderbolt", RUN+="${pkgs.systemd}/bin/systemctl --no-block start installer-device-probe.service"
  '';

  environment.etc."systemd/system-sleep/installer-device-probe-refresh" = {
    mode = "0755";
    text = ''
      #!/bin/sh
      if [ "$1" = post ]; then
        ${pkgs.systemd}/bin/systemctl --no-block start installer-device-probe.service
      fi
    '';
  };

  environment.systemPackages = with pkgs; [
    btrfs-progs
    dosfstools
    gptfdisk
    parted
    xorriso
    age
    cryptsetup
    curl
    efibootmgr
    gitMinimal
    jq
    nixos-install-tools
    openssl
    sbctl
    systemd
    tpm2-tools
    util-linux
    tools.gjallarctl
    tools.installer
    tools.recovery
    tools.recoveryConsole
    tools.recoveryExecutor
  ];

  nix.settings = {
    experimental-features = [
      "nix-command"
      "flakes"
    ];
    require-sigs = true;
  };

  environment.etc."gjallar/recovery-image".text = ''
    gjallarOS minimal recovery and installer environment.
    Verify the signed release manifest before copying this image to recovery media.
    Network access is opt-in; SSH is disabled.
  '';

  systemd.tmpfiles.rules = [
    "d /run/gjallarOS/device-config 0700 root root - -"
  ];

  environment.etc."gjallar/device-config-contract".text = ''
    gjallarOS fresh-install device configuration handoff

    Runtime provider path:
      /run/gjallarOS/device-config/user.config.json

    Resolution order:
      1. Runtime device configuration
      2. Reinstall only: the installed system's answers, read from
         /mnt/etc/gjallar/system/user.config.json after the root is unlocked
      3. Interactive installer

    The image carries no answers of its own: it sits on an unencrypted
    partition and knows only the keyboard layout.

    The runtime provider may later be JODS, local recovery storage, or another
    trusted provisioning component.

    Supplying configuration does not grant storage authority and does not
    bypass target-disk validation, destructive confirmation, LUKS credential
    handling, Secure Boot checks, or recovery lifecycle policy.

    Plaintext passwords, LUKS keys, TPM secrets, private signing keys, and
    enrollment secrets must not be stored in this configuration handoff.
  '';

  system.stateVersion = releaseVersion;
}
