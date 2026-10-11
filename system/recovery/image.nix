{
  config,
  lib,
  pkgs,
  modulesPath,
  releaseVersion,
  settings,
  repoSource,
  sourceRevision,
  ...
}:
let
  tools = import ./tools.nix { inherit config pkgs sourceRevision; };

  getSetting =
    name: fallback: if builtins.hasAttr name settings then builtins.getAttr name settings else fallback;

  sourceUserConfig = repoSource + "/user.config.json";

  webApplicationPresetFields =
    if settings ? webApplications then
      {
        webApplications = getSetting "webApplications" [ ];
      }
    else
      {
        # Preserve pre-GJAL-95 generated-state semantics. Absence of the
        # canonical field tells config normalization to migrate legacy state.
        planeEnable = getSetting "planeEnable" false;
        planeHost = getSetting "planeHost" "";
        drawioEnable = getSetting "drawioEnable" false;
        drawioSelfHosted = getSetting "drawioSelfHosted" false;
        drawioHost = getSetting "drawioHost" "";
      };

  generatedInstallerPresetData = {
      system = getSetting "system" pkgs.stdenv.hostPlatform.system;
      hostname = getSetting "hostname" "gjallarOS";
      username = getSetting "username" "user";

      timezone = getSetting "timezone" "UTC";
      locale = getSetting "locale" "en_US.UTF-8";
      keyboardLayout = getSetting "keyboardLayout" "de";
      keyboardVariant = getSetting "keyboardVariant" "";

      touchpadWorkspaceSwipe = getSetting "touchpadWorkspaceSwipe" true;
      clamshellEnable = getSetting "clamshellEnable" true;
      consoleLoginEnable = getSetting "consoleLoginEnable" false;
      usbguardEnable = getSetting "usbguardEnable" false;
      usbTrustEnforce = getSetting "usbTrustEnforce" false;
      usbTrustTpmHandle = getSetting "usbTrustTpmHandle" "";
      dolphinDriveUsage = getSetting "dolphinDriveUsage" false;

      name = getSetting "name" "";
      email = getSetting "email" "";
      githubUsername = getSetting "githubUsername" "";

      dotfilesDir = "";

      apps = settings.apps;
      debugFunctions = getSetting "debugFunctions" false;

      shell = getSetting "shell" "zsh";
      editors = getSetting "editors" [ "vscodium" ];
      browsers = getSetting "browsers" [ "firefox" ];
      preferredEditor = getSetting "preferredEditor" "vscodium";
      preferredBrowser = getSetting "preferredBrowser" "firefox";

      theme = getSetting "theme" "noctalia";


      aiAgentMode = getSetting "aiAgentMode" "workspace";
      overrideAiSelection = false;
      overrideModelWith = "";

      luksTpm2Enable = getSetting "luksTpm2Enable" false;

      recoveryEnable = getSetting "recoveryEnable" true;
      recoveryPartitionEnable = getSetting "recoveryPartitionEnable" true;

      secureBootEnable = getSetting "secureBootEnable" false;
      # As in config.Defaults(): a reinstall from recovery is offered
      # Secure Boot like any other install. Generated state does not carry
      # this key, so a fixed false here meant never.
      secureBootPrompt = true;

      endpointManagedDevice = false;
      jodsPrebootLockEnable = false;

      autoReboot = false;
      runUpdateChecks = false;
      writeConfig = true;
      runRebuild = true;
      unattendedInstall = false;
    }
    // webApplicationPresetFields;

  generatedInstallerPreset = pkgs.writeText "gjallar-installer-fallback.json" (
    builtins.toJSON generatedInstallerPresetData
  );

  installerFallbackPreset =
    if builtins.pathExists sourceUserConfig then sourceUserConfig else generatedInstallerPreset;

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

  # The ISO adds boot.shell_on_fail for its own boot menu; on the partition
  # any stage 1 failure (a missing or changed ISO) would then offer a root
  # shell past the recovery console. root= names the ISO volume, unused here.
  partitionKernelParams = lib.filter (
    p: p != "boot.shell_on_fail" && !lib.hasPrefix "root=" p
  ) config.boot.kernelParams;

  # The recovery partition starts this UKI, not the ISO's GRUB: GRUB needs
  # shim under Secure Boot (e2e-fw13, 2026-10-07: shim_lock protocol not
  # found), and the ISO's /iso mount wants an iso9660 device. gjallar.iso=
  # makes stage 1 find the ISO file install-partition.sh copies next to it
  # and gjallar.verity= pins its store.
  # systemd-boot lists it by its os-release PRETTY_NAME, next to the
  # installed system's entries.
  recoveryPartitionUki = pkgs.runCommand "gjallar-recovery-partition.efi" { } ''
    sed 's/^PRETTY_NAME=.*/PRETTY_NAME="GjallarOS recovery (partition)"/' \
      ${config.system.build.etc}/etc/os-release > os-release
    ${pkgs.buildPackages.systemdUkify}/lib/systemd/ukify build \
      --linux=${config.boot.kernelPackages.kernel}/${config.system.boot.loader.kernelFile} \
      --initrd=${config.system.build.initialRamdisk}/${config.system.boot.loader.initrdFile} \
      --cmdline="init=${config.system.build.toplevel}/init ${toString partitionKernelParams} gjallar.iso=/gjallar-recovery.iso gjallar.verity=$(cat ${recoveryStoreVerity}/params) gjallar.recovery=partition" \
      --stub=${pkgs.systemd}/lib/systemd/boot/efi/linux${pkgs.stdenv.hostPlatform.efiArch}.efi.stub \
      --uname=${config.boot.kernelPackages.kernel.modDirVersion} \
      --os-release=@os-release \
      --output=$out
  '';
