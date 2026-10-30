package main

import (
	"strings"

	"filemanager/remoteclient"

	"github.com/egoist/mygo/ui"
)

// Icons, drawn in the color of their text: line drawings on a 24 grid.
func icon(paths string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">` + paths + `</svg>`))
}

var (
	iconCount       = icon(`<path d="M21 12A9 9 0 1 1 12 3v9z"/><path d="M15.5 3.7A9 9 0 0 1 20.3 8.5H15.5z"/>`)
	iconDetails     = icon(`<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M15 4v16"/>`)
	iconFolder      = icon(`<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>`)
	iconFile        = icon(`<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/>`)
	iconDoc         = icon(`<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/><path d="M9 13h6"/><path d="M9 17h6"/>`)
	iconImage       = icon(`<rect x="3" y="4" width="18" height="16" rx="2"/><circle cx="9" cy="10" r="2"/><path d="m21 16-5-5-9 9"/>`)
	iconVideo       = icon(`<rect x="2" y="5" width="14" height="14" rx="2"/><path d="m16 10 6-3v10l-6-3z"/>`)
	iconAudio       = icon(`<circle cx="7" cy="17" r="3"/><circle cx="18" cy="15" r="3"/><path d="M10 17V5l11-2v12"/>`)
	iconArchive     = icon(`<rect x="3" y="3" width="18" height="5" rx="1"/><path d="M5 8v11a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8"/><path d="M10 12h4"/>`)
	iconCode        = icon(`<path d="m8 7-5 5 5 5"/><path d="m16 7 5 5-5 5"/>`)
	iconServer      = icon(`<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7.5h.01"/><path d="M7 16.5h.01"/>`)
	iconRoots       = icon(`<ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v14c0 1.7 3.6 3 8 3s8-1.3 8-3V5"/><path d="M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3"/>`)
	iconTrash       = icon(`<path d="M3 6h18"/><path d="M8 6V4h8v2"/><path d="M6 6l1 14a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-14"/>`)
	iconBackup      = icon(`<path d="M7 18a5 5 0 1 1 .9-9.9A6 6 0 0 1 19 10a4 4 0 0 1 0 8"/><path d="M12 12v9"/><path d="M8.5 15.5 12 12l3.5 3.5"/>`)
	iconLaptop      = icon(`<rect x="4" y="5" width="16" height="11" rx="2"/><path d="M2 20h20"/>`)
	iconHome        = icon(`<path d="M3 11 12 4l9 7"/><path d="M5 10v10h14V10"/><path d="M10 20v-6h4v6"/>`)
	iconSidebar     = icon(`<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M9 4v16"/>`)
	iconDisk        = icon(`<rect x="3" y="13" width="18" height="7" rx="2"/><path d="M5 13 7.5 5h9L19 13"/><path d="M16 16.5h.01"/>`)
	iconNetwork     = icon(`<rect x="9" y="3" width="6" height="5" rx="1"/><rect x="3" y="16" width="6" height="5" rx="1"/><rect x="15" y="16" width="6" height="5" rx="1"/><path d="M12 8v4"/><path d="M6 16v-2h12v2"/>`)
	iconTasks       = icon(`<path d="M9 6h11"/><path d="M9 12h11"/><path d="M9 18h11"/><path d="m3 6 1.5 1.5L7 5"/><path d="m3 12 1.5 1.5L7 11"/><path d="m3 18 1.5 1.5L7 17"/>`)
	iconClose       = icon(`<path d="M6 6l12 12"/><path d="M18 6 6 18"/>`)
	iconAlert       = icon(`<circle cx="12" cy="12" r="9"/><path d="M12 7.5v5.5"/><path d="M12 16.5h.01"/>`)
	iconRetry       = icon(`<path d="M20 11a8 8 0 1 0-2.3 5.7"/><path d="M20 4v7h-7"/>`)
	iconPrev        = icon(`<path d="m15 5-7 7 7 7"/>`)
	iconNext        = icon(`<path d="m9 5 7 7-7 7"/>`)
	iconZoomIn      = icon(`<circle cx="11" cy="11" r="7"/><path d="m20 20-4-4"/><path d="M11 8v6"/><path d="M8 11h6"/>`)
	iconZoomOut     = icon(`<circle cx="11" cy="11" r="7"/><path d="m20 20-4-4"/><path d="M8 11h6"/>`)
	iconFit         = icon(`<path d="M4 9V4h5"/><path d="M20 9V4h-5"/><path d="M4 15v5h5"/><path d="M20 15v5h-5"/>`)
	iconActual      = icon(`<rect x="3" y="5" width="18" height="14" rx="2"/><path d="M8 10v4"/><path d="M16 10v4"/><path d="M12 10.5h.01"/><path d="M12 13.5h.01"/>`)
	iconOpenOut     = icon(`<path d="M14 4h6v6"/><path d="m20 4-9 9"/><path d="M18 14v4a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4"/>`)
	iconSave        = icon(`<path d="M12 4v11"/><path d="m7 10 5 5 5-5"/><path d="M5 20h14"/>`)
	iconSaveOpen    = icon(`<path d="M7 3v11"/><path d="m3 10 4 4 4-4"/><path d="M3 20h18"/><path d="M15 4h6v6"/><path d="m21 4-8 8"/>`)
	iconUp          = icon(`<path d="M12 19V5"/><path d="m5 12 7-7 7 7"/>`)
	iconSearch      = icon(`<circle cx="11" cy="11" r="7"/><path d="m20 20-4-4"/>`)
	iconChevronDown = icon(`<path d="m6 9 6 6 6-6"/>`)
	iconPlus        = icon(`<path d="M12 5v14"/><path d="M5 12h14"/>`)
	iconNewDir      = icon(`<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><path d="M12 10v6"/><path d="M9 13h6"/>`)
	iconUpload      = icon(`<path d="M12 15V4"/><path d="m7 9 5-5 5 5"/><path d="M5 20h14"/>`)
	iconCopy        = icon(`<rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2"/>`)
	iconMove        = icon(`<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><path d="M10 13h6"/><path d="m13 10 3 3-3 3"/>`)
	iconRename      = icon(`<path d="M4 20h4L19 9l-4-4L4 16z"/><path d="m13 7 4 4"/>`)
	iconList        = icon(`<path d="M8 6h12"/><path d="M8 12h12"/><path d="M8 18h12"/><path d="M4 6h.01"/><path d="M4 12h.01"/><path d="M4 18h.01"/>`)
	iconGrid        = icon(`<rect x="4" y="4" width="7" height="7" rx="1.5"/><rect x="13" y="4" width="7" height="7" rx="1.5"/><rect x="4" y="13" width="7" height="7" rx="1.5"/><rect x="13" y="13" width="7" height="7" rx="1.5"/>`)
	iconMore        = icon(`<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>`)
	iconGear        = icon(`<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>`)
	iconPlay        = icon(`<path d="M7 5v14l11-7z"/>`)
	iconStop        = icon(`<rect x="6" y="6" width="12" height="12" rx="2"/>`)
	iconCheck       = icon(`<path d="m5 12 5 5 9-10"/>`)
	iconMinimize    = icon(`<path d="M6 12h12"/>`)
	iconMaximize    = icon(`<rect x="6" y="6" width="12" height="12" rx="1.5"/>`)
	iconRestoreWin  = icon(`<rect x="5" y="8" width="11" height="11" rx="1.5"/><path d="M8 8V6.5A1.5 1.5 0 0 1 9.5 5H17.5A1.5 1.5 0 0 1 19 6.5V14.5A1.5 1.5 0 0 1 17.5 16H16"/>`)
	iconStar        = icon(`<path d="M12 3.5l2.6 5.3 5.9.9-4.3 4.1 1 5.8L12 16.9l-5.2 2.7 1-5.8-4.3-4.1 5.9-.9z"/>`)
	iconStarFilled  = icon(`<path fill="currentColor" d="M12 3.5l2.6 5.3 5.9.9-4.3 4.1 1 5.8L12 16.9l-5.2 2.7 1-5.8-4.3-4.1 5.9-.9z"/>`)
	iconLink        = icon(`<path d="M9 15l6-6"/><path d="M10 6l1-1a4 4 0 0 1 6 6l-1 1"/><path d="M14 18l-1 1a4 4 0 0 1-6-6l1-1"/>`)
)

