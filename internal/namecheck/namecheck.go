// Package namecheck finds out whether a hostname is already taken before
// the settings service writes it: on the tailnet, by asking tailscale, and
// on the current network, by asking for <name>.local over mDNS (RFC 6762).
//
// The mDNS part reads packets anyone on the network can send, so it runs in
// gjallar-namecheck, an unprivileged helper the root service asks over a
// unix socket; root never parses them.
package namecheck

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/nettrust"
)

const (
	mdnsPort = 5353
	// ReplyPort is where the helper asks from and gets its answers. A stateful
	// firewall cannot tie a unicast answer to a question sent to a multicast
	// group, so it must let answers to this port in; below 1024, no
	// unprivileged program can take it. IANA keeps 1011-1020 reserved.
	ReplyPort = 1015
	typeA     = 1
	typeAAAA  = 28
	classIN   = 1

	// queryGap and window: three questions, then wait for late answers.
	queryGap = 250 * time.Millisecond
	window   = time.Second
)

var (
	groupV4 = netip.MustParseAddr("224.0.0.251")
	groupV6 = netip.MustParseAddr("ff02::fb")

	labelPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,63}$`)
	devicePattern = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,15}$`)
)

// Clash returns the tailnet peer that already uses name as its host name or
// MagicDNS name. Letter case does not matter; shared peers count too.
func Clash(tn nettrust.Tailnet, name string) (nettrust.Peer, bool) {
	for _, peer := range tn.Peers {
		if peer.Matches(name) {
			return peer, true
		}
	}
	return nettrust.Peer{}, false
}

// Probe asks the network on device who answers for name.local. It returns
// the first address that answered, or an invalid address when none did.
func Probe(ctx context.Context, device, name string) (netip.Addr, error) {
	if !labelPattern.MatchString(name) {
		return netip.Addr{}, fmt.Errorf("invalid name %q", name)
	}
	if !devicePattern.MatchString(device) {
		return netip.Addr{}, fmt.Errorf("invalid device %q", device)
	}
	iface, err := net.InterfaceByName(device)
	if err != nil {
		return netip.Addr{}, err
	}
	id, err := randomID()
	if err != nil {
		return netip.Addr{}, err
	}
	packet := query(id, name)

	ctx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	answers := make(chan netip.Addr, 2)
	var conns []*net.UDPConn
	var groups []*net.UDPAddr
	var errs []error
	for _, family := range []string{"udp4", "udp6"} {
		conn, group, err := listen(ctx, family, iface)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		defer conn.Close()
		conns = append(conns, conn)
		groups = append(groups, group)
		go read(conn, id, name, answers)
	}
	if len(conns) == 0 {
		return netip.Addr{}, fmt.Errorf("ask %s.local on %s: %w", name, device, errors.Join(errs...))
	}

	sent := 0
	tick := time.NewTicker(queryGap)
	defer tick.Stop()
	for round := 0; ; {
		if round < 3 {
			for i, conn := range conns {
				if _, err := conn.WriteToUDP(packet, groups[i]); err == nil {
					sent++
				} else {
					errs = append(errs, err)
				}
			}
			round++
		}
		select {
		case addr := <-answers:
			return addr, nil
		case <-ctx.Done():
			if sent == 0 {
				return netip.Addr{}, fmt.Errorf("ask %s.local on %s: %w", name, device, errors.Join(errs...))
			}
			return netip.Addr{}, nil
		case <-tick.C:
		}
	}
}

// listen opens a socket that sends to the mDNS group on iface only, with
// the hop limit of 255 RFC 6762 section 11 asks for. The port is ReplyPort,
// not 5353, so responders answer it by unicast (section 6.7).
func listen(ctx context.Context, family string, iface *net.Interface) (*net.UDPConn, *net.UDPAddr, error) {
	group := &net.UDPAddr{IP: groupV4.AsSlice(), Port: mdnsPort}
	if family == "udp6" {
		group = &net.UDPAddr{IP: groupV6.AsSlice(), Port: mdnsPort, Zone: iface.Name}
	}
	config := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
		var err error
		if ctrl := raw.Control(func(fd uintptr) {
			if family == "udp4" {
				if err = syscall.SetsockoptIPMreqn(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, &syscall.IPMreqn{Ifindex: int32(iface.Index)}); err == nil {
					err = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, 255)
				}
				return
			}
			if err = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_MULTICAST_IF, iface.Index); err == nil {
				err = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_MULTICAST_HOPS, 255)
			}
		}); ctrl != nil {
			return ctrl
		}
		return err
	}}
	address := fmt.Sprintf("0.0.0.0:%d", ReplyPort)
	if family == "udp6" {
		address = fmt.Sprintf("[::]:%d", ReplyPort)
	}
	pc, err := config.ListenPacket(ctx, family, address)
	if err != nil {
		return nil, nil, err
	}
	return pc.(*net.UDPConn), group, nil
}

