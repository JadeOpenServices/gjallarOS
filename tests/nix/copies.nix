# Copied code (Plane 445): fails on a copy that tests/copies/known.json does
# not list, and on a listed copy that is gone. How to: docs/copies.md.
{ pkgs }:
let
  inherit (pkgs) lib;
  root = ../..;
  code =
    file:
    lib.any file.hasExt [
      "go"
      "py"
      "js"
      "lua"
      "nix"
      "sh"
      "qml"
      "luau"
    ];
  # The checker leaves tests out by path; only the list comes from tests/.
  sources = lib.fileset.toSource {
    inherit root;
    fileset = lib.fileset.unions [
      (lib.fileset.difference (lib.fileset.fileFilter code root) ../../tests)
      ../copies/known.json
    ];
  };
in
pkgs.runCommand "gjallar-copies-check"
  {
    nativeBuildInputs = [
      pkgs.python3
      pkgs.python3Packages.lizard
    ];
  }
  ''
    python3 -I ${../copies/copies.py} ${sources}
    touch $out
  ''
