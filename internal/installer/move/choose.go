package move

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/freespace"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/identity"
	"github.com/JadeOpenServices/gjallarOS/internal/movehelper"
)

// Target is the new disk as the install will lay it out. The caller fills
// it from the picked disk and the new system's closure; Choose never
// guesses a number.
type Target struct {
	DiskBytes uint64
	Recovery  bool
	// BaseClosure and FileCache as in freespace.Input.
	BaseClosure, FileCache uint64
}

// Choices is what the user chose to move.
type Choices struct {
	// Categories are the old disk's categories; Chosen ones move.
	Categories []freespace.Category
	// Identity holds one entry per identity screen shown, with the
	// answer in Keep.
	Identity []identity.Choice
	// Space is the free-space check for the chosen categories; it fits.
	Space freespace.Result
}

// Chosen tells whether the category name moves.
func (c Choices) Chosen(name string) bool {
	for _, cat := range c.Categories {
		if cat.Name == name {
			return cat.Chosen
		}
	}
	return false
}

// left tells whether the category was offered and stays behind. A category
// that was never offered (nothing to move) is not left behind.
func (c Choices) left(name string) bool {
	for _, cat := range c.Categories {
		if cat.Name == name {
			return !cat.Chosen
		}
	}
	return false
}

// Choose runs steps 6 to 8 on the opened old disk: the size list, the
// Nextcloud note, the free-space check and the identity screens. The space
// check comes before the identity screens, so a home left behind also
// drops the Nextcloud account screen. Nothing is written. When the user
// stops, the old disk is closed and ErrStopped returned.
func (f Flow) Choose(ctx context.Context, old *Old, t Target) (Choices, error) {
	var c Choices
	var sizes movehelper.SizesResult
	if err := f.Helper.Run(ctx, movehelper.Request{Op: "sizes"}, &sizes); err != nil {
		return c, f.refused(err)
	}
	var inv movehelper.InventoryResult
	if err := f.Helper.Run(ctx, movehelper.Request{Op: "inventory"}, &inv); err != nil {
		return c, f.refused(err)
	}

	c.Categories = categories(sizes.Sizes)
	f.sizeList(c.Categories, sizes.Sizes)

	in := freespace.Input{
		DiskBytes: t.DiskBytes, Recovery: t.Recovery,
		BaseClosure: t.BaseClosure, FileCache: t.FileCache,
	}
	in.Categories = c.Categories
	if sizes.Nextcloud > 0 && freespace.NextcloudWarn(sizes.Nextcloud, freespace.Plan(in).Available) {
		skip, err := f.nextcloudNote(ctx, sizes.Nextcloud)
		if err != nil {
			return c, f.stop(ctx, err)
		}
		if skip {
			setChosen(c.Categories, freespace.Nextcloud, false)
		}
	}

	space, err := f.spaceCheck(ctx, in, c.Categories)
	if err != nil {
		return c, f.stop(ctx, err)
	}
	c.Space = space

	c.Identity, err = f.identityScreens(ctx, old, inv.Inventory, c.Chosen(freespace.Home), c.left(freespace.Nextcloud))
	if err != nil {
		return c, f.stop(ctx, err)
	}
	return c, nil
}

// stop closes the old disk when the user stopped or the screen failed.
func (f Flow) stop(ctx context.Context, err error) error {
	if cerr := f.Close(context.WithoutCancel(ctx)); cerr != nil {
		return errors.Join(err, cerr)
	}
	return err
}

// categories lists what the old disk holds, every one on by default (full
// move). The home is always listed; the others only when they hold
// something.
func categories(s freespace.Sizes) []freespace.Category {
	out := []freespace.Category{{Name: freespace.Home, Bytes: s.Home, Chosen: true}}
	if s.Nextcloud > 0 {
		out = append(out, freespace.Category{Name: freespace.Nextcloud, Bytes: s.Nextcloud, Chosen: true})
	}
	if s.Branches > 0 {
		out = append(out, freespace.Category{Name: freespace.Branches, Bytes: s.Branches, Chosen: true})
	}
	return out
}

func setChosen(cats []freespace.Category, name string, on bool) {
	for i := range cats {
		if cats[i].Name == name {
			cats[i].Chosen = on
		}
	}
}

// label is a category as the screen names it.
func label(name string) string {
	switch name {
	case freespace.Home:
		return "Your files (home folder)"
	case freespace.Nextcloud:
		return "Nextcloud folders"
	case freespace.Branches:
		return "System settings and their history"
	}
	return name
}

