package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	"github.com/openabstractions/abstraction-identity/listen"
)

// J-COMFY-DESCRIBE (owner, 2026-11-12): the owner opened a workflow in
// ComfyUI's frontend. The adapter's graph page became a new context, and its
// application-model core sent notifications/resources/list_changed. The owned
// session marked itself invalidated for good, so oa_applications_describe
// listed the instance with reason "description" and every command answered
// unavailable with reason "virtualized" until ComfyUI's connector restarted.
// Contexts are read on every call; a context list change keeps the session
// and its description. A tools/list_changed has the description read again
// (FAC-M17), and a description the application does not serve says why.
func TestApplicationV3GatewayKeepsServingAcrossCatalogueNotifications(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	var mu sync.Mutex
	var reported []string
	f.g.report = func(err error) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, err.Error())
	}
	notify := func(method string) {
		t.Helper()
		if err := listen.WriteFrameTo(f.server, []byte(`{"jsonrpc":"2.0","method":"`+method+`"}`), ownedApplicationFrameLimit); err != nil {
			t.Fatal(err)
		}
	}
	commands := func() []string {
		t.Helper()
		listed, err := f.c.Commands(appmodel.CommandsQuery{Application: "fixture", Instance: f.instance})
		if err != nil || listed.Answer == nil || listed.Answer.Outcome != appmodel.ReadingOutcomePage {
			t.Fatalf("commands: %+v %v", listed, err)
		}
		var names []string
		for _, spec := range listed.Answer.Commands {
			names = append(names, spec.Name)
		}
		return names
	}
	listedInstance := func() appmodel.ListedInstance {
		t.Helper()
		described, err := f.c.Describe(appmodel.ApplicationsQuery{Application: "fixture"})
		if err != nil || described.Answer == nil || len(described.Answer.Applications) != 1 || len(described.Answer.Applications[0].Instances) != 1 {
			t.Fatalf("describe: %+v %v", described, err)
		}
		return described.Answer.Applications[0].Instances[0]
	}
	applied := func(n int) {
		t.Helper()
		result, _ := f.call(t, n)
		if result.Answer == nil || result.Answer.Result == nil || result.Answer.Result.Outcome != appmodel.CommandOutcomeApplied {
			t.Fatalf("call %d: %+v", n, result)
		}
	}

	applied(1201)
	// The person opens a workflow: the application's contexts change.
	notify("notifications/resources/list_changed")
	notify("notifications/resources/updated")
	if listed := listedInstance(); listed.Reason != "" || listed.Interface != "abstraction.facade/appmodel@1" || len(listed.Contexts) != 1 {
		t.Fatalf("a context list change ended the instance's listing: %+v", listed)
	}
	if got := commands(); strings.Join(got, ",") != "note.read" {
		t.Fatalf("commands after a context list change: %v", got)
	}
	applied(1202)

	// The application serves one more command and says so.
	f.manifest.Offers.Commands = append(f.manifest.Offers.Commands, appmodel.DeclaredCommand{Name: "note.find", Class: appmodel.CommandClassRead})
	more := *f.description
	more.Commands = append(append([]appmodel.CommandSpec(nil), f.description.Commands...),
		appmodel.CommandSpec{Name: "note.find", Class: appmodel.CommandClassRead, Title: "Find", InputSchema: `{}`, OutputSchema: `{}`, Targets: []string{}})
	f.mu.Lock()
	f.description = &more
	f.mu.Unlock()
	notify("notifications/tools/list_changed")
	waitCatalogue(t, f, 1)
	if got := commands(); strings.Join(got, ",") != "note.read,note.find" {
		t.Fatalf("the description was not read again after tools/list_changed: %v", got)
	}
	applied(1203)

	// The application answers app_describe without a page: the listing and
	// a call say so, and one log line names the cause.
	f.mu.Lock()
	f.answer = func(tool string, _ json.RawMessage) []byte {
		if tool != "app_describe" {
			return nil
		}
		raw, _ := appmodel.ToJSON(&appmodel.DescriptionResult{Outcome: appmodel.ReadingOutcomeUnavailable, Reason: "starting"})
		return raw
	}
	f.mu.Unlock()
	notify("notifications/tools/list_changed")
	waitCatalogue(t, f, 2)
	if listed := listedInstance(); listed.Reason != "describe:unavailable" {
		t.Fatalf("listing reason %q, want describe:unavailable", listed.Reason)
	}
	result, _ := f.call(t, 1204)
	if result.Answer == nil || result.Answer.Result == nil || result.Answer.Result.Outcome != appmodel.CommandOutcomeUnavailable || result.Answer.Result.Reason != "describe:unavailable" {
		t.Fatalf("call without a description: %+v", result)
	}
	mu.Lock()
	lines := strings.Join(reported, "\n")
	mu.Unlock()
	if !strings.Contains(lines, `"fixture" has no description (unavailable, describe:unavailable)`) {
		t.Fatalf("no log line names the cause: %q", lines)
	}
}

func waitCatalogue(t *testing.T, f *applicationV3GatewayFixture, want uint64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for f.s.catalogue.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("catalogue count %d, want %d", f.s.catalogue.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
}
