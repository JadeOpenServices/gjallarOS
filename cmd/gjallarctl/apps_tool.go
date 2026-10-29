package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/JadeOpenServices/gjallarOS/apps"
)

const appsUsage = "Usage: gjallarctl apps list [--json] | add ID [--window] | remove ID [--window]"

// catalogueEntry is one app as the app window shows it (Plane 220).
// Installed is null when the settings service does not answer.
type catalogueEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Installed   *bool  `json:"installed"`
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
	selected, known := selectedApps(stderr)
	entries := catalogueEntries(catalogue, selected, known)
	if asJSON {
		data, err := json.Marshal(map[string]any{"apps": entries})
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
		fmt.Fprintf(stdout, "%s %-14s %-14s %s\n", mark, e.ID, e.Category, e.Description)
	}
	return 0
}

func catalogueEntries(catalogue []apps.App, selected []string, known bool) []catalogueEntry {
	entries := make([]catalogueEntry, 0, len(catalogue))
	for _, app := range catalogue {
		e := catalogueEntry{ID: app.ID, Name: app.Name, Description: app.Description, Category: app.Category}
		if known {
			installed := slices.Contains(selected, app.ID)
			e.Installed = &installed
		}
		entries = append(entries, e)
	}
	return entries
}

// selectedApps reads the apps setting; known is false when the settings
// service does not answer.
func selectedApps(stderr io.Writer) (selected []string, known bool) {
	bus, ok := openSettings(stderr)
	if !ok {
		return nil, false
	}
	defer bus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key, err := settingsKey(ctx, bus, "apps")
	if err == nil {
		err = json.Unmarshal(key.Value, &selected)
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: read apps: %v\n", err)
		return nil, false
	}
	return selected, true
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
