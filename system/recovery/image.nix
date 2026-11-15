{
  config,
  lib,
  pkgs,
  modulesPath,
  releaseVersion,
  recoveryKeyboard,
  repoSource,
  sourceRevision,
  ...
}:
let
  tools = import ./tools.nix { inherit config pkgs sourceRevision; };

  # The ISO's store, built here instead of by isoImage so its dm-verity
  # root hash can go on the partition UKI's command line. The squashfs holds
  # the initrd, so the hash cannot live inside it; the UKI sits next to it
  # and under Secure Boot its command line is signed (loophole 9: before,
  # the partition booted whatever gjallar-recovery.iso any disk carried).
  recoveryStore = pkgs.callPackage "${modulesPath}/../lib/make-squashfs.nix" {
    storeContents = [ config.system.build.toplevel ];
    comp = config.isoImage.squashfsCompression;
  };

  recoveryStoreVerity =
    pkgs.runCommand "gjallar-recovery-store-verity" { nativeBuildInputs = [ pkgs.cryptsetup ]; }
      ''
        size=$(stat -c %s ${recoveryStore})
        [ $((size % 4096)) -eq 0 ] || { echo "squashfs size $size is not 4K aligned" >&2; exit 1; }
        mkdir $out
        touch $out/nix-store.squashfs.verity
        # Fixed salt keeps the build reproducible.
        salt=$(printf gjallar-recovery-store | sha256sum | cut -d' ' -f1)
        veritysetup format --no-superblock --hash=sha256 \
          --data-block-size=4096 --hash-block-size=4096 --salt="$salt" \
          ${recoveryStore} $out/nix-store.squashfs.verity > $out/format.log
        root=$(sed -n 's/^Root hash:[[:space:]]*//p' $out/format.log)
        [ -n "$root" ] || { cat $out/format.log >&2; exit 1; }
        printf '%s,%s,%s' "$((size / 4096))" "$salt" "$root" > $out/params
      '';

  # The recovery partition starts this UKI, not the ISO's GRUB: GRUB needs
  # shim under Secure Boot (e2e-fw13, 2026-10-07: shim_lock protocol not
  # found), and the ISO's /iso mount wants an iso9660 device. gjallar.iso=
  # makes stage 1 (../installer-media/base.nix) find the ISO file
  # install-partition.sh copies next to it and gjallar.verity= pins its store.
  # systemd-boot lists it by its os-release PRETTY_NAME, next to the
  # installed system's entries.
  recoveryPartitionUki = pkgs.runCommand "gjallar-recovery-partition.efi" { } ''
    sed 's/^PRETTY_NAME=.*/PRETTY_NAME="gjallarOS recovery (partition)"/' \
      ${config.system.build.etc}/etc/os-release > os-release
    ${pkgs.buildPackages.systemdUkify}/lib/systemd/ukify build \
      --linux=${config.boot.kernelPackages.kernel}/${config.system.boot.loader.kernelFile} \
      --initrd=${config.system.build.initialRamdisk}/${config.system.boot.loader.initrdFile} \
      --cmdline="init=${config.system.build.toplevel}/init ${toString config.boot.kernelParams} gjallar.iso=/gjallar-recovery.iso gjallar.verity=$(cat ${recoveryStoreVerity}/params) gjallar.recovery=partition" \
      --stub=${pkgs.systemd}/lib/systemd/boot/efi/linux${pkgs.stdenv.hostPlatform.efiArch}.efi.stub \
      --uname=${config.boot.kernelPackages.kernel.modDirVersion} \
      --os-release=@os-release \
      --output=$out
  '';
in
{
  imports = [
    "${modulesPath}/installer/cd-dvd/installation-cd-minimal.nix"
    ../installer-media/base.nix
  ];

  services.usbguard.enable = lib.mkForce false;

  # isoImage builds its file name from image.baseName; image.fileName alone
  # left it nixos-minimal-*.iso.
  image.baseName = lib.mkForce "gjallar-recovery-${config.system.nixos.label}-${pkgs.stdenv.hostPlatform.system}";
  # Started from a stick, stage 1 finds the medium by this label. Upstream's
  # nixos-minimal-<release>-<arch> also matches a stock NixOS stick of the
  # same release. ISO 9660 allows 32 characters.
  isoImage.volumeID = "gjallar-recovery-${config.system.nixos.release}-${pkgs.stdenv.hostPlatform.linuxArch}";
  isoImage.squashfsCompression = "zstd -Xcompression-level 15";
  isoImage.storeContents = lib.mkForce [ ];
  isoImage.contents = [
    {
      source = recoveryPartitionUki;
      target = "/EFI/gjallar/recovery-partition.efi";
    }
    {
      source = recoveryStore;
      target = "/nix-store.squashfs";
    }
    {
      source = "${recoveryStoreVerity}/nix-store.squashfs.verity";
      target = "/nix-store.squashfs.verity";
    }
  ];
  boot.zfs.forceImportRoot = false;

  # The image knows the keyboard and nothing else about the machine; the
  # answers stay inside the encrypted root (L78).
  services.xserver.xkb = {
    inherit (recoveryKeyboard) layout variant;
  };

  console =
    if recoveryKeyboard.variant == "" then
      {
        keyMap = recoveryKeyboard.layout;
      }
    else
      {
        useXkbConfig = true;
      };

  networking = {
    hostName = "gjallar-recovery";
    networkmanager.enable = true;
    firewall.enable = true;
  };
  services.openssh.enable = lib.mkForce false;
  programs.ssh.startAgent = false;

  services.fprintd.enable = true;

  # Firmware boots the recovery partition even with USB boot locked, so it
  # must not hand out the passwordless nixos/root login the installer media
  # has. Apple recoveryOS asks for an admin password before Terminal;
  # Android and ChromeOS recovery offer only wipe and reinstall
  # (support.apple.com/en-us/102633, support.google.com/chromebook/answer/1080595).
  # With the flag the partition UKI carries, tty1 runs the recovery console
  # and no getty starts. Under Secure Boot the UKI command line is signed.
  systemd.generators.gjallar-recovery-gate = pkgs.writeShellScript "gjallar-recovery-gate" ''
    case " $(${pkgs.coreutils}/bin/cat /proc/cmdline) " in
      *" gjallar.recovery=partition "*) ;;
      *) exit 0 ;;
    esac
    for unit in getty@ getty@tty1 autovt@ serial-getty@ serial-getty@ttyS0 console-getty container-getty@ debug-shell; do
      ${pkgs.coreutils}/bin/ln -sf /dev/null "$2/$unit.service"
    done
  '';
  systemd.services.gjallar-recovery-console = {
    description = "gjallarOS recovery console";
    wantedBy = [ "multi-user.target" ];
    after = [
      "installer-repository.service"
      "systemd-user-sessions.service"
    ];
    unitConfig.ConditionKernelCommandLine = "gjallar.recovery=partition";
    environment.TERM = "linux";
    serviceConfig = {
      Type = "idle";
      ExecStart = "${tools.recoveryConsole}/bin/gjallar-recovery-console";
      StandardInput = "tty-force";
      StandardOutput = "tty";
      StandardError = "tty";
      TTYPath = "/dev/tty1";
      TTYReset = true;
      TTYVHangup = true;
      Restart = "always";
      RestartSec = 1;
    };
  };
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
