package legacy

import "testing"

// The shipped table parses; it may be empty between renames.
func TestShippedTableParses(t *testing.T) {
	if _, err := rows(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigFieldReadsConfigFieldRows(t *testing.T) {
	// Made-up rows in the shipped format.
	all, err := parse([]byte(`{"renames": [
		{"kind": "config-field", "old": "oldField", "new": "group.field"},
		{"kind": "command", "old": "oldcmd", "new": "newcmd"},
		{"kind": "label", "old": "OLD-LABEL", "new": "NEW-LABEL"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if now, ok := configField(all, "oldField"); !ok || now != "group.field" {
		t.Fatalf("oldField = %q, %v", now, ok)
	}
	// Only config-field rows count: a command or label name is no field.
	for _, old := range []string{"oldcmd", "OLD-LABEL", "group", ""} {
		if now, ok := configField(all, old); ok {
			t.Fatalf("%q = %q, want no row", old, now)
		}
	}
}