// behind is a category as the space check names it when it stays behind.
func behind(name string) string {
	switch name {
	case freespace.Home:
		return "your home folder (all your files)"
	case freespace.Nextcloud:
		return "the Nextcloud folders"
	case freespace.Branches:
		return "the system settings and their history"
	}
	return name
}

// plural picks the word form for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// sizeList is step 6.
func (f Flow) sizeList(cats []freespace.Category, s freespace.Sizes) {
	var b strings.Builder
	b.WriteString("This is what the move copies:\n")
	for _, cat := range cats {
		fmt.Fprintf(&b, "  %s: %s\n", label(cat.Name), usize(cat.Bytes))
	}
	if n := s.Skipped + s.Foreign; n > 0 {
		fmt.Fprintf(&b, "%d %s (pipes, sockets, devices or files of other users) %s behind.\n", n, plural(n, "special file", "special files"), plural(n, "stays", "stay"))
	}
	f.say(strings.TrimSuffix(b.String(), "\n"))
}

// nextcloudNote is the note of section 4.6. It tells whether the user
// skips the Nextcloud folders.
func (f Flow) nextcloudNote(ctx context.Context, bytes uint64) (bool, error) {
	f.say(fmt.Sprintf("Your Nextcloud folders take %s. Copying them keeps them as they are, and the Nextcloud app goes on syncing without downloading anything. Skipping them makes the move shorter and smaller. The Nextcloud app then downloads them again from the server after the move.", usize(bytes)))
	n, err := f.choose(ctx, []string{"Copy them", "Skip them, sync again from the server after the move"}, "Copy them")
	if err != nil {
		return false, err
	}
	if n == 1 {
		installlog.Mark(installlog.StickMoveNextcloudWarned, installlog.Choice("answer", "skip"))
		return true, nil
	}
	installlog.Mark(installlog.StickMoveNextcloudWarned, installlog.Choice("answer", "copy"))
	return false, nil
}

// spaceCheck is step 8: it loops until the chosen categories fit, or the
// user stops. Leaving the home behind also leaves the Nextcloud folders,
// which live inside it.
func (f Flow) spaceCheck(ctx context.Context, in freespace.Input, cats []freespace.Category) (freespace.Result, error) {
	for {
		in.Categories = cats
		r := freespace.Plan(in)
		if r.Fits {
			installlog.Mark(installlog.StickMoveSpaceOK,
				installlog.Count("needed", gib(r.Needed, true)), installlog.Count("free", gib(r.Available, false)))
			f.say(passLine(cats))
			return r, nil
		}
		installlog.Mark(installlog.StickMoveSpaceShort, installlog.Count("short", gib(r.Short, true)))
		// An empty category frees nothing when left behind.
		var biggest []freespace.Category
		for _, cat := range r.Biggest {
			if cat.Bytes > 0 {
				biggest = append(biggest, cat)
			}
		}
		if len(biggest) == 0 {
			f.say(fmt.Sprintf("The new disk is %s too small even for gjallarOS itself. Use a bigger disk.", usize(r.Short)))
			return r, ErrStopped
		}
		f.say(fmt.Sprintf("The new disk is %s short. Leave something behind or use a bigger disk.", usize(r.Short)))
		options := make([]string, 0, len(biggest)+1)
		for _, cat := range biggest {
			line := fmt.Sprintf("Leave behind: %s, %s", behind(cat.Name), usize(cat.Bytes))
			if cat.Name == freespace.Home && chosen(cats, freespace.Nextcloud) {
				line += ", with the Nextcloud folders inside it"
			}
			options = append(options, line)
		}
		options = append(options, "Stop the move")
		n, err := f.choose(ctx, options, "")
		if err != nil {
			return r, err
		}
		if n == len(biggest) {
			return r, ErrStopped
		}
		name := biggest[n].Name
		setChosen(cats, name, false)
		if name == freespace.Home {
			setChosen(cats, freespace.Nextcloud, false)
		}
	}
}

// passLine says the check passed and, when something stays behind, what.
func passLine(cats []freespace.Category) string {
	var left []string
	for _, cat := range cats {
		if !cat.Chosen {
			left = append(left, behind(cat.Name))
		}
	}
	if len(left) == 0 {
		return "Space check passed: everything you chose fits on the new disk."
	}
	return "Space check passed. Stays behind: " + strings.Join(left, ", ") + "."
}

func chosen(cats []freespace.Category, name string) bool {
	return Choices{Categories: cats}.Chosen(name)
}

