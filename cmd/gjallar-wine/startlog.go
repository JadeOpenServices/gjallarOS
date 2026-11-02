package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/winefix"
	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

// What the doctor takes from the start log (Plane 277, wiki 931 part 5).
// The log holds what the program wrote too, so only lines that start the
// way Wine's loader, Wine's .NET part or the .NET host start theirs are
// read, captures are capped in the patterns and checked again after, and
// a capture is only ever shown as text or looked up in the fix table.
const (
	maxLogLine    = 4 << 10
	maxLogMatches = 64
)

var (
	// Wine's debug lines: thread id, or process and thread id, then the
	// class and channel.
	wineLine = regexp.MustCompile(`^(?:[0-9a-f]{4,8}:){1,2}err:(module|mscoree):[A-Za-z0-9_]{1,64} (.*)$`)

	reNotFound    = regexp.MustCompile(`^Library ([A-Za-z0-9_.-]{1,64}) \(which is needed by L"(.{1,512}?)"\) not found$`)
	reLoadFailed  = regexp.MustCompile(`^Loading library ([A-Za-z0-9_.-]{1,64}) \(which is needed by L"(.{1,512}?)"\) failed \(error (c000007b|c0000139|c0000138)\)\.$`)
	reForwardFunc = regexp.MustCompile(`^function not found for forward '([A-Za-z0-9_-]{1,64})\.([A-Za-z0-9_@?$#]{1,128})' used by L"(.{1,512}?)"\. If you are using builtin`)
	reForwardMod  = regexp.MustCompile(`^module not found for forward '([A-Za-z0-9_-]{1,64})\.([A-Za-z0-9_@?$#]{1,128})' used by L"(.{1,512}?)"`)
	reILOnly      = regexp.MustCompile(`^mscoree\.dll not found, IL-only binary L"(.{1,512}?)" cannot be loaded`)
	reImportFail  = regexp.MustCompile(`^Importing dlls for L"(.{1,512}?)" failed, status ([0-9a-f]{8})$`)
	reNoMono      = regexp.MustCompile(`^Wine Mono is not installed`)
	reMonoLoad    = regexp.MustCompile(`^Could not load Mono into this process`)

	reMainModule = regexp.MustCompile(`^wine: failed to create main module for L"(.{1,512}?)", status ([0-9a-f]{8})$`)
	reCrash      = regexp.MustCompile(`^wine: Unhandled (page fault on (?:read|write|execute) access to [0-9A-Fa-fx]{1,18}|stack overflow|illegal instruction|privileged instruction) at address [0-9A-Fa-fx]{1,18} \(thread [0-9a-f]{4,8}\), starting debugger\.\.\.$`)
	reUnimpl     = regexp.MustCompile(`^wine: (?:Unimplemented function ([A-Za-z0-9_.-]{1,64})\.([A-Za-z0-9_@?$#]{1,128}) called|Call from (?:0x)?[0-9A-Fa-f]{1,16} to unimplemented function ([A-Za-z0-9_.-]{1,64})\.([A-Za-z0-9_@?$#]{1,128}), aborting$)`)
	reException  = regexp.MustCompile(`^wine: Unhandled exception (0x[0-9a-f]{8}) in thread [0-9a-f]{1,8}`)

	reAssembly  = regexp.MustCompile(`^Unhandled [Ee]xception[.:] System\.IO\.File(?:NotFound|Load)Exception: Could not load file or assembly '([A-Za-z0-9_.]{1,128}), Version=([0-9.]{1,32})`)
	reNeedNet   = regexp.MustCompile(`^You must install (?:or update )?\.NET to run this application\.$`)
	reFramework = regexp.MustCompile(`^ *Framework: '(Microsoft\.[A-Za-z]{1,32}\.App)', version '([0-9][0-9A-Za-z.+-]{0,31})' \((x64|x86)\)$`)

	// A program name from a Windows path, shown as text.
	shownName = regexp.MustCompile(`^[A-Za-z0-9 _.()+-]{1,64}$`)
)

// startFinding is one thing the log says, in plain words, with what to do.
type startFinding struct {
	text  string
	times int
}

// winBase gives the last part of a Windows path when it is a plain name,
// else a stand-in: the text is the program's own and is never used.
func winBase(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		p = p[i+1:]
	}
	if !shownName.MatchString(p) {
		return "a program"
	}
	return p
}

