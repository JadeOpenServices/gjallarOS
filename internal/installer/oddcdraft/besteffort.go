package oddcdraft

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	portable "github.com/JadeOpenServices/oddc/pkg/oddc"
)

// BestEffort lists the parts of a machine no ODDC model matches, the way
// `oddc devices` does for it: every device the facts report, with the
// catalog component that has its ID and whether that component is known
// not to work on Linux. bound is the kernel drivers bound now, by bus and
// device ID (portable.ReadDrivers). Nothing in the list is proven for this
// machine and nothing in it is applied.
func BestEffort(catalog string, facts portable.Facts, bound map[string]map[string][]string) ([]portable.PresentPart, error) {
	if catalog == "" {
		return nil, errors.New("installer was built without the ODDC catalog")
	}

	registry, err := portable.LoadRegistry(catalog)
	if err != nil {
		return nil, fmt.Errorf("load ODDC catalog: %w", err)
	}

	return registry.PresentParts(facts, bound), nil
}

// WriteBestEffort prints the best-effort label and the parts list. Parts
// known not to work on Linux come first, with ODDC's reason.
func WriteBestEffort(out io.Writer, parts []portable.PresentPart) {
	fmt.Fprintln(out, "Machine support: best effort. No ODDC model matches this machine, so nothing here is proven on it and nothing optional is switched on.")

	known := 0
	var unsupported []portable.PresentPart
	for _, part := range parts {
		if part.ID != "" {
			known++
		}
		if !part.Supported {
			unsupported = append(unsupported, part)
		}
	}

	fmt.Fprintf(out, "Parts found: %d, %d of them in the ODDC catalog.\n", len(parts), known)

	if len(unsupported) == 0 {
		fmt.Fprintln(out, "No part found is known not to work on Linux.")
	} else {
		fmt.Fprintln(out, "Known not to work on Linux:")
		for _, part := range unsupported {
			reason := printable(part.UnsupportedReason)
			if reason == "" {
				reason = "ODDC gives no reason"
			}
			fmt.Fprintf(out, "  %s %s %s: %s\n", printable(part.Bus), printable(part.DeviceID), partName(part), reason)
		}
	}

	if len(parts) > 0 {
		fmt.Fprintln(out, "All parts found:")
		for _, part := range parts {
			fmt.Fprintf(out, "  %s %s %s\n", printable(part.Bus), printable(part.DeviceID), partName(part))
		}
	}
}

func partName(part portable.PresentPart) string {
	if part.ID == "" {
		return "(not in the ODDC catalog)"
	}
	if name := printable(part.Name); name != "" {
		return name
	}
	return printable(part.ID)
}

// printable drops control characters, so catalog text and sysfs IDs
// cannot move the cursor or recolour the installer's terminal.
func printable(value string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || !unicode.IsPrint(r) && r != ' ' {
			return -1
		}
		return r
	}, value))
}
