package oldroot

import (
	"os/exec"
	"strings"
	"testing"
)

func TestRemovableUnknownCounts(t *testing.T) {
	for _, v := range []any{true, "1", float64(1), nil, "yes"} {
		if !removable(v) {
			t.Errorf("removable(%v) = false", v)
		}
	}
	for _, v := range []any{false, "0", float64(0)} {
		if removable(v) {
			t.Errorf("removable(%v) = true", v)
		}
	}
}

func TestCryptsetupFailure(t *testing.T) {
	exit := func(code string) error {
		return exec.Command("sh", "-c", "exit "+code).Run()
	}
	raw := []byte("No key available with this passphrase.\n")
	if r, ok := AsRefusal(cryptsetupFailure(exit("2"), raw)); !ok || r.Reason != WrongKey {
		t.Errorf("exit 2: %v", r)
	}
	busy := []byte("Device \x1b[31mroot\x1b[0m is busy.\n\nsecond line\n")
	got := cryptsetupFailure(exit("5"), busy).Error()
	if strings.ContainsRune(got, '\x1b') || got != "cryptsetup could not open the disk: Device [31mroot[0m is busy.; second line" {
		t.Errorf("exit 5: %q", got)
	}
	if got := cryptsetupFailure(exit("4"), nil).Error(); got != "cryptsetup could not open the disk: exit status 4" {
		t.Errorf("exit 4 without text: %q", got)
	}
	long := cryptsetupFailure(exit("1"), []byte(strings.Repeat("x", 500))).Error()
	if n := len([]rune(long)); n != len([]rune("cryptsetup could not open the disk: "))+201 {
		t.Errorf("long text not capped: %d runes", n)
	}
	var c capWriter
	c.max = 4
	if n, err := c.Write([]byte("abcdef")); n != 6 || err != nil || c.buf.String() != "abcd" {
		t.Errorf("capWriter: %d %v %q", n, err, c.buf.String())
	}
}
