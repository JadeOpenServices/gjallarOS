{
  nixpkgs,
  system,
  snapshotsModule,
}:
let
  evaluate =
    homeOptions: extraSettings:
    (nixpkgs.lib.nixosSystem {
      inherit system;
      modules = [
        snapshotsModule
        {
          system.stateVersion = "26.05";
          fileSystems."/" = {
            device = "/dev/null";
            fsType = "btrfs";
          };
          fileSystems."/home" = {
            device = "/dev/null";
            fsType = "btrfs";
            options = homeOptions;
          };
          boot.loader.grub.enable = false;
        }
      ];
      specialArgs.settings = extraSettings;
    }).config;
  layout = evaluate [ "subvol=@home" ] { };
  old = evaluate [ "defaults" ] { };
  off = evaluate [ "subvol=@home" ] { snapshotsEnable = false; };
  noHourly = evaluate [ "subvol=@home" ] { snapshotsKeepHours = 0; };
  home = layout.systemd.services.gjallar-snapshot-home;
  homeConf = layout.environment.etc."gjallar/snapshots/home.conf".source;
  btrbk = nixpkgs.legacyPackages.${system}.btrbk;
  configConf = layout.environment.etc."gjallar/snapshots/config.conf".source;
  restore = layout.systemd.services.gjallar-snapshotd.serviceConfig;
  polkitPolicy = nixpkgs.lib.findFirst (
    p: p.name or "" == "org.gjallaros.snapshots.policy"
  ) (throw "no snapshots polkit policy") layout.environment.systemPackages;
  dbusFiles = nixpkgs.lib.concatMapStringsSep " " toString layout.services.dbus.packages;
in
# Only the subvolume layout gets snapshot units, and the setting turns them off.
assert layout.systemd.services ? gjallar-snapshot-config;
assert layout.systemd.timers ? gjallar-snapshot-home;
assert !(old.systemd.services ? gjallar-snapshot-home);
assert !(old.environment.etc ? "gjallar/snapshots/config.conf");
assert !(off.systemd.services ? gjallar-snapshot-home);
# @snapshots stays out of fstab.
assert !(layout.fileSystems ? "/.snapshots");
# The units mount @snapshots only in their own namespace, without sudo.
assert home.serviceConfig.PrivateMounts;
assert home.serviceConfig.ProtectSystem == "strict";
assert home.serviceConfig.PrivateNetwork;
# The restore service mounts @snapshots read-only in its own namespace and
# holds only the mount and read capabilities.
assert !(old.systemd.services ? gjallar-snapshotd);
assert restore.PrivateMounts;
assert restore.ProtectHome;
assert restore.PrivateNetwork;
assert
  restore.CapabilityBoundingSet == [
    "CAP_SYS_ADMIN"
    "CAP_DAC_READ_SEARCH"
  ];
assert home.serviceConfig.CapabilityBoundingSet == [ "CAP_SYS_ADMIN" ];
# Restore opens files with openat2; without it every restore fails.
assert nixpkgs.lib.elem "openat2" restore.SystemCallFilter;
# Not the nixpkgs btrbk module: it lets its user run any btrfs command as root.
assert layout.services.btrbk.instances == { };
nixpkgs.legacyPackages.${system}.runCommand "gjallar-snapshots-check" { } ''
  grep -qx 'snapshot_preserve 24h 7d' ${homeConf}
  grep -qx 'snapshot_preserve_min latest' ${homeConf}
  grep -qx 'snapshot_preserve 7d' ${configConf}
  grep -qx 'snapshot_preserve 7d' ${noHourly.environment.etc."gjallar/snapshots/home.conf".source}
  grep -q 'subvol=@snapshots,nosuid,nodev,noexec' ${home.serviceConfig.ExecStart}
  grep -q 'subvol=@snapshots,nosuid,nodev,noexec' ${layout.systemd.services.gjallar-snapshot-config.serviceConfig.ExecStart}
  # Each restore asks the caller's own password.
  grep -q '<allow_active>auth_self</allow_active>' ${polkitPolicy}/share/polkit-1/actions/org.gjallaros.snapshots.policy
  grep -rq 'SystemdService=gjallar-snapshotd.service' ${dbusFiles}
  # The service binary is built and refuses to start without its arguments.
  set -- ${restore.ExecStart}
  if "$1" 2>"$TMPDIR/usage"; then exit 1; fi
  grep -q 'DEVICE MOUNTPOINT' "$TMPDIR/usage"
  # btrbk itself accepts both files.
  ${btrbk}/bin/btrbk --lockfile="$TMPDIR/lock" -c ${homeConf} config print >/dev/null
  ${btrbk}/bin/btrbk --lockfile="$TMPDIR/lock" -c ${configConf} config print >/dev/null
  touch "$out"
''
