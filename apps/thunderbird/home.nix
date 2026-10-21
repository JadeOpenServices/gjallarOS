{ config, lib, ... }:

# Every mailto link on the system opens Thunderbird, whatever browser is
# installed (Firefox hands them on, see user/browsers/firefox.nix).
lib.mkIf config.gjallar.apps.thunderbird.enable {
  xdg.mimeApps = {
    enable = true;
    defaultApplications = {
      "x-scheme-handler/mailto" = [ "thunderbird.desktop" ];
      "message/rfc822" = [ "thunderbird.desktop" ];
    };
  };
}
