package main

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	wire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	facade "github.com/openabstractions/abstraction-facade/go"
)

func typedPanelJSON(w http.ResponseWriter, value any) {
	data, err := wire.ToJSON(value)
	if err != nil {
		panelError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

// The Panel forwards typed settings through the selected native service. The
// service owns authorization, locks, validation and conditional persistence.
func (p *servicePanel) typedSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "GET or POST required", 405)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	contract := (&wire.ConfigReaderV2Dispatcher{}).ServiceContract()
	action := r.URL.Query().Get("action")
	if action == "observe" {
		contract = (&wire.ConfigObserverV2Dispatcher{}).ServiceContract()
	}
	if action == "user" || r.Method == http.MethodPost {
		contract = (&wire.ConfigEditorV2Dispatcher{}).ServiceContract()
	}
	binding, err := panelMachine().ResolveService(ctx, contract, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	connection := binding.Transport()
	connection.Timeout = settingsObserveWait + 3*time.Second
	transport := connection.WithContext(ctx)
	if r.Method == http.MethodPost {
		var request struct {
			Revision string
			Changes  []json.RawMessage
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "one typed settings replacement required", 400)
			return
		}
		changes := make([]wire.Change, len(request.Changes))
		for i, data := range request.Changes {
			if wire.FromJSON(data, &changes[i]) != nil {
				http.Error(w, "invalid typed change", 400)
				return
			}
		}
		p.logAction(ctx, "settings.replace", map[string]string{"settings.revision": request.Revision})
		result, err := wire.NewConfigEditorV2Client(transport).ReplaceUser(request.Revision, changes)
		if err != nil {
			panelError(w, err)
			return
		}
		typedPanelJSON(w, result)
		return
	}
	switch action {
	case "observe":
		result, err := wire.NewConfigObserverV2Client(transport).Observe(r.URL.Query().Get("cursor"), settingsObserveWait.Milliseconds())
		if err != nil {
			panelError(w, err)
			return
		}
		typedPanelJSON(w, result)
	case "describe":
		result, err := wire.NewConfigReaderV2Client(transport).Describe()
		if err != nil {
			panelError(w, err)
			return
		}
		typedPanelJSON(w, result)
	case "user":
		result, err := wire.NewConfigEditorV2Client(transport).ReadUser()
		if err != nil {
			panelError(w, err)
			return
		}
		typedPanelJSON(w, result)
	case "", "read":
		result, err := wire.NewConfigReaderV2Client(transport).Read(nil)
		if err != nil {
			panelError(w, err)
			return
		}
		typedPanelJSON(w, result)
	default:
		http.Error(w, "invalid settings action", 400)
	}
}