// fileKind sorts an entry for its icon and color.
func fileKind(e fileindex.Entry) string {
	if e.IsDir {
		return "dir"
	}
	switch e.Ext {
	case "jpg", "jpeg", "png", "gif", "bmp", "webp", "heic", "heif", "tif", "tiff", "svg", "raw", "cr2", "nef", "arw", "dng", "psd":
		return "image"
	case "mp4", "mkv", "avi", "mov", "wmv", "flv", "webm", "m4v", "m2ts", "rmvb", "rm", "3gp", "mpg", "mpeg", "vob":
		return "video"
	case "mp3", "flac", "wav", "aac", "m4a", "ogg", "wma", "ape", "opus":
		return "audio"
	case "zip", "rar", "7z", "tar", "gz", "tgz", "bz2", "xz", "zst":
		return "archive"
	// disk images: of virtual machines, Ghost and other backups, discs
	case "vmdk", "vdi", "vhd", "vhdx", "avhd", "avhdx", "qcow", "qcow2", "qed", "hdd", "ova", "xva",
		"gho", "ghs", "v2i", "tib", "tibx", "wim", "esd", "swm", "iso", "img", "dmg", "nrg", "mdf":
		return "disk"
	case "pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "txt", "md", "rtf", "odt", "ods", "odp", "csv", "epub", "wps", "et", "dps":
		return "doc"
	case "go", "py", "js", "ts", "java", "c", "cpp", "h", "hpp", "cs", "rs", "php", "rb", "sh", "sql", "json", "xml", "yaml", "yml", "html", "css", "vue", "kt", "swift":
		return "code"
	}
	return "file"
}

