# Hyprland clears a cursor image another compositor left on the hardware
# cursor plane. The greeter (a wlroots compositor) puts its cursor there on
# Intel, and 0.55.4 only cleared the plane after setting it itself, so the
# old image could stay on screen next to the real pointer. Fixed upstream
# in 0.56.0; drop this when nixpkgs ships it.
#
# A screen capture grant ("Allow once") belongs to one Wayland client, but
# 0.55.4 only dropped it when the compositor shut down. A later client that
# got the same address kept the grant and could capture without asking.
# Fixed on Hyprland main (PR #16353) after 0.56.2; drop this when nixpkgs
# ships a release with it.
#
# Hyprland told every app that scrolling was never natural, even with
# input:touchpad:natural_scroll on. Aquamarine already reads the setting
# from libinput, but 0.55.4 (and main as of 2026-10-12) sends IDENTICAL
# instead. Apps that follow the fingers (Qt's QWheelEvent::inverted, the
# Dolphin swipe in dolphin.nix) then went the wrong way.
#
# sendshortcut looked the key up in the keyboard's live xkb state, which
# holds the pressed modifiers. The paste-once bind for Ctrl+Shift+V runs
# while Shift is still down, so v read as V, the lookup failed and nothing
# was pasted until a plain Ctrl+V had put v in Hyprland's keycode cache.
# The patch looks keys up in the state that only tracks the layout. Open
# upstream as of 2026-10-13.
final: prev:

let
  lib = prev.lib;
in
{
  hyprland =
    assert lib.assertMsg (prev.hyprland.version == "0.55.4")
      "GjallarOS hyprland overlay patches 0.55.4; drop pkgs/patches/hyprland-stale-cursor.patch if ${prev.hyprland.version} has Hyprland PR #15376, pkgs/patches/hyprland-permission-client-destroy.patch if it has PR #16353, pkgs/patches/hyprland-axis-relative-direction.patch if onMouseWheel passes e.relativeDirection, and pkgs/patches/hyprland-sendshortcut-unshifted.patch if sendshortcut no longer reads KB->m_xkbState.";
    prev.hyprland.overrideAttrs (old: {
      patches = (old.patches or [ ]) ++ [
        ../patches/hyprland-stale-cursor.patch
        ../patches/hyprland-permission-client-destroy.patch
        ../patches/hyprland-axis-relative-direction.patch
        ../patches/hyprland-sendshortcut-unshifted.patch
      ];
    });
}
