package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func importedAliasFixture(t *testing.T, body string) (*Definition, error) {
	t.Helper()
	dir := t.TempDir()
	owner := constraintHead + `namespace * alias.owner
typedef string timestamp(write="rfc3339-micros",read="rfc3339-wide")
typedef string Label(length="1..8",pattern="no_control")
typedef i64 Count
struct Owner{1:required timestamp time}(document="true",unknown_fields="refuse")`
	writeNamespaceFile(t, dir, "owner.thrift", owner)
	writeNamespaceFile(t, dir, "use.thrift", constraintHead+`namespace * alias.use
include "owner.thrift"
`+body)
	return loadDefinition(filepath.Join(dir, "use.thrift"))
}

func TestImportedScalarAliasIR(t *testing.T) {
	s, err := importedAliasFixture(t, `struct Record{
1:required owner.timestamp time
2:required owner.Label label(length="2..4")
3:required list<owner.Label> labels
4:required map<owner.Label,owner.Label> slots
5:required owner.Count count
}(document="true",unknown_fields="refuse")
service Clock{owner.timestamp Read(1:owner.timestamp at)}(wire_name="example.clock/read@1")`)
	if err != nil {
		t.Fatal(err)
	}
	f := s.Structs[0].Fields[0]
	if f.Type != "string" || f.Alias != "owner.timestamp" || f.TypeAliases[""] != "owner.timestamp" ||
		f.Grammar != (Grammar{Read: "rfc3339-wide", Write: "rfc3339-micros"}) {
		t.Fatalf("lost alias identity/grammar: %+v", f)
	}
	if !strings.Contains(constraintText(s.Structs[0].Fields[1]), "length 2..4") {
		t.Fatal("imported bounds not intersected")
	}
	for _, n := range []int{2, 3} {
		if len(s.Structs[0].Fields[n].Checks) == 0 {
			t.Fatal("lost collection alias constraints")
		}
	}
	m := s.Services[0].Methods[0]
	if m.Result.Grammar != f.Grammar || m.Args[0].Grammar != f.Grammar {
		t.Fatal("service alias grammar lost")
	}
	if len(s.Typedefs) != 0 || s.Imports[0].Def.Typedefs[0].Alias != "timestamp" {
		t.Fatal("import mutated canonical declarations")
	}
}

func TestImportedScalarAliasEmissionParity(t *testing.T) {
	body := `struct Record{1:required owner.timestamp time 2:required owner.Label label 3:required list<owner.Label> labels 4:required map<owner.Label,owner.Label> slots}(document="true",unknown_fields="refuse")
service Clock{owner.timestamp Read(1:owner.timestamp at)}(wire_name="example.clock/read@1")`
	s, err := importedAliasFixture(t, body)
	if err != nil {
		t.Fatal(err)
	}
	local := mustParse(t, constraintHead+`namespace * alias.use
typedef string timestamp(write="rfc3339-micros",read="rfc3339-wide")
typedef string Label(length="1..8",pattern="no_control")
`+strings.ReplaceAll(body, "owner.", ""))
	s.NamedCodecs, local.NamedCodecs = true, true
	local.Imports = s.Imports // compare the same named-codec/include output mode
	// Every backend sees the same primitive/grammar/check IR. Alias owner names
	// are metadata; no per-language imported timestamp codec is introduced.
	for _, emit := range []struct {
		name string
		fn   func(*Definition) string
	}{{"go", genGo}, {"cpp", genCpp}, {"python", genPy}, {"javascript", genJS}, {"rust", genRust}} {
		t.Run(emit.name, func(t *testing.T) {
			a, b := emit.fn(s), emit.fn(local)
			if a != b {
				i := 0
				for i < len(a) && i < len(b) && a[i] == b[i] {
					i++
				}
				t.Fatalf("imported alias changed codec semantics at %d: %q / %q", i, a[i:min(i+120, len(a))], b[i:min(i+120, len(b))])
			}
		})
	}
	if !reflect.DeepEqual(jsonSchemaRecords(s), jsonSchemaRecords(local)) {
		t.Fatal("schema projection lost imported constraints")
	}
}

