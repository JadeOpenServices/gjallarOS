# The Hyprland portal crashed at every logout: after its event loop ended it
# disconnected from Wayland, then the outputs, registry and other proxies were
# destroyed at exit() and wrote to the freed connection. Drop them before the
# disconnect (upstream PR #443, still open; 1.4.1 has the same bug). Drop this
# when nixpkgs ships a release with the fix.
final: prev:

let
  lib = prev.lib;
in
{
  xdg-desktop-portal-hyprland =
    assert lib.assertMsg (prev.xdg-desktop-portal-hyprland.version == "1.3.12")
      "GjallarOS xdg-desktop-portal-hyprland overlay patches 1.3.12; drop pkgs/patches/xdph-release-wayland.patch if ${prev.xdg-desktop-portal-hyprland.version} has xdph PR #443.";
    prev.xdg-desktop-portal-hyprland.overrideAttrs (old: {
      patches = (old.patches or [ ]) ++ [ ../patches/xdph-release-wayland.patch ];
    });
}
