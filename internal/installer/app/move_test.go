package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/freespace"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/oldroot"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/repocopy"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/storecopy"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/targetdisk"
	"github.com/JadeOpenServices/gjallarOS/internal/movehelper"
	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
	"golang.org/x/sys/unix"
)

// fakeMoveHelper records the ops it is asked for and answers with fixed
// results. Like the real helper it runs nothing once ctx is cancelled.
type fakeMoveHelper struct {
	ops     []string
	budgets map[string]int64
	fail    map[string]error
	export  movehelper.ExportResult
	closure movehelper.ClosureResult
	repo    repocopy.Result
	home    movehelper.HomeResult
	syncs   map[string]bool
}

func (h *fakeMoveHelper) Run(ctx context.Context, req movehelper.Request, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.ops = append(h.ops, req.Op)
	if h.budgets == nil {
		h.budgets = map[string]int64{}
	}
	h.budgets[req.Op] = req.Budget
	if h.syncs == nil {
		h.syncs = map[string]bool{}
	}
	h.syncs[req.Op] = req.Sync
	if err := h.fail[req.Op]; err != nil {
		return err
	}
	switch r := result.(type) {
	case *movehelper.ExportResult:
		*r = h.export
	case *movehelper.ClosureResult:
		*r = h.closure
	case *repocopy.Result:
		*r = h.repo
	case *movehelper.HomeResult:
		*r = h.home
	}
	return nil
}

func testMoveRun(h *fakeMoveHelper, out *bytes.Buffer) *moveRun {
	return &moveRun{flow: move.Flow{Helper: h}, out: out}
}

