package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func configCaseStrings(rows []json.RawMessage, lang string) string {
	var out []string
	for _, row := range rows {
		out = append(out, fmt.Sprintf("%q", string(row)))
	}
	if lang == "rust" {
		return "vec![" + strings.Join(out, ",") + "]"
	}
	return "{" + strings.Join(out, ",") + "}"
}

func runConfigOverlayOther(t *testing.T, out, lang string, cases []configOverlayCase) {
	t.Helper()
	var source strings.Builder
	var argv []string
	script := filepath.Join(out, "overlay-"+lang)
	switch lang {
	case "python":
		data, err := json.Marshal(cases)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&source, "import json,sys\nsys.path.insert(0,%q)\nfrom abstraction.config import *\ncases=json.loads(%q)\n", filepath.Join(out, "py"), string(data))
		source.WriteString(`for case in cases:
    entries = []
    before = []
    failed = False
    try:
        entries = [from_json(Entry, json.dumps(row).encode()) for row in case["entries"]]
        changes = [from_json(Change, json.dumps(row).encode()) for row in case["changes"]]
        before = [to_json(e) for e in entries]
        got = overlay_config_process(entries, changes)
    except Refusal:
        failed = True
    assert failed == case.get("error", False), case["name"]
    if before:
        assert [to_json(e) for e in entries] == before, case["name"] + " mutated input"
    if not failed:
        expected = [from_json(Entry, json.dumps(row).encode()) for row in case["expected"]]
        assert [to_json(e) for e in got] == [to_json(e) for e in expected], (case["name"], got, expected)
rows = config_key_descriptors()
rows[0].key = "mutated"
assert config_key_descriptors()[0].key != "mutated"
`)
		python := os.Getenv("PYTHON")
		if python == "" {
			python = "python"
		}
		argv = []string{python, script}
	case "javascript":
		script += ".mjs"
		data, err := json.Marshal(cases)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&source, "import assert from 'node:assert/strict';\nimport * as r from %q;\nconst cases=JSON.parse(%q);const enc=new TextEncoder();\n", "file:///"+filepath.ToSlash(filepath.Join(out, "js", "abstraction", "config", "index.mjs")), string(data))
		source.WriteString(`for(const c of cases){let entries=[],before=[],got,failed=false;
 try{entries=c.entries.map(e=>r.fromJson("Entry",enc.encode(JSON.stringify(e))));const changes=c.changes.map(e=>r.fromJson("Change",enc.encode(JSON.stringify(e))));before=entries.map(e=>[...r.toJson("Entry",e)]);got=r.overlayConfigProcess(entries,changes);}catch(e){if(!(e instanceof r.Refusal))throw e;failed=true;}
 assert.equal(failed,c.error,c.name);if(before.length)assert.deepEqual(entries.map(e=>[...r.toJson("Entry",e)]),before,c.name+" input");
 if(!failed){const expected=c.expected.map(e=>r.fromJson("Entry",enc.encode(JSON.stringify(e))));assert.deepEqual(got.map(e=>[...r.toJson("Entry",e)]),expected.map(e=>[...r.toJson("Entry",e)]),c.name);}
}const rows=r.configKeyDescriptors();rows[0].key="mutated";assert.notEqual(r.configKeyDescriptors()[0].key,"mutated");
`)
		argv = []string{"node", script}
	case "cpp":
		script += ".cpp"
		fmt.Fprintf(&source, "#include %q\n#include <iostream>\nusing namespace abstraction::config;\nint main(){\n", filepath.ToSlash(filepath.Join(out, "cpp", "abstraction", "config", "rec.h")))
		for _, c := range cases {
			fmt.Fprintf(&source, "{std::vector<std::string> entries_json=%s,changes_json=%s,expected_json=%s;bool wanted=%t,failed=false;std::vector<Entry> entries,got;std::vector<std::string> before;try{\n", configCaseStrings(c.Entries, lang), configCaseStrings(c.Changes, lang), configCaseStrings(c.Expected, lang), c.Error)
			source.WriteString(`for(const auto& raw:entries_json)entries.push_back(from_json<Entry>(raw));std::vector<Change> changes;for(const auto& raw:changes_json)changes.push_back(from_json<Change>(raw));for(const auto& e:entries)before.push_back(to_json(e));got=overlay_config_process(entries,changes);}catch(const Refusal&){failed=true;}
if(failed!=wanted){std::cerr<<"failure mismatch";return 1;}if(!before.empty()){for(std::size_t i=0;i<entries.size();++i)if(to_json(entries[i])!=before[i])return 2;}
if(!failed){if(got.size()!=expected_json.size())return 3;for(std::size_t i=0;i<got.size();++i)if(to_json(got[i])!=to_json(from_json<Entry>(expected_json[i]))){std::cerr<<to_json(got[i])<<" wanted "<<expected_json[i];return 4;}}}
`)
		}
		source.WriteString("auto rows=config_key_descriptors();rows[0].key=\"mutated\";if(config_key_descriptors()[0].key==\"mutated\")return 5;return 0;}\n")
		argv = []string{"g++", "-std=c++17", "-O0", script, "-o", filepath.Join(out, "overlay-cpp.exe")}
	case "rust":
		script += ".rs"
		fmt.Fprintf(&source, "#[path=%q] mod rec;\nuse rec::*;\nfn main(){\n", filepath.ToSlash(filepath.Join(out, "rs", "abstraction", "config", "rec.rs")))
		for _, c := range cases {
			fmt.Fprintf(&source, "{let entries_json:Vec<&str>=%s;let changes_json:Vec<&str>=%s;let expected_json:Vec<&str>=%s;let wanted=%t;let mut entries=Vec::new();let mut before=Vec::new();let result=(||->Result<Vec<Entry>,Refusal>{\n", configCaseStrings(c.Entries, lang), configCaseStrings(c.Changes, lang), configCaseStrings(c.Expected, lang), c.Error)
			source.WriteString(`for raw in entries_json{entries.push(Entry::from_json(raw.as_bytes())?);}let mut changes=Vec::new();for raw in changes_json{changes.push(Change::from_json(raw.as_bytes())?);}for e in &entries{before.push(e.to_json());}overlay_config_process(&entries,&changes)})();
assert_eq!(result.is_err(),wanted);if !before.is_empty(){assert_eq!(entries.iter().map(|e|e.to_json()).collect::<Vec<_>>(),before);}
if let Ok(got)=result{assert_eq!(got.len(),expected_json.len());for(i,raw)in expected_json.iter().enumerate(){assert_eq!(got[i].to_json(),Entry::from_json(raw.as_bytes()).unwrap().to_json());}}}
`)
		}
		source.WriteString("let mut rows=config_key_descriptors();rows[0].key=\"mutated\".into();assert_ne!(config_key_descriptors()[0].key,\"mutated\");}\n")
		argv = []string{"rustc", "--edition=2021", script, "-o", filepath.Join(out, "overlay-rust.exe")}
	default:
		t.Fatal("unknown corpus backend")
	}
	if err := os.WriteFile(script, []byte(source.String()), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(argv[0], argv[1:]...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	if lang == "cpp" || lang == "rust" {
		if output, err := exec.Command(filepath.Join(out, "overlay-"+lang+".exe")).CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, output)
		}
	}
}
