{
  lib,
  pkgs,
  settings,
  ...
}:
let
  usbguard = settings.usbguardEnable or false;
  diskReview = pkgs.writeShellApplication {
    name = "gjallar-disk-review";
    runtimeInputs = [
      pkgs.python3
      pkgs.util-linux
      pkgs.systemd
      pkgs.zenity
    ];
    text = ''
      exec python3 ${./removable-media-review.py}
    '';
  };
  gjallarctlPackage = pkgs.callPackage ../../pkgs/gjallarctl { };
in
lib.mkMerge [
  {
    # Disks from another system open read-only on every system
    # (system/services/foreign-media-readonly.nix, Plane 286). The file
    # manager cannot say why, so this notice does and offers "Allow writing".
    systemd.user.services.foreign-media-notice = {
      Unit = {
        Description = "Offer writing on disks from another system";
        After = [ "graphical-session.target" ];
        PartOf = [ "graphical-session.target" ];
      };
      Service = {
        Type = "exec";
        ExecStart = "${gjallarctlPackage}/bin/gjallarctl media notice";
        Restart = "on-failure";
        RestartSec = 5;
      };
      Install.WantedBy = [ "graphical-session.target" ];
    };
  }
  (lib.mkIf usbguard {
    home.packages = [
      pkgs.zenity
    ];

    # pkgs/overlays/udiskie.nix: only mounts what is there at plug-in, not
    # what dd, mkfs or a stick writer creates later.
    services.udiskie = {
      enable = true;
      automount = true;
      notify = true;
      tray = "auto";
    };

    systemd.user.services.removable-media-review =
      lib.mkIf (!settings.management.enable)
        {
          Unit = {
            Description = "Review unformatted USB disks";
            After = [ "graphical-session.target" ];
            PartOf = [ "graphical-session.target" ];
          };
          Service = {
            ExecStart = "${diskReview}/bin/gjallar-disk-review";
            Restart = "on-failure";
            RestartSec = 5;
          };
          Install.WantedBy = [ "graphical-session.target" ];
        };
  })
]
