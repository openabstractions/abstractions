package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	mwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/machines"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-facade/go/probe"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
)

type machinesScreenshotList struct {
	mwire.MachineControl
	empty *atomic.Bool
}

func TestMachinesChoicesPrivateInterfaces(t *testing.T) {
	for _, text := range []string{"192.168.10.20", "10.10.2.3", "172.16.1.2", "fd00::1"} {
		if !pairingPrivateAddress(netip.MustParseAddr(text)) {
			t.Fatal("private interface excluded", text)
		}
	}
	for _, text := range []string{"127.0.0.1", "::1", "0.0.0.0", "::", "169.254.1.2", "fe80::1", "8.8.8.8", "2001:4860:4860::8888"} {
		if pairingPrivateAddress(netip.MustParseAddr(text)) {
			t.Fatal("nonprivate bind proposal", text)
		}
	}
}

func (s machinesScreenshotList) List(string, int64) (mwire.MachinePage, error) {
	page := mwire.MachinePage{Outcome: mwire.MachineListOutcomePage, Epoch: strings.Repeat("c", 32), RequestExpiresMs: 300000, Revision: "fixture", LocalServiceAddress: "192.168.10.20:54321", Machines: []mwire.MachineRecord{
		{PeerKey: strings.Repeat("a", 64), BindingName: "office-computer", State: "active", ServiceAddress: "192.0.2.10:8735", LastCallUnixMs: 1767268800000},
		{PeerKey: strings.Repeat("b", 64), BindingName: "studio-computer", State: "active", ServiceAddress: "192.0.2.11:8735"},
	}}
	if s.empty != nil && s.empty.Load() {
		page.Machines = []mwire.MachineRecord{}
		page.LocalServiceAddress = ""
	}
	return page, nil
}
func (machinesScreenshotList) StartOffer(mwire.MachineRequestIdentity, string, mwire.MachinePreparation) (mwire.MachineStart, error) {
	return mwire.MachineStart{Outcome: mwire.MachineStartOutcomeStarted, Code: "fixture-invitation-code", Operation: &mwire.MachineOperation{ID: "owned-visual-operation", Phase: mwire.MachinePhaseOffering, ExpiresMs: 300000}}, nil
}
func (machinesScreenshotList) Observe(string, string, int64) (mwire.MachineObservation, error) {
	return mwire.MachineObservation{Outcome: mwire.MachineObserveOutcomeChanged, Cursor: "fixture", Operation: &mwire.MachineOperation{ID: "owned-visual-operation", Phase: mwire.MachinePhaseOffering, ExpiresMs: 300000}}, nil
}

