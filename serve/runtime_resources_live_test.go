package main

import (
	"bytes"
	"context"
	"encoding/json"

	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	rwire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

// liveResourcesModel is the model this test loads into the LM Studio running
// here. It is the smallest one that build will actually load: the measurement
// in research/resources/MEASUREMENT-2026-09-22.md found that the two smaller
// GGUFs on this machine both refuse with model_load_failed, and the embedding
// model loads but holds less than the instrument's 256 MiB row threshold, so
// neither proves a holder row. OA_LIVE_RESOURCES_MODEL names another.
const liveResourcesModel = "gemma-4-26b-a4b-it-ultra-uncensored-heretic"

// lmStudioModel is one entry of GET /api/v0/models.
type lmStudioModel struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	State string `json:"state"`
}

func lmStudioCall(t *testing.T, method, path string, body any, into any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	request, err := http.NewRequest(method, liveLMStudio+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 6 * time.Minute}).Do(request)
	if err != nil {
		t.Fatalf("LM Studio %s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(response.Body)
	if response.StatusCode/100 != 2 {
		t.Fatalf("LM Studio %s %s: %s %s", method, path, response.Status, buf.String())
	}
	if into != nil {
		if err := json.Unmarshal(buf.Bytes(), into); err != nil {
			t.Fatalf("LM Studio %s %s: %v: %s", method, path, err, buf.String())
		}
	}
}

func lmStudioModels(t *testing.T) []lmStudioModel {
	t.Helper()
	var listing struct {
		Data []lmStudioModel `json:"data"`
	}
	lmStudioCall(t, http.MethodGet, "/api/v0/models", nil, &listing)
	return listing.Data
}

// The runtime's resource table sees what LM Studio holds on this machine.
// Live: it loads one model through LM Studio's own REST API, reads
// abstraction.resource/table@1 from an isolated runtime, asserts a verified
// holder row exists, and puts LM Studio back the way it was found.
func TestLiveResourceTableSeesTheHolderOfTheCard(t *testing.T) {
	if os.Getenv("OA_LIVE_RESOURCES") != "1" {
		t.Skip("set OA_LIVE_RESOURCES=1 to load a model into this machine's LM Studio and read the card")
	}
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}

	// What LM Studio holds before this test is what it holds after it.
	loadedBefore := map[string]bool{}
	for _, m := range lmStudioModels(t) {
		if m.State == "loaded" {
			loadedBefore[m.ID] = true
		}
	}
	t.Logf("LM Studio was holding %d model(s) before this test", len(loadedBefore))

	model := os.Getenv("OA_LIVE_RESOURCES_MODEL")
	if model == "" {
		model = liveResourcesModel
	}
	restore := func() {
		for _, m := range lmStudioModels(t) {
			switch {
			case m.State == "loaded" && !loadedBefore[m.ID]:
				lmStudioCall(t, http.MethodPost, "/api/v1/models/unload", map[string]string{"instance_id": m.ID}, nil)
				t.Logf("unloaded %s, which this test loaded", m.ID)
			case m.State != "loaded" && loadedBefore[m.ID]:
				lmStudioCall(t, http.MethodPost, "/api/v1/models/load", map[string]string{"model": m.ID}, nil)
				t.Logf("reloaded %s, which was loaded before this test", m.ID)
			}
		}
	}
	t.Cleanup(restore)

	var loaded struct {
		InstanceID      string  `json:"instance_id"`
		LoadTimeSeconds float64 `json:"load_time_seconds"`
	}
	lmStudioCall(t, http.MethodPost, "/api/v1/models/load", map[string]string{"model": model}, &loaded)
	t.Logf("loaded %s in %.2f s", loaded.InstanceID, loaded.LoadTimeSeconds)

	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "60s"}
	if out, err := runInference(t, append([]string{"host", "add", "lmstudio", "--base", liveLMStudio}, endpoint...)...); err != nil &&
		!strings.Contains(out+err.Error(), "conflict: name") {
		t.Fatalf("host add lmstudio: %v\n%s", err, out)
	}

	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	call, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	machine := client.New(options.endpoint)

	// This test binary is not one of the runtime's operator programs, so it
	// holds no rule by installation and would see only its own rows.
	operator, err := machine.ResolveRightsOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := operator.ListPolicyContext(call, "", 1)
	if err != nil || page.Outcome.String() != "page" {
		t.Fatalf("list policy: %+v %v", page, err)
	}
	permit := rights.PolicyRule{Subject: rights.Subject{Account: account.Uid, Program: filepath.Clean(self)},
		Action: resourceservice.ActionTableRead, Resource: resourceservice.ResourceAccount, Permit: true}
	if edit, err := operator.SetRuleContext(call, page.Revision, permit); err != nil || edit.Outcome.String() != "applied" {
		t.Fatalf("permit table.read: %+v %v", edit, err)
	}

	table, err := machine.ResolveResourceTable(call, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	list, err := table.ResourcesContext(call)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("resources: %v (observed %s)", list.Resources, list.Observed)

	var state rwire.ResourceState
	verified := 0
	eventually(t, 2*time.Minute, "a verified holder of card:0", func() bool {
		state, err = table.HoldersContext(call, instrument.Card0, true)
		if err != nil {
			t.Fatalf("holders: %v", err)
		}
		verified = 0
		for _, row := range state.Holders {
			if row.Evidence == rwire.EvidenceVerified {
				verified++
			}
		}
		return verified > 0
	})

	t.Logf("card:0  held %.2f GiB  capacity %d  instrument %s  observed %s",
		float64(state.Held)/(1<<30), state.Capacity, state.Instrument, state.Observed)
	for _, row := range state.Holders {
		t.Logf("  %-9s %-58s account=%-46s %8.2f GiB  detail=%s",
			row.Evidence, row.Program, row.Account, float64(row.Amount)/(1<<30), row.Detail)
	}
	if verified == 0 {
		t.Fatalf("LM Studio holds %s and the table reports no verified row", model)
	}
	if state.Instrument != instrument.NameWindowsGPUCounters {
		t.Logf("instrument is %s on this platform", state.Instrument)
	}
	if state.Held <= 0 {
		t.Fatalf("held is %d with %d verified rows", state.Held, verified)
	}
	claimed := 0
	for _, row := range state.Holders {
		if row.Evidence == rwire.EvidenceClaimed {
			claimed++
		}
	}
	t.Logf("%d verified row(s), %d claimed row(s)", verified, claimed)

	// The router surveys with this table wired as its residency source; a
	// fresh survey that answers is the router reading it without refusing.
	routerClient, err := machine.ResolveRouter(call, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	hosts, err := routerClient.HostsContext(call, true)
	if err != nil {
		t.Fatalf("router hosts: %v", err)
	}
	for _, h := range hosts.Hosts {
		if h.Up && len(h.Resident) > 0 {
			t.Logf("host %s holds %v", h.Host, h.Resident)
		}
	}
}
