package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/winefix"
	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
	"github.com/JadeOpenServices/gjallarOS/internal/winpe"
)

// SYNTHETIC lines in the shape Wine, its .NET part and the .NET host
// print them; the names and numbers are made up.
const synthLog = `0024:fixme:ntdll:NtQuerySystemInformation info_class SYSTEM_PERFORMANCE_INFORMATION
0024:err:module:import_dll Library MSVCR100.dll (which is needed by L"C:\\Program Files\\Synthetic\\tool.exe") not found
0024:err:module:import_dll Library MSVCR100.dll (which is needed by L"C:\\Program Files\\Synthetic\\tool.exe") not found
0024:err:module:import_dll Library gone.dll (which is needed by L"C:\\Program Files\\Synthetic\\tool.exe") not found
0024:err:module:import_dll Loading library old32.dll (which is needed by L"C:\\Program Files\\Synthetic\\tool.exe") failed (error c000007b).
0024:err:module:import_dll Loading library MSVCP100.dll (which is needed by L"C:\\Program Files\\Synthetic\\tool.exe") failed (error c0000139).
0024:err:module:loader_init Importing dlls for L"C:\\Program Files\\Synthetic\\tool.exe" failed, status c0000135
0030:0034:err:mscoree:CLRRuntimeInfo_GetRuntimeHost Wine Mono is not installed
wine: Unhandled page fault on read access to 0000000000000000 at address 00000001400010A2 (thread 0024), starting debugger...
wine: Call from 00006FFFFFC2B2E5 to unimplemented function msvcp100.dll.?_Synthetic@@YAXXZ, aborting
wine: Unhandled exception 0xe0434352 in thread 24 at address 00006FFFFFC3A2B1 (thread 0024), starting debugger...
Unhandled Exception: System.IO.FileNotFoundException: Could not load file or assembly 'Synthetic.Part, Version=1.2.0.0, Culture=neutral' or one of its dependencies.
You must install or update .NET to run this application.
  Framework: 'Microsoft.NETCore.App', version '6.0.0' (x64)
wine: failed to create main module for L"C:\\Program Files\\Synthetic\\tool.exe", status c0000017
`

func synthTable() *winefix.Table {
	return &winefix.Table{Fixes: []winefix.Fix{
		{ID: "vcrun2010", Title: "SYNTHETIC runtime", Kind: winefix.KindOffer, Why: "SYNTHETIC",
			DLLs: []string{"msvcp100.dll", "msvcr100.dll"}},
	}}
}

