// Package localtrash is retained for callers migrating to FileManager.
package localtrash

import "filemanager/trash"

func Move(p string) error { return trash.Move(p) }
