package usbtrust

// LearnBluetoothAddresses stores the Bluetooth address of built-in parts
// that match their stored identity but have no address stored yet. The
// address is only readable once the part is allowed, so it is learned
// after enrollment, the first time it is seen. It reports whether doc
// changed.
func LearnBluetoothAddresses(input AuditInput, doc *Document) (bool, error) {
	audit, err := Audit(input)
	if err != nil {
		return false, err
	}

	changed := false

	for _, finding := range audit.Findings {
		if finding.Code != CodeInternalMatch || !wantsBluetoothAddress(input.Expected, finding.Role) {
			continue
		}

		address := ""
		for _, observed := range input.Observed {
			if observed.RuntimeID == finding.RuntimeID {
				address = observed.Identity.BluetoothAddress
			}
		}

		if address == "" {
			continue
		}

		for i := range doc.Devices {
			device := &doc.Devices[i]
			if device.ID == finding.TrustedID && device.Identity.BluetoothAddress == "" {
				device.Identity.BluetoothAddress = address
				changed = true
			}
		}
	}

	return changed, nil
}

func wantsBluetoothAddress(expected []ExpectedDevice, role string) bool {
	for _, device := range expected {
		if device.Role == role && device.Port != nil && device.Port.Anchor == AnchorBluetoothAddress {
			return true
		}
	}

	return false
}