// Opt-in visual receiving uses the real guarded page and generated public IPC.
// The fixture owns every endpoint and returns inert setup replies; no pairing
// runtime, native question, peer state or permission effect is created.
func TestMachinesPageScreenshot(t *testing.T) {
	dir := os.Getenv("OA_MACHINES_SCREENSHOT")
	if dir == "" {
		t.Skip("OA_MACHINES_SCREENSHOT selects an owned screenshot output folder")
	}
	own(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	endpoint := listen.Endpoint(fixtureName("machines-visual"))
	l, err := listen.Listen(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var empty atomic.Bool
	fixtureControl := machinesScreenshotList{empty: &empty}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 1<<20)
				if call != nil {
					defer call.Close()
				}
				if err != nil {
					return
				}
				reply, err := fwire.ServeEndpoint(call.Frame, "owned-visual-fixture", "", &mwire.MachineControlDispatcher{Handler: fixtureControl}, &fwire.RegistryDispatcher{Handler: &fixedRegistry{}})
				if err == nil {
					_ = call.Reply(reply)
				}
			}()
		}
	}()
	t.Cleanup(func() { cancel(); l.Close(); <-done })
	definitions, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := rights.LoadDecisionPolicyWithDefinitions(filepath.Join(t.TempDir(), "private-fixture-policy.json"), []string{"abstraction.inference/complete"}, definitions)
	if err != nil {
		t.Fatal(err)
	}
	if err = policy.Set(rwire.Subject{Account: probe.Self().Account, Program: "remote:" + strings.Repeat("a", 64)}, "abstraction.inference/complete", "host:ollama-engine", true); err != nil {
		t.Fatal(err)
	}
	rightsAt := listen.Endpoint(fixtureName("machines-visual-rights"))
	rightsHost, err := host.Listen(host.Options{Endpoint: listen.Endpoint(fixtureName("machines-visual-policy-runtime")), LogEndpoint: listen.Endpoint(fixtureName("machines-visual-log")), ConfigEndpoint: listen.Endpoint(fixtureName("machines-visual-config")), RightsEndpoint: rightsAt, RightsPolicy: policy, RightsOperator: func(ctx context.Context, peer *identity.Peer) error {
		process, e := peer.Process.AtLeast(listen.Program.Process)
		if e != nil || process.PID != os.Getpid() {
			return rightsservice.ErrOperatorForbidden
		}
		return ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	rightsDone := make(chan error, 1)
	go func() { rightsDone <- rightsHost.Serve(ctx) }()
	t.Cleanup(func() { cancel(); rightsHost.Close(); <-rightsDone })
	candidates := []resolution.Candidate{}
	for _, contract := range []string{"abstraction.facade/machines@1", "abstraction.facade/registry@1"} {
		candidates = append(candidates, resolution.Candidate{Ready: true, Reference: fwire.ServiceReference{Provider: "owned-visual-fixture", Capability: "abstraction.facade", Contract: contract, Scope: fwire.ScopeLocal, Transport: resolution.LocalTransport, Endpoint: endpoint}})
	}
	for _, contract := range []string{"abstraction.rights/operator@1", "abstraction.rights/authorization@2", "abstraction.rights/operator@2"} {
		candidates = append(candidates, resolution.Candidate{Ready: true, Reference: fwire.ServiceReference{Provider: "owned-visual-policy", Capability: "abstraction.rights", Contract: contract, Scope: fwire.ScopeLocal, Transport: resolution.LocalTransport, Endpoint: rightsAt}})
	}
	catalog, err := resolution.New(candidates)
	if err != nil {
		t.Fatal(err)
	}
	resolverEndpoint := listen.Endpoint(fixtureName("machines-visual-resolver"))
	r, err := resolution.Listen(resolverEndpoint, catalog, func(*identity.Peer, fwire.ServiceReference) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	rdone := make(chan error, 1)
	go func() { rdone <- r.Serve(ctx) }()
	t.Cleanup(func() { cancel(); r.Close(); <-rdone })
	trustPanelRuntime(t, resolverEndpoint)
	if choices := pairingChoices(ctx); len(choices.Hosts) != 1 || choices.Hosts[0] != "ollama-engine" {
		t.Fatalf("actual registered local choices: %+v", choices)
	}
	handler := (&servicePanel{}).handler("owned-visual-key")
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/machines" {
			empty.Store(r.URL.Query().Get("review") == "empty")
		}
		handler.ServeHTTP(w, r)
	}))
	defer panel.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, panel.URL+"/machines/control", strings.NewReader(`{"action":"details","peer":"`+strings.Repeat("a", 64)+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Panel-Key", "owned-visual-key")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var details struct{ Permissions machinePermissions }
	decodeErr := json.NewDecoder(response.Body).Decode(&details)
	response.Body.Close()
	if decodeErr != nil || response.StatusCode != 200 || details.Permissions.Outcome != "page" || len(details.Permissions.Rules) != 1 || details.Permissions.Rules[0].Resource != "host:ollama-engine" {
		t.Fatalf("actual private current-policy Details: %+v %v", details, decodeErr)
	}
	python, err := exec.LookPath("py")
	if err != nil {
		t.Fatal(err)
	}
	pages := []string{"machines-empty", "machines", "machines-add", "machines-progress", "machines-details"}
	if os.Getenv("OA_MACHINES_SCREENSHOT_DETAILS_ONLY") == "1" {
		pages = []string{"machines-details"}
	} else if os.Getenv("OA_MACHINES_SCREENSHOT_RECEIVER_ONLY") == "1" {
		pages = []string{"machines-empty", "machines"}
	}
	args := []string{"-3.12", "-X", "utf8", privateInput(t, "..", "scripts", "panel_screenshots.py"), "--run", "--panel-url", panel.URL + "/?k=owned-visual-key", "--pages"}
	args = append(args, pages...)
	args = append(args, "--output-dir", dir, "--scheme", "both", "--platforms", "windows", "--size", "1100x760")
	cmd := exec.CommandContext(ctx, python, args...)
	{
		fixture := filepath.Join(t.TempDir(), "machines-review.js")
		if err := os.WriteFile(fixture, []byte(`(()=>{const review=new URL(location.href).searchParams.get('review');if(!['add','progress','details'].includes(review)){document.getElementById('pair-list-technical').open=true;return;}const ready=window.oaReady;window.oaReady=()=>{};document.documentElement.dataset.ready='fixture';let opened=false,detailReads=0;const fetchPage=window.fetch;if(review==='details')window.fetch=async(...args)=>{const response=await fetchPage(...args);if(args[1]&&args[1].body&&JSON.parse(args[1].body).action==='details'&&response.ok)detailReads++;return response};const timer=setInterval(()=>{const el=id=>document.getElementById(id);if(el('pair-host').options.length<2||el('pair-machines').children.length<1)return;if(!opened){opened=true;if(review==='details'){el('pair-machines').querySelector('button').click()}else{el('pair-add').click();el('pair-create').click();el('pair-purpose').value='both';el('pair-purpose').dispatchEvent(new Event('change'));el('pair-host').selectedIndex=1;if(!el('pair-network').value){el('pair-network').selectedIndex=1;el('pair-network').dispatchEvent(new Event('change'))}if(review==='progress')el('pair-start').click()}}if(review==='progress'&&!el('pair-invitation').textContent)return;if(review==='details'&&(detailReads<2||el('pair-permissions').children.length<1))return;clearInterval(timer);window.oaReady=ready;ready();},20)})()`), 0600); err != nil {
			t.Fatal(err)
		}
		cmd.Args = append(cmd.Args, "--page-script", fixture)
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("owned page screenshot: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
}

func TestMachinesPageLastCallVisibility(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable for the inert page receiver")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, node, "machines_page_test.js").CombinedOutput(); err != nil {
		t.Fatalf("machines page receiver: %v\n%s", err, output)
	}
}

func TestMachinePairingNativePresentationAndPassivePage(t *testing.T) {
	record := askclient.RecordMetadata{ID: "owned-question", Key: asks.PairingKey, Text: "PC key fixture; check 123456; gives no permissions; receives host owned", Options: []string{"once", "refuse"}}
	view := trayQuestionView(record, trayPublisher{})
	if view.Content != record.Text || view.Title != "OpenAbstractions" || view.Instruction != "Pair these computers?" || view.Default != "refuse" || view.Footer != "" || !strings.Contains(view.Details, "uncertain") {
		t.Fatalf("native frozen agreement projection: %+v", view)
	}
	for _, choice := range view.Choices {
		if choice.Option == "refuse" && choice.Label != "Cancel" {
			t.Fatal("negative decision lost Cancel label")
		}
		if choice.Option == "once" && choice.Label != "Pair with these permissions" {
			t.Fatal("positive decision lost approved label")
		}
	}
	if !strings.Contains(view.Details, "Closing this question leaves it unanswered") || view.Dismiss != "" {
		t.Fatal("close semantics changed")
	}
	other := record
	other.Key = "unrelated"
	if got := trayQuestionView(other, trayPublisher{}); got.Instruction == view.Instruction {
		t.Fatal("text alone selected pairing presenter")
	}
	mux := http.NewServeMux()
	(&servicePanel{}).machinesRoutes(mux, "owned-panel-key")
	for _, path := range []string{"/machines", "/machines/control"} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusForbidden {
			t.Fatalf("unguarded pairing route %s: %d", path, response.Code)
		}
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/machines?k=owned-panel-key", nil))
	if response.Code != 200 {
		t.Fatal(response.Code)
	}
	page := response.Body.String()
	for _, required := range []string{"Add computer", "pair-purpose", "Connect without model access", "Use models on the other computer", "Let the other computer use my models", "Use models on both computers", "Setup progress", "Access to this computer"} {
		if !strings.Contains(page, required) {
			t.Fatalf("approved pairing page missing %q", required)
		}
	}
	main := strings.Split(strings.Split(page, "<section id=\"pair-connections\" data-runtime-echo>")[1], "</section>")[0]
	if strings.Contains(main, "<form") || strings.Contains(main, "Pair these computers?") || strings.Contains(main, "This computer allows") {
		t.Fatal("settings became a permission question")
	}
	if strings.Contains(page, "localStorage") || strings.Contains(page, "sessionStorage") {
		t.Fatal("invitation persisted in browser storage")
	}
}
