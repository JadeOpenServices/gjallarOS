{
  config,
  lib,
  pkgs,
  settings,
  ...
}:

let
  # null: generated state from before the installer asked about Tailscale.
  # No subnet counts as home then.
  intent = if (settings.tailscale or null) == null then { } else settings.tailscale;

  policy = {
    homeSubnets = intent.homeSubnets or [ ];
    trustedWifis = intent.trustedWifis or [ ];
    exitNode = intent.exitNode or "";
    wifiExitNodes = intent.wifiExitNodes or { };
    routerHome = intent.routerHome or false;
    siteRouterTrust = intent.siteRouterTrust or false;
    siteRouterTargets = intent.siteRouterTargets or [ ];
  };

  policyFile = builtins.toFile "gjallar-vpn-trust.json" (builtins.toJSON policy);
  gjallarctl = pkgs.callPackage ../../pkgs/gjallarctl { };

  # restart, not start: a decision still probing the previous network is
  # stale and the new one must not wait behind it.
  reapply = "${config.systemd.package}/bin/systemctl restart --no-block gjallar-vpn-trust.service";
in
lib.mkIf config.gjallar.apps.tailscale.enable {
  services.tailscale.enable = true;
  # Using an exit node needs loose reverse-path filtering, and
  # gjallarctl vpn exit-node can turn one on at runtime.
  services.tailscale.useRoutingFeatures = "client";

  # Decide whether this network is trusted, then keep the home LAN bypass
  # and the exit node in line with it. See internal/nettrust.
  systemd.services.gjallar-vpn-trust = {
    description = "Switch the Tailscale VPN by network trust";
    after = [
      "tailscaled.service"
      "NetworkManager.service"
    ];
    wants = [ "tailscaled.service" ];
    path = [
      pkgs.iproute2
      pkgs.iputils
      pkgs.networkmanager
      pkgs.nftables
      config.services.tailscale.package
      config.systemd.package
    ];
    # The dispatcher, tailscaled and its state file can fire in bursts.
    unitConfig.StartLimitIntervalSec = 0;
    serviceConfig = {
      Type = "oneshot";
      ExecStart = "${gjallarctl}/bin/gjallarctl vpn apply --policy ${policyFile} --override /var/lib/gjallar/vpn-trust-override.json";
    };
  };

  # gjallarctl vpn pause: the settings service writes the pause here, and
  # may write nothing else in /run.
  systemd.tmpfiles.rules = [ "d /run/gjallar/vpn-pause 0755 root root -" ];

  # A pause ends before the device sleeps; the block is back before it does.
  systemd.services.gjallar-vpn-pause-sleep = {
    description = "End the VPN pause before sleep";
    wantedBy = [ "sleep.target" ];
    before = [ "sleep.target" ];
    path = [ config.systemd.package ];
    serviceConfig = {
      Type = "oneshot";
      ExecStart = "${gjallarctl}/bin/gjallarctl vpn resume";
      TimeoutStartSec = "30s";
    };
  };

  # The built-in policy, for gjallarctl vpn trust-wifi/exit-node/export.
  environment.etc."gjallar/vpn-trust.json".source = policyFile;

  networking.networkmanager.dispatcherScripts = [
    {
      source = pkgs.writeShellScript "gjallar-vpn-trust-dispatcher" ''
        case "$2" in
          up|down|dhcp4-change|dhcp6-change|connectivity-change|reapply)
            ${reapply}
            ;;
        esac
      '';
      type = "basic";
    }
  ];

  systemd.services.tailscaled.postStart = reapply;

  # Login, logout and tailnet route changes land in the state file.
  systemd.paths.gjallar-vpn-trust = {
    description = "Watch Tailscale state for network trust changes";
    wantedBy = [ "multi-user.target" ];
    pathConfig = {
      PathChanged = "/var/lib/tailscale/tailscaled.state";
      Unit = "gjallar-vpn-trust.service";
    };
  };

  # Routers come online and go offline without a local network event.
  systemd.timers.gjallar-vpn-trust = {
    wantedBy = [ "timers.target" ];
    timerConfig = {
      OnBootSec = "1min";
      OnUnitActiveSec = "5min";
    };
  };

  # A new verdict can start or end the home router time fallback
  # (system/services/time.nix). vpn apply rewrites the file only on change.
  systemd.paths.gjallar-time-check = {
    description = "Watch the network verdict for the time fallback";
    wantedBy = [ "multi-user.target" ];
    pathConfig = {
      PathChanged = "/run/gjallar/vpn-trust.json";
      Unit = "gjallar-time-check.service";
    };
  };
}
