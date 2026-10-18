{ pkgs, lib, settings, ... }:
let
  gjallarctl = pkgs.callPackage ../../../pkgs/gjallarctl { };
  checkInstaller = pkgs.writeShellScriptBin "check-installer" ''
    exec ${gjallarctl}/bin/gjallarctl check --repo ${lib.escapeShellArg settings.dotfilesDir} "$@"
  '';
  preflight = pkgs.writeShellScriptBin "gjallar-preflight" ''
    exec ${gjallarctl}/bin/gjallarctl preflight --repo ${lib.escapeShellArg settings.dotfilesDir} "$@"
  '';
  # Rebuild with the checkout's gjallarctl: an installed binary older than
  # the checkout rejects config keys it does not know yet ("unknown field
  # apps", 2026-10-08) and could never deploy its own successor.
  rebuild = pkgs.writeShellScriptBin "rebuild" ''
    repo=${lib.escapeShellArg settings.dotfilesDir}
    if [ -n "$repo" ] && [ -f "$repo/pkgs/gjallarctl/default.nix" ]; then
      echo "[GjallarOS] Building gjallarctl from $repo..." >&2
      errors="$(${pkgs.coreutils}/bin/mktemp)"
      if current="$(${pkgs.nix}/bin/nix-build --no-out-link -E \
        "(import ${pkgs.path} { }).callPackage (/. + \"$repo/pkgs/gjallarctl\") { }" 2>"$errors")"; then
        rm -f "$errors"
        exec "$current/bin/gjallarctl" rebuild "$@"
      fi
      echo "[GjallarOS] Building it failed; using the installed gjallarctl:" >&2
      ${pkgs.coreutils}/bin/tail -n 3 "$errors" >&2
      rm -f "$errors"
    fi
    exec ${gjallarctl}/bin/gjallarctl rebuild "$@"
  '';
  # The root side of `rebuild` for the system checkout (ROADMAP section 14).
  # The user's environment stays outside: the inner script starts from
  # env -i with fixed values and the Noctalia colors, which gjallarctl checks
  # again before the evaluation sees them. The installed gjallarctl checks the checkout's
  # ownership before anything in it is evaluated.
  systemRebuildInner = pkgs.writeShellScript "gjallar-system-rebuild-inner" ''
    set -eu
    if [ "$(${pkgs.coreutils}/bin/id -u)" != 0 ]; then
      echo "[GjallarOS] Error: gjallar-system-rebuild runs as root; use rebuild" >&2
      exit 2
    fi
    # The rebuild below prints the same source line, so only errors show.
    ${gjallarctl}/bin/gjallarctl system check >/dev/null
    ctl=${gjallarctl}/bin/gjallarctl
    errors="$(${pkgs.coreutils}/bin/mktemp)"
    if current="$(${pkgs.nix}/bin/nix-build --no-out-link -E \
      '(import ${pkgs.path} { }).callPackage /etc/gjallar/system/pkgs/gjallarctl { }' 2>"$errors")"; then
      ctl="$current/bin/gjallarctl"
    else
      echo "[GjallarOS] Building gjallarctl from the checkout failed; using the installed one:" >&2
      ${pkgs.coreutils}/bin/tail -n 3 "$errors" >&2
    fi
    ${pkgs.coreutils}/bin/rm -f "$errors"
    exec "$ctl" system rebuild "$@"
  '';
  systemRebuild = pkgs.writeShellScriptBin "gjallar-system-rebuild" ''
    exec ${pkgs.coreutils}/bin/env -i \
      PATH=/run/wrappers/bin:/run/current-system/sw/bin \
      HOME=/root \
      TERM="''${TERM:-dumb}" \
      LANG="''${LANG:-C.UTF-8}" \
      GJALLAR_NOCTALIA_COLORS="''${GJALLAR_NOCTALIA_COLORS:-}" \
      ${systemRebuildInner} "$@"
  '';
  goCommand = name: command: pkgs.writeShellScriptBin name ''
    exec ${gjallarctl}/bin/gjallarctl ${command} "$@"
  '';

in
{
  # One fixed place on every deployment that names the system's checkout;
  # gjallarctl falls back to it when no --repo is given.
  environment.etc."gjallar/repository" = lib.mkIf (settings.dotfilesDir != "") {
    text = settings.dotfilesDir + "\n";
  };

  environment.systemPackages = [
    (goCommand "helpme" "helpme")
    rebuild
    systemRebuild
    (goCommand "cleanup" "cleanup")
    (goCommand "cleanup-old-generations" "cleanup-old-generations")
    (goCommand "thermal-status" "thermal-status")
    (goCommand "thermal-test" "thermal-test")
    gjallarctl
    checkInstaller
    preflight
    pkgs.yad
  ];
}
