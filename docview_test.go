package main

import (
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filemanager/remoteclient/fitest"
	"github.com/egoist/mygo/ui"
)

func TestDocFilesServeOnlyTheDocumentShown(t *testing.T) {
	var d docFiles
	get := func(url string) (int, string, string) {
		rec := httptest.NewRecorder()
		d.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		body, _ := io.ReadAll(rec.Body)
		return rec.Code, rec.Header().Get("Content-Type"), string(body)
	}
	pdf := d.put("pdf", []byte("%PDF-1.7"))
	if code, typ, body := get(pdf); code != 200 || typ != "application/pdf" || body != "%PDF-1.7" {
		t.Fatalf("pdf: %d %q %q", code, typ, body)
	}
	page := d.put("html", []byte(`<p class="x">你好</p><script>alert(1)</script>`))
	if page == pdf {
		t.Fatal("documents share a URL")
	}
	// The one before is gone.
	if code, _, _ := get(pdf); code != 404 {
		t.Fatalf("old document still served: %d", code)
	}
	code, typ, body := get(page)
	if code != 200 || !strings.HasPrefix(typ, "text/html") {
		t.Fatalf("html: %d %q", code, typ)
	}
	// The page goes in a frame without scripts, under a policy loading
	// nothing, escaped so that it cannot leave the frame.
	for _, want := range []string{"<iframe sandbox ", "default-src 'none'", `srcdoc="&lt;p class=&#34;x&#34;&gt;你好&lt;/p&gt;&lt;script&gt;`} {
		if !strings.Contains(body, want) {
			t.Fatalf("no %q in %s", want, body)
		}
	}
	if strings.Contains(body, "<script>") {
		t.Fatal("page's script left unescaped")
	}
	if code, _, _ := get(docScheme + "://other/" + strings.TrimPrefix(page, docScheme+"://doc/")); code != 404 {
		t.Fatal("served under another host")
	}
	d.drop()
	if code, _, _ := get(page); code != 404 {
		t.Fatal("dropped document still served")
	}
}

func TestDocNavigable(t *testing.T) {
	for url, ok := range map[string]bool{
		"fsprev://doc/abc":                       true,
		"about:srcdoc":                           true,
		"about:blank":                            true,
		"webkit-pdfjs-viewer://pdfjs/web/viewer": true,
		"https://example.com/":                   false,
		"file:///etc/passwd":                     false,
		"javascript:alert(1)":                    false,
	} {
		if docNavigable(url) != ok {
			t.Errorf("docNavigable(%q) = %v", url, !ok)
		}
	}
}

func TestDocumentPreviewLoadsInWebView(t *testing.T) {
	f := fitest.New(t)
	os.WriteFile(filepath.Join(f.Root, "a.pdf"), []byte("%PDF-1.4 a"), 0644)
	os.WriteFile(filepath.Join(f.Root, "b.pdf"), []byte("%PDF-1.4 b"), 0644)
	os.WriteFile(filepath.Join(f.Root, "页面.html"), []byte("<h1>标题</h1>"), 0644)
	h := browserHarness(t, f)
	var loads []string
	made := 0
	h.a.newDocView = func() (ui.NativeWebView, func(string), error) {
		made++
		return nil, func(u string) { loads = append(loads, u) }, nil
	}
	served := func() string {
		rec := httptest.NewRecorder()
		h.a.docs.ServeHTTP(rec, httptest.NewRequest("GET", loads[len(loads)-1], nil))
		return rec.Body.String()
	}
	h.click("a.pdf")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	p := h.a.preview
	if p == nil || p.doc == "" || len(loads) != 1 || loads[0] != p.doc || served() != "%PDF-1.4 a" {
		t.Fatalf("pdf not loaded: %+v %v", p, loads)
	}
	// The arrows step among the PDFs, in the same web view.
	h.a.previewStep(1)
	h.settle()
	if h.a.preview.entry.Name != "b.pdf" || served() != "%PDF-1.4 b" || made != 1 {
		t.Fatalf("stepped to %q, loads %v, made %d", h.a.preview.entry.Name, loads, made)
	}
	h.click("关闭预览")
	if loads[len(loads)-1] != "about:blank" {
		t.Fatalf("closing kept the page: %v", loads)
	}
	// A web page shows rendered, or as its source.
	h.clickPage("页面.html")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if p := h.a.preview; p == nil || p.text != "<h1>标题</h1>" || !strings.Contains(served(), "srcdoc=") {
		t.Fatalf("html: %+v", h.a.preview)
	}
	h.want("网页 · 不运行脚本，不加载外部资源")
	h.click("源代码")
	if !h.a.preview.source {
		t.Fatal("source not shown")
	}
	h.want("文本预览") // the source, in the text area
	h.click("关闭预览")
}

func TestDocumentPreviewWithoutWebView(t *testing.T) {
	f := fitest.New(t)
	os.WriteFile(filepath.Join(f.Root, "a.pdf"), []byte("%PDF-1.4 a"), 0644)
	h := browserHarness(t, f)
	h.a.newDocView = func() (ui.NativeWebView, func(string), error) {
		return nil, nil, errors.New("没有 WebKitGTK")
	}
	opened := ""
	h.a.openPath = func(p string) { opened = p }
	h.click("a.pdf")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	h.want("无法在这里显示：没有 WebKitGTK")
	h.click("用系统程序打开")
	h.waitTransfers()
	if h.a.preview != nil || opened == "" {
		t.Fatalf("system open: preview %v, opened %q", h.a.preview != nil, opened)
	}
}
