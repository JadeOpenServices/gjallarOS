{
  lib,
  writeShellScriptBin,
  python3,
}:

# Adds a folder to the Places panel or takes it out; used by Nextcloud and
# gjallarctl wine.
writeShellScriptBin "dolphin-place" ''
  exec ${lib.getExe python3} -I ${./dolphin-place.py} "$@"
''
