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
  dispatchers = nixpkgs.lib.concatMapStringsSep " " (
    s: toString s.source
  ) config.networking.networkmanager.dispatcherScripts;
in
assert config.services.chrony.enable;
assert !config.services.timesyncd.enable;
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
  # No router, no plain pool, no servers from DHCP (loophole 25).
  ! grep -q '192\.168\.' "$conf"
  ! grep -q 'pool\.ntp\.org' "$conf"
  ! grep -qE '^(sourcedir|nocerttimecheck)' "$conf"
  ! grep -q 'dhcp' ${dispatchers}
  # chronyd itself accepts the file.
  ${pkgs.chrony}/bin/chronyd -p -f "$conf" >/dev/null
  echo "${chronyd}" | grep -q -- ' -s'
  touch "$out"
''
