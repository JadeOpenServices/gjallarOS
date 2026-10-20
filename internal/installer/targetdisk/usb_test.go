package targetdisk

import (
	"context"
	"errors"
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
