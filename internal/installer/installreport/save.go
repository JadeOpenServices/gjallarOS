package installreport

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// FileName is the report file's name for a report made at now.
func FileName(now time.Time) string {
	return "gjallaros-install-report-" + now.UTC().Format("20060102-150405") + ".txt"
}

// Save writes data to a new file in dir, readable by the owner only. It
// never replaces or follows an existing file or link, so nothing planted
// at that name gets the report.
func Save(dir string, data []byte, now time.Time) (string, error) {
	if len(data) > MaxSize {
		return "", fmt.Errorf("report is larger than %d bytes", MaxSize)
	}
	path := filepath.Join(dir, FileName(now))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%s already exists; nothing written", path)
		}
		return "", err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}
