package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/winpe"
)

// Before Wine runs an installer, its headers are read in a sandbox: what
// it is built for, the libraries and .NET version it names, what its
// manifest asks for (Plane 277, wiki 931 step 1). A file Wine cannot run
// stops here with the reason; anything else is shown with where in the
// file it comes from, and the installer runs.

const (
	// scanTimeout bounds one scan; the file is read only up to
	// winpe.MaxRead.
	scanTimeout = 20 * time.Second
	scanCPU     = 10 * time.Second
	scanMem     = 512 << 20
	// scanOutMax bounds the scan's answer: MaxNames names per table.
	scanOutMax = 64 << 10
	// shownNames is how many libraries a list shows.
	shownNames = 20
)

// scanFile scans src, a plain file; tests replace it.
var scanFile = sandboxedScan

// sandboxedScan scans src in the sandbox that sandboxed sets up and checks
// the answer's shape.
func sandboxedScan(src *os.File) (winpe.Report, error) {
	out, err := sandboxed(scanTimeout, scanOutMax, src, "scan")
	if err != nil {
		return winpe.Report{}, err
	}
	return parseScanOut(out)
}

// parseScanOut reads the scan's answer: one Report and nothing else.
func parseScanOut(out []byte) (winpe.Report, error) {
	var r winpe.Report
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return winpe.Report{}, fmt.Errorf("scan sent a broken answer: %w", err)
	}
	if dec.More() {
		return winpe.Report{}, errors.New("scan sent more than one answer")
	}
	if err := r.Check(); err != nil {
		return winpe.Report{}, fmt.Errorf("scan sent a broken answer: %w", err)
	}
	return r, nil
}

// scanCmd is the scan inside the sandbox, with the file as fd 3.
func scanCmd(args []string, stdout, stderr io.Writer) (code int) {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: gjallar-wine scan, with the file as fd 3")
		return 2
	}
	defer func() {
		if recover() != nil {
			fmt.Fprintln(stderr, "the scan failed on this file")
			code = 1
		}
	}()
	if err := limitStep(scanMem, scanCPU); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	src := os.NewFile(3, "scanned file")
	info, err := src.Stat()
	if err != nil || !info.Mode().IsRegular() {
		fmt.Fprintln(stderr, "the scanned file is not a plain file")
		return 1
	}
	r, err := winpe.Scan(src, info.Size())
	if err == nil {
		err = r.Check()
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	out, err := json.Marshal(r)
	if err != nil || len(out) > scanOutMax {
		fmt.Fprintln(stderr, "the scan's answer is too large")
		return 1
	}
	if _, err := stdout.Write(out); err != nil {
		return 1
	}
	return 0
}

// preflight scans installer, an .exe or .msi file checkInstaller passed,
// and tells what it found. It fails for a file Wine cannot run. A scan
// that fails only warns: the installer runs in the app's sandbox all the
// same.
func preflight(installer string, stdout, stderr io.Writer) error {
	r, err := scanPath(installer)
	if err != nil {
		fmt.Fprintf(stderr, "WARNING: could not check %s before running it: %v\n", filepath.Base(installer), err)
		return nil
	}
	return tell(filepath.Base(installer), strings.ToLower(filepath.Ext(installer)), r, " before running it", stdout, stderr)
}

// scanPath opens path as a plain file, without following a link at its
// end, and scans it.
func scanPath(path string) (winpe.Report, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return winpe.Report{}, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return winpe.Report{}, fmt.Errorf("%s is not a plain file", path)
	}
	return scanFile(f)
}

// tell fails for a file Wine cannot run, and else prints what the scan
// found. ext is the file's extension in lower case; when ends the heading.
func tell(name, ext string, r winpe.Report, when string, stdout, stderr io.Writer) error {
	switch {
	case r.Kind == "other":
		return fmt.Errorf("%s is neither a program nor an .msi package (its first bytes say so)", name)
	case r.Kind == "dos":
		return fmt.Errorf("%s is a DOS program (from its file header); Wine does not run DOS programs", name)
	case r.Kind == "msi" && ext != ".msi":
		return fmt.Errorf("%s is an .msi package, not a program (from its first bytes); rename it to end in .msi", name)
	case r.Kind != "msi" && ext == ".msi":
		return fmt.Errorf("%s is a program, not an .msi package (from its file header); rename it to end in .exe", name)
	case r.Kind == "pe" && r.Machine != "x86" && r.Machine != "x64":
		return fmt.Errorf("%s is built for %s processors (from its file header); Wine here runs x86 and x64 programs only", name, machineName(r.Machine))
	}
	fmt.Fprintf(stdout, "Checked %s in a sandbox%s:\n", name, when)
	for _, line := range findings(r) {
		fmt.Fprintf(stdout, "  %s\n", line)
	}
	if r.Kind == "ne" {
		fmt.Fprintf(stderr, "WARNING: %s is a 16-bit program (from its file header); Wine here runs 32- and 64-bit programs, so it will most likely not start\n", name)
	}
	return nil
}

