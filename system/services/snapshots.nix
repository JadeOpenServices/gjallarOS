{
  config,
  lib,
  pkgs,
  settings,
  ...
}:
let
  # Only installs made with the subvolume layout have @home, @config and
  # @snapshots; nixos-generate-config writes the subvol= option into
  # hardware.nix. Older installs get no snapshots until they reinstall.
  layout = lib.elem "subvol=@home" (config.fileSystems."/home".options or [ ]);
  enable = layout && (settings.snapshotsEnable or true);
  keepHours = settings.snapshotsKeepHours or 24;
  keepDays = settings.snapshotsKeepDays or 7;

  # Each kind snapshots one subvolume into its own folder of @snapshots.
  # / and /var/lib are never snapshotted: generations cover the system,
  # and the keys in /var/lib must not live on in old copies.
  kinds = {
    home = {
      mountPoint = "/home";
      subvolume = "@home";
      preserve = "snapshot_preserve_min latest\nsnapshot_preserve ${
        lib.optionalString (keepHours > 0) "${toString keepHours}h "
      }${toString keepDays}d";
    };
    config = {
      mountPoint = "/etc/gjallar/system";
      subvolume = "@config";
      # Rebuilds add snapshots between the daily ones; keep all of the
      # last day so a bad change can be undone the same day.
      preserve = "snapshot_preserve_min 1d\nsnapshot_preserve ${toString keepDays}d";
    };
  };

  runDir = name: "/run/gjallar-snapshot-${name}";

  # btrbk without a volume section takes absolute paths. long-iso
  # timestamps carry the offset, so the DST hour cannot repeat a name.
  btrbkConf =
    name: kind:
    pkgs.writeText "gjallar-snapshot-${name}.conf" ''
      timestamp_format long-iso
      snapshot_create onchange
      lockfile ${runDir name}/btrbk.lock
      ${kind.preserve}

      subvolume ${kind.mountPoint}
        snapshot_dir ${runDir name}/snapshots/${name}
        snapshot_name ${name}
    '';

  GiB = 1024 * 1024 * 1024;

  snapshotScript =
    name: kind:
    pkgs.writeShellApplication {
      name = "gjallar-snapshot-${name}";
      runtimeInputs = [
        pkgs.btrbk
        pkgs.btrfs-progs
        pkgs.coreutils
        pkgs.util-linux
      ];
      text = ''
        if [ "$(findmnt -nro FSROOT --mountpoint ${kind.mountPoint})" != /${kind.subvolume} ]; then
          echo "${kind.mountPoint} is not the ${kind.subvolume} subvolume; no snapshot."
          exit 0
        fi
        device="$(findmnt -nvro SOURCE --mountpoint ${kind.mountPoint})"
        # @snapshots is mounted only here, in this unit's own mount
        # namespace, so no one else sees the old copies.
        mkdir -p ${runDir name}/snapshots
        mount -o subvol=@snapshots,nosuid,nodev,noexec "$device" ${runDir name}/snapshots
        mkdir -p ${runDir name}/snapshots/${name}
        chmod 0700 ${runDir name}/snapshots/${name}

        read -r size avail < <(df -B1 --output=size,avail ${runDir name}/snapshots | tail -n 1)
        floor=$((size / 10))
        if [ "$floor" -gt ${toString (10 * GiB)} ]; then
          floor=${toString (10 * GiB)}
        fi
        if [ "$avail" -lt "$floor" ]; then
          echo "Disk almost full ($((avail / 1048576)) MiB free); no new ${name} snapshot. Old ones still age out."
          exec btrbk -c ${btrbkConf name kind} prune
        fi
        exec btrbk -c ${btrbkConf name kind} snapshot
      '';
    };

  service = name: kind: {
    description = "Snapshot of ${kind.mountPoint}";
    after = [ "local-fs.target" ];
    unitConfig.RequiresMountsFor = [ kind.mountPoint ];
    serviceConfig = {
      Type = "oneshot";
      ExecStart = lib.getExe (snapshotScript name kind);
      RuntimeDirectory = "gjallar-snapshot-${name}";
      RuntimeDirectoryMode = "0700";
      UMask = "0077";
      Nice = 19;
      IOSchedulingClass = "idle";

      PrivateMounts = true;
      ProtectSystem = "strict";
      ProtectHome = "read-only";
      PrivateTmp = true;
      PrivateNetwork = true;
      RestrictAddressFamilies = [ "AF_UNIX" ];
      NoNewPrivileges = true;
      RestrictSUIDSGID = true;
      RestrictNamespaces = true;
      RestrictRealtime = true;
      LockPersonality = true;
      MemoryDenyWriteExecute = true;
      ProtectClock = true;
      ProtectHostname = true;
      ProtectKernelLogs = true;
      ProtectKernelModules = true;
      ProtectKernelTunables = true;
      ProtectControlGroups = true;
      SystemCallArchitectures = "native";
      SystemCallFilter = [
        "@system-service"
        "@mount"
      ];
      CapabilityBoundingSet = [
        "CAP_SYS_ADMIN"
        "CAP_FOWNER"
        "CAP_DAC_OVERRIDE"
      ];
    };
  };
in
{
  config = lib.mkIf enable {
    # The rebuild command takes a settings snapshot before and after a
    # rebuild while this file exists.
    environment.etc = lib.mapAttrs' (
      name: kind: lib.nameValuePair "gjallar/snapshots/${name}.conf" { source = btrbkConf name kind; }
    ) kinds;

    systemd.services = lib.mapAttrs' (
      name: kind: lib.nameValuePair "gjallar-snapshot-${name}" (service name kind)
    ) kinds;

    systemd.timers = {
      gjallar-snapshot-home = {
        wantedBy = [ "timers.target" ];
        timerConfig = {
          OnCalendar = "hourly";
          Persistent = true;
          RandomizedDelaySec = "5min";
        };
      };
      gjallar-snapshot-config = {
        wantedBy = [ "timers.target" ];
        timerConfig = {
          OnCalendar = "daily";
          Persistent = true;
          RandomizedDelaySec = "1h";
        };
      };
    };
  };
}
