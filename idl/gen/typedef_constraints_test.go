package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func namedConstraintRecord() string {
	s := `typedef string Key(length="1..8",pattern="no_control")
typedef string Element(length="1..4",pattern="no_control")
typedef string Name(length="2..4",pattern="ascii_name")
`
	r := strings.Replace(constraintRecord, `string key(length="1..8",pattern="no_control")`, `Key key`, 1)
	r = strings.Replace(r, "list<string> tags", "list<Element> tags", 1)
	r = strings.Replace(r, "map<string,string> slots", "map<Element,Element> slots", 1)
	r = strings.Replace(r, `string name(omit="zero",length="2..4",pattern="ascii_name")`, `Name name(omit="zero")`, 1)
	r = strings.Replace(r, `}(document="true"`, `11:required Child child
12:optional Child maybe(omit="absent")
}(document="true"`, 1)
	return constraintHead + s + `struct Child{1:required Element value}(unknown_fields="refuse")
` + r
}

func TestNamedConstraintValidation(t *testing.T) {
	source := namedConstraintRecord()
	for _, tc := range []struct{ old, replacement string }{
		{`length="1..8"`, `length="8..1"`},
		{`length="1..8"`, `length="01..8"`},
		{`length="1..8"`, `length=""`},
		{`length="1..8"`, `length="1..9223372036854775808"`},
		{`length="1..8"`, `length="1..1537228672809129301"`},
		{`pattern="no_control"`, `pattern="arbitrary"`},
		{`length="1..8"`, `range="1..8"`},
		{`typedef string Key`, `typedef i64 Key`},
		{`length="1..8"`, `length="1..8",read="rfc3339-wide",write="rfc3339-micros"`},
		{`Key key`, `Key key(length="9..10")`},
	} {
		if _, err := parse(strings.Replace(source, tc.old, tc.replacement, 1)); err == nil {
			t.Fatalf("accepted invalid named constraint %s", tc.replacement)
		}
	}
	s := mustParse(t, strings.Replace(source, `Key key`, `Key key(length="2..3")`, 1))
	schema := jsonSchemaRecords(s)["Record.schema.json"]
	if !strings.Contains(constraintText(s.byName["Record"].Fields[0]), "length 2..3") {
		t.Fatal("field and named bounds were not intersected")
	}
	for _, want := range []string{`"minLength": 1`, `"maxLength": 3`, `"maxLength": 4`, `"items":`, `"propertyNames":`, `"additionalProperties":`} {
		if !strings.Contains(schema, want) {
			t.Fatalf("schema lacks %s:\n%s", want, schema)
		}
	}
	if !strings.Contains(genDocs(s), "length 1..8 UTF-8 bytes; pattern no_control") {
		t.Fatal("named owner absent from documentation")
	}
	intersected := mustParse(t, strings.Replace(source, `Key key`, `Key key(pattern="ascii_name")`, 1))
	if schema := jsonSchemaRecords(intersected)["Record.schema.json"]; !strings.Contains(schema, `"allOf":`) {
		t.Fatal("field and named patterns lost their intersection")
	}
	plain := strings.ReplaceAll(source, `(length="1..4",pattern="no_control")`, "")
	service := `service Readout{Record Read()}(wire_name="example.readout/application@1")`
	if definitionHashes(mustParse(t, plain+service))[0][1] == definitionHashes(mustParse(t, source+service))[0][1] {
		t.Fatal("element constraint absent from hash")
	}
}

