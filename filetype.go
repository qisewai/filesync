package main

import (
	"path"
	"strings"

	"filemanager/remoteclient"
)

// Match the index's extension key so a paged search and a folder sort agree.
func fileExtension(name string) string {
	return strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
}

// A deterministic description works the same for local and remote files,
// including platforms without a file association for the extension.
func fileTypeLabel(e fileindex.Entry) string {
	if e.IsDir {
		return "文件夹"
	}
	if ext := fileExtension(e.Name); ext != "" {
		return strings.ToUpper(ext) + " 文件"
	}
	return "文件"
}
