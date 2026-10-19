package namecheck

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/nettrust"
)

// All packets and names below are SYNTHETIC test data.

func TestClash(t *testing.T) {
	tn := nettrust.Tailnet{Running: true, Peers: []nettrust.Peer{
		{HostName: "Desk", DNSName: "desk.example-tailnet.ts.net."},
		{HostName: "shared box", DNSName: "nas.other-tailnet.ts.net."},
	}}
	for _, name := range []string{"desk", "DESK", "nas"} {
		if _, ok := Clash(tn, name); !ok {
			t.Errorf("%q not found", name)
		}
	}
	if peer, ok := Clash(tn, "lab"); ok {
		t.Errorf("lab clashes with %s", peer.DNSName)
	}
}

func TestQuery(t *testing.T) {
	packet := query(0x1234, "lab")
	if binary.BigEndian.Uint16(packet) != 0x1234 || binary.BigEndian.Uint16(packet[2:]) != 0 || binary.BigEndian.Uint16(packet[4:]) != 2 {
		t.Fatalf("header % x", packet[:12])
	}
	at := 12
	for _, kind := range []uint16{typeA, typeAAAA} {
		labels, next, ok := readName(packet, at)
		if !ok || strings.Join(labels, ".") != "lab.local" {
			t.Fatalf("question name %v", labels)
		}
		if got := binary.BigEndian.Uint16(packet[next:]); got != kind {
			t.Fatalf("question type %d, want %d", got, kind)
		}
		at = next + 4
	}
	if at != len(packet) {
		t.Fatalf("%d bytes left over", len(packet)-at)
	}
}

// response builds a SYNTHETIC answer with one record per entry.
func response(id uint16, flags uint16, owner []byte, kind uint16, rdata []byte) []byte {
	packet := binary.BigEndian.AppendUint16(nil, id)
	packet = binary.BigEndian.AppendUint16(packet, flags)
	packet = append(packet, 0, 0, 0, 1, 0, 0, 0, 0)
	packet = append(packet, owner...)
	packet = binary.BigEndian.AppendUint16(packet, kind)
	packet = binary.BigEndian.AppendUint16(packet, 0x8000|classIN)
	packet = append(packet, 0, 0, 0, 10)
	packet = binary.BigEndian.AppendUint16(packet, uint16(len(rdata)))
	return append(packet, rdata...)
}

func name(labels ...string) []byte {
	var out []byte
	for _, label := range labels {
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	return append(out, 0)
}

func TestClaims(t *testing.T) {
	v4 := []byte{192, 0, 2, 7}
	v6 := netip.MustParseAddr("2001:db8::7").AsSlice()
	cases := []struct {
		label  string
		packet []byte
		want   bool
	}{
		{"A record", response(7, 0x8400, name("lab", "local"), typeA, v4), true},
		{"AAAA record, other case", response(7, 0x8400, name("LAB", "Local"), typeAAAA, v6), true},
		{"other id", response(8, 0x8400, name("lab", "local"), typeA, v4), false},
		{"a question, not a response", response(7, 0, name("lab", "local"), typeA, v4), false},
		{"other name", response(7, 0x8400, name("lab2", "local"), typeA, v4), false},
		{"dot inside a label", response(7, 0x8400, name("lab.local"), typeA, v4), false},
		{"other type", response(7, 0x8400, name("lab", "local"), 16, []byte("\x03txt")), false},
		{"A record of the wrong size", response(7, 0x8400, name("lab", "local"), typeA, v6), false},
		{"cut short", response(7, 0x8400, name("lab", "local"), typeA, v4)[:30], false},
		{"too short for a header", []byte{0, 7, 0x84}, false},
	}
	for _, c := range cases {
		if got := claims(c.packet, 7, "lab"); got != c.want {
			t.Errorf("%s: claims = %v, want %v", c.label, got, c.want)
		}
	}
}

func TestClaimsCompressed(t *testing.T) {
	// The answer repeats the question and points back at its name.
	packet := binary.BigEndian.AppendUint16(nil, 7)
	packet = append(packet, 0x84, 0, 0, 1, 0, 1, 0, 0, 0, 0)
	packet = append(packet, name("lab", "local")...)
	packet = append(packet, 0, typeA, 0, classIN)
	packet = append(packet, 0xc0, 12, 0, typeA, 0x80, classIN, 0, 0, 0, 10, 0, 4, 192, 0, 2, 7)
	if !claims(packet, 7, "lab") {
		t.Fatal("compressed answer not read")
	}
}

func TestReadNameRefusesLoops(t *testing.T) {
	cases := map[string][]byte{
		"points at itself":       {0xc0, 0},
		"points forward":         {0xc0, 2, 0},
		"reserved label kind":    {0x40, 0},
		"label past the end":     {5, 'a', 'b'},
		"pointer past the end":   {0xc0},
		"name longer than 255":   append([]byte(strings.Repeat("\x3f"+strings.Repeat("a", 63), 4)), 0),
		"loop through two jumps": {3, 'l', 'a', 'b', 0xc0, 6, 0xc0, 4},
	}
	for label, packet := range cases {
		at := 0
		if label == "loop through two jumps" {
			at = 6
		}
		if _, _, ok := readName(packet, at); ok {
			t.Errorf("%s: read as a name", label)
		}
	}
}

func TestAskAndServe(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "namecheck.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	answers := map[string]netip.Addr{"lab": netip.MustParseAddr("192.0.2.7")}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			Serve(context.Background(), conn, conn, func(_ context.Context, device, name string) (netip.Addr, error) {
				if device != "eth0" {
					return netip.Addr{}, errors.New("no such device")
				}
				return answers[name], nil
			})
			conn.Close()
		}
	}()

	ctx := context.Background()
	if addr, err := Ask(ctx, socket, "eth0", "lab"); err != nil || addr.String() != "192.0.2.7" {
		t.Errorf("taken name: %v, %v", addr, err)
	}
	if addr, err := Ask(ctx, socket, "eth0", "free"); err != nil || addr.IsValid() {
		t.Errorf("free name: %v, %v", addr, err)
	}
	if _, err := Ask(ctx, socket, "eth9", "lab"); err == nil || err.Error() != "no such device" {
		t.Errorf("helper error: %v", err)
	}
}
