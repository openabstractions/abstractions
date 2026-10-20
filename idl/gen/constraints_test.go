package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The refusal block every constraint fixture declares: bad_value after
// missing_field, because a bound is judged once the record is whole.
const constraintHead = `encoding json {
  escape         = "minimal"
  indent         = "2"
  map_keys       = "utf8-bytes"
  numbers        = "integer-decimal"
  opaque         = "verbatim"
  terminator     = "newline"
  duplicate_keys = "refuse"
  depth_limit    = "64"
}
refusal {
   1: malformed        (stage = "grammar")
   2: bad_string       (stage = "grammar")
   3: number_spelling  (stage = "grammar")
   4: wrong_type       (stage = "grammar")
   5: depth_exceeded   (stage = "grammar")
   6: duplicate_key    (stage = "grammar")
   7: duplicate_field  (stage = "structure")
   8: unknown_field    (stage = "structure")
   9: missing_field    (stage = "structure")
  10: bad_binary       (stage = "structure")
  11: bad_value        (stage = "structure")
  12: trailing_bytes   (stage = "document")
}
`

const constraintRecord = `struct Record{
1:required string key(length="1..8",pattern="no_control")
2:required i64 count(range="-5..5")
3:required list<string> tags(length="..2")
4:required map<string,string> slots(length="..1")
5:required binary blob(length="..3")
6:optional string name(omit="zero",length="2..4",pattern="ascii_name")
7:optional binary extra(omit="absent",length="1..2")
8:optional i64 limit(omit="zero",range="1..64")
9:required json doc(length="..16")
10:required i32 small(range="..0")
}(document="true",unknown_fields="refuse")
`

func constraintDefinition(t *testing.T) *Definition {
	t.Helper()
	s, e := parse(constraintHead + constraintRecord)
	if e != nil {
		t.Fatal(e)
	}
	return s
}

func TestASCIILowerTokenConstraintAcrossBackends(t *testing.T) {
	source := strings.Replace(constraintRecord, `pattern="no_control"`, `pattern="ascii_lower_token"`, 1)
	mustParse(t, constraintHead+source)
	if got := jsonSchemaPatterns["ascii_lower_token"]; got != `^[a-z0-9_.:-]*(?![\s\S])` {
		t.Fatal(got)
	}
	for _, language := range []string{"go", "python", "cpp", "rust", "javascript"} {
		var out strings.Builder
		emitPattern(&out, check{Class: "ascii_lower_token", Field: Field{Name: "token", Type: "string"}, Word: boundWord}, language, siteDecode)
		if out.Len() == 0 || !strings.Contains(out.String(), ":") {
			t.Fatalf("%s did not emit the lower token check", language)
		}
	}
}

func TestConstraintValidation(t *testing.T) {
	source := constraintHead + constraintRecord
	for _, tc := range []struct{ old, new, want string }{
		{`length="1..8"`, `length="8..1"`, "min is above max"},
		{`length="1..8"`, `length="-1..8"`, "not negative"},
		{`length="1..8"`, `length="1-8"`, "min..max"},
		{`length="1..8"`, `length=".."`, "min..max"},
		{`length="1..8"`, `length="01..8"`, "canonical"},
		{`range="-5..5"`, `range="-5..9223372036854775808"`, "canonical in-range"},
		{`range="..0"`, `range="..2147483648"`, "canonical in-range"},
		{`2:required i64 count(range="-5..5")`, `2:required bool count(range="-5..5")`, "range bounds"},
		{`8:optional i64 limit(omit="zero",range="1..64")`, `8:optional string limit(omit="zero",range="1..64")`, "range bounds"},
		{`2:required i64 count(range="-5..5")`, `2:required i64 count(length="1..5")`, "length bounds"},
		{`pattern="no_control"`, `pattern="[a-z]+"`, "this profile defines ascii_lower_token, ascii_name, no_control"},
		{`2:required i64 count(range="-5..5")`, `2:required i64 count(pattern="no_control")`, "pattern constrains a string"},
		{`  11: bad_value        (stage = "structure")`, `  11: bad_other        (stage = "structure")`, "declares bad_value"},
	} {
		if !strings.Contains(source, tc.old) {
			t.Fatalf("fixture lost %q", tc.old)
		}
		_, e := parse(strings.Replace(source, tc.old, tc.new, 1))
		if e == nil || !strings.Contains(e.Error(), tc.want) {
			t.Fatalf("%s: want %s got %v", tc.new, tc.want, e)
		}
	}
	s := constraintDefinition(t)
	schema := jsonSchemaRecords(s)["Record.schema.json"]
	for _, want := range []string{`"maxLength": 8`, `"minLength": 1`, `"minimum": -5`, `"maximum": 5`, `"maxItems": 2`, `"maxProperties": 1`, `"pattern": "^[^\\u0000-\\u001f\\u007f-\\u009f]*$"`, `"pattern": "^[A-Za-z0-9_.-]*$"`} {
		if !strings.Contains(schema, want) {
			t.Fatalf("schema lost %s:\n%s", want, schema)
		}
	}
	if strings.Contains(schema, `"minimum": 1`) {
		t.Fatalf("an optional field's zero must pass its schema:\n%s", schema)
	}
	docs := genDocs(s)
	for _, want := range []string{"length ..8 UTF-8 bytes", "length 1..8 UTF-8 bytes; pattern no_control", "range -5..5", "length ..2 entries", "length ..3 bytes", "refusal <code>bad_value</code>"} {
		if !strings.Contains(docs, want) && !strings.Contains(docs, strings.Replace(want, "length ..8", "length 1..8", 1)) {
			t.Fatalf("docs lost %q", want)
		}
	}
}

