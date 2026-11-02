package winefix

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestShipped(t *testing.T) {
	tab, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(tab.Fixes) == 0 || len(tab.Refused) == 0 {
		t.Fatalf("table: %d fixes, %d refused", len(tab.Fixes), len(tab.Refused))
	}
	if f, _ := tab.Find("vcrun2010"); f == nil || f.Size() != 5076456+5677032 {
		t.Errorf("vcrun2010: %+v", f)
	}
	if _, r := tab.Find("vcrun2019"); r == nil {
		t.Error("vcrun2019 is not refused")
	}
	if f, r := tab.Find("nothing"); f != nil || r != nil {
		t.Error("found a name the table does not have")
	}
	for _, name := range []string{"MSVCP100.DLL", "msvcp100", "msvcp100.dll"} {
		if got := tab.ByDLL(name); len(got) != 1 || got[0].ID != "vcrun2010" {
			t.Errorf("ByDLL(%q): %v", name, got)
		}
	}
	// Never offered: Visual C++ 2015-2019 replaced by what every app has.
	for _, f := range tab.Fixes {
		if strings.HasPrefix(f.ID, "vcrun201") && f.ID >= "vcrun2015" {
			t.Errorf("%s offered", f.ID)
		}
	}
}

// The real script, from the build (pkgs/gjallar-wine sets it).
func TestShippedWinetricks(t *testing.T) {
	p := os.Getenv("WINETRICKS_SCRIPT")
	if p == "" {
		t.Skip("WINETRICKS_SCRIPT is not set; the Nix build sets it")
	}
	script, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	tab, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckWinetricks(script, tab); err != nil {
		t.Fatal(err)
	}
	// The refused rows are refused for the reason the table gives.
	for verb, word := range map[string]string{"mdac28": "w_package_unsupported_win64", "jet40": "w_package_unsupported_win64", "vcrun2019": "https://aka.ms/"} {
		body, err := verbBody(script, verb)
		if err != nil || !strings.Contains(body, word) {
			t.Errorf("%s: %v, no %s", verb, err, word)
		}
	}
	if _, err := verbBody(script, "webview2"); err == nil {
		t.Error("webview2 is now a verb; check the refusal")
	}
}

// SYNTHETIC rows on the real hosts with made-up paths and sums.
const goodRow = `{"id":"demo","title":"SYNTHETIC demo","kind":"offer","why":"SYNTHETIC test row",
 "dlls":["demo.dll"],"actions":[{"do":"verb","verb":"demo"}],
 "files":[{"verb":"demo","url":"https://download.microsoft.com/download/SYNTHETIC/demo.exe","sha256":"` + sum + `","size":10}]}`

const sum = "0000000000000000000000000000000000000000000000000000000000000001"

func table(rows ...string) []byte {
	return []byte(`{"schema":1,"source":"SYNTHETIC","fixes":[` + strings.Join(rows, ",") + `],"refused":[{"id":"old","reason":"SYNTHETIC reason"}]}`)
}

