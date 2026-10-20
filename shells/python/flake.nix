{
  description = "Python development shell";

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
          packages = [
            (pkgs.python3.withPackages (python-pkgs: [
              python-pkgs.pip
              python-pkgs.numpy
              python-pkgs.graphviz
              python-pkgs.sympy
              python-pkgs.scipy
              python-pkgs.pandas
              python-pkgs.matplotlib
              python-pkgs.virtualenv
              python-pkgs.jupyterlab
            ]))
          ];

          hardeningDisable = [ "fortify" ];
        };
      }
    );
}
