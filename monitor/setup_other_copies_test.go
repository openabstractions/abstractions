package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// No test reads the machine for other copies; a test that needs one sets
// findOtherCopies itself.
func init() {
	findOtherCopies = func() ([]setup.OtherCopy, error) { return nil, nil }
}

func TestSetupIncompleteCopyCheckRequiresBesideChoiceForEachSelectedApp(t *testing.T) {
	prior := findOtherCopies
	findOtherCopies = func() ([]setup.OtherCopy, error) { return nil, errors.New("uninstall registry unreadable") }
	t.Cleanup(func() { findOtherCopies = prior })
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	handler := (&servicePanel{setupJournal: &journal}).handler("setup-test-key")
	plan := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/setup/plan", strings.NewReader(body))
		req.Host = "127.0.0.1"
		req.Header.Set("X-Panel-Key", "setup-test-key")
		got := httptest.NewRecorder()
		handler.ServeHTTP(got, req)
		return got
	}
	for _, body := range []string{
		`{"selected":["comfyui"]}`,
		`{"selected":["comfyui","lemonade"]}`,
	} {
		if got := plan(body); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "could not finish checking") {
			t.Fatalf("incomplete scan allowed unreviewed app %s: %d %s", body, got.Code, got.Body.String())
		}
	}
	if got := plan(`{"selected":["comfyui","lemonade"],"beside":["comfyui"]}`); got.Code != http.StatusOK {
		t.Fatalf("explicit install-beside was refused: %d %s", got.Code, got.Body.String())
	}
}

func withOtherCopies(t *testing.T, copies []setup.OtherCopy) {
	t.Helper()
	prior := findOtherCopies
	findOtherCopies = func() ([]setup.OtherCopy, error) { return copies, nil }
	t.Cleanup(func() { findOtherCopies = prior })
}

// An unmarked ComfyUI keeps OA's build out of a plan unless the person chose
// to install it beside that copy; the review names the copy's location.
func TestSetupPlanLeavesAnUnmarkedCopyAndNeedsAnExplicitChoice(t *testing.T) {
	location := filepath.Join(t.TempDir(), "ComfyUI_windows_portable")
	withOtherCopies(t, []setup.OtherCopy{{App: setup.ComfyUI, Title: "ComfyUI", Location: location, Source: "folder"}})
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	handler := (&servicePanel{setupJournal: &journal}).handler("setup-test-key")
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Host = "127.0.0.1"
		req.Header.Set("X-Panel-Key", "setup-test-key")
		got := httptest.NewRecorder()
		handler.ServeHTTP(got, req)
		return got
	}
	listed := call("GET", "/setup/other-copies", "")
	var reply setupOtherCopiesReply
	if listed.Code != 200 || json.Unmarshal(listed.Body.Bytes(), &reply) != nil || len(reply.Copies) != 1 || reply.Copies[0].Location != location {
		t.Fatalf("other copies: %d %s", listed.Code, listed.Body.String())
	}
	refused := call("POST", "/setup/plan", `{"selected":["comfyui"]}`)
	if refused.Code != http.StatusConflict || !strings.Contains(refused.Body.String(), location) {
		t.Fatalf("plan with an unmarked ComfyUI and no explicit choice: %d %s", refused.Code, refused.Body.String())
	}
	plan, err := setup.NewCatalog().Plan([]setup.AppID{setup.ComfyUI})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(setupChoiceRequest{Selected: []setup.AppID{setup.ComfyUI}, PlanID: plan.ID, Confirm: true})
	if got := call("POST", "/setup/intent", string(body)); got.Code != http.StatusConflict {
		t.Fatalf("intent without the explicit choice: %d %s", got.Code, got.Body.String())
	}
	chosen := call("POST", "/setup/plan", `{"selected":["comfyui"],"beside":["comfyui"]}`)
	var review setupPlanReview
	if chosen.Code != 200 || json.Unmarshal(chosen.Body.Bytes(), &review) != nil {
		t.Fatalf("plan with the explicit choice: %d %s", chosen.Code, chosen.Body.String())
	}
	if len(review.OtherCopies) != 1 || review.OtherCopies[0].Location != location {
		t.Fatalf("the review does not name the copy it leaves: %+v", review.OtherCopies)
	}
	if other := call("POST", "/setup/plan", `{"selected":["lemonade"]}`); other.Code != 200 {
		t.Fatalf("an app with no other copy is unchanged: %d %s", other.Code, other.Body.String())
	}
}

// With no other copy, the plan and its review are unchanged.
func TestSetupPlanWithNoOtherCopyIsUnchanged(t *testing.T) {
	withOtherCopies(t, nil)
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	handler := (&servicePanel{setupJournal: &journal}).handler("setup-test-key")
	req := httptest.NewRequest("POST", "/setup/plan", strings.NewReader(`{"selected":["comfyui","lemonade"]}`))
	req.Host = "127.0.0.1"
	req.Header.Set("X-Panel-Key", "setup-test-key")
	got := httptest.NewRecorder()
	handler.ServeHTTP(got, req)
	var review setupPlanReview
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &review) != nil || len(review.OtherCopies) != 0 || len(review.Selected) != 2 {
		t.Fatalf("plan with no other copy: %d %s", got.Code, got.Body.String())
	}
	if strings.Contains(got.Body.String(), "other_copies") {
		t.Fatal("a review with no other copy carries an other_copies field")
	}
}
