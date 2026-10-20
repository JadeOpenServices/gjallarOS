# systemd with remove-before-add.patch. Used by default.nix and by the
# flake's prebuilt set; what the patch does and when to drop it: default.nix.
{ pkgs }:

let
  inherit (pkgs) lib systemd;
in
assert lib.assertMsg (systemd.version == "260.5")
  "system/fixes/systemd-partition-reread was checked for systemd 260.5; check whether ${systemd.version} still adds partitions before it removes the ones in the way, then update or remove the fix.";
systemd.overrideAttrs (old: {
  patches = (old.patches or [ ]) ++ [ ./remove-before-add.patch ];
})
