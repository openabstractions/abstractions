package setup

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

func TestAdopterAssetsPassiveReleaseDeclaration(t *testing.T) {
	legacy := assetFixtureJSON(t, nativeAppPinFixture(t))
	var document map[string]any
	if err := json.Unmarshal(legacy, &document); err != nil {
		t.Fatal(err)
	}
	document["release_admission"] = map[string]any{
		"schema": 1, "version": "0.8.0", "scope_sha256": strings.Repeat("d", 64),
		"delivery_accepted": false, "outcomes": map[string]any{
			"adopters": []any{map[string]any{"id": "lemonade", "target": "windows-x64", "state": "pending"}},
			"journeys": []any{map[string]any{"id": "W2-17a", "target": "web", "state": "pending"}}},
		"selections": []any{}, "capsule_sha256": strings.Repeat("e", 64),
	}
	current, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	base := Journal{}
	old, err := base.WithAdopterAssets(legacy)
	if err != nil {
		t.Fatalf("legacy catalogue: %v", err)
	}
	updated, err := base.WithAdopterAssets(current)
	if err != nil {
		t.Fatalf("catalogue with passive release declaration: %v", err)
	}
	if !reflect.DeepEqual(old.Catalog(), updated.Catalog()) {
		t.Fatal("passive release declaration changed Setup catalog behavior")
	}
	// Release metadata supplies no new artifact or installation authority.
	document["release_admission"] = map[string]any{"delivery_accepted": true, "install": "unreviewed-program"}
	claimed, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	ignored, err := base.WithAdopterAssets(claimed)
	if err != nil || !reflect.DeepEqual(old.Catalog(), ignored.Catalog()) {
		t.Fatalf("opaque release claim affected Setup catalog: %v", err)
	}
	for _, field := range []string{"unexpected", "Release_Admission"} {
		t.Run(field, func(t *testing.T) {
			document[field] = true
			raw, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := base.WithAdopterAssets(raw); err == nil {
				t.Fatal("unknown top-level catalogue field was accepted")
			}
			delete(document, field)
		})
	}
}

func TestOpenCodeIntegrationPin(t *testing.T) {
	product := "{01234567-89AB-CDEF-0123-456789ABCDEF}"
	digest := strings.Repeat("a", 64)
	good := fmt.Sprintf(`{"schema":2,"release_tag":"v0.8.0","apps":[],"extensions":[{"id":"opencode","file":"opencode-0.8.0.msi","sha256":%q,"bytes":73435179,"version":"0.8.0","product_code":%q}]}`, digest, product)
	c := catalogForTarget("windows", "amd64")
	if err := projectAdopterAssets(&c, "windows", "amd64", []byte(good)); err != nil {
		t.Fatal(err)
	}
	want := DesktopIntegration{App: OpenCode, Title: "OpenCode", UpgradeCode: openCodeWatcherFixup.RepairUpgradeCode, File: "opencode-0.8.0.msi", URL: "https://github.com/openabstractions/redist/releases/download/v0.8.0/opencode-0.8.0.msi", SHA256: digest, Bytes: 73435179, ProductCode: product}
	if c.DesktopIntegration == nil || *c.DesktopIntegration != want {
		t.Fatalf("OpenCode pin = %+v, want %+v", c.DesktopIntegration, want)
	}
	signed := strings.Replace(good, `"product_code":`, `"upgrade_code":"`+want.UpgradeCode+`","product_code":`, 1)
	if err := projectAdopterAssets(&c, "windows", "amd64", []byte(signed)); err != nil || c.DesktopIntegration == nil || *c.DesktopIntegration != want {
		t.Fatalf("signed OpenCode pin = %+v (%v)", c.DesktopIntegration, err)
	}
	for _, bad := range []string{
		strings.Replace(signed, want.UpgradeCode, product, 1),
		strings.ReplaceAll(signed, "opencode", "other"),
		strings.ReplaceAll(signed, "opencode", "comfyui"),
		strings.Replace(good, `"product_code":`, `"upgrade_code":`, 1),
		strings.Replace(good, `"schema":2`, `"schema":1`, 1),
		strings.Replace(good, product, "invalid", 1),
		strings.Replace(good, "v0.8.0", "v0.8.1", 1),
		strings.Replace(good, "73435179", "134217729", 1),
		strings.Replace(good, "opencode-0.8.0.msi", "../opencode-0.8.0.msi", 1),
		strings.Replace(good, `"apps":[]`, `"apps":[],"external_url":"https://example.com"`, 1),
	} {
		if err := projectAdopterAssets(&c, "windows", "amd64", []byte(bad)); err == nil {
			t.Fatalf("accepted invalid package %s", bad)
		}
	}
	other := catalogForTarget("linux", "amd64")
	if err := projectAdopterAssets(&other, "linux", "amd64", []byte(good)); err != nil || other.DesktopIntegration != nil {
		t.Fatalf("unexpected non-Windows package: %+v (%v)", other.DesktopIntegration, err)
	}
}

