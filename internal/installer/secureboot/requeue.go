package secureboot

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"strings"
)

// clearForeignMokNew looks for enroll requests other than the gjallarOS key
// (L97). MokManager enrolls everything in MokNew at once, and mokutil
// --import adds to a request that already waits and sets a new password
// over all of it, so a key another system queued earlier would be trusted
// together with the gjallarOS key. The user may drop them all (mokutil
// --revoke-import); gjallarOS queues its own key again after that.
func clearForeignMokNew(ctx context.Context, ui UI, out io.Writer, f firmwareFiles, current []byte) error {
	raw, present, err := f.shimRequest("MokNew")
	if err != nil || !present {
		return err
	}
	lists, err := signatureLists(raw)
	if err != nil {
		return fmt.Errorf("MokNew: %w", err)
	}
	var foreign []string
	for _, l := range lists {
		for _, cert := range l.Certs {
			if !bytes.Equal(cert.der, current) {
				foreign = append(foreign, "the key "+entryName(cert.der))
			}
		}
		for _, sum := range l.FileSums {
			foreign = append(foreign, "the file hash "+spacedHex(sum[:16]))
		}
		for range l.Hashes - len(l.FileSums) {
			foreign = append(foreign, "a certificate hash")
		}
		for range l.Other {
			foreign = append(foreign, "an entry of an unknown type")
		}
	}
	if len(foreign) == 0 {
		return nil
	}
	fmt.Fprintln(out, "MokManager also holds requests to trust entries that gjallarOS did not ask for:")
	for _, s := range foreign {
		fmt.Fprintln(out, "  "+s)
	}
	fmt.Fprintln(out, "MokManager would trust them together with the gjallarOS key.")
	drop, err := ui.Confirm(ctx, "Drop these requests? gjallarOS then asks for its own key again.", false)
	if err != nil {
		return err
	}
	if !drop {
		return errors.New("MokManager holds requests to trust entries that gjallarOS did not make; finish them first or drop them with mokutil --revoke-import")
	}
	if err := run(ctx, "sudo", privilegedTool("mokutil"), "--revoke-import"); err != nil {
		return fmt.Errorf("drop the enroll requests: %w", err)
	}
	if _, present, err := f.shimRequest("MokNew"); err != nil || present {
		if err == nil {
			err = errors.New("mokutil returned, but the enroll requests are still there")
		}
		return err
	}
	return nil
}

// mokNewOnly checks after queueing that MokNew asks to trust der and
// nothing else. When something else landed there meanwhile it drops the
// whole request, so the password the user just chose confirms nothing.
func mokNewOnly(ctx context.Context, f firmwareFiles, der []byte) error {
	raw, present, err := f.shimRequest("MokNew")
	if err != nil {
		return err
	}
	lists, err := signatureLists(raw)
	if err != nil {
		return fmt.Errorf("MokNew: %w", err)
	}
	if !present || len(lists) != 1 || len(lists[0].Certs) != 1 || lists[0].Hashes != 0 || lists[0].Other != 0 || !bytes.Equal(lists[0].Certs[0].der, der) {
		if err := run(context.WithoutCancel(ctx), "sudo", privilegedTool("mokutil"), "--revoke-import"); err != nil {
			return fmt.Errorf("MokNew holds more than the gjallarOS key, and dropping the request failed: %w", err)
		}
		return errors.New("MokNew held more than the gjallarOS key, so the request was dropped; start again")
	}
	return nil
}