func TestCheckMoveFlags(t *testing.T) {
	saved, savedDir := geteuid, stickDir
	t.Cleanup(func() { geteuid, stickDir = saved, savedDir })
	geteuid = func() int { return 0 }
	stickDir = "/"
	base := options{recovery: true, move: move.NewDisk}
	for name, tc := range map[string]struct {
		change func(*options)
		want   string
	}{
		"plain install":      {func(o *options) { o.move = "" }, ""},
		"new-disk":           {func(*options) {}, ""},
		"bring-over":         {func(o *options) { o.move = move.BringOver }, ""},
		"unknown mode":       {func(o *options) { o.move = "--x" }, "takes"},
		"not recovery":       {func(o *options) { o.recovery = false }, "recovery menu"},
		"accept existing":    {func(o *options) { o.acceptExisting = true }, "--accept-existing"},
		"channel":            {func(o *options) { o.channel = "dev" }, "--channel"},
		"force redeploy":     {func(o *options) { o.forceRedeploy = true }, "--force-redeploy"},
		"target disk":        {func(o *options) { o.targetDisk = "/dev/sda" }, "--target-disk"},
		"recovery disk":      {func(o *options) { o.recoveryDisk = "/dev/sda" }, "--recovery-disk"},
		"recovery partition": {func(o *options) { o.recoveryPartition = "/dev/sda2" }, "--recovery-disk"},
	} {
		t.Run(name, func(t *testing.T) {
			opt := base
			tc.change(&opt)
			err := checkMoveFlags(opt)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	geteuid = func() int { return 1000 }
	if err := checkMoveFlags(base); err == nil || !strings.Contains(err.Error(), "root") {
		t.Errorf("non-root: %v", err)
	}
}

// Off the stick, a link to a root folder or a folder the user owns is no
// stick folder.
func TestOnStick(t *testing.T) {
	saved := stickDir
	t.Cleanup(func() { stickDir = saved })
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink("/", link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(dir, "missing"), link, file, dir} {
		stickDir = p
		if os.Geteuid() == 0 && p == dir {
			continue
		}
		if err := onStick(); err == nil {
			t.Errorf("%s accepted as the stick folder", p)
		}
	}
	stickDir = "/"
	if err := onStick(); err != nil {
		t.Errorf("root folder refused: %v", err)
	}
}

// Anything at the preset path, a dangling link or a FIFO included, stops
// the move before the helper starts.
func TestStartMoveRefusesPlantedPreset(t *testing.T) {
	saved := moveHelper
	t.Cleanup(func() { moveHelper = saved })
	started := false
	moveHelper = func() (move.Helper, error) { started = true; return &fakeMoveHelper{}, nil }
	for name, plant := range map[string]func(string) error{
		"dangling link": func(p string) error { return os.Symlink(filepath.Join(filepath.Dir(p), "nowhere"), p) },
		"link":          func(p string) error { return os.Symlink("/etc/hostname", p) },
		"fifo":          func(p string) error { return unix.Mkfifo(p, 0o600) },
		"file":          func(p string) error { return os.WriteFile(p, []byte("{}"), 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			preset := filepath.Join(t.TempDir(), "user.config.json")
			if err := plant(preset); err != nil {
				t.Fatal(err)
			}
			_, err := startMove(context.Background(), prompt.UI{}, move.NewDisk, preset, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("err = %v", err)
			}
			if started {
				t.Fatal("the helper started")
			}
		})
	}
}

func TestAttendedInstall(t *testing.T) {
	u := config.User{UnattendedInstall: true, AutoReboot: true, ForceRedeploy: true}
	attendedInstall(&u)
	if u.UnattendedInstall || u.AutoReboot || u.ForceRedeploy || !u.WriteConfig || !u.RunRebuild {
		t.Fatalf("installer keys after attendedInstall: %+v", u)
	}
}

func TestPickTargetRefuses(t *testing.T) {
	dir := t.TempDir()
	oldDisk := filepath.Join(dir, "nvme0n1")
	newDisk := filepath.Join(dir, "sda")
	for _, p := range []string{oldDisk, newDisk} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldLink := filepath.Join(dir, "by-id-old")
	if err := os.Symlink(oldDisk, oldLink); err != nil {
		t.Fatal(err)
	}
	saved := discoverTargetDisks
	t.Cleanup(func() { discoverTargetDisks = saved })
	var eligible []targetdisk.Result
	discoverTargetDisks = func(context.Context, uint64) ([]targetdisk.Result, []targetdisk.Rejected, error) {
		return eligible, nil, nil
	}
	user := config.User{}
	for name, tc := range map[string]struct {
		target   string
		eligible []targetdisk.Result
		closure  int64
		want     string
	}{
		"same disk":       {oldDisk, nil, 1, "holds the old system"},
		"same disk link":  {oldLink, nil, 1, "holds the old system"},
		"not eligible":    {newDisk, []targetdisk.Result{{Path: oldDisk, SizeBytes: 1 << 40}}, 1, "not among"},
		"missing":         {filepath.Join(dir, "sdz"), nil, 1, "the new disk"},
		"closure unknown": {newDisk, []targetdisk.Result{{Path: newDisk, SizeBytes: 1 << 40}}, 0, "could not be measured"},
	} {
		t.Run(name, func(t *testing.T) {
			eligible = tc.eligible
			h := &fakeMoveHelper{closure: movehelper.ClosureResult{Bytes: tc.closure}}
			m := testMoveRun(h, &bytes.Buffer{})
			m.old = &move.Old{Disk: oldroot.Part{Disk: oldLink}}
			err := m.pickTarget(context.Background(), tc.target, user)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if slices.Contains(h.ops, "erase") {
				t.Fatal("erase asked")
			}
		})
	}
}

func TestPrepareNew(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()

	h := &fakeMoveHelper{}
	if _, err := testMoveRun(h, &bytes.Buffer{}).prepareNew(ctx, repo, "/mnt2"); err == nil || len(h.ops) != 0 {
		t.Fatalf("other mount point: err %v, ops %v", err, h.ops)
	}

	h = &fakeMoveHelper{export: movehelper.ExportResult{Skipped: "not\nenough\x1b room"}}
	var out bytes.Buffer
	m := testMoveRun(h, &out)
	m.fileCache = 7 << 30
	args, err := m.prepareNew(ctx, repo, movehelper.TargetRoot)
	if err != nil || args != nil || m.exported {
		t.Fatalf("skipped export: args %v, err %v, exported %v", args, err, m.exported)
	}
	if !strings.Contains(out.String(), "(not enough? room)") {
		t.Errorf("output = %q", out.String())
	}

	h = &fakeMoveHelper{export: movehelper.ExportResult{Stats: storecopy.Stats{Paths: 3, Bytes: 1}}}
	m = testMoveRun(h, &bytes.Buffer{})
	m.fileCache = 7 << 30
	args, err = m.prepareNew(ctx, repo, movehelper.TargetRoot)
	if err != nil || !m.exported {
		t.Fatalf("err %v, exported %v", err, m.exported)
	}
	if want := []string{"attach", "export"}; !slices.Equal(h.ops, want) {
		t.Errorf("ops = %v, want %v", h.ops, want)
	}
	if h.budgets["export"] != 7<<30 {
		t.Errorf("export budget %d", h.budgets["export"])
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"extra-substituters file:///mnt/" + storecopy.CacheName + "?priority=10",
		"trusted-public-keys " + storecopy.NixOSKey + " --option",
		"require-sigs true",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q lack %q", joined, want)
		}
	}

	h = &fakeMoveHelper{fail: map[string]error{"attach": errors.New("already attached")}}
	if _, err := testMoveRun(h, &bytes.Buffer{}).prepareNew(ctx, repo, movehelper.TargetRoot); err == nil || slices.Contains(h.ops, "export") {
		t.Fatalf("failed attach: err %v, ops %v", err, h.ops)
	}
}

func TestCopyBranches(t *testing.T) {
	ctx := context.Background()
	chosen := func(on bool, bytes uint64) move.Choices {
		return move.Choices{Categories: []freespace.Category{{Name: freespace.Branches, Chosen: on, Bytes: bytes}}}
	}
	h := &fakeMoveHelper{}
	m := testMoveRun(h, &bytes.Buffer{})
	m.choices = chosen(false, 10)
	if err := m.copyBranches(ctx); err != nil || len(h.ops) != 0 {
		t.Fatalf("not chosen: err %v, ops %v", err, h.ops)
	}
	m.choices = chosen(true, 0)
	if err := m.copyBranches(ctx); err != nil || len(h.ops) != 0 {
		t.Fatalf("no budget: err %v, ops %v", err, h.ops)
	}

	var out bytes.Buffer
	h = &fakeMoveHelper{repo: repocopy.Result{Branches: 3, Refused: "a\nhook"}}
	m = testMoveRun(h, &out)
	m.choices = chosen(true, 4096)
	if err := m.copyBranches(ctx); err != nil {
		t.Fatal(err)
	}
	if h.budgets["repo"] != 4096 || !strings.Contains(out.String(), "stay on the old disk: a hook") {
		t.Errorf("budget %d, output %q", h.budgets["repo"], out.String())
	}

	h = &fakeMoveHelper{fail: map[string]error{"repo": errors.New("helper gone")}}
	m = testMoveRun(h, &bytes.Buffer{})
	m.choices = chosen(true, 4096)
	if err := m.copyBranches(ctx); err == nil {
		t.Fatal("a helper error is no hard stop")
	}
}

// copyHome asks for the home with the Nextcloud folders only when both
// were chosen, with a budget of what was chosen; a failed copy is told and
// the install goes on, a helper error stops it.
func TestCopyHome(t *testing.T) {
	ctx := context.Background()
	chosen := func(home, nc bool) move.Choices {
		return move.Choices{Categories: []freespace.Category{
			{Name: freespace.Home, Chosen: home, Bytes: 4096},
			{Name: freespace.Nextcloud, Chosen: nc, Bytes: 1024},
		}}
	}
	h := &fakeMoveHelper{}
	m := testMoveRun(h, &bytes.Buffer{})
	m.choices = chosen(false, true)
	if err := m.copyHome(ctx); err != nil || len(h.ops) != 0 {
		t.Fatalf("not chosen: err %v, ops %v", err, h.ops)
	}
	var nilRun *moveRun
	if err := nilRun.copyHome(ctx); err != nil {
		t.Fatalf("no move: %v", err)
	}

	for name, tc := range map[string]struct {
		nc     bool
		budget int64
		res    movehelper.HomeResult
		want   []string
		absent string
	}{
		"with nextcloud": {nc: true, budget: 5120,
			res:  movehelper.HomeResult{Stats: safefs.Stats{Files: 7, Folders: 2}},
			want: []string{"Copied your home folder: 7 files, 2 folders."}, absent: "Nextcloud"},
		"without": {budget: 4096,
			res:  movehelper.HomeResult{Stats: safefs.Stats{Files: 1, Left: 1, Skipped: 1, Foreign: 2}},
			want: []string{"The Nextcloud folders stay on the old disk", "3 entries stay on the old disk"}},
		"failed": {budget: 4096,
			res:  movehelper.HomeResult{Failed: "disk\nfull"},
			want: []string{"stays on the old disk; the copy was taken back: disk full"}, absent: "Copied"},
	} {
		var out bytes.Buffer
		h := &fakeMoveHelper{home: tc.res}
		m := testMoveRun(h, &out)
		m.choices = chosen(true, tc.nc)
		if err := m.copyHome(ctx); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(h.ops, []string{"home"}) || h.budgets["home"] != tc.budget || h.syncs["home"] != tc.nc {
			t.Errorf("%s: ops %v, budget %d, sync %v", name, h.ops, h.budgets["home"], h.syncs["home"])
		}
		for _, w := range tc.want {
			if !strings.Contains(out.String(), w) {
				t.Errorf("%s: output %q lacks %q", name, out.String(), w)
			}
		}
		if tc.absent != "" && strings.Contains(out.String(), tc.absent) {
			t.Errorf("%s: output %q has %q", name, out.String(), tc.absent)
		}
	}

	h = &fakeMoveHelper{fail: map[string]error{"home": errors.New("helper gone")}}
	m = testMoveRun(h, &bytes.Buffer{})
	m.choices = chosen(true, false)
	if err := m.copyHome(ctx); err == nil {
		t.Fatal("a helper error is no hard stop")
	}
}

// end drops the cache, closes the old disk and detaches the new one, in
// that order, once, also after a cancel; a nil moveRun does nothing.
func TestMoveEnd(t *testing.T) {
	var nilRun *moveRun
	nilRun.end(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := &fakeMoveHelper{}
	m := testMoveRun(h, &bytes.Buffer{})
	m.exported = true
	m.end(ctx)
	m.end(ctx)
	if want := []string{"dropcache", "close", "detach"}; !slices.Equal(h.ops, want) {
		t.Fatalf("ops = %v, want %v", h.ops, want)
	}

	var out bytes.Buffer
	h = &fakeMoveHelper{fail: map[string]error{"close": errors.New("busy"), "detach": errors.New("busy")}}
	testMoveRun(h, &out).end(context.Background())
	if want := []string{"close", "detach"}; !slices.Equal(h.ops, want) {
		t.Fatalf("ops = %v, want %v", h.ops, want)
	}
	if !strings.Contains(out.String(), "old disk is still open") || !strings.Contains(out.String(), "still shown to the move") {
		t.Errorf("output = %q", out.String())
	}
}

// L151: the move detaches the new disk before the install unmounts and
// closes it; deferred in this order in runFreshBareMetal.
func TestMoveDetachesBeforeTargetCloses(t *testing.T) {
	calls := recordPrivileged(t, "")
	h := &fakeMoveHelper{}
	var order []string
	closeTarget := closeFreshTarget(context.Background(), "/mnt", "cryptroot", &bytes.Buffer{})
	releaseFreshTarget(context.Background(), testMoveRun(h, &bytes.Buffer{}), func() {
		order = append(order, h.ops...)
		closeTarget()
		order = append(order, *calls...)
	})
	want := []string{"close", "detach", "umount -R -- /mnt", "cryptsetup close cryptroot"}
	if !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("a\n\tb\x1b[0m\x7f"); got != "a b?[0m?" {
		t.Errorf("oneLine = %q", got)
	}
	if got := oneLine(strings.Repeat("x", 400)); len(got) != 303 {
		t.Errorf("len %d", len(got))
	}
}
