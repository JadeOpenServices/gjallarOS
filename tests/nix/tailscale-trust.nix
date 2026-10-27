{
  nixpkgs,
  system,
  tailscaleModules,
}:
let
  pkgs = nixpkgs.legacyPackages.${system};
  evaluate =
    settings:
    (nixpkgs.lib.nixosSystem {
      inherit system;
      modules = tailscaleModules ++ [
        {
          system.stateVersion = "26.05";
          boot.loader.grub.enable = false;
          networking.networkmanager.enable = true;
          fileSystems."/" = {
            device = "/dev/null";
            fsType = "ext4";
          };
        }
      ];
      specialArgs = { inherit settings; };
    }).config;
  policyOf =
    cfg:
    let
      exec = cfg.systemd.services.gjallar-vpn-trust.serviceConfig.ExecStart;
      file = cfg.environment.etc."gjallar/vpn-trust.json".source;
    in
    # The CLI's copy must be the policy the service applies.
    assert nixpkgs.lib.hasInfix "--policy ${builtins.unsafeDiscardStringContext file} " (
      builtins.unsafeDiscardStringContext exec
    );
    builtins.fromJSON (builtins.readFile file);

  # Generated state from before the installer asked: the old live behaviour.
  legacy = evaluate { apps = [ "tailscale" ]; };
  legacyNull = evaluate {
    apps = [ "tailscale" ];
    tailscale = null;
  };
  vpn = evaluate {
    apps = [ "tailscale" ];
    tailscale = {
      homeSubnets = [ "192.168.8.0/24" ];
      trustedWifis = [
        "synthetic-5Ghz"
        "synthetic-guest"
      ];
      exitNode = "home-router";
      wifiExitNodes.synthetic-guest = "auto";
      siteRouterTrust = true;
      siteRouterTargets = [ "192.168.8.1:53" ];
    };
  };
  routerHome = evaluate {
    apps = [ "tailscale" ];
    tailscale = {
      exitNode = "exit-1";
      routerHome = true;
    };
  };
  off = evaluate {
    apps = [ ];
    tailscale = {
      homeSubnets = [ ];
      trustedWifis = [ ];
      exitNode = "";
      siteRouterTrust = false;
      siteRouterTargets = [ ];
    };
  };
in
assert legacy.services.tailscale.enable;
assert
  policyOf legacy == {
    homeSubnets = [ ];
    trustedWifis = [ ];
    exitNode = "";
    wifiExitNodes = { };
    routerHome = false;
    siteRouterTrust = false;
    siteRouterTargets = [ ];
  };
assert policyOf legacyNull == policyOf legacy;
assert legacy.services.tailscale.useRoutingFeatures == "client";
assert nixpkgs.lib.hasInfix "--override /var/lib/gjallar/vpn-trust-override.json"
  vpn.systemd.services.gjallar-vpn-trust.serviceConfig.ExecStart;
assert !(legacy.systemd.services ? tailscale-local-lan-bypass);
assert
  (policyOf vpn).trustedWifis == [
    "synthetic-5Ghz"
    "synthetic-guest"
  ];
assert (policyOf vpn).exitNode == "home-router";
assert (policyOf vpn).wifiExitNodes == { synthetic-guest = "auto"; };
assert vpn.services.tailscale.useRoutingFeatures == "client";
# The internet block (nettrust ApplyBlock) needs nft on the unit's path.
assert builtins.elem pkgs.nftables vpn.systemd.services.gjallar-vpn-trust.path;
assert !(policyOf vpn).routerHome;
assert (policyOf routerHome).routerHome && (policyOf routerHome).homeSubnets == [ ];
assert vpn.systemd.paths.gjallar-vpn-trust.pathConfig.Unit == "gjallar-vpn-trust.service";
assert builtins.elem "timers.target" vpn.systemd.timers.gjallar-vpn-trust.wantedBy;
assert nixpkgs.lib.hasInfix "restart --no-block gjallar-vpn-trust.service"
  vpn.systemd.services.tailscaled.postStart;
# The boot block runs before networking with the policy the verdict uses,
# and a rebuild never runs it under a running system.
assert
  let
    boot = vpn.systemd.services.gjallar-vpn-boot-block;
  in
  boot.unitConfig.DefaultDependencies == false
  && builtins.elem "sysinit.target" boot.wantedBy
  && builtins.all (u: builtins.elem u boot.before) [
    "network-pre.target"
    "gjallar-vpn-trust.service"
    "sysinit.target"
  ]
  && !boot.restartIfChanged
  && builtins.elem pkgs.nftables boot.path
  &&
    builtins.unsafeDiscardStringContext boot.serviceConfig.ExecStart
    ==
      builtins.unsafeDiscardStringContext (
        builtins.replaceStrings [ "vpn apply" ] [ "vpn boot-block" ]
          vpn.systemd.services.gjallar-vpn-trust.serviceConfig.ExecStart
      );
assert !off.services.tailscale.enable;
assert !(off.systemd.services ? gjallar-vpn-trust);
assert !(off.systemd.services ? gjallar-vpn-boot-block);
pkgs.runCommand "gjallar-tailscale-trust-check" { } "touch $out"
