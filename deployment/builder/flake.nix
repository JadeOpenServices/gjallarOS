# Release builder system, updated in place inside the VM:
#   nixos-rebuild switch --flake github:JadeOpenServices/gjallarOS/<rev>?dir=deployment/builder#gjallaros-build
# Own flake: the main one needs generated/state.nix, which only exists on a
# device. nixpkgs is locked to the same revision as the main flake.
{
  description = "gjallarOS second release builder";

  inputs.nixpkgs.url = "github:nixos/nixpkgs/nixos-26.05";

  outputs =
    { nixpkgs, ... }:
    {
      nixosConfigurations.gjallaros-build = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        modules = [
          ./configuration.nix
          "${nixpkgs}/nixos/modules/virtualisation/disk-image.nix"
        ];
      };
    };
}
