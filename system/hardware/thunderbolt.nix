{
  lib,
  pkgs,
  settings,
  ...
}:
let
  managed = settings.endpointManagedDevice or false;
  gjallarctl = pkgs.callPackage ../../pkgs/gjallarctl { };

  # zenity --password has no --text option, see removable-media.nix.
  thunderboltAskpass = pkgs.writeShellScript "gjallar-thunderbolt-askpass" ''
    exec ${pkgs.zenity}/bin/zenity \
      --entry \
      --hide-text \
      --title="Authorize Thunderbolt device" \
      --text="Fingerprint authentication was unavailable, failed, or timed out.\nEnter your password to let this Thunderbolt device in."
  '';
in
{
  # Thunderbolt and USB4 controllers in "user" or "secure" mode keep new
  # devices out until boltd authorizes them; without boltd a dock never
  # works. udev starts boltd only when a Thunderbolt domain exists.
  services.hardware.bolt.enable = true;

  # bolt ships a rule that lets any active local wheel user authorize,
  # enroll and manage devices without a password. A device let in tunnels
  # PCIe, so that is the same decision as a USB trust change: admin password
  # every time (AUTH_ADMIN, not _KEEP). This file sorts before bolt's rule,
  # and the first rule with an answer wins. Root asks no one, which is how
  # the session review acts after sudo.
  security.polkit.extraConfig = ''
    polkit.addRule(function(action, subject) {
      if (action.id != "org.freedesktop.bolt.enroll" &&
          action.id != "org.freedesktop.bolt.authorize" &&
          action.id != "org.freedesktop.bolt.manage") {
        return polkit.Result.NOT_HANDLED;
      }
      if (!subject.active || !subject.local) {
        return polkit.Result.NO;
      }
      return polkit.Result.AUTH_ADMIN;
    });
  '';

  home-manager.users.${settings.username} = {
    systemd.user.services = lib.optionalAttrs (!managed) {
      thunderbolt-device-review = {
        Unit = {
          Description = "Thunderbolt device review";
          After = [ "hyprland-session.target" ];
          PartOf = [ "hyprland-session.target" ];
        };
        Service = {
          ExecStart = "${gjallarctl}/bin/gjallarctl thunderbolt review";
          Environment = [
            # oddc comes with the system and says whether the model's DMA
            # protection holds.
            "PATH=${
              lib.makeBinPath [
                pkgs.zenity
                "/run/current-system/sw"
              ]
            }"
            "SUDO_ASKPASS=${thunderboltAskpass}"
          ];
          Restart = "on-failure";
          RestartSec = 3;
        };
        Install.WantedBy = [ "hyprland-session.target" ];
      };
    };
  };
}
