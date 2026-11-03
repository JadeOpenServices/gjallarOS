package winesandbox

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Watch reports when the rules may be out of date: the device's links,
// addresses, routes or neighbours changed, or the file at trustPath was
// written, replaced or removed. Changes that come while the caller is busy
// add up to one report. Watch runs in the device's own network namespace.
// It sends on errs and stops when it can no longer see changes; the rules
// would then go stale.
func Watch(ctx context.Context, trustPath string) (changed <-chan struct{}, errs <-chan error, err error) {
	link, err := netlinkWatch()
	if err != nil {
		return nil, nil, err
	}
	trust, err := inotifyWatch(filepath.Dir(trustPath))
	if err != nil {
		link.Close()
		return nil, nil, err
	}
	out := make(chan struct{}, 1)
	errc := make(chan error, 2)
	notify := func() {
		select {
		case out <- struct{}{}:
		default:
		}
	}
	go func() {
		<-ctx.Done()
		link.Close()
		trust.Close()
	}()
	go readNetlink(ctx, link, notify, errc)
	go readInotify(ctx, trust, filepath.Base(trustPath), notify, errc)
	return out, errc, nil
}

func netlinkWatch() (*os.File, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.NETLINK_ROUTE)
	if err != nil {
		return nil, fmt.Errorf("netlink: %w", err)
	}
	groups := uint32(unix.RTMGRP_LINK | unix.RTMGRP_NEIGH |
		unix.RTMGRP_IPV4_IFADDR | unix.RTMGRP_IPV6_IFADDR |
		unix.RTMGRP_IPV4_ROUTE | unix.RTMGRP_IPV6_ROUTE)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: groups}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("netlink: %w", err)
	}
	return os.NewFile(uintptr(fd), "netlink"), nil
}

func inotifyWatch(dir string) (*os.File, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, fmt.Errorf("inotify: %w", err)
	}
	mask := uint32(unix.IN_CLOSE_WRITE | unix.IN_MOVED_TO | unix.IN_MOVED_FROM |
		unix.IN_CREATE | unix.IN_DELETE | unix.IN_ATTRIB | unix.IN_DELETE_SELF | unix.IN_MOVE_SELF)
	if _, err := unix.InotifyAddWatch(fd, dir, mask); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("watch %s: %w", dir, err)
	}
	return os.NewFile(uintptr(fd), "inotify"), nil
}

// readNetlink reports every message. A full queue (ENOBUFS) means some
// were lost; the report covers them, since the caller rereads everything.
func readNetlink(ctx context.Context, f *os.File, notify func(), errc chan<- error) {
	buf := make([]byte, 1<<16)
	for {
		_, err := f.Read(buf)
		if ctx.Err() != nil {
			return
		}
		if err != nil && !errors.Is(err, unix.ENOBUFS) {
			errc <- fmt.Errorf("netlink: %w", err)
			return
		}
		notify()
	}
}

// readInotify reports events for name. The directory itself going away
// ends the watch.
func readInotify(ctx context.Context, f *os.File, name string, notify func(), errc chan<- error) {
	buf := make([]byte, 4096)
	for {
		n, err := f.Read(buf)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			errc <- fmt.Errorf("inotify: %w", err)
			return
		}
		for off := 0; off+unix.SizeofInotifyEvent <= n; {
			// struct inotify_event: wd, mask, cookie, len, name.
			mask := binary.NativeEndian.Uint32(buf[off+4:])
			end := off + unix.SizeofInotifyEvent + int(binary.NativeEndian.Uint32(buf[off+12:]))
			if end > n {
				break
			}
			evName := cString(buf[off+unix.SizeofInotifyEvent : end])
			off = end
			switch {
			case mask&(unix.IN_DELETE_SELF|unix.IN_MOVE_SELF|unix.IN_IGNORED) != 0:
				notify()
				errc <- errors.New("the trust file's folder went away")
				return
			case mask&unix.IN_Q_OVERFLOW != 0, evName == name:
				notify()
			}
		}
	}
}

func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
