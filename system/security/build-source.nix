{
  lib,
  pkgs,
  settings,
  buildSource,
  sourceRevision,
  ...
}:
let
  gjallarctlPackage = pkgs.callPackage ../../pkgs/gjallarctl { };
  offPin = (buildSource.oddc or null) != (buildSource.oddcPinned or null);
in
{
  # The commit this generation was built from, as `nixos-version
  # --configuration-revision` prints it. Builds without gjallarctl's facts
  # keep the flake's own provenance.
  system.configurationRevision = buildSource.revision or sourceRevision;

  environment.etc."gjallar/build-source.json".text = builtins.toJSON buildSource;

  # While ODDC runs a commit given with --override-input oddc instead of
  # the pinned one: a notice at every login and after every switch
  # (security-notice.nix writes /run/gjallar/system-generation), gone after
  # a plain rebuild.
  home-manager.users.${settings.username} = lib.mkIf offPin {
    systemd.user.services.oddc-notice = {
      Unit = {
        Description = "Notice while ODDC is off its pinned commit";
        After = [ "hyprland-session.target" ];
        PartOf = [ "hyprland-session.target" ];
      };
      Service = {
        Type = "exec";
        ExecStart = "${gjallarctlPackage}/bin/gjallarctl security oddc-notice";
      };
      Install.WantedBy = [ "hyprland-session.target" ];
    };
    systemd.user.paths.oddc-notice = {
      Unit = {
        Description = "Watch for a new system generation";
        PartOf = [ "hyprland-session.target" ];
      };
      Path.PathChanged = "/run/gjallar/system-generation";
      Install.WantedBy = [ "hyprland-session.target" ];
    };
  };
}
