package recoverytarget

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskplan"
)

type fakeExitError struct {
	code int
}

func (e fakeExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.code)
}

func (e fakeExitError) ExitCode() int {
	return e.code
}

type fakeRunner struct {
	outputs   map[string][]byte
	sequences map[string][][]byte
	errors    map[string]error
	runs      []string
}

func commandKey(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), "\x00")
}

func (r *fakeRunner) Output(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	key := commandKey(name, args...)

	if seq := r.sequences[key]; len(seq) > 0 {
		out := seq[0]
		r.sequences[key] = seq[1:]
		return out, nil
	}

	if err := r.errors[key]; err != nil {
		return nil, err
	}

	return r.outputs[key], nil
}

func (r *fakeRunner) Run(
	_ context.Context,
	name string,
	args ...string,
) error {
	key := commandKey(name, args...)
	r.runs = append(r.runs, key)
	return r.errors[key]
}

func writeNixOSFstab(t *testing.T, root, source string) {
	t.Helper()

	store := filepath.Join(root, "nix", "store")
	static := filepath.Join(root, "etc", "static")

	if err := os.MkdirAll(store, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(static, 0755); err != nil {
		t.Fatal(err)
	}

	storeFstab := filepath.Join(store, "test-etc-fstab")
	content := source + " /boot vfat fmask=0077,dmask=0077 0 2\n"

	if err := os.WriteFile(storeFstab, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(
		"/nix/store/test-etc-fstab",
		filepath.Join(static, "fstab"),
	); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(
		"/etc/static/fstab",
		filepath.Join(root, "etc", "fstab"),
	); err != nil {
		t.Fatal(err)
	}
}

func TestReadTargetFileFollowsNixOSAbsoluteSymlinksWithinRoot(t *testing.T) {
	dir := t.TempDir()
	writeNixOSFstab(t, dir, "/dev/test-esp")

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	data, err := readTargetFile(root, "etc/fstab")
	if err != nil {
		t.Fatalf("readTargetFile: %v", err)
	}

	if !strings.Contains(string(data), "/dev/test-esp /boot vfat") {
		t.Fatalf("unexpected fstab: %q", data)
	}
}

func TestReadTargetFileRejectsRelativeEscape(t *testing.T) {
	dir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(dir, "etc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		"../../outside",
		filepath.Join(dir, "etc", "fstab"),
	); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	_, err = readTargetFile(root, "etc/fstab")
	if err == nil || !strings.Contains(err.Error(), "escapes recovery root") {
		t.Fatalf("expected root escape rejection, got %v", err)
	}
}

func TestBootSource(t *testing.T) {
	fstab := []byte(
		"# generated\n" +
			"/dev/mapper/root / btrfs defaults 0 1\n" +
			"/dev/disk/by-uuid/ABCD-1234 /boot vfat defaults 0 2\n",
	)

	got, err := bootSource(fstab)
	if err != nil {
		t.Fatal(err)
	}

	if got != "/dev/disk/by-uuid/ABCD-1234" {
		t.Fatalf("got %q", got)
	}
}

func TestBootSourceRejectsMultipleEntries(t *testing.T) {
	fstab := []byte(
		"/dev/a /boot vfat defaults 0 2\n" +
			"/dev/b /boot vfat defaults 0 2\n",
	)

	_, err := bootSource(fstab)
	if err == nil || !strings.Contains(err.Error(), "multiple /boot") {
		t.Fatalf("expected duplicate /boot rejection, got %v", err)
	}
}

func TestResolveDeviceSourceTags(t *testing.T) {
	tests := map[string]string{
		"UUID=ABCD":      "/dev/disk/by-uuid/ABCD",
		"PARTUUID=1234":  "/dev/disk/by-partuuid/1234",
		"LABEL=EFI":      "/dev/disk/by-label/EFI",
		"PARTLABEL=EFI":  "/dev/disk/by-partlabel/EFI",
		"/dev/nvme0n1p1": "/dev/nvme0n1p1",
	}

	for input, want := range tests {
		got, err := resolveDeviceSource(input)
		if err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if got != want {
			t.Fatalf("%s: got %q, want %q", input, got, want)
		}
	}
}

func TestPrepareBootAcceptsVerifiedExistingMount(t *testing.T) {
	dir := t.TempDir()
	source := "/dev/disk/by-uuid/TEST-ESP"
	resolved := "/dev/nvme0n1p1"
	target := filepath.Join(dir, "boot")

	writeNixOSFstab(t, dir, source)

	r := &fakeRunner{
		outputs: map[string][]byte{
			commandKey(
				"findmnt",
				"-nvro", "SOURCE,OPTIONS",
				"--mountpoint", target,
			): []byte(source + " rw,relatime\n"),
			commandKey(
				"readlink",
				"-f", "--", source,
			): []byte(resolved + "\n"),
		},
		errors: map[string]error{},
	}

	if err := prepareBoot(context.Background(), dir, r); err != nil {
		t.Fatalf("prepareBoot: %v", err)
	}

	for _, run := range r.runs {
		if strings.HasPrefix(run, "mount\x00") {
			t.Fatalf("unexpected mount command: %q", run)
		}
	}
}

func TestBootSourceRejectsMissingBoot(t *testing.T) {
	fstab := []byte(
		"/dev/mapper/root / btrfs defaults 0 1\n",
	)

	_, err := bootSource(fstab)
	if err == nil || !strings.Contains(err.Error(), "does not define /boot") {
		t.Fatalf("expected missing /boot rejection, got %v", err)
	}
}

func TestResolveDeviceSourceRejectsInvalidTag(t *testing.T) {
	_, err := resolveDeviceSource("UUID=../escape")
	if err == nil || !strings.Contains(err.Error(), "invalid installed /boot source") {
		t.Fatalf("expected invalid device tag rejection, got %v", err)
	}
}

func TestMountedSourceTreatsExitOneAsNotMounted(t *testing.T) {
	target := "/mnt/boot"
	r := &fakeRunner{
		outputs: map[string][]byte{},
		errors: map[string]error{
			commandKey(
				"findmnt",
				"-nvro", "SOURCE,OPTIONS",
				"--mountpoint", target,
			): fakeExitError{code: 1},
		},
	}

	source, options, err := mountedSource(context.Background(), r, target)
	if err != nil {
		t.Fatalf("mountedSource: %v", err)
	}
	if source != "" || options != "" {
		t.Fatalf("expected no mounted source/options, got %q %q", source, options)
	}
}

func TestPrepareBootRejectsNonBlockSource(t *testing.T) {
	dir := t.TempDir()
	source := "/dev/disk/by-uuid/TEST-ESP"

	writeNixOSFstab(t, dir, source)

	r := &fakeRunner{
		outputs: map[string][]byte{},
		errors: map[string]error{
			commandKey("test", "-b", source): fmt.Errorf("not a block device"),
		},
	}

	err := prepareBoot(context.Background(), dir, r)
	if err == nil || !strings.Contains(err.Error(), "not a block device") {
		t.Fatalf("expected block-device rejection, got %v", err)
	}
}

func TestPrepareBootRejectsWrongExistingMount(t *testing.T) {
	dir := t.TempDir()
	expected := "/dev/disk/by-uuid/TEST-ESP"
	mounted := "/dev/disk/by-uuid/OTHER-ESP"
	target := filepath.Join(dir, "boot")

	writeNixOSFstab(t, dir, expected)

	r := &fakeRunner{
		outputs: map[string][]byte{
			commandKey(
				"findmnt",
				"-nvro", "SOURCE,OPTIONS",
				"--mountpoint", target,
			): []byte(mounted + " rw,relatime\n"),
			commandKey(
				"readlink",
				"-f", "--", expected,
			): []byte("/dev/nvme0n1p1\n"),
			commandKey(
				"readlink",
				"-f", "--", mounted,
			): []byte("/dev/nvme0n1p2\n"),
		},
		errors: map[string]error{},
	}

	err := prepareBoot(context.Background(), dir, r)
	if err == nil || !strings.Contains(err.Error(), "uses "+mounted+"; expected "+expected) {
		t.Fatalf("expected wrong-mount rejection, got %v", err)
	}
}

func TestPrepareBootFailsClosedWhenMountFails(t *testing.T) {
	dir := t.TempDir()
	source := "/dev/disk/by-uuid/TEST-ESP"
	target := filepath.Join(dir, "boot")

	writeNixOSFstab(t, dir, source)

	r := &fakeRunner{
		outputs: map[string][]byte{},
		errors: map[string]error{
			commandKey(
				"findmnt",
				"-nvro", "SOURCE,OPTIONS",
				"--mountpoint", target,
			): fakeExitError{code: 1},
			commandKey(
				"mount",
				"-o", diskplan.PrivateFATMasks,
				source,
				target,
			): fmt.Errorf("mount failed"),
		},
	}

	err := prepareBoot(context.Background(), dir, r)
	if err == nil || !strings.Contains(err.Error(), "mount installed /boot") {
		t.Fatalf("expected mount failure, got %v", err)
	}
}

func TestPrepareBootMountsAndVerifiesBoot(t *testing.T) {
	dir := t.TempDir()
	source := "/dev/disk/by-uuid/TEST-ESP"
	resolved := "/dev/nvme0n1p1"
	target := filepath.Join(dir, "boot")
	findmntKey := commandKey(
		"findmnt",
		"-nvro", "SOURCE,OPTIONS",
		"--mountpoint", target,
	)

	writeNixOSFstab(t, dir, source)

	r := &fakeRunner{
		outputs: map[string][]byte{
			commandKey(
				"readlink",
				"-f", "--", source,
			): []byte(resolved + "\n"),
		},
		sequences: map[string][][]byte{
			findmntKey: {
				nil,
				[]byte(source + " rw,relatime\n"),
			},
		},
		errors: map[string]error{},
	}

	// The first findmnt lookup must behave as "not mounted".
	r.errors[findmntKey] = fakeExitError{code: 1}

	err := prepareBoot(context.Background(), dir, &mountingRunner{
		fakeRunner: r,
		findmntKey: findmntKey,
	})
	if err != nil {
		t.Fatalf("prepareBoot: %v", err)
	}

	wantMount := commandKey("mount", "-o", diskplan.PrivateFATMasks, source, target)
	found := false
	for _, run := range r.runs {
		if run == wantMount {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected mount command %q, got %q", wantMount, r.runs)
	}
}

// Plane 434: the installed /boot holds the boot loader random seed, so a
// mount that the kernel took without the masks stops the stage.
func TestPrepareBootRejectsOwnMountWithoutMasks(t *testing.T) {
	dir := t.TempDir()
	source := "/dev/disk/by-uuid/TEST-ESP"
	target := filepath.Join(dir, "boot")
	findmntKey := commandKey(
		"findmnt",
		"-nvro", "SOURCE,OPTIONS",
		"--mountpoint", target,
	)

	writeNixOSFstab(t, dir, source)

	r := &fakeRunner{
		outputs: map[string][]byte{},
		errors:  map[string]error{},
	}

	err := prepareBoot(context.Background(), dir, &mountingRunner{
		fakeRunner: r,
		findmntKey: findmntKey,
		options:    "rw,relatime,fmask=0022,dmask=0022,codepage=437",
	})
	if err == nil || !strings.Contains(err.Error(), "without fmask=0077") {
		t.Fatalf("mount without masks accepted: %v", err)
	}
}

type mountingRunner struct {
	fakeRunner *fakeRunner
	findmntKey string
	lookups    int
	// options is what findmnt lists after the mount; empty means a
	// mount with diskplan.PrivateFATMasks.
	options string
}

func (r *mountingRunner) Output(
	ctx context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	key := commandKey(name, args...)

	if key == r.findmntKey {
		r.lookups++
		if r.lookups == 1 {
			return nil, fakeExitError{code: 1}
		}
		options := r.options
		if options == "" {
			options = "rw,relatime," + diskplan.PrivateFATMasks + ",codepage=437"
		}
		return []byte("/dev/disk/by-uuid/TEST-ESP " + options + "\n"), nil
	}

	return r.fakeRunner.Output(ctx, name, args...)
}

func (r *mountingRunner) Run(
	ctx context.Context,
	name string,
	args ...string,
) error {
	return r.fakeRunner.Run(ctx, name, args...)
}

func TestPrepareBootRejectsReadOnlyExistingMount(t *testing.T) {
	dir := t.TempDir()
	source := "/dev/disk/by-uuid/TEST-ESP"
	resolved := "/dev/nvme0n1p1"
	target := filepath.Join(dir, "boot")

	writeNixOSFstab(t, dir, source)

	r := &fakeRunner{
		outputs: map[string][]byte{
			commandKey(
				"findmnt",
				"-nvro", "SOURCE,OPTIONS",
				"--mountpoint", target,
			): []byte(source + " ro,relatime\n"),
			commandKey(
				"readlink",
				"-f", "--", source,
			): []byte(resolved + "\n"),
		},
		errors: map[string]error{},
	}

	err := prepareBoot(context.Background(), dir, r)
	if err == nil || !strings.Contains(err.Error(), "not mounted read-write") {
		t.Fatalf("expected read-only mount rejection, got %v", err)
	}
}

// Loophole pass for Plane 434: a planted boot link on the installed root
// must stop the stage before mount, so root never mounts the ESP over a
// path outside the installed root.
func TestPrepareBootRejectsPlantedBootLink(t *testing.T) {
	for name, link := range map[string]string{
		"absolute": "",
		"relative": "../outside",
	} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "root")
			outside := filepath.Join(parent, "outside")
			if err := os.MkdirAll(outside, 0o755); err != nil {
				t.Fatal(err)
			}
			writeNixOSFstab(t, dir, "/dev/disk/by-uuid/TEST-ESP")
			if link == "" {
				link = outside
			}
			if err := os.Symlink(link, filepath.Join(dir, "boot")); err != nil {
				t.Fatal(err)
			}

			r := &fakeRunner{
				outputs: map[string][]byte{},
				errors:  map[string]error{},
			}
			err := prepareBoot(context.Background(), dir, r)
			if err == nil {
				t.Fatal("planted boot link accepted")
			}
			if !strings.Contains(err.Error(), "escapes") {
				t.Fatalf("planted boot link failed for another reason: %v", err)
			}
			for _, run := range r.runs {
				if strings.HasPrefix(run, commandKey("mount")) {
					t.Fatalf("mount ran through a planted link: %q", r.runs)
				}
			}
		})
	}
}
