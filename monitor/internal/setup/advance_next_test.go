package setup

import "testing"

// The one package driver (architect review 25, A2): NextAutomatic chooses
// for both the setup step and the Setup page. Dependencies come first, a
// running transfer waits, a finished one advances, a failed or uncertain
// component waits for the person, and an installed ComfyUI's earlier
// version waits for its updated registration.
func TestNextAutomaticIsTheOneDriver(t *testing.T) {
	plan := Plan{Preparable: true, Components: []PlannedComponent{
		{Component: Component{ID: "lemonade-engine"}},
		{Component: Component{ID: "comfyui-app", Dependencies: []string{"lemonade-engine"}}},
	}}
	status := func(engine, app ComponentStatus) Status {
		engine.ID, app.ID = "lemonade-engine", "comfyui-app"
		return Status{Plan: plan, CurrentCatalog: true, Components: []ComponentStatus{engine, app}}
	}
	pending := ComponentStatus{Effect: Effect{Phase: Pending}}
	installed := ComponentStatus{Effect: Effect{Phase: Installed}}
	cases := []struct {
		name   string
		status Status
		want   string
	}{
		{"pending engine first", status(pending, pending), "lemonade-engine"},
		{"dependent waits", status(ComponentStatus{Effect: Effect{Phase: Requested}, Transfer: &TransferObservation{State: "running"}}, pending), ""},
		{"finished transfer advances", status(ComponentStatus{Effect: Effect{Phase: Requested}, Transfer: &TransferObservation{State: "complete"}}, pending), "lemonade-engine"},
		{"then the app", status(installed, pending), "comfyui-app"},
		{"failed waits for the person", status(installed, ComponentStatus{Effect: Effect{Phase: Failed}}), ""},
		{"uncertain waits for the person", status(installed, ComponentStatus{Effect: Effect{Phase: Uncertain}}), ""},
		{"an engine's earlier version is removed", status(ComponentStatus{Effect: Effect{Phase: Installed, Retired: []Artifact{{Version: "1"}}}}, installed), "lemonade-engine"},
		{"ComfyUI's waits for its registration", status(installed, ComponentStatus{Effect: Effect{Phase: Installed, Retired: []Artifact{{Version: "1"}}}}), ""},
		{"a failed ComfyUI update is removed", status(installed, ComponentStatus{Effect: Effect{Phase: Failed, Retired: []Artifact{{Version: "2"}}}}), "comfyui-app"},
		{"removing is left alone", status(ComponentStatus{Effect: Effect{Phase: Pending, Removing: true}}, installed), ""},
		{"a removed package is never installed again", status(installed, ComponentStatus{Effect: Effect{Phase: Removed}}), ""},
		{"all installed", status(installed, installed), ""},
	}
	for _, c := range cases {
		if got := c.status.NextAutomatic(); got != c.want {
			t.Errorf("%s: next = %q, want %q", c.name, got, c.want)
		}
	}
	stale := status(pending, pending)
	stale.CurrentCatalog = false
	if got := stale.NextAutomatic(); got != "" {
		t.Fatalf("a prior catalog's plan advanced %q", got)
	}
}