// read passes on the source of the first answer for name, then stops; a
// closed socket ends it.
func read(conn *net.UDPConn, id uint16, name string, answers chan<- netip.Addr) {
	buf := make([]byte, 9000)
	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		if from.Port() != mdnsPort || !claims(buf[:n], id, name) {
			continue
		}
		select {
		case answers <- from.Addr().Unmap():
		default:
		}
		return
	}
}

func randomID() (uint16, error) {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b[:]), nil
}

// query is a legacy unicast question (RFC 6762 section 6.7) for the A and
// AAAA records of name.local.
func query(id uint16, name string) []byte {
	packet := binary.BigEndian.AppendUint16(nil, id)
	packet = append(packet, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0)
	for _, kind := range []uint16{typeA, typeAAAA} {
		packet = append(packet, byte(len(name)))
		packet = append(packet, name...)
		packet = append(packet, 5)
		packet = append(packet, "local"...)
		packet = append(packet, 0)
		packet = binary.BigEndian.AppendUint16(packet, kind)
		packet = binary.BigEndian.AppendUint16(packet, classIN)
	}
	return packet
}

// claims reports whether packet is a response to query id that holds an A
// or AAAA record for name.local, in any section. Anything malformed is not.
func claims(packet []byte, id uint16, name string) bool {
	if len(packet) < 12 || binary.BigEndian.Uint16(packet) != id || packet[2]&0x80 == 0 {
		return false
	}
	questions := int(binary.BigEndian.Uint16(packet[4:]))
	records := int(binary.BigEndian.Uint16(packet[6:])) + int(binary.BigEndian.Uint16(packet[8:])) + int(binary.BigEndian.Uint16(packet[10:]))
	at := 12
	for range questions {
		_, next, ok := readName(packet, at)
		if !ok || next+4 > len(packet) {
			return false
		}
		at = next + 4
	}
	for range records {
		owner, next, ok := readName(packet, at)
		if !ok || next+10 > len(packet) {
			return false
		}
		kind := binary.BigEndian.Uint16(packet[next:])
		// The top bit of the class is mDNS's cache-flush flag.
		class := binary.BigEndian.Uint16(packet[next+2:]) & 0x7fff
		length := int(binary.BigEndian.Uint16(packet[next+8:]))
		at = next + 10 + length
		if at > len(packet) {
			return false
		}
		if class == classIN && len(owner) == 2 && strings.EqualFold(owner[0], name) && strings.EqualFold(owner[1], "local") &&
			(kind == typeA && length == 4 || kind == typeAAAA && length == 16) {
			return true
		}
	}
	return false
}

// readName reads a domain name at offset at and returns its labels with
// the offset after it. Compression pointers must point backwards, so a name cannot
// loop; a name longer than 255 bytes is malformed.
func readName(packet []byte, at int) ([]string, int, bool) {
	var labels []string
	size, next := 0, -1
	for limit := at; ; {
		if at >= len(packet) {
			return nil, 0, false
		}
		length := int(packet[at])
		switch {
		case length == 0:
			if next < 0 {
				next = at + 1
			}
			return labels, next, true
		case length&0xc0 == 0xc0:
			if at+1 >= len(packet) {
				return nil, 0, false
			}
			target := int(binary.BigEndian.Uint16(packet[at:]) & 0x3fff)
			if target >= limit {
				return nil, 0, false
			}
			if next < 0 {
				next = at + 2
			}
			at, limit = target, target
		case length&0xc0 != 0:
			return nil, 0, false
		default:
			if at+1+length > len(packet) {
				return nil, 0, false
			}
			if size += length + 1; size > 255 {
				return nil, 0, false
			}
			labels = append(labels, string(packet[at+1:at+1+length]))
			at += 1 + length
		}
	}
}
