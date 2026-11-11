package app

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
)

// freshTargetStatePaths are host paths whose contents belong to the installed
// system. On live media the installer provisions Secure Boot keys and
// ownership (/var/lib/sbctl, /var/lib/gjallarOS) before the target exists,
// and the Secure Boot continuation after nixos-install writes markers there
// and runs sbctl verify against /boot. All of that landed in the live RAM
// root: Lanzaboote inside nixos-install found no db.pem and the keys would
// have been lost at reboot (e2e-target, 2026-09-29).
var freshTargetStatePaths = []struct {
	path string
	copy bool // seed the target from state the live host already holds
}{
	{"/var/lib/sbctl", true},
	{"/var/lib/gjallarOS", true},
	{"/boot", false},
}

// bindFreshTargetState copies live state into targetRoot without replacing
// anything already staged there, then bind-mounts the target paths over the
// host paths so every later step reads and writes the installed system. The
// returned release unmounts in reverse order.
func bindFreshTargetState(ctx context.Context, targetRoot string, out io.Writer) (func(), error) {
	var mounted []string
	// Release must also run once an interrupt cancelled ctx: the binds sit
	// over the live host's /boot and /var/lib.
	cleanupCtx := context.WithoutCancel(ctx)
	release := func() {
		for i := len(mounted) - 1; i >= 0; i-- {
			_, _ = privilegedCommand(cleanupCtx, "umount", "--", mounted[i])
		}
		mounted = nil
	}

	for _, state := range freshTargetStatePaths {
		target := filepath.Join(targetRoot, state.path)
		if _, err := privilegedCommand(ctx, "mkdir", "-p", "--", target, state.path); err != nil {
			release()
			return nil, fmt.Errorf("prepare target state %s: %w", target, err)
		}
		if state.copy {
			if _, err := privilegedCommand(
				ctx, "cp", "-a", "--update=none", "--", state.path+"/.", target+"/",
			); err != nil {
				release()
				return nil, fmt.Errorf("copy live %s into target: %w", state.path, err)
			}
		}
		if _, err := privilegedCommand(ctx, "mount", "--bind", "--", target, state.path); err != nil {
			release()
			return nil, fmt.Errorf("bind target %s over %s: %w", target, state.path, err)
		}
		mounted = append(mounted, state.path)
	}

	fmt.Fprintln(out, "PASS: Secure Boot and gjallarOS state now live on the target (/var/lib/sbctl, /var/lib/gjallarOS, /boot)")
	return release, nil
}
