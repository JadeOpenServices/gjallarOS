# avahi-daemon dies at boot and never starts again (Plane 181).
#
# Linked from: system/services/printing.nix. Printing turns avahi on: the
# ipp-usb module enables it, and network printing needs it.
# Written 2026-10-14 against nixpkgs 5dfba623, avahi 0.8.
# Test: tests/nix/avahi-daemon-fix.nix (flake check avahi-daemon-fix).
#
# What goes wrong
#   1. On every DNS change openresolv runs its libc.d/avahi-daemon hook:
#      kill -HUP $(cat /var/run/avahi-daemon/pid). avahi writes that pid file
#      before it installs its SIGHUP handler (avahi-daemon/main.c:
#      daemon_pid_file_create() in main(), daemon_signal_init() later in
#      run_server()). A DNS change in that gap ends avahi with the default
#      SIGHUP action. At boot NetworkManager and Tailscale change DNS while
#      avahi starts, so this happens.
#   2. The dead daemon leaves its pid file. On the next start avahi checks
#      the file as root, but the nixpkgs unit drops CAP_DAC_OVERRIDE
#      (CapabilityBoundingSet), so root cannot unlink a file in the
#      avahi-owned /run/avahi-daemon. avahi exits with "Failed to create PID
#      file: File exists" and stays down until someone deletes the file.
#   3. While the stale file stays, every DNS change sends SIGHUP to whatever
#      process gets that pid next, and the failing hook makes Tailscale
#      report dns-set-os-config-failed.
#
# What this does
#   - ignore-sighup-until-handler.patch ignores SIGHUP from the moment avahi
#     resets its signals until daemon_signal_init() installs the real
#     handler. A reload asked for in that gap is dropped; avahi reads its
#     configuration right then anyway. Only the daemon this service runs is
#     rebuilt; other programs keep the cached libavahi.
#   - ExecStopPost deletes the pid file whenever the daemon stops, crashed or
#     not, so no stale pid gets the hook's SIGHUP. ExecStartPre deletes a file
#     left by a daemon that died before this fix was installed. Both run with
#     full privileges ("+"); rm on a planted link removes only the link.
#
# Upstream
#   avahi: main.c on master still creates the pid file before
#   daemon_signal_init(). nixpkgs: the avahi-daemon unit has no pid file
#   cleanup (same failure: olafkfreund/nixarchy#891, fixed there by PR #928).
#   Both reports wait in the human-session queue.
#
# Remove when
#   - the test's stock machine starts avahi over a stale pid file: drop
#     ExecStartPre and ExecStopPost.
#   - avahi is no longer 0.8 (the assertion below stops the build): check
#     whether main.c ignores or handles SIGHUP before the pid file exists.
#     If it does, drop the patch; if not, update the patch and the version.
{
  config,
  lib,
  pkgs,
  ...
}:

let
  avahi = pkgs.avahi;
  removePidFile = "+${pkgs.coreutils}/bin/rm -f /run/avahi-daemon/pid";
in
lib.mkIf config.services.avahi.enable {
  services.avahi.package =
    assert lib.assertMsg (avahi.version == "0.8")
      "system/fixes/avahi-daemon was written for avahi 0.8; check whether ${avahi.version} still writes its pid file before it handles SIGHUP, then update or remove the fix.";
    avahi.overrideAttrs (old: {
      patches = (old.patches or [ ]) ++ [ ./ignore-sighup-until-handler.patch ];
    });

  systemd.services.avahi-daemon.serviceConfig = {
    ExecStartPre = [ removePidFile ];
    ExecStopPost = [ removePidFile ];
  };
}
