# Ghostty releases the mouse button when a touch is cancelled. A touch the
# compositor takes as a gesture (hyprgrass sends wl_touch.cancel) never gets
# a GTK "released", so the terminal kept the button down and every later
# pointer move grew the selection.
final: prev:

let
  lib = prev.lib;
in
{
  ghostty =
    assert lib.assertMsg (prev.ghostty.version == "1.3.1")
      "GjallarOS ghostty overlay patches 1.3.1; check pkgs/patches/ghostty-touch-cancel.patch against ${prev.ghostty.version}.";
    prev.ghostty.overrideAttrs (old: {
      patches = (old.patches or [ ]) ++ [ ../patches/ghostty-touch-cancel.patch ];
    });
}
