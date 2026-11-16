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
  # Runs everything that touches tagged code: listing tags, evaluating and
  # building the flake. It cannot read keyDir.
  buildUser = "gjallar-release-build";
  sourceRepo = "JadeOpenServices/gjallarOS";
  statementsRepo = "JadeOpenServices/gjallarOS-statements";
  # The first tag with a gjallar-release output; older tags are skipped.
  firstTag = "v2026.10.67";

  statement = pkgs.writeShellApplication {
    name = "gjallar-builder-statement";
    runtimeInputs = with pkgs; [
      coreutils
      gnugrep
      gnused
      git
      nix
      openssh
      util-linux
    ];
    text = builtins.readFile ./statement.sh;
  };

  # Puts the statements push token in place. Reads it from stdin so it never
  # shows on a command line:
  #   qm guest exec <vmid> --pass-stdin 1 -- /run/current-system/sw/bin/gjallar-builder-set-token
  setToken = pkgs.writeShellApplication {
    name = "gjallar-builder-set-token";
    runtimeInputs = [ pkgs.coreutils ];
    text = ''
      umask 077
      [ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
      tok=$(head -c 1024 | tr -d '\n')
      [[ $tok =~ ^github_pat_[A-Za-z0-9_]+$ ]] || { echo "not a fine-grained token (github_pat_...)" >&2; exit 1; }
      install -d -m 0700 ${keyDir}
      printf '%s\n' "$tok" >${keyDir}/statements-token.tmp
      mv -f ${keyDir}/statements-token.tmp ${keyDir}/statements-token
      echo "token saved"
    '';
  };
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
    trusted-public-keys = lib.mkForce [
      "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="
    ];
    sandbox = true;
    # The build user is not trusted: it cannot pick other caches or keys.
    allowed-users = [
      "root"
      buildUser
    ];
    trusted-users = [ "root" ];
    # Builds only pinned revisions: no name lookups in any registry.
    flake-registry = "";
    # A tagged flake.nix must not add caches or settings.
    accept-flake-config = false;
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

  # The statement key signs the SHA256SUMS of each release this VM built;
  # the installer-sign environment pins its public half (BUILDER_SSH_KEY).
  systemd.services.gjallar-builder-statement-key = {
    description = "Make the release statement SSH key";
    wantedBy = [ "multi-user.target" ];
    unitConfig.ConditionPathExists = "!${keyDir}/statement-key";
    serviceConfig = {
      Type = "oneshot";
      UMask = "0077";
      StateDirectory = "gjallar-builder";
      StateDirectoryMode = "0700";
    };
    script = ''
      rm -f ${keyDir}/statement-key.tmp ${keyDir}/statement-key.tmp.pub
      ${pkgs.openssh}/bin/ssh-keygen -q -t ed25519 -N "" -C gjallar-builder \
        -f ${keyDir}/statement-key.tmp
      mv ${keyDir}/statement-key.tmp.pub ${keyDir}/statement-key.pub
      chmod 0644 ${keyDir}/statement-key.pub
      mv ${keyDir}/statement-key.tmp ${keyDir}/statement-key
    '';
  };

  users.groups.${buildUser} = { };
  users.users.${buildUser} = {
    isSystemUser = true;
    group = buildUser;
    home = "/var/lib/${buildUser}";
    createHome = true;
  };

  # Builds the oldest tag without a statement and pushes its statement.
  systemd.services.gjallar-builder-statement = {
    description = "Build a release tag and push the builder statement";
    after = [
      "network-online.target"
      "gjallar-builder-statement-key.service"
    ];
    wants = [ "network-online.target" ];
    environment = {
      SOURCE_URL = "https://github.com/${sourceRepo}";
      STATEMENTS_URL = "https://github.com/${statementsRepo}";
      FIRST_TAG = firstTag;
      BUILD_USER = buildUser;
      KEY_DIR = keyDir;
    };
    serviceConfig = {
      Type = "oneshot";
      UMask = "0077";
      StateDirectory = "gjallar-builder";
      StateDirectoryMode = "0700";
      PrivateTmp = true;
      ExecStart = "${statement}/bin/gjallar-builder-statement";
      TimeoutStartSec = "12h";
    };
  };
  systemd.timers.gjallar-builder-statement = {
    wantedBy = [ "timers.target" ];
    timerConfig = {
      OnBootSec = "15min";
      OnUnitInactiveSec = "1h";
    };
  };

  environment.systemPackages = [
    pkgs.git
    setToken
  ];
}
