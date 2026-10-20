package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type configOverlayCase struct {
	Name     string            `json:"name"`
	Entries  []json.RawMessage `json:"entries"`
	Changes  []json.RawMessage `json:"changes"`
	Expected []json.RawMessage `json:"expected"`
	Error    bool              `json:"error"`
}

func configCatalogueFixture(t *testing.T, languages ...string) string {
	t.Helper()
	out := t.TempDir()
	args := []string{"testdata/config_catalogue/config.thrift", out, "--no-ipc", "--config-catalogue=keys.json"}
	args = append(args, languages...)
	if err := run(args, new(strings.Builder)); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestConfigCatalogueAdmissionBeforeOutputs(t *testing.T) {
	source, err := os.ReadFile("testdata/config_catalogue/config.thrift")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/config_catalogue/keys.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"unknown kind", "missing key", "wrong range type", "long description", "multibyte description", "duplicate key", "duplicate member", "number spelling", "non-tagged default", "unpaired surrogate", "invalid map key", "oversize unknown member", "oversize presets", "nested encoded limit", "missing unlimited declaration", "missing unlimited flag", "disallowed unlimited default", "disallowed unlimited preset", "not array"} {
		t.Run(name, func(t *testing.T) {
			var rows []map[string]any
			if err := json.Unmarshal(data, &rows); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "unknown kind":
				rows[0]["kind"] = "other"
			case "missing key":
				delete(rows[0], "key")
			case "wrong range type":
				rows[1]["range"] = map[string]any{"min": "zero", "max": 100}
			case "long description":
				rows[0]["description"] = strings.Repeat("x", 10000)
			case "multibyte description":
				rows[0]["description"] = strings.Repeat("é", 300)
			case "non-tagged default":
				rows[0]["default"] = map[string]any{"kind": "boolean", "text": "wrong"}
			case "invalid map key":
				rows[7]["default"] = map[string]any{"kind": "text_map", "text_map": map[string]any{"bad key": "reason"}}
			case "oversize unknown member":
				rows[0]["future_metadata"] = strings.Repeat("x", 270000)
			case "oversize presets":
				var presets []any
				for i := 0; i < 16; i++ {
					var texts []string
					for j := 0; j < 128; j++ {
						texts = append(texts, strings.Repeat("x", 128))
					}
					presets = append(presets, map[string]any{"kind": "text_list", "text_list": texts})
				}
				rows[6]["presets"] = presets
			case "nested encoded limit":
				rows[4]["default"] = map[string]any{"kind": "text", "text": strings.Repeat("x", 80)}
			case "missing unlimited flag":
				delete(rows[0], "allows_unlimited")
			case "disallowed unlimited default":
				rows[0]["default"] = map[string]any{"kind": "unlimited"}
			case "disallowed unlimited preset":
				rows[0]["presets"] = []any{map[string]any{"kind": "unlimited"}}
			case "duplicate key":
				rows = append(rows, rows[0])
			}
			bad, err := json.Marshal(rows)
			if err != nil {
				t.Fatal(err)
			}
			if name == "not array" {
				bad = []byte(`{}`)
			}
			if name == "duplicate member" {
				bad = []byte(strings.Replace(string(bad), `"key":"boolean"`, `"key":"boolean","key":"boolean"`, 1))
			}
			if name == "number spelling" {
				bad = []byte(strings.Replace(string(bad), `"max":100`, `"max":1e2`, 1))
			}
			if name == "unpaired surrogate" {
				bad = []byte(strings.Replace(string(bad), `Fixture setting.`, `\ud800`, 1))
			}
			dir := t.TempDir()
			schema := filepath.Join(dir, "config.thrift")
			schemaSource := source
			if name == "missing unlimited declaration" {
				schemaSource = []byte(strings.Replace(string(source), " 14: required bool allows_unlimited", "", 1))
				if string(schemaSource) == string(source) {
					t.Fatal("unlimited declaration marker missing")
				}
			}
			if name == "nested encoded limit" {
				schemaSource = []byte(strings.Replace(string(source), `max_encoded="262144",doc="The tag`, `max_encoded="64",doc="The tag`, 1))
				if string(schemaSource) == string(source) {
					t.Fatal("nested limit fixture marker missing")
				}
			}
			if err := os.WriteFile(schema, schemaSource, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "keys.json"), bad, 0600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, "out")
			if err := run([]string{schema, out, "--no-ipc", "--config-catalogue=keys.json", "go"}, new(strings.Builder)); err == nil {
				t.Fatal("invalid catalogue accepted")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("refusal left outputs: %v", err)
			}
		})
	}
}

