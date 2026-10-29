package usbtrust

// MediaDecision is one memory card (or other removable medium) inside an
// allowed USB storage reader. USBGuard only decides about the reader; each
// inserted medium stays unread until the owner allows that insertion.
// Connection binds the review to this insertion: a swap or removal ends it.
type MediaDecision struct {
	ID         string `json:"id"`
	Connection string `json:"connection"`
	Reader     string `json:"reader,omitempty"`
	VIDPID     string `json:"vidPid,omitempty"`
	Port       string `json:"port,omitempty"`
	Bytes      uint64 `json:"bytes,omitempty"`
	// Kind is what the medium looks like (MediaDisc, MediaSDCard, ...),
	// for the notice only. It comes from what the device reports about
	// itself, so it never decides anything.
	Kind    string `json:"kind,omitempty"`
	Allowed bool   `json:"allowed"`
}

// Media kinds. An empty Kind means plain USB storage: a stick, a disk or a
// reader that gave no hint.
const (
	MediaDisc        = "disc"
	MediaSDCard      = "sd-card"
	MediaMemoryStick = "memory-stick"
	MediaCard        = "card"
)
