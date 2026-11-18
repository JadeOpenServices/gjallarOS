package oldroot

import (
	"os"
	"slices"
	"testing"
)

// The testdata is real lsblk LsblkArgs output from the CP2 VM: the VM's own
// root on vda, the fw13 VM disk on vdb (an in-place install, partition
// name "primary"), and a USB disk sda with a GJALLAROS LUKS2 partition,
// captured with its mapping open and mounted, open only, and closed.
func TestFind(t *testing.T) {
	cases := map[string][]Part{
		"lsblk-usb-mounted.json": nil,
		"lsblk-usb-open.json":    nil,
		"lsblk-usb-free.json": {{
			Path: "/dev/sda2", Disk: "/dev/sda", Encrypted: true, Internal: false,
			Size: 4 << 30, Model: "QEMU HARDDISK",
		}},
	}
	for file, want := range cases {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/" + file)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Find(raw)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("Find = %+v, want %+v", got, want)
			}
		})
	}
}

func TestPartitions(t *testing.T) {
	raw, err := os.ReadFile("testdata/lsblk-usb-free.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Partitions(raw, "/dev/sda")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/dev/sda1", "/dev/sda2"}; !slices.Equal(got, want) {
		t.Fatalf("Partitions = %v, want %v", got, want)
	}
}