// queueVisit queues one MokManager visit for der: the key itself unless
// shim lists it, older gjallarOS keys and the installer stick's own entry.
// A fresh install runs it, and the installer stick runs it again when that
// visit was missed (Q234). MokManager waits for a key press when anything
// was queued.
func queueVisit(ctx context.Context, ui UI, out io.Writer, der []byte) (mokVisit, error) {
	var visit mokVisit
	if err := clearForeignMokNew(ctx, ui, out, liveFirmware, der); err != nil {
		return visit, err
	}
	listed, err := mokHolds(liveFirmware, "MokListRT", der)
	if err != nil {
		return visit, err
	}
	if !listed {
		visit.key = der
		if visit.keyReused, err = mokHolds(liveFirmware, "MokNew", der); err != nil {
			return visit, err
		}
		if visit.keyReused {
			fmt.Fprintln(out, "The gjallarOS key already waits for MokManager.")
		} else if err := queueMOK(ctx, der, out); err != nil {
			return visit, err
		}
		if err := mokNewOnly(ctx, liveFirmware, der); err != nil {
			return visit, err
		}
	}
	choose := "Choose a one-time password for MokManager. MokManager reads the keyboard as US layout; letters and digits are safe."
	again := "Type the same password again: MokManager asks for it once more to confirm the deletions."
	prompted := visit.key != nil && !visit.keyReused
	password := func() string {
		if prompted {
			return again
		}
		return choose
	}
	if err := clearForeignMokDel(ctx, ui, out, liveFirmware, liveStickCopy, der); err != nil {
		return visit, err
	}
	oldQueued, err := queueOldKeys(ctx, ui, out, liveFirmware, der, password())
	if err != nil {
		return visit, err
	}
	prompted = prompted || oldQueued
	stick, err := queueStickDelete(ctx, liveFirmware, liveStickCopy, out, password())
	if err != nil {
		return visit, err
	}
	if visit.del, err = readMokDel(liveFirmware); err != nil {
		return visit, err
	}
	// MokManager checks the password of the last mokutil delete call.
	visit.delReused = !oldQueued && (stick == nil || stick.reused)
	if visit.key != nil || len(visit.del) > 0 {
		if err := holdMokManager(ctx, liveFirmware); err != nil {
			return visit, err
		}
	}
	return visit, nil
}

// RequeueKey runs on the installer stick when the MokManager visit after an
// install was missed (Q234, Plane 370): shim dropped the requests, so the
// installed gjallarOS does not start with Secure Boot on. der is the
// installed system's key. The caller reads it from the installed root (R48):
// on an encrypted install the user types the recovery key and the root is
// opened read-only; on an unencrypted one it is read from the root as it is,
// with a warning (Q241). The fingerprint printed here lets the user compare
// it before MokManager asks. It queues the same visit the install queued and prints the steps.
// It reports false when nothing needed queueing.
func RequeueKey(ctx context.Context, ui UI, out io.Writer, der []byte) (bool, error) {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return false, fmt.Errorf("the key of the installed gjallarOS does not parse: %w", err)
	}
	if !strings.HasPrefix(cert.Subject.CommonName, ourKeyName) {
		return false, fmt.Errorf("the key of the installed gjallarOS is named %q; gjallarOS names its keys %q", cert.Subject.CommonName, ourKeyName)
	}
	started, err := ShimStarted()
	if err != nil {
		return false, fmt.Errorf("read shim's key list: %w", err)
	}
	if !started {
		return false, errors.New("this start did not go through shim, so MokManager cannot run; start the installer stick with Secure Boot on")
	}
	trust, err := ReadFirmwareTrust()
	if err != nil {
		return false, fmt.Errorf("read firmware trust: %w", err)
	}
	if trust.ShimValidationOff {
		return false, errors.New("shim's own check is switched off in MokManager; turn it back on there first")
	}
	fmt.Fprintf(out, "The installed gjallarOS has the key %q, fingerprint\n  %s\n", certName(cert), sha1Fingerprint(der))
	visit, err := queueVisit(ctx, ui, out, der)
	if err != nil {
		return false, err
	}
	if visit.key == nil && len(visit.del) == 0 {
		fmt.Fprintln(out, "shim already trusts this key and nothing waits for MokManager.")
		return false, nil
	}
	visit.retry = "start the installer stick again and queue the key once more."
	fmt.Fprint(out, visit.steps())
	return true, nil
}