in
{
  imports = [ "${modulesPath}/installer/cd-dvd/installation-cd-minimal.nix" ];

  services.usbguard.enable = lib.mkForce false;

  # isoImage builds its file name from image.baseName; image.fileName alone
  # left it nixos-minimal-*.iso.
  image.baseName = lib.mkForce "gjallar-recovery-${config.system.nixos.label}-${pkgs.stdenv.hostPlatform.system}";
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
  # The ISO hook below runs only in the scripted stage 1.
  boot.initrd.systemd.enable = lib.mkForce false;

  # Stage 1 sets up both devices before it mounts anything:
  # - partition UKI (gjallar.iso=): only a vfat JODSRECOV partition whose
  #   ISO store matches gjallar.verity= is used; the store is read through
  #   dm-verity, so a changed block fails to read instead of running.
  # - GRUB findiso= and USB/DVD boot (root=): no hash to check against, the
  #   store is used as is, like upstream.
  # installation-cd-base.nix sets the whole fileSystems set with
  # mkImageMediaOverride and iso-image.nix wraps each entry the same way, so
  # both levels need it or this definition is dropped.
  fileSystems = lib.mkImageMediaOverride {
    "/iso" = lib.mkImageMediaOverride { device = lib.mkForce "/dev/gjallar-iso"; };
    "/nix/.ro-store" = lib.mkImageMediaOverride {
      device = lib.mkForce "/dev/gjallar-store";
      # Drops "loop": the device is already a loop or dm-verity device.
      options = lib.mkForce (
        [ "x-initrd.mount" ]
        ++ lib.optional (config.boot.kernelPackages.kernel.kernelAtLeast "6.2") "threads=multi"
      );
    };
  };
  boot.initrd.availableKernelModules = [
    "dm_mod"
    "dm_verity"
  ];
  boot.initrd.postResumeCommands = ''
    gjallarIso=
    gjallarVerity=
    for o in $(cat /proc/cmdline); do
      case $o in
        gjallar.iso=*) gjallarIso=''${o#gjallar.iso=} ;;
        gjallar.verity=*) gjallarVerity=''${o#gjallar.verity=} ;;
      esac
    done
    mkdir -p /gjallar-src /gjallar-iso

    gjallarLoop() {
      local d
      d=$(losetup -f) && losetup -r "$d" "$1" && echo "$d"
    }

    # $1: ISO block device or file. Mounts it and links /dev/gjallar-iso and
    # /dev/gjallar-store; with $2 = verity params the store goes through
    # dm-verity. Undoes its own steps on failure.
    gjallarAttach() {
      local iso="$1" isoDev= data= hash= blocks salt root rest
      if [ -b "$iso" ]; then
        isoDev=$(readlink -f "$iso")
      else
        isoDev=$(gjallarLoop "$iso") || return 1
      fi
      if mount -t iso9660 -o ro "$isoDev" /gjallar-iso; then
        if [ -z "$2" ]; then
          if data=$(gjallarLoop /gjallar-iso/nix-store.squashfs); then
            ln -sf "$isoDev" /dev/gjallar-iso
            ln -sf "$data" /dev/gjallar-store
            return 0
          fi
        else
          blocks=''${2%%,*}
          rest=''${2#*,}
          salt=''${rest%%,*}
          root=''${rest#*,}
          if data=$(gjallarLoop /gjallar-iso/nix-store.squashfs) \
            && hash=$(gjallarLoop /gjallar-iso/nix-store.squashfs.verity) \
            && dmsetup create gjallar-store --readonly \
              --table "0 $((blocks * 8)) verity 1 $data $hash 4096 4096 $blocks 0 sha256 $root $salt" \
            && dd if=/dev/mapper/gjallar-store of=/dev/null bs=4096 count=1 2>/dev/null; then
            ln -sf "$isoDev" /dev/gjallar-iso
            ln -sf /dev/mapper/gjallar-store /dev/gjallar-store
            return 0
          fi
          # udev still probes a just-created mapping; removing it before
          # that ends fails with "busy" and leaves every later try stuck.
          udevadm settle
          dmsetup remove --retry gjallar-store 2>/dev/null
          [ -n "$hash" ] && losetup -d "$hash"
        fi
        [ -n "$data" ] && losetup -d "$data"
        umount /gjallar-iso
      fi
      [ -b "$iso" ] || losetup -d "$isoDev"
      return 1
    }

    if [ -n "$gjallarIso" ]; then
      if [ -z "$gjallarVerity" ]; then
        echo "gjallar.iso= without gjallar.verity="
        fail
      fi
      modprobe dm_verity
      gjallarFound=
      for delay in 0 5 10; do
        sleep "$delay"
        udevadm settle
        for dev in $(blkid -t LABEL=JODSRECOV -o device); do
          mount -t vfat -o ro,nodev,nosuid,noexec "$dev" /gjallar-src || continue
          if [ -f "/gjallar-src$gjallarIso" ] \
            && gjallarAttach "/gjallar-src$gjallarIso" "$gjallarVerity"; then
            echo "recovery store verified on $dev"
            gjallarFound=1
            break 2
          fi
          echo "$dev: no recovery image matching this boot entry"
          umount /gjallar-src
        done
      done
      if [ -z "$gjallarFound" ]; then
        echo "No recovery image matching this boot entry was found."
        fail
      fi
    elif [ -n "$isoPath" ]; then
      for delay in 5 10; do
        for dev in $(blkid -o device); do
          mount -t "$(blkid -o value -s TYPE "$dev")" -o ro "$dev" /gjallar-src || continue
          [ -f "/gjallar-src$isoPath" ] && gjallarAttach "/gjallar-src$isoPath" "" && break 2
          umount /gjallar-src
        done
        sleep "$delay"
      done
      # Done here; skip the upstream findiso scan.
      isoPath=
    else
      waitDevice /dev/root
      udevadm settle
      gjallarAttach /dev/root ""
    fi
  '';
  boot.zfs.forceImportRoot = false;

  services.xserver.xkb = {
    layout = settings.keyboardLayout;
    variant = settings.keyboardVariant;
  };

  console =
    if settings.keyboardVariant == "" then
      {
        keyMap = settings.keyboardLayout;
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
    description = "GjallarOS recovery console";
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
    GjallarOS minimal recovery and installer environment.
    Verify the signed release manifest before copying this image to recovery media.
    Network access is opt-in; SSH is disabled.
  '';

  systemd.tmpfiles.rules = [
    "d /run/gjallarOS/device-config 0700 root root - -"
  ];

  environment.etc."gjallar/installer-fallback-user.config.json".source = installerFallbackPreset;

  environment.etc."gjallar/device-config-contract".text = ''
    GjallarOS fresh-install device configuration handoff

    Runtime provider path:
      /run/gjallarOS/device-config/user.config.json

    Embedded fallback:
      /etc/gjallar/installer-fallback-user.config.json

    Resolution order:
      1. Runtime device configuration
      2. Embedded device configuration
      3. Interactive installer fallback

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
