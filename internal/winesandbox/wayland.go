package winesandbox

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// SandboxEngine names the sandbox to the compositor.
const SandboxEngine = "org.gjallaros.sandbox"

const securityContextManager = "wp_security_context_manager_v1"

// SecurityContext is a Wayland socket whose clients the compositor treats
// as sandboxed: no screen capture, virtual input, clipboard snooping or
// lock screen. The plain compositor socket offers all of those, so the
// program only ever sees this one. The socket works while the
// SecurityContext is open.
type SecurityContext struct {
	conn  *net.UnixConn
	close *os.File
	path  string
}

// NewSecurityContext asks the compositor at display for a sandboxed socket
// at path. path's directory must be private to this user. It fails when
// the compositor does not offer wp_security_context_v1; the program then
// gets no display at all.
func NewSecurityContext(display, path, appID, instanceID string) (sc *SecurityContext, err error) {
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: display, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("connect to the compositor: %w", err)
	}
	defer func() {
		if err != nil {
			conn.Close()
		}
	}()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return nil, err
	}
	w := &wlConn{conn: conn}

	// wl_display (1).get_registry -> 2, then .sync -> 3 to know when the
	// list of globals is complete.
	if err := w.send(1, 1, u32(2), nil); err != nil {
		return nil, err
	}
	if err := w.send(1, 0, u32(3), nil); err != nil {
		return nil, err
	}
	var name uint32
	found := false
	for {
		obj, op, body, err := w.next()
		if err != nil {
			return nil, err
		}
		if obj == 3 {
			break
		}
		if obj == 2 && op == 0 && len(body) >= 8 {
			iface, _ := wlString(body[4:])
			if iface == securityContextManager {
				name = binary.NativeEndian.Uint32(body)
				found = true
			}
		}
	}
	if !found {
		return nil, errors.New("the compositor offers no Wayland security context")
	}

	// wl_registry.bind(name, interface, version 1) -> 4.
	bind := append(u32(name), wlArg(securityContextManager)...)
	bind = append(bind, u32(1)...)
	bind = append(bind, u32(4)...)
	if err := w.send(2, 0, bind, nil); err != nil {
		return nil, err
	}

	listenFD, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(listenFD)
	if err := unix.Unlink(path); err != nil && !errors.Is(err, unix.ENOENT) {
		return nil, fmt.Errorf("remove old socket %s: %w", path, err)
	}
	if err := unix.Bind(listenFD, &unix.SockaddrUnix{Name: path}); err != nil {
		return nil, fmt.Errorf("bind %s: %w", path, err)
	}
	defer func() {
		if err != nil {
			os.Remove(path)
		}
	}()
	if err := unix.Listen(listenFD, 16); err != nil {
		return nil, err
	}
	// The compositor serves the socket until the pipe's write end closes.
	pipe := make([]int, 2)
	if err := unix.Pipe2(pipe, unix.O_CLOEXEC); err != nil {
		return nil, err
	}
	closeRead, closeWrite := pipe[0], os.NewFile(uintptr(pipe[1]), "security context")
	defer unix.Close(closeRead)
	defer func() {
		if err != nil {
			closeWrite.Close()
		}
	}()

	// wp_security_context_manager_v1(4).create_listener -> 5, then set
	// the engine, app id and instance id and commit.
	if err := w.send(4, 1, u32(5), []int{listenFD, closeRead}); err != nil {
		return nil, err
	}
	for i, value := range []string{SandboxEngine, appID, instanceID} {
		if err := w.send(5, uint16(i+1), wlArg(value), nil); err != nil {
			return nil, err
		}
	}
	if err := w.send(5, 4, nil, nil); err != nil {
		return nil, err
	}
	if err := w.send(1, 0, u32(6), nil); err != nil {
		return nil, err
	}
	for {
		obj, _, _, err := w.next()
		if err != nil {
			return nil, err
		}
		if obj == 6 {
			break
		}
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return &SecurityContext{conn: conn, close: closeWrite, path: path}, nil
}

// Close stops the socket and removes it.
func (sc *SecurityContext) Close() error {
	err := errors.Join(sc.close.Close(), sc.conn.Close())
	if rmErr := os.Remove(sc.path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		err = errors.Join(err, rmErr)
	}
	return err
}

type wlConn struct {
	conn *net.UnixConn
	buf  []byte
}

func (w *wlConn) send(obj uint32, op uint16, body []byte, fds []int) error {
	msg := make([]byte, 8, 8+len(body))
	binary.NativeEndian.PutUint32(msg, obj)
	binary.NativeEndian.PutUint32(msg[4:], uint32(8+len(body))<<16|uint32(op))
	msg = append(msg, body...)
	var oob []byte
	if len(fds) > 0 {
		oob = unix.UnixRights(fds...)
	}
	if _, _, err := w.conn.WriteMsgUnix(msg, oob, nil); err != nil {
		return fmt.Errorf("talk to the compositor: %w", err)
	}
	return nil
}

// next returns the next event. A wl_display.error ends the talk.
func (w *wlConn) next() (obj uint32, op uint16, body []byte, err error) {
	for {
		if len(w.buf) >= 8 {
			size := int(binary.NativeEndian.Uint32(w.buf[4:]) >> 16)
			if size < 8 {
				return 0, 0, nil, errors.New("the compositor sent a broken message")
			}
			if len(w.buf) >= size {
				obj = binary.NativeEndian.Uint32(w.buf)
				op = uint16(binary.NativeEndian.Uint32(w.buf[4:]))
				body = bytes.Clone(w.buf[8:size])
				w.buf = w.buf[size:]
				if obj == 1 && op == 0 {
					msg := ""
					if len(body) > 8 {
						msg, _ = wlString(body[8:])
					}
					return 0, 0, nil, fmt.Errorf("the compositor refused: %s", msg)
				}
				return obj, op, body, nil
			}
		}
		chunk := make([]byte, 4096)
		n, err := w.conn.Read(chunk)
		if err != nil {
			return 0, 0, nil, fmt.Errorf("read from the compositor: %w", err)
		}
		w.buf = append(w.buf, chunk[:n]...)
	}
}

func u32(v uint32) []byte {
	return binary.NativeEndian.AppendUint32(nil, v)
}

// wlArg encodes a Wayland string: length with the NUL, bytes, padding.
func wlArg(s string) []byte {
	out := u32(uint32(len(s) + 1))
	out = append(out, s...)
	out = append(out, 0)
	for len(out)%4 != 0 {
		out = append(out, 0)
	}
	return out
}

func wlString(b []byte) (string, bool) {
	if len(b) < 4 {
		return "", false
	}
	n := int(binary.NativeEndian.Uint32(b))
	if n == 0 || len(b) < 4+n {
		return "", false
	}
	return string(b[4 : 4+n-1]), true
}
