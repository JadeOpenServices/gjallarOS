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
  teamsEnable = builtins.any (
    application: (application.id or "") == "teams"
  ) webApplications;

  teamsHosts = [
    "teams.microsoft.com"
    "teams.live.com"
    "teams.microsoft.us"
    "teams.cloud.microsoft"
  ];

  teamsApp = webApplication.mkIsolatedWebApplication {
    name = "gjallar-teams";
    browser = pkgs.microsoft-edge;
    profile = ".config/microsoft-edge-teams";
    url = "https://teams.microsoft.com";
    runtimeHosts = teamsHosts;
    browserArguments = [
      "--password-store=basic"
      "--class=gjallar-teams"
      "--name=Microsoft Teams"
    ];
  };

  # msteams:/l/meetup-join/... -> https://teams.microsoft.com/l/meetup-join/...
  msteamsHandler = pkgs.writeShellScript "gjallar-open-msteams" ''
    set -euo pipefail
    if [ "$#" -ne 1 ]; then
      echo "gjallar-open-msteams: expects exactly one URL" >&2
      exit 2
    fi
    path="''${1#[Mm][Ss][Tt][Ee][Aa][Mm][Ss]:}"
    case "$path" in
      "$1" | //* | *[[:space:][:cntrl:]\\]*)
        echo "gjallar-open-msteams: refusing $1" >&2
        exit 2
        ;;
      /*) exec ${teamsApp}/bin/gjallar-teams "https://teams.microsoft.com$path" ;;
    esac
    echo "gjallar-open-msteams: refusing $1" >&2
    exit 2
  '';
in
{
  config = lib.mkIf (config.gjallar.apps.teams.enable && teamsEnable) {
    home.packages = [
      pkgs.microsoft-edge
      teamsApp
    ];

    gjallar.urlHandler.routes = [
      {
        hosts = teamsHosts;
        command = "${teamsApp}/bin/gjallar-teams";
      }
    ];

    home.file.".local/share/icons/hicolor/scalable/apps/gjallar-teams.svg".source =
      "${pkgs.papirus-icon-theme}/share/icons/Papirus/64x64/apps/teams-for-linux.svg";

    xdg.mimeApps = {
      enable = true;
      defaultApplications = {
        "x-scheme-handler/msteams" = [ "gjallar-msteams-handler.desktop" ];
      };
    };

    home.file.".local/share/applications/gjallar-msteams-handler.desktop".text = ''
      [Desktop Entry]
      Type=Application
      Name=Microsoft Teams link handler
      NoDisplay=true
      Exec=${msteamsHandler} %u
      MimeType=x-scheme-handler/msteams;
    '';

    home.file.".local/share/applications/microsoft-teams.desktop".text = ''
      [Desktop Entry]
      Type=Application
      Name=Microsoft Teams
      Exec=${teamsApp}/bin/gjallar-teams %U
      Terminal=false
      Icon=gjallar-teams
      Categories=Network;Office;InstantMessaging;
      StartupWMClass=gjallar-teams
    '';

    home.activation.gjallarDesktopDatabase =
      lib.hm.dag.entryAfter [ "writeBoundary" ] ''
        if command -v update-desktop-database >/dev/null 2>&1; then
            update-desktop-database "$HOME/.local/share/applications" >/dev/null 2>&1 || true
        fi
      '';

    xdg.dataFile."applications/microsoft-edge.desktop".text = ''
      [Desktop Entry]
      Type=Application
      Name=Backend Browser
      NoDisplay=true
      Hidden=true
    '';
  };
}
