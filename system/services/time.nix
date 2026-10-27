{ lib, pkgs, ... }:

# Time comes only from NTS servers, so nobody on the path can forge it
# (ROADMAP section 18, questions 30-33). Plain NTP, the NixOS pool and
# servers from DHCP are never used: before this, every install preferred
# whatever answered at 192.168.8.1 on any network (loophole 25).
let
  # One source per operator: with minsources 2 a single operator, wrong or
  # hostile, cannot set the clock alone. A server name containing "pool"
  # would become a pool directive in the NixOS module, so the sources are
  # written here instead of in services.chrony.servers.
  sources = [
    "server time.cloudflare.com iburst nts"
    "server nts.netnod.se iburst nts"
    "server ptbtime1.ptb.de iburst nts"
    "pool ntspool.time.nl iburst nts maxsources 1"
  ];
  hosts = map (s: builtins.elemAt (lib.splitString " " s) 1) sources;
  # The roots the four operators' chains end in (checked 2026-10-21). The
  # first-update exception trusts only these; names missing from the
  # system bundle are skipped.
  roots = [
    "ISRG Root X1"
    "ISRG Root X2"
    "Root YE"
    "Root YR"
    "SSL.com Root Certification Authority ECC"
  ];
  gjallarctlPackage = pkgs.callPackage ../../pkgs/gjallarctl { };
in
{
  services.chrony = {
    enable = true;
    servers = [ ];
    # Also sets ntsdumpdir: saved cookies spare the TLS key exchange after a
    # restart, so a clock that starts wrong does not need a certificate.
    enableNTS = true;
    # Start from the time chronyd last stopped when that is later than the
    # RTC: a dead RTC battery then starts near the right time.
    extraFlags = [ "-s" ];
    extraConfig = ''
      ${lib.concatStringsSep "\n" sources}
      authselectmode require
      minsources 2
      confdir /run/gjallar-time/chrony
    '';
  };

  # The confdir is empty except while gjallar-time-check holds the one-time
  # exception for a clock behind every certificate (step 2) or the home
  # router fallback (step 3).
  systemd.tmpfiles.rules = [
    "d /run/gjallar-time 0755 root root -"
    "d /run/gjallar-time/chrony 0755 root root -"
  ];

  systemd.services.gjallar-time-check = {
    description = "Confirm the clock over NTS";
    wantedBy = [ "multi-user.target" ];
    after = [
      "chronyd.service"
      "network-online.target"
    ];
    wants = [ "network-online.target" ];
    # ip, ping and nmcli tell which network the device is on, for the home
    # router fallback (step 3).
    path = [
      pkgs.chrony
      pkgs.iproute2
      pkgs.iputils
      pkgs.networkmanager
      pkgs.systemd
    ];
    serviceConfig = {
      Type = "oneshot";
      TimeoutStartSec = "10min";
      ExecStart = "${gjallarctlPackage}/bin/gjallarctl system time-check --wait 90s ${
        lib.escapeShellArgs (
          lib.concatMap (h: [
            "--server"
            h
          ]) hosts
          ++ lib.concatMap (r: [
            "--root"
            r
          ]) roots
        )
      }";
      StateDirectory = "gjallar-time";
      ProtectSystem = "strict";
      ReadWritePaths = [
        "/run/gjallar-time"
        "/var/lib/chrony"
      ];
      ProtectHome = true;
      PrivateTmp = true;
      NoNewPrivileges = true;
    };
  };

  # Reruns keep the last confirmed time fresh and retry when the boot run
  # found no network.
  systemd.timers.gjallar-time-check = {
    wantedBy = [ "timers.target" ];
    timerConfig = {
      OnBootSec = "30min";
      OnUnitInactiveSec = "30min";
    };
  };
}