// The same scalar owner governs direct values and every collection element.
// Runtime checks below use the existing five-backend readers and writers.
func TestNamedConstraintsFiveBackends(t *testing.T) {
	s := mustParse(t, namedConstraintRecord())
	dir := t.TempDir()
	base := `{"key":"abc","count":5,"tags":["éé","b"],"slots":{"a":"b"},"blob":"AAAA","name":"ab-_","extra":"AAA=","limit":64,"doc":{"a":[1,2]},"small":-3,"child":{"value":"éé"},"maybe":{"value":"ok"}}`
	edit := func(old, value string) string { return strings.Replace(base, old, value, 1) }
	cases := []struct{ input, want string }{
		{base, "accept"},
		{edit(`"key":"abc"`, `"key":"<>&"`), "accept"},
		{edit(`"key":"abc"`, `"key":"\"\\"`), "accept"},
		{edit(`"key":"abc"`, "\"key\":\"\u2028\""), "accept"},
		{edit(`"key":"abc"`, `"key":"éééé"`), "accept"},
		{edit(`"key":"abc"`, `"key":"ééééa"`), "bad_value"},
		{edit(`"key":"abc"`, `"key":""`), "bad_value"},
		{edit(`"éé"`, `"ééa"`), "bad_value"},
		{edit(`"éé"`, `""`), "bad_value"},
		{edit(`"éé"`, `"a\u0085"`), "bad_value"},
		{edit(`"a":"b"`, `"abcde":"b"`), "bad_value"},
		{edit(`"a":"b"`, `"a":"abcde"`), "bad_value"},
		{edit(`"name":"ab-_"`, `"name":""`), "accept"},
		{edit(`"name":"ab-_"`, `"name":"bad space"`), "bad_value"},
		{edit(`"child":{"value":"éé"}`, `"child":{"value":"ééa"}`), "bad_value"},
		{edit(`"maybe":{"value":"ok"}`, `"maybe":{"value":""}`), "bad_value"},
	}
	for i := range cases {
		for _, member := range []string{`"child":{"value":"ééa"}`, `"maybe":{"value":""}`} {
			if at := strings.Index(cases[i].input, member); at >= 0 {
				cases[i].want = "bad_value " + strconv.Itoa(at+len(member))
			}
		}
	}
	writers := map[string]string{
		"main.go":  strings.Replace(constraintGo, `v.Key="abcdefghi"`, `v.Tags[0]="abcde"`, 1),
		"main.py":  strings.Replace(constraintPy, `v.key='abcdefghi'`, `v.tags[0]='abcde'`, 1),
		"main.cpp": strings.Replace(constraintCpp, `v.key="abcdefghi"`, `v.tags[0]="abcde"`, 1),
		"main.rs":  strings.Replace(constraintRust, `v.key="abcdefghi".into()`, `v.tags[0]="abcde".into()`, 1),
		"main.mjs": strings.Replace(constraintJS, `v.key='abcdefghi'`, `v.tags[0]='abcde'`, 1),
	}
	// The Python runner's later wrong-type control starts from a valid element.
	writers["main.py"] = strings.Replace(writers["main.py"], `v.key='abc';v.count=True`, `v.tags[0]='abc';v.count=True`, 1)
	writers["main.py"] = strings.Replace(writers["main.py"], `.splitlines()`, `.split('\n')[:-1]`, 1)
	// Canonical minimal escaping leaves HTML characters, U+2028 and UTF-8
	// multibyte text literal. Quotes and backslashes remain two-byte escapes.
	writers["main.go"] = strings.Replace(writers["main.go"], `if _,e=r.Decode(r.Encode(v));`, `if strings.Contains(string(r.Encode(v)),"\\u"){panic("nonminimal escape")};if _,e=r.Decode(r.Encode(v));`, 1)
	writers["main.py"] = strings.Replace(writers["main.py"], `rec.decode(rec.encode(v));print('accept')`, `rec.decode(rec.encode(v));assert b'\\u' not in rec.encode(v);print('accept')`, 1)
	writers["main.cpp"] = strings.Replace(writers["main.cpp"], `rec::decode(rec::encode(v));`, `if(rec::encode(v).find("\\u")!=std::string::npos)return 3;rec::decode(rec::encode(v));`, 1)
	writers["main.rs"] = strings.Replace(writers["main.rs"], `rec::decode(&rec::encode(&v)).unwrap();`, `assert!(!String::from_utf8(rec::encode(&v)).unwrap().contains("\\u"));rec::decode(&rec::encode(&v)).unwrap();`, 1)
	writers["main.mjs"] = strings.Replace(writers["main.mjs"], `r.decode(r.encode(v));`, `if(new TextDecoder().decode(r.encode(v)).includes('\\u'))throw Error('nonminimal escape');r.decode(r.encode(v));`, 1)
	runConstraintBackendWriters(t, s, dir, cases, writers)
}

func TestNamedConstraintUnannotatedFingerprint(t *testing.T) {
	s := mustParse(t, constraintHead+`typedef string Label
struct Record{1:required Label label 2:required list<Label> labels}(document="true",unknown_fields="refuse")`)
	for _, out := range []struct{ name, text string }{{"go", genGo(s)}, {"cpp", genCpp(s)}, {"python", genPy(s)}, {"rust", genRust(s)}, {"javascript", genJS(s)}, {"docs", genDocs(s)}, {"schema", jsonSchemaRecords(s)["Record.schema.json"]}} {
		t.Logf("unannotated %s %x", out.name, sha256.Sum256([]byte(out.text)))
	}
}

func TestNamedConstraintSizes(t *testing.T) {
	s := mustParse(t, namedConstraintRecord())
	got := s.recordSize(s.byName["Record"], 0, "Record", map[*Struct]bool{})
	if got.open != "" || got.bytes <= 0 {
		t.Fatalf("named elements not bounded: %+v", got)
	}
	plain := mustParse(t, strings.ReplaceAll(namedConstraintRecord(), `(length="1..4",pattern="no_control")`, ""))
	if got := plain.recordSize(plain.byName["Record"], 0, "Record", map[*Struct]bool{}); got.open == "" {
		t.Fatal("unbounded elements became bounded")
	}
	if got := fixedSize(6).times(1537228672809129301).plus(fixedSize(2)); got.bytes != sizeCap {
		t.Fatalf("encoded bound overflowed: %+v", got)
	}
}

