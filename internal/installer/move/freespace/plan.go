// Package freespace checks, before the new disk is erased, whether what the
// user chose to move fits on it (345 design, section 4.5), and measures the
// old disk for that check.
package freespace

import (
	"sort"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/freshdiskplan"
)

// Category names, as the stick shows and logs them.
const (
	Home      = "home"
	Nextcloud = "nextcloud"
	Branches  = "branches"
)

const (
	// MinReserve is the least room left free after the move:
	// gjallarctl rebuild --update needs room before the garbage collection
	// can free any.
	MinReserve uint64 = 10 << 30
	// NextcloudWarnBytes is the Nextcloud size above which the stick asks
	// first whether to copy it or sync it again from the server.
	NextcloudWarnBytes uint64 = 50 << 30
	// biggest is how many categories a "too small" answer names.
	biggest = 3
)

// Category is one thing the user can move or leave behind.
type Category struct {
	Name   string
	Bytes  uint64
	Chosen bool
}

// Input is what the check works from. All sizes are bytes.
type Input struct {
	// DiskBytes is the size of the new disk; Recovery tells whether the
	// install puts a recovery partition on it.
	DiskBytes uint64
	Recovery  bool
	// BaseClosure is the new system without anything from the old disk.
	BaseClosure uint64
	// StoreImport is the narSize of the old store paths the new system
	// reuses; FileCache the file cache they pass through while it exists.
	StoreImport, FileCache uint64
	Categories          []Category
}

// Result is the answer. Short is zero when the move fits.
type Result struct {
	Root, Swap, Reserve uint64
	// Available is the root minus the install swap and the base system.
	Available uint64
	// Needed is the chosen categories, the store import, the file cache and
	// the reserve.
	Needed, Short uint64
	Fits          bool
	// Biggest names the largest chosen categories, largest first, for the
	// "uncheck something" hint.
	Biggest []Category
}

// Plan works out whether the chosen categories fit the new disk. The root
// size comes from the same plan the install partitions the disk with.
func Plan(in Input) Result {
	var r Result
	r.Root = freshdiskplan.RootBytes(in.DiskBytes, in.Recovery)
	r.Swap = freshdiskplan.InstallSwap(r.Root)
	r.Reserve = max(MinReserve, r.Root/20)
	r.Available = sub(sub(r.Root, r.Swap), in.BaseClosure)
	r.Needed = add(add(in.StoreImport, in.FileCache), r.Reserve)
	var chosen []Category
	for _, c := range in.Categories {
		if c.Chosen {
			r.Needed = add(r.Needed, c.Bytes)
			chosen = append(chosen, c)
		}
	}
	r.Short = sub(r.Needed, r.Available)
	r.Fits = r.Short == 0
	sort.SliceStable(chosen, func(i, j int) bool { return chosen[i].Bytes > chosen[j].Bytes })
	r.Biggest = chosen[:min(len(chosen), biggest)]
	return r
}

// NextcloudWarn tells whether the Nextcloud folders are big enough that the
// stick asks first: above 50 GiB, or above a quarter of the free space.
func NextcloudWarn(nextcloud, available uint64) bool {
	return nextcloud > NextcloudWarnBytes || nextcloud > available/4
}

// sub is a-b, or zero when b is larger.
func sub(a, b uint64) uint64 {
	if b >= a {
		return 0
	}
	return a - b
}

// add saturates instead of wrapping, so a crafted size can never make a
// move look small.
func add(a, b uint64) uint64 {
	if a+b < a {
		return ^uint64(0)
	}
	return a + b
}
