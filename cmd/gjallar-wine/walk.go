package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/winefix"
	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
	"github.com/JadeOpenServices/gjallarOS/internal/winpe"
)

// gjallarctl wine doctor (Plane 277, wiki 931 step 6): follows the
// libraries an app's program needs, the way Wine's loader looks for them,
// and tells for each one where Wine finds it, or that it is missing and
// what brings it. Every file of the program's own is read in a sandbox
// (scan.go); Wine's own folders are only listed, and a library there is
// told apart from the publisher's by the mark Wine writes into its own.
// Nothing runs and nothing changes.

// maxWalkScans caps how many of the program's own libraries one walk
// scans, each in its own sandbox.
const maxWalkScans = 32

// walkDir is one folder Wine's loader looks in: its names in lower case,
// since Wine finds them whatever their case, mapped to the real ones.
type walkDir struct {
	label string
	root  *os.Root
	names map[string]string
}

func openWalkDir(label, dir string) (*walkDir, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	d, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	entries, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		root.Close()
		return nil, err
	}
	w := &walkDir{label: label, root: root, names: map[string]string{}}
	for _, e := range entries {
		w.names[strings.ToLower(e.Name())] = e.Name()
	}
	return w, nil
}

// open opens name in the folder as a plain file. The os.Root keeps a link
// from leading out of the folder.
func (w *walkDir) open(name string) (*os.File, error) {
	f, err := w.root.OpenFile(name, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a plain file", name)
	}
	return f, nil
}

// wineMade tells whether name in the folder carries Wine's mark. Only the
// mark's bytes are read; a file too short to hold it carries none.
func (w *walkDir) wineMade(name string) (bool, error) {
	f, err := w.open(name)
	if err != nil {
		return false, err
	}
	defer f.Close()
	b := make([]byte, winpe.WineMarkLen)
	switch _, err := f.ReadAt(b, winpe.WineMarkAt); {
	case errors.Is(err, io.EOF):
		return false, nil
	case err != nil:
		return false, err
	}
	return winpe.IsWineMark(b), nil
}

// need is a library the walk has to find: who names it first, and whether
// only through a delay-load table on the way.
type need struct {
	name, by string
	delay    bool
}

// found is where the walk found one library.
type found struct {
	need
	where string // "app", "wine", "publisher", "missing"
	real  string
	dir   string
}

type walk struct {
	machine           string
	appDir, sys, win  *walkDir
	lib               map[string]bool
	scans             int
	results           []found
	apiSets           [2]int
	wrongArch, failed []string
	unchecked         []string
	skipped           int
}

// wineLibDir is where this Wine keeps its own libraries for arch.
func wineLibDir(arch string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(winePath)), "lib", "wine", arch)
}

