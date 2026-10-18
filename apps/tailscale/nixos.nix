{
  config,
  lib,
  pkgs,
  settings,
  ...
}:

let
  # null: generated state from before the installer asked about Tailscale.
  # Keep 192.168.8.0/24 local everywhere, as before.
  intent =
    if (settings.tailscale or null) == null then
      {
        homeSubnets = [ "192.168.8.0/24" ];
      }
    else
      settings.tailscale;

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
      config.services.tailscale.package
    ];
    # The dispatcher, tailscaled and its state file can fire in bursts.
    unitConfig.StartLimitIntervalSec = 0;
    serviceConfig = {
      Type = "oneshot";
      ExecStart = "${gjallarctl}/bin/gjallarctl vpn apply --policy ${policyFile} --override /var/lib/gjallar/vpn-trust-override.json";
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
}
