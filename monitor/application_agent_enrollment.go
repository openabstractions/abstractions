package main

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"

	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type panelAgentEnrollment interface {
	ReadAgentEnrollment(context.Context, string) (applications.AgentEnrollmentResult, error)
	SetAgentEnrollment(context.Context, applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error)
}
type nativePanelAgentEnrollment struct{ transport listen.FrameClient }

func (n nativePanelAgentEnrollment) ReadAgentEnrollment(ctx context.Context, name string) (applications.AgentEnrollmentResult, error) {
	return applications.NewApplicationsClient(n.transport.WithContext(ctx)).ReadAgentEnrollment(name)
}
func (n nativePanelAgentEnrollment) SetAgentEnrollment(ctx context.Context, r applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error) {
	return applications.NewApplicationsClient(n.transport.WithContext(ctx)).SetAgentEnrollment(r)
}
func (p *servicePanel) agentEnrollmentForPanel(ctx context.Context) (panelAgentEnrollment, error) {
	// Existing scoped fixture dependency; production uses actual @3 resolution.
	if apps, err := p.setupApplicationsForPanel(ctx); err == nil {
		if f, ok := apps.(panelAgentEnrollment); ok {
			return f, nil
		}
	}
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, err
	}
	return nativePanelAgentEnrollment{transport: binding.Transport()}, nil
}

// appIsAgent is whether a registered name is an agent app: one of
// OpenAbstractions' agent connectors, with or without its digest part, or
// an agent registered under its own name, as the installed OpenCode is.
func appIsAgent(name string) bool {
	for _, base := range nativecli.AgentApplicationBases() {
		if name == base || strings.HasPrefix(name, base+"-") {
			return true
		}
	}
	for _, party := range []setup.AgentParty{setup.AgentClaude, setup.AgentCodex, setup.AgentOpenCode} {
		if name == string(party) {
			return true
		}
	}
	return false
}

func appAgentEnrollmentLink(key, name string) string {
	href := "/app/" + url.PathEscape(name) + "/agent?k=" + url.QueryEscape(key)
	return oaGroup("app-agent-identity", "Assistant identity", "Each access permission is reviewed separately.", oaRowNav("Review assistant identity", "Keeping an app as an assistant grants no access or answering permission.", href))
}

// Proposed P-J15 copy remains reserved for owner C-59 acceptance. This source
// control never accepts the native question itself or writes a permission.
func agentEnrollmentPageBody(name, key string, r applications.AgentEnrollmentResult) string {
	e := html.EscapeString
	if r.Enrollment == nil || r.Outcome != applications.OutcomePage && r.Outcome != applications.OutcomeApplied {
		if existing, ok := strings.CutPrefix(r.Reason, "enrollment:"); ok && validApplicationName(existing) {
			return `<p>This program already has an assistant enrollment. <a href="/app/` + url.PathEscape(existing) + `/agent?k=` + url.QueryEscape(key) + `">Review its existing enrollment</a>.</p>`
		}
		return `<p>Assistant identity is unavailable: ` + e(r.Outcome.String()) + `.</p>`
	}
	v := r.Enrollment
	state := "This app has no kept assistant identity."
	if v.State == applications.AgentEnrollmentStatePending {
		state = "Waiting for your answer in the OpenAbstractions question."
	}
	if v.State == applications.AgentEnrollmentStateEnrolled {
		state = "You kept this exact program as an assistant."
	}
	button := "Ask to keep as assistant…"
	enabled := true
	if v.State != applications.AgentEnrollmentStateNone {
		button = "Revoke assistant identity"
		enabled = false
	}
	clear := ""
	if v.State == applications.AgentEnrollmentStateNone && v.Revision != "" {
		clear = `<form method="post"><input type="hidden" name="revision" value="` + e(v.Revision) + `"><input type="hidden" name="enabled" value="false"><button type="submit">Revoke the previous assistant enrollment</button></form>`
	}
	return `<p>` + e(state) + `</p><p>Each access permission is reviewed separately. This choice grants no command, messaging or answering permission.</p><details><summary>Details</summary><p>Program: <code>` + e(v.Program) + `</code></p></details>` +
		`<form method="post"><input type="hidden" name="revision" value="` + e(v.Revision) + `"><input type="hidden" name="enabled" value="` + fmt.Sprint(enabled) + `"><button type="submit">` + e(button) + `</button></form>` + clear
}

func (p *servicePanel) appAgentEnrollment(key, name string) http.HandlerFunc {
	return guard(key, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := panelCall(r)
		defer cancel()
		client, err := p.agentEnrollmentForPanel(ctx)
		if err != nil {
			http.Error(w, "Assistant identity requires the current local runtime: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		var result applications.AgentEnrollmentResult
		if r.Method == http.MethodPost {
			// Ordinary guarded form navigation preserves the semantic response,
			// including a conflict's link to the existing enrollment owner.
			raw, readErr := io.ReadAll(io.LimitReader(r.Body, 4097))
			form, parseErr := url.ParseQuery(string(raw))
			if readErr != nil || parseErr != nil || len(raw) > 4096 || len(form) != 2 || len(form["revision"]) != 1 || len(form["enabled"]) != 1 || len(form.Get("revision")) > 256 || form.Get("enabled") != "true" && form.Get("enabled") != "false" {
				http.Error(w, "Invalid assistant identity change", http.StatusBadRequest)
				return
			}
			result, err = client.SetAgentEnrollment(ctx, applications.AgentEnrollmentRequest{Application: name, ExpectedRevision: form.Get("revision"), Enabled: form.Get("enabled") == "true"})
		} else {
			result, err = client.ReadAgentEnrollment(ctx, name)
		}
		if err != nil {
			http.Error(w, "Assistant identity could not be read or changed: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		body := agentEnrollmentPageBody(name, key, result)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, panelPage(r, key, "application", "Assistant identity", body))
	})
}
