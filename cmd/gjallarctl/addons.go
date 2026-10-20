package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/apps"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/firefoxstore"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installercheck"
	"github.com/JadeOpenServices/gjallarOS/internal/managedapps"
)

const addonsUsage = `usage: gjallarctl addons list [--names] [--repo PATH]
       gjallarctl addons set COLLECTION on|off [--repo PATH]
       gjallarctl addons set COLLECTION.ADDON off|on|locked [--repo PATH]
       gjallarctl addons set STORE-ID off|on|locked [--repo PATH]
       gjallarctl addons pin STORE-ID [--mode on|locked] [--yes] [--repo PATH]
                                           add an add-on from addons.mozilla.org
       gjallarctl addons update [STORE-ID...] [--yes] [--repo PATH]
                                           pin the newest version; shows new permissions
       gjallarctl addons reconcile firefox --desired FILE --profile DIR
                                           remove add-ons GjallarOS installed and
                                           no longer wants (run by the session)

Modes: on installs the add-on and lets you turn it off in Firefox, locked
keeps it on, off removes it again. Changes go to user.config.json; run
gjallarctl rebuild to apply them.`

var addonsPin = firefoxstore.Pin

type addonsArgs struct {
	repo, mode, desired, profile string
	yes, names                   bool
	rest                         []string
}

func parseAddonsArgs(args []string) (addonsArgs, error) {
	var a addonsArgs
	value := func(i *int, name string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s needs a value", name)
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		var err error
		switch args[i] {
		case "--repo":
			a.repo, err = value(&i, "--repo")
		case "--mode":
			a.mode, err = value(&i, "--mode")
		case "--desired":
			a.desired, err = value(&i, "--desired")
		case "--profile":
			a.profile, err = value(&i, "--profile")
		case "--yes", "-y":
			a.yes = true
		case "--names":
			a.names = true
		default:
			if strings.HasPrefix(args[i], "-") {
				return a, fmt.Errorf("unknown option %s", args[i])
			}
			a.rest = append(a.rest, args[i])
		}
		if err != nil {
			return a, err
		}
	}
	return a, nil
}

func runAddons(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stdout, addonsUsage)
		return 0
	}
	a, err := parseAddonsArgs(args[1:])
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n%s\n", err, addonsUsage)
		return 2
	}
	if args[0] == "reconcile" {
		return runAddonsReconcile(a, stdout, stderr)
	}
	if args[0] != "list" && os.Geteuid() == 0 {
		fmt.Fprintln(stderr, "ERROR: run gjallarctl addons as your user; user.config.json belongs to you")
		return 1
	}
	repo, err := installercheck.DiscoverRepository(a.repo)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: resolve GjallarOS repository: %v\n", err)
		return 1
	}
	path := filepath.Join(repo, "user.config.json")
	ui := prompt.UI{Reader: bufio.NewReader(os.Stdin), Out: stdout}

	switch args[0] {
	case "list":
		err = addonsList(path, a.names, stdout)
	case "set":
		err = addonsSet(path, a.rest, stdout)
	case "pin":
		err = addonsPinNew(context.Background(), ui, path, a, stdout)
	case "update":
		err = addonsUpdate(context.Background(), ui, path, a, stdout)
	default:
		fmt.Fprintf(stderr, "ERROR: unknown addons command %q\n%s\n", args[0], addonsUsage)
		return 2
	}
	if err != nil {
		if !errors.Is(err, errShown) {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
		}
		return 1
	}
	return 0
}

func addonsList(path string, names bool, out io.Writer) error {
	user, err := config.Load(path)
	if err != nil {
		return err
	}
	if names {
		for _, c := range apps.FirefoxCollections() {
			fmt.Fprintln(out, c.ID)
			for _, a := range c.Addons {
				fmt.Fprintln(out, c.ID+"."+a.ID)
			}
		}
		for _, s := range user.FirefoxStoreAddons {
			fmt.Fprintln(out, s.ID)
		}
		return nil
	}
	for _, c := range apps.FirefoxCollections() {
		choice := user.FirefoxAddons[c.ID]
		state := "off"
		if choice.Enable {
			state = "on"
		}
		fmt.Fprintf(out, "%s (%s): %s\n", c.ID, c.Name, state)
		for _, a := range c.Addons {
			fmt.Fprintf(out, "  %-26s %-7s %s\n", a.ID, choice.Addons[a.ID], a.Name)
		}
	}
	if len(user.FirefoxStoreAddons) == 0 {
		fmt.Fprintln(out, "store add-ons: none (gjallarctl addons pin STORE-ID adds one)")
		return nil
	}
	fmt.Fprintln(out, "store add-ons:")
	for _, s := range user.FirefoxStoreAddons {
		fmt.Fprintf(out, "  %-26s %-7s %s %s\n", s.ID, s.Mode, s.Name, s.Version)
	}
	return nil
}

