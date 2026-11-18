package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// These routes project the canonical native connection owner. The request
// context owns the wait; closing the page leaves any native Keep with its owner.
func (p *servicePanel) installedAgentConnectionRoutes(mux *http.ServeMux, key string) {
	p.installedAgentConnectionRoutesWith(mux, key, readInstalledAgentConnection, connectInstalledAgentConnection)
}

func (p *servicePanel) installedAgentConnectionRoutesWith(mux *http.ServeMux, key string, read func(context.Context) (installedAgentConnectionView, error), connect func(context.Context, string) (installedAgentConnectionView, error)) {
	connection := installedConnection()
	party := string(connection.party)
	path := "/connections/" + party
	mux.HandleFunc(path, panelHTMLPage(key, party+"-connections", connection.title+" connection", connection.page()))
	mux.HandleFunc(path+"/state", guard(key, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET required", 405)
			return
		}
		view, err := read(r.Context())
		if err != nil {
			view.Outcome, view.Reason = "unavailable", err.Error()
			view.Guidance = installedAgentSentence(err, connection.title, "read")
		}
		noteAgentAttention(party, view.Guidance)
		panelJSON(w, view)
	}))
	mux.HandleFunc(path+"/connect", guard(key, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", 405)
			return
		}
		var edit struct {
			Revision string `json:"revision"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		d.DisallowUnknownFields()
		if d.Decode(&edit) != nil || d.Decode(new(any)) != io.EOF || edit.Revision == "" {
			http.Error(w, "invalid setup request", 400)
			return
		}
		view, err := connect(r.Context(), edit.Revision)
		if err != nil {
			if view.Outcome == "page" || view.Outcome == "" {
				view.Outcome = "unavailable"
			}
			view.Reason = err.Error()
			view.Guidance = installedAgentSentence(err, connection.title, "connect")
			p.connectFailed(party, connection.title, view.Guidance)
		}
		panelJSON(w, view)
	}))
}

func (c installedAgentConnection) page() string {
	return agentConnectionPage(c.title, string(c.party), "This account's "+c.title+" connection.", true)
}

// installedAgentSentence is the page's sentence for a failed read or
// connect of an installed agent; the error stays in Technical details.
func installedAgentSentence(err error, title, step string) string {
	if sentence := agentSetupBlocker(err, title); sentence != "" {
		return sentence
	}
	if step == "connect" {
		return "OpenAbstractions couldn't connect " + title + ". Select Check again."
	}
	return "OpenAbstractions couldn't read the " + title + " connection. Select Check again."
}
