package main

import (
	"context"
	"net/http"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// An official host may open only after the person's confirmation. Finishing
// the desktop setup is available only when no primary host was found.
func catalogPackageOpenRoute(path string, target packageConnectTarget, primary, pending func() (setup.ExtensionHost, bool), open func(setup.ExtensionHost) error) setupExtensionRoute {
	return setupExtensionRoute{path: path, handler: func(_ *servicePanel, w http.ResponseWriter, r *http.Request) {
		if !decodeSetupConfirm(w, r) {
			return
		}
		host, found := primary()
		opens := found && host.Connected
		if !found {
			host, opens = pending()
		}
		if !opens {
			http.Error(w, target.title+" isn't connected yet.", http.StatusConflict)
			return
		}
		if err := open(host); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		panelJSON(w, map[string]bool{"opened": true})
	}}
}

// The native picker supplies the path; the package's own Setup operation
// validates and keeps it. A request body never supplies a folder.
func catalogPackageFolderRoute(path string, save func(context.Context, string) error) setupExtensionRoute {
	return setupExtensionRoute{path: path, handler: func(p *servicePanel, w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		p.mu.Lock()
		pick := p.directoryPicker
		p.mu.Unlock()
		if pick == nil {
			panelJSON(w, map[string]any{"available": false})
			return
		}
		folder, err := pick(r.Context())
		if err != nil {
			panelError(w, err)
			return
		}
		if folder == "" || r.Context().Err() != nil {
			panelJSON(w, map[string]any{"available": true, "chosen": false})
			return
		}
		if err := save(r.Context(), folder); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		panelJSON(w, map[string]any{"available": true, "chosen": true})
	}}
}
