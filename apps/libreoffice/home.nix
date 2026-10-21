{ config, lib, pkgs, ... }:
lib.mkIf config.gjallar.apps.libreoffice.enable {
  home.packages = [ pkgs.libreoffice-fresh ];

  xdg.mimeApps = {
    enable = true;
    defaultApplications."application/pdf" = [ "draw.desktop" ];
  };
}
