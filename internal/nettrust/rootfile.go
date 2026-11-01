package nettrust

import (
	"encoding/json"
	"io"
	"os"
	"syscall"
	"time"
)

// VerdictMaxAge is older than the trust timer's five minutes, so a verdict
// that stopped being renewed no longer counts.
const VerdictMaxAge = 15 * time.Minute

// ReadVerdict reads root's network verdict. Only a fresh verdict in a file
// root wrote counts; anything else is not ok.
func ReadVerdict(path string, now time.Time) (Verdict, bool) {
	verdict, _, ok := ReadVerdictExpiry(path, now)
	return verdict, ok
}

// ReadVerdictExpiry is ReadVerdict that also returns when the verdict stops
// counting unless root renews it.
func ReadVerdictExpiry(path string, now time.Time) (Verdict, time.Time, bool) {
	data, written, ok := ReadRootFile(path)
	if !ok || now.Sub(written) > VerdictMaxAge {
		return Verdict{}, time.Time{}, false
	}
	var verdict Verdict
	if err := json.Unmarshal(data, &verdict); err != nil {
		return Verdict{}, time.Time{}, false
	}
	return verdict, written.Add(VerdictMaxAge), true
}

// ReadRootFile reads a small regular file only root can write, and when it
// was written.
func ReadRootFile(path string) ([]byte, time.Time, bool) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, time.Time{}, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return nil, time.Time{}, false
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != 0 {
		return nil, time.Time{}, false
	}
	data, err := io.ReadAll(io.LimitReader(file, 64<<10))
	if err != nil {
		return nil, time.Time{}, false
	}
	return data, info.ModTime(), true
}
