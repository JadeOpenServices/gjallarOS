// Command gjallar-devproof answers one question from the settings service:
// does this startup PIN or recovery key open the system disk? systemd
// starts it per connection, one at a time, with the socket as stdin and
// stdout. It is apart from the settings service so that service keeps no
// access to disks or the security chip (Plane 283).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/devgate"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
)

// budgetPath sits in the unit's StateDirectory.
const budgetPath = "/var/lib/gjallar-devproof/budget.json"

// wrongKeyDelay slows down guessing recovery keys, like the USB disarm.
const wrongKeyDelay = 3 * time.Second

func main() {
	log.SetFlags(0)
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: gjallar-devproof (started by systemd for the settings service)")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	c := checker{
		budgetPath:   budgetPath,
		now:          time.Now,
		sleep:        time.Sleep,
		device:       diskcrypto.RootDevice,
		keyslots:     diskcrypto.ReadKeyslots,
		tpmAvailable: diskcrypto.TPMAvailable,
		testPIN:      diskcrypto.TestTPM2PIN,
		testSlot:     diskcrypto.TestKeySlot,
	}
	if err := devgate.Serve(ctx, os.Stdin, os.Stdout, c.check); err != nil {
		log.Fatal(err)
	}
}

type checker struct {
	budgetPath   string
	now          func() time.Time
	sleep        func(time.Duration)
	device       func(context.Context) (string, error)
	keyslots     func(context.Context, string) (diskcrypto.Keyslots, error)
	tpmAvailable func() bool
	testPIN      func(ctx context.Context, device, tokenID string, pin []byte) error
	testSlot     func(ctx context.Context, device string, slot int, key []byte) error
}

func (c checker) check(ctx context.Context, kind string, secret []byte) devgate.Reply {
	now := c.now()
	budget, err := devgate.LoadBudget(c.budgetPath, now)
	if err != nil {
		log.Print(err)
	}
	left, retry := budget.Left(now)
	reply := devgate.Reply{PINLeft: left, Retry: retry}
	if kind != devgate.ProofPIN && kind != devgate.ProofRecovery {
		reply.Error = "unknown proof"
		return reply
	}
	if len(secret) == 0 {
		reply.Error = "empty proof"
		return reply
	}
	device, err := c.device(ctx)
	if err == nil && device == "" {
		err = fmt.Errorf("the system disk is not encrypted")
	}
	if err != nil {
		log.Printf("system disk: %v", err)
		reply.Error = "the system disk cannot be checked: " + err.Error()
		return reply
	}
	slots, err := c.keyslots(ctx, device)
	if err != nil {
		log.Printf("keyslots: %v", err)
		reply.Error = "the system disk cannot be checked"
		return reply
	}

	if kind == devgate.ProofRecovery {
		if len(slots.Recovery) == 0 {
			reply.Error = "the system disk has no recovery key"
			return reply
		}
		for _, slot := range slots.Recovery {
			if c.testSlot(ctx, device, slot, secret) == nil {
				log.Print("recovery key accepted")
				reply.OK = true
				return reply
			}
		}
		log.Print("wrong recovery key")
		c.sleep(wrongKeyDelay)
		return reply
	}

	if !slots.TPM2PIN || len(slots.TPM2) != 1 {
		reply.Error = "the system disk has no startup PIN; use the recovery key"
		return reply
	}
	if !c.tpmAvailable() {
		reply.Error = "the security chip is not available; use the recovery key"
		return reply
	}
	if left == 0 {
		reply.Error = "too many wrong PINs; use the recovery key or wait"
		return reply
	}
	// Count the try before it runs, so stopping the helper midway does
	// not give it back.
	budget.Fail(now)
	if err := devgate.SaveBudget(c.budgetPath, budget); err != nil {
		log.Print(err)
		reply.Error = "the PIN check cannot keep count; use the recovery key"
		return reply
	}
	if c.testPIN(ctx, device, slots.TPM2Token[slots.TPM2[0]], secret) == nil {
		budget.Undo()
		if err := devgate.SaveBudget(c.budgetPath, budget); err != nil {
			log.Print(err)
		}
		log.Print("startup PIN accepted")
		reply.OK = true
		reply.PINLeft, reply.Retry = budget.Left(now)
		return reply
	}
	log.Print("wrong startup PIN")
	reply.PINLeft, reply.Retry = budget.Left(now)
	return reply
}
