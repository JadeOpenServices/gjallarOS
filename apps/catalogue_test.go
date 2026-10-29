package apps

import "testing"

func TestCatalogue(t *testing.T) {
	catalogue, err := Catalogue()
	if err != nil {
		t.Fatal(err)
	}
	asked := map[string]bool{}
	for _, app := range catalogue {
		if app.Name == "" || app.Category == "" {
			t.Errorf("%s: name and category are required", app.ID)
		}
		if !iconName.MatchString(app.Icon) {
			t.Errorf("%s: icon %q", app.ID, app.Icon)
		}
		if app.Installer != "" {
			asked[app.ID] = true
		}
	}
	for _, id := range []string{"ai", "containers", "nemu", "tailscale"} {
		if !asked[id] {
			t.Errorf("%s: installer question missing", id)
		}
	}
	if err := Validate([]string{"ai", "git"}); err != nil {
		t.Fatal(err)
	}
	if Validate([]string{"gaming"}) == nil || Validate([]string{"git", "git"}) == nil {
		t.Fatal("bad app list accepted")
	}
}

func TestRetired(t *testing.T) {
	retired, err := Retired()
	if err != nil {
		t.Fatal(err)
	}
	catalogue, err := Catalogue()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range retired {
		for _, app := range catalogue {
			if app.ID == id {
				t.Errorf("%s is retired but still in the catalogue", id)
			}
		}
	}
	kept, dropped, err := WithoutRetired([]string{"git", "drawio", "vlc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 2 || kept[0] != "git" || kept[1] != "vlc" || len(dropped) != 1 || dropped[0] != "drawio" {
		t.Fatalf("kept %v dropped %v", kept, dropped)
	}
}

func TestIconName(t *testing.T) {
	for _, name := range []string{"org.kde.dolphin", "libreoffice-startcenter", "vlc"} {
		if !iconName.MatchString(name) {
			t.Errorf("%q refused", name)
		}
	}
	for _, name := range []string{"", "../icon", "/usr/share/icons/x.svg", "http://example.com/x", ".hidden", "a b"} {
		if iconName.MatchString(name) {
			t.Errorf("%q accepted", name)
		}
	}
}
