package freshdiskplan

import (
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskplan"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/targetdisk"
)

func observed() targetdisk.Result {
	return targetdisk.Result{
		Path:      "/dev/nvme1n1",
		Model:     "Test NVMe",
		Serial:    "SERIAL-1",
		WWN:       "eui.0011223344556677",
		SizeBytes: 1000 * 1024 * 1024 * 1024,
	}
}

func TestBuildCanonicalBtrfsRecoveryLayout(t *testing.T) {
	p, err := Build(Input{
		Observed:       observed(),
		EnableRecovery: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := p.Validate(); err != nil {
		t.Fatalf("generated plan invalid: %v", err)
	}
	if p.ESP.Number != 1 || p.Root.Partition.Number != 2 {
		t.Fatalf(
			"unexpected canonical partition numbers: esp=%d root=%d",
			p.ESP.Number,
			p.Root.Partition.Number,
		)
	}
	if p.Recovery == nil || p.Recovery.Number != 3 {
		t.Fatalf("canonical recovery partition missing: %#v", p.Recovery)
	}
	if p.ESP.SizeBytes != ESPBytes {
		t.Fatalf("ESP size=%d", p.ESP.SizeBytes)
	}
	if p.Recovery.SizeBytes != RecoveryBytes {
		t.Fatalf("recovery size=%d", p.Recovery.SizeBytes)
	}
	if p.Root.Partition.Filesystem.Type != "btrfs" {
		t.Fatalf(
			"root filesystem=%q",
			p.Root.Partition.Filesystem.Type,
		)
	}
	if p.Root.Encryption.Type != diskplan.EncryptionLUKS2 {
		t.Fatalf("root encryption=%q", p.Root.Encryption.Type)
	}
	if p.Root.Encryption.MappingName != "cryptroot" {
		t.Fatalf(
			"mapping=%q",
			p.Root.Encryption.MappingName,
		)
	}
	if p.TargetDisk.GPTDiskGUID == "" ||
		p.ESP.PARTUUID == "" ||
		p.Root.Partition.PARTUUID == "" ||
		p.Recovery.PARTUUID == "" {
		t.Fatal("generated canonical identities are incomplete")
	}
}

func TestBuildWithoutRecovery(t *testing.T) {
	p, err := Build(Input{
		Observed: observed(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Recovery != nil {
		t.Fatal("recovery unexpectedly enabled")
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildRejectsMissingStableIdentity(t *testing.T) {
	o := observed()
	o.Serial = ""
	o.WWN = ""

	if _, err := Build(Input{Observed: o}); err == nil {
		t.Fatal("target without stable identity accepted")
	}
}

func TestBuildRejectsTooSmallDisk(t *testing.T) {
	o := observed()
	o.SizeBytes = ESPBytes + RecoveryBytes

	if _, err := Build(Input{
		Observed:       o,
		EnableRecovery: true,
	}); err == nil {
		t.Fatal("undersized disk accepted")
	}
}

func TestBuildGeneratesFreshIdentities(t *testing.T) {
	a, err := Build(Input{Observed: observed(), EnableRecovery: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(Input{Observed: observed(), EnableRecovery: true})
	if err != nil {
		t.Fatal(err)
	}

	if a.TargetDisk.GPTDiskGUID == b.TargetDisk.GPTDiskGUID {
		t.Fatal("GPT GUID was reused")
	}
	if a.Root.Partition.PARTUUID == b.Root.Partition.PARTUUID {
		t.Fatal("root PARTUUID was reused")
	}
}

// The move's free-space plan sizes the new root with RootBytes; it must be
// what Build lays out.
func TestRootBytesMatchesBuild(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		for _, size := range []uint64{MinimumRecoveryDiskBytes, 1000*1024*1024*1024 + 12345, 2000398934016} {
			o := observed()
			o.SizeBytes = size
			p, err := Build(Input{Observed: o, EnableRecovery: recovery})
			if err != nil {
				t.Fatal(err)
			}
			if got := RootBytes(size, recovery); got != p.Root.Partition.SizeBytes {
				t.Errorf("size %d recovery %v: RootBytes %d, Build %d", size, recovery, got, p.Root.Partition.SizeBytes)
			}
		}
	}
	if RootBytes(ESPBytes, false) != 0 {
		t.Error("a disk smaller than the fixed partitions has a root")
	}
}

func TestInstallSwap(t *testing.T) {
	if InstallSwap(SmallSwapRoot-1) != InstallSwapSmallBytes || InstallSwap(SmallSwapRoot) != InstallSwapBytes {
		t.Fatal("swap rule")
	}
}

// Plane 415 / Q259: the recovery partition comes on top of the 40 GiB
// minimum. Below 52 GiB the root would be smaller than a minimum install's,
// and Build refuses the partition before anything is written.
func TestRecoveryFitsOnlyAboveTheMinimum(t *testing.T) {
	if MinimumRecoveryDiskBytes != 52<<30 {
		t.Fatalf("recovery minimum = %d, want 52 GiB", MinimumRecoveryDiskBytes)
	}
	for _, tc := range []struct {
		size uint64
		fits bool
	}{
		{MinimumDiskBytes, false},
		{MinimumRecoveryDiskBytes - 1, false},
		{MinimumRecoveryDiskBytes - AlignmentBytes, false},
		{MinimumRecoveryDiskBytes, true},
		{1000 << 30, true},
	} {
		if got := RecoveryFits(tc.size); got != tc.fits {
			t.Errorf("RecoveryFits(%d) = %v, want %v", tc.size, got, tc.fits)
		}
		o := observed()
		o.SizeBytes = tc.size
		_, err := Build(Input{Observed: o, EnableRecovery: true})
		if (err == nil) != tc.fits {
			t.Errorf("Build(%d, recovery) err = %v, want fits=%v", tc.size, err, tc.fits)
		}
		if _, err := Build(Input{Observed: o}); err != nil {
			t.Errorf("Build(%d) without recovery: %v", tc.size, err)
		}
	}
	if RootBytes(MinimumRecoveryDiskBytes, true) != RootBytes(MinimumDiskBytes, false) {
		t.Fatal("the recovery minimum must leave the root of a minimum install")
	}
}
