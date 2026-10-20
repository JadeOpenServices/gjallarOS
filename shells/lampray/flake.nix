{
  description = "Nix shell for running Lampray";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-26.05";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs { inherit system; };
      in
      {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            zenity
          ];
          shellHook = ''
          '';
          LD_LIBRARY_PATH = "${nixpkgs.lib.makeLibraryPath [
            pkgs.SDL2
            pkgs.lz4
            pkgs.p7zip
          ]}";
          hardeningDisable = [ "fortify" ];
        };
      }
    );
}
