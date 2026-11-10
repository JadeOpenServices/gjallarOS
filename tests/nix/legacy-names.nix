# The legacy table (system/tools/legacy, Jade Q166) holds every rename. This
# check fails when an old name is still used in the product outside that
# folder, or when the tree's release (system/release) is past a row's
# deleteAfter tag. A revision gives the same answer on every machine and day.
{ pkgs }:
let
  inherit (pkgs) lib;
  inherit (import ../../system/tools/legacy/table.nix { inherit lib; }) rows releasePattern;
  release = lib.fileContents ../../system/release;
  version = tag: lib.removePrefix "v" tag;
  root = ../..;
  textFile =
    file:
    lib.any file.hasExt [
      "nix"
      "go"
      "sh"
      "py"
      "md"
      "qml"
      "js"
      "json"
      "lua"
      "conf"
      "service"
      "rules"
    ]
    || lib.hasPrefix "_" file.name;
  # Tests may name old names to prove they are refused, and captured
  # fixtures keep what the device printed.
  testFile = file: lib.hasSuffix "_test.go" file.name;
  sources = lib.fileset.toSource {
    inherit root;
    fileset = lib.fileset.difference (lib.fileset.fileFilter textFile root) (
      lib.fileset.unions [
        ../../system/tools/legacy
        ../../tests
        (lib.fileset.fileFilter testFile root)
      ]
    );
  };
  expired =
    assert lib.assertMsg (
      builtins.match releasePattern release != null
    ) "system/release must hold one release tag like v2026.10.63, not ${release}";
    lib.filter (row: builtins.compareVersions (version release) (version row.deleteAfter) > 0) rows;
  # Without an explicit list the old name is matched as a whole word, so
  # JODS-REC does not hit GJALLAR-RECOVERY or JODS-RECOVERY.
  scan =
    row:
    let
      allowed = lib.concatMapStrings (file: " -e ${lib.escapeShellArg "./${file}"}") (row.allowedIn or [ ]);
      filter = if allowed == "" then "cat" else "grep -v -x -F${allowed}";
      search =
        if row ? match then
          lib.concatMapStrings (pattern: " -e ${lib.escapeShellArg pattern}") row.match
        else
          " -w -e ${lib.escapeShellArg row.old}";
    in
    ''
      hits=$(grep -r -l -F${search} . | ${filter} | sort || true)
      if [ -n "$hits" ]; then
        printf '%s\n' ${lib.escapeShellArg "${row.old} was renamed to ${row.new} (${row.item}); still used in:"} $hits
        failed=1
      fi
    '';
in
pkgs.runCommand "gjallar-legacy-names-check" { } ''
  cd ${sources}
  failed=0
  ${lib.concatMapStrings scan rows}
  ${lib.concatMapStrings (row: ''
    printf '%s\n' ${lib.escapeShellArg "${row.old} (${row.item}): deleteAfter is ${row.deleteAfter} and this tree is release ${release}; remove the row and the last traces"}
    failed=1
  '') expired}
  [ "$failed" = 0 ]
  touch $out
''
