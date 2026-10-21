{ lib, settings, ... }:

let
  all = import ../../apps;
  catalogue = lib.filterAttrs (_: app: builtins.pathExists (app.path + "/home.nix")) all;

  # `settings.apps` selects catalogue apps. System apps (ai, containers,
  # nemu) are enabled on the NixOS side; see apps/nixos-options.nix.
  # tailscale has both sides.
  selected = builtins.filter (id: catalogue ? ${id}) settings.apps;

  unknown = builtins.filter (id: !(all ? ${id})) settings.apps;
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
