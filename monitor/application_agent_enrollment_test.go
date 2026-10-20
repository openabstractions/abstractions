package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
)

type agent1478PanelFake struct {
	applicationDirectoryFake
	read      applications.AgentEnrollmentResult
	sets      []applications.AgentEnrollmentRequest
	setResult *applications.AgentEnrollmentResult
}

func (f *agent1478PanelFake) ReadAgentEnrollment(context.Context, string) (applications.AgentEnrollmentResult, error) {
	return f.read, nil
}
func (f *agent1478PanelFake) SetAgentEnrollment(_ context.Context, r applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error) {
	f.sets = append(f.sets, r)
	if f.setResult != nil {
		return *f.setResult, nil
	}
	v := *f.read.Enrollment
	v.State = applications.AgentEnrollmentStatePending
	v.Revision = "pending-revision"
	return applications.AgentEnrollmentResult{Outcome: applications.OutcomeApplied, Enrollment: &v}, nil
}

func TestApplicationAgentPanelControlIsConditionalAndDoesNotAnswer(t *testing.T) {
	f := &agent1478PanelFake{read: applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &applications.AgentEnrollment{Application: "fixture", Program: `C:\Fixture\assistant.exe`, Revision: "seen-revision", State: applications.AgentEnrollmentStateNone}}}
	p := &servicePanel{comfyApps: f}
	request := func(method, body, key string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/app/fixture/agent", strings.NewReader(body))
		r.Header.Set("X-Panel-Key", key)
		w := httptest.NewRecorder()
		p.appAgentEnrollment("window-key", "fixture")(w, r)
		return w
	}
	if w := request(http.MethodGet, "", "bad-key"); w.Code != http.StatusForbidden {
		t.Fatal("unguarded read", w.Code)
	}
	w := request(http.MethodGet, "", "window-key")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Each access permission is reviewed separately") || !strings.Contains(w.Body.String(), "seen-revision") {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(f.sets) != 0 {
		t.Fatal("read changed enrollment")
	}
	w = request(http.MethodPost, `revision=seen-revision&enabled=true&agent=true`, "window-key")
	if w.Code != http.StatusBadRequest || len(f.sets) != 0 {
		t.Fatal("extra class field accepted", w.Code, f.sets)
	}
	w = request(http.MethodPost, `revision=seen-revision&enabled=true`, "window-key")
	if w.Code != http.StatusOK || len(f.sets) != 1 || f.sets[0].ExpectedRevision != "seen-revision" || !f.sets[0].Enabled || !strings.Contains(w.Body.String(), "Waiting for your answer") {
		t.Fatal(w.Code, f.sets, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "You kept this exact program") {
		t.Fatal("request was displayed as approval")
	}
	f.setResult = &applications.AgentEnrollmentResult{Outcome: applications.OutcomeConflict, Reason: "enrollment:existing-owner"}
	w = request(http.MethodPost, `revision=seen-revision&enabled=true`, "window-key")
	// The shell's runtime banner (runtimeScript) may reload a page once the
	// runtime is back; the conflict response itself never reloads.
	own := strings.Replace(w.Body.String(), runtimeScript, "", 1)
	if w.Code != http.StatusOK || !strings.Contains(own, "/app/existing-owner/agent?k=window-key") || strings.Contains(own, "location.reload") || strings.Contains(own, "fetch(location") {
		t.Fatal("normal conflict response discarded owner navigation", w.Code, w.Body.String())
	}
}

func TestApplicationAgentStartUsesExistingGuardedNativeEnrollmentPage(t *testing.T) {
	run, err := newPanelRun("agent:opencode")
	if err != nil {
		t.Fatal(err)
	}
	f := &agent1478PanelFake{read: applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &applications.AgentEnrollment{Application: "opencode", Program: `C:\Fixture\cli\opencode.exe`, Revision: "seen", State: applications.AgentEnrollmentStateNone}}}
	run.panel.comfyApps = f
	if run.start.path != "/app/opencode/agent" {
		t.Fatal(run.start.path)
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+run.start.path+"?k="+run.key, nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	run.panel.handler(run.key).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Ask to keep as assistant") || len(f.sets) != 0 {
		t.Fatal(response.Code, response.Body.String(), f.sets)
	}
	for _, bad := range []string{"agent:", "agent:../opencode", "agent:opencode/agent", "agent:opencode?enabled=true"} {
		if _, err := parsePanelStartPage(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
