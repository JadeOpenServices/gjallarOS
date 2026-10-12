# Hyprland clears a cursor image another compositor left on the hardware
# cursor plane. The greeter (a wlroots compositor) puts its cursor there on
# Intel, and 0.55.4 only cleared the plane after setting it itself, so the
# old image could stay on screen next to the real pointer. Fixed upstream
# in 0.56.0; drop this when nixpkgs ships it.
final: prev:

let
  lib = prev.lib;
in
{
  hyprland =
    assert lib.assertMsg (prev.hyprland.version == "0.55.4")
      "GjallarOS hyprland overlay patches 0.55.4; drop pkgs/patches/hyprland-stale-cursor.patch if ${prev.hyprland.version} has Hyprland PR #15376.";
    prev.hyprland.overrideAttrs (old: {
      patches = (old.patches or [ ]) ++ [ ../patches/hyprland-stale-cursor.patch ];
    });
}