// itemText is what one identity screen says: what the item is, and what
// keep and make new mean.
var itemText = map[identity.Item]struct{ name, keep, renew string }{
	identity.WiFi:           {"Saved Wi-Fi networks", "the new disk joins them at once", "type the passwords again"},
	identity.WiFiEnterprise: {"Work or school Wi-Fi with certificates", "the certificates and logins come along", "set them up again"},
	identity.VPN:            {"VPN connections and their keys", "the same VPN identity", "the profiles stay behind. Set them up again"},
	identity.Bluetooth:      {"Bluetooth devices paired with this computer", "they connect at once", "pair them again"},
	identity.Fingerprints:   {"Enrolled fingerprints", "no new enrolment", "enrol your fingers again"},
	identity.Tailscale:      {"Tailscale", "the same machine on your tailnet", "log in again, and it shows as a new machine"},
	identity.SSHHostKeys:    {"SSH host keys", "other computers keep trusting this one", "they warn that the key changed"},
	identity.MachineID:      {"Machine ID", "pairings and logs stay linked to this computer", "the new disk gets a new ID"},
	identity.Nextcloud:      {"Nextcloud account", "the Nextcloud app keeps syncing with no new login", "log in to Nextcloud again"},
}

// ItemLabel is how the screens name an identity item, and what making it
// new means for the user.
type ItemLabel struct{ Name, Renew string }

// ItemText gives the label of it; false for an item with no screen.
func ItemText(it identity.Item) (ItemLabel, bool) {
	t, ok := itemText[it]
	return ItemLabel{Name: t.name, Renew: t.renew}, ok
}

// nextcloudRefetch is the keep text for the Nextcloud account when its
// folders stay behind.
const nextcloudRefetch = "the Nextcloud app logs in by itself and downloads your folders again"

