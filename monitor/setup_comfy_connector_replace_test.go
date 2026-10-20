package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// connectorBytes are the connector the pinned extension MSI carries.
const connectorBytes = "comfyui-connector 0.8.1"

func comfyExtensionFixturePins() []byte {
	sum := sha256.Sum256([]byte(connectorBytes))
	return []byte(fmt.Sprintf(`{"schema":2,"release_tag":"v0.8.0","apps":[],"extensions":[{"id":"comfyui","file":"comfyui-0.8.1.msi","sha256":"%s","bytes":100000,"version":"0.8.1","connector_sha256":"%s","connector_bytes":%d}]}`,
		strings.Repeat("a", 64), hex.EncodeToString(sum[:]), len(connectorBytes)))
}

func TestComfyExtensionFixtureFollowsCatalogPlatform(t *testing.T) {
	journal, err := (setup.Journal{}).WithAdopterAssets(comfyExtensionFixturePins())
	if err != nil {
		t.Fatal(err)
	}
	supported := supportsComfyExtensionFixture()
	if available := journal.Catalog().LegacyConnectorPin != nil; available != supported {
		t.Fatalf("ComfyUI extension availability = %v, want %v on %s/%s", available, supported, runtime.GOOS, runtime.GOARCH)
	}
}

func supportsComfyExtensionFixture() bool {
	return runtime.GOOS == "windows" && runtime.GOARCH == "amd64"
}

// upgradedComfy is a portable ComfyUI whose integration folder holds onDisk
// as its connector, after the extension MSI updated it in place, and a
// journal whose extension pin names connectorBytes.
func upgradedComfy(t *testing.T, onDisk string) (setup.ExtensionHost, *setup.Journal) {
	t.Helper()
	if !supportsComfyExtensionFixture() {
		t.Skip("the pinned ComfyUI extension MSI is available only on Windows x64")
	}
	base := filepath.Join(t.TempDir(), "ComfyUI")
	folder := filepath.Join(base, "custom_nodes", "comfyui-openabstractions")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"__init__.py": "", "comfyui-connector.exe": onDisk} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	journal, err := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}.WithAdopterAssets(comfyExtensionFixturePins())
	if err != nil {
		t.Fatal(err)
	}
	if journal.Catalog().LegacyConnectorPin == nil {
		t.Fatal("the Windows x64 fixture catalog did not project its ComfyUI extension")
	}
	return setup.ExtensionHost{Kind: setup.ExtensionPortable, Base: base, Connected: true}, &journal
}

// connectorService is the registry holding the earlier connector's comfyui
// entry at found's connector path, pinned to the earlier image, in state.
// images reads the connector's bytes on disk.
func connectorService(t *testing.T, found setup.ExtensionHost, state string) *nativeComfyService {
	connector := officialPicture.extension.Connector(found.Base)
	raw, err := os.ReadFile(connector)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return &nativeComfyService{images: map[string]string{connector: hex.EncodeToString(sum[:])},
		entry: &nativeComfyEntry{program: connector, pin: "earlier-connector", state: state, title: "ComfyUI", revision: 4}}
}

// connectorRegisterSelf is the connector's register-self against service,
// answered as the registry answers it (serve publishApplicationV3Candidate):
// the same path with another image meets conflict "identity".
func connectorRegisterSelf(service *nativeComfyService) func(context.Context, string) error {
	return func(_ context.Context, base string) error {
		program := officialPicture.extension.Connector(base)
		switch {
		case service.entry == nil:
			service.entry = &nativeComfyEntry{program: program, pin: service.images[program], state: "pending", title: "ComfyUI", revision: 1}
			service.questions++
			service.matchExpected()
			return officialPicture.extension.RegistrationOutcome("applied", "")
		case !identity.SameSubjectProgram(service.entry.program, program):
			return officialPicture.extension.RegistrationOutcome("conflict", "revision")
		case service.entry.pin != service.images[program]:
			return officialPicture.extension.RegistrationOutcome("conflict", "identity")
		}
		service.entry.revision++
		service.matchExpected()
		return officialPicture.extension.RegistrationOutcome("applied", "")
	}
}

