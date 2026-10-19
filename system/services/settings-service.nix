# The root settings service (ROADMAP section 14, step 3.3). It runs only
# where the system builds from the root-owned checkout, because only there
# does a user need root to change user.config.json.
{
  config,
  pkgs,
  lib,
  settings,
  ...
}:
let
  settingsd = pkgs.callPackage ../../pkgs/gjallar-settingsd { };
  busName = "org.gjallaros.Settings1";

  busPolicy = pkgs.writeTextDir "share/dbus-1/system.d/${busName}.conf" ''
    <!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-BUS Bus Configuration 1.0//EN"
     "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
    <busconfig>
      <policy user="root">
        <allow own="${busName}"/>
      </policy>
      <policy context="default">
        <allow send_destination="${busName}" send_interface="${busName}"/>
        <allow send_destination="${busName}" send_interface="org.freedesktop.DBus.Introspectable"/>
      </policy>
    </busconfig>
  '';
  busActivation = pkgs.writeTextDir "share/dbus-1/system-services/${busName}.service" ''
    [D-BUS Service]
    Name=${busName}
    Exec=/run/current-system/sw/bin/false
    User=root
    SystemdService=gjallar-settingsd.service
  '';

  # Every tier asks an administrator each time; the service passes the
  # old -> new text as the message. The installer tier has no action: the
  # service refuses those keys.
  action = tier: description: ''
    <action id="org.gjallaros.settings.${tier}">
      <description>${description}</description>
      <message>Authentication is required to change GjallarOS settings</message>
      <defaults>
        <allow_any>no</allow_any>
        <allow_inactive>no</allow_inactive>
        <allow_active>auth_admin</allow_active>
      </defaults>
    </action>
  '';
  polkitActions = pkgs.writeTextDir "share/polkit-1/actions/org.gjallaros.settings.policy" ''
    <?xml version="1.0" encoding="UTF-8"?>
    <!DOCTYPE policyconfig PUBLIC "-//freedesktop//DTD PolicyKit Policy Configuration 1.0//EN"
     "http://www.freedesktop.org/standards/PolicyKit/1/policyconfig.dtd">
    <policyconfig>
      <vendor>GjallarOS</vendor>
      ${action "everyday" "Change an everyday GjallarOS setting"}
      ${action "security" "Change a security GjallarOS setting"}
      ${action "critical" "Change a critical GjallarOS setting"}
    </policyconfig>
  '';
in
lib.mkIf (settings.dotfilesDir == "/etc/gjallar/system") {
  services.dbus.packages = [
    busPolicy
    busActivation
  ];
  environment.systemPackages = [ polkitActions ];

  systemd.services.gjallar-settingsd = {
    description = "GjallarOS settings service";
    # tailscale status lists the tailnet a new hostname must not clash with.
    path = lib.optional config.services.tailscale.enable config.services.tailscale.package;
    serviceConfig = {
      Type = "dbus";
      BusName = busName;
      ExecStart = lib.getExe settingsd;
      # Pending changes live in memory, so the service does not exit idle.
      Restart = "on-failure";
      RestartSec = "2s";
      UMask = "0077";
      RuntimeDirectory = "gjallar/settings";
      RuntimeDirectoryMode = "0700";
      # The rebuild unit reads its environment file after this service
      # may have stopped.
      RuntimeDirectoryPreserve = true;
      ReadWritePaths = [ "-/etc/gjallar/system" ];
      ProtectSystem = "strict";
      ProtectHome = true;
      PrivateTmp = true;
      PrivateDevices = true;
      PrivateNetwork = true;
      RestrictAddressFamilies = [ "AF_UNIX" ];
      CapabilityBoundingSet = "";
      NoNewPrivileges = true;
      ProtectKernelTunables = true;
      ProtectKernelModules = true;
      ProtectKernelLogs = true;
      ProtectControlGroups = true;
      ProtectClock = true;
      ProtectHostname = true;
      RestrictNamespaces = true;
      RestrictRealtime = true;
      RestrictSUIDSGID = true;
      LockPersonality = true;
      MemoryDenyWriteExecute = true;
      SystemCallArchitectures = "native";
      SystemCallFilter = [ "@system-service" "~@privileged" ];
    };
  };

  # The service asks this helper who answers for <name>.local before it
  # takes a new hostname. The answers come from the network, so they are
  # read by a throwaway user that can only send to the mDNS groups and
  # receive from local addresses, never by root.
  systemd.sockets.gjallar-namecheck = {
    description = "GjallarOS hostname check socket";
    wantedBy = [ "sockets.target" ];
    socketConfig = {
      ListenStream = "/run/gjallar/namecheck.sock";
      SocketUser = "root";
      SocketMode = "0600";
      Accept = true;
      MaxConnections = 4;
    };
  };

  systemd.services."gjallar-namecheck@" = {
    description = "GjallarOS hostname check";
    serviceConfig = {
      ExecStart = "${settingsd}/bin/gjallar-namecheck";
      StandardInput = "socket";
      StandardOutput = "socket";
      StandardError = "journal";
      RuntimeMaxSec = 15;
      DynamicUser = true;
      UMask = "0077";
      ProtectSystem = "strict";
      ProtectHome = true;
      PrivateTmp = true;
      PrivateDevices = true;
      PrivateIPC = true;
      PrivateUsers = true;
      RestrictAddressFamilies = [
        "AF_INET"
        "AF_INET6"
        "AF_NETLINK"
      ];
      IPAddressDeny = "any";
      IPAddressAllow = [
        "multicast"
        "link-local"
        "10.0.0.0/8"
        "172.16.0.0/12"
        "192.168.0.0/16"
        "fc00::/7"
      ];
      CapabilityBoundingSet = "";
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
      MemoryDenyWriteExecute = true;
      SystemCallArchitectures = "native";
      SystemCallFilter = [
        "@system-service"
        "~@privileged"
        "~@resources"
      ];
    };
  };

  # Started by the service after it wrote a change. A switch must not stop
  # the rebuild that runs it, as with nixos-upgrade.
  systemd.services.gjallar-settings-rebuild = {
    description = "GjallarOS rebuild after a settings change";
    restartIfChanged = false;
    stopIfChanged = false;
    unitConfig.X-StopOnRemoval = false;
    serviceConfig = {
      Type = "oneshot";
      EnvironmentFile = "-/run/gjallar/settings/rebuild.env";
      ExecStart = "/run/current-system/sw/bin/gjallar-system-rebuild --no-cleanup";
    };
  };
}
