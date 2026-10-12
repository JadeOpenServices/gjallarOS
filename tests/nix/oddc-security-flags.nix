# ODDC item 82.4: every security flag and finding field gjallarctl reads must
# still come out of `oddc security --json` at the pinned ODDC revision. A
# missing flag fails closed at runtime (unknown = unprotected); this check says
# so before a pin bump lands. The flag names are read from the gjallarctl
# source, so a new consumer is covered without editing this file.
#
# Only names and types are checked, never values: what a model is protected
# against stays ODDC's knowledge (Jade's rule, ODDC 82).
{
  nixpkgs,
  system,
  oddc,
  gjallarctlSource,
}:
let
  pkgs = nixpkgs.legacyPackages.${system};

  # The models the e2e VMs emulate. A model without findings has no flags
  # (unknown, ODDC 97), so a flag only has to show up on one of them.
  models = [
    "model/hp/zbook-x2-g4"
    "model/framework/laptop-13-amd-ryzen-7040"
  ];

  machine =
    model:
    (nixpkgs.lib.nixosSystem {
      inherit system;
      modules = [
        oddc.nixosModules.default
        {
          oddc.catalog = oddc;
          oddc.device = model;
          boot.loader.grub.enable = false;
          fileSystems."/" = {
            device = "none";
            fsType = "tmpfs";
          };
          system.stateVersion = "25.11";
        }
      ];
    }).config;

  first = machine (builtins.head models);
  oddcPackage = builtins.head (
    builtins.filter (p: (p.pname or "") == "oddc") first.environment.systemPackages
  );

  reports = map (model: {
    inherit model;
    resolved = (machine model).environment.etc."oddc/resolved.json".source;
  }) models;
in
pkgs.runCommand "gjallar-oddc-security-flags-check"
  {
    nativeBuildInputs = [
      oddcPackage
      pkgs.python3
    ];
  }
  ''
    mkdir reports
    ${pkgs.lib.concatMapStrings (r: ''
      oddc security --json --resolved ${r.resolved} > "reports/${builtins.replaceStrings [ "/" ] [ "_" ] r.model}.json"
    '') reports}
    python3 -I ${./oddc-security-flags.py} ${gjallarctlSource} reports
    touch $out
  ''
