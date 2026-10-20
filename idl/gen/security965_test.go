package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exact published RemoteTrust excerpt from Facade baseline
// 4d2a1071fb4e2eb6b1a13dc15774f93e9aca903f. Only the document entry name
// changes to exercise every ordinary backend's decoder directly.
const security965PublishedFacadeRecord = `encoding json {
 escape="minimal"
 indent="2"
 map_keys="utf8-bytes"
 numbers="integer-decimal"
 opaque="verbatim"
 terminator="newline"
 duplicate_keys="refuse"
 depth_limit="64"
}
refusal {
 1: malformed(stage="grammar")
 2: bad_string(stage="grammar")
 3: number_spelling(stage="grammar")
 4: wrong_type(stage="grammar")
 5: depth_exceeded(stage="grammar")
 6: duplicate_key(stage="grammar")
 7: duplicate_field(stage="structure")
 8: unknown_field(stage="structure")
 9: missing_field(stage="structure")
 10: bad_enum(stage="structure")
 11: trailing_bytes(stage="document")
}

struct Record {
 1: required string server_name
 2: required string roots
 3: required string certificate
 4: required string key
 5: optional string credential(omit="zero")
}(document="true",unknown_fields="refuse",doc="The explicit mutual-TLS trust of a remote runtime. server_name is the name its certificate must carry. roots, certificate and key are absolute paths of PEM files on this machine: the roots trusted for that server, and this runtime's client certificate and private key, which the remote maps to its own caller. credential, when present, names the abstraction.credentials record the remote holds and applies to requests delegated to it. The paths are configuration; no key material travels in registry@1.")
`

// This uses the current facade's actual refusal block and RemoteTrust fields.
// A document alias gives each ordinary backend decoder a direct test entry.
func security965FacadeRecord(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(privateInput(t, "..", "..", "openabstractions-flat", "abstraction-facade", "facade.thrift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	head := text[strings.Index(text, "encoding json {"):strings.Index(text, "enum Scope")]
	begin := strings.Index(text, "struct RemoteTrust {")
	end := strings.Index(text[begin:], "\nstruct DeclarationCeiling") + begin
	record := strings.Replace(text[begin:end], "struct RemoteTrust", "struct Record", 1)
	record = strings.Replace(record, `}(unknown_fields="refuse"`, `}(document="true",unknown_fields="refuse"`, 1)
	return head + record
}

func TestSecurity965FacadeCompatibility(t *testing.T) {
	current := security965FacadeRecord(t) + `
service PinFixture { Record Read() }(wire_name="security965/fixture@1")
`
	old := security965PublishedFacadeRecord + `
service PinFixture { Record Read() }(wire_name="security965/fixture@1")
`
	if problems := compatProblems(t, old, current); len(problems) != 0 {
		t.Fatalf("published V1 RemoteTrust changed: %v", problems)
	}
	broken := strings.Replace(current, ` 5: optional string credential(omit="zero")`, ` 5: optional string credential(omit="zero")`+"\n"+` 6: required string server_key_sha256(length="64..64")`, 1)
	wantProblem(t, compatProblems(t, old, broken), `closed struct Record gained member server_key_sha256`)
	withoutNewRefusal := strings.Replace(current, " 11: bad_value(stage=\"structure\")\n 12: trailing_bytes", " 11: trailing_bytes", 1)
	if withoutNewRefusal == current {
		t.Fatal("refusal edit did not apply")
	}
	if definitionHashes(mustParse(t, withoutNewRefusal))[0][1] != definitionHashes(mustParse(t, current))[0][1] {
		t.Fatal("refusal precedence changed shape hash")
	}
	if problems := compatProblems(t, withoutNewRefusal, current); len(problems) != 0 {
		t.Fatalf("refusal-only compatibility: %v", problems)
	}
}

func TestSecurity965FacadePinFiveBackends(t *testing.T) {
	s := mustParse(t, security965FacadeRecord(t))
	dir := t.TempDir()
	base := `{"server_name":"runtime.test","roots":"roots.pem","certificate":"client.pem","key":"client.key"}`
	sixth := strings.TrimSuffix(base, "}") + `,"server_key_sha256":"` + strings.Repeat("a", 64) + `"}`
	// A valid sixth pin is still unknown to the actual published five-field reader.
	cases := []string{base, sixth, sixth + " true", strings.Replace(base, `,"key":"client.key"`, "", 1), strings.Replace(base, `"server_name":"runtime.test"`, `"server_name":5`, 1)}
	want := "accept\nunknown_field\nunknown_field\nmissing_field\nwrong_type\n"
	goDriver := `package main
import("fmt";"os";"strings";r "bound.test/rec")
func main(){b,_:=os.ReadFile("cases.txt");for _,line:=range strings.Split(strings.TrimSpace(string(b)),"\n"){_,e:=r.Decode([]byte(line));if e!=nil{fmt.Println(e.(*r.Refusal).Word)}else{fmt.Println("accept")}}}`
	pyDriver := `import rec
for line in open('cases.txt',encoding='utf-8').read().splitlines():
 try:rec.decode(line.encode());print('accept')
 except rec.Refusal as e:print(e.word)
`
	jsDriver := `import * as r from './rec.mjs';import fs from 'node:fs';for(const line of fs.readFileSync('cases.txt','utf8').trim().split('\n')){try{r.decode(Buffer.from(line));console.log('accept')}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word)}}`
	rustDriver := `mod rec;fn main(){for line in std::fs::read_to_string("cases.txt").unwrap().lines(){match rec::decode(line.as_bytes()){Ok(_)=>println!("accept"),Err(e)=>println!("{}",e.word)}}}`
	cppDriver := `#include "rec.h"
#include <fstream>
#include <iostream>
int main(){std::ifstream f("cases.txt");std::string line;while(std::getline(f,line)){try{rec::decode(line);std::cout<<"accept\n";}catch(const rec::Refusal&e){std::cout<<e.word<<'\n';}}}`
	for n, b := range map[string]string{"cases.txt": strings.Join(cases, "\n") + "\n", "rec/rec.go": genGo(s), "rec.py": genPy(s), "rec.h": genCpp(s), "rec.rs": genRust(s), "rec.mjs": genJS(s), "go.mod": "module bound.test\n\ngo 1.22\n", "main.go": goDriver, "main.py": pyDriver, "main.cpp": cppDriver, "main.rs": rustDriver, "main.mjs": jsDriver} {
		writeNamespaceFile(t, dir, n, b)
	}
	run := func(t *testing.T, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
		if got := strings.ReplaceAll(string(out), "\r\n", "\n"); got != want {
			t.Fatalf("%s: got %q want %q", name, got, want)
		}
	}
	compile := func(t *testing.T, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
	}
	t.Run("go", func(t *testing.T) { run(t, "go", "run", "main.go") })
	t.Run("python", func(t *testing.T) { run(t, servicePython(t), "-B", "main.py") })
	t.Run("javascript", func(t *testing.T) { run(t, "node", "main.mjs") })
	t.Run("rust", func(t *testing.T) {
		exe := filepath.Join(dir, "rust.exe")
		compile(t, "rustc", "--edition=2021", "main.rs", "-o", exe)
		run(t, exe)
	})
	t.Run("cpp", func(t *testing.T) {
		exe := filepath.Join(dir, "cpp.exe")
		compile(t, "g++", "-std=c++17", "main.cpp", "-o", exe)
		run(t, exe)
	})
}
