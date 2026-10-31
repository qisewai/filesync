package api

// MutationRequest describes one explicitly requested filesystem change.
// Rename never replaces an existing destination. Delete is permanent.
type MutationRequest struct {
	Action string   `json:"action"`
	Source Location `json:"source"`
	Target Location `json:"target"`
}
type MutationResult struct {
	ID uint64 `json:"id"`
}
