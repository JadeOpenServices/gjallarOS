# snapshot_create onchange compared the newest snapshot's creation generation
# with the subvolume's generation and skipped only when they were equal. Up to
# Linux 6.18 taking a snapshot also bumped the source subvolume's generation,
# so the two matched. On the 7.x kernels GjallarOS ships the source keeps its
# generation and the snapshot lands in a later transaction, so an unchanged
# /home or /etc/gjallar/system got a new snapshot on every timer run. The
# patch commits pending changes first, rereads the generation and skips when
# the newest snapshot of this very subvolume is not older than it, which holds
# on both kernels. Upstream: digint/btrbk#656, book 753.
# Test: checks btrbk-onchange (6.18 and linuxPackages_latest); remove when
# stock btrbk skips unchanged subvolumes on linuxPackages_latest.
final: prev:

let
  lib = prev.lib;
in
{
  btrbk =
    assert lib.assertMsg (prev.btrbk.version == "0.32.7")
      "GjallarOS btrbk overlay was written for 0.32.7; check whether ${prev.btrbk.version} still snapshots unchanged subvolumes with snapshot_create onchange (digint/btrbk#656) and update or remove pkgs/overlays/btrbk.nix.";
    prev.btrbk.overrideAttrs (old: {
      patches = (old.patches or [ ]) ++ [ ../patches/btrbk-onchange-up-to-date.patch ];
    });
}