func pinnedAppFixture() adopterAssetPin {
	commit := strings.Repeat("a", 40)
	return adopterAssetPin{
		ID: "comfyui", BinaryZIP: "ComfyUI-OA-windows-x64.zip",
		BinarySHA256: strings.Repeat("b", 64), BinaryBytes: 100,
		SourceZIP: "ComfyUI-OA-source.zip", SourceSHA256: strings.Repeat("c", 64), SourceBytes: 200,
		SourceCommit: commit, SourceURL: "https://github.com/openabstractions/abstractions/commit/" + commit,
		Signer: "unsigned", Version: "v0.4.0", Platform: "windows/amd64", PeakBytes: 1000,
		License: "GPL-3.0-only", Publisher: "openabstractions", InstallIdentity: "comfyui-oa",
		Action: "archive_per_user", RecordsSHA256: strings.Repeat("f", 64),
	}
}

func nativeAppPin(a Artifact) adopterAssetPin {
	return adopterAssetPin{ID: "comfyui", Variant: "oa-comfyui-native", BinaryZIP: "ComfyUI-OA.zip",
		BinarySHA256: a.SHA256, BinaryBytes: a.Bytes, SourceCommit: a.SourceCommit, SourceURL: a.SourceURL,
		Version: a.Version, Platform: a.Platform, PeakBytes: a.PeakBytes, License: a.License, Publisher: a.Publisher,
		InstallIdentity: a.InstallIdentity, Action: a.Action, ApplicationProgram: a.ApplicationProgram,
		ServiceProgram: a.ServiceProgram, ArchiveToolProgram: a.ArchiveToolProgram,
		RecordBytes: a.NativeResources.RecordBytes, MarkerBytes: a.NativeResources.MarkerBytes,
		RecordRoots: a.NativeResources.RecordRoots, CorrespondingSource: &a.NativeResources.Source, Backend: &a.NativeResources.Backend}
}

func nativeAppPinFixture(t *testing.T) adopterAssetPin {
	t.Helper()
	_, artifact, _, _ := nativeArchiveFixture(t)
	return nativeAppPin(artifact)
}

func assetFixtureJSON(t *testing.T, apps ...adopterAssetPin) []byte {
	t.Helper()
	schema := 1
	if len(apps) > 0 && apps[0].Variant != "" {
		schema = 2
	}
	encoded, err := json.Marshal(adopterAssetPins{Schema: schema, ReleaseTag: "v0.4.0", Apps: apps})
	if err != nil {
		t.Fatal(err)
	}
	if schema == 2 {
		var document map[string]any
		if err := json.Unmarshal(encoded, &document); err != nil {
			t.Fatal(err)
		}
		for _, value := range document["apps"].([]any) {
			row := value.(map[string]any)
			for _, key := range []string{"source_zip", "source_sha256", "source_bytes", "signer", "records_sha256"} {
				delete(row, key)
			}
		}
		encoded, err = json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
	}
	return encoded
}

// Setup installs no ComfyUI (owner, 2026-11-11): an earlier document's
// ComfyUI row still reads, and pins nothing on any target.
func TestAComfyRowPinsNoBuild(t *testing.T) {
	data := assetFixtureJSON(t, nativeAppPinFixture(t))
	for _, target := range [][2]string{{"windows", "amd64"}, {"windows", "arm64"}, {"linux", "amd64"}} {
		c := catalogForTarget(target[0], target[1])
		if err := projectAdopterAssets(&c, target[0], target[1], data); err != nil {
			t.Fatal(err)
		}
		if component := c.Components[0]; component.Artifact != nil || component.Pending == "" {
			t.Fatalf("target %v pinned the OA build of ComfyUI: %+v", target, component)
		}
	}
}

