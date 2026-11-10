{ lib }:
let
  table = lib.importJSON ./renames.json;
  kinds = [
    "command"
    "settings-key"
    "config-field"
    "label"
    "unit"
    "path"
  ];
  fields = [
    "kind"
    "old"
    "new"
    "removedIn"
    "deleteAfter"
    "item"
  ];
  # Old names land in shell case patterns and grep patterns; anything
  # beyond these characters is refused rather than quoted.
  safeName = name: builtins.match "[A-Za-z0-9._/-]+" name != null;
  # Release tags, the same form as system/release.
  releasePattern = "v[0-9]{4}[.][0-9]{2}[.][0-9]+";
  check =
    row:
    assert lib.assertMsg (lib.all (field: row ? ${field}) fields)
      "system/tools/legacy/renames.json: a row lacks one of ${toString fields}";
    assert lib.assertMsg (lib.elem row.kind kinds)
      "system/tools/legacy/renames.json: ${row.old} has unknown kind ${row.kind}";
    assert lib.assertMsg (safeName row.old)
      "system/tools/legacy/renames.json: ${row.old} has characters outside [A-Za-z0-9._/-]";
    assert lib.assertMsg (builtins.match releasePattern row.deleteAfter != null)
      "system/tools/legacy/renames.json: ${row.old} deleteAfter is not a release tag like v2026.10.70";
    assert lib.assertMsg (lib.all (pattern: pattern != "" && !(lib.hasInfix "\n" pattern)) (
      row.match or [ ]
    )) "system/tools/legacy/renames.json: ${row.old} has an empty or multi-line match";
    row;
  rows = map check table.renames;
in
{
  inherit rows releasePattern;
  commands = lib.filter (row: row.kind == "command") rows;
}
