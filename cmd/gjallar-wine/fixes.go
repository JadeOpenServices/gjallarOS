package main

import (
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/winefix"
)

// fixesCmd lists the fix table (internal/winefix), tells one fix, or
// tells which fixes bring a library. --ids gives the fix names alone, for
// completion.
func fixesCmd(args []string, stdout io.Writer) error {
	tab, err := winefix.Load()
	if err != nil {
		return err
	}
	switch {
	case len(args) == 0:
		listFixes(tab, stdout)
		return nil
	case len(args) == 1 && args[0] == "--ids":
		for _, f := range tab.Fixes {
			fmt.Fprintln(stdout, f.ID)
		}
		return nil
	case len(args) != 1 || strings.HasPrefix(args[0], "-"):
		return errUsage
	}
	name := args[0]
	if f, r := tab.Find(strings.ToLower(name)); f != nil {
		tellFix(f, stdout)
		return nil
	} else if r != nil {
		fmt.Fprintf(stdout, "%s is not offered: %s.\n", r.ID, r.Reason)
		return nil
	}
	fixes := tab.ByDLL(name)
	if len(fixes) == 0 {
		return fmt.Errorf("%q is neither a fix nor a library a fix brings; gjallarctl wine fixes lists them", name)
	}
	fmt.Fprintf(stdout, "%s comes with:\n", winefix.DLLName(name))
	for _, f := range fixes {
		fmt.Fprintf(stdout, "  %-10s %s\n", f.ID, f.Title)
	}
	return nil
}

func listFixes(tab *winefix.Table, w io.Writer) {
	fmt.Fprintln(w, "Fixes this version knows:")
	for _, f := range tab.Fixes {
		fmt.Fprintf(w, "  %-10s %-26s %s\n", f.ID, f.Title, downloads(&f))
	}
	fmt.Fprintln(w, "Not offered:")
	for _, r := range tab.Refused {
		fmt.Fprintf(w, "  %-10s %s\n", r.ID, r.Reason)
	}
	fmt.Fprintln(w, "gjallarctl wine fixes NAME tells more about one of them.")
}

func tellFix(f *winefix.Fix, w io.Writer) {
	fmt.Fprintf(w, "%s: %s\n", f.ID, f.Title)
	fmt.Fprintf(w, "  what it does: %s\n", f.Why)
	if f.Kind == winefix.KindOffer {
		fmt.Fprintln(w, "  use it only for an app that asks for these libraries: it replaces some of Wine's own")
	} else {
		fmt.Fprintln(w, "  changes nothing Wine brings")
	}
	if len(f.DLLs) > 0 {
		fmt.Fprintf(w, "  libraries: %s\n", strings.Join(f.DLLs, ", "))
	}
	if len(f.Conflicts) > 0 {
		fmt.Fprintf(w, "  does not go together with: %s\n", strings.Join(f.Conflicts, ", "))
	}
	if len(f.Files) == 0 {
		fmt.Fprintln(w, "  downloads nothing")
		return
	}
	fmt.Fprintf(w, "  downloads %s, each checked against its size and sha256:\n", downloads(f))
	for _, file := range f.Files {
		host := ""
		if u, err := url.Parse(file.URL); err == nil {
			host = u.Host
		}
		fmt.Fprintf(w, "    %s from %s, %s\n      sha256 %s\n", file.Name(), host, megabytes(file.Size), file.SHA256)
	}
}

func downloads(f *winefix.Fix) string {
	switch len(f.Files) {
	case 0:
		return "no download"
	case 1:
		return "1 file, " + megabytes(f.Size())
	}
	return fmt.Sprintf("%d files, %s", len(f.Files), megabytes(f.Size()))
}

func megabytes(n int64) string {
	return fmt.Sprintf("%.1f MB", float64(n)/1e6)
}