func TestConfigCatalogueExplicitSelectionPreservesHook(t *testing.T) {
	schema := "testdata/config_catalogue/config.thrift"
	def, err := loadDefinition(schema)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	args := []string{schema, out, "--no-ipc", "--config-catalogue=keys.json", "-only=" + strings.Join(surfaces(def), ","), "go"}
	if err := run(args, new(strings.Builder)); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(out, "go", "abstraction", "config", "rec.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "func OverlayConfigProcess") {
		t.Fatal("selection silently lost catalogue hook")
	}
	runConfigOverlayCorpus(t, out, "go", []configOverlayCase{{Name: "selection", Entries: []json.RawMessage{json.RawMessage(`{"key":"integer","value":{"kind":"integer","integer":2,"text":"wrong"},"origin":{"location":"user","path":"","state":"set"}}`)}, Error: true}})
	partial := filepath.Join(t.TempDir(), "out")
	if err := run([]string{schema, partial, "--no-ipc", "--config-catalogue=keys.json", "-only=Value,ValueKind", "go"}, new(strings.Builder)); err == nil {
		t.Fatal("incomplete catalogue selection accepted")
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatalf("incomplete selection left outputs: %v", err)
	}
}

func TestConfigCatalogueOptInLeavesOtherDefinitionsUnchanged(t *testing.T) {
	def, err := loadDefinition("testdata/config_catalogue/config.thrift")
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, backend := range backends {
		if backend.emitsCode {
			before[backend.lang] = mustEmit(t, backend, def)
		}
	}
	if err := loadConfigCatalogue(def, "testdata/config_catalogue/keys.json"); err != nil {
		t.Fatal(err)
	}
	def.ConfigCatalogue = ""
	for _, backend := range backends {
		if backend.emitsCode && mustEmit(t, backend, def) != before[backend.lang] {
			t.Fatalf("%s changed without hook", backend.lang)
		}
	}
}

