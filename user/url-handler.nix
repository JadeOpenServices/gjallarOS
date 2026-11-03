{
  config,
  lib,
  pkgs,
  settings,
  ...
}:
let
  urlLib = import ../apps/lib/url.nix { inherit lib; };
  cfg = config.gjallar.urlHandler;
  # The configured browser, with its policies and prefs.
  browser = lib.getExe config.programs.${settings.preferredBrowser}.finalPackage;
  # Its desktop entry, named after the program (firefox.desktop).
  browserDesktop = "${settings.preferredBrowser}.desktop";

  # One handler for every http/https link (xdg-open, portal OpenURI, $BROWSER).
  # Apps plug in exact https hosts; everything else goes to the browser.
  handler = pkgs.writeShellScriptBin "gjallar-open-url" ''
    set -euo pipefail
    if [ "$#" -ne 1 ]; then
      echo "gjallar-open-url: expects exactly one URL" >&2
      exit 2
    fi
    url="$1"
    ${urlLib.parseUrl}
    case "$url_scheme:$url_host" in
    ${lib.concatMapStrings (route: ''
      ${urlLib.hostPattern "https" route.hosts})
        exec ${route.command} "$url"
        ;;
    '') cfg.routes}
      http:* | https:*)
        exec ${browser} "$url"
        ;;
      file:*)
        # Only via $BROWSER (cargo doc --open etc.); not a registered scheme.
        case "$url" in
          file:///*) exec ${browser} "$url" ;;
        esac
        ;;
    esac
    echo "gjallar-open-url: refusing $url_scheme URL" >&2
    exit 2
  '';
in
{
  options.gjallar.urlHandler.routes = lib.mkOption {
    type = lib.types.listOf (
      lib.types.submodule {
        options = {
          hosts = lib.mkOption {
            type = lib.types.nonEmptyListOf (lib.types.strMatching "[a-z0-9.-]+");
            description = "Exact lowercase https hosts routed to command.";
          };
          command = lib.mkOption {
            type = lib.types.str;
            description = "Executable called with the URL as its only argument.";
          };
        };
      }
    );
    default = [ ];
    description = "https hosts opened by an app instead of the browser.";
  };

  config = {
    home.packages = [
      handler
      (pkgs.writeTextDir "share/zsh/site-functions/_gjallar-open-url" ''
        #compdef gjallar-open-url
        _arguments '1:URL:_urls'
      '')
    ];
    home.sessionVariables.BROWSER = "${handler}/bin/gjallar-open-url";

    xdg.mimeApps = {
      enable = true;
      defaultApplications = {
        "x-scheme-handler/http" = [ "gjallar-url-handler.desktop" ];
        "x-scheme-handler/https" = [ "gjallar-url-handler.desktop" ];
        # Local web pages too. Without an entry the first installed program
        # that lists text/html won, alphabetically (Edge from Teams, 257).
        "text/html" = [ browserDesktop ];
        "application/xhtml+xml" = [ browserDesktop ];
      };
    };

    xdg.dataFile."applications/gjallar-url-handler.desktop".text = ''
      [Desktop Entry]
      Type=Application
      Name=GjallarOS URL handler
      NoDisplay=true
      Exec=${handler}/bin/gjallar-open-url %u
      MimeType=x-scheme-handler/http;x-scheme-handler/https;
    '';
  };
}
