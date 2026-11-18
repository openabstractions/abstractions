//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/fileid"
)

// J-CONNECT (owner, R30, 2026-11-11): the person pressed Connect on the
// Catalog's ComfyUI row for Comfy Desktop's ComfyUI. The connector
// registered itself and its entry waited for Keep; nothing asked, the app
// stayed pending and OA listed no ComfyUI.
//
// R31 ruling: the Connect click is the person's Keep, carried by an
// expectation record; Connect asks no further question. The journey lays a
// Comfy Desktop install out in the fixture home (Comfy Desktop's
// installations.json, ComfyUI\custom_nodes, .venv), presses Connect on the
// Panel's route, and expects the comfyui entry kept with no question. The
// agent then opens ComfyUI and oa_applications_describe lists it as openable.
//
// The integration is already in custom_nodes, as Connect's extension MSI
// places it: running that MSI would act on the person's real ComfyUI
// connection, which shares its upgrade code. Its connector is the suite's
// stand-in (cmd/oa-journey-app) carrying the real connector's manifest
// (adopters/comfyui/connector/application-manifest.json). Connect runs its
// register-self as it runs the real one.
func init() { registerTimed(45, "J-CONNECT", connectBudget, runConnect) }

func connectBudget(*options) time.Duration { return 3 * time.Minute }

func connectFixtureFolders(ctx context.Context, person string, folders ...string) bool {
	root, err := fileid.Open(ctx, fileid.Self, person, fileid.Options{})
	if err != nil {
		return false
	}
	defer root.Close()
	for _, folder := range folders {
		held, err := fileid.Open(ctx, fileid.Self, folder, fileid.Options{})
		if err != nil {
			return false
		}
		relation, err := held.Relate(root)
		isDir := held.FileID().IsDir()
		held.Close()
		if err != nil || !isDir || relation.Kind != fileid.RelationInside {
			return false
		}
	}
	return true
}

func sameConnectFolder(ctx context.Context, shown, expected string) bool {
	fixture, err := fileid.Open(ctx, fileid.Self, expected, fileid.Options{})
	if err != nil {
		return false
	}
	defer fixture.Close()
	displayed, err := fileid.Resolve(ctx, fileid.Self, shown, fileid.Options{})
	return err == nil && displayed.IsDir() && displayed.Equal(fixture.FileID())
}