func TestConfigCatalogueOverlayCorpus(t *testing.T) {
	data, err := os.ReadFile("testdata/config_catalogue/overlay.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []configOverlayCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	out := configCatalogueFixture(t, "go", "python", "javascript", "cpp", "rust")
	for _, lang := range []string{"go", "python", "javascript", "cpp", "rust"} {
		t.Run(lang, func(t *testing.T) { runConfigOverlayCorpus(t, out, lang, cases) })
	}
}

func TestConfigCatalogueRustIdioms(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("generated Rust Clippy qualification runs on Windows; Rust compile and corpus tests run on every platform")
	}
	out := configCatalogueFixture(t, "rust")
	dir := filepath.Join(out, "rs", "abstraction", "config")
	manifest := filepath.Join(dir, "Cargo.toml")
	if err := os.WriteFile(manifest, []byte("[package]\nname=\"config-catalogue-idiom\"\nversion=\"0.0.0\"\nedition=\"2021\"\n[lib]\npath=\"rec.rs\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("cargo", "clippy", "--offline", "--manifest-path", manifest, "--", "-D", "warnings")
	command.Env = append(os.Environ(), "CARGO_HOME="+t.TempDir())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
}

func TestConfigCatalogueRustWithIncludedDefinitionsCompiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"config.thrift", "keys.json"} {
		data, err := os.ReadFile(filepath.Join("testdata/config_catalogue", name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "config.thrift" {
			data = append([]byte("include \"external.thrift\"\n"), data...)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "external.thrift"), []byte(head+`namespace * external
struct Marker { 1: required string name }(document="true",unknown_fields="refuse")`), 0600); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := run([]string{filepath.Join(dir, "config.thrift"), out, "--no-ipc", "--config-catalogue=keys.json", "--rust-import=external=external", "rust"}, new(strings.Builder)); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(out, "Cargo.toml")
	if err := os.WriteFile(manifest, []byte("[package]\nname=\"config-catalogue-included\"\nversion=\"0.0.0\"\nedition=\"2021\"\n[lib]\npath=\"rs/abstraction/config/rec.rs\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("cargo", "check", "--offline", "--manifest-path", manifest)
	command.Env = append(os.Environ(), "CARGO_HOME="+t.TempDir())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
}

func configGoStrings(rows []json.RawMessage) string {
	var out []string
	for _, row := range rows {
		out = append(out, fmt.Sprintf("%q", string(row)))
	}
	return "[]string{" + strings.Join(out, ",") + "}"
}

func runConfigOverlayCorpus(t *testing.T, out, lang string, cases []configOverlayCase) {
	t.Helper()
	if lang != "go" {
		runConfigOverlayOther(t, out, lang, cases)
		return
	}
	var source strings.Builder
	source.WriteString("package config\nimport(\"testing\";\"reflect\";\"strings\")\nfunc TestOverlay(t *testing.T){\n")
	for _, c := range cases {
		fmt.Fprintf(&source, "t.Run(%q,func(t *testing.T){entriesJSON:=%s;changesJSON:=%s;expectedJSON:=%s;wantError:=%t\n", c.Name, configGoStrings(c.Entries), configGoStrings(c.Changes), configGoStrings(c.Expected), c.Error)
		source.WriteString(`var entries []Entry;var changes []Change;var failure error
for _,raw:=range entriesJSON{var e Entry;if err:=FromJSON([]byte(raw),&e);err!=nil{failure=err;break};entries=append(entries,e)}
if failure==nil{for _,raw:=range changesJSON{var c Change;if err:=FromJSON([]byte(raw),&c);err!=nil{failure=err;break};changes=append(changes,c)}}
var before [][]byte;for _,e:=range entries{raw,err:=ToJSON(e);if err!=nil{t.Fatal(err)};before=append(before,raw)}
var got []Entry;if failure==nil{got,failure=OverlayConfigProcess(entries,changes)}
if (failure!=nil)!=wantError{t.Fatalf("failure=%v wanted=%t",failure,wantError)}
for i,e:=range entries{raw,err:=ToJSON(e);if err!=nil{t.Fatal(err)};if !reflect.DeepEqual(raw,before[i]){t.Fatal("input mutated")}}
if failure==nil{if len(got)!=len(expectedJSON){t.Fatalf("entries=%d want=%d",len(got),len(expectedJSON))};for i,raw:=range expectedJSON{var want Entry;if err:=FromJSON([]byte(raw),&want);err!=nil{t.Fatal(err)};actual,err:=ToJSON(got[i]);if err!=nil{t.Fatal(err)};expected,err:=ToJSON(want);if err!=nil{t.Fatal(err)};if string(actual)!=string(expected){t.Fatalf("got %s want %s",actual,expected)}}}
})
`)
	}
	source.WriteString("rows:=ConfigKeyDescriptors();rows[0].Key=\"mutated\";if ConfigKeyDescriptors()[0].Key==\"mutated\"{t.Fatal(\"catalogue shared mutable state\")}\n}\n")
	source.WriteString(`func TestNativeConfigValueBounds(t *testing.T){
 for _,v:=range []Value{{Kind:ValueKindText,Text:strings.Repeat("x",4097)},{Kind:ValueKindText,Text:"bad\ncontrol"},{Kind:ValueKindTextList,TextList:[]string{""}},{Kind:ValueKindInteger,Integer:2,Boolean:true}}{if ValidateConfigValue(&v)==nil{t.Fatalf("native invalid value accepted: %+v",v)}}
 if err:=ValidateConfigValue(&Value{Kind:ValueKindBoolean});err!=nil{t.Fatal(err)}
 var invalid Value;if FromJSON([]byte("{\"kind\":\"integer\",\"integer\":2,\"text\":\"wrong\"}"),&invalid)==nil{t.Fatal("Value decoder accepted multiple payloads")}
}
`)
	dir := filepath.Join(out, "go", "abstraction", "config")
	if err := os.WriteFile(filepath.Join(dir, "overlay_test.go"), []byte(source.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture\ngo 1.26.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "-C", dir, "test", ".", "-count=1")
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
}

func TestConfigCatalogueRenamedSource(t *testing.T) {
	source, err := os.ReadFile("testdata/config_catalogue/config.thrift")
	if err != nil {
		t.Fatal(err)
	}
	keys, err := os.ReadFile("testdata/config_catalogue/keys.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	schema := filepath.Join(dir, "config.thrift")
	if err = os.WriteFile(schema, []byte(strings.ReplaceAll(string(source), "Shadow", "ConfigSource")), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "keys.json"), keys, 0600); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err = run([]string{schema, out, "--no-ipc", "--config-catalogue=keys.json", "go", "python", "javascript", "cpp", "rust"}, new(strings.Builder)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/config_catalogue/overlay.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []configOverlayCase
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"go", "python", "javascript", "cpp", "rust"} {
		t.Run(lang, func(t *testing.T) { runConfigOverlayCorpus(t, out, lang, cases) })
	}
}
