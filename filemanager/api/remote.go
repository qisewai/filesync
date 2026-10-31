package api

import "filemanager/engine"

type Endpoint struct {
	Address, Token              string
	ArchivePath, ArchiveVersion string
	LocalArchive                bool
}
type RemoteCommand struct {
	Kind                 string
	Source, Target       Endpoint
	Root, TargetRoot     int64
	Paths                []string
	TargetPath           string
	LocalPaths           [][]byte
	Destination, Partial []byte
	Protected            []Location
}
type RemoteProgress struct {
	Done, Total int64
	Current     string
}
type RemoteFrame struct {
	Done           bool
	Error          *Error
	Progress       *RemoteProgress
	EngineProgress *engine.Progress
	Action         *engine.Action
	ActionError    string
	Outcome        *BackupOutcome
	Whole          map[string]bool
	Errors         []string
}
