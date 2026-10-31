// Package localpath maps desktop filesystem paths to backend locations.
package localpath

import (
	"filemanager/api"
	"path/filepath"
	"strings"
)

func Location(p string) api.Location {
	p = filepath.Clean(p)
	volume := filepath.VolumeName(p)
	if volume != "" {
		return api.Location{Root: "local-" + strings.ToUpper(filepath.ToSlash(volume)), Path: filepath.ToSlash(strings.TrimLeft(strings.TrimPrefix(p, volume), `\/`))}
	}
	return api.Location{Root: "local", Path: filepath.ToSlash(strings.TrimPrefix(p, string(filepath.Separator)))}
}
