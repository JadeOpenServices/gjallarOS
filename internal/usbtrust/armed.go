package usbtrust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// StoredArmed reports whether the stored trust state says USB blocking is
// armed. It does not check the TPM signature, so it is only fit for
// decisions where "armed" is the stricter answer: callers treat an error as
// armed too (loophole 23, Plane 194). No state at all means never armed.
func StoredArmed(dir string) (bool, error) {
	for _, name := range []string{EnvelopeFile, TrustFile} {
		path := filepath.Join(dir, name)
		present, err := pathPresent(path)
		if err != nil {
			return false, err
		}
		if !present {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return false, fmt.Errorf("read USB trust state: %w", err)
		}
		var document Document
		if name == EnvelopeFile {
			var envelope signedEnvelope
			err = json.Unmarshal(data, &envelope)
			document = envelope.Document
		} else {
			err = json.Unmarshal(data, &document)
		}
		if err != nil {
			return false, fmt.Errorf("decode USB trust state %s: %w", name, err)
		}
		return document.Enforcement != nil && document.Enforcement.Armed, nil
	}
	return false, nil
}
