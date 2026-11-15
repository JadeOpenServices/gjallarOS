package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/JadeOpenServices/gjallarOS/internal/input/xkb"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/region"
)

// liveRegion reads what the live session uses; tests replace it so they
// never see the machine they run on.
var liveRegion = region.Live

// collectRegion is the region step (Q-M): timezone, locale and keyboard,
// each picked from a list. A rerun starts from the running answers, a new
// install from the live session (what the stick was set to), and without
// either from Etc/UTC, en_US.UTF-8 and us. A running value the lists do not
// know stays offered, so a rerun never loses a working setting.
func collectRegion(ctx context.Context, ui prompt.UI, running *config.User, a config.User, u *config.User) error {
	live := liveRegion(ctx)
	keep := config.User{}
	if running != nil {
		keep = a
	}
	liveLayout := ""
	if live.KeyboardLayout != "" {
		if l, err := xkb.Normalize(live.KeyboardLayout); err == nil {
			liveLayout = l.Name
		}
	}
	fmt.Fprintln(ui.Out, "Region. Press Enter to keep a value, or type part of a name to search the list.")
	var err error
	if u.Timezone, err = pickRegion(ctx, ui, "Timezone", region.Timezones(), region.DefaultTimezone, keep.Timezone, live.Timezone); err != nil {
		return err
	}
	if u.Locale, err = pickRegion(ctx, ui, "Language and formats (locale)", region.Locales(), region.DefaultLocale, keep.Locale, live.Locale); err != nil {
		return err
	}
	layout, err := pickRegion(ctx, ui, "Keyboard layout", region.KeyboardLayouts(), region.DefaultKeyboard, keep.KeyboardLayout, liveLayout)
	if err != nil {
		return err
	}
	normalized, err := xkb.Normalize(layout)
	if err != nil {
		return err
	}
	u.KeyboardLayout, u.KeyboardVariant = normalized.Name, normalized.Variant
	if running != nil && normalized.Name == running.KeyboardLayout && normalized.Variant == "" {
		// The step takes no variant; the running one stays with its layout.
		u.KeyboardVariant = running.KeyboardVariant
	}
	return nil
}

// pickRegion offers options with keep (the running value, added when the
// list lacks it) or else live as the default.
func pickRegion(ctx context.Context, ui prompt.UI, label string, options []string, fallback, keep, live string) (string, error) {
	if keep != "" && !slices.Contains(options, keep) {
		options = append(slices.Clone(options), keep)
	}
	return ui.Pick(ctx, label, region.Prefill(options, fallback, keep, live), options)
}
