# A NixOS module. Rename this file to example.nix to build it.
{ pkgs, ... }:
{
  environment.systemPackages = [ pkgs.hello ];
}
