package devgate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// SocketPath is where systemd listens for gjallar-devproof; only root may
// connect.
const SocketPath = "/run/gjallar/devproof.sock"

const (
	// maxMessage bounds what either side reads. A recovery key is 71
	// characters; a PIN is shorter.
	maxMessage = 1 << 10
	// askTimeout covers the helper's start and one key slot test, which
	// runs the key derivation (about a second or two on a laptop).
	askTimeout = 30 * time.Second
)

// Question is the one line the settings service sends.
type Question struct {
	Kind   string `json:"kind"`
	Secret []byte `json:"secret"`
}

// Reply is the one line the helper answers. OK means the proof opened the
// system disk. PINLeft is how many PIN tries the gate has left in its
// window; Retry is when the next PIN try is allowed once none are left.
type Reply struct {
	OK      bool      `json:"ok"`
	PINLeft int       `json:"pinLeft"`
	Retry   time.Time `json:"retry,omitzero"`
	Error   string    `json:"error,omitempty"`
}

// ErrWrong is returned by Ask for a proof that did not open the disk.
var ErrWrong = errors.New("wrong")

// Ask has the helper at socket test one proof of kind (ProofPIN or
// ProofRecovery).
func Ask(ctx context.Context, socket, kind string, secret []byte) (Reply, error) {
	ctx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return Reply{}, fmt.Errorf("reach the proof check: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	line, err := json.Marshal(Question{Kind: kind, Secret: secret})
	if err != nil {
		return Reply{}, err
	}
	_, err = conn.Write(append(line, '\n'))
	clear(line)
	if err != nil {
		return Reply{}, fmt.Errorf("ask the proof check: %w", err)
	}
	var reply Reply
	if err := readLine(conn, &reply); err != nil {
		return Reply{}, fmt.Errorf("read the proof check: %w", err)
	}
	if reply.Error != "" {
		return reply, errors.New(reply.Error)
	}
	if !reply.OK {
		return reply, ErrWrong
	}
	return reply, nil
}

// Serve answers one question from r on w with check.
func Serve(ctx context.Context, r io.Reader, w io.Writer, check func(ctx context.Context, kind string, secret []byte) Reply) error {
	var question Question
	var reply Reply
	if err := readLine(r, &question); err != nil {
		reply.Error = "unreadable question"
	} else {
		reply = check(ctx, question.Kind, question.Secret)
	}
	clear(question.Secret)
	return json.NewEncoder(w).Encode(reply)
}

func readLine(r io.Reader, value any) error {
	reader := bufio.NewReaderSize(io.LimitReader(r, maxMessage), maxMessage)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return err
	}
	err = json.Unmarshal(line, value)
	clear(line)
	return err
}
