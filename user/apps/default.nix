{ lib, settings, ... }:

let
  all = import ../../apps;
  catalogue = lib.filterAttrs (_: app: builtins.pathExists (app.path + "/home.nix")) all;

  # `settings.apps` selects catalogue apps. System apps (ai, containers,
  # nemu) are enabled on the NixOS side; see apps/nixos-options.nix.
  # tailscale has both sides.
  # Retired apps (apps/retired.json) still in an older config are ignored;
  # rebuild drops them from user.config.json.
  retired = builtins.fromJSON (builtins.readFile ../../apps/retired.json);
  listed = builtins.filter (id: !(builtins.elem id retired)) settings.apps;

  selected = builtins.filter (id: catalogue ? ${id}) listed;

  unknown = builtins.filter (id: !(all ? ${id})) listed;
in
{
  imports = [
    ../../apps/home.nix

    # Base: the window managers launch ghostty, and fingerprint follows
    # the hardware ODDC resolved.
    ./ghostty.nix
    ./fingerprint.nix
  ];

  gjallar.apps =
    assert lib.assertMsg (unknown == [ ]) "unknown apps: ${toString unknown}";
    lib.genAttrs selected (_: {
      enable = true;
    });
}
