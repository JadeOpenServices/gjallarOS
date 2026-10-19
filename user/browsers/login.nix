{ lib, ... }:

{
  options.gjallar.loginBrowsers = lib.mkOption {
    type = lib.types.attrsOf lib.types.path;
    default = { };
    description = ''
      Per browser, an executable run as `RECIPE PROFILE_DIR APP_ID URL` that
      opens URL in a throwaway profile in PROFILE_DIR, with window class
      APP_ID and no saved passwords. It must exec the browser, so the window
      keeps the pid Hyprland started.
    '';
  };
}
