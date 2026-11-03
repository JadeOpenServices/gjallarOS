package targetdisk

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// All device trees below are SYNTHETIC lsblk output.

func TestMountedDisk(t *testing.T) {
	cryptOnNVMe := fakeRunner{output: []byte(`{"blockdevices":[{"path":"/dev/mapper/cryptroot","type":"crypt","children":[
		{"path":"/dev/nvme0n1p2","type":"part","children":[{"path":"/dev/nvme0n1","type":"disk"}]}]}]}`)}
	spanning := fakeRunner{output: []byte(`{"blockdevices":[{"path":"/dev/md0","type":"raid1","children":[
		{"path":"/dev/sda1","type":"part","children":[{"path":"/dev/sda","type":"disk"}]},
		{"path":"/dev/sdb1","type":"part","children":[{"path":"/dev/sdb","type":"disk"}]}]}]}`)}
	for _, test := range []struct {
		name    string
		runner  commandOutputs
		want    string
		wantErr string
	}{
		{name: "crypt root", runner: commandOutputs{"findmnt": {output: []byte("/dev/mapper/cryptroot[/@]\n")}, "lsblk": cryptOnNVMe}, want: "/dev/nvme0n1"},
		{name: "two disks", runner: commandOutputs{"findmnt": {output: []byte("/dev/md0\n")}, "lsblk": spanning}, wantErr: "more than one disk"},
		{name: "no disk", runner: commandOutputs{"findmnt": {output: []byte("/dev/mapper/cryptroot\n")}, "lsblk": {output: []byte(`{"blockdevices":[{"path":"/dev/mapper/cryptroot","type":"crypt"}]}`)}}, wantErr: "no whole disk"},
		{name: "not a device", runner: commandOutputs{"findmnt": {output: []byte("tmpfs\n")}}, wantErr: "not mounted from a device"},
		{name: "findmnt fails", runner: commandOutputs{"findmnt": {err: errors.New("SYNTHETIC findmnt failure")}}, wantErr: "find the device"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := mountedDisk(context.Background(), "/", test.runner)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("disk = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestPartitionByLabel(t *testing.T) {
	const label = "GJALLAR-RECOVERY"
	list := func(parts string) fakeRunner {
		return fakeRunner{output: []byte(`{"blockdevices":[{"path":"/dev/nvme0n1","type":"disk","partlabel":null,"pkname":null},` + parts + `]}`)}
	}
	for _, test := range []struct {
		name    string
		disk    string
		runner  *fakeRunner
		want    string
		wantErr string
	}{
		{name: "found", disk: "/dev/nvme0n1", runner: ptr(list(`{"path":"/dev/nvme0n1p1","type":"part","partlabel":"EFI","pkname":"/dev/nvme0n1"},{"path":"/dev/nvme0n1p3","type":"part","partlabel":"GJALLAR-RECOVERY","pkname":"/dev/nvme0n1"}`)), want: "/dev/nvme0n1p3"},
		{name: "none", disk: "/dev/nvme0n1", runner: ptr(list(`{"path":"/dev/nvme0n1p1","type":"part","partlabel":"EFI","pkname":"/dev/nvme0n1"}`))},
		{name: "two on the disk", disk: "/dev/nvme0n1", runner: ptr(list(`{"path":"/dev/nvme0n1p3","type":"part","partlabel":"GJALLAR-RECOVERY","pkname":"/dev/nvme0n1"},{"path":"/dev/nvme0n1p4","type":"part","partlabel":"GJALLAR-RECOVERY","pkname":"/dev/nvme0n1"}`)), wantErr: "more than one partition"},
		{name: "partition of another disk does not count", disk: "/dev/nvme0n1", runner: ptr(list(`{"path":"/dev/sda1","type":"part","partlabel":"GJALLAR-RECOVERY","pkname":"/dev/sda"}`))},
		{name: "name on a crypt mapping is not a partition", disk: "/dev/nvme0n1", runner: ptr(list(`{"path":"/dev/nvme0n1p2","type":"part","partlabel":"root","pkname":"/dev/nvme0n1"},{"path":"/dev/mapper/x","type":"crypt","partlabel":"GJALLAR-RECOVERY","pkname":"/dev/nvme0n1p2"}`))},
		{name: "partition given", disk: "/dev/nvme0n1p3", runner: &fakeRunner{output: []byte(`{"blockdevices":[{"path":"/dev/nvme0n1p3","type":"part","partlabel":"GJALLAR-RECOVERY","pkname":"/dev/nvme0n1"}]}`)}, wantErr: "not a whole disk"},
		{name: "not a device", disk: "nvme0n1", runner: &fakeRunner{}, wantErr: "not a device"},
		{name: "lsblk fails", disk: "/dev/nvme0n1", runner: &fakeRunner{err: errors.New("SYNTHETIC lsblk failure")}, wantErr: "inspect"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := partitionByLabel(context.Background(), test.disk, label, test.runner)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("partition = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

// emulatedVMList is lsblk -J -l -p -o PATH,TYPE,PARTLABEL,PKNAME -- /dev/vda,
// captured verbatim on the EMULATED e2e-fw13 VM (QEMU virtio disk, installed
// before the label rename, so the partition still has its old GPT name).
const emulatedVMList = `{
   "blockdevices": [
      {
         "path": "/dev/vda",
         "type": "disk",
         "partlabel": null,
         "pkname": null
      },{
         "path": "/dev/vda1",
         "type": "part",
         "partlabel": "ESP",
         "pkname": "/dev/vda"
      },{
         "path": "/dev/vda2",
         "type": "part",
         "partlabel": "primary",
         "pkname": "/dev/vda"
      },{
         "path": "/dev/vda3",
         "type": "part",
         "partlabel": "JODS-RECOVERY",
         "pkname": "/dev/vda"
      },{
         "path": "/dev/mapper/cryptroot",
         "type": "crypt",
         "partlabel": null,
         "pkname": "/dev/vda2"
      }
   ]
}`

// emulatedVMRoot is lsblk -J -l -p -s -o PATH,TYPE -- /dev/mapper/cryptroot
// from the same VM.
const emulatedVMRoot = `{
   "blockdevices": [
      {
         "path": "/dev/mapper/cryptroot",
         "type": "crypt"
      },{
         "path": "/dev/vda2",
         "type": "part"
      },{
         "path": "/dev/vda",
         "type": "disk"
      }
   ]
}`

func TestPartitionByLabelEmulatedVM(t *testing.T) {
	runner := &fakeRunner{output: []byte(emulatedVMList)}
	got, err := partitionByLabel(context.Background(), "/dev/vda", "JODS-RECOVERY", runner)
	if err != nil || got != "/dev/vda3" {
		t.Fatalf("partition = %q, %v; want /dev/vda3", got, err)
	}
	want := "lsblk -J -l -p -o PATH,TYPE,PARTLABEL,PKNAME -- /dev/vda"
	if len(runner.calls) != 1 || strings.Join(runner.calls[0], " ") != want {
		t.Fatalf("calls = %q, want %q", runner.calls, want)
	}
	runner = &fakeRunner{output: []byte(emulatedVMList)}
	if got, err := partitionByLabel(context.Background(), "/dev/vda", "GJALLAR-RECOVERY", runner); err != nil || got != "" {
		t.Fatalf("current label on the old VM = %q, %v; want none", got, err)
	}
}

func TestMountedDiskEmulatedVM(t *testing.T) {
	runner := commandOutputs{"findmnt": {output: []byte("/dev/mapper/cryptroot\n")}, "lsblk": {output: []byte(emulatedVMRoot)}}
	got, err := mountedDisk(context.Background(), "/", runner)
	if err != nil || got != "/dev/vda" {
		t.Fatalf("disk = %q, %v; want /dev/vda", got, err)
	}
}

func ptr[T any](v T) *T { return &v }