func doctorCmd(args []string, stdout, stderr io.Writer) error {
	id, a, paths, lock, err := doctorApp(args)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := winesandbox.RealDir(paths.Prefix); err != nil {
		return err
	}
	if a.Exe == "" {
		return fmt.Errorf("app %s has no program yet; gjallarctl wine program %s picks one", id, id)
	}
	exe, err := winesandbox.HostPath(paths, a.Exe)
	if err != nil {
		return err
	}
	tab, err := loadFixes()
	if err != nil {
		return err
	}
	if err := winesandbox.RealDir(filepath.Dir(exe)); err != nil {
		return err
	}
	appDir, err := openWalkDir("the program's folder", filepath.Dir(exe))
	if err != nil {
		return err
	}
	defer appDir.root.Close()
	name := filepath.Base(exe)
	f, err := appDir.open(name)
	if err != nil {
		return err
	}
	r, err := scanFile(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("could not check %s: %w", name, err)
	}
	if r.Kind != "pe" || r.Machine != "x86" && r.Machine != "x64" {
		if err := tell(name, ".exe", r, "", io.Discard, io.Discard); err != nil {
			return err
		}
		return fmt.Errorf("%s is not a 32- or 64-bit program (from its file header)", name)
	}

	sysName, libArch := "system32", "x86_64-windows"
	if r.Machine == "x86" {
		sysName, libArch = "syswow64", "i386-windows"
	}
	w := &walk{machine: r.Machine}
	w.appDir = appDir
	if w.sys, err = openWalkDir(`C:\windows\`+sysName, filepath.Join(paths.Prefix, "drive_c", "windows", sysName)); err != nil {
		return err
	}
	defer w.sys.root.Close()
	if w.win, err = openWalkDir(`C:\windows`, filepath.Join(paths.Prefix, "drive_c", "windows")); err != nil {
		return err
	}
	defer w.win.root.Close()
	entries, err := os.ReadDir(wineLibDir(libArch))
	if err != nil {
		return err
	}
	w.lib = map[string]bool{}
	for _, e := range entries {
		w.lib[strings.ToLower(e.Name())] = true
	}
	w.skipped = r.Skipped
	w.run(name, r)

	lines := findings(r)
	fmt.Fprintf(stdout, "Checked %s of app %s in a sandbox: %s\n", name, id, lines[0])
	if r.DotNet != nil {
		fmt.Fprintln(stdout, "  a .NET program (from its .NET header): its import table names only mscoree.dll, and .NET loads the rest itself, so this walk sees little of what it needs")
	}
	w.report(id, a, tab, stdout)
	if err := reportStart(paths, id, a, tab, stdout); err != nil {
		return err
	}
	reportGraphics(id, stdout)
	return nil
}

// run walks from the program's tables: libraries needed at start first,
// then those loaded later.
func (w *walk) run(exe string, r winpe.Report) {
	seen := map[string]bool{strings.ToLower(exe): true}
	var direct, later []need
	add := func(names []string, by string, delay bool) {
		for _, n := range names {
			// Wine's loader adds .dll to a name without an extension.
			n = strings.ToLower(n)
			if !strings.Contains(n, ".") {
				n += ".dll"
			}
			if seen[n] {
				continue
			}
			seen[n] = true
			if delay {
				later = append(later, need{n, by, true})
			} else {
				direct = append(direct, need{n, by, false})
			}
		}
	}
	add(r.Imports, exe, false)
	add(r.DelayImports, exe, true)
	for len(direct) > 0 || len(later) > 0 {
		var n need
		if len(direct) > 0 {
			n, direct = direct[0], direct[1:]
		} else {
			n, later = later[0], later[1:]
		}
		if strings.HasPrefix(n.name, "api-ms-") || strings.HasPrefix(n.name, "ext-ms-") {
			w.apiSets[b2i(n.delay)]++
			continue
		}
		if real, ok := w.appDir.names[n.name]; ok {
			if dr, done := w.local(n, real); done {
				w.skipped += dr.Skipped
				add(dr.Imports, real, n.delay)
				add(dr.DelayImports, real, true)
				continue
			}
		}
		w.results = append(w.results, w.system(n))
	}
}

// local scans a library from the program's folder. done is false when
// Wine's loader goes on looking elsewhere: the file is built for another
// processor, or it could not be read.
func (w *walk) local(n need, real string) (winpe.Report, bool) {
	if w.scans >= maxWalkScans {
		w.unchecked = append(w.unchecked, real)
		w.results = append(w.results, found{need: n, where: "app", real: real, dir: w.appDir.label})
		return winpe.Report{}, false
	}
	w.scans++
	f, err := w.appDir.open(real)
	if err != nil {
		w.failed = append(w.failed, fmt.Sprintf("%s: %v", real, err))
		return winpe.Report{}, false
	}
	r, err := scanFile(f)
	f.Close()
	switch {
	case err != nil:
		w.failed = append(w.failed, fmt.Sprintf("%s: %v", real, err))
		return winpe.Report{}, false
	case r.Kind != "pe" || r.Machine != w.machine:
		w.wrongArch = append(w.wrongArch, real)
		return winpe.Report{}, false
	}
	w.results = append(w.results, found{need: n, where: "app", real: real, dir: w.appDir.label})
	return r, true
}

// system looks for a library in the prefix's system folders, then in
// Wine's own.
func (w *walk) system(n need) found {
	for _, d := range []*walkDir{w.sys, w.win} {
		real, ok := d.names[n.name]
		if !ok {
			continue
		}
		wine, err := d.wineMade(real)
		if err != nil {
			w.failed = append(w.failed, fmt.Sprintf("%s in %s: %v", real, d.label, err))
			return found{need: n, where: "missing"}
		}
		if wine {
			return found{need: n, where: "wine", real: real, dir: d.label}
		}
		return found{need: n, where: "publisher", real: real, dir: d.label}
	}
	if w.lib[n.name] {
		return found{need: n, where: "wine", real: n.name}
	}
	return found{need: n, where: "missing"}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (w *walk) report(id string, a winesandbox.App, tab *winefix.Table, stdout io.Writer) {
	for i, head := range []string{
		"Needed at start (from the import tables of the program and its own libraries):",
		"May be loaded later (from delay-load tables):",
	} {
		delay := i == 1
		var app, wine, pub []string
		var missing []found
		for _, f := range w.results {
			if f.delay != delay {
				continue
			}
			switch f.where {
			case "app":
				app = append(app, f.real)
			case "wine":
				wine = append(wine, f.name)
			case "publisher":
				pub = append(pub, fmt.Sprintf(`%s\%s`, f.dir, f.real))
			default:
				missing = append(missing, f)
			}
		}
		if len(app)+len(wine)+len(pub)+len(missing)+w.apiSets[i] == 0 {
			continue
		}
		fmt.Fprintln(stdout, head)
		for _, l := range []struct {
			what  string
			names []string
		}{{"from the program's folder", app}, {"Wine's own", wine}, {"from the publisher, not Wine", pub}} {
			if len(l.names) > 0 {
				slices.Sort(l.names)
				fmt.Fprintf(stdout, "  %s, %s: %s\n", libraries(len(l.names)), l.what, nameList(l.names))
			}
		}
		if n := w.apiSets[i]; n > 0 {
			fmt.Fprintf(stdout, "  %s, system sets (api-ms-, ext-ms-) that Wine maps to its own\n", libraries(n))
		}
		for _, f := range missing {
			fmt.Fprintf(stdout, "  MISSING %s, named by %s: %s\n", f.name, f.by, missingHint(id, a, tab, f.name))
		}
	}
	if len(w.wrongArch) > 0 {
		fmt.Fprintf(stdout, "In the program's folder but built for another processor, so Wine looks further: %s\n", nameList(w.wrongArch))
	}
	if len(w.unchecked) > 0 {
		fmt.Fprintf(stdout, "Stopped after %d of the program's own libraries; not followed further: %s\n", maxWalkScans, nameList(w.unchecked))
	}
	for _, f := range w.failed {
		fmt.Fprintf(stdout, "Could not check %s\n", f)
	}
	if w.skipped > 0 {
		fmt.Fprintf(stdout, "%s left out: their names are not plain file names\n", libraries(w.skipped))
	}
	// Wine's own libraries that a fix would replace, by fix.
	byFix := map[string][]string{}
	var order []string
	for _, f := range w.results {
		if f.where != "wine" {
			continue
		}
		for _, fix := range tab.ByDLL(f.name) {
			if slices.Contains(a.Fixes, fix.ID) {
				continue
			}
			if _, ok := byFix[fix.ID]; !ok {
				order = append(order, fix.ID)
			}
			if !slices.Contains(byFix[fix.ID], f.name) {
				byFix[fix.ID] = append(byFix[fix.ID], f.name)
			}
		}
	}
	for _, fixID := range order {
		names := byFix[fixID]
		slices.Sort(names)
		fmt.Fprintf(stdout, "Wine brings its own %s; if the program fails over it, gjallarctl wine fix %s %s puts in the publisher's.\n", nameList(names), id, fixID)
	}
	if !slices.ContainsFunc(w.results, func(f found) bool { return f.where == "missing" }) {
		fmt.Fprintln(stdout, "Nothing it names is missing.")
	}
}

// missingHint tells what brings a missing library.
func missingHint(id string, a winesandbox.App, tab *winefix.Table, name string) string {
	fixes := tab.ByDLL(name)
	var ids []string
	for _, fix := range fixes {
		if slices.Contains(a.Fixes, fix.ID) {
			return fmt.Sprintf("the app has %s, which brings it; gjallarctl wine repair %s puts it in again", fix.ID, id)
		}
		ids = append(ids, fix.ID)
	}
	if len(ids) > 0 {
		return fmt.Sprintf("gjallarctl wine fix %s %s brings it", id, strings.Join(ids, " or "))
	}
	return fmt.Sprintf("no fix brings it, so it has to come with the program; gjallarctl wine install %s INSTALLER runs its installer again", id)
}
