{
  config,
  lib,
  pkgs,
  settings,
  ...
}:
let
  gjallarctlPackage = pkgs.callPackage ../../pkgs/gjallarctl { };
  managed = settings.endpointManagedDevice or false;

  # gjallarctl luks remind opens Ghostty and zenity; setup inside the
  # terminal needs sudo and the usual system tools.
  reminderEnvironment = [
    "PATH=${
      lib.makeBinPath [
        pkgs.ghostty
        pkgs.zenity
      ]
    }:/run/wrappers/bin:/etc/profiles/per-user/${settings.username}/bin:/run/current-system/sw/bin"
  ]
  ++ lib.optional managed "GJALLAR_MANAGED_DEVICE=1";
  remind = "${gjallarctlPackage}/bin/gjallarctl luks remind";
in
{
  # systemd-cryptsetup counts every unlock method against tries= (default
  # 3), not only typed passphrases: the TPM2 token takes the first try, and
  # after a wrong passphrase it retries the same one once without asking. One
  # typo then ended in emergency mode with root locked (e2e-fw13, 2026-10-09,
  # systemd 260). The counter restarts on every boot, so it never limited
  # guessing; keep asking instead.
  options.boot.initrd.luks.devices = lib.mkOption {
    type = lib.types.attrsOf (
      lib.types.submodule {
        config.crypttabExtraOpts = [ "tries=0" ];
      }
    );
  };

  # Which unlock methods the system disk has, as counts in
  # /run/gjallar-luks/keyslots, so the login reminder and the bar need no
  # root. gjallarctl luks setup and recovery-key rewrite it after a change.
  # Upstream's rule for the cryptsetup lock directory, which NixOS leaves out.
  config.systemd.tmpfiles.packages = [ pkgs.cryptsetup ];

  config.systemd.services.gjallar-luks-record = {
    description = "Record which disk unlock methods are set up";
    wantedBy = [ "multi-user.target" ];
    # After the first-boot chip enrollment and its recovery key.
    after = [ "measured-boot-tpm2-enrollment.service" ];
    path = [
      pkgs.cryptsetup
      pkgs.util-linux
      config.systemd.package
    ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = "${gjallarctlPackage}/bin/gjallarctl luks record";
      RuntimeDirectory = "gjallar-luks";
      RuntimeDirectoryMode = "0755";
      RuntimeDirectoryPreserve = true;
      ProtectSystem = "strict";
      # luksDump takes a read lock there; without it the header reads as
      # "not a valid LUKS device".
      ReadWritePaths = [ "/run/cryptsetup" ];
      ProtectHome = true;
      PrivateNetwork = true;
      NoNewPrivileges = true;
    };
  };

  # Until the disk is protected: a card at every login (Set up now, Later,
  # Don't ask again on standalone installs) and a shield in the bar
  # (user/wm/shells/noctalia/plugins/disk-protection).
  config.home-manager.users.${settings.username} = {
    systemd.user.services.disk-protection-reminder = {
      Unit = {
        Description = "Disk protection reminder";
        After = [ "hyprland-session.target" ];
        PartOf = [ "hyprland-session.target" ];
      };
      Service = {
        Type = "exec";
        ExecStart = remind;
        Environment = reminderEnvironment;
      };
      Install.WantedBy = [ "hyprland-session.target" ];
    };

    # The shield's click.
    systemd.user.services.disk-protection-setup = {
      Unit.Description = "Disk protection setup";
      Service = {
        Type = "exec";
        ExecStart = "${remind} --open";
        Environment = reminderEnvironment;
      };
    };

    # Keyslot changes outside the reminder (gjallarctl luks setup typed in a
    # terminal) update the shield right away.
    systemd.user.services.disk-protection-state = {
      Unit.Description = "Update the disk protection state";
      Service = {
        Type = "oneshot";
        ExecStart = "${remind} --refresh";
        Environment = reminderEnvironment;
      };
    };
    systemd.user.paths.disk-protection-state = {
      Unit = {
        Description = "Watch the disk unlock methods";
        PartOf = [ "hyprland-session.target" ];
      };
      Path.PathChanged = "/run/gjallar-luks/keyslots";
      Install.WantedBy = [ "hyprland-session.target" ];
    };
  };
}
