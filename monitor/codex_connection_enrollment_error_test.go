package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/fileid"
)

func TestCodexEnrollmentQuestionUnavailableRetainsNativeFactsWithoutExecution(t *testing.T) {
	program := filepath.Join(t.TempDir(), "inert-agent.exe")
	if err := os.WriteFile(program, []byte("inert"), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := fileid.Resolve(context.Background(), fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	image := &claudePanelFixtureImage{program: program, key: id.Key()}
	failed := applications.AgentEnrollmentResult{Outcome: applications.OutcomeUnavailable, Reason: "question"}
	native := &agent1478PanelFake{read: applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &applications.AgentEnrollment{Application: "oa-codex-integration", Program: program, Revision: "pending-revision", State: applications.AgentEnrollmentStatePending}}, setResult: &failed}
	panel := &servicePanel{comfyApps: native}
	controller := codexPanelFixture(t)
	controller.prepare = func(context.Context) (claudePanelImage, error) { return image, nil }
	controller.enroll = panel.enrollCodexImage
	controller.command = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("helper/vendor executed after unavailable permission question")
		return nil, nil
	}
	panel.codexConnections = controller
	mux := http.NewServeMux()
	panel.codexConnectionRoutes(mux, "fixture")
	request := httptest.NewRequest(http.MethodPost, "/connections/codex/add", strings.NewReader(`{"revision":"1"}`))
	request.Header.Set("X-Panel-Key", "fixture")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	var view codexConnectionView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(response.Code, response.Body.String(), err)
	}
	if response.Code != 200 || view.Outcome != "permission_error" || view.PermissionOutcome != "unavailable" || view.PermissionReason != "question" || view.Available || view.Owned || view.Registration != "" {
		t.Fatal(response.Code, view)
	}
	if len(native.sets) != 1 || native.sets[0].ExpectedRevision != "pending-revision" || !native.sets[0].Enabled {
		t.Fatal(native.sets)
	}
	controller.close()
}

func TestCodexEnrollmentUnknownNativeReasonIsNotCopied(t *testing.T) {
	err := codexEnrollmentFailureFor(applications.AgentEnrollmentResult{Outcome: applications.OutcomeUnavailable, Reason: "private/path SECRET"}).(*codexEnrollmentFailure)
	if err.reason != "unknown" || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "SECRET") {
		t.Fatal(err)
	}
}