// refusalOffset is the byte every reader names with word for input, as
// " <offset>", or "" for an accepted input. A bound is judged at the end of
// the record ([DEF-A11]); the other words name the byte that broke a rule.
func refusalOffset(input, word string) string {
	if strings.HasPrefix(word, "bad_value ") {
		return ""
	}
	switch word {
	case "accept":
		return ""
	case "bad_value", "missing_field":
		return " " + strconv.Itoa(len(input)) // just past the record's closing brace
	case "wrong_type":
		return " " + strconv.Itoa(strings.Index(input, `"5"`))
	}
	panic("no offset rule for " + word)
}

// One input, five readers, one verdict per line: every reader and every
// writer refuses a declared bound with bad_value, at the same byte.
func TestConstraintsFiveBackends(t *testing.T) {
	s := constraintDefinition(t)
	dir := t.TempDir()
	base := `{"key":"abc","count":5,"tags":["a","b"],"slots":{"a":"b"},"blob":"AAAA","name":"ab-_","extra":"AAA=","limit":64,"doc":{"a":[1,2]},"small":-3}`
	edit := func(old, new string) string {
		if !strings.Contains(base, old) {
			t.Fatalf("fixture lost %q", old)
		}
		return strings.Replace(base, old, new, 1)
	}
	cases := []struct{ input, want string }{
		{base, "accept"},
		{edit(`"key":"abc"`, `"key":"abcdefghi"`), "bad_value"},
		{edit(`"key":"abc"`, `"key":""`), "bad_value"},
		{edit(`"key":"abc"`, `"key":"a\u0001"`), "bad_value"},
		{edit(`"key":"abc"`, `"key":"a\u0085"`), "bad_value"},
		{edit(`"key":"abc"`, `"key":"a "`), "accept"},
		{edit(`"key":"abc"`, `"key":"éééé"`), "accept"},
		{edit(`"key":"abc"`, `"key":"ééééé"`), "bad_value"},
		{edit(`"count":5`, `"count":6`), "bad_value"},
		{edit(`"count":5`, `"count":-6`), "bad_value"},
		{edit(`"count":5`, `"count":-5`), "accept"},
		{edit(`"tags":["a","b"]`, `"tags":["a","b","c"]`), "bad_value"},
		{edit(`"slots":{"a":"b"}`, `"slots":{"a":"b","c":"d"}`), "bad_value"},
		{edit(`"blob":"AAAA"`, `"blob":"AAAAAA=="`), "bad_value"},
		{edit(`"name":"ab-_"`, `"name":"a"`), "bad_value"},
		{edit(`"name":"ab-_"`, `"name":""`), "accept"},
		{edit(`"name":"ab-_"`, `"name":"a b"`), "bad_value"},
		{edit(`"name":"ab-_"`, `"name":"aé"`), "bad_value"},
		{edit(`"extra":"AAA="`, `"extra":""`), "accept"},
		{edit(`"extra":"AAA="`, `"extra":"AAAA"`), "bad_value"},
		{edit(`"limit":64`, `"limit":0`), "accept"},
		{edit(`"limit":64`, `"limit":65`), "bad_value"},
		{edit(`"doc":{"a":[1,2]}`, `"doc":{"a":[1,2,3,4]}`), "accept"},
		{edit(`"doc":{"a":[1,2]}`, `"doc":{"a":[1,2,3,4,5]}`), "bad_value"},
		// A json value's length counts the bytes as held, whitespace inside it included.
		{edit(`"doc":{"a":[1,2]}`, `"doc":{ "a": [1, 2] }`), "accept"},
		{edit(`"doc":{"a":[1,2]}`, `"doc":{"a":[1,2,3]}`), "accept"},
		{edit(`"doc":{"a":[1,2]}`, `"doc":{ "a": [1, 2, 3] }`), "bad_value"},
		{edit(`"small":-3`, `"small":1`), "bad_value"},
		{edit(`"count":5`, `"count":"5"`), "wrong_type"},
		{edit(`,"small":-3`, ``), "missing_field"},
	}
	runConstraintBackends(t, s, dir, cases)
}

func runConstraintBackends(t *testing.T, s *Definition, dir string, cases []struct{ input, want string }) {
	runConstraintBackendWriters(t, s, dir, cases, map[string]string{"main.go": constraintGo, "main.py": constraintPy, "main.cpp": constraintCpp, "main.rs": constraintRust, "main.mjs": constraintJS})
}

func runConstraintBackendWriters(t *testing.T, s *Definition, dir string, cases []struct{ input, want string }, writers map[string]string) {
	t.Helper()
	var inputs, wants []string
	for _, c := range cases {
		inputs = append(inputs, c.input)
		wants = append(wants, c.want+refusalOffset(c.input, c.want))
	}
	// The last line is the encoder's verdict on a key one byte past its bound,
	// at offset zero: a writer has no input byte ([DEF-A11]).
	want := strings.Join(wants, "\n") + "\nbad_value 0\n"
	for n, b := range map[string]string{"cases.txt": strings.Join(inputs, "\n") + "\n", "rec/rec.go": genGo(s), "rec.py": genPy(s), "rec.h": genCpp(s), "rec.rs": genRust(s), "rec.mjs": genJS(s), "go.mod": "module bound.test\n\ngo 1.22\n"} {
		writeNamespaceFile(t, dir, n, b)
	}
	for n, b := range writers {
		writeNamespaceFile(t, dir, n, b)
	}
	runFiveConstraintPrograms(t, dir, want)
}

