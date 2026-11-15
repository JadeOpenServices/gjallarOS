{
  config,
  lib,
  pkgs,
  recoveryKeyboard,
  sourceRevision,
  ...
}:
let
  tools = import ./tools.nix { inherit config pkgs sourceRevision; };
  media = config.system.build.gjallarMedia;

  # The recovery partition starts this UKI, not the ISO's GRUB: GRUB needs
  # shim under Secure Boot (e2e-fw13, 2026-10-07: shim_lock protocol not
  # found), and the ISO's /iso mount wants an iso9660 device. gjallar.iso=
  # makes stage 1 (../installer-media/base.nix) find the ISO file
  # install-partition.sh copies next to it and gjallar.verity= pins its store.
  # systemd-boot lists it by its os-release PRETTY_NAME, next to the
  # installed system's entries.
  recoveryPartitionUki = media.mkUki {
    name = "gjallar-recovery-partition.efi";
    prettyName = "gjallarOS recovery (partition)";
    cmdline = "gjallar.iso=/gjallar-recovery.iso gjallar.recovery=partition";
  };
in
{
  imports = [ ../installer-media/live.nix ];

  # isoImage builds its file name from image.baseName; image.fileName alone
  # left it nixos-minimal-*.iso.
  image.baseName = lib.mkForce "gjallar-recovery-${config.system.nixos.label}-${pkgs.stdenv.hostPlatform.system}";
  # Started from a stick, stage 1 finds the medium by this label. Upstream's
  # nixos-minimal-<release>-<arch> also matches a stock NixOS stick of the
  # same release. ISO 9660 allows 32 characters.
  isoImage.volumeID = "gjallar-recovery-${config.system.nixos.release}-${pkgs.stdenv.hostPlatform.linuxArch}";
  isoImage.contents = [
    {
      source = recoveryPartitionUki;
      target = "/EFI/gjallar/recovery-partition.efi";
    }
    {
      source = media.store;
      target = "/nix-store.squashfs";
    }
    {
      source = "${media.storeVerity}/nix-store.squashfs.verity";
      target = "/nix-store.squashfs.verity";
    }
  ];

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

  networking.hostName = "gjallar-recovery";

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
}