// The ComfyUI integration's extension MSI is pinned by file, size and
// SHA-256 under the release's tag; a malformed pin refuses the document.
func TestTheComfyExtensionPinProjects(t *testing.T) {
	good := `{"schema":2,"release_tag":"v0.8.0","apps":[],"extensions":[{"id":"comfyui","file":"comfyui-0.8.0.msi","sha256":"` + strings.Repeat("a", 64) + `","bytes":10,"version":"0.8.0"}]}`
	c := catalogForTarget("windows", "amd64")
	if err := projectAdopterAssets(&c, "windows", "amd64", []byte(good)); err != nil {
		t.Fatal(err)
	}
	if e := c.LegacyConnectorPin; e == nil || e.File != "comfyui-0.8.0.msi" || e.Bytes != 10 || e.Version != "0.8.0" ||
		e.URL != "https://github.com/openabstractions/redist/releases/download/v0.8.0/comfyui-0.8.0.msi" {
		t.Fatalf("extension pin %+v", e)
	}
	connector := strings.Replace(good, `"version":"0.8.0"}`, `"version":"0.8.0","connector_sha256":"`+strings.Repeat("b", 64)+`","connector_bytes":4}`, 1)
	if err := projectAdopterAssets(&c, "windows", "amd64", []byte(connector)); err != nil || c.LegacyConnectorPin.ConnectorSHA256 != strings.Repeat("b", 64) || c.LegacyConnectorPin.ConnectorBytes != 4 {
		t.Fatalf("connector pin %+v %v", c.LegacyConnectorPin, err)
	}
	// The MSI compresses its connector: R31's 7.5 MB connector rode in a
	// 4.5 MB MSI.
	larger := strings.Replace(good, `"version":"0.8.0"}`, `"version":"0.8.0","connector_sha256":"`+strings.Repeat("b", 64)+`","connector_bytes":40}`, 1)
	if err := projectAdopterAssets(&c, "windows", "amd64", []byte(larger)); err != nil || c.LegacyConnectorPin.ConnectorBytes != 40 {
		t.Fatalf("a connector larger than its MSI: %+v %v", c.LegacyConnectorPin, err)
	}
	for _, bad := range []string{
		strings.Replace(good, `"id":"comfyui"`, `"id":"opencode"`, 1),
		strings.Replace(good, `comfyui-0.8.0.msi`, `comfyui-0.8.0.exe`, 1),
		strings.Replace(good, `"bytes":10`, `"bytes":0`, 1),
		strings.Replace(good, `"version":"0.8.0"`, `"version":""`, 1),
		strings.Replace(good, `"version":"0.8.0"}`, `"version":"0.8.0","url":"https://example.com"}`, 1),
		strings.Replace(good, `"version":"0.8.0"}`, `"version":"0.8.0","connector_sha256":"`+strings.Repeat("b", 64)+`"}`, 1),
		strings.Replace(good, `"version":"0.8.0"}`, `"version":"0.8.0","connector_sha256":"xyz","connector_bytes":4}`, 1),
		strings.Replace(good, `"version":"0.8.0"}`, `"version":"0.8.0","connector_sha256":"`+strings.Repeat("b", 64)+`","connector_bytes":67108864}`, 1),
	} {
		if _, err := parseAdopterAssets([]byte(bad)); err == nil {
			t.Fatalf("accepted a malformed extension pin: %s", bad)
		}
	}
}

