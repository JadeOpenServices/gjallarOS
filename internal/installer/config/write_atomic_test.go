package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomicPersistsUserConfigMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "user.config.json")

	want := User{
		Hostname:          "gjallarOS",
		Username:          "testuser",
		WeatherCity:       "Frankfurt am Main",
		WeatherCountry:    "Germany",
		AIAgentMode:       "workspace",
		RecoveryEnable:    true,
		WriteConfig:       true,
		RunRebuild:        true,
		ForceRedeploy:     true,
		UnattendedInstall: false,
	}

	if err := WriteAtomic(path, want); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("mode = %o, want 600", got)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatal("user configuration is not newline terminated")
	}

	var got User
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	if got.Hostname != want.Hostname ||
		got.Username != want.Username ||
		got.WeatherCity != want.WeatherCity ||
		got.WeatherCountry != want.WeatherCountry ||
		got.AIAgentMode != want.AIAgentMode ||
		got.RecoveryEnable != want.RecoveryEnable ||
		got.ForceRedeploy != want.ForceRedeploy {
		t.Fatalf("persisted configuration mismatch: %+v", got)
	}
}

func TestWriteAtomicKeepsExistingModeWithoutWriteBits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "user.config.json")
	for _, tc := range []struct{ before, want os.FileMode }{{0o644, 0o644}, {0o666, 0o644}, {0o600, 0o600}} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, tc.before); err != nil {
			t.Fatal(err)
		}
		if err := WriteAtomic(path, User{Hostname: "box"}); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != tc.want {
			t.Fatalf("mode %o became %o, want %o", tc.before, got, tc.want)
		}
	}
}
