package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/JadeOpenServices/gjallarOS/apps"
)

const appsUsage = "Usage: gjallarctl apps list [--json] | add ID [--window] | remove ID [--window]"

// catalogueEntry is one app as the app window shows it (Plane 220).
// Installed is the saved choice and is null when the settings service does
// not answer; the list's "settings" field then says why. Built is whether
// the running system has the app, null when that is not recorded.
type catalogueEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Icon        string `json:"icon"`
	Installed   *bool  `json:"installed"`
	Built       *bool  `json:"built"`
}

// appsBuiltPath lists the apps the running system was built with.
var appsBuiltPath = "/etc/gjallar/apps.json"

// builtApps reads appsBuiltPath; known is false on a system built before
// it was written.
func builtApps() (built []string, known bool) {
	data, err := os.ReadFile(appsBuiltPath)
	if err != nil || json.Unmarshal(data, &built) != nil {
		return nil, false
	}
	return built, true
}

func runApps(args []string, stdout, stderr io.Writer) int {
	// --window runs the change in a terminal, so the app window's button
	// shows the password prompt and the build output.
	if len(args) == 3 && args[2] == "--window" && (args[0] == "add" || args[0] == "remove") {
		return runInTerminal("GjallarOS apps", []string{"apps", args[0], args[1]}, stderr)
	}
	switch {
	case len(args) >= 1 && args[0] == "list" && len(args) <= 2:
		asJSON := len(args) == 2 && args[1] == "--json"
		if len(args) == 2 && !asJSON {
			break
		}
		return runAppsList(asJSON, stdout, stderr)
	case len(args) == 2 && args[0] == "add" && args[1] == "ai":
		// Local AI checks the memory first.
		return runAIEnable(nil, stdout, stderr)
	case len(args) == 2 && (args[0] == "add" || args[0] == "remove"):
		return runAppsChange(args[1], args[0] == "add", stdout, stderr)
	}
	fmt.Fprintln(stderr, appsUsage)
	return 2
}

func runAppsList(asJSON bool, stdout, stderr io.Writer) int {
	catalogue, err := apps.Catalogue()
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	selected, state, rebuilding := selectedApps(stderr)
	entries := catalogueEntries(catalogue, selected, state == settingsAnswered)
	if built, ok := builtApps(); ok {
		for i := range entries {
			has := slices.Contains(built, entries[i].ID)
			entries[i].Built = &has
		}
	}
	if asJSON {
		data, err := json.Marshal(map[string]any{"apps": entries, "settings": state, "rebuilding": rebuilding})
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	for _, e := range entries {
		mark := "?"
		if e.Installed != nil && *e.Installed {
			mark = "*"
		} else if e.Installed != nil {
			mark = " "
		}
		fmt.Fprintf(stdout, "%s %-14s %-14s %s%s\n", mark, e.ID, e.Category, e.Description, appWaits(e, rebuilding))
	}
	return 0
}

// appWaits says when a saved choice is not built yet.
func appWaits(e catalogueEntry, rebuilding bool) string {
	if e.Installed == nil || e.Built == nil || *e.Installed == *e.Built {
		return ""
	}
	switch {
	case *e.Installed && rebuilding:
		return " (installing now)"
	case *e.Installed:
		return " (installed with the next rebuild)"
	case rebuilding:
		return " (removing now)"
	}
	return " (removed with the next rebuild)"
}

func catalogueEntries(catalogue []apps.App, selected []string, known bool) []catalogueEntry {
	entries := make([]catalogueEntry, 0, len(catalogue))
	for _, app := range catalogue {
		e := catalogueEntry{ID: app.ID, Name: app.Name, Description: app.Description, Category: app.Category, Icon: app.Icon}
		if known {
			installed := slices.Contains(selected, app.ID)
			e.Installed = &installed
		}
		entries = append(entries, e)
	}
	return entries
}

// What selectedApps learned from the settings service.
const (
	settingsAnswered = "ok"
	// settingsNotThere: no settings service, the system still builds from
	// the user's own checkout.
	settingsNotThere = "missing"
	settingsFailed   = "failed"
)

// selectedApps reads the apps setting, says whether the settings service
// answered and whether a rebuild is running.
func selectedApps(stderr io.Writer) (selected []string, state string, rebuilding bool) {
	bus, ok := openSettings(stderr)
	if !ok {
		return nil, settingsFailed, false
	}
	defer bus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key, err := settingsKey(ctx, bus, "apps")
	if err == nil {
		err = json.Unmarshal(key.Value, &selected)
	}
	switch {
	case errors.Is(err, errSettingsMissing):
		fmt.Fprintf(stderr, "ERROR: read apps: %v\n", err)
		return nil, settingsNotThere, false
	case err != nil:
		fmt.Fprintf(stderr, "ERROR: read apps: %v\n", err)
		return nil, settingsFailed, false
	}
	rebuilding, _, err = bus.Rebuild(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "rebuild state: %v\n", err)
	}
	return selected, settingsAnswered, rebuilding
}

// runAppsChange asks the settings service for the apps list with one app
// added or removed and applies it, as `settings set` does.
func runAppsChange(id string, add bool, stdout, stderr io.Writer) int {
	if err := apps.Validate([]string{id}); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 2
	}
	bus, ok := openSettings(stderr)
	if !ok {
		return 1
	}
	defer bus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), settingsCallTimeout)
	defer cancel()
	key, err := settingsKey(ctx, bus, "apps")
	var selected []string
	if err == nil {
		err = json.Unmarshal(key.Value, &selected)
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: read apps: %v\n", err)
		return 1
	}
	has := slices.Contains(selected, id)
	switch {
	case add && has:
		fmt.Fprintf(stdout, "%s is already installed.\n", id)
		return 0
	case !add && !has:
		fmt.Fprintf(stdout, "%s is not installed.\n", id)
		return 0
	case add:
		selected = append(selected, id)
	default:
		selected = slices.DeleteFunc(selected, func(s string) bool { return s == id })
	}
	request, err := json.Marshal(selected)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	return settingsRequest(ctx, bus, map[string]json.RawMessage{"apps": request}, stdout, stderr)
}
