{
  lib,
  pkgs,
  settings,
  ...
}:
let
  gjallarctlPackage = pkgs.callPackage ../../pkgs/gjallarctl { };
in
{
  # A user path unit cannot watch /run/current-system: systemd follows the
  # link and watches the store path it points to, which never changes. This
  # file is written in place on every switch instead.
  system.activationScripts.gjallar-system-generation = ''
    mkdir -p /run/gjallar
    printf '%s\n' "$systemConfig" > /run/gjallar/system-generation
  '';

  # One calm notice when there is a security finding for this model that
  # the user has not seen yet: at login, and after a rebuild brings a new or
  # changed one. ODDC owns its findings and their wording; GjallarOS adds its
  # own notes with IDs that start with "gjallaros/" (Plane 194).
  home-manager.users.${settings.username} = {
    systemd.user.services.security-notice = {
      Unit = {
        Description = "Security notes for this device";
        After = [ "hyprland-session.target" ];
        PartOf = [ "hyprland-session.target" ];
      };
      Service = {
        Type = "exec";
        ExecStart = "${gjallarctlPackage}/bin/gjallarctl security notice";
        # oddc comes with the system; a rebuild brings a newer one.
        Environment = [ "PATH=${lib.makeBinPath [ pkgs.ghostty ]}:/run/current-system/sw/bin" ];
      };
      Install.WantedBy = [ "hyprland-session.target" ];
    };
    systemd.user.paths.security-notice = {
      Unit = {
        Description = "Watch for a new system generation";
        PartOf = [ "hyprland-session.target" ];
      };
      Path.PathChanged = "/run/gjallar/system-generation";
      Install.WantedBy = [ "hyprland-session.target" ];
    };
  };
}
