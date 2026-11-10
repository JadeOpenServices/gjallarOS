package oddcdraft

import (
	"bytes"
	"strings"
	"testing"

	portable "github.com/JadeOpenServices/oddc/pkg/oddc"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc/oddctest"
)

// EXAMPLE facts: one catalog part ODDC marks unusable on Linux (the
// Validity VFS495 reader, 138a:003f) and one ID the catalog has no
// component for. Neither comes from a real machine.
func exampleFacts() portable.Facts {
	return portable.Facts{Devices: map[string][]string{
		"usb": {"138a:003f", "ffff:0001"},
	}}
}

func TestBestEffortNamesUnsupportedParts(t *testing.T) {
	parts, err := BestEffort(oddctest.Catalog(t), exampleFacts(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %+v", parts)
	}

	var out bytes.Buffer
	WriteBestEffort(&out, parts)
	text := out.String()

	for _, want := range []string{
		"Machine support: best effort.",
		"nothing optional is switched on",
		"Parts found: 2, 1 of them in the ODDC catalog.",
		"Known not to work on Linux:\n  usb 138a:003f Validity VFS495: no Linux driver",
		"usb ffff:0001 (not in the ODDC catalog)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output lacks %q:\n%s", want, text)
		}
	}
}

func TestBestEffortWithoutUnsupportedParts(t *testing.T) {
	var out bytes.Buffer
	WriteBestEffort(&out, []portable.PresentPart{{Bus: "pci", DeviceID: "ffff:0002", Supported: true, Proven: "untested"}})
	if !strings.Contains(out.String(), "No part found is known not to work on Linux.") {
		t.Fatal(out.String())
	}
}

func TestBestEffortDropsControlCharacters(t *testing.T) {
	var out bytes.Buffer
	WriteBestEffort(&out, []portable.PresentPart{{
		Bus:               "usb",
		DeviceID:          "ffff:0003",
		ID:                "example/part",
		Name:              "EXAMPLE\x1b[2Jpart",
		UnsupportedReason: "EXAMPLE\rreason\x07",
	}})
	if strings.ContainsAny(out.String(), "\x1b\r\x07") {
		t.Fatalf("control characters reached the terminal: %q", out.String())
	}
	if !strings.Contains(out.String(), "EXAMPLE[2Jpart: EXAMPLEreason") {
		t.Fatal(out.String())
	}
}

func TestBestEffortNeedsCatalog(t *testing.T) {
	if _, err := BestEffort("", portable.Facts{}, nil); err == nil {
		t.Fatal("no error without a catalog")
	}
}