func TestMatchStart(t *testing.T) {
	got := matchStart([]byte(synthLog), "synthetic-app", winesandbox.App{}, synthTable())
	var lines []string
	for _, f := range got {
		lines = append(lines, fmt.Sprintf("%s|%d", f.text, f.times))
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{
		"Wine said msvcr100.dll is missing, needed by tool.exe: gjallarctl wine fix synthetic-app vcrun2010 brings it.|2",
		"Wine said gone.dll is missing, needed by tool.exe: no fix brings it",
		"Wine said old32.dll, needed by tool.exe, is built for another processor (error c000007b); gjallarctl wine repair synthetic-app",
		"Wine said msvcp100.dll lacks a function that tool.exe needs; gjallarctl wine fix synthetic-app vcrun2010 brings the publisher's.",
		"Wine found no .NET to run the program; gjallarctl wine reset synthetic-app",
		"The program crashed (page fault on read).",
		"The program called ?_Synthetic@@YAXXZ in msvcp100.dll, which Wine's own msvcp100.dll lacks; gjallarctl wine fix synthetic-app vcrun2010",
		"The program stopped on a .NET error of its own (exception 0xe0434352)",
		"The program could not load the .NET part Synthetic.Part 1.2.0.0; no fix brings it",
		"The program needs .NET Microsoft.NETCore.App 6.0.0 (x64)",
		"Wine could not open tool.exe as a program (status c0000017); no fix changes that.",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "fixme") || strings.Contains(all, "Importing dlls") || strings.Contains(all, "a library it needs did not load") {
		t.Errorf("fixme line or the import line shown next to its cause:\n%s", all)
	}

	// The app has the fix: repair, not fix.
	got = matchStart([]byte(synthLog), "synthetic-app", winesandbox.App{Fixes: []string{"vcrun2010"}}, synthTable())
	if !strings.Contains(got[0].text, "the app has vcrun2010, which brings it; gjallarctl wine repair synthetic-app") {
		t.Errorf("recorded fix: %s", got[0].text)
	}

	// Alone, the import line is all there is.
	only := `0024:err:module:loader_init Importing dlls for L"C:\\Synthetic\\tool.exe" failed, status c0000135` + "\n"
	got = matchStart([]byte(only), "synthetic-app", winesandbox.App{}, synthTable())
	if len(got) != 1 || !strings.Contains(got[0].text, "Wine could not start tool.exe: a library it needs did not load (status c0000135).") {
		t.Errorf("import line alone: %v", got)
	}
}

func TestMatchStartUntrusted(t *testing.T) {
	tab := synthTable()
	for _, line := range []string{
		// A program name with a terminal escape is not shown.
		"0024:err:module:import_dll Library gone.dll (which is needed by L\"C:\\\\x\\\\\x1b[2Jevil.exe\") not found",
		// A library name outside the charset does not match.
		"0024:err:module:import_dll Library g\x1bone.dll (which is needed by L\"C:\\\\tool.exe\") not found",
		// The program printing the line somewhere else in it.
		"SYNTHETIC 0024:err:module:import_dll Library gone.dll (which is needed by L\"C:\\\\tool.exe\") not found",
		"  wine: Unhandled stack overflow at address 0 (thread 0024), starting debugger...",
		// A framework line with no "You must install" before it.
		"  Framework: 'Microsoft.NETCore.App', version '6.0.0' (x64)",
	} {
		for _, f := range matchStart([]byte(line+"\n"), "synthetic-app", winesandbox.App{}, tab) {
			if strings.ContainsRune(f.text, 0x1b) || !strings.Contains(f.text, "a program") {
				t.Errorf("line %q gave %q", line, f.text)
			}
		}
	}

	// A line longer than the cap is cut, and the lines after it count.
	long := strings.Repeat("S", 3*maxLogLine) + "\n" +
		"0024:err:module:import_dll Library gone.dll (which is needed by L\"C:\\\\tool.exe\") not found\n"
	if got := matchStart([]byte(long), "synthetic-app", winesandbox.App{}, tab); len(got) != 1 {
		t.Errorf("after a long line: %v", got)
	}

	// At most maxLogMatches lines are read.
	var b strings.Builder
	for i := range maxLogMatches + 10 {
		fmt.Fprintf(&b, "0024:err:module:import_dll Library lib%d.dll (which is needed by L\"C:\\\\tool.exe\") not found\n", i)
	}
	if got := matchStart([]byte(b.String()), "synthetic-app", winesandbox.App{}, tab); len(got) != maxLogMatches {
		t.Errorf("%d findings", len(got))
	}
}

func TestDoctorStartLog(t *testing.T) {
	paths, _ := walkSetup(t, map[string]winpe.Report{"tool.exe": x64([]string{"user32.dll"}, nil)})
	var out strings.Builder
	if err := doctorCmd([]string{"synthetic-app"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "No start of the app is logged yet; start it once, then run gjallarctl wine doctor synthetic-app again.") {
		t.Errorf("no log:\n%s", got)
	}

	l, err := winesandbox.OpenStartLog(paths.Home, "synthetic-app")
	if err != nil {
		t.Fatal(err)
	}
	l.Stream(io.Discard).Write([]byte(synthLog))
	l.Close()
	out.Reset()
	if err := doctorCmd([]string{"synthetic-app"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "At the last start (") || !strings.Contains(got, "), Wine said:\n  Wine said msvcr100.dll is missing, needed by tool.exe: gjallarctl wine fix synthetic-app vcrun2010 brings it. (2 times)") {
		t.Errorf("log part:\n%s", got)
	}

	// A record changed after the start marks the log as older.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(paths.Record, later, later); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := doctorCmd([]string{"synthetic-app"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), ", before the app last changed), Wine said:") {
		t.Errorf("old log not marked:\n%s", out.String())
	}

	l, _ = winesandbox.OpenStartLog(paths.Home, "synthetic-app")
	l.Stream(io.Discard).Write([]byte("SYNTHETIC output only\n"))
	l.Close()
	out.Reset()
	if err := doctorCmd([]string{"synthetic-app"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "), Wine named no missing library or .NET.") {
		t.Errorf("clean log:\n%s", out.String())
	}
}
