{
  lib,
  writeShellScriptBin,
  python3,
}:

# Adds a folder to the Places panel or takes it out; used by Nextcloud and
# gjallar-wine.
writeShellScriptBin "dolphin-place" ''
  exec ${lib.getExe python3} -I ${./dolphin-place.py} "$@"
''
