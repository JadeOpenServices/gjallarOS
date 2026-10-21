# KIO 6.26 takes a FUSE mount such as OpenVFS (~/Nextcloud) for a fast local
# disk. Dolphin then reads every file there to guess its type and makes
# thumbnails, and each read downloads an online-only file (Plane 252).
# KIO 6.31 counts FUSE mounts that do not come from a /dev device as slow:
# file types come from the name only and remote preview limits apply.
# Upstream 26081ad6 does the same in the file worker, which Dolphin asks for
# the type of files whose name gives none. The patch is that upstream series
# (4c5fb059, 741f40da, 0d477be2, 26081ad6) on 6.26.
# Test: checks kio-fuse-slow; remove once stock KIO has all four.
final: prev:
let
  lib = prev.lib;
in
{
  kdePackages = prev.kdePackages.overrideScope (
    _scopeFinal: scopePrev: {
      kio =
        assert lib.assertMsg (scopePrev.kio.version == "6.26.0")
          "GjallarOS KIO overlay backports the 6.31 FUSE slow-mount check to 6.26.0; drop pkgs/overlays/kio.nix if ${scopePrev.kio.version} has KIO commits 0d477be2 and 26081ad6, otherwise update the patch.";
        scopePrev.kio.overrideAttrs (old: {
          patches = (old.patches or [ ]) ++ [ ../patches/kio-fuse-mounts-slow.patch ];
        });
    }
  );
}
