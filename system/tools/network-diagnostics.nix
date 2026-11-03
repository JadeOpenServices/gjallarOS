{ pkgs, ... }:
let
  # gjallarctl network starts this from the system profile's libexec
  # (Plane 282); it is not on PATH.
  networkDiagnostics = pkgs.writeTextFile {
    name = "gjallar-network-diagnostics";
    destination = "/libexec/gjallar-network-diagnostics";
    executable = true;
    checkPhase = "${pkgs.stdenv.shellDryRun} \"$target\"";
    text = "#!${pkgs.runtimeShell}\n" + ''
    section() {
      printf '\n=== %s ===\n' "$1"
    }

    usage() {
      cat <<'EOF'
Usage:
  gjallarctl network [summary]
  gjallarctl network interfaces
  gjallarctl network sockets
  gjallarctl network dns NAME
  gjallarctl network route IP
  gjallarctl network tools

Commands:
  summary     Local link, address, route, DNS, NetworkManager and socket state.
  interfaces  Interface, address, Wi-Fi and link information.
  sockets     Listening TCP/UDP sockets.
  dns NAME    Query the configured resolver plus A, AAAA and DNSSEC-aware data.
  route IP    Show the kernel route selected for an IPv4 or IPv6 address.
  tools       Show the diagnostic tools provided by GjallarOS.

Packet capture, active scanning and throughput testing remain explicit operator
actions through tcpdump, tshark, termshark, nmap, iperf3 and the other raw tools.
EOF
    }

    mode="$1"
    if [ -z "$mode" ]; then
      mode="summary"
    else
      shift
    fi

    case "$mode" in
      summary)
        section "LINKS"
        ${pkgs.iproute2}/bin/ip -brief link || true

        section "ADDRESSES"
        ${pkgs.iproute2}/bin/ip -brief address || true

        section "IPv4 ROUTES"
        ${pkgs.iproute2}/bin/ip -4 route show || true

        section "IPv6 ROUTES"
        ${pkgs.iproute2}/bin/ip -6 route show || true

        section "NEIGHBOURS"
        ${pkgs.iproute2}/bin/ip neigh show || true

        section "NETWORKMANAGER"
        ${pkgs.networkmanager}/bin/nmcli general status || true
        ${pkgs.networkmanager}/bin/nmcli device status || true

        section "DNS"
        ${pkgs.systemd}/bin/resolvectl status || true

        section "LISTENING SOCKETS"
        ${pkgs.iproute2}/bin/ss -lntu || true
        ;;

      interfaces)
        section "LINKS"
        ${pkgs.iproute2}/bin/ip -details -statistics link show || true

        section "ADDRESSES"
        ${pkgs.iproute2}/bin/ip -brief address show || true

        section "NETWORKMANAGER DEVICES"
        ${pkgs.networkmanager}/bin/nmcli device status || true

        section "WI-FI DEVICES"
        ${pkgs.iw}/bin/iw dev || true
        ;;

      sockets)
        section "LISTENING TCP/UDP SOCKETS"
        ${pkgs.iproute2}/bin/ss -lntup || true
        ;;

      dns)
        target="$1"

        if [ -z "$target" ]; then
          printf 'ERROR: dns requires a hostname\n' >&2
          usage >&2
          exit 64
        fi

        section "SYSTEM RESOLVER"
        ${pkgs.systemd}/bin/resolvectl query "$target" || true

        section "A"
        ${pkgs.bind}/bin/dig +short A "$target" || true

        section "AAAA"
        ${pkgs.bind}/bin/dig +short AAAA "$target" || true

        section "DNSSEC / FULL RESPONSE"
        ${pkgs.bind}/bin/dig +dnssec "$target" || true
        ;;

      route)
        target="$1"

        if [ -z "$target" ]; then
          printf 'ERROR: route requires an IPv4 or IPv6 address\n' >&2
          usage >&2
          exit 64
        fi

        section "SELECTED ROUTE"

        case "$target" in
          *:*)
            ${pkgs.iproute2}/bin/ip -6 route get "$target"
            ;;
          *)
            ${pkgs.iproute2}/bin/ip -4 route get "$target"
            ;;
        esac
        ;;

      tools)
        cat <<'EOF'
Connectivity and routing:
  ip, ss, ping, tracepath, traceroute, mtr

DNS:
  dig, host, nslookup, drill

Link and wireless:
  ethtool, iw, arping

Discovery and transport:
  nmap, nc, ncat, socat, iperf3

Packet and traffic inspection:
  tcpdump, tshark, termshark, bandwhich, bmon, iftop, nethogs

Firewall and connection state:
  nft, conntrack

HTTP, TLS and registration:
  curl, wget, openssl, whois
EOF
        ;;

      help|-h|--help)
        usage
        ;;

      *)
        printf 'ERROR: unknown command: %s\n' "$mode" >&2
        usage >&2
        exit 64
        ;;
    esac
  '';
  };
in
{
  # Operator-facing network diagnostics and traffic-inspection toolkit.
  #
  # This module owns diagnostic utilities only. Network policy, interface
  # configuration, firewall policy and background services remain with their
  # respective feature modules.
  environment.systemPackages =
    (with pkgs; [
      # Routing, sockets and reachability.
      iproute2
      iputils
      traceroute
      mtr

      # DNS.
      bind
      ldns

      # Physical/link and wireless inspection.
      ethtool
      iw
      arping

      # Discovery, transport and throughput testing.
      nmap
      netcat-openbsd
      socat
      iperf3

      # Packet and live traffic inspection.
      tcpdump
      wireshark-cli
      termshark
      bandwhich
      bmon
      iftop
      nethogs

      # Firewall and connection-state inspection.
      nftables
      conntrack-tools

      # HTTP, TLS and registration diagnostics.

            curl
      wget
      openssl

          whois
    ])
    ++ [
      networkDiagnostics
    ];
}
