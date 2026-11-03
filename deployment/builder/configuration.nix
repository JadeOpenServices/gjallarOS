# Second release builder (Plane 214, Q104/Q105). It builds each release on
# its own and signs the result with a key that never leaves this VM, so a
# device can ask for two signatures from two machines.
#
# No personal values: no host names, addresses or accounts of the machine it
# runs on. It only makes outbound connections; nothing listens.
{
  lib,
  pkgs,
  modulesPath,
  ...
}:
let
  keyName = "gjallaros-build-2";
  keyDir = "/var/lib/gjallar-builder";
in
{
  imports = [ "${modulesPath}/profiles/qemu-guest.nix" ];

  system.stateVersion = "26.05";
  networking.hostName = "gjallaros-build";

  # Image boots on OVMF; disk-image.nix sets the partitions and grows root.
  image.efiSupport = true;
  boot.loader.efi.canTouchEfiVariables = false;

  # The hypervisor reaches the VM through the guest agent only.
  services.qemuGuest.enable = true;
  services.openssh.enable = false;
  networking.firewall = {
    enable = true;
    allowedTCPPorts = [ ];
    allowedUDPPorts = [ ];
  };

  # Own subnet behind NAT on the hypervisor; the hypervisor drops anything
  # aimed at private ranges, so only public hosts answer.
  networking.useDHCP = false;
  networking.useNetworkd = true;
  systemd.network.networks."10-uplink" = {
    matchConfig.Type = "ether";
    address = [ "10.250.0.2/30" ];
    gateway = [ "10.250.0.1" ];
    networkConfig.IPv6AcceptRA = false;
  };

  # Networks may refuse DNS to outside servers (port 53 and 853), and the VM
  # must not use the LAN resolver. So names go over HTTPS to Quad9, reached
  # by address: the stamps hold the IPs, so no plain DNS is needed to start.
  # resolved goes too, which also drops its LLMNR and mDNS listeners.
  services.resolved.enable = false;
  # Without resolved, networkd hands nameservers to nothing: name the local
  # proxy in resolv.conf directly.
  networking.resolvconf.useLocalResolver = true;
  services.dnscrypt-proxy = {
    enable = true;
    upstreamDefaults = false;
    settings = {
      listen_addresses = [
        "127.0.0.1:53"
        "[::1]:53"
      ];
      server_names = [
        "quad9-a"
        "quad9-b"
      ];
      ipv6_servers = false;
      # No IPv6 route: answer AAAA empty instead of letting clients try it.
      block_ipv6 = true;
      ignore_system_dns = true;
      netprobe_address = "9.9.9.9:443";
      cache = true;
      # DoH, DNSSEC + no logs, dns.quad9.net at 9.9.9.9 / 149.112.112.112.
      static.quad9-a.stamp = "sdns://AgMAAAAAAAAABzkuOS45LjkADWRucy5xdWFkOS5uZXQKL2Rucy1xdWVyeQ";
      static.quad9-b.stamp = "sdns://AgMAAAAAAAAADzE0OS4xMTIuMTEyLjExMgANZG5zLnF1YWQ5Lm5ldAovZG5zLXF1ZXJ5";
    };
  };

  # No login: no passwords, no keys, no console user.
  users.mutableUsers = false;
  users.users.root.hashedPassword = "!";
  users.allowNoPasswordLogin = true;

  # Builds gjallarOS paths itself: only the upstream NixOS cache, never the
  # gjallarOS one, so its signature is a second witness and not a copy.
  nix.settings = {
    experimental-features = [
      "nix-command"
      "flakes"
    ];
    substituters = lib.mkForce [ "https://cache.nixos.org" ];
    trusted-public-keys = lib.mkForce [ "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=" ];
    sandbox = true;
    allowed-users = [ "root" ];
    trusted-users = [ "root" ];
    # Builds only pinned revisions: no name lookups in any registry.
    flake-registry = "";
  };
  nixpkgs.flake = {
    setFlakeRegistry = false;
    setNixPath = false;
  };
  nix.channel.enable = false;
  nix.gc = {
    automatic = true;
    dates = "weekly";
    options = "--delete-older-than 14d";
  };

  # The signing key is made on first boot and stays in this VM. Only the
  # public half is ever printed.
  systemd.services.gjallar-builder-key = {
    description = "Make the release builder signing key";
    wantedBy = [ "multi-user.target" ];
    unitConfig.ConditionPathExists = "!${keyDir}/${keyName}.secret";
    serviceConfig = {
      Type = "oneshot";
      UMask = "0077";
      StateDirectory = "gjallar-builder";
      StateDirectoryMode = "0700";
    };
    script = ''
      ${pkgs.nix}/bin/nix-store --generate-binary-cache-key ${keyName} \
        ${keyDir}/${keyName}.secret.tmp ${keyDir}/${keyName}.public
      mv ${keyDir}/${keyName}.secret.tmp ${keyDir}/${keyName}.secret
    '';
  };

  environment.systemPackages = [ pkgs.git ];
}
