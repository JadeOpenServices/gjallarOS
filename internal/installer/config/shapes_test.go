package config

import (
	"strings"
	"testing"
)

// L145: the strings the build turns into file names, package names or
// Hyprland config text keep a plain shape.
func TestValidateShapes(t *testing.T) {
	base, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	base.Username = "example"
	if err := Validate(base); err != nil {
		t.Fatalf("defaults refused: %v", err)
	}
	good := map[string]func(u *User){
		"two layouts":     func(u *User) { u.KeyboardLayout = "de,us"; u.KeyboardVariant = "nodeadkeys," },
		"variant with -":  func(u *User) { u.KeyboardLayout = "us"; u.KeyboardVariant = "altgr-intl" },
		"variant T3":      func(u *User) { u.KeyboardVariant = "T3" },
		"second variant":  func(u *User) { u.KeyboardLayout = "us,de"; u.KeyboardVariant = ",nodeadkeys" },
		"no layout":       func(u *User) { u.KeyboardLayout = "" },
		"system checkout": func(u *User) { u.DotfilesDir = "/etc/gjallar/system" },
		"no preferred":    func(u *User) { u.PreferredEditor = ""; u.PreferredBrowser = "" },
		"retired browser": func(u *User) { u.Browsers = []string{"firefox", "librewolf"} },
	}
	for name, change := range good {
		u := base
		change(&u)
		if err := Validate(u); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
	bad := map[string]func(u *User){
		"theme path":         func(u *User) { u.Theme = "../../SYNTHETIC" },
		"theme upper":        func(u *User) { u.Theme = "Noctalia" },
		"shell path":         func(u *User) { u.Shell = "lib/SYNTHETIC" },
		"shell long":         func(u *User) { u.Shell = strings.Repeat("z", 65) },
		"editor path":        func(u *User) { u.Editors = []string{"vscodium", "../SYNTHETIC"} },
		"browser dot":        func(u *User) { u.Browsers = []string{"fire.fox"} },
		"preferred editor":   func(u *User) { u.PreferredEditor = "x y" },
		"preferred browser":  func(u *User) { u.PreferredBrowser = "x\nexec" },
		"layout newline":     func(u *User) { u.KeyboardLayout = "de\nexec-once = SYNTHETIC" },
		"variant space":      func(u *User) { u.KeyboardVariant = "a b" },
		"layout long":        func(u *User) { u.KeyboardLayout = strings.Repeat("a", 33) },
		"layout plus":        func(u *User) { u.KeyboardLayout = "us+inet" },
		"dotfiles relative":  func(u *User) { u.DotfilesDir = "dotfiles" },
		"dotfiles unclean":   func(u *User) { u.DotfilesDir = "/home/example/../../etc" },
		"dotfiles trailing":  func(u *User) { u.DotfilesDir = "/home/example/dotfiles/" },
		"dotfiles two lines": func(u *User) { u.DotfilesDir = "/home/example\n/etc" },
	}
	for name, change := range bad {
		u := base
		u.Editors = append([]string(nil), base.Editors...)
		u.Browsers = append([]string(nil), base.Browsers...)
		change(&u)
		if err := Validate(u); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