func TestConnectorPackageBindsOnlyTheAdmittedReleaseRow(t *testing.T) {
	spec := product.PackageSpec{
		ID: "example-app", Title: "Example App", Platform: "windows/amd64",
		ConnectorDigestRef: "release-extension.connector_sha256",
		ArtifactID:         "example-app", ArtifactDigestRef: "release-extension.sha256",
		Rollback:  "keep-previous-digest-until-verified",
		Connector: "example-connector.exe", Registration: "example-app",
	}
	declaration := func(id string) (product.PackageSpec, bool) {
		if id == spec.ID {
			return spec, true
		}
		return product.PackageDeclaration(id)
	}
	pin := adopterExtensionPin{
		ID: spec.ID, File: "example-0.8.0.msi", SHA256: strings.Repeat("a", 64),
		Bytes: 123, Version: "0.8.0", ConnectorSHA256: strings.Repeat("b", 64), ConnectorBytes: 456,
	}
	parse := func(rows ...adopterExtensionPin) (adopterAssetPins, error) {
		t.Helper()
		data, err := json.Marshal(adopterAssetPins{
			Schema: 2, ReleaseTag: "v0.8.0", Apps: []adopterAssetPin{}, Extensions: rows,
		})
		if err != nil {
			t.Fatal(err)
		}
		return parseAdopterAssetsWithPackages(data, declaration)
	}
	desktop := adopterExtensionPin{
		ID: "opencode", File: "opencode-0.8.0.msi", SHA256: strings.Repeat("c", 64),
		Bytes: 660, Version: "0.8.0", ProductCode: "{01234567-89AB-CDEF-0123-456789ABCDEF}",
	}
	legacy := adopterExtensionPin{
		ID: "comfyui", File: "comfyui-0.8.0.msi", SHA256: strings.Repeat("d", 64),
		Bytes: 550, Version: "0.8.0",
	}
	pins, err := parse(pin, desktop, legacy)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := pins.releasePackage(spec.ID, "windows", "amd64")
	if !ok || got != (admittedPackage{spec: spec, pin: pin, releaseTag: "v0.8.0"}) {
		t.Fatalf("package binding = %+v, %v", got, ok)
	}
	catalog := Catalog{LegacyConnectorPin: connectorPackagePin("v0.8.0", legacy)}
	catalog.projectConnectorPackages(pins, "windows", "amd64")
	if binding, ok := catalog.integrationPackage(spec.ID); !ok || binding != got {
		t.Fatalf("Catalog lost the admitted package for %q: %+v, %v", spec.ID, binding, ok)
	}
	if asset, err := catalog.connectorPin(spec.ID); err != nil || asset == nil || *asset != *connectorPackagePin("v0.8.0", pin) {
		t.Fatalf("the declared package did not resolve to its own pin: %+v, %v", asset, err)
	}
	if undeclared, err := catalog.connectorPin("other-app"); undeclared != nil || err != nil {
		t.Fatalf("unrelated app resolved another app's legacy pin: %+v, %v", undeclared, err)
	}
	if historical, err := catalog.connectorPin(legacy.ID); historical != catalog.LegacyConnectorPin || err != nil {
		t.Fatalf("legacy connector resolved to a different record: %+v, %v", historical, err)
	}
	for _, id := range []string{desktop.ID, legacy.ID, "other-app"} {
		if _, ok := catalog.integrationPackage(id); ok {
			t.Fatalf("Catalog admitted a connector package for %q", id)
		}
	}
	catalog.projectConnectorPackages(pins, "windows", "arm64")
	if _, ok := catalog.integrationPackage(spec.ID); ok {
		t.Fatal("Catalog retained a connector package for a different target")
	}
	for _, request := range [][3]string{
		{"other-app", "windows", "amd64"}, {spec.ID, "linux", "amd64"},
		{spec.ID, "windows", "arm64"}, {desktop.ID, "windows", "amd64"},
		{legacy.ID, "windows", "amd64"},
	} {
		if _, ok := pins.releasePackage(request[0], request[1], request[2]); ok {
			t.Fatalf("bound package for %q", request)
		}
	}
	without, err := parse()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := without.releasePackage(spec.ID, "windows", "amd64"); ok {
		t.Fatal("bound package with no release pins")
	}
	rejected, err := parse(pin, pin)
	if err == nil {
		t.Fatal("accepted a duplicate release row")
	}
	if _, ok := rejected.releasePackage(spec.ID, "windows", "amd64"); ok {
		t.Fatal("bound a package in a rejected release")
	}

	for name, change := range map[string]func(*adopterExtensionPin){
		"unowned ID":           func(p *adopterExtensionPin) { p.ID = "other-app" },
		"artifact digest":      func(p *adopterExtensionPin) { p.SHA256 = "invalid" },
		"connector digest":     func(p *adopterExtensionPin) { p.ConnectorSHA256 = "" },
		"connector size":       func(p *adopterExtensionPin) { p.ConnectorBytes = 0 },
		"absent connector":     func(p *adopterExtensionPin) { p.ConnectorSHA256, p.ConnectorBytes = "", 0 },
		"desktop product code": func(p *adopterExtensionPin) { p.ProductCode = desktop.ProductCode },
		"unsafe filename":      func(p *adopterExtensionPin) { p.File = "../example.msi" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := pin
			change(&bad)
			if _, err := parse(bad); err == nil {
				t.Fatal("accepted invalid connector extension pin")
			}
		})
	}
}

