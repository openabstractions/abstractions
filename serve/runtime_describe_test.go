package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	config "github.com/openabstractions/abstraction-config/go/abstraction/config"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	logprovider "github.com/openabstractions/abstraction-logging/go"
	logging "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

// Every endpoint of a running runtime answers abstraction.facade/endpoint@1
// Describe and lists every service it hosts, in the order its host serves them.
func TestEveryRuntimeEndpointDescribesTheServicesItHosts(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := isolatedRuntime(t)
	indexPath, indexBefore := seedPriorRuntimeApplication(t, options)
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
	appEndpoint, err := runtimeApplicationsEndpoint(options)
	if err != nil {
		t.Fatal(err)
	}
	registryEndpoint, err := registryEndpoint(options)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{options.endpoint, options.logEndpoint, options.configEndpoint, options.jobEndpoint, appEndpoint, registryEndpoint} {
		legacy, err := client.New("").DescribeEndpoint(ctx, endpoint)
		if err != nil {
			t.Fatal(err)
		}
		current, err := registration.NewEndpointV2Client((listen.FrameClient{Endpoint: endpoint, Timeout: 5 * time.Second, MaxFrame: 1 << 20}).WithContext(ctx)).Describe()
		if err != nil {
			t.Fatalf("endpoint@2 %s: %v", endpoint, err)
		}
		var oldContracts, newContracts []string
		for _, service := range legacy.Services {
			oldContracts = append(oldContracts, service.Contract)
		}
		for _, service := range current.Services {
			newContracts = append(newContracts, service.Service)
		}
		if !slices.Equal(oldContracts, newContracts) {
			t.Fatalf("endpoint@2 %s: %v, want %v", endpoint, newContracts, oldContracts)
		}
	}
	for endpoint, hosted := range map[string][]wire.ServedService{
		options.endpoint:       {&wire.ResolverDispatcher{}, &wire.CallerDispatcher{}},
		options.logEndpoint:    {&logging.SinkDispatcher{}, &logging.HistoryReaderDispatcher{}, &logging.HistoryObserverDispatcher{}, &logging.ApplicationHistoryReaderDispatcher{}, &logging.ApplicationHistoryObserverDispatcher{}, &logging.SourceReaderDispatcher{}},
		options.configEndpoint: {&config.ConfigReaderDispatcher{}, &config.ConfigEditorDispatcher{}, &config.ConfigObserverDispatcher{}, &config.ConfigReaderV2Dispatcher{}, &config.ConfigEditorV2Dispatcher{}, &config.ConfigObserverV2Dispatcher{}},
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
	for _, contract := range []string{ApplicationsContract, "abstraction.facade/applications@3", "abstraction.facade/messages@1"} {
		_, err := machine.ResolveService(ctx, contract, client.Requirements{})
		if err != nil {
			t.Fatalf("resolve composed %s: %v", contract, err)
		}
	}
	if after, err := os.ReadFile(indexPath); err != nil || !bytes.Equal(after, indexBefore) {
		t.Fatalf("startup rewrote original retained index: %v", err)
	}
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

// Seed the exact pre-rename persisted shape, including its old canonical digest.
// The native source remains absent: retained display never proves an effect.
func seedPriorRuntimeApplication(t *testing.T, options runtimeFlags) (string, []byte) {
	t.Helper()
	policy, err := composeRights(options)
	if err != nil {
		t.Fatal(err)
	}
	d, err := openApplications(options.stateDir, policy.owner, func(context.Context, rightswire.Subject, string, string) (bool, error) { return false, nil })
	if err != nil {
		t.Fatal(err)
	}
	g := &applicationV3Gateway{directory: d, policy: policy}
	if err := d.attachApplicationV3(t.Context(), func(raw []byte, program string, _ *heldApplicationManifest) (wire.ApplicationDescriptor, string, error) {
		return wire.ApplicationDescriptor{Name: "prior-app", Title: "Prior App", Program: program}, applicationV3Hash(raw), nil
	}); err != nil {
		t.Fatal(err)
	}
	v := &registration.Manifest{Version: "applications@3", Name: "prior-app", Title: "Prior App", Host: registration.HostRecipe{ReadinessDeadlineMs: 1000}, Offers: appmodel.CommandDeclaration{Version: "appmodel@1", Commands: []appmodel.DeclaredCommand{}}, Needs: registration.Needs{Models: []registration.ModelNeed{{Modality: "image", RequiredFeatures: []string{}, Placement: registration.PlacementLocal}}}, Choosers: &registration.Choosers{Entries: []registration.Chooser{{Name: "image", Modality: "image"}}}}
	raw := applicationV3PriorCanonical(registration.EncodeManifest(v))
	program := registration1459Program("prior-absent.exe")
	_, f := registration1459File(t, v.Name, program, v.Title)
	f.Raw, f.Digest = raw, applicationV3Hash(raw)
	projection := wire.ApplicationDescriptor{Name: v.Name, Title: v.Title, Program: program}
	if out := d.publishApplicationV3(t.Context(), projection, f, "operator", ""); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	file := d.v3.filePath(d.v3.entries[v.Name].FileRevision)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.decoder(raw, program, &heldApplicationManifest{}); err == nil {
		t.Fatal("old source acquired current native admission")
	}
	if _, err := registration.DecodeManifest(raw); err == nil {
		t.Fatal("current public codec admitted old manifest")
	}
	var kept applicationV3ManifestFile
	if json.Unmarshal(before, &kept) != nil || !bytes.Equal(kept.Raw, raw) || kept.Digest != f.Digest {
		t.Fatal("original custody changed")
	}
	index := filepath.Join(d.v3.dir, "index.json")
	legacyIndex, err := json.Marshal(d.v3.entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index, legacyIndex, 0600); err != nil {
		t.Fatal(err)
	}
	indexBefore, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		after, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(before, after) {
			t.Errorf("retained raw/custody changed: %v", err)
		}
	})
	return index, indexBefore
}
