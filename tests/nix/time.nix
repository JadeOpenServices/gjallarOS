{
  nixpkgs,
  system,
  timeModule,
}:
let
  config =
    (nixpkgs.lib.nixosSystem {
      inherit system;
      modules = [
        timeModule
        {
          system.stateVersion = "26.05";
          fileSystems."/" = {
            device = "/dev/null";
            fsType = "btrfs";
          };
          boot.loader.grub.enable = false;
          networking.networkmanager.enable = true;
        }
      ];
    }).config;
  pkgs = nixpkgs.legacyPackages.${system};
  chronyd = config.systemd.services.chronyd.serviceConfig.ExecStart;
  check = config.systemd.services.gjallar-time-check.serviceConfig.ExecStart;
  has = nixpkgs.lib.hasInfix;
  dispatchers = nixpkgs.lib.concatMapStringsSep " " (
    s: toString s.source
  ) config.networking.networkmanager.dispatcherScripts;
in
assert config.services.chrony.enable;
assert !config.services.timesyncd.enable;
# The first-update exception asks the same four operators and trusts only
# their roots.
assert has " system time-check --wait 90s " check;
assert has "--server time.cloudflare.com" check;
assert has "--server ntspool.time.nl" check;
assert has "--root 'ISRG Root X1'" check;
assert config.systemd.timers.gjallar-time-check.timerConfig.OnUnitInactiveSec == "30min";
pkgs.runCommand "gjallar-time-check" { } ''
  set -- ${chronyd}
  conf=
  while [ $# -gt 0 ]; do
    if [ "$1" = -f ]; then conf=$2; fi
    shift
  done
  [ -n "$conf" ]
  # Four operators, each over NTS, one source each.
  [ "$(grep -cE '^(server|pool) .* nts( |$)' "$conf")" = 4 ]
  [ "$(grep -cE '^(server|pool) ' "$conf")" = 4 ]
  grep -q '^pool ntspool.time.nl .*maxsources 1' "$conf"
  # Nothing unauthenticated is ever selected, and one operator alone does
  # not move the clock.
  grep -qx 'authselectmode require' "$conf"
  grep -qx 'minsources 2' "$conf"
  grep -q '^ntsdumpdir ' "$conf"
  # The exception is only ever a file in /run, never in the config.
  grep -qx 'confdir /run/gjallar-time/chrony' "$conf"
  # No router, no plain pool, no servers from DHCP (loophole 25).
  ! grep -q '192\.168\.' "$conf"
  ! grep -q 'pool\.ntp\.org' "$conf"
  ! grep -qE '^(sourcedir|nocerttimecheck|nosystemcert|ntstrustedcerts)' "$conf"
  ! grep -q 'dhcp' ${dispatchers}
  # chronyd itself accepts the file, with the confdir missing as here.
  [ ! -e /run/gjallar-time ]
  ${pkgs.chrony}/bin/chronyd -p -f "$conf" >/dev/null
  echo "${chronyd}" | grep -q -- ' -s'
  touch "$out"
''