// identityScreens is step 7, one screen per item. home tells whether the
// home moves; without it the Nextcloud account has nothing to sync into.
// ncLeft tells whether Nextcloud folders were found and stay behind.
func (f Flow) identityScreens(ctx context.Context, old *Old, inv identity.Inventory, home, ncLeft bool) ([]identity.Choice, error) {
	offer := identity.Offer(inv, identity.Options{PINUnchecked: old.PINUnchecked, SameComputer: f.Mode == NewDisk})
	var notes []string
	if n := inv.BluetoothOther; n > 0 {
		pairings := fmt.Sprintf("%d Bluetooth %s made with an adapter", n, plural(n, "pairing was", "pairings were"))
		if f.Mode == NewDisk {
			// Same computer: the adapter may be a dongle that is unplugged.
			notes = append(notes, fmt.Sprintf("%s that is not connected now. Plug the adapter in and start the move again to keep %s. Otherwise pair %s again.", pairings, plural(n, "it", "them"), plural(n, "that device", "those devices")))
		} else {
			notes = append(notes, fmt.Sprintf("%s that is not in this computer. %s behind. Pair %s again.", pairings, plural(n, "It stays", "They stay"), plural(n, "that device", "those devices")))
		}
	}
	if n := inv.Skipped; n > 0 {
		notes = append(notes, fmt.Sprintf("%d settings %s could not be read safely. %s behind.", n, plural(n, "file", "files"), plural(n, "It stays", "They stay")))
	}
	if len(notes) > 0 {
		f.say(strings.Join(notes, "\n"))
	}
	var out []identity.Choice
	for _, ch := range offer {
		if ch.Item == identity.Nextcloud && !home {
			continue
		}
		text, ok := itemText[ch.Item]
		if !ok {
			return nil, fmt.Errorf("no screen text for identity item %q", ch.Item)
		}
		screen := text.name
		if ch.Count > 1 {
			screen += fmt.Sprintf(" (%d)", ch.Count)
		}
		keep := text.keep
		if ch.Item == identity.Nextcloud && ncLeft {
			keep = nextcloudRefetch
		}
		screen += "\nKeep: " + keep + ".\nMake new: " + text.renew + "."
		if ch.Risk {
			screen += "\n" + identity.RiskLine
		}
		f.say(screen)
		def := "Keep"
		if !ch.Keep {
			def = "Make new"
		}
		n, err := f.choose(ctx, []string{"Keep", "Make new"}, def)
		if err != nil {
			return nil, err
		}
		ch.Keep = n == 0
		if err := markIdentity(ch); err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, nil
}

// markIdentity writes the anchor for one identity answer. Every item and
// answer is a literal, so the anchor test can check it against the
// registry.
func markIdentity(ch identity.Choice) error {
	switch {
	case ch.Keep && ch.Item == identity.WiFi:
		installlog.Mark(installlog.StickMoveIdentityChoice, installlog.Choice("item", "wifi"), installlog.Choice("answer", "keep"))
	case ch.Keep && ch.Item == identity.WiFiEnterprise:
		installlog.Mark(installlog.StickMoveIdentityChoice, installlog.Choice("item", "wifi-enterprise"), installlog.Choice("answer", "keep"))
	case ch.Keep && ch.Item == identity.VPN:
		installlog.Mark(installlog.StickMoveIdentityChoice, installlog.Choice("item", "vpn"), installlog.Choice("answer", "keep"))
	case ch.Keep && ch.Item == identity.Bluetooth:
		installlog.Mark(installlog.StickMoveIdentityChoice, installlog.Choice("item", "bluetooth"), installlog.Choice("answer", "keep"))
	case ch.Keep && ch.Item == identity.Fingerprints:
		installlog.Mark(installlog.StickMoveIdentityChoice, installlog.Choice("item", "fingerprints"), installlog.Choice("answer", "keep"))
	case ch.Keep && ch.Item == identity.Tailscale:
		installlog.Mark(installlog.StickMoveIdentityChoice, installlog.Choice("item", "tailscale"), installlog.Choice("answer", "keep"))
	case ch.Keep && ch.Item == identity.SSHHostKeys:
		installlog.Mark(installlog.StickMoveIdentityChoice, installlog.Choice("item", "ssh-host-keys"), installlog.Choice("answer", "keep"))
	case ch.Keep && ch.Item == identity.MachineID:
		installlog.Mark(installlog.StickMoveIdentityChoice, installlog.Choice("item", "machine-id"), installlog.Choice("answer", "keep"))
	case ch.Keep && ch.Item == identity.Nextcloud:
		installlog.Mark(installlog.StickMoveIdentityChoice, installlog.Choice("item", "nextcloud"), installlog.Choice("answer", "keep"))
	case !ch.Keep && ch.Item == identity.WiFi:
		installlog.Mark(installlog.StickMoveIdentityChoice, installlog.Choice("item", "wifi"), installlog.Choice("answer", "new"))
	case !ch.Keep && ch.Item == identity.WiFiEnterprise:
		installlog.Mark(installlog.StickMoveIdentityChoice, installlog.Choice("item", "wifi-enterprise"), installlog.Choice("answer", "new"))
	case !ch.Keep && ch.Item == identity.VPN:
		installlog.Mark(installlog.StickMoveIdentityChoice, installlog.Choice("item", "vpn"), installlog.Choice("answer", "new"))
	case !ch.Keep && ch.Item == identity.Bluetooth:
		installlog.Mark(installlog.StickMoveIdentityChoice, installlog.Choice("item", "bluetooth"), installlog.Choice("answer", "new"))
	case !ch.Keep && ch.Item == identity.Fingerprints:
		installlog.Mark(installlog.StickMoveIdentityChoice, installlog.Choice("item", "fingerprints"), installlog.Choice("answer", "new"))
	case !ch.Keep && ch.Item == identity.Tailscale:
		installlog.Mark(installlog.StickMoveIdentityChoice, installlog.Choice("item", "tailscale"), installlog.Choice("answer", "new"))
	case !ch.Keep && ch.Item == identity.SSHHostKeys:
		installlog.Mark(installlog.StickMoveIdentityChoice, installlog.Choice("item", "ssh-host-keys"), installlog.Choice("answer", "new"))
	case !ch.Keep && ch.Item == identity.MachineID:
		installlog.Mark(installlog.StickMoveIdentityChoice, installlog.Choice("item", "machine-id"), installlog.Choice("answer", "new"))
	case !ch.Keep && ch.Item == identity.Nextcloud:
		installlog.Mark(installlog.StickMoveIdentityChoice, installlog.Choice("item", "nextcloud"), installlog.Choice("answer", "new"))
	default:
		return fmt.Errorf("unknown identity item %q", ch.Item)
	}
	return nil
}

// usize is size for a byte count from the helper; a crafted count above
// what int64 holds shows as the largest one, never as a small number.
func usize(b uint64) string {
	return size(int64(min(b, math.MaxInt64)))
}

// gib is b in GiB for an anchor, rounded up or down, capped so it never
// wraps.
func gib(b uint64, up bool) int {
	n := b >> 30
	if up && b&(1<<30-1) != 0 {
		n++
	}
	return int(min(n, math.MaxInt32))
}
