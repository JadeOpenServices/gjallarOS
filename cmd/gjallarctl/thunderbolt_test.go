package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestThunderboltWaitingOnlyForBlockedPeripherals(t *testing.T) {
	cases := []struct {
		d    tbDevice
		want bool
	}{
		{tbDevice{Type: "peripheral", Status: "connected"}, true},
		{tbDevice{Type: "peripheral", Status: "authorized"}, false},
		{tbDevice{Type: "peripheral", Status: "disconnected"}, false},
		{tbDevice{Type: "peripheral", Status: "authorizing"}, false},
		{tbDevice{Type: "host", Status: "connected"}, false},
	}
	for _, c := range cases {
		if got := tbWaiting(c.d); got != c.want {
			t.Errorf("tbWaiting(%+v) = %v, want %v", c.d, got, c.want)
		}
	}
}

func TestThunderboltAnswerEndsWhenDeviceMovesOrGoes(t *testing.T) {
	dev := dbus.ObjectPath("/org/freedesktop/bolt/devices/a")
	status := map[string]dbus.Variant{"Status": dbus.MakeVariant("disconnected")}
	label := map[string]dbus.Variant{"Label": dbus.MakeVariant("desk")}
	cases := []struct {
		sig  *dbus.Signal
		want bool
	}{
		{&dbus.Signal{Path: boltPath, Name: boltManager + ".DeviceRemoved", Body: []interface{}{dev}}, true},
		{&dbus.Signal{Path: dev, Name: "org.freedesktop.DBus.Properties.PropertiesChanged", Body: []interface{}{boltDevice, status, []string{}}}, true},
		{&dbus.Signal{Path: dev, Name: "org.freedesktop.DBus.Properties.PropertiesChanged", Body: []interface{}{boltDevice, label, []string{}}}, false},
		{&dbus.Signal{Path: boltPath, Name: "org.freedesktop.DBus.Properties.PropertiesChanged", Body: []interface{}{boltManager, status, []string{}}}, false},
		{&dbus.Signal{Path: boltPath, Name: boltManager + ".DeviceAdded", Body: []interface{}{dev}}, false},
	}
	for _, c := range cases {
		path, ended := tbAnswerEnded(c.sig)
		if ended != c.want || (ended && path != dev) {
			t.Errorf("tbAnswerEnded(%s %v) = %q, %v; want %v", c.sig.Name, c.sig.Body, path, ended, c.want)
		}
	}
}

func TestThunderboltNoticeEscapesDeviceStrings(t *testing.T) {
	n := tbReviewNotice(tbDevice{Vendor: "<b>Evil</b>", Name: "Dock & Co"}, true)
	if strings.Contains(n.Body, "<b>") || !strings.Contains(n.Body, "&lt;b&gt;Evil&lt;/b&gt; Dock &amp; Co") {
		t.Fatalf("device strings not escaped: %q", n.Body)
	}
	keys := []string{}
	for _, a := range n.Actions {
		keys = append(keys, a.Key)
	}
	if strings.Join(keys, ",") != "allow-once,always-allow,keep" {
		t.Fatalf("actions = %v", keys)
	}
}

func TestThunderboltNoticeStatesDMAProtection(t *testing.T) {
	if body := tbReviewNotice(tbDevice{Name: "Dock"}, true).Body; !strings.Contains(body, "DMA protection is on") {
		t.Errorf("protected body = %q", body)
	}
	if body := tbReviewNotice(tbDevice{Name: "Dock"}, false).Body; !strings.Contains(body, "No DMA protection") {
		t.Errorf("unprotected body = %q", body)
	}
}

func TestThunderboltKernelDMAByDomain(t *testing.T) {
	domains := []tbDomainInfo{{id: "a", iommu: true}, {id: "b", iommu: false}}
	if !tbKernelDMA(domains, "a") || tbKernelDMA(domains, "b") || tbKernelDMA(domains, "missing") {
		t.Fatal("DMA protection must follow the device's own domain; unknown is unprotected")
	}
}

func TestThunderboltDMAWorstWins(t *testing.T) {
	for _, c := range []struct {
		kernel  bool
		oddc    tbFlag
		want    bool
		warning bool
	}{
		{true, tbFlagYes, true, false},
		{true, tbFlagNo, false, false},
		{true, tbFlagUnknown, false, false},
		{false, tbFlagYes, false, true},
		{false, tbFlagNo, false, false},
		{false, tbFlagUnknown, false, false},
	} {
		if got := tbDMAProtected(c.kernel, c.oddc); got != c.want {
			t.Errorf("kernel %v, ODDC %v: protected = %v", c.kernel, c.oddc, got)
		}
		if warn := tbDMAContradiction(c.kernel, c.oddc, "d0"); (warn != "") != c.warning {
			t.Errorf("kernel %v, ODDC %v: warning = %q", c.kernel, c.oddc, warn)
		}
	}
}

func TestParseODDCDMAFlag(t *testing.T) {
	for in, want := range map[string]tbFlag{
		`{"findings":[],"flags":{"thunderboltDmaProtected":true}}`:  tbFlagYes,
		`{"findings":[],"flags":{"thunderboltDmaProtected":false}}`: tbFlagNo,
		`{"findings":[],"flags":{}}`:                                tbFlagUnknown,
		`{"findings":[]}`:                                           tbFlagUnknown,
		`{"flags":{"thunderboltDmaProtected":"yes"}}`:               tbFlagUnknown,
		`{"flags":{"thunderboltDmaProtected":null}}`:                tbFlagUnknown,
		`not json`: tbFlagUnknown,
		``:         tbFlagUnknown,
	} {
		if got := parseODDCDMAFlag([]byte(in)); got != want {
			t.Errorf("%q = %v, want %v", in, got, want)
		}
	}
}

func TestThunderboltRejectsBadArguments(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"bogus"},
		{"allow-once"},
		{"allow-once", "--uid", "x;rm"},
		{"always-allow", "--uid", strings.Repeat("a", 65)},
		{"forget", "--uid", "abc", "extra"},
		{"review", "extra"},
	} {
		var stderr bytes.Buffer
		if code := runThunderbolt(args, &bytes.Buffer{}, &stderr); code != 2 {
			t.Errorf("runThunderbolt(%q) = %d, want 2", args, code)
		}
	}
}