func TestParse(t *testing.T) {
	if _, err := Parse(table(goodRow)); err != nil {
		t.Fatal(err)
	}
	// Each change below breaks one rule.
	bad := map[string]string{
		"schema":     `"schema":1`,
		"unknown":    `"kind":"offer"`,
		"kind":       `"kind":"offer"`,
		"id":         `"id":"demo"`,
		"dll":        `"dlls":["demo.dll"]`,
		"action":     `"do":"verb"`,
		"http":       `https://download`,
		"host":       `download.microsoft.com`,
		"archive":    `download.microsoft.com/download`,
		"query":      `demo.exe"`,
		"dotdot":     `/SYNTHETIC/`,
		"sha":        sum,
		"size":       `"size":10`,
		"big":        `"size":10`,
		"file verb":  `{"verb":"demo","url"`,
		"title":      `"title":"SYNTHETIC demo"`,
		"conflict":   `"kind":"offer"`,
		"override":   `{"do":"verb","verb":"demo"}`,
		"no actions": `[{"do":"verb","verb":"demo"}]`,
	}
	repl := map[string]string{
		"schema":     `"schema":2`,
		"unknown":    `"kind":"offer","extra":1`,
		"kind":       `"kind":"maybe"`,
		"id":         `"id":"Demo!"`,
		"dll":        `"dlls":["Demo.DLL"]`,
		"action":     `"do":"reg_set"`,
		"http":       `http://download`,
		"host":       `example.org`,
		"archive":    `web.archive.org/web/2020/https://download.microsoft.com/download`,
		"query":      `demo.exe?x=1"`,
		"dotdot":     `/SYNTHETIC/../`,
		"sha":        strings.Repeat("g", 64),
		"size":       `"size":0`,
		"big":        `"size":300000000`,
		"file verb":  `{"verb":"other","url"`,
		"title":      `"title":"SYNTHETIC\u0007"`,
		"conflict":   `"kind":"offer","conflicts":["demo"]`,
		"override":   `{"do":"dll_override","dll":"demo.dll","mode":"native"}`,
		"no actions": `[]`,
	}
	for name, old := range bad {
		row := strings.Replace(goodRow, old, repl[name], 1)
		data := table(row)
		if name == "schema" {
			data = []byte(strings.Replace(string(table(goodRow)), old, repl[name], 1))
		}
		if !json.Valid(data) {
			t.Fatalf("%s: SYNTHETIC table is not JSON", name)
		}
		if _, err := Parse(data); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A fix twice, a URL twice, a refusal that is also a fix, two tables.
	for name, data := range map[string][]byte{
		"twice":   table(goodRow, goodRow),
		"url":     table(goodRow, strings.Replace(goodRow, `"id":"demo"`, `"id":"demo2"`, 1)),
		"refused": []byte(strings.Replace(string(table(goodRow)), `"id":"old"`, `"id":"demo"`, 1)),
		"two":     append(table(goodRow), table(goodRow)...),
	} {
		if _, err := Parse(data); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A dll_override with a known mode is fine.
	ov := strings.Replace(goodRow, `{"do":"verb","verb":"demo"}`, `{"do":"verb","verb":"demo"},{"do":"dll_override","dll":"demo","mode":"native,builtin"}`, 1)
	if _, err := Parse(table(ov)); err != nil {
		t.Errorf("override: %v", err)
	}
}

func TestCheckWinetricks(t *testing.T) {
	tab, err := Parse(table(goodRow))
	if err != nil {
		t.Fatal(err)
	}
	line := "    w_download https://download.microsoft.com/download/SYNTHETIC/demo.exe " + sum + "\n"
	script := func(body string) []byte {
		return []byte("#!/bin/sh\n# SYNTHETIC script\nload_other()\n{\n" + line + "}\n\nload_demo()\n{\n" + body + "}\n\nload_after()\n{\n}\n")
	}
	if err := CheckWinetricks(script("    # a comment w_call x\n"+line+"    w_try_ms_installer \"${WINE}\" demo.exe\n"), tab); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"missing":  "    w_try true\n",
		"extra":    line + "    w_download https://download.microsoft.com/download/SYNTHETIC/more.exe " + sum + "\n",
		"w_call":   line + "    w_call vcrun2022\n",
		"helper":   line + "    helper_win7sp1 x\n",
		"to":       line + "    w_download_to x https://download.microsoft.com/a " + sum + "\n",
		"name":     strings.TrimSuffix(line, "\n") + " demo.exe\n",
		"variable": "    w_download \"${url}\" " + sum + "\n",
		"win64":    line + "    w_package_unsupported_win64\n",
	} {
		if err := CheckWinetricks(script(body), tab); err == nil {
			t.Errorf("%s: passed", name)
		}
	}
	if err := CheckWinetricks([]byte("load_demo()\n{\n}\n"), tab); err == nil {
		t.Error("verb at the very start of the script without a newline before it passed")
	}
	twice := append(script(line), script(line)...)
	if err := CheckWinetricks(twice, tab); err == nil {
		t.Error("verb defined twice passed")
	}
}