func addonsSet(path string, rest []string, out io.Writer) error {
	if len(rest) != 2 {
		return errors.New("set needs a target and a mode; see gjallarctl addons --help")
	}
	target, mode := rest[0], rest[1]
	user, err := config.Load(path)
	if err != nil {
		return err
	}

	if apps.ValidStoreAddonID(target) {
		if !slices.Contains(apps.AddonModes(), mode) {
			return fmt.Errorf("mode must be one of %v", apps.AddonModes())
		}
		i := slices.IndexFunc(user.FirefoxStoreAddons, func(s apps.FirefoxStoreAddon) bool { return s.ID == target })
		if i < 0 {
			if err := apps.CheckStoreAddonID(target); err != nil {
				return err
			}
			return fmt.Errorf("%s is not pinned yet; add it with gjallarctl addons pin %s", target, target)
		}
		user.FirefoxStoreAddons[i].Mode = mode
		return saveAddons(path, user, "firefoxStoreAddons", out)
	}

	collection, addon, isAddon := strings.Cut(target, ".")
	choice, ok := user.FirefoxAddons[collection]
	if !ok {
		return fmt.Errorf("no collection %q; gjallarctl addons list shows them", collection)
	}
	if !isAddon {
		if mode != "on" && mode != "off" {
			return errors.New("a collection is on or off")
		}
		choice.Enable = mode == "on"
		if choice.Enable && !slices.ContainsFunc(slices.Collect(maps.Values(choice.Addons)), func(m string) bool { return m != apps.AddonOff }) {
			fmt.Fprintf(out, "Every add-on in %s is off; turn one on with gjallarctl addons set %s.ADDON on\n", collection, collection)
		}
	} else {
		if _, ok := choice.Addons[addon]; !ok {
			return fmt.Errorf("collection %s has no add-on %q", collection, addon)
		}
		if !slices.Contains(apps.AddonModes(), mode) {
			return fmt.Errorf("mode must be one of %v", apps.AddonModes())
		}
		choice.Addons[addon] = mode
		if mode != apps.AddonOff && !choice.Enable {
			choice.Enable = true
			fmt.Fprintf(out, "Turned the %s collection on as well.\n", collection)
		}
	}
	user.FirefoxAddons[collection] = choice
	return saveAddons(path, user, "firefoxAddons", out)
}

func addonsPinNew(ctx context.Context, ui prompt.UI, path string, a addonsArgs, out io.Writer) error {
	if len(a.rest) != 1 {
		return errors.New("pin needs one add-on ID; about:support lists the ID of each installed add-on")
	}
	id := a.rest[0]
	mode := a.mode
	if mode == "" {
		mode = apps.AddonOn
	}
	if mode != apps.AddonOn && mode != apps.AddonLocked {
		return errors.New("--mode is on or locked")
	}
	if err := apps.CheckStoreAddonID(id); err != nil {
		return err
	}
	user, err := config.Load(path)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(user.FirefoxStoreAddons, func(s apps.FirefoxStoreAddon) bool { return s.ID == id }) {
		return fmt.Errorf("%s is pinned already; use gjallarctl addons update %s or set %s MODE", id, id, id)
	}
	pinned, err := addonsPin(ctx, id, mode)
	if err != nil {
		return err
	}
	question := fmt.Sprintf("Install %s %s (%s)? It may use: %s", pinned.Name, pinned.Version, id, addonPermissions(pinned.Permissions))
	if ok, err := addonsConfirm(ctx, ui, a.yes, question, out); err != nil || !ok {
		return err
	}
	user.FirefoxStoreAddons = append(user.FirefoxStoreAddons, pinned)
	return saveAddons(path, user, "firefoxStoreAddons", out)
}