func runConnect(ctx context.Context, e *env, o *options, r *result) {
	manifest, err := os.ReadFile(o.comfyManifest)
	if !r.check("the ComfyUI connector's manifest", err == nil && len(manifest) > 0, "%s: %v", o.comfyManifest, err) {
		return
	}
	if image, err := connectorStandIn(e.bin, manifest); err == nil {
		sum := sha256.Sum256(image)
		e.logf("J-CONNECT: the fixture's connector is %d bytes, SHA-256 %s", len(image), hex.EncodeToString(sum[:]))
	}
	local, roaming := os.Getenv("LOCALAPPDATA"), os.Getenv("APPDATA")
	person := os.Getenv(testPersonVariable)
	if !r.check("the fixture home holds the account folders", connectFixtureFolders(ctx, person, local, roaming), "LOCALAPPDATA %s APPDATA %s", local, roaming) {
		return
	}
	install := filepath.Join(local, "Comfy-Desktop", "ComfyUI-Installs", "journey")
	base := filepath.Join(install, "ComfyUI")
	integration := filepath.Join(base, "custom_nodes", "comfyui-openabstractions")
	settings := filepath.Join(roaming, "Comfy Desktop")
	defer func() {
		_ = os.RemoveAll(filepath.Join(local, "Comfy-Desktop"))
		_ = os.RemoveAll(settings)
	}()
	program, err := placeConnectorStandIn(e, install, integration, settings, manifest)
	if !r.check("a Comfy Desktop install with the integration in custom_nodes", err == nil, "%s %v", program, err) {
		return
	}
	defer stopComfyStandIn(e, integration, program)

	// The Catalog row shows this ComfyUI, and no other.
	var overview struct {
		PictureApplication struct {
			State, Kind, Folder string
		} `json:"picture_application"`
	}
	_, err = e.panel(ctx, "GET", "/setup/overview", nil, &overview)
	shown := overview.PictureApplication
	if !r.check("the Catalog row shows the fixture's ComfyUI", err == nil && sameConnectFolder(ctx, shown.Folder, base),
		"%+v %v (Connect is pressed only for the fixture)", shown, err) {
		return
	}

	// The person presses Connect.
	mark := e.answers.mark()
	var started struct {
		StepID string `json:"step_id"`
	}
	_, err = e.panel(ctx, "POST", "/setup/comfy/connect", map[string]bool{"confirm": true}, &started)
	if !r.check("Connect starts its step", err == nil && started.StepID != "", "%+v %v", started, err) {
		return
	}
	outcome, err := waitSetupOutcome(ctx, e, "comfyui-connect", started.StepID, 2*time.Minute)
	r.check("Connect's step ends without a failure", err == nil && outcome.Failure == "", "%+v %v", outcome, err)
	r.check("the connector registered itself", strings.Contains(appLog(integration), "start [register-self]"), "%s", clip(appLog(integration), 300))

	state, detail := waitRegistration(ctx, e, comfyOpenApp, "enabled", 20*time.Second)
	r.check("Connect keeps the comfyui entry", state == "enabled", "%s", detail)
	// The answerer reads the Questions page every 250 ms.
	time.Sleep(2 * time.Second)
	r.check("Connect asks no further question", len(e.answers.since(mark)) == 0, "%s", questionKeys(e.answers.since(mark)))

	// An agent opens ComfyUI; OA lists it while it runs.
	a, err := e.enrollAgent(ctx, "oa-opencode-integration", "OpenCode")
	if !r.check("agent app kept", err == nil, "%v", err) {
		return
	}
	s, err := e.connect(ctx, a, []string{"OA_OPENCODE_HOST_CHAT_ID=" + newUUID()})
	if !r.check("agent connected", err == nil, "%v", err) {
		return
	}
	defer s.Close()
	defer func() { r.Calls = s.calls }()
	agentOpen(ctx, e, s, "J-CONNECT", "the agent's open", integration, program, false, func(name string, pass bool, detail string, args ...any) bool {
		return r.check(name, pass, detail, args...)
	})
	res, listed, _, err := s.call(ctx, "oa_applications_describe", map[string]any{})
	if !r.check("Describe answers a page for the kept agent", err == nil && res != nil && !res.IsError, "%s %s", clip(listed, 400), errString(err)) {
		return
	}
	page, err := appmodel.DecodeApplicationListing([]byte(listed))
	if !r.check("Describe returns the application listing schema", err == nil && page.Outcome == appmodel.ReadingOutcomePage, "%s %v", clip(listed, 400), err) {
		return
	}
	visible, openable := false, false
	for _, entry := range page.Applications {
		if entry.Application == comfyOpenApp && entry.Title == "ComfyUI" {
			visible = true
		}
	}
	for _, entry := range page.OpenVerbs {
		if entry.Application != comfyOpenApp {
			continue
		}
		for _, verb := range entry.Verbs {
			if verb.Name == "open" && verb.InputSchema != "" {
				openable = true
			}
		}
	}
	r.check("Describe names ComfyUI and its Open verb", visible && openable, "%s", clip(listed, 600))
}

