{
  lib,
  osConfig,
  pkgs,
  settings,
  ...
}:

let
  # Same test as system/hardware/fingerprint.nix: an "unsupported" reader counts
  # as none.
  fingerprint = lib.attrByPath [
    "oddc"
    "resolved"
    "hardware"
    "security"
    "fingerprint"
  ] { } osConfig;
  fingerprintPresent =
    fingerprint ? primary
    && lib.attrByPath [ "primary" "support" "linux" "status" ] null fingerprint != "unsupported";

  enrollment = pkgs.writeShellScript "gjallar-fingerprint-enroll" ''
    set -eu

    marker="$HOME/.local/state/gjallar/fingerprint-enrollment-seen"
    mkdir -p "$(dirname "$marker")"

    [ ! -e "$marker" ] || exit 0

    printf 'Set up a fingerprint to unlock the lock screen? [Y/n] '
    read -r answer

    case "$answer" in
      n|N|no|NO|No)
        touch "$marker"
        exit 0
        ;;
    esac

    if ${pkgs.fprintd}/bin/fprintd-enroll "$USER"; then
      touch "$marker"
      printf '\nFingerprint enrolled. Your password remains available.\n'
    else
      printf '\nEnrollment failed. This prompt will return next login.\n' >&2
    fi

    printf 'Press Enter to close.\n'
    read -r _
  '';

  launcher = pkgs.writeShellScript "gjallar-fingerprint-enroll-launcher" ''
    marker="$HOME/.local/state/gjallar/fingerprint-enrollment-seen"

    [ ! -e "$marker" ] || exit 0

    # ODDC may list a reader the machine lacks (a model profile in a VM, a
    # disabled reader); asking then only opens a dead-end window at login.
    if ! ${pkgs.fprintd}/bin/fprintd-list "$USER" >/dev/null 2>&1; then
      mkdir -p "$(dirname "$marker")"
      touch "$marker"
      exit 0
    fi

    # Ghostty takes --key=value only; a separate value is a config error.
    exec ${lib.getExe pkgs.ghostty} \
      --title='GjallarOS fingerprint setup' \
      -e ${enrollment}
  '';
in
lib.mkIf (fingerprintPresent && !settings.management.enable) {
  home.packages = [
    pkgs.fprintd
    pkgs.yad
  ];

  systemd.user.services.fingerprint-enrollment-prompt = {
    Unit = {
      Description = "Offer first-login fingerprint enrollment";
      After = [ "graphical-session.target" ];
    };

    Service = {
      Type = "exec";
      ExecStart = launcher;
    };

    Install.WantedBy = [ "graphical-session.target" ];
  };
}
