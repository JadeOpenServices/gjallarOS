package winesandbox

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Landlock scopes from ABI 6 on; x/sys has no names for them yet.
const (
	landlockScopeAbstractSocket = 1 << 0
	landlockScopeSignal         = 1 << 1
)

// landlockRuleset carries the scoped field that x/sys's
// LandlockRulesetAttr lacks.
type landlockRuleset struct {
	accessFS  uint64
	accessNet uint64
	scoped    uint64
}

// ExecLocked starts argv with Landlock on: symbolic links may be made only
// below symDirs, files may be moved or linked across directories only
// below symDirs or referDirs, and from ABI 6 on the program can neither
// reach abstract unix sockets nor signal processes outside the sandbox.
// It returns only on error.
//
// Landlock and no_new_privs hold for the calling thread only, so the
// thread is pinned until exec replaces the process.
func ExecLocked(symDirs, referDirs []string, argv, env []string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return fmt.Errorf("landlock is not available: %w", errno)
	}
	attr := landlockRuleset{accessFS: unix.LANDLOCK_ACCESS_FS_MAKE_SYM | unix.LANDLOCK_ACCESS_FS_REFER}
	size := unsafe.Offsetof(attr.scoped)
	if abi >= 6 {
		attr.scoped = landlockScopeAbstractSocket | landlockScopeSignal
		size = unsafe.Sizeof(attr)
	}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), size, 0)
	if errno != 0 {
		return fmt.Errorf("create landlock ruleset: %w", errno)
	}
	ruleset := int(fd)
	defer unix.Close(ruleset)

	add := func(dir string, allowed uint64) error {
		dirFD, err := unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("open %s: %w", dir, err)
		}
		defer unix.Close(dirFD)
		rule := unix.LandlockPathBeneathAttr{Allowed_access: allowed, Parent_fd: int32(dirFD)}
		if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&rule)), 0, 0, 0); errno != 0 {
			return fmt.Errorf("landlock rule for %s: %w", dir, errno)
		}
		return nil
	}
	for _, dir := range symDirs {
		if err := add(dir, unix.LANDLOCK_ACCESS_FS_MAKE_SYM|unix.LANDLOCK_ACCESS_FS_REFER); err != nil {
			return err
		}
	}
	for _, dir := range referDirs {
		if err := add(dir, unix.LANDLOCK_ACCESS_FS_REFER); err != nil {
			return err
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(ruleset), 0, 0); errno != 0 {
		return fmt.Errorf("landlock restrict: %w", errno)
	}
	if err := unix.Exec(argv[0], argv, env); err != nil {
		return fmt.Errorf("start %s: %w", argv[0], err)
	}
	return nil
}
