package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/nettrust"
)

// pauseAction lets a user at the machine carry a blocked network's traffic
// without the tunnel. It asks an administrator's password each time, admins
// too, and only on the local, active seat.
const pauseAction = "org.gjallaros.vpn.pause"

// PauseVPN lets the network the VPN blocks now carry the traffic without
// the tunnel for nettrust.MaxPause. The pause is for that network only:
// gjallarctl vpn apply ends it when the network changes, and suspend and
// reboot end it too.
func (s *service) PauseVPN(ctx context.Context, sender string) (nettrust.PauseState, error) {
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return nettrust.PauseState{}, err
	}
	verdict, err := s.blockedVerdict()
	if err != nil {
		return nettrust.PauseState{}, err
	}
	name, err := s.userName(who.UID)
	if err != nil {
		return nettrust.PauseState{}, err
	}
	if err := s.sys.Authorize(ctx, sender, pauseAction, map[string]string{
		"polkit.message":        pauseMessage(verdict.Blocked),
		"org.gjallaros.network": verdict.Network,
	}); err != nil {
		return nettrust.PauseState{}, err
	}

	// Asked again: the network may have changed while the dialog was open.
	again, err := s.blockedVerdict()
	if err != nil {
		return nettrust.PauseState{}, err
	}
	if again.Network != verdict.Network {
		return nettrust.PauseState{}, fmt.Errorf("%w: the network changed while the password was asked; nothing paused", errRefused)
	}
	now := s.now()
	pause := nettrust.Pause{
		Network: verdict.Network,
		User:    name,
		UID:     who.UID,
		Started: now,
		Until:   now.Add(nettrust.MaxPause),
	}
	if err := s.writePause(s.pausePath, pause); err != nil {
		return nettrust.PauseState{}, err
	}
	log.Printf("uid %d paused the VPN block (%s) on network %s until %s", who.UID, verdict.Blocked, verdict.Network, pause.Until.Format(time.DateTime))
	if err := s.sys.ReapplyVPN(ctx); err != nil {
		return nettrust.PauseState{}, fmt.Errorf("the pause is written, but the VPN decision did not run again: %w", err)
	}
	return nettrust.PauseState{User: name, Until: pause.Until, Blocked: verdict.Blocked}, nil
}

// ResumeVPN ends a pause early. Blocking again needs no password.
func (s *service) ResumeVPN(ctx context.Context, sender string) error {
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return err
	}
	if err := nettrust.RemovePause(s.pausePath); err != nil {
		return err
	}
	log.Printf("uid %d ended the VPN pause", who.UID)
	return s.sys.ReapplyVPN(ctx)
}

// blockedVerdict is root's verdict when it blocks a network a pause can
// name. A network that is already paused is refused: a pause cannot be
// made longer.
func (s *service) blockedVerdict() (nettrust.Verdict, error) {
	verdict, ok := s.sys.Verdict()
	switch {
	case !ok:
		return nettrust.Verdict{}, fmt.Errorf("%w: the VPN has no current decision for this network", errRefused)
	case verdict.Paused != nil:
		return nettrust.Verdict{}, fmt.Errorf("%w: the VPN is already paused until %s", errRefused, verdict.Paused.Until.Local().Format("15:04"))
	case verdict.Blocked == "":
		return nettrust.Verdict{}, fmt.Errorf("%w: the VPN does not block this network", errRefused)
	case verdict.Network == "":
		return nettrust.Verdict{}, fmt.Errorf("%w: this network cannot be told apart from others, so it cannot be paused", errRefused)
	}
	return verdict, nil
}

// pauseMessage is the password dialog's text. The dialog shows about six
// lines of 40 characters and cuts the rest, so it stays short.
func pauseMessage(blocked string) string {
	return fmt.Sprintf("Pause the VPN on this network? %s Apps here then skip the tunnel; this network sees where they connect. Ends after %d min, on a network change, sleep or restart.",
		nettrust.BlockCause(blocked), int(nettrust.MaxPause.Minutes()))
}
