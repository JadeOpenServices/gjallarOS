package main

import "testing"

// A checkout without ODDC data counts as untested, so the shim question
// gets the best-effort notice (Q221).
func TestShimFirmwareWithoutODDC(t *testing.T) {
	name, tested := shimFirmware(t.TempDir())
	if name != "" || tested {
		t.Fatalf("shimFirmware(empty) = %q, %v; want \"\", false", name, tested)
	}
}
