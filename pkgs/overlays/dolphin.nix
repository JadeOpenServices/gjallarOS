# Two fingers swiped sideways on a touchpad go back or forward in Dolphin, as
# they do in web browsers. Dolphin only has this for touchscreens (KDE bug
# 506603).
final: prev:
let
  lib = prev.lib;
in
{
  kdePackages = prev.kdePackages.overrideScope (
    _scopeFinal: scopePrev: {
      dolphin =
        assert lib.assertMsg (scopePrev.dolphin.version == "26.04.3")
          "GjallarOS Dolphin touchpad swipe patch targets Dolphin 26.04.3; check whether KDE bug 506603 is fixed and update or remove pkgs/overlays/dolphin.nix.";
        scopePrev.dolphin.overrideAttrs (old: {
          patches = (old.patches or [ ]) ++ [ ../patches/dolphin-touchpad-swipe-navigation.patch ];
        });
    }
  );
}
