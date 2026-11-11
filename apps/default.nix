# The gjallarOS app catalogue. Every folder with a meta.json is one app;
# its ID is the folder name. Each app may provide home.nix (Home Manager)
# and nixos.nix (NixOS), and does nothing until gjallar.apps.<id>.enable.
let
  entries = builtins.readDir ./.;
  isApp = id: entries.${id} == "directory" && builtins.pathExists (./. + "/${id}/meta.json");
in
builtins.listToAttrs (
  map (id: {
    name = id;
    value = builtins.fromJSON (builtins.readFile (./. + "/${id}/meta.json")) // {
      inherit id;
      path = ./. + "/${id}";
    };
  }) (builtins.filter isApp (builtins.attrNames entries))
)