// placeConnectorStandIn lays out Comfy Desktop's install: its settings
// folder's installations.json naming the install, ComfyUI\custom_nodes with
// the integration folder, and the install's .venv. The integration's
// connector is the stand-in with manifest as its OA_APPLICATION_MANIFEST.
func placeConnectorStandIn(e *env, install, integration, settings string, manifest []byte) (string, error) {
	for _, dir := range []string{integration, filepath.Join(install, ".venv", "Scripts"), settings} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(install, ".venv", "Scripts", "python.exe"), nil, 0o600); err != nil {
		return "", err
	}
	// The node pack's entry module beside the connector, as the MSI places it.
	if err := os.WriteFile(filepath.Join(integration, "__init__.py"), []byte("# J-CONNECT stand-in node pack\n"), 0o600); err != nil {
		return "", err
	}
	records, _ := json.Marshal([]map[string]string{{"id": "journey", "name": "ComfyUI", "sourceId": "standalone", "status": "installed", "installPath": install}})
	if err := os.WriteFile(filepath.Join(settings, "installations.json"), records, 0o600); err != nil {
		return "", err
	}
	image, err := connectorStandIn(e.bin, manifest)
	if err != nil {
		return "", err
	}
	program := filepath.Join(integration, "comfyui-connector.exe")
	if err := os.WriteFile(program, image, 0o700); err != nil {
		return "", err
	}
	cfg, _ := json.Marshal(map[string]any{"endpoint": e.endpoint, "runtime_program": e.runtime, "application": comfyOpenApp, "start_delay_ms": 3000})
	return program, os.WriteFile(filepath.Join(integration, "journey-app.json"), cfg, 0o600)
}

// connectorStandIn is the J-CONNECT connector program: the suite's stand-in
// app (bin\oa-journey-app.exe) carrying manifest as its
// OA_APPLICATION_MANIFEST. Its bytes are deterministic; journeys.py pins
// them as the build's connector (-connector-image), so Connect finds the
// connector it expects, as an installed build finds the one its extension
// MSI carries.
func connectorStandIn(bin string, manifest []byte) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(bin, "oa-journey-app.exe"))
	if err != nil {
		return nil, err
	}
	return withManifestResource(raw, "OA_APPLICATION_MANIFEST", manifest)
}

type stepOutcome struct {
	StepID  string `json:"step_id"`
	Failure string `json:"failure"`
	Detail  string `json:"detail"`
	EndedAt string `json:"ended_at"`
}

// waitSetupOutcome follows the Setup page's state until component's step
// stepID has an outcome.
func waitSetupOutcome(ctx context.Context, e *env, component, stepID string, limit time.Duration) (stepOutcome, error) {
	var last string
	for deadline := time.Now().Add(limit); time.Now().Before(deadline) && ctx.Err() == nil; time.Sleep(500 * time.Millisecond) {
		var state struct {
			Outcomes map[string]stepOutcome `json:"outcomes"`
		}
		raw := map[string]any{}
		if _, err := e.panel(ctx, "GET", "/setup/state", nil, &raw); err != nil {
			last = err.Error()
			continue
		}
		b, _ := json.Marshal(raw)
		_ = json.Unmarshal(b, &state)
		if o, ok := state.Outcomes[component]; ok && o.StepID == stepID {
			return o, nil
		}
		last = clip(string(b), 300)
	}
	return stepOutcome{}, fmt.Errorf("no outcome for %s step %s within %s: %s", component, stepID, limit, last)
}

// waitRegistration reads application's registration until its state is
// want or limit passes; it returns the last state and a description.
func waitRegistration(ctx context.Context, e *env, application, want string, limit time.Duration) (string, string) {
	var state, detail string
	for deadline := time.Now().Add(limit); time.Now().Before(deadline) && ctx.Err() == nil; time.Sleep(500 * time.Millisecond) {
		binding, err := e.machine.ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
		if err != nil {
			detail = err.Error()
			continue
		}
		page, err := applications.NewApplicationsClient(binding.Transport().WithContext(ctx)).Observe(applications.Query{Application: application, Limit: 8})
		if err != nil {
			detail = err.Error()
			continue
		}
		state, detail = "", fmt.Sprintf("%s: no registration", page.Outcome)
		for _, reg := range page.Registrations {
			state = reg.State.String()
			detail = fmt.Sprintf("%s %s (%s)", reg.Key, state, reg.Reason)
		}
		if state == want {
			break
		}
	}
	return state, detail
}
