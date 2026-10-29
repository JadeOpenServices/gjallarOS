{ lib, ... }:
{
  # The openJade palettes in Noctalia's custom palette list.
  xdg.configFile = lib.mapAttrs' (
    name: palette:
    lib.nameValuePair "noctalia/palettes/${name}.json" { text = builtins.toJSON palette; }
  ) (import ../../../../themes/palettes/openjade.nix);
}
