package config

import (
	"strings"
	"testing"
)

func TestValidateHostname(t *testing.T) {
	base := User{
		Username:    "tester",
		Theme:       "noctalia",
		Shell:       "zsh",
		Editors:     []string{"vscodium"},
		Browsers:    []string{"firefox"},
		AIAgentMode: "workspace",
	}
	for _, name := range []string{"gjallarOS", "a", "lab-2", "old_name", strings.Repeat("a", 63)} {
		user := base
		user.Hostname = name
		if err := Validate(user); err != nil {
			t.Errorf("hostname %q rejected: %v", name, err)
		}
	}
	for _, name := range []string{"-lab", "lab-", "lab.example", "lab name", "läb", "_lab", strings.Repeat("a", 64)} {
		user := base
		user.Hostname = name
		if err := Validate(user); err == nil {
			t.Errorf("hostname %q accepted", name)
		}
	}
}
