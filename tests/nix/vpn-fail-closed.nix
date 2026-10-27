# Loophole 24 (Plane 190): on an untrusted network the VPN fails closed.
# headscale on "server" stands in for the Tailscale servers, and 203.0.113.1
# on server is "the internet". It sits behind server, not on the exit node's
# LAN: an exit node does not forward to its own LAN. The client's LAN is
# vlan 1, behind router.
{ pkgs, tailscaleModules }:
let
  cert = pkgs.runCommand "vpn-fail-closed-cert" { nativeBuildInputs = [ pkgs.openssl ]; } ''
    openssl req -x509 -newkey rsa:2048 -sha256 -days 365 -nodes \
      -out cert.pem -keyout key.pem \
      -subj '/CN=server' -addext "subjectAltName=DNS:server"
    mkdir -p $out
    cp key.pem cert.pem $out
  '';
  # Node numbers follow the sorted names: client 1, exit 2, router 3, server 4.
  toLAN = {
    networking.interfaces.eth1.ipv4.routes = [
      {
        address = "192.168.1.0";
        prefixLength = 24;
        via = "192.168.2.3";
      }
    ];
  };
  internetAddress = "203.0.113.1";
  toInternet = nic: {
    networking.interfaces.${nic}.ipv4.routes = [
      {
        address = internetAddress;
        prefixLength = 32;
        via = "192.168.2.4";
      }
    ];
  };
  web = port: {
    systemd.services.web = {
      wantedBy = [ "multi-user.target" ];
      serviceConfig.ExecStart = "${pkgs.python3}/bin/python3 -m http.server ${toString port} -d /etc";
    };
    networking.firewall.allowedTCPPorts = [ port ];
  };
