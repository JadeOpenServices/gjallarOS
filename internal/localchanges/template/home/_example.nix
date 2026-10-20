# A home-manager module for your account. Rename this file to example.nix
# to build it.
{ pkgs, ... }:
{
  home.packages = [ pkgs.hello ];
}
