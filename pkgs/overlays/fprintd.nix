final: prev:
let
  lib = prev.lib;
in
{
  # A Claim that arrives while the reader still closes for the previous
  # holder waits for the close instead of failing with AlreadyInUse, so a
  # lock screen that locks again right after a fingerprint unlock can scan
  # again; see
  # tests/nix/fprintd-claim-after-close.nix.
  fprintd =
    assert lib.assertMsg (prev.fprintd.version == "1.94.5")
      "gjallarOS fprintd overlay was written for 1.94.5; check whether ${prev.fprintd.version} still refuses a Claim while the device closes for a vanished client, and update or remove pkgs/overlays/fprintd.nix.";
    prev.fprintd.overrideAttrs (old: {
      patches = (old.patches or [ ]) ++ [ ../patches/fprintd-claim-waits-for-close.patch ];
    });
}