func TestNamedConstraintCompatibility(t *testing.T) {
	service := `service Readout{Record Read(1:list<Element> names(length="..2"))}(wire_name="example.readout/application@1")`
	before := namedConstraintRecord() + service
	narrower := strings.Replace(before, `length="1..4"`, `length="1..3"`, 1)
	wantProblem(t, compatProblems(t, before, narrower), `narrowed its declared bounds`)
	wider := strings.Replace(before, `length="1..4"`, `length="1..5"`, 1)
	if got := compatProblems(t, before, wider); len(got) != 0 {
		t.Fatalf("widened named bound: %v", got)
	}
	plain := strings.Replace(before, `(length="1..4",pattern="no_control")`, "", 1)
	wantProblem(t, compatProblems(t, plain, before), `added declared constraints`)
}

func TestNamedConstraintSizesCollectAll(t *testing.T) {
	source := constraintHead + `typedef string Text(length="1..200000")
struct First{1:required Text text}(document="true",unknown_fields="refuse")
struct Valid{1:required bool yes}(unknown_fields="refuse")
struct Last{1:required list<Text> text(length="..2")}(unknown_fields="refuse")`
	file := filepath.Join(t.TempDir(), "large.thrift")
	writeNamespaceFile(t, filepath.Dir(file), "large.thrift", source)
	for _, args := range [][]string{{"--sizes", file}, {file, filepath.Join(filepath.Dir(file), "output"), "go"}} {
		var output bytes.Buffer
		err := run(args, &output)
		if err == nil || !strings.Contains(err.Error(), "record First encodes") || !strings.Contains(err.Error(), "record Last encodes") || strings.Index(err.Error(), "First") > strings.Index(err.Error(), "Last") || strings.Contains(err.Error(), "record Valid") || output.Len() != 0 {
			t.Fatalf("complete refusal before output: %v / %q", err, output.String())
		}
	}
}

func TestNamedConstraintHostArgumentsAtMax(t *testing.T) {
	s := mustParse(t, constraintHead+`typedef string HostArgument(length="1..4096",pattern="no_control")
struct HostRecipe{1:required list<HostArgument> arguments(length="0..64")}(document="true",unknown_fields="refuse")`)
	size := s.recordSize(s.byName["HostRecipe"], 0, "HostRecipe", map[*Struct]bool{})
	if size.open != "" || size.bytes > recordCeiling {
		t.Fatalf("minimal bounded argument frame: %+v", size)
	}
	limit := strconv.FormatInt(size.bytes, 10)
	values := make([]string, 64)
	for i := range values {
		values[i] = strings.Repeat(`\`, 4096)
	}
	input, err := json.Marshal(map[string]any{"arguments": values})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, body := range map[string]string{
		"cases.txt": string(input), "rec/rec.go": genGo(s), "rec.py": genPy(s), "rec.h": genCpp(s), "rec.rs": genRust(s), "rec.mjs": genJS(s), "go.mod": "module bound.test\n\ngo 1.22\n",
		"main.go": `package main
import("fmt";"os";r "bound.test/rec")
func main(){b,e:=os.ReadFile("cases.txt");if e!=nil{panic(e)};v,e:=r.Decode(b);if e!=nil{panic(e)};out:=r.Encode(v);if len(out)>` + limit + `{panic("size ceiling")};fmt.Println("accept")}`,
		"main.py": `import rec
v=rec.decode(open('cases.txt','rb').read())
assert len(rec.encode(v))<=` + limit + `
print('accept')
`,
		"main.cpp": `#include "rec.h"
#include <fstream>
#include <iostream>
#include <iterator>
int main(){std::ifstream f("cases.txt");std::string b((std::istreambuf_iterator<char>(f)),{});auto v=rec::decode(b);if(rec::encode(v).size()>` + limit + `)return 2;std::cout<<"accept\n";}`,
		"main.rs":  `mod rec;fn main(){let b=std::fs::read("cases.txt").unwrap();let v=rec::decode(&b).unwrap();assert!(rec::encode(&v).len()<=` + limit + `);println!("accept");}`,
		"main.mjs": `import * as r from './rec.mjs';import fs from 'node:fs';const v=r.decode(fs.readFileSync('cases.txt'));if(r.encode(v).length>` + limit + `)throw Error('size ceiling');console.log('accept');`,
	} {
		writeNamespaceFile(t, dir, name, body)
	}
	runFiveConstraintPrograms(t, dir, "accept\n")
	s.Encoding.Escape = "other"
	if conservative := s.recordSize(s.byName["HostRecipe"], 0, "HostRecipe", map[*Struct]bool{}); conservative.bytes <= recordCeiling {
		t.Fatal("nonminimal writer received a minimal escape bound")
	}
	f := Field{Checks: []check{{Kind: checkPattern, Class: "ascii_name"}}}
	s.Encoding.Escape = "minimal"
	if got := s.stringSize(f, "", 4); got.bytes != 6 {
		t.Fatalf("closed ASCII ceiling: %+v", got)
	}
}

func TestNamedConstraintsRefusedBeforeTransport(t *testing.T) {
	service := strings.ReplaceAll(boundService, `string request_key(length="1..128",pattern="no_control")`, `RequestKey request_key`)
	s := mustParse(t, constraintHead+`typedef string RequestKey(length="1..128",pattern="no_control")
`+service)
	runBoundServiceBackends(t, s)
}
