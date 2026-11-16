package app

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func recordPrivileged(t *testing.T, fail string) *[]string {
	t.Helper()
	var calls []string
	previous := privilegedCommand
	privilegedCommand = func(ctx context.Context, args ...string) ([]byte, error) {
		// Like exec.CommandContext: a cancelled context never starts the command.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		call := strings.Join(args, " ")
		calls = append(calls, call)
		if call == fail {
			return nil, errors.New("target is busy")
		}
		return nil, nil
	}
	t.Cleanup(func() { privilegedCommand = previous })
	return &calls
}

func TestCloseFreshTargetAfterCancel(t *testing.T) {
	calls := recordPrivileged(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	closeTarget := closeFreshTarget(ctx, "/mnt", "cryptroot", &out)
	cancel()
	closeTarget()
	want := []string{"umount -R -- /mnt", "cryptsetup close cryptroot"}
	if !slices.Equal(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
	if !strings.Contains(out.String(), "closed again") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestCloseFreshTargetKeepsMappingWhileMounted(t *testing.T) {
	calls := recordPrivileged(t, "umount -R -- /mnt")
	var out bytes.Buffer
	closeFreshTarget(context.Background(), "/mnt", "cryptroot", &out)()
	if want := []string{"umount -R -- /mnt"}; !slices.Equal(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
	if !strings.Contains(out.String(), "Restart before you install again") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestCloseFreshTargetRefusesOptionLikeMapping(t *testing.T) {
	calls := recordPrivileged(t, "")
	closeFreshTarget(context.Background(), "/mnt", "--help", &bytes.Buffer{})()
	if want := []string{"umount -R -- /mnt"}; !slices.Equal(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
}