// readStart tells what the log of app id's last start says, and when it
// was written. A missing log is no error.
func readStart(home, id string, a winesandbox.App, tab *winefix.Table) ([]startFinding, time.Time, error) {
	data, when, err := winesandbox.ReadStartLog(home, id)
	if err != nil {
		return nil, time.Time{}, err
	}
	return matchStart(data, id, a, tab), when, nil
}

func matchStart(data []byte, id string, a winesandbox.App, tab *winefix.Table) []startFinding {
	var out []startFinding
	seen := map[string]int{}
	add := func(text string) {
		if i, ok := seen[text]; ok {
			out[i].times++
			return
		}
		seen[text] = len(out)
		out = append(out, startFinding{text: text, times: 1})
	}
	var importFails []string
	matches := 0
	needNet := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, maxLogLine), maxLogLine)
	sc.Split(cappedLines)
	for sc.Scan() && matches < maxLogMatches {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if m := wineLine.FindStringSubmatch(line); m != nil {
			if t, ok := wineFinding(m[1], m[2], id, a, tab); ok {
				matches++
				if t != "" {
					add(t)
				}
			} else if m[1] == "module" {
				if f := reImportFail.FindStringSubmatch(m[2]); f != nil {
					matches++
					importFails = append(importFails, fmt.Sprintf("Wine could not start %s: a library it needs did not load (status %s).", winBase(f[1]), f[2]))
				}
			}
			continue
		}
		if t, ok := hostFinding(line, id, a, tab, &needNet); ok {
			matches++
			if t != "" {
				add(t)
			}
		}
	}
	// "Importing dlls failed" follows the line that tells why; alone, it
	// is all there is.
	if len(out) == 0 {
		for _, t := range importFails {
			add(t)
		}
	}
	return out
}

