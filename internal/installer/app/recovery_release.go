package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// recoveryReleasePrefix starts the root-only folder that holds root's copy
// of a signed recovery release. Tests point it at their own folder.
var recoveryReleasePrefix = "/run/gjallar-recovery-release."

// recoveryReleaseCap bounds each release file; a manifest, a signature and
// a public key are a few hundred bytes.
const recoveryReleaseCap = 64 << 10

// stageRecoveryRelease gives root its own copy of the manifest, signature
// and public key that sign-image.sh wrote to releaseDir for image, and
// returns root's folder and the three paths in install-partition.sh order.
// install-partition.sh checks the files and then copies them again: given
// paths in the user's folder, the user could swap them in between, for an
// unchecked manifest, a link to a root-only file or a pipe (L56). The data
// reaches root on stdin, in a folder only root can change.
func stageRecoveryRelease(ctx context.Context, releaseDir, image string) (string, []string, error) {
	names := []string{
		filepath.Base(image) + ".manifest",
		filepath.Base(image) + ".manifest.sig",
		"recovery-signing-public.pem",
	}
	data := make([][]byte, len(names))
	for i, name := range names {
		var err error
		if data[i], err = readReleaseFile(filepath.Join(releaseDir, name)); err != nil {
			return "", nil, err
		}
	}
	dir, err := makeRootTempDir(ctx, recoveryReleasePrefix)
	if err != nil {
		return "", nil, err
	}
	paths := make([]string, len(names))
	for i, name := range names {
		paths[i] = filepath.Join(dir, name)
		if err := writeRootFile(ctx, data[i], paths[i]); err != nil {
			_, _ = privilegedCommand(context.WithoutCancel(ctx), "rm", "-rf", "--", dir)
			return "", nil, fmt.Errorf("stage %s: %w", name, err)
		}
	}
	return dir, paths, nil
}

// readReleaseFile reads one plain file of at most recoveryReleaseCap bytes.
// It opens without following a link and without waiting on a pipe.
func readReleaseFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a plain file (%s)", path, info.Mode().Type())
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a plain file: %v", path, err)
	}
	data, err := io.ReadAll(io.LimitReader(file, recoveryReleaseCap+1))
	if err != nil {
		return nil, err
	}
	if len(data) > recoveryReleaseCap {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, recoveryReleaseCap)
	}
	return data, nil
}