// fileIcon shows an entry's icon, in the color of its kind.
func fileIcon(c *ui.Context, e fileindex.Entry, size float32) ui.Element {
	svg, color := iconFile, c.Theme().TextMuted
	switch fileKind(e) {
	case "dir":
		svg, color = iconFolder, folderColor
	case "image":
		svg, color = iconImage, ui.Hex("#2f9e6b")
	case "video":
		svg, color = iconVideo, ui.Hex("#8a5cd6")
	case "audio":
		svg, color = iconAudio, ui.Hex("#d6588f")
	case "archive":
		svg, color = iconArchive, ui.Hex("#a8743a")
	case "disk":
		svg, color = iconDisk, ui.Hex("#475569")
	case "doc":
		svg, color = iconDoc, ui.Hex("#3b7ddd")
	case "code":
		svg, color = iconCode, ui.Hex("#1f9aa8")
	}
	return ui.Icon(c, svg).FontSize(size).TextColor(color)
}

// labeled builds an icon then a label, in a row of what holds them.
func labeled(c *ui.Context, svg *ui.SVG, label string, color ui.Color) {
	ui.Icon(c, svg).FontSize(16).TextColor(color).Margin(0, 4, 0, 0)
	ui.Text(c, label).SingleLine().Grow(1).Shrink(1).MinWidth(0)
}

// extOf is the extension of a name, lowercased, without its dot.
func extOf(name string) string {
	for i := len(name) - 1; i > 0; i-- {
		if name[i] == '.' {
			return strings.ToLower(name[i+1:])
		}
		if name[i] == '/' {
			break
		}
	}
	return ""
}
