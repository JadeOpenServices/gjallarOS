package main

import (
	"context"
	"errors"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	notifyName = "org.freedesktop.Notifications"
	notifyPath = dbus.ObjectPath("/org/freedesktop/Notifications")
)

// desktopNotice is a notification that stays until it is answered.
type desktopNotice struct {
	App     string
	Icon    string
	Summary string
	// Body may carry notification markup; escape untrusted strings.
	Body    string
	Actions []noticeAction
}

type noticeAction struct{ Key, Label string }

// showNotice posts n and waits for an answer: the chosen action key, or ""
// when the notice was dismissed. The notice lives only as long as this call.
// It is closed when ctx ends, so a stopped or restarted caller leaves no card
// behind, and posted again when the notification server restarts and forgets
// it. notify-send could do neither: systemd killed it together with its
// parent, and it waited forever on a card the new server never knew.
func showNotice(ctx context.Context, n desktopNotice) (string, error) {
	// Not tied to ctx: the card must still be closed after ctx ends.
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return "", err
	}
	defer conn.Close()
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)
	if err := conn.AddMatchSignal(dbus.WithMatchObjectPath(notifyPath), dbus.WithMatchInterface(notifyName)); err != nil {
		return "", err
	}
	if err := conn.AddMatchSignal(dbus.WithMatchSender("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, notifyName)); err != nil {
		return "", err
	}

	server := conn.Object(notifyName, notifyPath)
	var actions []string
	for _, a := range n.Actions {
		actions = append(actions, a.Key, a.Label)
	}
	var id uint32
	var owner string
	// Calls are not cut short by ctx: a card the server made while ctx ended
	// still has to be closed, so post must learn its id.
	call := func(obj dbus.BusObject, method string, args ...any) *dbus.Call {
		callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return obj.CallWithContext(callCtx, method, 0, args...)
	}
	post := func() error {
		owner = ""
		if err := call(conn.BusObject(), "org.freedesktop.DBus.GetNameOwner", notifyName).Store(&owner); err != nil {
			return err
		}
		return call(server, notifyName+".Notify",
			n.App, uint32(0), n.Icon, n.Summary, n.Body, actions, map[string]dbus.Variant{}, int32(0)).Store(&id)
	}
	closeCard := func() { _ = call(server, notifyName+".CloseNotification", id).Err }
	posted := true
	if err := post(); err != nil {
		// Without a server (the shell is restarting) wait for the next one.
		if owner != "" {
			return "", err
		}
		posted = false
	}
	for {
		select {
		case <-ctx.Done():
			if posted {
				closeCard()
			}
			return "", ctx.Err()
		case s, ok := <-signals:
			if !ok {
				return "", errors.New("session bus closed")
			}
			if s.Name == "org.freedesktop.DBus.NameOwnerChanged" {
				if len(s.Body) == 3 {
					posted = s.Body[2] != "" && post() == nil
				}
				continue
			}
			// Only the server may answer; other clients can emit the same signals.
			if !posted || s.Sender != owner || len(s.Body) != 2 || s.Body[0] != id {
				continue
			}
			switch s.Name {
			case notifyName + ".ActionInvoked":
				key, _ := s.Body[1].(string)
				closeCard()
				return key, nil
			case notifyName + ".NotificationClosed":
				return "", nil
			}
		}
	}
}
