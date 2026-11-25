# systemd with our patches. Used by default.nix and by the flake's prebuilt
# set; what each patch does and when to drop it: default.nix.
{ pkgs }:

let
  inherit (pkgs) lib systemd;
in
assert lib.assertMsg (systemd.version == "260.5")
  "system/fixes/systemd was checked for systemd 260.5; for ${systemd.version}, go through the \"Remove when\" part of each patch in system/fixes/systemd/default.nix, then rebase or drop each patch and update the version.";
systemd.overrideAttrs (old: {
  patches = (old.patches or [ ]) ++ [
    ./remove-before-add.patch
    ./tpm2-setup-lockout.patch
  ];
})
