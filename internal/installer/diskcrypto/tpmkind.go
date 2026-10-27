package diskcrypto

// TPMKind says where the TPM sits. A separate chip talks to the CPU over a
// wire on the board; a TPM inside the CPU (firmware TPM) has no such wire.
// Without a startup PIN, a separate chip can be brought to give out the disk
// key by someone with the device and special tools (ROADMAP section 19).
type TPMKind string

const (
	TPMFirmware TPMKind = "firmware"
	TPMSeparate TPMKind = "separate"
	// TPMUnknown is handled like a separate chip and shown as not confirmed.
	TPMUnknown TPMKind = ""
)

// ModelTPMKind is the TPM kind of this model. Per-model security facts come
// only from ODDC, and ODDC has no TPM kind yet (asked as answer item 103,
// Plane 248), so every model is TPMUnknown until it sends one.
func ModelTPMKind() TPMKind {
	return TPMUnknown
}

// PINRecommended reports models where only a startup PIN keeps the disk
// key in the chip against an attacker who has the device.
func (k TPMKind) PINRecommended() bool {
	return k != TPMFirmware
}

// Describe is the kind in words for status output.
func (k TPMKind) Describe() string {
	switch k {
	case TPMFirmware:
		return "inside the processor (firmware TPM)"
	case TPMSeparate:
		return "separate chip"
	}
	return "not confirmed, handled as a separate chip"
}
