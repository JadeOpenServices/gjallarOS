package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// recoveryKeyboardFile is the only per-machine fact the recovery image
// carries. The partition is unencrypted, so the image gets the keyboard and
// nothing else; the answers stay inside the encrypted root (L78). flake.nix
// reads this file and checks it again with the same patterns.
const recoveryKeyboardFile = "generated/recovery-keyboard.json"

var (
	recoveryLayout  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	recoveryVariant = regexp.MustCompile(`^[A-Za-z0-9_-]{0,48}$`)
)

type recoveryKeyboard struct {
	Layout  string `json:"layout"`
	Variant string `json:"variant"`
}

// recoveryKeyboardFor picks the console keyboard for the recovery image: the
// first layout of a list ("us,de" gives "us") with its own variant. An empty
// layout gives "us".
func recoveryKeyboardFor(layout, variant string) (recoveryKeyboard, error) {
	first, _, _ := strings.Cut(strings.TrimSpace(layout), ",")
	firstVariant, _, _ := strings.Cut(strings.TrimSpace(variant), ",")
	if first == "" {
		return recoveryKeyboard{Layout: "us"}, nil
	}
	if !recoveryLayout.MatchString(first) {
		return recoveryKeyboard{}, fmt.Errorf("keyboard layout %q cannot be used on the recovery console", first)
	}
	if !recoveryVariant.MatchString(firstVariant) {
		return recoveryKeyboard{}, fmt.Errorf("keyboard variant %q cannot be used on the recovery console", firstVariant)
	}
	return recoveryKeyboard{Layout: first, Variant: firstVariant}, nil
}

// writeRecoveryKeyboard writes the keyboard file into a staged source. The
// staged copy belongs to this process, but an old file copied from the
// checkout is replaced, never followed.
func writeRecoveryKeyboard(sourceDir string, kb recoveryKeyboard) error {
	data, err := json.Marshal(kb)
	if err != nil {
		return err
	}
	// The stage lives in a private temporary folder, but it copies links
	// from the checkout as links, and this may run as root
	// (installer-resume): a linked generated/ is refused, never followed.
	dir := filepath.Join(sourceDir, "generated")
	if err := os.Mkdir(dir, 0o755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("recovery keyboard: %w", err)
	}
	if info, err := os.Lstat(dir); err != nil {
		return fmt.Errorf("recovery keyboard: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("recovery keyboard: %s is not a real folder", dir)
	}
	path := filepath.Join(sourceDir, recoveryKeyboardFile)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("recovery keyboard: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return fmt.Errorf("recovery keyboard: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("recovery keyboard: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("recovery keyboard: %w", err)
	}
	return nil
}