func runFiveConstraintPrograms(t *testing.T, dir, want string) {
	t.Helper()
	run := func(t *testing.T, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("%s: %v\n%s", name, e, out)
		}
		if got := strings.ReplaceAll(string(out), "\r\n", "\n"); got != want {
			t.Fatalf("%s wrong verdicts:\n%s\nwant:\n%s", name, got, want)
		}
	}
	compile := func(t *testing.T, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("%s: %v\n%s", name, e, out)
		}
	}
	t.Run("go", func(t *testing.T) { run(t, "go", "run", "main.go") })
	t.Run("python", func(t *testing.T) { run(t, servicePython(t), "-B", "main.py") })
	t.Run("javascript", func(t *testing.T) {
		p, e := exec.LookPath("node")
		if e != nil {
			t.Skip("Node unavailable")
		}
		run(t, p, "main.mjs")
	})
	t.Run("rust", func(t *testing.T) {
		p, e := exec.LookPath("rustc")
		if e != nil {
			t.Skip("Rust unavailable")
		}
		exe := filepath.Join(dir, "rust.exe")
		compile(t, p, "--edition=2021", "main.rs", "-o", exe)
		run(t, exe)
	})
	t.Run("cpp", func(t *testing.T) {
		p := os.Getenv("CXX")
		if p == "" {
			p, _ = exec.LookPath("g++")
		}
		if p == "" {
			t.Skip("C++ requires CXX or g++")
		}
		exe := filepath.Join(dir, "cpp.exe")
		args := []string{"-std=c++17", "main.cpp", "-o", exe}
		if n := strings.ToLower(filepath.Base(p)); n == "cl" || n == "cl.exe" {
			args = []string{"/nologo", "/EHsc", "/std:c++17", "main.cpp", "/Fe:" + exe}
		}
		compile(t, p, args...)
		run(t, exe)
	})
}

// [DEF-V1]: a declared bound joins the hash; a bound a published member
// lacked is narrowing, a widened one is accepted, a narrowed one is a new
// version.
func TestConstraintCompatibility(t *testing.T) {
	published := strings.Replace(compatFixture, " 11: trailing_bytes  (stage = \"document\")", " 11: bad_value       (stage = \"structure\")\n 12: trailing_bytes  (stage = \"document\")", 1)
	if published == compatFixture {
		t.Fatal("fixture edit did not apply")
	}
	bound := func(src, path, id string) string {
		out := strings.Replace(src, "  2: required string path\n", "  2: required string path"+path+"\n", 1)
		return strings.Replace(out, "Read(1: string id)", "Read(1: string id"+id+")", 1)
	}
	declared := bound(published, `(length = "1..64", pattern = "no_control")`, `(length = "1..128")`)
	wantProblem(t, compatProblems(t, published, declared), `added declared constraints`)
	before, after := definitionHashes(mustParse(t, published)), definitionHashes(mustParse(t, declared))
	if before[0][1] == after[0][1] {
		t.Fatal("a declared bound left the definition hash unchanged")
	}
	if got := definitionHashes(mustParse(t, compatFixture)); got[0][1] != definitionHashes(mustParse(t, published))[0][1] {
		t.Fatal("declaring bad_value alone changed the hash")
	}
	wider := bound(published, `(length = "..65", pattern = "no_control")`, `(length = "0..200")`)
	if problems := compatProblems(t, declared, wider); len(problems) != 0 {
		t.Fatalf("a widened bound is accepted: %q", problems)
	}
	member := bound(published, `(length = "2..64", pattern = "no_control")`, `(length = "1..128")`)
	wantProblem(t, compatProblems(t, declared, member), `struct Origin member path narrowed its declared bounds`)
	argument := bound(published, `(length = "1..64", pattern = "no_control")`, `(length = "1..127")`)
	wantProblem(t, compatProblems(t, declared, argument), `argument Read\.id narrowed its declared bounds`)
	class := bound(published, `(length = "1..64", pattern = "ascii_name")`, `(length = "1..128")`)
	wantProblem(t, compatProblems(t, declared, class), `member path changed its declared pattern`)
}

const boundService = `struct Question{
1:required string request_key(length="1..128",pattern="no_control")
2:required string key
}(document="true",unknown_fields="refuse")
enum Outcome{1:pending 2:invalid}(unknown="refuse")
struct Observation{1:required Outcome outcome}(unknown_fields="refuse")
service Asks{
Observation Ask(1:Question question)
Observation Observe(1:string request_key(length="1..128",pattern="no_control"),2:i64 wait_ms(range="0..30000"))
}(wire_name="example.asks/application@1")
`

// [IPC-A6]: a declared bound is refused by every generated client before its
// transport is called, with the same word, and by the receiver before
// dispatch. The 129-byte request key is the plan's falsifier.
func TestConstraintsRefusedBeforeTransport(t *testing.T) {
	s, e := parse(constraintHead + boundService)
	if e != nil {
		t.Fatal(e)
	}
	runBoundServiceBackends(t, s)
}