// cappedLines splits like bufio.ScanLines, but cuts a line longer than
// maxLogLine instead of stopping: such a line is never one of Wine's.
func cappedLines(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i], nil
	}
	if len(data) >= maxLogLine {
		return len(data), data[:0], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// wineFinding reads one err:module or err:mscoree line. ok is false for a
// line it does not know.
func wineFinding(channel, rest, id string, a winesandbox.App, tab *winefix.Table) (string, bool) {
	if channel == "mscoree" {
		if reNoMono.MatchString(rest) || reMonoLoad.MatchString(rest) {
			return dotNetMissing(id, ""), true
		}
		return "", false
	}
	if m := reNotFound.FindStringSubmatch(rest); m != nil {
		return libMissing(m[1], winBase(m[2]), id, a, tab), true
	}
	if m := reLoadFailed.FindStringSubmatch(rest); m != nil {
		name, by := winefix.DLLName(m[1]), winBase(m[2])
		if m[3] == "c000007b" {
			return fmt.Sprintf("Wine said %s, needed by %s, is built for another processor (error c000007b); gjallarctl wine repair %s sets the app's fixes up again.", name, by, id), true
		}
		return lacksFunction(name, "", by, id, a, tab), true
	}
	if m := reForwardFunc.FindStringSubmatch(rest); m != nil {
		return lacksFunction(winefix.DLLName(m[1]), m[2], winBase(m[3]), id, a, tab), true
	}
	if m := reForwardMod.FindStringSubmatch(rest); m != nil {
		return libMissing(m[1], winBase(m[3]), id, a, tab), true
	}
	if m := reILOnly.FindStringSubmatch(rest); m != nil {
		return dotNetMissing(id, winBase(m[1])), true
	}
	return "", false
}

// hostFinding reads one of Wine's "wine:" notes or a line of the .NET
// host. ok is false for any other line.
func hostFinding(line, id string, a winesandbox.App, tab *winefix.Table, needNet *bool) (string, bool) {
	if m := reMainModule.FindStringSubmatch(line); m != nil {
		return fmt.Sprintf("Wine could not open %s as a program (status %s); no fix changes that.", winBase(m[1]), m[2]), true
	}
	if m := reCrash.FindStringSubmatch(line); m != nil {
		what, _, _ := strings.Cut(m[1], " access")
		return fmt.Sprintf("The program crashed (%s). If it crashes right at start every time, gjallarctl wine repair %s sets the app's fixes up again.", what, id), true
	}
	if m := reUnimpl.FindStringSubmatch(line); m != nil {
		if m[1] == "" {
			m[1], m[2] = m[3], m[4]
		}
		name := winefix.DLLName(m[1])
		if hint := fixFor(name, id, a, tab); hint != "" {
			return fmt.Sprintf("The program called %s in %s, which Wine's own %s lacks; %s.", m[2], name, name, hint), true
		}
		return fmt.Sprintf("The program called %s in %s, which Wine does not have yet; no fix brings it.", m[2], name), true
	}
	if m := reException.FindStringSubmatch(line); m != nil {
		if m[1] == "0xe0434352" {
			return "The program stopped on a .NET error of its own (exception 0xe0434352); the lines before it in the program's output tell which.", true
		}
		return fmt.Sprintf("The program stopped on an error it did not handle (exception %s).", m[1]), true
	}
	if m := reAssembly.FindStringSubmatch(line); m != nil {
		return fmt.Sprintf("The program could not load the .NET part %s %s; no fix brings it, so it has to come with the program.", m[1], m[2]), true
	}
	if reNeedNet.MatchString(line) {
		*needNet = true
		return "", true
	}
	if m := reFramework.FindStringSubmatch(line); m != nil && *needNet {
		*needNet = false
		return fmt.Sprintf("The program needs .NET %s %s (%s); the app's setup does not bring that .NET yet.", m[1], m[2], m[3]), true
	}
	return "", false
}

// fixFor tells which fix brings name, or "" when none does.
func fixFor(name, id string, a winesandbox.App, tab *winefix.Table) string {
	var ids []string
	for _, fix := range tab.ByDLL(name) {
		if slices.Contains(a.Fixes, fix.ID) {
			return fmt.Sprintf("the app has %s, which brings it; gjallarctl wine repair %s puts it in again", fix.ID, id)
		}
		ids = append(ids, fix.ID)
	}
	if len(ids) == 0 {
		return ""
	}
	return fmt.Sprintf("gjallarctl wine fix %s %s brings the publisher's", id, strings.Join(ids, " or "))
}

func libMissing(name, by, id string, a winesandbox.App, tab *winefix.Table) string {
	name = winefix.DLLName(name)
	return fmt.Sprintf("Wine said %s is missing, needed by %s: %s.", name, by, missingHint(id, a, tab, name))
}

func lacksFunction(name, function, by, id string, a winesandbox.App, tab *winefix.Table) string {
	what := "a function"
	if function != "" {
		what = function
	}
	if hint := fixFor(name, id, a, tab); hint != "" {
		return fmt.Sprintf("Wine said %s lacks %s that %s needs; %s.", name, what, by, hint)
	}
	return fmt.Sprintf("Wine said %s lacks %s that %s needs; no fix brings another %s.", name, what, by, name)
}

func dotNetMissing(id, by string) string {
	if by == "" {
		by = "the program"
	}
	return fmt.Sprintf("Wine found no .NET to run %s; gjallarctl wine reset %s puts back the app's setup, which has .NET Framework 4.8.", by, id)
}

// reportStart prints what the last start's log says.
func reportStart(paths winesandbox.Paths, id string, a winesandbox.App, tab *winefix.Table, stdout io.Writer) error {
	found, when, err := readStart(paths.Home, id, a, tab)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(stdout, "No start of the app is logged yet; start it once, then run gjallarctl wine doctor %s again.\n", id)
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the start log: %w", err)
	}
	head := fmt.Sprintf("At the last start (%s), Wine said:", when.Local().Format("2006-01-02 15:04"))
	if info, err := os.Stat(paths.Record); err == nil && info.ModTime().After(when) {
		head = fmt.Sprintf("At the last start (%s, before the app last changed), Wine said:", when.Local().Format("2006-01-02 15:04"))
	}
	if len(found) == 0 {
		fmt.Fprintf(stdout, "At the last start (%s), Wine named no missing library or .NET.\n", when.Local().Format("2006-01-02 15:04"))
		return nil
	}
	fmt.Fprintln(stdout, head)
	for _, f := range found {
		if f.times > 1 {
			fmt.Fprintf(stdout, "  %s (%d times)\n", f.text, f.times)
		} else {
			fmt.Fprintf(stdout, "  %s\n", f.text)
		}
	}
	return nil
}
