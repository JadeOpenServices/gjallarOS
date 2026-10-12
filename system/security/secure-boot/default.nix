{ pkgs, ... }:
let
  gjallarctlPackage = pkgs.callPackage ../../../pkgs/gjallarctl { };
in
{
  _module.args.gjallarctlPackage = gjallarctlPackage;

  imports = [
    ./lanzaboote.nix
    ./verification.nix
    ./lifecycle.nix
    ./measured-boot.nix
    ./dbx-update.nix
    ./tool.nix
  ];
}
