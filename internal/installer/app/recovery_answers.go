package app

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installedanswers"
)

// readInstalledAnswers is swapped in tests.
var readInstalledAnswers = installedanswers.Read

// importInstalledAnswers gives a reinstall from the recovery partition the
// answers of the installed system. The recovery image carries none: they
// stay inside the encrypted root and are read only after it is unlocked
// and mounted at installedRoot (L78). A device configuration handed to
// recovery at runtime is already at presetPath and wins. Without either the
// installer asks.
//
// The copy goes into the live repository, never back into the installed
// root: root writes nothing on a disk it did not make.
func importInstalledAnswers(installedRoot, presetPath string, out io.Writer) error {
	if info, err := os.Lstat(presetPath); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refuse %s: not a regular file", presetPath)
		}
		fmt.Fprintln(out, "Installer answers: the device configuration given to recovery.")
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check %s: %w", presetPath, err)
	}

	data, err := readInstalledAnswers(installedRoot)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(out, "Installer answers: the installed system has none saved; the installer asks.")
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the installed system's answers: %w", err)
	}
	if _, err := config.Parse(data); err != nil {
		return fmt.Errorf("the installed system's answers in /%s: %w", installedanswers.Path, err)
	}

	f, err := os.OpenFile(presetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("copy the installed system's answers: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(presetPath)
		return fmt.Errorf("copy the installed system's answers: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(presetPath)
		return fmt.Errorf("copy the installed system's answers: %w", err)
	}
	fmt.Fprintf(out, "Installer answers: the installed system's /%s.\n", installedanswers.Path)
	return nil
}
