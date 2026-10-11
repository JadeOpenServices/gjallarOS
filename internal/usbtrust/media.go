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
	Allowed    bool   `json:"allowed"`
}
