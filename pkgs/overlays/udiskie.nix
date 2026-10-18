# udiskie mounts every filesystem that shows up on a USB stick, also the ones a
# tool has just written there (dd, mkfs, an installer stick writer), and the
# tool then cannot unmount or repartition (Plane 185, likely 186). Like
# GNOME, only mount what shows up within a few seconds of the media being
# detected.
# Test: checks udiskie-new-media-only; remove when its stock machine stops
# mounting a written stick.
final: prev:

let
  lib = prev.lib;
in
{
  udiskie =
    assert lib.assertMsg (prev.udiskie.version == "2.6.2")
      "GjallarOS udiskie overlay was written for 2.6.2; check whether ${prev.udiskie.version} still mounts devices that appear long after their media and update or remove pkgs/overlays/udiskie.nix.";
    prev.udiskie.overrideAttrs (old: {
      patches = (old.patches or [ ]) ++ [ ../patches/udiskie-mount-new-media-only.patch ];
    });
}
