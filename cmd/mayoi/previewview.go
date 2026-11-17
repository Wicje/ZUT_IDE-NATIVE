package main

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// previewView is a web page in a tab: the browser for the app we work on,
// as an editor plugin's webview does, in the main area rather than the
// sidebar. For the demo it loads the dev server of the web app.
type previewView struct {
	pane    ui.Handle
	web     *mygo.WebView
	url     string
	destroyed bool
}

func (v *previewView) name() string { return "Zut" }
func (v *previewView) clone() *previewView { return &previewView{url: v.url} }

func (v *previewView) reload() {
	if v.web == nil {
		return
	}
	v.web.Page().ReloadIgnoringCache()
}

const previewURL = "http://127.0.0.1:5173"

// detectDevServer finds the dev server the agent started by trying the
// ports commonly used for web development. Any one that answers is the
// preview. Zut can see which ports are listening, not which project each
// port serves.
func detectDevServer() string {
	candidates := []int{5173, 5174, 5175, 3000, 3001, 8080, 8000, 8081}
	for _, port := range candidates {
		client := &http.Client{Timeout: 400 * time.Millisecond}
		req, err := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
		if err != nil {
			continue
		}
		// A probe, not a crawl: read one byte and close.
		req.Header.Set("User-Agent", "Zut/0.1 (dev-server-probe)")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	return previewURL
}

// openPreview shows the web preview in a tab, focusing it if open,
// reloading its page so a dev server that came up after the first try
// works too.
func (a *app) openPreview() {
	url := detectDevServer()
	for _, col := range a.cols {
		for i, t := range col.tabs {
			if t.preview != nil {
				t.preview.url = url
				t.preview.web.Page().LoadURL(url)
				a.keepTab(col.tabs[i])
				a.activate(col, i, true)
				return
			}
		}
	}
	col := a.fileColumn()
	at := min(col.active+1, len(col.tabs))
	a.addTab(col, at, &colTab{preview: &previewView{url: url}})
	a.activate(col, at, true)
	a.rememberTabs()
}

// previewViewUI draws the tab of the web preview, creating its web view
// in this window on first draw.
func (a *app) previewViewUI(c *ui.Context, v *previewView, name string) {
	p := a.pal
	h, ok := a.host.(*desktop)
	if !ok || h.f == nil || h.f.win == nil {
		ui.Text(c, "Preview is not available on this platform").FontSize(12).TextColor(p.muted).Padding(12, 16)
		return
	}
	if v.web == nil {
		wv, err := h.f.win.NewWebView(mygo.WebViewOptions{URL: v.url})
		if err != nil {
			ui.Text(c, err.Error()).FontSize(12).TextColor(p.muted).Padding(12, 16)
			return
		}
		v.web = wv
	}
	ui.WebView(c.Key("preview-webview"), v.web).Fill()
}
