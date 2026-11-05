package remote

import (
	"embed"
	"net/http"
	"strings"
)

// Assets are the browser shell (served by every relay) and the Cloudflare
// Worker that is uploaded together with them.
//
//go:embed assets
var Assets embed.FS

// ShellFiles are served under /__pocket/; anything else gets shell.html.
var ShellFiles = []string{"shell.js", "shell.css", "sw.js", "inject.js", "noise.js"}

// ShellCSP is the shell page's policy: its own scripts only, nothing third-party.
const ShellCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; " +
	"connect-src 'self'; frame-src 'self'; worker-src 'self'; manifest-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Asset returns one embedded file.
func Asset(name string) []byte {
	b, _ := Assets.ReadFile("assets/" + name)
	return b
}

// ServeAsset answers every path that is not part of the relay protocol.
func ServeAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-cache")
	if name, ok := strings.CutPrefix(r.URL.Path, "/__pocket/"); ok {
		for _, f := range ShellFiles {
			if f == name {
				if strings.HasSuffix(f, ".css") {
					h.Set("Content-Type", "text/css; charset=utf-8")
				} else {
					h.Set("Content-Type", "text/javascript; charset=utf-8")
				}
				if f == "sw.js" {
					h.Set("Service-Worker-Allowed", "/")
				}
				_, _ = w.Write(Asset(f))
				return
			}
		}
		http.NotFound(w, r)
		return
	}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", ShellCSP)
	_, _ = w.Write(Asset("shell.html"))
}
