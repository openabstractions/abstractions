package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

//go:embed static/native-host.mjs
var panelOwnedShellAssets embed.FS

type panelOwnedWindow struct {
	frames       *panelOwnedFrames
	instance     string
	manifest     string
	mu           sync.RWMutex
	visible      func(context.Context) bool
	bootstrapped bool
	// sdk is the exact generated resource closure supplied by the normal owner.
	sdk http.Handler
}

func (p *panelOwnedWindow) bindVisible(check func(context.Context) bool) {
	p.mu.Lock()
	p.visible = check
	p.mu.Unlock()
}

func (p *panelOwnedWindow) routes(key string, next http.Handler) http.Handler {
	return p.frames.callbacks(key, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/native-host/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Selected static modules contain no instance, key or capability result.
		// Browser module imports carry no custom request header.
		if r.Method == http.MethodGet && r.URL.Path == "/native-host/native-host.mjs" {
			body, err := panelOwnedShellAssets.ReadFile("static/native-host.mjs")
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(body)
			return
		}
		if r.Method == http.MethodGet && p.sdk != nil && strings.HasPrefix(r.URL.Path, "/native-host/sdk/") && strings.HasSuffix(r.URL.Path, ".mjs") {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			p.sdk.ServeHTTP(w, r)
			return
		}
		guard(key, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			switch r.URL.Path {
			case "/native-host/bootstrap":
				if p.frames.ctx.Err() != nil {
					w.WriteHeader(http.StatusGone)
					return
				}
				p.mu.Lock()
				claimed := p.bootstrapped
				p.bootstrapped = true
				p.mu.Unlock()
				if claimed {
					p.frames.retire()
					w.WriteHeader(http.StatusGone)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(struct {
					Application string `json:"application"`
					Instance    string `json:"instance"`
					Manifest    string `json:"manifest"`
				}{panelOwnedApplication, p.instance, p.manifest})
			case "/native-host/visible":
				ctx, cancel := context.WithTimeout(r.Context(), time.Second)
				defer cancel()
				p.mu.RLock()
				check := p.visible
				p.mu.RUnlock()
				if p.frames.ctx.Err() != nil || check == nil || !check(ctx) {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			case "/native-host/shell":
				sum := sha256.Sum256([]byte(key))
				nonce := base64.RawStdEncoding.EncodeToString(sum[:])
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' 'nonce-"+nonce+"'; style-src 'nonce-"+nonce+"'; frame-src 'self'; connect-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'none'")
				w.Header().Set("Referrer-Policy", "no-referrer")
				body := `<!doctype html><html><head><meta charset="utf-8"><title>OpenAbstractions Panel</title><style nonce="` + nonce + `">html,body,iframe{margin:0;width:100%;height:100%;border:0;display:block}</style><script type="importmap" nonce="` + nonce + `">{"imports":{"@openabstractions/download-request/internal":"/native-host/sdk/abstraction-download/javascript/js/abstraction/download/request/internal.mjs","@openabstractions/rights/internal":"/native-host/sdk/abstraction-rights/javascript/js/abstraction/rights/api/internal.mjs"}}</script></head><body><iframe id="panel-content" title="OpenAbstractions Panel" sandbox="allow-scripts allow-same-origin allow-forms allow-popups" src="/credentials-page?k=` + url.QueryEscape(key) + `"></iframe><script type="module" src="/native-host/native-host.mjs?k=` + url.QueryEscape(key) + `"></script></body></html>`
				_, _ = w.Write([]byte(body))
			default:
				http.NotFound(w, r)
			}
		}).ServeHTTP(w, r)
	}))
}
