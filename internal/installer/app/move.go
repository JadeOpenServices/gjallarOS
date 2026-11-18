package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/JadeOpenServices/gjallarOS/internal/binarycache"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/freespace"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/repocopy"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/storecopy"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/movehelper"
	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
)

// The move's test seams. stickDir is the folder the stick's tmpfiles make;
// only the stick has it.
var (
	stickDir     = "/run/gjallar/stick"
	openMoveTerm = func() (*os.File, error) { return os.OpenFile("/dev/tty", os.O_RDWR, 0) }
	moveHelper   = func() (move.Helper, error) { return move.StoreHelper() }
	runNix       = func(ctx context.Context, args []string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, storecopy.Nix, args...)
		cmd.Env = []string{"PATH=/run/current-system/sw/bin", "LC_ALL=C", "HOME=/root"}
		return cmd.CombinedOutput()
	}
)

// moveRun is a guided move to a new disk in progress (wiki 1133 section
// 4.2). It holds the old disk open read-only through the move helper from
// the first question until end. Every method is a no-op on a nil moveRun,
// so the plain fresh install calls them unchanged.
type moveRun struct {
	flow    move.Flow
	old     *move.Old
	choices move.Choices
	// fileCache is the budget of the store export (wiki 1133 step 11).
	fileCache uint64
	exported  bool
	tty       *os.File
	out       io.Writer
	ended     bool
}

// checkMoveFlags refuses option mixes a move never runs with. A move only
// starts from the stick's recovery menu as root, installs fresh, and takes
// its channel from the old disk.
func checkMoveFlags(opt options) error {
	switch opt.move {
	case "":
		return nil
	case move.NewDisk, move.BringOver:
	default:
		return fmt.Errorf("--move takes %s or %s", move.NewDisk, move.BringOver)
	}
	switch {
	case !opt.recovery:
		return errors.New("--move runs only from the gjallarOS stick's recovery menu (--recovery)")
	case opt.acceptExisting:
		return errors.New("--move installs fresh; it cannot update an existing install (--accept-existing)")
	case opt.channel != "":
		return errors.New("--move takes the release channel from the old disk; leave out --channel")
	case opt.forceRedeploy:
		return errors.New("--move installs fresh; leave out --force-redeploy")
	case opt.targetDisk != "":
		return errors.New("--move asks for the new disk and confirms the erase; leave out --target-disk")
	case opt.recoveryDisk != "" || opt.recoveryPartition != "":
		return errors.New("--move installs fresh; leave out --recovery-disk and --recovery-partition")
	case geteuid() != 0:
		return errors.New("--move must run as root")
	}
	return onStick()
}

