package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/broker"
)

const (
	DefaultSocketMode     = 0660
	DefaultRequestTimeout = 5 * time.Second
)

type Server struct {
	SocketPath string
	SocketMode os.FileMode
	// SocketOwner, when set, owns the socket, so the owner can connect
	// without a group that only applies at the next login.
	SocketOwner    *uint32
	RequestTimeout time.Duration
	Handler        broker.Handler

	OnError func(error)
}

func (s Server) Serve(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf(
			"USB trust daemon context is nil",
		)
	}

	if err := s.validate(); err != nil {
		return err
	}

	if err := prepareSocketPath(
		s.SocketPath,
	); err != nil {
		return err
	}

	address := &net.UnixAddr{
		Name: s.SocketPath,
		Net:  "unix",
	}

	listener, err := net.ListenUnix(
		"unix",
		address,
	)
	if err != nil {
		return fmt.Errorf(
			"listen on USB trust socket: %w",
			err,
		)
	}

	defer func() {
		_ = listener.Close()
		removeSocket(s.SocketPath)
	}()

	if s.SocketOwner != nil {
		if err := os.Chown(
			s.SocketPath,
			int(*s.SocketOwner),
			-1,
		); err != nil {
			return fmt.Errorf(
				"set USB trust socket owner: %w",
				err,
			)
		}
	}

	if err := os.Chmod(
		s.SocketPath,
		s.socketMode(),
	); err != nil {
		return fmt.Errorf(
			"set USB trust socket permissions: %w",
			err,
		)
	}

	stop := make(chan struct{})

	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()

		case <-stop:
		}
	}()

	defer close(stop)

	var connections sync.WaitGroup

	defer connections.Wait()

	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil ||
				errors.Is(err, net.ErrClosed) {
				return nil
			}

			return fmt.Errorf(
				"accept USB trust connection: %w",
				err,
			)
		}

		connections.Add(1)

		go func(conn *net.UnixConn) {
			defer connections.Done()
			defer conn.Close()

			deadline := time.Now().Add(
				s.requestTimeout(),
			)

			if err := conn.SetDeadline(
				deadline,
			); err != nil {
				s.report(fmt.Errorf(
					"set USB trust connection deadline: %w",
					err,
				))
				return
			}

			requestCtx, cancel := context.WithDeadline(ctx, deadline)
			defer cancel()
			if err := s.Handler.ServeConn(
				requestCtx,
				conn,
			); err != nil {
				s.report(fmt.Errorf(
					"serve USB trust connection: %w",
					err,
				))
			}
		}(conn)
	}
}

func (s Server) validate() error {
	if s.SocketPath == "" {
		return fmt.Errorf(
			"USB trust socket path is required",
		)
	}

	if !filepath.IsAbs(s.SocketPath) {
		return fmt.Errorf(
			"USB trust socket path must be absolute",
		)
	}

	if s.requestTimeout() <= 0 {
		return fmt.Errorf(
			"USB trust request timeout must be positive",
		)
	}

	return nil
}

func (s Server) socketMode() os.FileMode {
	if s.SocketMode == 0 {
		return DefaultSocketMode
	}

	return s.SocketMode
}

func (s Server) requestTimeout() time.Duration {
	if s.RequestTimeout == 0 {
		return DefaultRequestTimeout
	}

	return s.RequestTimeout
}

func (s Server) report(err error) {
	if s.OnError != nil {
		s.OnError(err)
	}
}

func prepareSocketPath(path string) error {
	parent := filepath.Dir(path)

	info, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf(
			"inspect USB trust runtime directory: %w",
			err,
		)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(
			"USB trust runtime directory is a symlink",
		)
	}

	if !info.IsDir() {
		return fmt.Errorf(
			"USB trust runtime path is not a directory",
		)
	}

	existing, err := os.Lstat(path)
	switch {
	case os.IsNotExist(err):
		return nil

	case err != nil:
		return fmt.Errorf(
			"inspect existing USB trust socket: %w",
			err,
		)

	case existing.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf(
			"refusing to replace symlink at USB trust socket path",
		)

	case existing.Mode()&os.ModeSocket == 0:
		return fmt.Errorf(
			"refusing to replace non-socket at USB trust socket path",
		)
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf(
			"remove stale USB trust socket: %w",
			err,
		)
	}

	return nil
}

func removeSocket(path string) {
	info, err := os.Lstat(path)
	if err != nil {
		return
	}

	if info.Mode()&os.ModeSocket == 0 {
		return
	}

	_ = os.Remove(path)
}
