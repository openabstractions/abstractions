package setup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// Exported by the actual canonical producer test; all payloads are inert.
func TestNativeProducerCatalogReceiving(t *testing.T) {
	raw, err := os.ReadFile("testdata/native_producer_catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	// Git's checkout line endings vary by target; pin the exported inert JSON
	// with canonical LF. The author packet retains the original raw-file pin.
	sum := sha256.Sum256(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")))
	if hex.EncodeToString(sum[:]) != "86ccb7425b63d46fde6451a7c1e5cabf9c85dc7595e1f787ae165a65f90c3a74" {
		t.Fatal("canonical producer fixture changed")
	}
	var exported struct {
		Row map[string]any `json:"row"`
	}
	if err := json.Unmarshal(raw, &exported); err != nil {
		t.Fatal(err)
	}
	doc := nativeCatalogFixture(t)
	row := nativeRow(doc)
	for k := range row {
		delete(row, k)
	}
	for k, v := range exported.Row {
		row[k] = v
	}
	// application_stage_final projects the internal row's release_tag to
	// the containing catalogue document before typed Go intake.
	delete(row, "release_tag")
	assets, err := parseAdopterAssets(nativeJSON(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	a := assets.Apps[0].artifact(assets.ReleaseTag)
	if a.ApplicationProgram != "launch-helper.exe" || a.ArchiveToolProgram != "archive-tool/7zr.exe" || a.NativeResources == nil || a.NativeResources.MarkerBytes != 2008 || a.NativeResources.RecordBytes != 5908 || a.PeakBytes != 107098 {
		t.Fatal("canonical native roles or source space terms were lost")
	}
	row["marker_bytes"] = float64(0)
	if _, err := parseAdopterAssets(nativeJSON(t, doc)); err == nil {
		t.Fatal("missing native source budget accepted")
	}
}