// connectThroughCatalog presses Connect on the Catalog's ComfyUI row and
// returns the step's outcome.
func connectThroughCatalog(t *testing.T, found setup.ExtensionHost, journal *setup.Journal, apps setupApplications, service *nativeComfyService) setupOutcome {
	t.Helper()
	p := &servicePanel{setupJournal: journal, comfyApps: apps, appActivity: applicationActivityLog{path: filepath.Join(t.TempDir(), "activity.json")}}
	return connectThroughPanel(t, p, found, service)
}

// connectThroughPanel presses Connect on p's Catalog row.
func connectThroughPanel(t *testing.T, p *servicePanel, found setup.ExtensionHost, service *nativeComfyService) setupOutcome {
	t.Helper()
	p.setupOutcomes = nil
	request := installHandlerFor(p.handler("setup-test-key"))
	register := connectorRegisterSelf(service)
	oldConnect, oldRegister := officialPicture.reconnect, officialPicture.register
	t.Cleanup(func() { officialPicture.reconnect, officialPicture.register = oldConnect, oldRegister })
	officialPicture.reconnect = func(ctx context.Context, _ setup.Catalog, found setup.ExtensionHost) error {
		return register(ctx, found.Base)
	}
	officialPicture.register = func(ctx context.Context, program string) error {
		if program != officialPicture.extension.Connector(found.Base) {
			t.Fatalf("register-self target = %q", program)
		}
		return register(ctx, found.Base)
	}
	stubOfficialComfy(t, found)
	if got := request("POST", "/setup/comfy/connect", `{"confirm":true}`); got.Code != http.StatusOK {
		t.Fatalf("connect: %d %s", got.Code, got.Body.String())
	}
	return waitOutcome(t, p, officialPicture.legacy.ConnectStep)
}

// The person kept the earlier connector; the extension MSI updated it in
// place. Connect re-pins the kept entry to the pinned connector it holds,
// the person is asked nothing, and a second Connect changes nothing.
func TestConnectRepinsTheKeptEarlierConnector(t *testing.T) {
	found, journal := upgradedComfy(t, connectorBytes)
	service := connectorService(t, found, "enabled")
	if o := connectThroughCatalog(t, found, journal, service, service); o.Failure != "" {
		t.Fatalf("connect: %+v", o)
	}
	connector := officialPicture.extension.Connector(found.Base)
	if e := service.entry; e.pin != service.images[connector] || e.state != "enabled" || service.replaces != 1 || service.removes != 0 || service.questions != 0 || service.keepAsks != 0 {
		t.Fatalf("entry %+v replaces=%d removes=%d asked=%d keep asks=%d", e, service.replaces, service.removes, service.questions, service.keepAsks)
	}
	if o := connectThroughCatalog(t, found, journal, service, service); o.Failure != "" || service.replaces != 1 {
		t.Fatalf("second connect: %+v replaces=%d", o, service.replaces)
	}
}

