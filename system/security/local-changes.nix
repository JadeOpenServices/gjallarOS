# Developer mode: the owner's own Nix files. A system rebuild stages root's
# copy (/etc/gjallar/local, checked by internal/localchanges) at
# .gjallar-local next to the official source and records its hash. Files
# below system/ are NixOS modules, files below home/ home-manager modules of
# the owner; a name starting with _ is skipped. A staged folder that does
# not match the recorded hash, or one gjallarctl did not stage, stops the
# build. localDir is the staged folder's place in the source.
{ localDir }:
{
  lib,
  settings,
  buildSource,
  ...
}:
let
  local = buildSource.local or null;
  dir = localDir;
  present = builtins.pathExists dir;
  at = rel: if rel == "" then dir else dir + "/${rel}";

  listFiles =
    sub:
    lib.concatLists (
      lib.mapAttrsToList (
        name: type:
        let
          rel = if sub == "" then name else "${sub}/${name}";
        in
        if type == "directory" then
          listFiles rel
        else if type == "regular" then
          [ rel ]
        else
          throw "GjallarOS: ${rel} in the local folder is not a plain file"
      ) (builtins.readDir (at sub))
    );

  files = if present then builtins.sort builtins.lessThan (listFiles "") else [ ];
  manifest = lib.concatMapStrings (rel: "${builtins.hashFile "sha256" (at rel)}  ${rel}\n") files;
  hash = builtins.hashString "sha256" manifest;

  modulesBelow =
    top:
    map at (
      builtins.filter (
        rel:
        lib.hasPrefix "${top}/" rel
        && lib.hasSuffix ".nix" rel
        && !(lib.any (lib.hasPrefix "_") (lib.splitString "/" rel))
      ) files
    );

  checked =
    if present && local == null then
      throw "GjallarOS: .gjallar-local is in the source, but gjallarctl did not stage it; build with rebuild"
    else if !present && local != null then
      throw "GjallarOS: the build context names local changes, but .gjallar-local is missing"
    else if local != null && (local.hash or "") != hash then
      throw "GjallarOS: .gjallar-local does not match the hash gjallarctl recorded"
    else
      true;
in
assert checked;
{
  imports = modulesBelow "system";

  home-manager.users.${settings.username}.imports = modulesBelow "home";

  # The boot entry and nixos-version say the generation holds local changes.
  system.nixos.tags = lib.optional (local != null) "local-changes";
}
