{ lib, ... }:

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
    '';
  };
}
