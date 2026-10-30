package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os/user"
	"path/filepath"
	"slices"

	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

// gjallarctl wine shows the Wine apps and sets their network switches in
// the settings service (Plane 272, wiki 746). gjallar-wine reads the same
// switches when it starts an app.

const wineUsage = `usage: gjallarctl wine list [--ids]              your Wine apps and their switches
       gjallarctl wine set ID [--internet on|off] [--home on|off]
                                                switch app ID's network
Internet lets the app reach the internet. Home lets it reach your tailnet,
and your home network's devices while this device is at home. Turning a
switch on asks your password; turning it off does not.`

// wineHome finds the user's home without symbolic links; tests replace it.
var wineHome = func() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(u.HomeDir)
}

func runWine(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, wineUsage)
		return 2
	}
	switch {
	case args[0] == "list" && len(args) == 1:
		return wineList(false, stdout, stderr)
	case args[0] == "list" && len(args) == 2 && args[1] == "--ids":
		return wineList(true, stdout, stderr)
	case args[0] == "set" && len(args) >= 2:
		return wineSet(args[1], args[2:], stdout, stderr)
	}
	fmt.Fprintln(stderr, wineUsage)
	return 2
}

func wineList(idsOnly bool, stdout, stderr io.Writer) int {
	home, err := wineHome()
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	apps, err := winesandbox.Apps(home)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if idsOnly {
		for _, a := range apps {
			fmt.Fprintln(stdout, a.ID)
		}
		return 0
	}
	programs, err := wineSwitches(stderr)
	if err != nil && !errors.Is(err, errShown) {
		// The apps still show; they stay offline until the service answers.
		fmt.Fprintf(stderr, "ERROR: read the switches: %v\n", err)
	}
	if len(apps) == 0 && len(programs) == 0 {
		fmt.Fprintln(stdout, "No Wine apps yet.")
		return wineExit(err)
	}
	for _, a := range apps {
		if a.Err != nil {
			fmt.Fprintf(stdout, "%s  cannot start: %v\n", a.ID, a.Err)
			continue
		}
		fmt.Fprintf(stdout, "%s  %s  %s\n", a.ID, a.App.Name, describeWine(programs[a.ID]))
	}
	// Switches of an app whose record is gone stay until turned off.
	for _, id := range slices.Sorted(maps.Keys(programs)) {
		if !slices.ContainsFunc(apps, func(a winesandbox.Listed) bool { return a.ID == id }) {
			fmt.Fprintf(stdout, "%s  (removed)  %s; gjallarctl wine set %s --internet off --home off clears it\n",
				id, describeWine(programs[id]), id)
		}
	}
	return wineExit(err)
}

func wineExit(err error) int {
	if err != nil {
		return 1
	}
	return 0
}

func describeWine(f winesandbox.Flags) string {
	onOff := func(on bool) string {
		if on {
			return "on"
		}
		return "off"
	}
	return "internet " + onOff(f.Internet) + ", home network " + onOff(f.Home)
}

// wineSwitches reads the caller's switches from the settings service.
func wineSwitches(stderr io.Writer) (map[string]winesandbox.Flags, error) {
	bus, ok := openSettings(stderr)
	if !ok {
		return nil, errShown
	}
	defer bus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), settingsCallTimeout)
	defer cancel()
	answer, err := bus.Call(ctx, "Wine")
	if err != nil {
		return nil, err
	}
	var programs map[string]winesandbox.Flags
	if err := json.Unmarshal([]byte(answer), &programs); err != nil {
		return nil, fmt.Errorf("settings service answer: %w", err)
	}
	return programs, nil
}

func wineSet(id string, args []string, stdout, stderr io.Writer) int {
	internet, home, err := parseWineFlags(args)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n%s\n", err, wineUsage)
		return 2
	}
	if !winesandbox.ValidID(id) {
		fmt.Fprintf(stderr, "ERROR: %q is not an app id; gjallarctl wine list shows them\n", id)
		return 2
	}
	// An app that is gone may still have switches; those can go off.
	if (internet != nil && *internet) || (home != nil && *home) {
		dir, err := wineHome()
		if err == nil {
			_, _, err = winesandbox.LoadApp(dir, id)
		}
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
	}

	bus, ok := openSettings(stderr)
	if !ok {
		return 1
	}
	defer bus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), settingsCallTimeout)
	defer cancel()
	answer, err := bus.Call(ctx, "Wine")
	var programs map[string]winesandbox.Flags
	if err == nil {
		err = json.Unmarshal([]byte(answer), &programs)
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: read the switches: %v\n", err)
		return 1
	}
	want := programs[id]
	if internet != nil {
		want.Internet = *internet
	}
	if home != nil {
		want.Home = *home
	}
	answer, err = bus.Call(ctx, "SetWine", id, want.Internet, want.Home)
	if err == nil {
		err = json.Unmarshal([]byte(answer), &programs)
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s: %s\n", id, describeWine(programs[id]))
	return 0
}

// parseWineFlags reads --internet and --home; a switch not named stays as
// it is.
func parseWineFlags(args []string) (internet, home *bool, err error) {
	for len(args) > 0 {
		if len(args) < 2 {
			return nil, nil, fmt.Errorf("%s needs on or off", args[0])
		}
		var on bool
		switch args[1] {
		case "on":
			on = true
		case "off":
		default:
			return nil, nil, fmt.Errorf("%s needs on or off, not %q", args[0], args[1])
		}
		switch args[0] {
		case "--internet":
			if internet != nil {
				return nil, nil, errors.New("--internet given twice")
			}
			internet = &on
		case "--home":
			if home != nil {
				return nil, nil, errors.New("--home given twice")
			}
			home = &on
		default:
			return nil, nil, fmt.Errorf("unknown option %q", args[0])
		}
		args = args[2:]
	}
	if internet == nil && home == nil {
		return nil, nil, errors.New("name --internet or --home")
	}
	return internet, home, nil
}
