package nettrust

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"syscall"
	"time"
)

// VerdictMaxAge is older than the trust timer's five minutes, so a verdict
// that stopped being renewed no longer counts.
const VerdictMaxAge = 15 * time.Minute

// CheckedPath is the stamp root renews each time it checks the network,
// also when the verdict stays the same and its file is left alone.
func CheckedPath(verdictPath string) string {
	return verdictPath + ".checked"
}

// checkedStamp ties the stamp to the verdict it vouches for.
func checkedStamp(verdict []byte) []byte {
	sum := sha256.Sum256(verdict)
	return []byte(hex.EncodeToString(sum[:]) + "\n")
}

// ReadVerdict reads root's network verdict. Only a verdict in a file root
// wrote, with a fresh stamp from root for exactly that verdict, counts;
// anything else is not ok.
func ReadVerdict(path string, now time.Time) (Verdict, bool) {
	verdict, _, ok := ReadVerdictExpiry(path, now)
	return verdict, ok
}

// ReadVerdictExpiry is ReadVerdict that also returns when the verdict stops
// counting unless root renews it.
func ReadVerdictExpiry(path string, now time.Time) (Verdict, time.Time, bool) {
	data, _, ok := ReadRootFile(path)
	if !ok {
		return Verdict{}, time.Time{}, false
	}
	stamp, checked, ok := ReadRootFile(CheckedPath(path))
	if !ok || !bytes.Equal(stamp, checkedStamp(data)) || now.Sub(checked) > VerdictMaxAge {
		return Verdict{}, time.Time{}, false
	}
	var verdict Verdict
	if err := json.Unmarshal(data, &verdict); err != nil {
		return Verdict{}, time.Time{}, false
	}
	return verdict, checked.Add(VerdictMaxAge), true
}

// rootFileOwner is root; tests that run unprivileged use their own uid.
var rootFileOwner uint32 = 0

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
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != rootFileOwner {
		return nil, time.Time{}, false
	}
	data, err := io.ReadAll(io.LimitReader(file, 64<<10))
	if err != nil {
		return nil, time.Time{}, false
	}
	return data, info.ModTime(), true
}
