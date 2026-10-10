package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"image"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"filesync/fileindex"
	"github.com/egoist/mygo/ui"
)

const textPreviewLimit = 256 << 10
const imagePreviewLimit = 32 << 20

type preview struct {
	open, loading, actual bool
	server                string
	client                *fileindex.Client
	entry                 fileindex.Entry
	group                 []fileindex.Entry
	text, err, note       string
	bitmap                *ui.Bitmap
	cancel                context.CancelFunc
	zoom                  float32
}

func previewKind(e fileindex.Entry) string {
	if e.IsDir {
		return ""
	}
	ext := strings.ToLower(path.Ext(e.Name))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp":
		return "image"
	case ".txt", ".md", ".log", ".csv", ".tsv", ".json", ".jsonl", ".yaml", ".yml", ".toml", ".ini", ".conf", ".cfg", ".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".vue", ".css", ".html", ".htm", ".xml", ".svg", ".sh", ".sql", ".rs", ".c", ".h", ".cpp", ".java", ".srt", ".vtt":
		return "text"
	}
	if !strings.Contains(e.Name, ".") || strings.HasPrefix(e.Name, ".") && strings.Count(e.Name, ".") == 1 {
		return "text"
	}
	return ""
}
func mediaFile(e fileindex.Entry) bool {
	switch strings.ToLower(path.Ext(e.Name)) {
	case ".mp4", ".mkv", ".avi", ".mov", ".webm", ".m4v", ".mpeg", ".mpg", ".mts", ".m2ts", ".wmv", ".flv", ".mp3", ".flac", ".wav", ".ogg", ".opus", ".m4a", ".aac", ".wma", ".aiff":
		return true
	}
	return false
}
func (a *app) sourceCurrent(server string, c *fileindex.Client) bool {
	sv := a.store.Server(server)
	return !a.closed && sv != nil && sv.Addr == c.Addr && sv.Token == c.Token && sv.Token != ""
}
func (a *app) closePreview() {
	if p := a.preview; p != nil {
		p.cancel()
		a.preview = nil
	}
}
func (a *app) showPreview(server string, c *fileindex.Client, e fileindex.Entry, group []fileindex.Entry) {
	a.closePreview()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	p := &preview{open: true, loading: true, server: server, client: c, entry: e, group: group, cancel: cancel, zoom: 1}
	a.preview = p
	kind := previewKind(e)
	go func() {
		defer cancel()
		var bitmap *ui.Bitmap
		var text, note string
		var err error
		limit := int64(textPreviewLimit)
		if kind == "image" {
			limit = imagePreviewLimit
		}
		var data []byte
		var truncated bool
		if kind == "image" && e.Size > limit {
			err = fmt.Errorf("图片超过 32 MB，请用系统程序打开")
		} else {
			data, truncated, err = c.ReadPreview(ctx, e.Root, e.Path, limit)
		}
		if err == nil {
			if kind == "image" {
				if truncated {
					err = fmt.Errorf("图片超过 32 MB，请用系统程序打开")
				} else {
					var cfg image.Config
					cfg, _, err = image.DecodeConfig(bytes.NewReader(data))
					if err == nil && (cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 16_000_000) {
						err = fmt.Errorf("图片超过 1600 万像素，请用系统程序打开")
					}
					if err == nil {
						bitmap, err = ui.DecodeBitmap(data)
					}
					if err == nil {
						w, h := bitmap.Size()
						note = fmt.Sprintf("%d × %d · %s", w, h, formatBytes(e.Size))
						if strings.EqualFold(path.Ext(e.Name), ".gif") {
							note += " · GIF 显示首帧"
						}
					}
				}
			} else {
				text, err = decodePreviewText(data, truncated)
				note = "UTF-8 / UTF-16 · 只读，可选择复制"
				if truncated {
					note += " · 仅显示前 256 KiB，完整内容请用系统程序打开"
				}
			}
		}
		a.post(func() {
			if a.preview != p || !p.open || !a.sourceCurrent(server, c) {
				return
			}
			p.loading = false
			p.bitmap = bitmap
			p.text = text
			p.note = note
			if err != nil {
				p.err = err.Error()
				if err == fileindex.ErrAuth {
					a.signedOut(a.store.Server(server))
				}
			}
		})
	}()
}
func decodePreviewText(data []byte, truncated bool) (string, error) {
	if len(data) >= 2 && (bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff})) {
		var order binary.ByteOrder = binary.LittleEndian
		if data[0] == 0xfe {
			order = binary.BigEndian
		}
		data = data[2:]
		if len(data)%2 != 0 && !truncated {
			return "", fmt.Errorf("UTF-16 文本不完整")
		}
		units := make([]uint16, len(data)/2)
		for i := range units {
			units[i] = order.Uint16(data[2*i:])
		}
		if truncated && len(units) > 0 && units[len(units)-1] >= 0xd800 && units[len(units)-1] <= 0xdbff {
			units = units[:len(units)-1]
		}
		return string(utf16.Decode(units)), nil
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if truncated {
		for i := 0; i < 3 && len(data) > 0 && !utf8.Valid(data); i++ {
			data = data[:len(data)-1]
		}
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return "", fmt.Errorf("这不是 UTF-8/UTF-16 文本，请用系统程序打开")
	}
	return string(data), nil
}
func (a *app) previewStep(delta int) {
	p := a.preview
	for i, e := range p.group {
		if e.Path == p.entry.Path && e.Root == p.entry.Root {
			j := i + delta
			if j >= 0 && j < len(p.group) {
				a.showPreview(p.server, p.client, p.group[j], p.group)
			}
			return
		}
	}
}
func (a *app) filePreviewView(c *ui.Context) {
	p := a.preview
	if p == nil {
		return
	}
	t := c.Theme()
	ui.DialogBase(c.Key("native-preview"), &p.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.45))
		w, h := c.Size()
		panel.Width(min(820, w-32)).Height(min(540, h-32)).Padding(18).Gap(10).Radius(t.Radius*3).Background(t.Background).Border(1, t.Border)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, p.entry.Name).Bold().FontSize(18).Grow(1).Shrink(1).SingleLine()
			idx := -1
			for i, e := range p.group {
				if e.Path == p.entry.Path && e.Root == p.entry.Root {
					idx = i
					break
				}
			}
			ui.Button(c, "上一项").Disabled(idx <= 0).OnClick(func() { a.previewStep(-1) })
			ui.Button(c, "下一项").Disabled(idx < 0 || idx >= len(p.group)-1).OnClick(func() { a.previewStep(1) })
			ui.Button(c, "系统程序打开").OnClick(func() { a.closePreview(); a.systemOpenSource(p.server, p.client, p.entry, false) })
			ui.Button(c, "关闭预览").OnClick(a.closePreview)
		})
		ui.Text(c, p.entry.Path).FontSize(12).TextColor(t.TextMuted).SingleLine()
		if p.loading {
			ui.Column(c).Grow(1).Center().Children(func() { ui.Spinner(c).Label("正在加载预览") })
		} else if p.err != "" {
			notice(c, t.Danger, p.err)
			ui.Spacer(c)
		} else if p.bitmap != nil {
			ui.Row(c).Gap(8).Children(func() {
				ui.Button(c, "适合窗口").OnClick(func() { p.actual = false })
				ui.Button(c, "原始大小").OnClick(func() { p.actual = true; p.zoom = 1 })
				ui.Button(c, "缩小").OnClick(func() { p.actual = true; p.zoom = max(0.125, p.zoom/1.25) })
				ui.Button(c, "放大").OnClick(func() { p.actual = true; p.zoom = min(4, p.zoom*1.25) })
				if p.actual {
					ui.Text(c, fmt.Sprintf("%.0f%%", p.zoom*100))
				}
			})
			if p.actual {
				w, h := p.bitmap.Size()
				ui.ScrollBoth(c.Key("image-scroll")).Grow(1).MinHeight(0).Children(func() { ui.Image(c, p.bitmap).Width(float32(w) * p.zoom).Height(float32(h) * p.zoom) })
			} else {
				ui.Image(c, p.bitmap).Grow(1).MinHeight(0).Fit(ui.Contain)
			}
		} else {
			ui.TextArea(c.Key("preview-text"), &p.text).ReadOnly(true).Grow(1).MinHeight(0).Label("文本预览")
		}
		ui.Text(c, p.note).FontSize(12).TextColor(t.TextMuted).SingleLine()
	})
	if !p.open && a.preview == p {
		a.closePreview()
	}
}

