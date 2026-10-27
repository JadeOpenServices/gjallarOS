{ pkgs }:
let
  inherit (pkgs) lib;
  lock = builtins.fromJSON (builtins.readFile ../../flake.lock);
  nixpkgs = lock.nodes.${lock.nodes.root.inputs.nixpkgs}.locked;
  encode = lib.replaceStrings [ "+" "/" "=" ] [ "%2B" "%2F" "%3D" ];
  ref = "github:${nixpkgs.owner}/${nixpkgs.repo}/${nixpkgs.rev}?narHash=${encode nixpkgs.narHash}#git";
  script = builtins.readFile ../../scripts/installation/install.sh;
in
# install.sh fetches git before any checkout exists, so it cannot read
# flake.lock; its pinned nixpkgs must follow every flake.lock update.
assert lib.assertMsg (lib.hasInfix "'${ref}'" script)
  "scripts/installation/install.sh must use ${ref} for git";
pkgs.runCommand "gjallar-bootstrap-pins-check" { } "touch $out"
