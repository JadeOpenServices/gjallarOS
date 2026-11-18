package oldroot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMappedDevice(t *testing.T) {
	mk := func(t *testing.T, name, uuid string, slaves ...string) {
		t.Helper()
		sys := t.TempDir()
		dev := t.TempDir()
		sysBlock, devDir = sys, dev
		t.Cleanup(func() { sysBlock, devDir = "/sys/block", "/dev" })
		os.MkdirAll(filepath.Join(sys, "sda"), 0o755)
		dir := filepath.Join(sys, "dm-0")
		os.MkdirAll(filepath.Join(dir, "dm"), 0o755)
		os.MkdirAll(filepath.Join(dir, "slaves"), 0o755)
		os.WriteFile(filepath.Join(dir, "dm", "name"), []byte(name+"\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "dm", "uuid"), []byte(uuid+"\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "dev"), []byte("254:0\n"), 0o644)
		for _, s := range slaves {
			os.WriteFile(filepath.Join(dir, "slaves", s), nil, 0o644)
		}
	}
	luks := "CRYPT-LUKS2-0123456789abcdef0123456789abcdef-" + Mapping
	for _, tc := range []struct {
		name, mapName, uuid string
		slaves              []string
		node                func(dev string)
		want                string
	}{
		{name: "not open", mapName: "other", uuid: luks, slaves: []string{"sda2"}, want: "is not open"},
		{name: "plain dm", mapName: Mapping, uuid: "LVM-x", slaves: []string{"sda2"}, want: "not a LUKS2 mapping"},
		{name: "other partition", mapName: Mapping, uuid: luks, slaves: []string{"sdb2"}, want: "does not sit on"},
		{name: "two slaves", mapName: Mapping, uuid: luks, slaves: []string{"sda2", "sdb2"}, want: "does not sit on"},
		{name: "no node", mapName: Mapping, uuid: luks, slaves: []string{"sda2"}, want: "no such file"},
		{name: "planted link", mapName: Mapping, uuid: luks, slaves: []string{"sda2"},
			node: func(dev string) { os.Symlink("/dev/null", filepath.Join(dev, "dm-0")) }, want: "is not the device"},
		{name: "regular file", mapName: Mapping, uuid: luks, slaves: []string{"sda2"},
			node: func(dev string) { os.WriteFile(filepath.Join(dev, "dm-0"), nil, 0o600) }, want: "is not the device"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mk(t, tc.mapName, tc.uuid, tc.slaves...)
			if tc.node != nil {
				tc.node(devDir)
			}
			_, err := MappedDevice("/dev/sda2")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