func TestImportedScalarAliasRefusals(t *testing.T) {
	for _, field := range []string{"timestamp time", "missing.timestamp time", "owner.Missing time", "owner.next.timestamp time", "list<owner.timestamp> time", "map<string,owner.timestamp> time"} {
		t.Run(field, func(t *testing.T) {
			if _, err := importedAliasFixture(t, `struct Record{1:required `+field+`}(document="true",unknown_fields="refuse")`); err == nil {
				t.Fatal("accepted unsupported alias scope/position")
			}
		})
	}
	for _, decl := range []string{`typedef owner.timestamp Copy`, `typedef string First typedef First Second`, `typedef i64 First typedef First Second`} {
		if _, err := importedAliasFixture(t, decl+` struct Record{1:required string value}(document="true",unknown_fields="refuse")`); err == nil {
			t.Fatal("accepted typedef chain", decl)
		}
	}
	// A local homonym remains local; qualification selects the canonical owner.
	s, err := importedAliasFixture(t, `typedef i64 timestamp struct Record{1:required timestamp local 2:required owner.timestamp remote}(document="true",unknown_fields="refuse")`)
	if err != nil {
		t.Fatal(err)
	}
	if s.Structs[0].Fields[0].Type != "i64" || s.Structs[0].Fields[1].Type != "string" {
		t.Fatal("scope collision changed identity")
	}
	if _, err := importedAliasFixture(t, `struct Record{1:required owner.Label label(length="9..10")}(document="true",unknown_fields="refuse")`); err == nil {
		t.Fatal("accepted disjoint imported and local constraints")
	}
}

func TestImportedScalarAliasConstraintRefusalOwnership(t *testing.T) {
	dir := t.TempDir()
	owner := constraintHead + `namespace * alias.owner
typedef string Label(length="1..8",pattern="no_control")
struct Owner{1:required Label value}(document="true",unknown_fields="refuse")`
	writeNamespaceFile(t, dir, "owner.thrift", owner)
	for _, tc := range []struct {
		typ    string
		refuse bool
	}{{"string", false}, {"owner.Label", true}} {
		// Unused imported constraints belong to their definition. A use must
		// declare the inherited refusal in the importing definition too.
		writeNamespaceFile(t, dir, "use.thrift", head+`namespace * alias.use
include "owner.thrift"
struct Record{1:required `+tc.typ+` value}(document="true",unknown_fields="refuse")`)
		_, err := loadDefinition(filepath.Join(dir, "use.thrift"))
		if (err != nil) != tc.refuse {
			t.Fatalf("%s inherited refusal ownership: %v", tc.typ, err)
		}
	}
}

func TestImportedCanonicalJobTimestamp(t *testing.T) {
	dir := t.TempDir()
	b, err := os.ReadFile(privateInput(t, "..", "..", "openabstractions-flat", "abstraction-job", "job.thrift"))
	if err != nil {
		t.Fatal(err)
	}
	writeNamespaceFile(t, dir, "job.thrift", string(b))
	writeNamespaceFile(t, dir, "input.thrift", head+`namespace * private.input
include "job.thrift"
struct Input{1:optional job.timestamp created_at(omit="zero") 2:required job.Progress progress}(document="true",unknown_fields="refuse")`)
	s, err := loadDefinition(filepath.Join(dir, "input.thrift"))
	if err != nil {
		t.Fatal(err)
	}
	s.NamedCodecs = true
	s.NoIPC = true
	s.GoImports = map[string]string{"job": "github.com/openabstractions/abstraction-job/go/rec"}
	if err = validateIncludes(s, "go"); err != nil {
		t.Fatal(err)
	}
	s.GoImports = nil
	if err = validateIncludes(s, "go"); err == nil {
		t.Fatal("unnamespaced include accepted without explicit Go package mapping")
	}
	s.GoImports = map[string]string{"job": "github.com/openabstractions/abstraction-job/go/rec"}
	out := mustEmitIncluded(t, backends[0], s)
	if !strings.Contains(out, "bad_timestamp") || !strings.Contains(out, "oa_dependency_job") {
		t.Fatal("canonical timestamp/nested-record projection lost")
	}
}
