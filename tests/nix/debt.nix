# Debt register (Plane 445): fails on a patch that tests/debt/register.json
# does not list, on a missing guard or guard test, and on a tier 1 review
# date that is overdue or more than 30 days ahead. system/release is an
# input, so every release runs the date check again. How to: docs/debt.md.
{ pkgs }:
let
  inherit (pkgs) lib;
  root = ../..;
  register = lib.importJSON ../debt/register.json;
  # Guard and test name a Plane item until the real file exists.
  named = lib.concatMap (
    entry:
    entry.members
    ++ lib.filter (p: !(lib.hasPrefix "Plane " p)) (
      lib.optional (entry ? guard) entry.guard ++ lib.optional (entry ? test) entry.test
    )
  ) register.debt;
  # maybeMissing: a missing file is reported by debt.py, not as an eval error.
  sources = lib.fileset.toSource {
    inherit root;
    fileset = lib.fileset.unions (
      [
        (lib.fileset.fileFilter (file: file.hasExt "patch") root)
        ../debt/register.json
        ../copies/known.json
        ../../flake.nix
        ../../system/release
      ]
      ++ map (path: lib.fileset.maybeMissing (root + "/${path}")) named
    );
  };
in
pkgs.runCommand "gjallar-debt-check" { nativeBuildInputs = [ pkgs.python3 ]; } ''
  python3 -I ${../debt/debt.py} ${sources}
  touch $out
''
