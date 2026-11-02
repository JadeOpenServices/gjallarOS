package secureboot

import "testing"

func verifiedOwnershipFixture() (ownershipRecord, localKeys) {
	keys := localKeys{
		ownerGUID: "12345678-1234-1234-1234-123456789abc",
		pkHash:    "pk-hash",
		kekHash:   "kek-hash",
		dbHash:    "db-hash",
	}

	record := ownershipRecord{
		Schema:         1,
		Manager:        "gjallarOS",
		Stage:          "enrolled",
		SbctlOwnerGUID: keys.ownerGUID,
		PKSHA256:       keys.pkHash,
		KEKSHA256:      keys.kekHash,
		DbSHA256:       keys.dbHash,
	}

	return record, keys
}

func TestVerifyOwnershipRecordAcceptsMatchingEnrollment(t *testing.T) {
	record, keys := verifiedOwnershipFixture()

	if err := verifyOwnershipRecord(record, keys, "enrolled"); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyOwnershipRecordRejectsWrongStage(t *testing.T) {
	record, keys := verifiedOwnershipFixture()
	record.Stage = "pending-enrollment"

	if err := verifyOwnershipRecord(record, keys, "enrolled"); err == nil {
		t.Fatal("accepted unexpected ownership stage")
	}
}

func TestVerifyOwnershipRecordRejectsForeignManager(t *testing.T) {
	record, keys := verifiedOwnershipFixture()
	record.Manager = "foreign"

	if err := verifyOwnershipRecord(record, keys, "enrolled"); err == nil {
		t.Fatal("accepted foreign Secure Boot manager")
	}
}

func TestVerifyOwnershipRecordRejectsGUIDMismatch(t *testing.T) {
	record, keys := verifiedOwnershipFixture()
	record.SbctlOwnerGUID = "foreign-guid"

	if err := verifyOwnershipRecord(record, keys, "enrolled"); err == nil {
		t.Fatal("accepted mismatched sbctl owner GUID")
	}
}

func TestVerifyOwnershipRecordRejectsCertificateMismatch(t *testing.T) {
	record, keys := verifiedOwnershipFixture()
	record.DbSHA256 = "foreign-db"

	if err := verifyOwnershipRecord(record, keys, "enrolled"); err == nil {
		t.Fatal("accepted mismatched Secure Boot certificate hash")
	}
}

func TestAcceptBootShimOnlyWhenBuiltForShim(t *testing.T) {
	for _, tc := range []struct {
		state   State
		viaShim bool
		ok      bool
	}{
		{StateGjallarManaged, false, true},
		{StateGjallarManaged, true, true},
		{StateGjallarShim, true, true},
		{StateGjallarShim, false, false},
		{StateOEMFactoryDerived, true, false},
		{StatePendingEnrollment, true, false},
		{StateForeignManaged, true, false},
		{StateInconsistent, true, false},
	} {
		err := acceptBoot(Inspection{State: tc.state}, tc.viaShim)
		if (err == nil) != tc.ok {
			t.Errorf("acceptBoot(%s, shim=%v) = %v, want ok=%v", tc.state, tc.viaShim, err, tc.ok)
		}
	}
}