// onStick is a clear early stop off the stick. It is no guard: the move
// helper itself refuses to run where installed users exist (CheckLiveUsers).
func onStick() error {
	info, err := os.Lstat(stickDir)
	if err != nil || !info.IsDir() {
		return errors.New("the move runs only from the gjallarOS stick")
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 {
		return errors.New("the move runs only from the gjallarOS stick")
	}
	return nil
}

// startMove runs steps 2 to 5: the old disk is picked, opened read-only
// and its settings become this install's answers in presetPath. Nothing on
// any disk is written. A stop by the user returns move.ErrStopped.
func startMove(ctx context.Context, ui prompt.UI, mode, presetPath string, out io.Writer) (*moveRun, error) {
	// The preset is only ever the move's own: a device config the stick
	// brought, or anything else already there, is refused, never replaced.
	if _, err := os.Lstat(presetPath); err == nil {
		return nil, fmt.Errorf("%s already exists; a move takes its answers from the old disk only. Restart the stick without a device config", presetPath)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	helper, err := moveHelper()
	if err != nil {
		return nil, err
	}
	tty, err := openMoveTerm()
	if err != nil {
		return nil, fmt.Errorf("open the terminal for the old disk's key: %w", err)
	}
	m := &moveRun{flow: move.Flow{UI: ui, Term: tty, Helper: helper, Mode: mode}, tty: tty, out: out}
	installlog.Mark(installlog.StickMoveStarted)
	if m.old, err = m.flow.OpenOld(ctx); err != nil {
		m.end(ctx)
		return nil, err
	}
	user, err := m.flow.Settings(ctx)
	if err == nil {
		attendedInstall(&user)
		err = config.WriteAtomic(presetPath, user)
	}
	if err != nil {
		m.end(ctx)
		return nil, err
	}
	return m, nil
}

// attendedInstall sets the keys that steer the installer itself. The old
// disk's values were for its own install; the move is a person at the stick.
func attendedInstall(u *config.User) {
	u.UnattendedInstall = false
	u.AutoReboot = false
	u.ForceRedeploy = false
	u.WriteConfig = true
	u.RunRebuild = true
}

// pickTarget runs steps 6 to 8 for the disk the user picked: it must not be
// the old disk and must still be an eligible target, and what the user
// moves must fit on it.
func (m *moveRun) pickTarget(ctx context.Context, target string, user config.User) error {
	if m == nil {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return fmt.Errorf("the new disk %s: %w", target, err)
	}
	if oldDisk, err := filepath.EvalSymlinks(m.old.Disk.Disk); err != nil || oldDisk == resolved {
		return fmt.Errorf("%s holds the old system; pick another disk for the new one", target)
	}
	eligible, _, err := discoverTargetDisks(ctx, freshTargetMinimumBytes)
	if err != nil {
		return err
	}
	var size uint64
	for _, d := range eligible {
		if p, err := filepath.EvalSymlinks(d.Path); err == nil && p == resolved {
			size = d.SizeBytes
		}
	}
	if size == 0 {
		return fmt.Errorf("%s is not among the disks the install may erase", target)
	}
	var closure movehelper.ClosureResult
	if err := m.flow.Helper.Run(ctx, movehelper.Request{Op: "closure"}, &closure); err != nil {
		return err
	}
	if closure.Bytes <= 0 {
		return errors.New("the old system's size could not be measured")
	}
	// Jade 2026-11-18: the new base system is sized as the old closure
	// plus a quarter; the file cache holds the old closure once.
	m.fileCache = uint64(closure.Bytes)
	m.choices, err = m.flow.Choose(ctx, m.old, move.Target{
		DiskBytes:   size,
		Recovery:    freshRecoveryPartition(user),
		BaseClosure: uint64(closure.Bytes) / 4 * 5,
		FileCache:   m.fileCache,
	})
	return err
}

// prepareNew runs steps 10 and 11 once the new disk is mounted at /mnt: the
// helper sees it, and the old system's store paths go into a cache on it.
// It returns the nix options that put the cache first, nil when the export
// was skipped.
func (m *moveRun) prepareNew(ctx context.Context, repo, mountPoint string) ([]string, error) {
	if m == nil {
		return nil, nil
	}
	if mountPoint != movehelper.TargetRoot {
		return nil, fmt.Errorf("the new disk is mounted at %s, not %s", mountPoint, movehelper.TargetRoot)
	}
	if err := m.flow.Helper.Run(ctx, movehelper.Request{Op: "attach"}, &struct{}{}); err != nil {
		return nil, fmt.Errorf("show the new disk to the move: %w", err)
	}
	fmt.Fprintln(m.out, "STAGE: copying the old system's programs to the new disk")
	var res movehelper.ExportResult
	if err := m.flow.Helper.Run(ctx, movehelper.Request{Op: "export", Budget: int64(m.fileCache)}, &res); err != nil {
		return nil, fmt.Errorf("copy the old system's programs: %w", err)
	}
	skipped := res.Skipped != ""
	installlog.Mark(installlog.StickMoveExported,
		installlog.Count("paths", res.Paths),
		installlog.Count("gib", int((res.Bytes+(1<<30)-1)>>30)),
		installlog.Bool("skipped", skipped))
	if skipped {
		fmt.Fprintf(m.out, "The old programs were not copied (%s); the install downloads them instead.\n", oneLine(res.Skipped))
		return nil, nil
	}
	m.exported = true
	keys := []string{storecopy.NixOSKey}
	if k := binarycache.Key(repo); k != "" {
		keys = append(keys, k)
	}
	return storecopy.SubstituteArgs(filepath.Join(movehelper.TargetRoot, storecopy.CacheName), keys...)
}

// checkInstalled runs after nixos-install: every path of the new system's
// closure matches its recorded hash, and the cache goes. A mismatch stops
// the move before anything of the old home is copied.
func (m *moveRun) checkInstalled(ctx context.Context) error {
	if m == nil {
		return nil
	}
	nix, err := safefs.OpenDir(filepath.Join(movehelper.TargetRoot, "nix"))
	if err != nil {
		return err
	}
	toplevel, err := storecopy.OldSystem(nix)
	nix.Close()
	if err != nil {
		return fmt.Errorf("the new system: %w", err)
	}
	args, err := storecopy.VerifyArgs(movehelper.TargetRoot, toplevel)
	if err != nil {
		return err
	}
	if out, err := runNix(ctx, args); err != nil {
		installlog.Mark(installlog.StickMoveVerifyFailed)
		return fmt.Errorf("the new system does not match its recorded hashes: %w: %s", err, oneLine(string(out)))
	}
	installlog.Mark(installlog.StickMoveVerified)
	if !m.exported {
		return nil
	}
	if err := m.flow.Helper.Run(ctx, movehelper.Request{Op: "dropcache"}, &struct{}{}); err != nil {
		return fmt.Errorf("remove the copied programs: %w", err)
	}
	m.exported = false
	return nil
}

// copyBranches runs step 13 when the user moves the system checkout's own
// branches. A refused or failed copy is told and the move goes on: the new
// checkout is the release, as in a plain install.
func (m *moveRun) copyBranches(ctx context.Context) error {
	if m == nil || !m.choices.Chosen(freespace.Branches) {
		return nil
	}
	var budget uint64
	for _, c := range m.choices.Categories {
		if c.Name == freespace.Branches {
			budget = c.Bytes
		}
	}
	if budget == 0 {
		return nil
	}
	var res repocopy.Result
	if err := m.flow.Helper.Run(ctx, movehelper.Request{Op: "repo", Budget: int64(budget)}, &res); err != nil {
		return fmt.Errorf("carry over the system checkout's branches: %w", err)
	}
	installlog.Mark(installlog.StickMoveRepoCopied,
		installlog.Count("branches", res.Branches),
		installlog.Count("moved", res.Moved),
		installlog.Count("stashes", res.Stashes),
		installlog.Count("skipped", res.Skipped),
		installlog.Bool("refused", res.Refused != ""),
		installlog.Bool("failed", res.Failed != ""))
	switch {
	case res.Refused != "":
		fmt.Fprintf(m.out, "Your own branches stay on the old disk: %s\n", oneLine(res.Refused))
	case res.Failed != "":
		fmt.Fprintf(m.out, "Your own branches stay on the old disk; the copy was taken back: %s\n", oneLine(res.Failed))
	default:
		fmt.Fprintf(m.out, "Carried over %d of %d branches and %d stashes.\n", res.Moved, res.Branches, res.Stashes)
	}
	return nil
}

// end closes the old disk and takes the new one away from the helper. It
// runs once, also after a cancelled ctx; later calls do nothing. The new
// disk must be detached before the install closes it (L151).
func (m *moveRun) end(ctx context.Context) {
	if m == nil || m.ended {
		return
	}
	m.ended = true
	ctx = context.WithoutCancel(ctx)
	if m.exported {
		if err := m.flow.Helper.Run(ctx, movehelper.Request{Op: "dropcache"}, &struct{}{}); err != nil {
			fmt.Fprintf(m.out, "WARNING: the copied programs are still on the new disk: %v\n", err)
		}
	}
	if err := m.flow.Close(ctx); err != nil {
		fmt.Fprintf(m.out, "WARNING: the old disk is still open (%v). Restart the stick before you use it.\n", err)
	}
	if err := m.flow.Helper.Run(ctx, movehelper.Request{Op: "detach"}, &struct{}{}); err != nil {
		fmt.Fprintf(m.out, "WARNING: the new disk is still shown to the move (%v). Restart the stick before you install again.\n", err)
	} else {
		installlog.Mark(installlog.StickMoveEnded)
	}
	if m.tty != nil {
		m.tty.Close()
	}
}

// oneLine keeps an error from the helper or nix on one printable line.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}
