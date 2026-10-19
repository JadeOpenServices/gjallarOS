package namecheck

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"time"
)

// SocketPath is where systemd listens for the helper; only root may
// connect.
const SocketPath = "/run/gjallar/namecheck.sock"

const (
	// maxMessage bounds what either side reads.
	maxMessage = 4 << 10
	// askTimeout covers the helper's start and its one-second probe.
	askTimeout = 5 * time.Second
)

// Question is the one line the service sends.
type Question struct {
	Name   string `json:"name"`
	Device string `json:"device"`
}

// Reply is the one line the helper answers. Taken is the address that
// answered for the name; it is empty when none did.
type Reply struct {
	Taken string `json:"taken,omitempty"`
	Error string `json:"error,omitempty"`
}

// Ask has the helper at socket probe name on device. It returns the
// address that answered, or an invalid address when none did.
func Ask(ctx context.Context, socket, device, name string) (netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("reach the name check: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(Question{Name: name, Device: device}); err != nil {
		return netip.Addr{}, fmt.Errorf("ask the name check: %w", err)
	}
	var reply Reply
	if err := readLine(conn, &reply); err != nil {
		return netip.Addr{}, fmt.Errorf("read the name check: %w", err)
	}
	if reply.Error != "" {
		return netip.Addr{}, errors.New(reply.Error)
	}
	if reply.Taken == "" {
		return netip.Addr{}, nil
	}
	addr, err := netip.ParseAddr(reply.Taken)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("the name check answered %q", reply.Taken)
	}
	return addr, nil
}

// Serve answers one question from r on w with probe.
func Serve(ctx context.Context, r io.Reader, w io.Writer, probe func(ctx context.Context, device, name string) (netip.Addr, error)) error {
	var question Question
	reply := Reply{}
	if err := readLine(r, &question); err != nil {
		reply.Error = "unreadable question"
	} else if addr, err := probe(ctx, question.Device, question.Name); err != nil {
		reply.Error = err.Error()
	} else if addr.IsValid() {
		reply.Taken = addr.String()
	}
	return json.NewEncoder(w).Encode(reply)
}

func readLine(r io.Reader, value any) error {
	line, err := bufio.NewReaderSize(io.LimitReader(r, maxMessage), maxMessage).ReadSlice('\n')
	if err != nil {
		return err
	}
	return json.Unmarshal(line, value)
}
