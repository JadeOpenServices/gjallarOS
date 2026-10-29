# gjallar.apps.<id>.enable for every app with a NixOS side, selected by
# `settings.apps`.
{ lib, settings, ... }:

let
  catalogue = lib.filterAttrs (_: app: builtins.pathExists (app.path + "/nixos.nix")) (import ./.);
in
{
  options.gjallar.apps = lib.mapAttrs (_: app: {
    enable = lib.mkEnableOption app.name;
  }) catalogue;

  config.gjallar.apps = lib.mapAttrs (id: _: {
    enable = lib.mkDefault (builtins.elem id settings.apps);
  }) catalogue;

  # The `apps` setting this system was built with, so the app window can
  # tell a saved change from a built one (`gjallarctl apps list`).
  config.environment.etc."gjallar/apps.json".text = builtins.toJSON (lib.sort lib.lessThan settings.apps);
}
