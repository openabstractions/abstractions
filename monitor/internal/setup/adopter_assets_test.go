package setup

import (
	"encoding/json"
	"strings"
	"testing"
)

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

func assetFixtureJSON(t *testing.T, apps ...adopterAssetPin) []byte {
	t.Helper()
	encoded, err := json.Marshal(adopterAssetPins{Schema: 1, ReleaseTag: "v0.4.0", Apps: apps})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestAdopterPinsProjectOnlyOnQualifiedTarget(t *testing.T) {
	data := assetFixtureJSON(t, pinnedAppFixture())
	for _, target := range [][2]string{{"windows", "amd64"}, {"windows", "arm64"}, {"linux", "amd64"}} {
		c := catalogForTarget(target[0], target[1])
		if err := projectAdopterAssets(&c, target[0], target[1], data); err != nil {
			t.Fatal(err)
		}
		component := c.Components[0]
		if target == [2]string{"windows", "amd64"} {
			if component.Artifact == nil || component.Pending != "" ||
				component.Artifact.URL != "https://github.com/openabstractions/redist/releases/download/v0.4.0/ComfyUI-OA-windows-x64.zip" ||
				component.Artifact.InstallIdentity != "comfyui-oa" {
				t.Fatalf("reviewed pin did not project: %+v", component)
			}
		} else if component.Artifact != nil || component.Pending == "" {
			t.Fatalf("unqualified target %v gained app: %+v", target, component)
		}
	}
}

func TestAdopterPinsRejectMalformedAuthority(t *testing.T) {
	app := pinnedAppFixture()
	good := assetFixtureJSON(t, app)
	for _, raw := range [][]byte{
		[]byte(`{"schema":1,"schema":1,"release_tag":"v0.4.0","apps":[]}`),
		[]byte(`{"schema":1,"Schema":1,"release_tag":"v0.4.0","apps":[]}`),
		[]byte(`{"schema":1,"release_tag":"v0.4.0","apps":[],"unexpected":true}`),
		[]byte(`{"schema":2,"release_tag":"v0.4.0","apps":[]}`),
		[]byte(strings.Replace(string(good), `"binary_sha256":"`+app.BinarySHA256+`"`, `"binary_sha256":"bad"`, 1)),
		[]byte(strings.Replace(string(good), `"source_url":"`+app.SourceURL+`"`, `"source_url":"https://example.com/source"`, 1)),
		[]byte(strings.Replace(string(good), `"source_bytes":200`, `"source_bytes":0`, 1)),
		[]byte(strings.Replace(string(good), `"source_bytes":200`, `"Source_Bytes":200`, 1)),
		[]byte(strings.Replace(string(good), `"records_sha256":"`+app.RecordsSHA256+`"`, `"records_sha256":"bad"`, 1)),
		[]byte(strings.Replace(string(good), `"records_sha256"`, `"install_manifest_sha256"`, 1)),
		[]byte(strings.Replace(string(good), `"id":"comfyui"`, `"id":"opencode"`, 1)),
		assetFixtureJSON(t, app, app),
	} {
		if _, err := parseAdopterAssets(raw); err == nil {
			t.Fatalf("accepted malformed app pins: %s", raw)
		}
	}
}

func TestComfyPinCarriesItsRecordsAndSourceArchiveOntoTheArtifact(t *testing.T) {
	comfy := pinnedAppFixture()
	comfy.RecordsSHA256 = ""
	if _, err := parseAdopterAssets(assetFixtureJSON(t, comfy)); err == nil {
		t.Fatal("ComfyUI pin without its RECORD-set digest was accepted")
	}
	comfy.RecordsSHA256 = strings.Repeat("f", 64)
	if _, err := parseAdopterAssets(assetFixtureJSON(t, comfy)); err != nil {
		t.Fatalf("ComfyUI pin with its RECORD-set digest was rejected: %v", err)
	}
	c := catalogForTarget("windows", "amd64")
	if err := projectAdopterAssets(&c, "windows", "amd64", assetFixtureJSON(t, comfy)); err != nil {
		t.Fatal(err)
	}
	a := c.Components[0].Artifact
	if a == nil || a.RecordsSHA256 != comfy.RecordsSHA256 || a.SourceArchiveSHA256 != comfy.SourceSHA256 ||
		a.SourceArchiveBytes != comfy.SourceBytes ||
		a.SourceArchiveURL != "https://github.com/openabstractions/redist/releases/download/v0.4.0/ComfyUI-OA-source.zip" {
		t.Fatalf("ComfyUI pins did not reach the catalog artifact: %+v", a)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("projected adopter pins fail catalog validation: %v", err)
	}
}