func TestAdopterArchiveRolesKeepLegacyAndRetainedRowsDistinct(t *testing.T) {
	commit := strings.Repeat("a", 40)
	shared := adopterAssetPin{
		ID: "pytorch", BinaryZIP: "shared-engine.zip", BinaryBytes: 100, BinarySHA256: strings.Repeat("b", 64),
		SourceZIP: "shared-source.zip", SourceBytes: 50, SourceSHA256: strings.Repeat("c", 64),
		SourceCommit: commit, SourceURL: "https://github.com/example/engine/commit/" + commit,
		Signer: "unsigned", Version: "0.8.0", Platform: "windows/amd64", PeakBytes: 200,
		License: "MIT", Publisher: "fixture", Action: "archive_per_user", InstallIdentity: "pt",
	}
	encode := func(value any) []byte {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	var native struct{ Apps []json.RawMessage }
	if err := json.Unmarshal(assetFixtureJSON(t, nativeAppPinFixture(t)), &native); err != nil {
		t.Fatal(err)
	}
	for _, schema := range []int{1, 2} {
		t.Run(fmt.Sprint(schema), func(t *testing.T) {
			rows := []json.RawMessage{encode(shared)}
			if schema == 2 {
				rows = append(rows, native.Apps...)
			}
			data := encode(map[string]any{"schema": schema, "release_tag": "v0.4.0", "apps": rows})
			c := Catalog{Components: []Component{{ID: "PYTORCH-ENGINE", Pending: "waiting"}, {ID: "comfyui-app", Pending: "retired"}}}
			if err := projectAdopterAssets(&c, "windows", "amd64", data); err != nil {
				t.Fatal(err)
			}
			if c.Components[0].Artifact == nil || c.Components[0].Artifact.SHA256 != shared.BinarySHA256 ||
				c.Components[0].Pending != "" || c.Components[1].Artifact != nil || c.Components[1].Pending != "retired" {
				t.Fatalf("archive admission projected the wrong components: %+v", c.Components)
			}
		})
	}
	shared.ID = "comfyui"
	rows := []adopterAssetPin{shared}
	for _, schema := range []int{1, 2} {
		data := encode(adopterAssetPins{Schema: schema, ReleaseTag: "v0.4.0", Apps: rows})
		_, err := parseAdopterAssets(data)
		if err == nil {
			t.Fatalf("schema %d accepted the native row with shared archive fields", schema)
		}
		if schema == 1 && err.Error() != "ComfyUI requires the native application row" {
			t.Fatalf("schema 1 refusal changed: %v", err)
		}
	}
}

func TestAdopterPinsRejectMalformedAuthority(t *testing.T) {
	app := nativeAppPinFixture(t)
	good := assetFixtureJSON(t, app)
	for _, raw := range [][]byte{
		[]byte(`{"schema":2,"schema":2,"release_tag":"v0.4.0","apps":[]}`),
		[]byte(`{"schema":2,"Schema":2,"release_tag":"v0.4.0","apps":[]}`),
		[]byte(`{"schema":2,"release_tag":"v0.4.0","apps":[],"unexpected":true}`),
		[]byte(`{"schema":3,"release_tag":"v0.4.0","apps":[]}`),
		[]byte(strings.Replace(string(good), `"binary_sha256":"`+app.BinarySHA256+`"`, `"binary_sha256":"bad"`, 1)),
		[]byte(strings.Replace(string(good), `"source_url":"`+app.SourceURL+`"`, `"source_url":"https://example.com/source"`, 1)),
		[]byte(strings.Replace(string(good), `"record_bytes":`, `"Record_Bytes":`, 1)),
		[]byte(strings.Replace(string(good), `"id":"comfyui"`, `"id":"opencode"`, 1)),
		assetFixtureJSON(t, app, app),
	} {
		if _, err := parseAdopterAssets(raw); err == nil {
			t.Fatalf("accepted malformed native app pins: %s", raw)
		}
	}
	for _, change := range []func(*adopterAssetPin){
		func(p *adopterAssetPin) { p.CorrespondingSource = nil },
		func(p *adopterAssetPin) { p.Backend = nil },
		func(p *adopterAssetPin) { p.RecordBytes = 0 },
		func(p *adopterAssetPin) { p.MarkerBytes = 0 },
	} {
		bad := nativeAppPinFixture(t)
		change(&bad)
		if _, err := parseAdopterAssets(assetFixtureJSON(t, bad)); err == nil {
			t.Fatal("accepted incomplete native authority")
		}
	}
}

func TestComfyPinCarriesNativeResourcesAndSourceArchiveOntoTheArtifact(t *testing.T) {
	comfy := nativeAppPinFixture(t)
	pins, err := parseAdopterAssets(assetFixtureJSON(t, comfy))
	if err != nil {
		t.Fatal(err)
	}
	a := pins.Apps[0].artifact(pins.ReleaseTag)
	if a == nil || a.RecordsSHA256 != "" || a.ApplicationProgram != comfy.ApplicationProgram ||
		!reflect.DeepEqual(a.NativeResources.Source, *comfy.CorrespondingSource) || !reflect.DeepEqual(a.NativeResources.Backend, *comfy.Backend) {
		t.Fatalf("native resources did not reach the artifact: %+v", a)
	}
	if err := a.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestComfyPortableRowRefusesBeforeCatalogProjection(t *testing.T) {
	c := catalogForTarget("windows", "amd64")
	before := c.Components[0]
	if err := projectAdopterAssets(&c, "windows", "amd64", assetFixtureJSON(t, pinnedAppFixture())); err == nil ||
		!strings.Contains(err.Error(), "native application row") {
		t.Fatalf("portable Comfy row accepted: %v", err)
	}
	if !reflect.DeepEqual(before, c.Components[0]) {
		t.Fatal("refused row changed the catalog")
	}
}

func nativeCatalogFixture(t *testing.T) map[string]any {
	t.Helper()
	pin := func(name string, size int64) map[string]any {
		return map[string]any{"name": name, "bytes": size, "sha256": strings.Repeat("a", 64)}
	}
	archive := func(name, format string) map[string]any {
		p := pin(name, 10)
		return map[string]any{"name": name, "bytes": int64(10), "sha256": p["sha256"], "format": format, "parts": []any{p}}
	}
	data := map[string]any{"repository": "openabstractions/comfy-data", "tag": "backend-0.8.0-fixture"}
	commit := strings.Repeat("b", 40)
	return map[string]any{"schema": 2, "release_tag": "v0.8.0", "apps": []any{map[string]any{
		"id": "comfyui", "variant": "oa-comfyui-desktop", "binary_zip": "ComfyUI-OA.zip", "binary_bytes": 100, "binary_sha256": strings.Repeat("c", 64), "source_commit": commit, "source_url": "https://github.com/openabstractions/adopter-comfyui/commit/" + commit, "version": "v0.8.0", "platform": "windows/amd64", "peak_bytes": 1000, "license": "AGPL-3.0-or-later", "publisher": "openabstractions",
		"corresponding_source": map[string]any{"archive": archive("Desktop-source.zip", "zip"), "data_release": data},
		"backend":              map[string]any{"schema": 2, "variant": "win-amd", "comfyui_ref": commit, "python_version": "3.12.10", "archive": archive("backend.7z", "7z"), "corresponding_source": archive("backend-source.zip", "zip"), "data_release": data, "provenance": map[string]any{"producer_receipt": pin("backend-producer.json", 10), "input_descriptor": pin("backend-inputs.json", 10), "manifest": pin("backend-manifest.json", 10), "backend_policy": pin("backend-amd-v1.json", 10)}},
	}}}
}
func nativeRow(doc map[string]any) map[string]any { return doc["apps"].([]any)[0].(map[string]any) }
func nativeJSON(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	b, e := json.Marshal(doc)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestNativeDesktopCatalogIsPassive(t *testing.T) {
	doc := nativeCatalogFixture(t)
	raw := nativeJSON(t, doc)
	pins, err := parseAdopterAssets(raw)
	if err != nil {
		t.Fatal(err)
	}
	if pins.Apps[0].artifact(pins.ReleaseTag) != nil {
		t.Fatal("native row became a portable artifact")
	}
	for _, target := range [][2]string{{"windows", "amd64"}, {"linux", "amd64"}, {"windows", "arm64"}} {
		c := catalogForTarget(target[0], target[1])
		before := catalogForTarget(target[0], target[1])
		if err := projectAdopterAssets(&c, target[0], target[1], raw); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(c, before) {
			t.Fatal("passive native metadata changed install catalog")
		}
	}
	// The declaration remains opaque even when its keys resemble resource fields.
	doc["release_admission"] = map[string]any{"archive": map[string]any{"claim": "opaque"}}
	if _, err := parseAdopterAssets(nativeJSON(t, doc)); err != nil {
		t.Fatal(err)
	}
}
func TestNativeDesktopCatalogRejectsMalformedBoundaries(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"legacy-schema":    func(d map[string]any) { d["schema"] = 1 },
		"portable-field":   func(d map[string]any) { nativeRow(d)["action"] = "" },
		"missing-resource": func(d map[string]any) { delete(nativeRow(d), "backend") },
		"wrong-variant":    func(d map[string]any) { nativeRow(d)["variant"] = "archive_per_user" },
		"wrong-source":     func(d map[string]any) { nativeRow(d)["source_url"] = "https://example.com/source" },
		"mutable-data": func(d map[string]any) {
			nativeRow(d)["backend"].(map[string]any)["data_release"].(map[string]any)["tag"] = "LATEST"
		},
		"different-source-release": func(d map[string]any) {
			nativeRow(d)["corresponding_source"].(map[string]any)["data_release"] = map[string]any{"repository": "openabstractions/comfy-data", "tag": "other-valid-fixture"}
		},
		"foreign-data": func(d map[string]any) {
			nativeRow(d)["backend"].(map[string]any)["data_release"].(map[string]any)["repository"] = "foreign/repo"
		},
		"wrong-python":    func(d map[string]any) { nativeRow(d)["backend"].(map[string]any)["python_version"] = "3.12.0" },
		"previous-python": func(d map[string]any) { nativeRow(d)["backend"].(map[string]any)["python_version"] = "3.13.17" },
		"part-limit": func(d map[string]any) {
			nativeRow(d)["backend"].(map[string]any)["archive"].(map[string]any)["parts"].([]any)[0].(map[string]any)["bytes"] = int64(maxReleaseAssetBytes)
		},
		"logical-limit": func(d map[string]any) {
			nativeRow(d)["backend"].(map[string]any)["archive"].(map[string]any)["bytes"] = int64(8*1024*1024*1024 + 1)
		},
		"part-pin": func(d map[string]any) {
			nativeRow(d)["backend"].(map[string]any)["archive"].(map[string]any)["parts"].([]any)[0].(map[string]any)["sha256"] = strings.Repeat("f", 64)
		},
		"bad-part-order": func(d map[string]any) {
			a := nativeRow(d)["backend"].(map[string]any)["archive"].(map[string]any)
			a["parts"].([]any)[0].(map[string]any)["name"] = "backend.7z.002"
		},
		"proof-name": func(d map[string]any) {
			nativeRow(d)["backend"].(map[string]any)["provenance"].(map[string]any)["manifest"].(map[string]any)["name"] = "other.json"
		},
		"proof-size": func(d map[string]any) {
			nativeRow(d)["backend"].(map[string]any)["provenance"].(map[string]any)["manifest"].(map[string]any)["bytes"] = 16*1024*1024 + 1
		},
		"unknown-nested": func(d map[string]any) {
			nativeRow(d)["backend"].(map[string]any)["data_release"].(map[string]any)["Tag"] = "v1"
		},
		"final-source-alias": func(d map[string]any) {
			a := nativeRow(d)["corresponding_source"].(map[string]any)["archive"].(map[string]any)
			a["name"] = "ComfyUI-OA.zip"
			a["parts"].([]any)[0].(map[string]any)["name"] = "ComfyUI-OA.zip"
		},
	} {
		t.Run(name, func(t *testing.T) {
			doc := nativeCatalogFixture(t)
			change(doc)
			if _, err := parseAdopterAssets(nativeJSON(t, doc)); err == nil {
				t.Fatal("accepted malformed native catalog")
			}
		})
	}
}
func TestNativeArchiveMultipartBoundaries(t *testing.T) {
	a := adopterArchiveSet{Name: "backend.7z", Format: "7z", Bytes: 2 * maxReleaseAssetBytes, SHA256: strings.Repeat("a", 64)}
	for i := 1; i <= 3; i++ {
		size := int64(maxReleaseAssetBytes - 1)
		if i == 3 {
			size = 2
		}
		a.Parts = append(a.Parts, adopterArchivePin{fmt.Sprintf("backend.7z.%03d", i), size, strings.Repeat("b", 64)})
	}
	if err := a.validate("7z", map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	a.Parts[1].Name = "backend.7z.001"
	if err := a.validate("7z", map[string]bool{}); err == nil {
		t.Fatal("accepted duplicate/noncontiguous parts")
	}
}
