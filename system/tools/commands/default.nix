{ pkgs, lib, settings, ... }:
let
  gjallarctl = pkgs.callPackage ../../../pkgs/gjallarctl { };
  gjallarSettings = pkgs.callPackage ../../../pkgs/gjallar-settings { inherit gjallarctl; };
  checkInstaller = pkgs.writeShellScriptBin "check-installer" ''
    exec ${gjallarctl}/bin/gjallarctl check --repo ${lib.escapeShellArg settings.dotfilesDir} "$@"
  '';
  preflight = pkgs.writeShellScriptBin "gjallar-preflight" ''
    exec ${gjallarctl}/bin/gjallarctl preflight --repo ${lib.escapeShellArg settings.dotfilesDir} "$@"
  '';
  # The root side of `gjallarctl rebuild` for the system checkout (ROADMAP section 14).
  # The user's environment stays outside: the inner script starts from
  # env -i with fixed values and the Noctalia colors, which gjallarctl checks
  # again before the evaluation sees them. The installed gjallarctl checks the checkout's
  # ownership before anything in it is evaluated.
  systemRebuildInner = pkgs.writeShellScript "gjallar-system-rebuild-inner" ''
    set -eu
    if [ "$(${pkgs.coreutils}/bin/id -u)" != 0 ]; then
      echo "[GjallarOS] Error: gjallar-system-rebuild runs as root; use gjallarctl rebuild" >&2
      exit 2
    fi
    # The rebuild below prints the same source line, so only errors show.
    ${gjallarctl}/bin/gjallarctl system check >/dev/null
    ctl=${gjallarctl}/bin/gjallarctl
    errors="$(${pkgs.coreutils}/bin/mktemp)"
    # Its build output goes to $errors, so without this line a changed
    # gjallarctl leaves the terminal silent for a minute.
    echo "[GjallarOS] Building gjallarctl from the checkout..." >&2
    if current="$(${pkgs.nix}/bin/nix-build --no-out-link -E \
      '(import ${pkgs.path} { }).callPackage /etc/gjallar/system/pkgs/gjallarctl { }' 2>"$errors")"; then
      ctl="$current/bin/gjallarctl"
    else
      echo "[GjallarOS] Building gjallarctl from the checkout failed; using the installed one:" >&2
      ${pkgs.coreutils}/bin/tail -n 3 "$errors" >&2
    fi
    ${pkgs.coreutils}/bin/rm -f "$errors"
    # On the subvolume layout the system settings get a snapshot before the
    # rebuild and after a good one, so the last built state can be restored.
    # The rebuild may turn snapshots off, so the check runs each time.
    snapshot_settings() {
      if [ -e /etc/gjallar/snapshots/config.conf ] &&
        ! ${pkgs.systemd}/bin/systemctl start gjallar-snapshot-config.service; then
        echo "[GjallarOS] Warning: no snapshot of the system settings; see journalctl -u gjallar-snapshot-config" >&2
      fi
    }
    snapshot_settings
    status=0
    "$ctl" system rebuild "$@" || status=$?
    if [ "$status" = 0 ]; then
      snapshot_settings
    fi
    exit "$status"
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
    systemRebuild
    (goCommand "cleanup" "cleanup")
    (goCommand "cleanup-old-generations" "cleanup-old-generations")
    (goCommand "thermal-status" "thermal-status")
    (goCommand "thermal-test" "thermal-test")
    gjallarctl
    gjallarSettings
    checkInstaller
    preflight
    pkgs.yad
  ];
}
