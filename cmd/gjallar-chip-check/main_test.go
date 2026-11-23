package main

import (
	"context"
	"errors"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
)

func TestChipReason(t *testing.T) {
	for _, test := range []struct {
		support diskcrypto.PCRLockSupport
		err     error
		want    string
	}{
		{diskcrypto.PCRLockSupported, nil, ""},
		{diskcrypto.PCRLockObsolete, nil, "the security chip lacks the PolicyAuthorizeNV command that the startup policy needs"},
		{diskcrypto.PCRLockPartial, nil, "the security chip lacks parts of TPM 2.0 that the startup policy needs"},
		{diskcrypto.PCRLockNoTPM, nil, "no usable TPM 2.0 security chip was found"},
		{diskcrypto.PCRLockSupported, errors.New("boom"), "the security chip could not be checked (boom)"},
	} {
		got := chipReason(context.Background(), func(context.Context) (diskcrypto.PCRLockSupport, error) {
			return test.support, test.err
		})
		if got != test.want {
			t.Errorf("%q/%v: got %q, want %q", test.support, test.err, got, test.want)
		}
	}
}