func addonsUpdate(ctx context.Context, ui prompt.UI, path string, a addonsArgs, out io.Writer) error {
	user, err := config.Load(path)
	if err != nil {
		return err
	}
	for _, id := range a.rest {
		if !slices.ContainsFunc(user.FirefoxStoreAddons, func(s apps.FirefoxStoreAddon) bool { return s.ID == id }) {
			return fmt.Errorf("%s is not pinned", id)
		}
	}
	changed := false
	for i, old := range user.FirefoxStoreAddons {
		if len(a.rest) > 0 && !slices.Contains(a.rest, old.ID) {
			continue
		}
		pinned, err := addonsPin(ctx, old.ID, old.Mode)
		if err != nil {
			return err
		}
		c := firefoxstore.Compare(old, pinned)
		if c.SameFile {
			fmt.Fprintf(out, "%s %s is the newest version.\n", old.ID, old.Version)
			continue
		}
		question := fmt.Sprintf("Update %s from %s to %s?", old.ID, c.From, c.To)
		if len(c.Added) > 0 {
			question += " New permissions: " + strings.Join(c.Added, ", ") + "."
		}
		if len(c.Removed) > 0 {
			question += " No longer asks for: " + strings.Join(c.Removed, ", ") + "."
		}
		ok, err := addonsConfirm(ctx, ui, a.yes, question, out)
		if err != nil {
			return err
		}
		if ok {
			user.FirefoxStoreAddons[i] = pinned
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return saveAddons(path, user, "firefoxStoreAddons", out)
}

func addonsConfirm(ctx context.Context, ui prompt.UI, yes bool, question string, out io.Writer) (bool, error) {
	if yes {
		fmt.Fprintln(out, question+" yes (--yes)")
		return true, nil
	}
	ok, err := ui.Confirm(ctx, question, false)
	if errors.Is(err, io.EOF) {
		return false, errors.New("no answer on standard input; pass --yes to accept")
	}
	if err == nil && !ok {
		fmt.Fprintln(out, "Nothing was changed.")
	}
	return ok, err
}

func addonPermissions(p []string) string {
	if len(p) == 0 {
		return "no special permissions"
	}
	return strings.Join(p, ", ")
}

func saveAddons(path string, user config.User, key string, out io.Writer) error {
	if err := apps.ValidateFirefoxAddons(user.FirefoxAddons, user.FirefoxStoreAddons); err != nil {
		return err
	}
	var value any = user.FirefoxAddons
	if key == "firefoxStoreAddons" {
		value = user.FirefoxStoreAddons
	}
	if onSystemCheckout(path) {
		if askSystemSettings(map[string]any{key: value}, out, os.Stderr) != 0 {
			return errShown
		}
		return nil
	}
	if err := config.SetValue(path, key, value); err != nil {
		return err
	}
	fmt.Fprintln(out, "Saved in user.config.json. Run gjallarctl rebuild to apply it.")
	return nil
}

func runAddonsReconcile(a addonsArgs, stdout, stderr io.Writer) int {
	if len(a.rest) != 1 || a.rest[0] != "firefox" || a.desired == "" || a.profile == "" {
		fmt.Fprintln(stderr, addonsUsage)
		return 2
	}
	if os.Geteuid() == 0 {
		fmt.Fprintln(stderr, "ERROR: reconcile runs as the user who owns the profile")
		return 1
	}
	res, err := reconcileFirefox(a.desired, a.profile)
	if errors.Is(err, os.ErrNotExist) {
		// No profile yet: Firefox never started, so there is nothing to remove.
		fmt.Fprintf(stdout, "Nothing to do: %v\n", err)
		return 0
	}
	return reportReconcile(res, err, stdout, stderr)
}

func reconcileFirefox(desiredPath, profile string) (managedapps.Result, error) {
	desired, err := managedapps.ReadDesired(desiredPath)
	if err != nil {
		return managedapps.Result{}, err
	}
	ff, err := managedapps.NewFirefox(profile)
	if err != nil {
		return managedapps.Result{}, err
	}
	state, err := managedapps.StateDir()
	if err != nil {
		return managedapps.Result{}, err
	}
	return managedapps.Reconcile(state, ff, desired)
}

func reportReconcile(res managedapps.Result, err error, stdout, stderr io.Writer) int {
	for _, id := range res.Removed {
		fmt.Fprintf(stdout, "Removed %s; Firefox drops it on its next start.\n", id)
	}
	for _, id := range res.Left {
		fmt.Fprintf(stdout, "Left %s: GjallarOS did not install it.\n", id)
	}
	if res.Busy {
		fmt.Fprintln(stdout, "Firefox is running; add-ons that were turned off go at the next login.")
	}
	for _, id := range slices.Sorted(maps.Keys(res.Failed)) {
		fmt.Fprintf(stderr, "WARN: %s stays for the next run: %v\n", id, res.Failed[id])
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if len(res.Failed) > 0 {
		return 1
	}
	return 0
}
