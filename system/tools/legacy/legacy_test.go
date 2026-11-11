package legacy

import "testing"

func TestConfigFieldReadsTheShippedTable(t *testing.T) {
	if now, ok := ConfigField("jodsPrebootLockEnable"); !ok || now != "prebootLock.enable" {
		t.Fatalf("jodsPrebootLockEnable = %q, %v", now, ok)
	}
	// Only config-field rows count: a command or label name is no field.
	for _, old := range []string{"rebuild", "JODS-RECOVERY", "prebootLock", ""} {
		if now, ok := ConfigField(old); ok {
			t.Fatalf("%q = %q, want no row", old, now)
		}
	}
}