// Legacy releases preserve their same-path re-registration and Keep flow.
func TestConnectReregistersAnUnpinnedOrPendingLegacyConnector(t *testing.T) {
	for _, c := range []struct {
		name, onDisk, state, want string
		asks                      int
	}{
		{"other bytes", "comfyui-connector 0.8.1 rebuilt", "enabled", "pending", 1},
		{"pending entry", connectorBytes, "pending", "pending", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			found, journal := upgradedComfy(t, c.onDisk)
			legacy, err := journal.WithAdopterAssets([]byte(`{"schema":2,"release_tag":"v0.8.0","apps":[],"extensions":[{"id":"comfyui","file":"comfyui-0.8.1.msi","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","bytes":100000,"version":"0.8.1"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			service := connectorService(t, found, c.state)
			if o := connectThroughCatalog(t, found, &legacy, service, service); o.Failure != "" {
				t.Fatalf("connect: %+v", o)
			}
			if e := service.entry; e == nil || e.pin != service.images[officialPicture.extension.Connector(found.Base)] || e.state != c.want || service.removes != 1 || service.questions != 1 || service.keepAsks != c.asks {
				t.Fatalf("entry %+v removes=%d asked=%d keep asks=%d", e, service.removes, service.questions, service.keepAsks)
			}
		})
	}
}

func TestConnectPreservesAConflictingUnkeptRegistration(t *testing.T) {
	found, journal := upgradedComfy(t, connectorBytes)
	service := connectorService(t, found, "pending")
	before := *service.entry
	o := connectThroughCatalog(t, found, journal, service, service)
	if o.Failure == "" || service.entry == nil || *service.entry != before ||
		service.removes != 0 || service.replaces != 0 || service.keepAsks != 0 {
		t.Fatalf("Connect changed a conflicting registration: outcome %+v, entry %+v", o, service.entry)
	}
}

func TestConnectRefusesAnOutdatedUnkeptRegistrationBeforeDispatch(t *testing.T) {
	const prior = "earlier connector image"
	found, journal := upgradedComfy(t, prior)
	service := connectorService(t, found, "pending")
	connector := officialPicture.extension.Connector(found.Base)
	service.entry.pin = service.images[connector]
	before := *service.entry
	panel := connectClickPanel(t, found, journal, service)
	useUndoPackageState(t)
	record := filepath.Join(t.TempDir(), "integration.json")
	oldRecord := officialPicture.recordFile
	officialPicture.recordFile = func() (string, error) { return record, nil }
	t.Cleanup(func() { officialPicture.recordFile = oldRecord })
	o := connectThroughPanel(t, panel, found, service)
	raw, err := os.ReadFile(connector)
	if err != nil || string(raw) != prior || service.entry == nil || *service.entry != before ||
		o.Failure != "the existing integration registration must be resolved before installing" ||
		service.expects != 0 || service.replaces != 0 || service.removes != 0 || officialPicture.readRecord().Connection != nil {
		t.Fatalf("Connect altered an unkept target: outcome %+v, entry %+v, image %q, read %v", o, service.entry, raw, err)
	}
}

// refusingReplace is a registry that refuses the re-pin outright.
type refusingReplace struct{ *nativeComfyService }

func (r refusingReplace) Replace(context.Context, string) (fwire.ApplicationChange, error) {
	return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeForbidden, Reason: "source"}, nil
}

// Connect never reports success while the earlier connector or another
// program keeps the name: the step fails with one plain sentence and the
// entry stays.
func TestConnectFailsPlainlyWhenTheConnectorStaysUnregistered(t *testing.T) {
	found, journal := upgradedComfy(t, connectorBytes)
	service := connectorService(t, found, "enabled")
	o := connectThroughCatalog(t, found, journal, refusingReplace{service}, service)
	if !strings.HasPrefix(o.Failure, "An earlier ComfyUI connector is in Apps, and OpenAbstractions could not replace it") || service.entry.pin != "earlier-connector" || service.removes != 0 || service.keepAsks != 0 {
		t.Fatalf("refused re-pin: %+v entry %+v removes=%d", o, service.entry, service.removes)
	}

	found, journal = upgradedComfy(t, connectorBytes)
	service = connectorService(t, found, "enabled")
	service.entry.program = filepath.Join(t.TempDir(), "other", "comfyui.exe")
	o = connectThroughCatalog(t, found, journal, service, service)
	if o.Failure != "Another program is registered as ComfyUI in Apps. Remove it there, then Connect again" || service.replaces != 0 || service.removes != 0 {
		t.Fatalf("another program: %+v replaces=%d removes=%d", o, service.replaces, service.removes)
	}
}
