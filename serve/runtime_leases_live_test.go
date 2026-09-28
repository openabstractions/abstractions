package main

import (
	"context"
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

// liveLeaseHeadroom is how much beyond what is free this test asks for. It has
// to exceed what is free and stay inside what LM Studio's model will give
// back: the measurement's model holds 17.29 GiB, so a few gigabytes over free
// is an ask one unload satisfies and a smaller ask would not provoke.
const liveLeaseHeadroom = 6 << 30

// liveLeaseWait is how long the holders have. Both unloads timed on
// 2026-09-22 froze on the counters within 7.9 s; ninety seconds is that with
// room for a model several times the size.
const liveLeaseWait = 90 * time.Second

// The card is given away on demand. Live: it loads one model into the LM
// Studio running here, asks an isolated runtime for more of card:0 than is
// free, and asserts that LM Studio unloaded — by its own /v1/models state and
// by the bytes the table reports — with the ask and the yield in the audit.
// It then denies the yield rule for that host and asserts holders_refused with
// the model still loaded. LM Studio is left holding what it held before.
func TestLiveLeaseAsksLMStudioForTheCard(t *testing.T) {
	if os.Getenv("OA_LIVE_LEASES") != "1" {
		t.Skip("set OA_LIVE_LEASES=1 to load a model into this machine's LM Studio and ask it to yield the card")
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
	t.Cleanup(func() {
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
	})

	model := os.Getenv("OA_LIVE_RESOURCES_MODEL")
	if model == "" {
		model = liveResourcesModel
	}
	loadLive(t, model)

	// isolatedRuntime defaults to no product hosts (this test binary's
	// declarationEnv stub, runtime_inference_declared_test.go's init), so
	// this in-process runtime does not discover this machine's LM Studio the
	// way `openabstractions serve runtime --isolated` would for a person
	// running it directly. ProductHosts would probe the real LM Studio
	// installation instead, but this ask needs a lease arbitration to reach
	// it: an explicit host declaration, at the address this test already
	// drives through the HTTP API, keeps the target deterministic regardless
	// of where LM Studio's own config file points.
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "60s"}
	if out, err := runInference(t, append([]string{"host", "add", "lmstudio", "--base", liveLMStudio}, endpoint...)...); err != nil &&
		!strings.Contains(out+err.Error(), "conflict: name") {
		t.Fatalf("host add lmstudio: %v\n%s", err, out)
	}

	call, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	machine := client.New(options.endpoint)
	me := liveSubject(t)

	// This test binary runs the runtime in its own process, so it is the
	// runtime's own program and installation wrote its yield rules. It is not
	// granted a hold on the card by anything, which is the rule under test.
	operator, err := machine.ResolveRightsOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	setLiveRule(t, call, operator, me, resourceservice.ActionTableRead, resourceservice.ResourceAccount, true)

	leases, err := machine.ResolveResourceLeases(call, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	table, err := machine.ResolveResourceTable(call, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}

	// The rule is decided before the table is read, and a refused program
	// holds nothing whatever the card says (RES-L1). This binary runs the
	// runtime in its own process, so installation already wrote it a permit;
	// the refusal is an explicit deny and the permit is written back after.
	setLiveRule(t, call, operator, me, resourceservice.ActionHold, instrument.Card0, false)
	refused, err := leases.AcquireContext(call, instrument.Card0, 1<<30, 0)
	if err != nil {
		t.Fatal(err)
	}
	if refused.Outcome != rwire.AcquireOutcomeNotPermitted {
		t.Fatalf("a program denied the hold rule read %s", refused.Outcome)
	}
	if len(refused.Asked) != 0 {
		t.Fatalf("a refused program had holders asked for it: %+v", refused.Asked)
	}
	setLiveRule(t, call, operator, me, resourceservice.ActionHold, instrument.Card0, true)

	before, err := table.HoldersContext(call, instrument.Card0, true)
	if err != nil {
		t.Fatal(err)
	}
	lmStudioBytes := liveHeldBy(before, "llama")
	t.Logf("card:0 held %.2f GiB, of which LM Studio's server processes hold %.2f GiB",
		float64(before.Held)/(1<<30), float64(lmStudioBytes)/(1<<30))
	if lmStudioBytes <= 0 {
		t.Fatalf("LM Studio holds %s and the table reports no llama-server row: %+v", model, before.Holders)
	}
	pool := before.Capacity
	if pool == 0 {
		pool = instrument.MachineMemory()
	}
	if pool <= before.Held {
		t.Fatalf("the pool is %d and %d is held; there is nothing to ask for", pool, before.Held)
	}
	amount := pool - before.Held + liveLeaseHeadroom
	t.Logf("asking for %.2f GiB of a %.2f GiB pool, which is %.2f GiB more than is free",
		float64(amount)/(1<<30), float64(pool)/(1<<30), float64(liveLeaseHeadroom)/(1<<30))

	began := time.Now()
	granted, err := leases.AcquireContext(call, instrument.Card0, amount, liveLeaseWait)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("acquire read %s in %s", granted.Outcome, time.Since(began).Round(time.Millisecond))
	for _, ask := range granted.Asked {
		t.Logf("  asked %-16s freed %8.2f GiB  %s  in %s", ask.Holder, float64(ask.Amount)/(1<<30),
			ask.Answer, (time.Duration(ask.TookMs) * time.Millisecond))
	}
	if granted.Outcome != rwire.AcquireOutcomeAcquired {
		t.Fatalf("an ask that one unload covers read %s", granted.Outcome)
	}
	var yielded bool
	for _, ask := range granted.Asked {
		if ask.Holder == claimedHostPrefix+"lmstudio" && ask.Answer == resourceservice.AnswerYielded && ask.Amount > 0 {
			yielded = true
		}
	}
	if !yielded {
		t.Fatalf("LM Studio was not asked, or did not yield: %+v", granted.Asked)
	}

	// LM Studio says so itself, and the table's bytes say so too.
	for _, m := range lmStudioModels(t) {
		if m.State == "loaded" {
			t.Fatalf("LM Studio still holds %s after yielding", m.ID)
		}
	}
	after, err := table.HoldersContext(call, instrument.Card0, true)
	if err != nil {
		t.Fatal(err)
	}
	if left := liveHeldBy(after, "llama"); left != 0 {
		t.Fatalf("the table still attributes %.2f GiB to LM Studio's server", float64(left)/(1<<30))
	}
	t.Logf("card:0 held %.2f GiB after the yield, down %.2f GiB",
		float64(after.Held)/(1<<30), float64(before.Held-after.Held)/(1<<30))

	if change, err := leases.ReleaseContext(call, granted.Lease.ID); err != nil || !change.Applied {
		t.Fatalf("release %+v %v", change, err)
	}
	liveAuditHas(t, options.stateDir, `"event":"ask"`, `"holder":"host:lmstudio"`, `"answer":"yielded"`)

	// The refusal variant: the same ask under a rule that refuses yields for
	// this host leaves it holding and the asker with nothing.
	loadLive(t, model)
	setLiveRule(t, call, operator, me, resourceservice.ActionYield, claimedHostPrefix+"lmstudio", false)

	before, err = table.HoldersContext(call, instrument.Card0, true)
	if err != nil {
		t.Fatal(err)
	}
	amount = pool - before.Held + liveLeaseHeadroom
	denied, err := leases.AcquireContext(call, instrument.Card0, amount, liveLeaseWait)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("under a deny rule the same ask read %s", denied.Outcome)
	for _, ask := range denied.Asked {
		t.Logf("  asked %-16s freed %8.2f GiB  %s", ask.Holder, float64(ask.Amount)/(1<<30), ask.Answer)
	}
	if denied.Outcome != rwire.AcquireOutcomeHoldersRefused {
		t.Fatalf("an ask no holder may answer read %s", denied.Outcome)
	}
	if denied.Lease != nil {
		t.Fatal("a refused asker was given a lease")
	}
	if len(denied.Asked) != 1 || !strings.HasPrefix(denied.Asked[0].Answer, resourceservice.AnswerRefused) ||
		!strings.Contains(denied.Asked[0].Answer, claimedHostPrefix+"lmstudio") {
		t.Fatalf("the refusal does not name the host: %+v", denied.Asked)
	}
	loaded := 0
	for _, m := range lmStudioModels(t) {
		if m.State == "loaded" {
			loaded++
		}
	}
	if loaded == 0 {
		t.Fatal("a host with a rule against yielding was unloaded anyway")
	}
	if left := liveHeldBy(before, "llama"); left <= 0 {
		t.Fatalf("LM Studio holds %s and the table reports no llama-server row", model)
	}
	liveAuditHas(t, options.stateDir, `"event":"ask"`, `"holder":"host:lmstudio"`, `"answer":"refused`)
	liveAuditHas(t, options.stateDir, `"event":"refused"`, `"answer":"holders_refused"`)
}

// loadLive loads one model through LM Studio's own REST API.
func loadLive(t *testing.T, model string) {
	t.Helper()
	var loaded struct {
		InstanceID      string  `json:"instance_id"`
		LoadTimeSeconds float64 `json:"load_time_seconds"`
	}
	lmStudioCall(t, http.MethodPost, "/api/v1/models/load", map[string]string{"model": model}, &loaded)
	t.Logf("loaded %s in %.2f s", loaded.InstanceID, loaded.LoadTimeSeconds)
}

// liveSubject is this test binary as rights names a subject.
func liveSubject(t *testing.T) rights.Subject {
	t.Helper()
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return rights.Subject{Account: account.Uid, Program: filepath.Clean(self)}
}

// setLiveRule writes one exact rule at the revision it reads.
func setLiveRule(t *testing.T, ctx context.Context, operator interface {
	ListPolicyContext(context.Context, string, int64) (rights.PolicyPage, error)
	SetRuleContext(context.Context, string, rights.PolicyRule) (rights.PolicyEdit, error)
}, who rights.Subject, action, resource string, permit bool) {
	t.Helper()
	page, err := operator.ListPolicyContext(ctx, "", 1)
	if err != nil || page.Outcome.String() != "page" {
		t.Fatalf("list policy: %+v %v", page, err)
	}
	rule := rights.PolicyRule{Subject: who, Action: action, Resource: resource, Permit: permit}
	edit, err := operator.SetRuleContext(ctx, page.Revision, rule)
	if err != nil || edit.Outcome.String() != "applied" {
		t.Fatalf("set %s on %s permit=%v: %+v %v", action, resource, permit, edit, err)
	}
}

// liveHeldBy sums the verified rows whose program contains name.
func liveHeldBy(state rwire.ResourceState, name string) int64 {
	var total int64
	for _, row := range state.Holders {
		if row.Evidence == rwire.EvidenceVerified && strings.Contains(strings.ToLower(row.Program), name) {
			total += row.Amount
		}
	}
	return total
}

// liveAuditHas fails unless some record of the runtime's own audit carries
// every fragment.
func liveAuditHas(t *testing.T, state string, fragments ...string) {
	t.Helper()
	path := filepath.Join(state, "resources", leaseAuditFile)
	lines, err := resourceservice.ReadAudit(path, 256)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	for _, line := range lines {
		matched := true
		for _, fragment := range fragments {
			if !strings.Contains(line, fragment) {
				matched = false
				break
			}
		}
		if matched {
			t.Logf("audit: %s", line)
			return
		}
	}
	t.Fatalf("no record carries %v; the record holds %d line(s)", fragments, len(lines))
}
