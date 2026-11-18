package oldroot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCryptsetup puts a cryptsetup on PATH that acts as mode says on open
// and logs each close. "hang" makes the mapping first, as a slow open could
// just before it is stopped, and ignores the polite stop.
func fakeCryptsetup(t *testing.T, mode string) (log string) {
	t.Helper()
	dir := t.TempDir()
	mapperDir = filepath.Join(dir, "mapper")
	if err := os.Mkdir(mapperDir, 0o755); err != nil {
		t.Fatal(err)
	}
	log = filepath.Join(dir, "log")
	sysBlockDir := filepath.Join(dir, "block")
	script := `#!/bin/sh
case "$1" in
open)
	cat >/dev/null
	case "` + mode + `" in
	hang) trap '' TERM; : >"` + mapperDir + `/` + Mapping + `"; exec sleep 30 ;;
	hangkernel) trap '' TERM; mkdir -p "` + sysBlockDir + `/dm-7/dm"; echo ` + Mapping + ` >"` + sysBlockDir + `/dm-7/dm/name"; exec sleep 30 ;;
	ok) : >"` + mapperDir + `/` + Mapping + `" ;;
	wrong) exit 2 ;;
	esac ;;
close) echo "close $3" >>"` + log + `"; rm -rf "` + mapperDir + `/$3" "` + sysBlockDir + `/dm-7" ;;
esac
`
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "cryptsetup"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldLimit, oldKill, oldWait, oldBlock := openTryLimit, killWait, wrongKeyWait, sysBlock
	openTryLimit, killWait, wrongKeyWait, sysBlock = 300*time.Millisecond, 300*time.Millisecond, 0, sysBlockDir
	t.Cleanup(func() {
		openTryLimit, killWait, wrongKeyWait, sysBlock, mapperDir = oldLimit, oldKill, oldWait, oldBlock, "/dev/mapper"
	})
	return log
}

func closes(t *testing.T, log string) string {
	t.Helper()
	data, err := os.ReadFile(log)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

var prepared = &Prepared{part: "/dev/example2", header: "/run/example/old-header"}

// R50, L118: a try that runs over its time is stopped, killed when it
// ignores the stop, and the mapping it made is closed again.
func TestOpenTooSlow(t *testing.T) {
	log := fakeCryptsetup(t, "hang")
	start := time.Now()
	err := prepared.Open(context.Background(), strings.NewReader("key"))
	if r, ok := AsRefusal(err); !ok || r.Reason != TooSlow {
		t.Fatalf("err %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %s", took)
	}
	if got := closes(t, log); got != "close "+Mapping {
		t.Errorf("closes %q", got)
	}
	if _, err := os.Lstat(filepath.Join(mapperDir, Mapping)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("mapping left: %v", err)
	}
}

// Killed after the kernel made the mapping but before udev made its node:
// the kernel's list finds it, and it is closed.
func TestOpenTooSlowNoNode(t *testing.T) {
	log := fakeCryptsetup(t, "hangkernel")
	err := prepared.Open(context.Background(), strings.NewReader("key"))
	if r, ok := AsRefusal(err); !ok || r.Reason != TooSlow {
		t.Fatalf("err %v", err)
	}
	if got := closes(t, log); got != "close "+Mapping {
		t.Errorf("closes %q", got)
	}
	if mapped() {
		t.Error("mapping left")
	}
}

// Stopped from outside: the stop is returned, not a refusal, and nothing
// stays open.
func TestOpenCancelled(t *testing.T) {
	log := fakeCryptsetup(t, "hang")
	openTryLimit = time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := prepared.Open(ctx, strings.NewReader("key"))
	if _, ok := AsRefusal(err); ok || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	if got := closes(t, log); got != "close "+Mapping {
		t.Errorf("closes %q", got)
	}
}

// Inside the time: an open stays open, a wrong key is a wrong key, and
// neither closes anything.
func TestOpenInTime(t *testing.T) {
	log := fakeCryptsetup(t, "ok")
	if err := prepared.Open(context.Background(), strings.NewReader("key")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(mapperDir, Mapping)); err != nil {
		t.Errorf("mapping: %v", err)
	}
	if got := closes(t, log); got != "" {
		t.Errorf("closes %q", got)
	}

	log = fakeCryptsetup(t, "wrong")
	err := prepared.Open(context.Background(), strings.NewReader("key"))
	if r, ok := AsRefusal(err); !ok || r.Reason != WrongKey {
		t.Fatalf("err %v", err)
	}
	if got := closes(t, log); got != "" {
		t.Errorf("closes %q", got)
	}
}
