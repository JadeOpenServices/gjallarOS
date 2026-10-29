{ settings, lib, pkgs, ... }:

{
  # gjallarHorn virus scan (Plane 216): signatures only, scans on demand
  # with `gjallarctl security scan`. No clamd daemon and no on-access
  # scanning; the scan runs as the user in a sandbox without network.
  # Off for people who bring a scanner of their own.
  config = lib.mkIf (settings.virusScanEnable or true) {
    environment.systemPackages = [ pkgs.clamav ];

    services.clamav.updater.enable = true;

    # Spread the hourly signature checks so devices do not all ask the
    # mirror at the full hour.
    systemd.timers.clamav-freshclam.timerConfig.RandomizedDelaySec = "20m";

    # Upstream only sets PrivateTmp and PrivateDevices. freshclam parses
    # downloaded files, so it gets the same walls as our other services.
    systemd.services.clamav-freshclam.serviceConfig = {
      ProtectSystem = "strict";
      ProtectHome = true;
      NoNewPrivileges = true;
      ProtectKernelTunables = true;
      ProtectKernelModules = true;
      ProtectKernelLogs = true;
      ProtectControlGroups = true;
      ProtectClock = true;
      ProtectHostname = true;
      ProtectProc = "invisible";
      ProcSubset = "pid";
      RestrictNamespaces = true;
      RestrictRealtime = true;
      RestrictSUIDSGID = true;
      LockPersonality = true;
      RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" "AF_NETLINK" ];
      SystemCallArchitectures = "native";
      SystemCallFilter = [ "@system-service" ];
      CapabilityBoundingSet = "";
      UMask = "0022";
    };
  };
}
