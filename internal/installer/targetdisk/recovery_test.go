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
	tree := func(parts string) fakeRunner {
		return fakeRunner{output: []byte(`{"blockdevices":[{"path":"/dev/nvme0n1","type":"disk","children":[` + parts + `]}]}`)}
	}
	for _, test := range []struct {
		name    string
		disk    string
		runner  *fakeRunner
		want    string
		wantErr string
	}{
		{name: "found", disk: "/dev/nvme0n1", runner: ptr(tree(`{"path":"/dev/nvme0n1p1","type":"part","partlabel":"EFI"},{"path":"/dev/nvme0n1p3","type":"part","partlabel":"GJALLAR-RECOVERY"}`)), want: "/dev/nvme0n1p3"},
		{name: "none", disk: "/dev/nvme0n1", runner: ptr(tree(`{"path":"/dev/nvme0n1p1","type":"part","partlabel":"EFI"}`))},
		{name: "two on the disk", disk: "/dev/nvme0n1", runner: ptr(tree(`{"path":"/dev/nvme0n1p3","type":"part","partlabel":"GJALLAR-RECOVERY"},{"path":"/dev/nvme0n1p4","type":"part","partlabel":"GJALLAR-RECOVERY"}`)), wantErr: "more than one partition"},
		{name: "name on a crypt mapping is not a partition", disk: "/dev/nvme0n1", runner: ptr(tree(`{"path":"/dev/nvme0n1p2","type":"part","partlabel":"root","children":[{"path":"/dev/mapper/x","type":"crypt","partlabel":"GJALLAR-RECOVERY"}]}`))},
		{name: "partition given", disk: "/dev/nvme0n1p3", runner: &fakeRunner{output: []byte(`{"blockdevices":[{"path":"/dev/nvme0n1p3","type":"part","partlabel":"GJALLAR-RECOVERY"}]}`)}, wantErr: "not a whole disk"},
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

func ptr[T any](v T) *T { return &v }
