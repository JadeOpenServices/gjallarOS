package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installreport"
)

// installLogPath is where an installed system keeps the log of the run
// that installed or last changed it (Plane 343): root only, no adm group.
const installLogPath = "/var/log/gjallar-install.log"

// openInstallLog opens this run's RAM log. Tests replace it so a test run
// never writes to the developer's runtime folder.
var openInstallLog = func() (*installlog.Log, error) {
	dir, err := installlog.RuntimeDir()
	if err != nil {
		return nil, err
	}
	return installlog.Open(dir, time.Now())
}

// reboot restarts the machine. Tests replace it.
var reboot = func(ctx context.Context, firmwareSetup bool) error {
	args := []string{"systemctl", "reboot"}
	if firmwareSetup {
		args = append(args, "--firmware-setup")
	}
	return attached(ctx, "sudo", args...)
}

// knownValues are the user's and machine's values a report or log hides.
func knownValues(user config.User) installreport.Known {
	host, _ := os.Hostname()
	return installreport.Known{
		User:     user.Username,
		Name:     user.Name,
		Host:     user.Hostname,
		Email:    user.Email,
		GitHub:   user.GitHubUsername,
		LiveHost: host,
		LiveUser: os.Getenv("USER"),
		Serials:  installreport.DiskSerials("/"),
	}
}

// logKeeper copies the RAM log into the installed system. target is the
// root of that system: "/" on an installed host, /mnt once a fresh install
// finished, empty while there is none. A live run without a target keeps
// the log in RAM only; saving it to a drive is a separate step.
type logKeeper struct {
	log    *installlog.Log
	target string
	// user holds the run's answers; every copy hides the values known by
	// the time it is made.
	user *config.User
	// restarting is set once a restart was asked for. The copy made just
	// before it is the last one: a later copy could be cut off by the
	// shutdown and leave a broken file.
	restarting bool
}

// startInstallLog opens the log and returns writers that copy into it. On
// any problem the run goes on without a log and says so once.
func startInstallLog(out, errOut io.Writer) (*logKeeper, io.Writer, io.Writer) {
	log, err := openInstallLog()
	if err != nil {
		fmt.Fprintf(errOut, "WARNING: no install log this run: %v\n", err)
		return &logKeeper{}, out, errOut
	}
	if log == nil {
		return &logKeeper{}, out, errOut
	}
	installlog.SetCurrent(log)
	installlog.Mark(installlog.RunStarted)
	return &logKeeper{log: log}, log.Tee(out), log.Tee(errOut)
}

// refreshKnown hides the user's values known by now in every later line
// and in every copy.
func (k *logKeeper) refreshKnown() {
	if k.log != nil && k.user != nil {
		k.log.SetKnown(knownValues(*k.user))
	}
}

// keep writes the redacted log into the target as root, mode 0600. A
// failure is a warning: the install itself is not undone for its log.
func (k *logKeeper) keep(ctx context.Context, errOut io.Writer) {
	if k.log == nil || k.target == "" || k.restarting {
		return
	}
	if err := k.copyTo(ctx, k.target); err != nil {
		fmt.Fprintf(errOut, "WARNING: install log not kept on the installed system: %v\n", err)
	}
}

func (k *logKeeper) copyTo(ctx context.Context, target string) error {
	if target != "/" {
		mounted, err := isMountPoint(target)
		if err != nil {
			return err
		}
		if !mounted {
			return fmt.Errorf("%s is not mounted", target)
		}
		// Root follows links in the folders above the file. On a mounted
		// system an absolute link would point into this live system.
		if err := noLinkedFolders(target, filepath.Dir(installLogPath)); err != nil {
			return err
		}
	}
	k.refreshKnown()
	data, err := k.log.Export()
	if err != nil {
		return err
	}
	dst := filepath.Join(target, installLogPath)
	if err := writeRootFile(ctx, data, dst); err != nil {
		return fmt.Errorf("copy to %s: %w", dst, err)
	}
	return nil
}

// writeRootFile writes data to dst as root, mode 0600. The data goes
// through a pipe: root never opens a path the user's session could swap
// for a link to a root-only file. install replaces a link or pipe already
// at dst instead of writing through it. Tests replace it.
var writeRootFile = func(ctx context.Context, data []byte, dst string) error {
	cmd := exec.CommandContext(ctx, "sudo", "install", "-D", "-m", "0600", "-o", "root", "-g", "root", "-T", "--", "/dev/stdin", dst)
	cmd.Stdin = bytes.NewReader(data)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// noLinkedFolders fails when a folder of rel below root is a link.
func noLinkedFolders(root, rel string) error {
	path := root
	for _, part := range strings.Split(strings.Trim(rel, "/"), "/") {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a link", path)
		}
	}
	return nil
}

// export is the redacted log as it stands, for a report step to save with
// the report; nil when this run has no log.
func (k *logKeeper) export() ([]byte, error) {
	if k == nil || k.log == nil {
		return nil, nil
	}
	k.refreshKnown()
	return k.log.Export()
}

// restart keeps the log in the target, then restarts the machine.
func (k *logKeeper) restart(ctx context.Context, errOut io.Writer, firmwareSetup bool) error {
	if firmwareSetup {
		installlog.Mark(installlog.RunRestart, installlog.Choice("into", "firmware-setup"))
	} else {
		installlog.Mark(installlog.RunRestart, installlog.Choice("into", "system"))
	}
	k.keep(ctx, errOut)
	k.restarting = true
	return reboot(ctx, firmwareSetup)
}

// finish ends the log: the last anchor, the copy into the target, close.
func (k *logKeeper) finish(ctx context.Context, errOut io.Writer, code int) {
	if k.log == nil {
		return
	}
	installlog.Mark(installlog.RunFinished, installlog.Count("code", code))
	// A run stopped with Ctrl-C has a cancelled context; its log is the one
	// most worth keeping, so the copy gets a short time of its own.
	copyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	k.keep(copyCtx, errOut)
	installlog.SetCurrent(nil)
	_ = k.log.Close()
}

func isMountPoint(path string) (bool, error) {
	var st, parent unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return false, err
	}
	if err := unix.Lstat(filepath.Dir(path), &parent); err != nil {
		return false, err
	}
	return st.Dev != parent.Dev, nil
}