func machineName(m string) string {
	switch m {
	case "arm64":
		return "64-bit ARM"
	case "arm":
		return "32-bit ARM"
	}
	return "other"
}

// findings are the lines tell prints, each with where it comes from.
func findings(r winpe.Report) []string {
	switch r.Kind {
	case "msi":
		return []string{"an .msi package (from its first bytes); msiexec installs it"}
	case "ne":
		return []string{"a 16-bit program (from its file header)"}
	}
	var lines []string
	bits := "64-bit"
	if r.Machine == "x86" {
		bits = "32-bit"
	}
	kind := map[string]string{"gui": "with a window", "console": "for the terminal"}[r.Subsystem]
	if kind == "" {
		kind = "of an unusual kind"
	}
	lines = append(lines, fmt.Sprintf("%s program %s (from its file header)", bits, kind))
	if r.Machine == "x86" && !r.LargeAddress {
		lines = append(lines, "can use at most 2 GB of memory, as a 32-bit program without the large-address flag (from its file header)")
	}
	if d := r.DotNet; d != nil {
		v := d.Version
		if v == "" {
			v = "an unnamed version"
		}
		line := fmt.Sprintf(".NET program for runtime %s (from its .NET header)", v)
		if strings.HasPrefix(v, "v1.") || strings.HasPrefix(v, "v2.") {
			line += "; it needs .NET 2.0 to 3.5, apps come with .NET 4.8 only, so it may not start"
		}
		if d.Needs32 {
			line += "; runs as 32-bit only"
		}
		lines = append(lines, line)
	}
	if r.Packer != "" {
		lines = append(lines, fmt.Sprintf("made with %s (from its section names); the scan sees only its unpacking or setup part, not the program it installs", r.Packer))
	}
	switch r.Elevation {
	case "requireAdministrator", "highestAvailable":
		lines = append(lines, "asks for administrator rights (from its manifest); here it runs with the app's own rights, which is enough to install into the app")
	}
	if r.Subsystem == "gui" {
		switch r.DPI {
		case "":
			lines = append(lines, "may look small or blurry on a scaled screen: its manifest says nothing about scaling")
		case "unaware":
			lines = append(lines, "may look small or blurry on a scaled screen: its manifest says it does not scale")
		}
	}
	if list := nameList(r.Imports); list != "" {
		lines = append(lines, fmt.Sprintf("uses %s (from its import table): %s", libraries(len(r.Imports)), list))
	}
	if list := nameList(r.DelayImports); list != "" {
		lines = append(lines, fmt.Sprintf("may load %s more later (from its delay-load table): %s", libraries(len(r.DelayImports)), list))
	}
	if r.Skipped > 0 {
		lines = append(lines, fmt.Sprintf("%s left out: their names are not plain file names", libraries(r.Skipped)))
	}
	return lines
}

func libraries(n int) string {
	if n == 1 {
		return "1 library"
	}
	return fmt.Sprintf("%d libraries", n)
}

// nameList joins up to shownNames names.
func nameList(names []string) string {
	if len(names) <= shownNames {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:shownNames], ", "), len(names)-shownNames)
}

// checkCmd scans a file and tells what it found, without running it.
func checkCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		return errUsage
	}
	path, err := checkInstaller(args[0])
	if err != nil {
		return err
	}
	r, err := scanPath(path)
	if err != nil {
		return fmt.Errorf("could not check %s: %w", args[0], err)
	}
	return tell(filepath.Base(path), strings.ToLower(filepath.Ext(path)), r, "", stdout, stderr)
}
