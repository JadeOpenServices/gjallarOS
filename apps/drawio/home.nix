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
  drawioApplication = lib.findFirst (
    application: (application.id or "") == "drawio"
  ) null webApplications;

  drawioEnable = drawioApplication != null;
  drawioHost = if drawioEnable then drawioApplication.endpoint or "" else "";
  drawioSelfHosted = drawioHost != "";

  publicEndpoint = "https://app.diagrams.net/";
  endpoint =
    if drawioSelfHosted
    then drawioHost
    else publicEndpoint;

  drawioLauncher = webApplication.mkIsolatedWebApplication {
    name = "gjallar-drawio";
    browser = pkgs.ungoogled-chromium;
    profile = ".config/gjallarOS/chromium-drawio";
    url = endpoint;
    browserArguments = [
      "--no-first-run"
      "--no-default-browser-check"
      "--disable-sync"
      "--disable-background-mode"
    ];
  };
in
{
  config = lib.mkIf (config.gjallar.apps.drawio.enable && drawioEnable) {
    home.packages = [
      drawioLauncher
    ];
    xdg.desktopEntries.drawio = {
      name = "Draw.io";
      genericName = "Diagram Editor";
      comment = "Open Draw.io";
      exec = "${drawioLauncher}/bin/gjallar-drawio";
      icon = "${pkgs.papirus-icon-theme}/share/icons/Papirus/64x64/apps/drawio.svg";
      terminal = false;
      categories = [
        "Development"
        "Graphics"
        "Office"
      ];
    };
  };
}
