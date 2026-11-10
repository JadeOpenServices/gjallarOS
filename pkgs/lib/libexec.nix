# Puts a script's main program into libexec instead of bin, so it is in the
# profile but not on PATH. gjallarctl starts these helpers from
# /run/current-system/sw/libexec, which only root writes, and is the only
# command users type (Plane 282).
{ lib, pkgs }:
drv:
let
  name = drv.meta.mainProgram;
in
pkgs.runCommand "${name}-libexec" { meta.mainProgram = name; } ''
  install -d "$out/libexec"
  ln -s ${lib.getExe drv} "$out/libexec/${name}"
''
