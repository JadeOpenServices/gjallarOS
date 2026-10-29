# A primary GPU that cannot make an EGL renderer (bochs in QEMU, other
# KMS-only nodes) made aquamarine 0.11.0 retry the renderer on every
# commit: it rebuilt the GBM allocator and wrote four error lines per
# frame. With Hyprland logging on, that filled /run/user within hours and
# every user service failed after. The patch tries once and carries on
# without one, as upstream already does for DisplayLink (evdi, PR #310).
# Still in aquamarine main as of 2026-10-29; upstream queue: wiki book 753.
final: prev:

let
  lib = prev.lib;
in
{
  aquamarine =
    assert lib.assertMsg (prev.aquamarine.version == "0.11.0")
      "GjallarOS aquamarine overlay patches 0.11.0; drop pkgs/patches/aquamarine-primary-renderer-once.patch if updateSecondaryRendererState no longer retries initMgpu for a primary GPU on every commit.";
    prev.aquamarine.overrideAttrs (old: {
      patches = (old.patches or [ ]) ++ [ ../patches/aquamarine-primary-renderer-once.patch ];
    });
}
