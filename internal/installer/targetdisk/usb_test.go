package targetdisk

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestUSBAttached(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		output  string
		want    bool
		wantErr bool
	}{
		{
			name:   "nvme disk",
			path:   "/dev/nvme0n1",
			output: `{"blockdevices":[{"path":"/dev/nvme0n1","type":"disk","tran":"nvme"}]}`,
		},
		{
			name:   "usb disk",
			path:   "/dev/sda",
			output: `{"blockdevices":[{"path":"/dev/sda","type":"disk","tran":"usb"}]}`,
			want:   true,
		},
		{
			name: "crypt root on a usb partition",
			path: "/dev/mapper/cryptroot",
			output: `{"blockdevices":[{"path":"/dev/mapper/cryptroot","type":"crypt","tran":null,"children":[
				{"path":"/dev/sda2","type":"part","tran":null,"children":[
					{"path":"/dev/sda","type":"disk","tran":"usb"}]}]}]}`,
			want: true,
		},
		{
			name:   "virtio disk without a transport",
			path:   "/dev/vda",
			output: `{"blockdevices":[{"path":"/dev/vda","type":"disk","tran":null}]}`,
		},
		{
			name:    "no whole disk",
			path:    "/dev/mapper/cryptroot",
			output:  `{"blockdevices":[{"path":"/dev/mapper/cryptroot","type":"crypt","tran":null}]}`,
			wantErr: true,
		},
		{
			name:    "not a device",
			path:    "/mnt",
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{output: []byte(test.output)}
			got, err := usbAttached(context.Background(), test.path, runner)
			if (err != nil) != test.wantErr {
				t.Fatalf("err = %v, wantErr %t", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("usb = %t, want %t", got, test.want)
			}
		})
	}
}

func TestUSBAttachedLSBLKFailure(t *testing.T) {
	runner := &fakeRunner{err: errors.New("SYNTHETIC lsblk failure")}
	if _, err := usbAttached(context.Background(), "/dev/sda", runner); err == nil {
		t.Fatal("lsblk failure was not reported")
	}
}

// commandOutputs answers each command by name.
type commandOutputs map[string]fakeRunner

func (c commandOutputs) Output(_ context.Context, name string, _ ...string) ([]byte, error) {
	answer := c[name]
	return answer.output, answer.err
}

func TestMountedUSBReason(t *testing.T) {
	usbLSBLK := fakeRunner{output: []byte(`{"blockdevices":[{"path":"/dev/mapper/cryptroot","type":"crypt","tran":null},{"path":"/dev/sda","type":"disk","tran":"usb"}]}`)}
	nvmeLSBLK := fakeRunner{output: []byte(`{"blockdevices":[{"path":"/dev/mapper/cryptroot","type":"crypt","tran":null},{"path":"/dev/nvme0n1","type":"disk","tran":"nvme"}]}`)}
	for _, test := range []struct {
		name   string
		runner commandOutputs
		want   string
	}{
		{name: "internal disk", runner: commandOutputs{"findmnt": {output: []byte("/dev/mapper/cryptroot\n")}, "lsblk": nvmeLSBLK}},
		{name: "usb disk", runner: commandOutputs{"findmnt": {output: []byte("/dev/mapper/cryptroot\n")}, "lsblk": usbLSBLK}, want: "is a USB drive"},
		{name: "btrfs subvolume suffix", runner: commandOutputs{"findmnt": {output: []byte("/dev/mapper/cryptroot[/@]\n")}, "lsblk": usbLSBLK}, want: "(/dev/mapper/cryptroot) is a USB drive"},
		{name: "findmnt fails", runner: commandOutputs{"findmnt": {err: errors.New("SYNTHETIC findmnt failure")}}, want: "could not be found"},
		{name: "lsblk fails: counts as USB", runner: commandOutputs{"findmnt": {output: []byte("/dev/mapper/cryptroot\n")}, "lsblk": {err: errors.New("SYNTHETIC lsblk failure")}}, want: "could not be checked"},
		{name: "not a device", runner: commandOutputs{"findmnt": {output: []byte("tmpfs\n")}}, want: "could not be checked"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := mountedUSBReason(context.Background(), "/", test.runner)
			if (test.want == "") != (got == "") || !strings.Contains(got, test.want) {
				t.Fatalf("reason = %q, want it to contain %q", got, test.want)
			}
		})
	}
}

func TestUSBSystemDiskReasonUnknownDisk(t *testing.T) {
	if got := usbSystemDiskReason(context.Background(), " ", &fakeRunner{}); !strings.Contains(got, "not known") {
		t.Fatalf("reason = %q", got)
	}
}
