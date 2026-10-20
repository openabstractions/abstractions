package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/openabstractions/abstraction-identity/go/privatedir/privatetest"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"github.com/openabstractions/abstraction-provider-kit/go/modelobserver"
	"github.com/openabstractions/abstraction-provider-modelbridge/go/bridge"
	router "github.com/openabstractions/abstraction-router/go"
	rservice "github.com/openabstractions/abstraction-router/go/service"
	source "github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	lend "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
)

type externalJourneySource struct{ path string }

func (s externalJourneySource) Stores() []string   { return []string{"fixture"} }
func (s externalJourneySource) Root(string) string { return filepath.Dir(s.path) }
func (s externalJourneySource) Resolve(store, id string, _ bridge.Accepts) (bridge.Object, error) {
	if store != "fixture" || id != "object" {
		return bridge.Object{}, bridge.ErrUnknownObject
	}
	info, err := os.Stat(s.path)
	if err != nil {
		return bridge.Object{}, err
	}
	return bridge.Object{Store: store, ID: id, Locator: filepath.Base(s.path), Path: s.path, Family: "fixture-model", File: filepath.Base(s.path), Size: info.Size(),
		Binding: bridge.ObjectBinding{Kind: bridge.BindFile, Path: s.path, Length: info.Size()}}, nil
}

func TestExternalEngineLentModelNativeJourney(t *testing.T) {
	for _, kind := range []string{"lmstudio", "lemonade"} {
		t.Run(kind, func(t *testing.T) { externalEngineLentModelNativeJourney(t, kind) })
	}
}

