package main

import (
	"crypto/rand"
	"encoding/hex"
	"html"
	"net/http"
	"strings"
	"sync"
)

// Documents, PDFs and web pages, show in the system's web view: the app
// serves the one previewed under a scheme of its own, a web page inside a
// sandbox that runs no scripts and loads nothing from outside.

// docScheme is the scheme the previewed document is served under.
const docScheme = "fsprev"

// htmlPreviewLimit is the largest web page previewed.
const htmlPreviewLimit = 4 << 20

// docCSP keeps a previewed page to itself: no scripts, nothing fetched,
// no frames navigated; its own styles and inline pictures show.
const docCSP = "default-src 'none'; style-src 'unsafe-inline' data:; img-src data:; font-src data:; media-src data:"

// docFiles holds the document previewed, which the web view asks for
// from its own goroutines.
type docFiles struct {
	mu    sync.Mutex
	token string
	kind  string // "pdf" or "html"
	data  []byte
}

// put makes data the document served, of kind, and returns its URL: a
// new one each time, which no other document had.
func (d *docFiles) put(kind string, data []byte) string {
	var b [12]byte
	rand.Read(b[:])
	d.mu.Lock()
	defer d.mu.Unlock()
	d.token, d.kind, d.data = hex.EncodeToString(b[:]), kind, data
	return docScheme + "://doc/" + d.token
}

// drop serves no document.
func (d *docFiles) drop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.token, d.kind, d.data = "", "", nil
}

func (d *docFiles) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	token, kind, data := d.token, d.kind, d.data
	d.mu.Unlock()
	if token == "" || r.URL.Host != "doc" || strings.TrimPrefix(r.URL.Path, "/") != token {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	switch kind {
	case "pdf":
		w.Header().Set("Content-Type", "application/pdf")
		w.Write(data)
	case "html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", docCSP)
		w.Write([]byte(sandboxedPage(string(data))))
	default:
		http.NotFound(w, r)
	}
}

// sandboxedPage shows a web page in a frame that runs no scripts, under
// the policy that keeps it from loading anything: the frame inherits it.
func sandboxedPage(page string) string {
	return `<!doctype html><html><head><meta charset="utf-8">` +
		`<meta http-equiv="Content-Security-Policy" content="` + docCSP + `">` +
		`<style>html,body{margin:0;height:100%;background:#fff}iframe{display:block;border:0;width:100%;height:100%}</style>` +
		`</head><body><iframe sandbox title="网页预览" srcdoc="` + html.EscapeString(page) + `"></iframe></body></html>`
}

// docNavigable tells whether the document's web view may go to url: its
// own document, a page's frame, and the PDF viewer of WebKitGTK.
func docNavigable(url string) bool {
	for _, prefix := range []string{docScheme + ":", "about:", "blob:", "webkit-pdfjs-viewer:"} {
		if strings.HasPrefix(url, prefix) {
			return true
		}
	}
	return false
}

// showDoc shows the document at url in the web view, made the first time.
func (a *app) showDoc(url string) {
	if a.docLoad == nil && a.docErr == "" {
		if a.newDocView == nil {
			a.docErr = "这里没有网页视图"
		} else if v, load, err := a.newDocView(); err != nil {
			a.docErr = err.Error()
		} else {
			a.docView, a.docLoad = v, load
		}
	}
	if a.docLoad != nil {
		a.docLoad(url)
	}
}

// hideDoc lets go of the document shown, and of its page.
func (a *app) hideDoc() {
	a.docs.drop()
	if a.docLoad != nil {
		a.docLoad("about:blank")
	}
}