in
pkgs.testers.runNixOSTest {
  name = "vpn-fail-closed";

  nodes = {
    router = {
      imports = [
        (toInternet "eth2")
        (web 8000)
      ];
      virtualisation.vlans = [
        1
        2
      ];
      boot.kernel.sysctl."net.ipv4.ip_forward" = 1;
    };

    server = {
      imports = [
        toLAN
        (web 8000)
      ];
      virtualisation.vlans = [ 2 ];
      # Answers to tailnet addresses go back through the exit node.
      networking.interfaces.eth1.ipv4.routes = [
        {
          address = "100.64.0.0";
          prefixLength = 10;
          via = "192.168.2.2";
        }
      ];
      services.headscale = {
        enable = true;
        port = 8080;
        settings = {
          server_url = "https://server";
          prefixes.v4 = "100.64.0.0/10";
          derp = {
            server = {
              enabled = true;
              region_id = 999;
              stun_listen_addr = "0.0.0.0:3478";
            };
            urls = [ ];
          };
          dns = {
            base_domain = "tailnet";
            override_local_dns = false;
          };
          # Every node may use the exit node.
          policy.path = pkgs.writeText "acl.json" (
            builtins.toJSON {
              acls = [
                {
                  action = "accept";
                  src = [ "*" ];
                  dst = [
                    "*:*"
                    "autogroup:internet:*"
                  ];
                }
              ];
            }
          );
        };
      };
      services.nginx = {
        enable = true;
        virtualHosts.server = {
          addSSL = true;
          sslCertificate = "${cert}/cert.pem";
          sslCertificateKey = "${cert}/key.pem";
          locations."/" = {
            proxyPass = "http://127.0.0.1:8080";
            proxyWebsockets = true;
          };
        };
      };
      networking.firewall.allowedTCPPorts = [
        80
        443
      ];
      networking.firewall.allowedUDPPorts = [ 3478 ];
      environment.systemPackages = [
        pkgs.headscale
        pkgs.jq
      ];
    };

    exit = {
      imports = [
        toLAN
        (toInternet "eth1")
      ];
      virtualisation.vlans = [ 2 ];
      services.tailscale = {
        enable = true;
        useRoutingFeatures = "server";
      };
      security.pki.certificateFiles = [ "${cert}/cert.pem" ];
    };

    client = {
      imports = tailscaleModules;
      _module.args.settings = {
        apps = [ "tailscale" ];
        tailscale = {
          homeSubnets = [ ];
          trustedWifis = [ ];
          exitNode = "auto";
        };
      };
      virtualisation.vlans = [ 1 ];
      networking.defaultGateway = {
        address = "192.168.1.3";
        interface = "eth1";
      };
      # The test network is configured statically; NetworkManager only
      # has to exist for nmcli and the dispatcher.
      networking.networkmanager = {
        enable = true;
        unmanaged = [ "*" ];
      };
      security.pki.certificateFiles = [ "${cert}/cert.pem" ];
      environment.systemPackages = [
        (pkgs.callPackage ../../pkgs/gjallarctl { })
        pkgs.jq
        pkgs.nftables
      ];
    };
  };

  testScript = ''
    import json
    import time

    start_all()
    # The internet address; networking.interfaces.lo does not set it here.
    server.wait_for_unit("network.target")
    server.succeed("ip addr add ${internetAddress}/32 dev lo")
    server.wait_for_unit("headscale.service")
    server.wait_for_open_port(443)
    for m in (router, server):
        m.wait_for_open_port(8000)
    server.succeed("headscale users create test")
    key = server.succeed("headscale preauthkeys -u 1 create --reusable").strip().splitlines()[-1]
    up = f"tailscale up --login-server https://server --auth-key {key}"

    exit.wait_for_unit("tailscaled.service")
    exit.succeed(up + " --advertise-exit-node")
    exit_id = server.wait_until_succeeds(
        "headscale nodes list -o json | jq -er '.[] | select(.given_name == \"exit\" or .givenName == \"exit\" or .name == \"exit\") | .id'"
    ).strip()
    server.succeed(f"headscale nodes approve-routes --identifier {exit_id} --routes 0.0.0.0/0,::/0")

    client.wait_for_unit("tailscaled.service")
    client.succeed(up)
    print(client.succeed("ip route; ip -6 route"))
    client.wait_until_succeeds("tailscale status --json | jq -e '.Peer[] | select(.HostName == \"exit\") | .ExitNodeOption'")

    internet = "curl -fsS -m 5 -o /dev/null http://${internetAddress}:8000/hostname"
    lan = "curl -fsS -m 5 -o /dev/null http://192.168.1.3:8000/hostname"
    no_table = "! nft list table inet gjallar_vpn_block"


    def apply(expect_rc=None):
        rc, out = client.execute("systemctl restart gjallar-vpn-trust.service 2>&1; journalctl -u gjallar-vpn-trust.service -n 20 -o cat")
        print(out)
        return json.loads(client.succeed("cat /run/gjallar/vpn-trust.json"))


    with subtest("the boot block ran before networking"):
        boot = "gjallar-vpn-boot-block.service"
        assert client.succeed(f"systemctl show -p Result --value {boot}").strip() == "success"
        client.succeed(f"journalctl -b -u {boot} -o cat | grep -q 'until the first network check since boot'")
        done = int(client.succeed(f"systemctl show -p ExecMainExitTimestampMonotonic --value {boot}"))
        pre = int(client.succeed("systemctl show -p ActiveEnterTimestampMonotonic --value network-pre.target"))
        assert 0 < done <= pre, (done, pre)

    with subtest("online exit node carries the internet, no block"):
        v = apply()
        assert v["trust"] == "untrusted" and v["exitNode"] == "exit" and "blocked" not in v, v
        client.succeed(no_table)
        client.succeed("test ! -e /run/gjallar/vpn-boot-block")
        client.wait_until_succeeds(internet, timeout=60)
        # A rebuild or a manual start never blocks a checked network.
        client.succeed("systemctl start gjallar-vpn-boot-block.service")
        client.succeed(no_table)

    with subtest("exit node goes offline: it stays set and nothing leaks"):
        exit.succeed("systemctl stop tailscaled.service")
        client.wait_until_succeeds("tailscale status --json | jq -e '.Peer[] | select(.HostName == \"exit\") | .Online | not'", timeout=120)
        v = apply()
        assert v["exitNode"] == "exit" and v["blocked"] == "exit-node-offline", v
        client.succeed("nft list table inet gjallar_vpn_block")
        client.fail(internet)
        client.succeed(lan)

    with subtest("auto with no current exit node sets the offline one"):
        client.succeed("tailscale set --exit-node=")
        v = apply()
        print("offline exit node accepted by tailscale set:", v.get("exitNodeError", "yes"))
        assert v["blocked"] in ("exit-node-offline", "no-exit-node"), v
        client.fail(internet)
        client.succeed(lan)

    with subtest("Tailscale stopped: the firewall blocks, LAN and marked traffic pass"):
        # gjallar-vpn-trust wants tailscaled, so a plain stop does not
        # last. This drop-in makes it fail to start, as a broken daemon
        # does, without the restart loop that would hold up every apply();
        # a runtime mask loses to the unit in /etc.
        client.succeed(
            "mkdir -p /run/systemd/system/tailscaled.service.d"
            " && printf '[Service]\\nExecStartPre=/run/current-system/sw/bin/false\\nRestart=no\\n' > /run/systemd/system/tailscaled.service.d/down.conf"
            " && systemctl daemon-reload && systemctl stop tailscaled.service"
        )
        v = apply()
        assert v["blocked"] == "tailscale-down", v
        client.fail(internet)
        client.succeed(lan)
        client.succeed("ping -c 1 -W 2 192.168.1.3")
        client.fail("ping -c 1 -W 2 ${internetAddress}")
        client.succeed("ping -c 1 -W 2 -m 524288 ${internetAddress}")
        client.fail("timeout 5 bash -c 'exec 3<>/dev/tcp/192.168.1.3/53'")
        print(client.succeed("nft list table inet gjallar_vpn_block"))

    def pause(seconds):
        """What the settings service writes after the password, as root."""
        # A fresh verdict: the last one may be for a network already left.
        key = apply().get("network")
        assert key, "the network has no key"
        now = int(client.succeed("date +%s"))
        stamp = lambda t: time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(t))
        p = json.dumps({"network": key, "user": "synthetic-user", "uid": 1000, "started": stamp(now - 1), "until": stamp(now - 1 + seconds)})
        client.succeed(f"echo '{p}' > /run/gjallar/vpn-pause/pause.json")
        return apply()


    with subtest("a pause lets this network through until the network changes"):
        v = pause(3600)
        assert "blocked" not in v and v["paused"]["blocked"] == "tailscale-down", v
        client.succeed(no_table)
        client.succeed(internet)
        client.succeed("gjallarctl vpn status | grep -q '^paused:'")
        client.succeed("ip addr add 10.99.0.1/24 dev eth1")
        v = apply()
        client.succeed("ip addr del 10.99.0.1/24 dev eth1")
        assert v["blocked"] == "tailscale-down" and "paused" not in v, v
        client.succeed("test ! -e /run/gjallar/vpn-pause/pause.json")
        client.succeed("nft list table inet gjallar_vpn_block")
        client.fail(internet)

    with subtest("a pause ends on its own, on resume, and before sleep"):
        pause(30)
        client.succeed(no_table)
        client.wait_until_succeeds("nft list table inet gjallar_vpn_block", timeout=60)
        client.succeed("test ! -e /run/gjallar/vpn-pause/pause.json")
        client.fail(internet)
        pause(3600)
        client.succeed(no_table)
        client.succeed("gjallarctl vpn resume")
        client.succeed("nft list table inet gjallar_vpn_block")
        pause(3600)
        client.succeed(no_table)
        client.succeed("systemctl start gjallar-vpn-pause-sleep.service")
        client.succeed("nft list table inet gjallar_vpn_block")
        client.fail(internet)
        client.fail("gjallarctl vpn pause")

    with subtest("exit node off: the block goes away"):
        client.succeed("gjallarctl vpn exit-node off")
        client.wait_until_succeeds(no_table, timeout=60)
        client.succeed(internet)

    with subtest("back to auto with the exit node online again"):
        exit.succeed("systemctl start tailscaled.service")
        client.succeed("rm /run/systemd/system/tailscaled.service.d/down.conf && systemctl daemon-reload && systemctl reset-failed tailscaled.service")
        client.succeed("systemctl start tailscaled.service")
        client.wait_until_succeeds("tailscale status --json | jq -e '.Peer[] | select(.HostName == \"exit\") | .Online'", timeout=120)
        client.succeed("gjallarctl vpn exit-node default")
        client.wait_until_succeeds("jq -e '.exitNode == \"exit\" and (has(\"blocked\") | not)' /run/gjallar/vpn-trust.json", timeout=60)
        client.succeed(no_table)
        client.wait_until_succeeds(internet, timeout=60)

    with subtest("boot block: only the tunnel until the first verdict, however late"):
        # As at boot: no verdict in /run, and the trust service does not
        # run, as when it hangs or fails.
        client.succeed(
            "mkdir -p /run/systemd/system/gjallar-vpn-trust.service.d"
            " && printf '[Unit]\\nConditionPathExists=/nonexistent\\n' > /run/systemd/system/gjallar-vpn-trust.service.d/off.conf"
            " && systemctl daemon-reload && rm /run/gjallar/vpn-trust.json"
            " && systemctl start gjallar-vpn-boot-block.service"
        )
        client.succeed("nft list chain inet gjallar_vpn_block output | grep -qF 'oifname \"tailscale*\" accept'")
        client.succeed("gjallarctl vpn status | grep -q 'not checked yet'")
        client.succeed("systemctl start gjallar-vpn-trust.service")
        client.succeed("test -e /run/gjallar/vpn-boot-block && test ! -e /run/gjallar/vpn-trust.json")
        # The exit node still carries the internet; nothing leaves through
        # eth1 but tailscaled's own packets until a verdict says where the
        # device is.
        client.succeed(internet)
        client.fail("ping -c 1 -W 2 -I eth1 192.168.1.3")
        client.fail("curl -fsS -m 5 -o /dev/null --interface eth1 http://${internetAddress}:8000/hostname")
        client.succeed("ping -c 1 -W 2 -m 524288 -I eth1 192.168.1.3")
        client.succeed("rm -r /run/systemd/system/gjallar-vpn-trust.service.d && systemctl daemon-reload")
        v = apply()
        assert v["exitNode"] == "exit" and "blocked" not in v, v
        client.succeed(no_table)
        client.succeed("test ! -e /run/gjallar/vpn-boot-block")
        client.succeed("ping -c 1 -W 2 -I eth1 192.168.1.3")
        client.succeed("! gjallarctl vpn status | grep -q 'not checked yet'")
  '';
}
