{
  config,
  pkgs,
  lib,
  settings,
  installState,
  ...
}:
{
  imports = [
    ../themes/lib/common.nix
    ../themes/lib/home.nix
    ./shells/${settings.shell}.nix
    ./services/storage-maintenance.nix
    ./services/resource-qos.nix
    ./services/removable-media.nix
    ./url-handler.nix
    ./browsers/login.nix
    ./apps
  ]
  ++ (map (wm: ./wm/${wm}) settings.wms)
  ++ (map (editor: ./editors/${editor}) settings.editors)
  ++ (map (browser: ./browsers/${browser}.nix) settings.browsers);

  home = {
    username = settings.username;
    homeDirectory = "/home/${settings.username}";
    stateVersion = installState.homeManagerStateVersion;
  };

  xdg = {
    enable = true;

    userDirs = {
      enable = true;
      createDirectories = true;

      music = "${config.home.homeDirectory}/Music";
      videos = "${config.home.homeDirectory}/Videos";
      pictures = "${config.home.homeDirectory}/Pictures";
      download = "${config.home.homeDirectory}/Downloads";
      documents = "${config.home.homeDirectory}/Documents";

      templates = null;
      desktop = null;
      publicShare = null;
      projects = null;

      extraConfig =
        if settings.dotfilesDir != "" then
          { DOTFILES = settings.dotfilesDir; }
        else
          { };
    };
  };

  home.sessionVariables = {
    EDITOR = lib.getExe pkgs.${settings.preferredEditor};
  };

  programs.home-manager.enable = true;
}
