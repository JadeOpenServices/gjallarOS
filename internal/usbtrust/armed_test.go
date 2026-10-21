package usbtrust

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoredArmed(t *testing.T) {
	for _, test := range []struct {
		name    string
		files   map[string]string
		want    bool
		wantErr bool
	}{
		{name: "no state"},
		{name: "envelope armed", files: map[string]string{EnvelopeFile: `{"document":{"enforcement":{"armed":true}}}`}, want: true},
		{name: "envelope disarmed", files: map[string]string{EnvelopeFile: `{"document":{"enforcement":{"armed":false}}}`}},
		{name: "envelope never armed", files: map[string]string{EnvelopeFile: `{"document":{"schema":1}}`}},
		{name: "legacy armed", files: map[string]string{TrustFile: `{"enforcement":{"armed":true}}`}, want: true},
		{name: "envelope wins over legacy", files: map[string]string{EnvelopeFile: `{"document":{}}`, TrustFile: `{"enforcement":{"armed":true}}`}},
		{name: "broken envelope", files: map[string]string{EnvelopeFile: `{"document":`}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, data := range test.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := StoredArmed(dir)
			if (err != nil) != test.wantErr {
				t.Fatalf("err = %v, wantErr %t", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("armed = %t, want %t", got, test.want)
			}
		})
	}
}

func TestStoredArmedMissingDirectory(t *testing.T) {
	if armed, err := StoredArmed(filepath.Join(t.TempDir(), "missing")); err != nil || armed {
		t.Fatalf("armed = %t, err = %v", armed, err)
	}
}