// The ranked refusal vocabulary is codec-private. Exercise actual unchanged
// published record declarations with the old and additive vocabularies, including
// simultaneous structure/document violations; new paths alone use bad_value.
func TestAdditiveConstraintRefusalPreservesOldRecordVerdicts(t *testing.T) {
	for _, tc := range []struct{ schema, record, valid, newCheck string }{
		{"abstraction-router/router.thrift", "HostAllowance", `{"hosts":[]}`, `var page current.ModalityPage; if e:=current.FromJSON([]byte("{\"outcome\":\"page\",\"modalities\":[\"\"],\"next\":\"\",\"complete\":true}"),&page);word(e)!="bad_value"{t.Fatalf("new profile bound: %v",e)}`},
		{"abstraction-inference/inference.thrift", "Usage", `{"input":0,"output":0,"cached":0}`, `f:=&fake{};_,e:=current.NewChatV2Client(f).StartWithGuarantees(current.RequestV2{},[]current.RequestGuarantee{current.RequestGuaranteeLocalOnly,current.RequestGuaranteeLocalOnly});if word(e)!="bad_value"||f.calls!=0{t.Fatalf("new bound dispatched: %v %d",e,f.calls)}`},
	} {
		t.Run(tc.record, func(t *testing.T) {
			def, err := loadDefinition(productionFile(t, tc.schema))
			if err != nil {
				t.Fatal(err)
			}
			if tc.schema == "abstraction-inference/inference.thrift" {
				// This dependency-free fixture exercises published Usage and the
				// ChatV2 bound below. Project that closed reachable surface with
				// the existing selection owner; later versions have real imports.
				only := []string{tc.record, "ChatV2"}
				for i := 0; i < len(only); i++ {
					for _, name := range needs(def, only[i]) {
						if !contains(only, name) {
							only = append(only, name)
						}
					}
				}
				def, err = selected(def, only)
				if err != nil {
					t.Fatal(err)
				}
			}
			legacy := *def
			legacy.Refusals = nil
			for _, r := range def.Refusals {
				if r.Word != "bad_value" {
					r.ID = len(legacy.Refusals) + 1
					legacy.Refusals = append(legacy.Refusals, r)
				}
			}
			if len(legacy.Refusals)+1 != len(def.Refusals) {
				t.Fatal("expected one additive refusal")
			}
			dir := t.TempDir()
			for name, body := range map[string]string{"go.mod": "module refusal.test\n\ngo 1.22\n", "old/rec.go": genGo(&legacy), "current/rec.go": genGo(def)} {
				path := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			corpus := []string{tc.valid, "{", "{}", "null", strings.TrimSuffix(tc.valid, "}") + `,"extra":1}`, strings.Replace(tc.valid, "0", "1.0", 1), strings.TrimSuffix(tc.valid, "}") + `,"hosts":[]}`}
			program := `package check
import("errors";"fmt";"testing";old "refusal.test/old";current "refusal.test/current")
func word(e error)string{var f *current.Refusal;if errors.As(e,&f){return f.Word};if e==nil{return "accept"};return e.Error()}
type fake struct{calls int};func(f *fake)ExchangeFrame([]byte)([]byte,error){f.calls++;return nil,errors.New("transport")}
func TestCorpus(t *testing.T){`
			for _, raw := range corpus {
				for _, suffix := range []string{"", " trailing"} {
					program += fmt.Sprintf("{data:=[]byte(%q);var a old.%s;var b current.%s;x,y:=old.FromJSON(data,&a),current.FromJSON(data,&b);if fmt.Sprint(x)!=fmt.Sprint(y){t.Fatalf(\"old verdict changed: %%s: %%v / %%v\",data,x,y)}}\n", raw+suffix, tc.record, tc.record)
				}
			}
			program += tc.newCheck + "}\n"
			if err := os.WriteFile(filepath.Join(dir, "corpus_test.go"), []byte(program), 0600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("go", "test", ".")
			command.Dir = dir
			command.Env = append(os.Environ(), "GOWORK=off")
			if out, err := command.CombinedOutput(); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
		})
	}
}

// CompatRestore1492 uses the actual owning definition. Legacy record codecs
// retain their language; real legacy handler invalid/no-effect controls live
// in credentials/go/service. Both published service generations retain that codec language.
func TestCredentialsPublishedInputsAllBackends(t *testing.T) {
	raw, err := os.ReadFile(privateInput(t, "..", "..", "openabstractions-flat", "abstraction-credentials", "credentials.thrift"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	s.NamedCodecs = true
	encode := func(v any) string {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	scope := func(n, m int) map[string]any {
		targets, consumers := []string{}, []string{}
		for i := 0; i < n; i++ {
			targets = append(targets, fmt.Sprintf("host%d.example", i))
		}
		for i := 0; i < m; i++ {
			consumers = append(consumers, fmt.Sprintf("fixture/consumer%d@1", i))
		}
		return map[string]any{"targets": targets, "consumers": consumers}
	}
	registration := func(name string, secret, n, m int) string {
		return encode(map[string]any{"name": name, "kind": "bearer", "scope": scope(n, m), "secret": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", secret)))})
	}
	rotation := func(name string, secret int) string {
		return encode(map[string]any{"name": name, "secret": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", secret)))})
	}
	use := func(name string, challenge int) string {
		return encode(map[string]any{"subject": map[string]string{"account": "fixture", "program": "/fixture"}, "consumer": "fixture/consumer@1", "name": name, "target": "example.com", "challenge": strings.Repeat("a", challenge)})
	}
	type codecCase struct {
		kind, modern, raw string
	}
	cases := []codecCase{
		{"Registration", "Registration", registration("!", 1, 1, 1)},
		{"Registration", "Registration", registration("alpha", 4097, 1, 1)},
		{"Registration", "Registration", registration("alpha", 1, 17, 1)},
		{"Registration", "Registration", registration("alpha", 1, 1, 9)},
		{"Rotation", "Rotation", rotation("!", 1)},
		{"Rotation", "Rotation", rotation("alpha", 4097)},
		{"Use", "Use", use("!", 0)},
		{"Use", "Use", use("alpha", 4097)},
		{"Registration", "Registration", registration(strings.Repeat("a", 64), 4096, 16, 8)},
		{"Registration", "Registration", registration("a", 1, 1, 1)},
		{"Rotation", "Rotation", rotation(strings.Repeat("a", 64), 4096)},
		{"Use", "Use", use(strings.Repeat("a", 64), 4096)},
	}
	variant := func(raw string, change func(map[string]any)) string {
		var v map[string]any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatal(err)
		}
		change(v)
		return encode(v)
	}
	for _, n := range []int{0, 254} {
		cases = append(cases, codecCase{"Registration", "Registration", variant(registration("a", 1, 1, 1), func(v map[string]any) { v["scope"].(map[string]any)["targets"] = []string{strings.Repeat("a", n)} })}, codecCase{"Use", "Use", variant(use("a", 1), func(v map[string]any) { v["target"] = strings.Repeat("a", n) })})
	}
	for _, n := range []int{0, 135} {
		cases = append(cases, codecCase{"Registration", "Registration", variant(registration("a", 1, 1, 1), func(v map[string]any) { v["scope"].(map[string]any)["consumers"] = []string{strings.Repeat("a", n)} })}, codecCase{"Use", "Use", variant(use("a", 1), func(v map[string]any) { v["consumer"] = strings.Repeat("a", n) })}, codecCase{"Registration", "Registration", variant(registration("a", 1, 1, 1), func(v map[string]any) { v["kind"] = strings.Repeat("a", n) })})
	}
	cases = append(cases, codecCase{"Registration", "Registration", variant(registration("a", 1, 1, 1), func(v map[string]any) { v["header"] = strings.Repeat("a", 65) })})
	for _, n := range []int{0, 64} {
		cases = append(cases, codecCase{"Registration", "Registration", variant(registration("a", 1, 1, 1), func(v map[string]any) {
			v["header"] = strings.Repeat("a", n)
			v["kind"] = strings.Repeat("a", 134)
			v["scope"].(map[string]any)["targets"] = []string{strings.Repeat("a", 253)}
			v["scope"].(map[string]any)["consumers"] = []string{strings.Repeat("a", 134)}
		})})
	}
	cases = append(cases, codecCase{"Use", "Use", variant(use("a", 1), func(v map[string]any) {
		v["target"] = strings.Repeat("a", 253)
		v["consumer"] = strings.Repeat("a", 134)
	})})
	programs := map[string]string{
		"main.go": `package main
import("errors";"fmt";"strings";r "svc.test/rec")
func word(e error)string{var f *r.Refusal;if errors.As(e,&f){return f.Word};if e==nil{return "accept"};return "transport"}
type fake struct{calls int};func(f *fake)ExchangeFrame([]byte)([]byte,error){f.calls++;return nil,errors.New("transport")}
func main(){`,
		"main.py": `import rec as r
def word(f):
 try:f();return 'accept'
 except r.Refusal as e:return e.word
 except RuntimeError:return 'transport'
class Fake:
 calls=0
 def exchange_frame(self,b):self.calls+=1;raise RuntimeError('transport')
`,
		"main.mjs": `import * as r from './rec.mjs';const enc=new TextEncoder();
async function word(f){try{await f();return 'accept'}catch(e){return e instanceof r.Refusal?e.word:'transport'}}
`,
		"main.cpp": `#include "rec.h"
#include <iostream>
#include <stdexcept>
namespace r=rec;
template<class F>std::string word(F f){try{f();return "accept";}catch(const r::Refusal&e){return e.word;}catch(const std::runtime_error&){return "transport";}}
struct Fake{int calls=0;std::string exchange_frame(std::string_view){++calls;throw std::runtime_error("transport");}};
int main(){`,
		"main.rs": `mod rec;use rec::{JsonRecord,ScopedHolder};use std::sync::atomic::{AtomicUsize,Ordering};
static CALLS:AtomicUsize=AtomicUsize::new(0);struct Fake;impl rec::FrameTransport for Fake{type Error=();fn write_frame(&self,_:&[u8])->Result<(),()>{Ok(())}fn exchange_frame(&self,_:&[u8])->Result<Vec<u8>,()>{CALLS.fetch_add(1,Ordering::SeqCst);Err(())}}
fn word<T>(v:Result<T,rec::CallError<()>>)->String{match v{Ok(_)=>"accept".into(),Err(rec::CallError::Refusal(e))=>e.word.into(),_=>"transport".into()}}
fn main(){std::panic::set_hook(Box::new(|_|{}));`,
	}
	want := ""
	for _, c := range cases {
		outcome := "accept" // Published codec language includes semantically invalid values.
		want += "legacy " + outcome + " " + outcome + "\n"
		goProjection := "old"
		programs["main.go"] += fmt.Sprintf("{data:=[]byte(%q);var old r.%s;if e:=r.FromJSON(data,&old);e!=nil{panic(e)};if _,e:=r.ToJSON(&old);e!=nil{panic(e)};var current r.%s;read:=word(r.FromJSON(data,&current));current=%s;_,e:=r.ToJSON(&current);fmt.Println(\"legacy\",read,word(e))}\n", c.raw, c.kind, c.modern, goProjection)
		pyProjection := "old"
		programs["main.py"] += fmt.Sprintf("data=%q.encode();old=r.from_json(r.%s,data);r.to_json(old);current=%s\nprint('legacy',word(lambda:r.from_json(r.%s,data)),word(lambda:r.to_json(current)))\n", c.raw, c.kind, pyProjection, c.modern)
		programs["main.mjs"] += fmt.Sprintf("{const data=enc.encode(%q);const old=r.fromJson(%q,data);r.toJson(%q,old);console.log('legacy',await word(()=>r.fromJson(%q,data)),await word(()=>r.toJson(%q,old)));}\n", c.raw, c.kind, c.kind, c.modern, c.modern)
		cppProjection := "old"
		programs["main.cpp"] += fmt.Sprintf("{std::string data=R\"case(%s)case\";auto old=r::from_json<r::%s>(data);r::to_json(old);auto current=%s;std::cout<<\"legacy \"<<word([&]{r::from_json<r::%s>(data);})<<' '<<word([&]{r::to_json(current);})<<'\\n';}\n", c.raw, c.kind, cppProjection, c.modern)
		rustProjection := "old"
		programs["main.rs"] += fmt.Sprintf("{let data=br#\"%s\"#;let old=rec::%s::from_json(data).unwrap();old.to_json();let read=match rec::%s::from_json(data){Ok(_)=>\"accept\",Err(e)=>e.word};let current=%s;let write=match std::panic::catch_unwind(||current.to_json()){Ok(_)=>\"accept\",Err(e)=>e.downcast_ref::<rec::Refusal>().unwrap().word};println!(\"legacy {} {}\",read,write);}\n", c.raw, c.kind, c.modern, rustProjection)
	}
	want += "transport 1\ntransport 2\ntransport 3\ntransport 4\ntransport 5\n"
	programs["main.go"] += `f:=&fake{};c:=r.NewScopedHolderClient(f);_,e:=c.Store(strings.Repeat("r",129),r.Registration{});fmt.Println(word(e),f.calls);_,e=c.Rotate("",r.Rotation{});fmt.Println(word(e),f.calls);_,e=c.Revoke("","alpha");fmt.Println(word(e),f.calls);_,e=c.Revoke("r","!");fmt.Println(word(e),f.calls);_,e=c.Revoke(strings.Repeat("r",128),strings.Repeat("a",64));fmt.Println(word(e),f.calls)}`
	programs["main.py"] += `f=Fake();c=r.ScopedHolderClient(f)
print(word(lambda:c.store('r'*129,r.Registration())),f.calls)
print(word(lambda:c.rotate('',r.Rotation())),f.calls)
print(word(lambda:c.revoke('','alpha')),f.calls)
print(word(lambda:c.revoke('r','!')),f.calls)
print(word(lambda:c.revoke('r'*128,'a'*64)),f.calls)
`
	programs["main.mjs"] += `const f={calls:0,async exchangeFrame(){this.calls++;throw Error('transport')}};const c=new r.ScopedHolderClient(f);
console.log(await word(()=>c.store('r'.repeat(129),{name:'alpha',kind:'bearer',scope:{targets:['example.com'],consumers:['fixture/use@1']},expires:'',header:'',secret:new Uint8Array([97])})),f.calls);console.log(await word(()=>c.rotate('',{name:'alpha',expires:'',secret:new Uint8Array([97])})),f.calls);console.log(await word(()=>c.revoke('','alpha')),f.calls);console.log(await word(()=>c.revoke('r','!')),f.calls);console.log(await word(()=>c.revoke('r'.repeat(128),'a'.repeat(64))),f.calls);
`
	programs["main.cpp"] += `Fake f;r::ScopedHolderClient<Fake> c(f);std::cout<<word([&]{c.store(std::string(129,'r'),r::Registration{});})<<' '<<f.calls<<'\n';std::cout<<word([&]{c.rotate("",r::Rotation{});})<<' '<<f.calls<<'\n';std::cout<<word([&]{c.revoke("","alpha");})<<' '<<f.calls<<'\n';std::cout<<word([&]{c.revoke("r","!");})<<' '<<f.calls<<'\n';std::cout<<word([&]{c.revoke(std::string(128,'r'),std::string(64,'a'));})<<' '<<f.calls<<'\n';}`
	programs["main.rs"] += `let c=rec::ScopedHolderClient::new(Fake);println!("{} {}",word(c.store("r".repeat(129),rec::Registration::default())),CALLS.load(Ordering::SeqCst));println!("{} {}",word(c.rotate("".into(),rec::Rotation::default())),CALLS.load(Ordering::SeqCst));println!("{} {}",word(c.revoke("".into(),"alpha".into())),CALLS.load(Ordering::SeqCst));println!("{} {}",word(c.revoke("r".into(),"!".into())),CALLS.load(Ordering::SeqCst));println!("{} {}",word(c.revoke("r".repeat(128),"a".repeat(64))),CALLS.load(Ordering::SeqCst));}`
	runBoundServicePrograms(t, s, programs, want, "")
}

func runBoundServiceBackends(t *testing.T, s *Definition) {
	runBoundServicePrograms(t, s, map[string]string{"main.go": boundClientGo, "main.py": boundClientPy, "main.cpp": boundClientCpp, "main.rs": boundClientRust, "main.mjs": boundClientJS}, "bad_value 0\nbad_value 0\nbad_value 0\naccept 1\n", "receiver bad_value\n")
}

func runBoundServicePrograms(t *testing.T, s *Definition, programs map[string]string, want, goSuffix string) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{"go.mod": "module svc.test\n\ngo 1.22\n"}
	for _, backend := range backends {
		name := map[string]string{"go": "rec/rec.go", "python": "rec.py", "cpp": "rec.h", "rust": "rec.rs", "javascript": "rec.mjs"}[backend.lang]
		if name != "" {
			files[name] = mustEmitIncluded(t, backend, s)
		}
	}
	for name, body := range programs {
		files[name] = body
	}
	for n, b := range files {
		writeNamespaceFile(t, dir, n, b)
	}
	run := func(t *testing.T, want, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("%s: %v\n%s", name, e, out)
		}
		if got := strings.ReplaceAll(string(out), "\r\n", "\n"); got != want {
			t.Fatalf("%s:\n%s\nwant:\n%s", name, got, want)
		}
	}
	compile := func(t *testing.T, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("%s: %v\n%s", name, e, out)
		}
	}
	t.Run("go", func(t *testing.T) { run(t, want+goSuffix, "go", "run", "main.go") })
	t.Run("python", func(t *testing.T) { run(t, want, servicePython(t), "-B", "main.py") })
	t.Run("javascript", func(t *testing.T) {
		p, e := exec.LookPath("node")
		if e != nil {
			t.Skip("Node unavailable")
		}
		run(t, want, p, "main.mjs")
	})
	t.Run("rust", func(t *testing.T) {
		exe := filepath.Join(dir, "rust.exe")
		compile(t, rustServiceCompiler(t), "--edition=2021", "main.rs", "-o", exe)
		run(t, want, exe)
	})
	t.Run("cpp", func(t *testing.T) {
		p := os.Getenv("CXX")
		if p == "" {
			p, _ = exec.LookPath("g++")
		}
		if p == "" {
			t.Skip("C++ requires CXX or g++")
		}
		exe := filepath.Join(dir, "cpp.exe")
		args := []string{"-std=c++17", "main.cpp", "-o", exe}
		if n := strings.ToLower(filepath.Base(p)); n == "cl" || n == "cl.exe" {
			args = []string{"/nologo", "/EHsc", "/std:c++17", "main.cpp", "/Fe:" + exe}
		}
		compile(t, p, args...)
		run(t, want, exe)
	})
}

const boundReply = `{"version":1,"service":"example.asks/application@1","method":"Observe","ok":true,"payload":{"value":{"outcome":"pending"}}}`

const boundClientGo = `package main
import("errors";"fmt";"strings";r "svc.test/rec")
type fake struct{calls int}
func(f *fake)ExchangeFrame(b []byte)([]byte,error){f.calls++;return []byte(` + "`" + boundReply + "`" + `),nil}
type handler struct{}
func(handler)Ask(r.Question)(r.Observation,error){return r.Observation{Outcome:r.OutcomePending},nil}
func(handler)Observe(string,int64)(r.Observation,error){return r.Observation{Outcome:r.OutcomePending},nil}
func word(e error)string{var x *r.Refusal;if errors.As(e,&x){return x.Word};if e==nil{return "accept"};return e.Error()}
func main(){f:=&fake{};c:=r.NewAsksClient(f)
_,e:=c.Observe(strings.Repeat("k",129),0);fmt.Println(word(e),f.calls)
_,e=c.Observe("k",30001);fmt.Println(word(e),f.calls)
_,e=c.Ask(r.Question{RequestKey:strings.Repeat("k",129),Key:"x"});fmt.Println(word(e),f.calls)
_,e=c.Observe(strings.Repeat("k",128),30000);fmt.Println(word(e),f.calls)
d:=r.AsksDispatcher{Handler:handler{}};out,e:=d.ExchangeFrame([]byte(` + "`" + `{"version":1,"service":"example.asks/application@1","method":"Observe","arguments":{"request_key":"` + "`" + `+strings.Repeat("k",129)+` + "`" + `","wait_ms":0}}` + "`" + `))
if e!=nil||!strings.Contains(string(out),"bad_value")||strings.Contains(string(out),"pending"){panic(string(out))};fmt.Println("receiver bad_value")}
`
const boundClientPy = `import rec as r
class T:
 calls=0
 def exchange_frame(self,b):
  self.calls+=1
  return b'` + boundReply + `'
t=T();c=r.AsksClient(t)
def w(f):
 try:f();return 'accept'
 except r.Refusal as e:return e.word
print(w(lambda:c.observe('k'*129,0)),t.calls)
print(w(lambda:c.observe('k',30001)),t.calls)
print(w(lambda:c.ask(r.Question(request_key='k'*129,key='x'))),t.calls)
print(w(lambda:c.observe('k'*128,30000)),t.calls)
`
const boundClientJS = `import * as r from './rec.mjs';
const t={calls:0,async exchangeFrame(){this.calls++;return new TextEncoder().encode('` + boundReply + `')}};
const c=new r.AsksClient(t);
async function w(f){try{await f();return 'accept'}catch(e){if(!(e instanceof r.Refusal))throw e;return e.word}}
console.log(await w(()=>c.observe('k'.repeat(129),0n)),t.calls);
console.log(await w(()=>c.observe('k',30001n)),t.calls);
console.log(await w(()=>c.ask({requestKey:'k'.repeat(129),key:'x'})),t.calls);
console.log(await w(()=>c.observe('k'.repeat(128),30000n)),t.calls);
`
const boundClientRust = `mod rec;
use rec::{Asks,FrameTransport,CallError};
use std::sync::atomic::{AtomicUsize,Ordering};
static CALLS:AtomicUsize=AtomicUsize::new(0);
struct T;
impl FrameTransport for T{type Error=();fn write_frame(&self,_:&[u8])->Result<(),()>{Ok(())}fn exchange_frame(&self,_:&[u8])->Result<Vec<u8>,()>{CALLS.fetch_add(1,Ordering::SeqCst);Ok(br#"` + boundReply + `"#.to_vec())}}
fn w<V>(r:Result<V,CallError<()>>)->String{match r{Ok(_)=>"accept".into(),Err(CallError::Refusal(x))=>x.word.into(),Err(e)=>format!("{:?}",e)}}
fn main(){let c=rec::AsksClient::new(T);
println!("{} {}",w(c.observe("k".repeat(129),0)),CALLS.load(Ordering::SeqCst));
println!("{} {}",w(c.observe("k".into(),30001)),CALLS.load(Ordering::SeqCst));
println!("{} {}",w(c.ask(rec::Question{request_key:"k".repeat(129),key:"x".into()})),CALLS.load(Ordering::SeqCst));
println!("{} {}",w(c.observe("k".repeat(128),30000)),CALLS.load(Ordering::SeqCst));}
`
const boundClientCpp = `#include "rec.h"
#include <iostream>
struct T{int calls=0;std::string exchange_frame(std::string_view){++calls;return R"(` + boundReply + `)";}};
template<class F>std::string w(F f){try{f();return "accept";}catch(const rec::Refusal&e){return e.word;}}
int main(){T t;rec::AsksClient<T> c(t);
std::cout<<w([&]{c.observe(std::string(129,'k'),0);})<<' '<<t.calls<<'\n';
std::cout<<w([&]{c.observe("k",30001);})<<' '<<t.calls<<'\n';
rec::Question q;q.request_key=std::string(129,'k');q.key="x";
std::cout<<w([&]{c.ask(q);})<<' '<<t.calls<<'\n';
std::cout<<w([&]{c.observe(std::string(128,'k'),30000);})<<' '<<t.calls<<'\n';}
`

const constraintGo = `package main
import("fmt";"os";"strings";r "bound.test/rec")
func main(){b,_:=os.ReadFile("cases.txt");lines:=strings.Split(strings.TrimSpace(string(b)),"\n");for _,line:=range lines{v,e:=r.Decode([]byte(line));if e!=nil{f:=e.(*r.Refusal);fmt.Println(f.Word,f.Offset)}else{if _,e=r.Decode(r.Encode(v));e!=nil{panic(e)};fmt.Println("accept")}};v,_:=r.Decode([]byte(lines[0]));v.Key="abcdefghi";defer func(){p:=recover();if p==nil{panic("encoder accepted")};f:=p.(*r.Refusal);fmt.Println(f.Word,f.Offset)}();r.Encode(v)}
`
const constraintPy = `import rec
lines=open('cases.txt',encoding='utf-8').read().splitlines()
for line in lines:
 try:
  v=rec.decode(line.encode());rec.decode(rec.encode(v));print('accept')
 except rec.Refusal as e:print(e.word,e.offset)
v=rec.decode(lines[0].encode());v.key='abcdefghi'
try:rec.encode(v);raise AssertionError('encoder accepted')
except rec.Refusal as e:print(e.word,e.offset)
v.key='abc';v.count=True
try:rec.encode(v);raise AssertionError('bool accepted as integer')
except rec.Refusal as e:assert e.word=='wrong_type',e.word
`
const constraintJS = `import * as r from './rec.mjs';import fs from 'node:fs';
const lines=fs.readFileSync('cases.txt','utf8').trim().split('\n');
for(const line of lines){try{const v=r.decode(Buffer.from(line));r.decode(r.encode(v));console.log('accept')}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word+' '+e.offset)}}
const v=r.decode(Buffer.from(lines[0]));v.key='abcdefghi';try{r.encode(v);throw Error('encoder accepted')}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word+' '+e.offset)}
`
const constraintCpp = `#include "rec.h"
#include <fstream>
#include <iostream>
int main(){std::ifstream f("cases.txt");std::string line,first;while(std::getline(f,line)){if(first.empty())first=line;try{auto v=rec::decode(line);rec::decode(rec::encode(v));std::cout<<"accept\n";}catch(const rec::Refusal&e){std::cout<<e.word<<' '<<e.offset<<'\n';}}auto v=rec::decode(first);v.key="abcdefghi";try{rec::encode(v);return 2;}catch(const rec::Refusal&e){std::cout<<e.word<<' '<<e.offset<<'\n';}}
`
const constraintRust = `mod rec;
fn main(){std::panic::set_hook(Box::new(|_|{}));let text=std::fs::read_to_string("cases.txt").unwrap();for line in text.lines(){match rec::decode(line.as_bytes()){Ok(v)=>{rec::decode(&rec::encode(&v)).unwrap();println!("accept")},Err(e)=>println!("{} {}",e.word,e.offset)}}let mut v=rec::decode(text.lines().next().unwrap().as_bytes()).unwrap();v.key="abcdefghi".into();let err=std::panic::catch_unwind(||rec::encode(&v)).expect_err("encoder accepted");let f=err.downcast_ref::<rec::Refusal>().unwrap();println!("{} {}",f.word,f.offset);}
`
