package main

import (
	"context"
	"slices"
	"testing"
	"time"

	config "github.com/openabstractions/abstraction-config/go/abstraction/config"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	logprovider "github.com/openabstractions/abstraction-logging/go"
	logging "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

// Every endpoint of a running runtime answers abstraction.facade/endpoint@1
// Describe and lists every service it hosts, in the order its host serves them.
func TestEveryRuntimeEndpointDescribesTheServicesItHosts(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := isolatedRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, options, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("startup: %v", err)
	case <-time.After(runtimeWait):
		t.Fatalf("runtime not ready within %v", runtimeWait)
	}
	defer func() {
		cancel()
		if err := awaitStopped(t, done, "runtime"); err != nil {
			t.Error(err)
		}
	}()
	for endpoint, hosted := range map[string][]wire.ServedService{
		options.endpoint:       {&wire.ResolverDispatcher{}, &wire.CallerDispatcher{}},
		options.logEndpoint:    {&logging.SinkDispatcher{}, &logging.HistoryReaderDispatcher{}, &logging.HistoryObserverDispatcher{}, &logging.ApplicationHistoryReaderDispatcher{}, &logging.ApplicationHistoryObserverDispatcher{}, &logging.SourceReaderDispatcher{}},
		options.configEndpoint: {&config.ConfigReaderDispatcher{}, &config.ConfigEditorDispatcher{}, &config.ConfigObserverDispatcher{}},
		options.jobEndpoint:    {&api.RecoverableAcceptanceDispatcher{}, &api.JobInventoryDispatcher{}, &api.OperationControlDispatcher{}, &api.JobOperatorDispatcher{}},
	} {
		call, finish := context.WithTimeout(ctx, 5*time.Second)
		description, err := client.New("").DescribeEndpoint(call, endpoint)
		finish()
		if err != nil {
			t.Fatalf("describe %s: %v", endpoint, err)
		}
		var listed, want []string
		for _, s := range description.Services {
			listed = append(listed, s.Contract)
		}
		for _, s := range hosted {
			want = append(want, s.ServiceContract())
		}
		if description.Outcome != wire.DescriptionOutcomeDescribed || !slices.Equal(listed, want) {
			t.Fatalf("describe %s: %+v, want contracts %v", endpoint, description, want)
		}
	}
	// Check the actual receiving catalogue after the runtime's policy setup.
	// A registration compiled before callbacks exist would retain the grammar
	// but falsely describe this ready owned receiver as unavailable.
	machine := client.New(options.endpoint)
	decisions, err := machine.ResolveRightsSelector(ctx, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := decisions.ListPermissionDefinitionsContext(ctx, "", 64)
	if err != nil || !definitions.Complete {
		t.Fatalf("receiving definitions: %+v %v", definitions, err)
	}
	found := false
	for _, definition := range definitions.Definitions {
		if definition.Definition.ResourceProfile == logprovider.ApplicationLogResourceProfile {
			found = true
			if !definition.Definition.Served || !slices.Contains(definition.SupportedSelectors, rights.ResourceSelectorKindOne) || !slices.Contains(definition.SupportedSelectors, rights.ResourceSelectorKindAll) {
				t.Fatalf("ready application receiver lacks served target grammar: %+v", definition)
			}
		}
	}
	if !found {
		t.Fatal("ready application receiver lacks owning definition")
	}
	reader, err := machine.ResolveLogApplicationReader(ctx, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	const cursor = ""
	page, err := reader.ReadContext(ctx, logging.ApplicationLogTarget{Selection: logging.ApplicationLogSelectionAllApps}, cursor, 1, 512)
	if err != nil || page.Outcome != logging.HistoryPageOutcomeNotPermitted || len(page.Records) != 0 || page.Next != cursor || page.AtEnd {
		t.Fatalf("legacy installation permit application refusal: %+v, %v", page, err)
	}
}