// Cache identities include the server address and remote version; filenames
// never contain NAS directory components or Windows-reserved characters.
func openCachePath(c *fileindex.Client, e fileindex.Entry) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s\x00%d\x00%d", c.Addr, e.Root, e.Path, e.Size, e.MTime)))
	name := strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, path.Base(e.Name))
	if len(name) > 235 {
		ext := path.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		stem := strings.TrimSuffix(name, ext)
		stem = stem[:235-len(ext)]
		for !utf8.ValidString(stem) {
			stem = stem[:len(stem)-1]
		}
		name = stem + ext
	}
	name = strings.TrimRight(name, ". ")
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	// Prefix avoids CON, NUL and the other Windows device names.
	return filepath.Join(cache, "filesync", "open", fmt.Sprintf("%x", key), "file-"+name), nil
}
func (a *app) systemOpenSource(server string, c *fileindex.Client, e fileindex.Entry, download bool) {
	if !a.sourceCurrent(server, c) {
		return
	}
	dest, err := openCachePath(c, e)
	if err != nil {
		a.errorMsg = err.Error()
		return
	}
	// The directory identifies the NAS version. Reuse a complete cached file
	// even when an Office application edited it locally; opening it again must
	// not silently overwrite those edits. A new NAS version gets a new directory.
	if !mediaFile(e) || download {
		if fi, err := os.Stat(dest); err == nil && fi.Mode().IsRegular() {
			a.openPath(dest)
			return
		}
	}
	if a.proxy == nil {
		a.proxy, err = fileindex.NewLocalProxy(func(server string, c *fileindex.Client) {
			a.post(func() {
				if a.sourceCurrent(server, c) {
					a.errorMsg = "登录会话已过期，请重新登录 NAS"
					a.signedOut(a.store.Server(server))
				}
			})
		})
		if err != nil {
			a.errorMsg = err.Error()
			return
		}
	}
	address, err := a.proxy.Register(server, c, e.Root, e.Path)
	if err != nil {
		a.errorMsg = err.Error()
		return
	}
	tr := a.addTransfer("打开", e.Name)
	tr.server = server
	tr.total = e.Size
	ctx, cancel := context.WithCancel(context.Background())
	tr.cancel = cancel
	go func() {
		var err error
		if mediaFile(e) && !download {
			// A playlist is a local file, so the OS selects a media application instead
			// of treating the proxy's HTTP URL as an ordinary browser link.
			dest += ".m3u"
			err = os.MkdirAll(filepath.Dir(dest), 0700)
			if err == nil {
				err = os.WriteFile(dest, []byte("#EXTM3U\n"+address+"\n"), 0600)
			}
		} else {
			err = fileindex.DownloadProxyFile(ctx, address, dest, func(p fileindex.Progress) { a.post(func() { tr.done = p.Done; tr.total = p.Total }) })
		}
		a.post(func() {
			defer cancel()
			if err == nil && ctx.Err() != nil {
				err = ctx.Err()
			}
			if err == nil && a.sourceCurrent(server, c) && ctx.Err() == nil {
				tr.after = func() { a.openPath(dest) }
			}
			a.finishTransfer(tr, err, nil)
		})
	}()
}
