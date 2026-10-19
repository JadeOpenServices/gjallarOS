{
  config,
  lib,
  pkgs,
  settings,
  ...
}:
let
  webApplication = import ../lib/web-application.nix { inherit lib pkgs; };

  webApplications = settings.webApplications or [ ];
  planeApplication = lib.findFirst (
    application: (application.id or "") == "plane"
  ) null webApplications;

  planeEnable = planeApplication != null;
  planeHost = if planeEnable then planeApplication.endpoint or "" else "";

  planeLauncher = webApplication.mkIsolatedWebApplication {
    name = "plane";
    browser = pkgs.ungoogled-chromium;
    profile = ".config/gjallarOS/chromium-plane";
    url = planeHost;
    browserArguments = [
      "--no-first-run"
      "--no-default-browser-check"
      "--disable-sync"
      "--disable-background-mode"
    ];
  };
in
{
  config = lib.mkIf (config.gjallar.apps.plane.enable && planeEnable) {
    home.packages = [
      planeLauncher
    ];



    xdg.desktopEntries.plane = {
      name = "Plane";
      comment = "Open the configured Plane workspace";
      exec = "${planeLauncher}/bin/plane";
      icon = "${pkgs.papirus-icon-theme}/share/icons/Papirus/64x64/apps/planner.svg";
      terminal = false;
      categories = [
        "Development"
        "Office"
        "ProjectManagement"
      ];
    };
  };
}
