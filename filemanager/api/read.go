package api

// ReadRequest bounds the amount of file content returned by the backend.
type ReadRequest struct {
	Location
	Offset int64 `json:"offset"`
	Limit  int64 `json:"limit"`
}
type Tally struct {
	Files, Dirs, Size, Unreadable int64
	Done                          bool
	Error                         *Error
}
