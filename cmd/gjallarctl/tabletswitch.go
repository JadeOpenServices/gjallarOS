package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Linux input event codes (linux/input-event-codes.h).
const (
	evSyn        = 0x00
	evSw         = 0x05
	synDropped   = 3
	swTabletMode = 0x01
	// struct input_event on 64-bit: timeval (16 bytes), type, code, value.
	inputEventSize = 24
)

// tabletSwitchState is what the tablet mode switch says right now. present is
// false while the device is missing or cannot be opened.
type tabletSwitchState struct {
	present bool
	on      bool
}

// findSwitchDevice returns the /dev/input/event* node whose input device has
// exactly this name, or "" when there is none.
func findSwitchDevice(sysRoot, devRoot, name string) string {
	names, _ := filepath.Glob(filepath.Join(sysRoot, "class/input/event*/device/name"))
	for _, path := range names {
		data, err := os.ReadFile(path)
		if err != nil || strings.TrimSpace(string(data)) != name {
			continue
		}
		event := filepath.Base(filepath.Dir(filepath.Dir(path)))
		return filepath.Join(devRoot, event)
	}
	return ""
}

// tabletModeOn asks the device for its current SW_TABLET_MODE state
// (EVIOCGSW, the switch bitmap).
func tabletModeOn(f *os.File) (bool, error) {
	var bits [8]byte
	request := uintptr(2<<30 | len(bits)<<16 | 'E'<<8 | 0x1b)
	conn, err := f.SyscallConn()
	if err != nil {
		return false, err
	}
	var errno syscall.Errno
	if err := conn.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(unsafe.Pointer(&bits[0])))
	}); err != nil {
		return false, err
	}
	if errno != 0 {
		return false, fmt.Errorf("EVIOCGSW: %w", errno)
	}
	return bits[swTabletMode/8]&(1<<(swTabletMode%8)) != 0, nil
}

// switchEvent reads one input_event record. It reports the new switch state
// for SW_TABLET_MODE, or resync when the kernel dropped events and the state
// has to be asked again.
func switchEvent(record []byte) (on, changed, resync bool) {
	kind := binary.NativeEndian.Uint16(record[16:18])
	code := binary.NativeEndian.Uint16(record[18:20])
	value := int32(binary.NativeEndian.Uint32(record[20:24]))
	switch {
	case kind == evSw && code == swTabletMode:
		return value != 0, true, false
	case kind == evSyn && code == synDropped:
		return false, false, true
	}
	return false, false, false
}

// readTabletSwitch sends the switch state now and after every change until
// the device goes away.
func readTabletSwitch(f *os.File, states chan<- tabletSwitchState) error {
	on, err := tabletModeOn(f)
	if err != nil {
		return err
	}
	states <- tabletSwitchState{present: true, on: on}

	buf := make([]byte, inputEventSize*64)
	for {
		n, err := f.Read(buf)
		if err != nil {
			return err
		}
		for i := 0; i+inputEventSize <= n; i += inputEventSize {
			next, changed, resync := switchEvent(buf[i : i+inputEventSize])
			if resync {
				if next, err = tabletModeOn(f); err != nil {
					return err
				}
				changed = true
			}
			if changed && next != on {
				on = next
				states <- tabletSwitchState{present: true, on: on}
			}
		}
	}
}

// watchTabletSwitch follows the named switch device. It finds the device
// again when it is created later, when its permissions change (logind adds
// the seat user's access after creation), or when it goes away and comes
// back (ODDC restarting its tablet mode service).
func watchTabletSwitch(sysRoot, devRoot, name string, states chan<- tabletSwitchState, stderr io.Writer) {
	inotify, err := syscall.InotifyInit1(syscall.IN_CLOEXEC)
	if err == nil {
		_, err = syscall.InotifyAddWatch(inotify, devRoot, syscall.IN_CREATE|syscall.IN_ATTRIB)
	}
	if err != nil {
		fmt.Fprintf(stderr, "WARN: watch %s: %v; a tablet mode switch added later is not seen\n", devRoot, err)
		inotify = -1
	}

	lastProblem := ""
	report := func(problem string) {
		if problem != lastProblem {
			fmt.Fprintf(stderr, "WARN: tablet mode switch %q: %s\n", name, problem)
			lastProblem = problem
		}
	}
	events := make([]byte, 4096)
	for {
		problem := "not found"
		if path := findSwitchDevice(sysRoot, devRoot, name); path != "" {
			f, err := os.Open(path)
			if err == nil {
				// One line after a problem, so a log that ends in a warning
				// means the switch is still unusable.
				if lastProblem != "" {
					fmt.Fprintf(stderr, "tablet mode switch %q: readable again\n", name)
				}
				lastProblem = ""
				err = readTabletSwitch(f, states)
				f.Close()
			}
			problem = err.Error()
		}
		report(problem)
		states <- tabletSwitchState{}

		if inotify < 0 {
			return
		}
		if _, err := syscall.Read(inotify, events); err != nil {
			fmt.Fprintf(stderr, "WARN: watch %s: %v\n", devRoot, err)
			return
		}
	}
}

// rotationGate decides when the sensor may turn the screen. Without a switch
// it always may. With one, only while the switch says tablet mode; leaving
// tablet mode brings back the configured orientation.
type rotationGate struct {
	useSwitch bool
	require   bool // the model has the switch, so a missing one means laptop mode
	known     bool
	tablet    bool
	current   string
	rotated   bool
}

type rotationAction int

const (
	rotationNone rotationAction = iota
	rotationApply
	rotationRestore
)

func newRotationGate(useSwitch, require bool) *rotationGate {
	return &rotationGate{useSwitch: useSwitch, require: require, known: !useSwitch, tablet: !useSwitch}
}

func (g *rotationGate) allowed() bool {
	return g.known && g.tablet
}

func (g *rotationGate) orientation(value string) rotationAction {
	g.current = value
	if g.allowed() {
		return rotationApply
	}
	return rotationNone
}

func (g *rotationGate) switchState(state tabletSwitchState) rotationAction {
	if state.present {
		// Once seen, the model has a switch; losing it later means laptop mode.
		g.require = true
	}
	was := g.allowed()
	g.known = true
	g.tablet = state.on || !state.present && !g.require
	switch {
	case g.allowed() && !was && g.current != "":
		return rotationApply
	case !g.allowed() && g.rotated:
		return rotationRestore
	}
	return rotationNone
}

// reloaded is a Hyprland config reload: the configured orientation is back.
func (g *rotationGate) reloaded() rotationAction {
	g.rotated = false
	if g.allowed() && g.current != "" {
		return rotationApply
	}
	return rotationNone
}

func (g *rotationGate) applied() {
	g.rotated = true
}
