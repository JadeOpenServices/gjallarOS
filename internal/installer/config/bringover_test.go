package config

import (
	"reflect"
	"strings"
	"testing"
)

// L142: a bring-over takes every device key from a fresh install except the
// written allow list, and every other key from the old disk. The test walks
// the struct tags, so a new device key is covered without a change here.
func TestWithDeviceKeysFrom(t *testing.T) {
	fresh, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	old := fresh
	old.Username = "example"
	// Make every field of the old disk differ from the fresh one where the
	// kind allows it.
	v := reflect.ValueOf(&old).Elem()
	for i := range v.NumField() {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString("SYNTHETIC-" + v.Type().Field(i).Name)
		case reflect.Bool:
			f.SetBool(!f.Bool())
		case reflect.Int, reflect.Int64:
			f.SetInt(f.Int() + 7)
		}
	}
	got, err := WithDeviceKeysFrom(old, fresh, BringOverKeeps)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := deviceScoped()
	if err != nil {
		t.Fatal(err)
	}
	g, o, n := reflect.ValueOf(got), reflect.ValueOf(old), reflect.ValueOf(fresh)
	typ := g.Type()
	for i := range typ.NumField() {
		key, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		_, kept := BringOverKeeps[key]
		want := o.Field(i)
		if keys[key] && !kept {
			want = n.Field(i)
		}
		if !reflect.DeepEqual(g.Field(i).Interface(), want.Interface()) {
			t.Errorf("%s: got %v, want %v", key, g.Field(i).Interface(), want.Interface())
		}
	}
	if got.Username != "SYNTHETIC-Username" {
		t.Errorf("username %q, want the old disk's", got.Username)
	}
	if got.Hostname != fresh.Hostname || got.DeveloperMode != fresh.DeveloperMode || got.USBGuardEnable != fresh.USBGuardEnable {
		t.Errorf("device keys of the old computer came along: %+v", got)
	}
}

func TestBringOverKeepsList(t *testing.T) {
	for key, reason := range BringOverKeeps {
		scoped, err := DeviceScoped(key)
		if err != nil || !scoped || strings.TrimSpace(reason) == "" {
			t.Errorf("%s: device %v, %v, reason %q", key, scoped, err, reason)
		}
	}
	for name, keep := range map[string]map[string]string{
		"not a device key": {"name": "SYNTHETIC"},
		"no reason":        {"hostname": " "},
		"unknown key":      {"SYNTHETICunknown": "SYNTHETIC"},
	} {
		if _, err := WithDeviceKeysFrom(User{}, User{}, keep); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
