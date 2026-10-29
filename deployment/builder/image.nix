# qcow2 disk image of the release builder, for import into a hypervisor:
#   nix-build deployment/builder/image.nix --arg nixpkgs <nixpkgs source>
{ nixpkgs }:
(import "${nixpkgs}/nixos/lib/eval-config.nix" {
  system = "x86_64-linux";
  modules = [
    ./configuration.nix
    "${nixpkgs}/nixos/modules/virtualisation/disk-image.nix"
  ];
}).config.system.build.image
