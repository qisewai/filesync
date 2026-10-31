package api

import "filemanager/engine"

type BackupSpec struct {
	Source                               []byte
	Mode                                 engine.Mode
	Excludes, Only                       []string
	Address, Token                       string
	Root                                 int64
	Path                                 string
	ArchiveName, ArchivePrefix, Compress string
}
type BackupCommand struct {
	Action, ID string
	Spec       BackupSpec
}
type BackupError struct {
	Action  engine.Action
	Message string
}
type BackupOutcome struct {
	Result engine.Result
	Errors []BackupError
	Fatal  string
}
type BackupFrame struct {
	Done        bool
	ID          string
	Error       *Error
	Plan        *engine.Plan
	Scan        *engine.ScanProgress
	Progress    *engine.Progress
	Action      *engine.Action
	ActionError string
	Outcome     *BackupOutcome
}

func EncodeBackupOutcome(result *engine.Result) *BackupOutcome {
	out := &BackupOutcome{Result: *result}
	out.Result.Errors = nil
	out.Result.Fatal = nil
	if result.Fatal != nil {
		out.Fatal = result.Fatal.Error()
	}
	for _, e := range result.Errors {
		out.Errors = append(out.Errors, BackupError{e.Action, e.Err.Error()})
	}
	return out
}
