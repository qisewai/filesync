package api

type TransferRequest struct {
	ID                string   `json:"id"` // caller-generated stable identifier; same request may be retried
	Source            Location `json:"source"`
	Target            Location `json:"target"`
	Move              bool     `json:"move"`
	FileConflict      string   `json:"file_conflict"`      // fail or replace
	DirectoryConflict string   `json:"directory_conflict"` // fail or merge
	// Transitional protection for the foreground index journal. May only add restrictions.
	Protected []Location `json:"protected,omitempty"`
}
type Task struct {
	ID      string `json:"id"`
	State   string `json:"state"` // queued, running, done, failed, canceled, interrupted
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Current string `json:"current,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (t Task) Finished() bool { return t.State != "queued" && t.State != "running" }