func externalEngineLentModelNativeJourney(t *testing.T, kind string) {
	if !identity.LoopbackCeiling().Bindable {
		t.Skip("native engine peer proof unavailable")
	}
	if err := identity.CanEver(router.Bound); err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := privatetest.Dir(t)
	weights := filepath.Join(root, "fixture-model.gguf")
	if err := os.WriteFile(weights, []byte("GGUF inert fixture bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	models := filepath.Join(root, "home", ".lmstudio", "models")
	if err := os.MkdirAll(models, 0700); err != nil {
		t.Fatal(err)
	}
	var available atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/models":
			if available.Load() {
				io.WriteString(w, `{"data":[{"id":"fixture-model","type":"llm","state":"loaded","max_context_length":32768}]}`)
			} else {
				io.WriteString(w, `{"data":[]}`)
			}
		case "/api/v1/models":
			if available.Load() {
				io.WriteString(w, `{"data":[{"id":"fixture-model","downloaded":true,"max_context_window":32768}]}`)
			} else {
				io.WriteString(w, `{"data":[]}`)
			}
		case "/internal/set":
			io.WriteString(w, `{}`)
		case "/v1/chat/completions", "/api/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"fixture answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	me, err := identity.Self()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	program := identity.NormalizeSubjectProgram(executable)
	account, err := me.Account()
	if err != nil {
		t.Fatal(err)
	}
	// The explicit fixture registration names this known native engine image.
	client := modelobserver.NewClient(func(context.Context, string) (string, error) { return program, nil })
	client.Timeout = 2 * time.Second
	providers, err := openFixtureProviders(t, filepath.Join(root, "providers"), false, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	defer providers.Close()
	_, revision, err := providers.read()
	if err != nil {
		t.Fatal(err)
	}
	added := providers.add(revision, newHostFile(iwire.HostEntry{Name: kind, Kind: kind, Base: upstream.URL}, "operator"))
	if added.Outcome != fwire.DeclarationEditOutcomeApplied {
		t.Fatal(added)
	}
	hosts := providers.hostRouterHosts()
	if len(hosts) != 1 || hosts[0].Name != kind {
		t.Fatal("explicit engine registration", hosts)
	}
	routes := router.NewWithHTTPClient(client, hosts...)
	if err := routes.SurveyContext(ctx); err != nil {
		t.Fatal(err)
	}
	request := router.Request{Op: router.OpObserveOffer, Placement: router.PlacementLocal, Reserve: 4096}
	before, err := routes.ObserveOffer(ctx, request, "", 0)
	if err != nil || len(before.Offers) != 0 {
		t.Fatal(before, err)
	}
	endpoint := filepath.Join(root, "router.sock")
	if runtime.GOOS == "windows" {
		endpoint = listen.Endpoint(fmt.Sprintf("external-engine-source-%d-%d", os.Getpid(), endpointSeq.Add(1)))
	}
	service, err := rservice.Listen(endpoint, routes)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.EnablePolicy(func(context.Context, *identity.Peer, string, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- service.Serve(ctx) }()
	defer func() { service.Close(); <-done }()
	script := privateInput(t, "..", "adopters", "opencode", "test", "fixtures", "external-engine-inventory.mjs")
	routerClient := filepath.Join("abstraction-router", "javascript", "js", "abstraction", "router", "index.mjs")
	module := filepath.Join("..", "openabstractions-flat", routerClient)
	if _, err := os.Stat(module); os.IsNotExist(err) {
		module = filepath.Join("..", "..", routerClient)
	} else if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(module); err != nil {
		t.Fatal(err)
	}
	node := exec.CommandContext(ctx, "node", script, listen.Endpoint(endpoint), kind, module)
	stdout, err := node.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	node.Stderr = &diagnostics
	if err := node.Start(); err != nil {
		t.Fatal(err)
	}
	stopNode := func() {
		if node.ProcessState == nil {
			_ = node.Process.Kill()
			_ = node.Wait()
		}
	}
	defer stopNode()
	output := bufio.NewReader(stdout)
	if line, err := output.ReadString('\n'); err != nil || line != "inventory-watching\n" {
		stopNode()
		t.Fatal(line, err, diagnostics.String())
	}
	ledger, err := bridge.OpenLedger(filepath.Join(root, "state", "lends.json"))
	if err != nil {
		t.Fatal(err)
	}
	var engine bridge.Engine = bridge.LMStudio{Env: source.Env{Home: filepath.Join(root, "home"), Getenv: func(string) string { return "" }}}
	if kind == "lemonade" {
		engine = bridge.Lemonade{Directory: filepath.Join(root, "lent", "lemonade"), Base: upstream.URL, Client: client}
	}
	lending := bridge.New(externalJourneySource{weights}, ledger, bridge.OSLinker(), engine)
	lent := lending.Lend("fixture", "object", kind, false)
	if lent.Outcome != lend.LendOutcomeLent || lent.Lend == nil || lent.Lend.Kind == lend.LinkCopy {
		t.Fatal(lent)
	}
	if same, err := identity.SameFile(weights, lent.Lend.Link); err != nil || !same {
		t.Fatal("lend copied or replaced the object", same, err)
	}
	available.Store(true)
	if err := routes.SurveyContext(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := routes.ObserveOffer(ctx, request, before.Cursor, 0)
	if err != nil || after.Cursor == before.Cursor || len(after.Offers) != 1 || after.Offers[0].Servers[0] != kind {
		t.Fatal(after, err)
	}
	if line, err := output.ReadString('\n'); err != nil || line != "inventory-sees-"+kind+"\n" {
		stopNode()
		t.Fatal(line, err, diagnostics.String())
	}
	if err := node.Wait(); err != nil {
		t.Fatal(err, diagnostics.String())
	}
	records := make(chan inference.Record, 1)
	provider, err := inference.New(inference.Config{Router: routes, HTTP: client, Record: func(r inference.Record) { records <- r },
		Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "permitted", nil },
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "applied"
		},
		ResolveSelectionV2: func(context.Context, inference.Subject, string) (iwire.SelectionPolicyV2, bool, error) {
			return iwire.SelectionPolicyV2{Name: "external", Candidates: []iwire.SelectionCandidateV2{{Model: "fixture-model", Placement: iwire.CandidatePlacementLocal}}}, true, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	subject := inference.Subject{Account: account, Program: program}
	admitted := provider.StartV2(ctx, subject, iwire.RequestV2{Target: iwire.Target{SelectionPolicy: "external"}, Content: iwire.RequestContent{Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "inert request"}}}}, Guarantees: []iwire.RequestGuarantee{}}})
	if admitted.Outcome != iwire.StartOutcomeAccepted {
		t.Fatal(admitted)
	}
	cursor := int64(0)
	for {
		page := provider.Observe(ctx, subject, admitted.Operation, cursor, 64, 65536, 1000)
		if page.Outcome != iwire.PageOutcomePage {
			t.Fatal(page)
		}
		cursor = page.Next
		if page.AtEnd {
			break
		}
	}
	select {
	case record := <-records:
		if record.Outcome != "completed" || record.Host != kind || record.Model != "fixture-model" || record.Preferred != "fixture-model" || record.PreferredHost != kind || record.Selection != "preferred" {
			raw, _ := json.Marshal(record)
			t.Fatal(string(raw))
		}
		t.Logf("host=%s preferred_host=%s actual_model=%s preferred_model=%s selection=%s; linked in place; native OpenCode watch observed the offer", record.Host, record.PreferredHost, record.Model, record.Preferred, record.Selection)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
