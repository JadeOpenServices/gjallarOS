package config

import (
	"reflect"
	"strings"
	"testing"
)

// differ sets v, a settable value, to something that is not DeepEqual to
// what it held, for every kind a settings field has. It reports false for a
// kind it does not know, so a new field kind fails the test instead of
// silently staying equal (G4).
func differ(v reflect.Value, name string) bool {
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "SYNTHETIC-" + name)
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(v.Int() + 7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(v.Uint() + 7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(v.Float() + 7)
	case reflect.Pointer:
		// A new pointer to a changed copy: the fresh value's target stays
		// as it was.
		p := reflect.New(v.Type().Elem())
		if !v.IsNil() {
			p.Elem().Set(v.Elem())
		}
		if !differ(p.Elem(), name) {
			return false
		}
		v.Set(p)
	case reflect.Struct:
		// A copy, so a pointer or slice the field shares with fresh is not
		// changed in place.
		c := reflect.New(v.Type()).Elem()
		c.Set(v)
		changed := false
		for i := range c.NumField() {
			if c.Type().Field(i).IsExported() && differ(c.Field(i), name+"."+c.Type().Field(i).Name) {
				changed = true
			}
		}
		if !changed {
			return false
		}
		v.Set(c)
	case reflect.Slice:
		e := reflect.New(v.Type().Elem()).Elem()
		differ(e, name)
		v.Set(reflect.Append(reflect.AppendSlice(reflect.MakeSlice(v.Type(), 0, v.Len()+1), v), e))
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return false
		}
		m := reflect.MakeMap(v.Type())
		for _, k := range v.MapKeys() {
			m.SetMapIndex(k, v.MapIndex(k))
		}
		e := reflect.New(v.Type().Elem()).Elem()
		differ(e, name)
		m.SetMapIndex(reflect.ValueOf("SYNTHETIC-"+name).Convert(v.Type().Key()), e)
		v.Set(m)
	default:
		return false
	}
	return true
}

// L142: a bring-over takes every device key from a fresh install except the
// written allow list, and every other key from the old disk. The test walks
// the struct tags, so a new device key is covered without a change here, and
// every field of the old disk differs from fresh, pointer and struct kinds
// too (G4).
func TestWithDeviceKeysFrom(t *testing.T) {
	fresh, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	old := fresh
	v := reflect.ValueOf(&old).Elem()
	typ := v.Type()
	for i := range v.NumField() {
		if !typ.Field(i).IsExported() {
			continue
		}
		if !differ(v.Field(i), typ.Field(i).Name) {
			t.Fatalf("%s: kind %s cannot be made to differ; extend differ", typ.Field(i).Name, v.Field(i).Kind())
		}
	}
	n := reflect.ValueOf(fresh)
	for i := range typ.NumField() {
		if typ.Field(i).IsExported() && reflect.DeepEqual(v.Field(i).Interface(), n.Field(i).Interface()) {
			t.Fatalf("%s: the old disk's value still equals fresh", typ.Field(i).Name)
		}
	}
	freshCopy, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}

	got, err := WithDeviceKeysFrom(old, fresh, BringOverKeeps)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := deviceScoped()
	if err != nil {
		t.Fatal(err)
	}
	g, o := reflect.ValueOf(got), reflect.ValueOf(old)
	devices := 0
	for i := range typ.NumField() {
		key, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		_, kept := BringOverKeeps[key]
		want := o.Field(i)
		if keys[key] && !kept {
			want = n.Field(i)
			devices++
		}
		if !reflect.DeepEqual(g.Field(i).Interface(), want.Interface()) {
			t.Errorf("%s: got %v, want %v", key, g.Field(i).Interface(), want.Interface())
		}
	}
	if devices == 0 {
		t.Error("no device key was reset")
	}
	if got.Username != old.Username {
		t.Errorf("username %q, want the old disk's", got.Username)
	}
	if got.Hostname != fresh.Hostname || !reflect.DeepEqual(got.Tailscale, fresh.Tailscale) ||
		!reflect.DeepEqual(got.PrebootLock, fresh.PrebootLock) || !reflect.DeepEqual(got.Management, fresh.Management) {
		t.Errorf("device keys of the old computer came along: %+v", got)
	}
	// Building the old disk's values changed nothing fresh points to.
	if !reflect.DeepEqual(fresh, freshCopy) {
		t.Error("fresh changed")
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
