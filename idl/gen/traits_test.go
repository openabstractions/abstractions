package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMethodTraitKeyFollowsImportedRecordOwner(t *testing.T) {
	for _, tc := range []struct {
		name, keyType, shadowType                      string
		optionalBox, optionalKey, optionalRoot, refuse bool
	}{
		{"foreign-owner", "string", "i64", false, false, false, false},
		{"wrong-foreign-key", "i64", "string", false, false, false, true},
		{"optional-imported-record", "string", "string", true, false, false, true},
		{"optional-imported-key", "string", "string", false, true, false, true},
		{"optional-root-record", "string", "string", false, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			key := "required " + tc.keyType + " key"
			if tc.optionalKey {
				key = "optional string key(omit=\"zero\")"
			}
			box := "required Identity inner"
			if tc.optionalBox {
				box = "optional Identity inner(omit=\"absent\")"
			}
			root := "required foreign.Box box"
			if tc.optionalRoot {
				root = "optional foreign.Box box(omit=\"absent\")"
			}
			writeNamespaceFile(t, dir, "foreign.thrift", constraintHead+
				"struct Identity {1:"+key+"}(unknown_fields=\"refuse\")\n"+
				"struct Box {1:"+box+"}(document=\"true\",unknown_fields=\"refuse\")\n")
			writeNamespaceFile(t, dir, "caller.thrift", "include \"foreign.thrift\"\n"+constraintHead+
				"struct Identity {1:required "+tc.shadowType+" key}(unknown_fields=\"refuse\")\n"+
				"struct Request {1:"+root+"}(document=\"true\",unknown_fields=\"refuse\")\n"+
				"service Caller {Request Submit(1:Request request)(idempotency_key=\"request.box.inner.key\")}(wire_name=\"example.caller/caller@1\")\n")
			def, err := loadDefinition(filepath.Join(dir, "caller.thrift"))
			if tc.refuse {
				if err == nil || !strings.Contains(err.Error(), "names no required string") {
					t.Fatalf("required-string path accepted or wrong refusal: %v", err)
				}
			} else if err != nil || def.Services[0].Methods[0].IdempotencyKey != "request.box.inner.key" {
				t.Fatalf("imported required-string path: %v", err)
			}
		})
	}
}

// [DEF-S3]: one trait per method, a key that names a required string.
const traitService = `struct Job {
  1: required string key (length = "1..64")
  2: optional string note (omit = "zero", length = "..64")
} (document = "true", unknown_fields = "refuse")
service Jobs {
  Job Read(1: string id (length = "..64")) (readonly = "true")
  Job Cancel(1: string id (length = "..64")) (idempotent = "true")
  Job Submit(1: Job job) (idempotency_key = "job.key")
  Job Retry(1: string token (length = "..64"), 2: Job job) (idempotency_key = "token")
  Job Plain(1: string id (length = "..64"))
  Job Unsafe(1: string id (length = "..64")) (idempotent = "false")
} (wire_name = "example.jobs/jobs@1")
`

func TestMethodTraits(t *testing.T) {
	s, err := parse(constraintHead + traitService)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range s.Services[0].Methods {
		got[m.Name] = m.Trait + ":" + m.IdempotencyKey
	}
	want := map[string]string{"Read": "readonly:", "Cancel": "idempotent:", "Submit": "keyed:job.key", "Retry": "keyed:token", "Plain": ":", "Unsafe": ":"}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %q want %q", k, got[k], v)
		}
	}
	docs := genDocs(s)
	for _, w := range []string{"· readonly", "· idempotent", "· idempotency key job.key", "· no idempotency or replay guarantee"} {
		if !strings.Contains(docs, w) {
			t.Fatalf("docs lost %q", w)
		}
	}
	for _, tc := range []struct{ old, new, want string }{
		{`(readonly = "true")`, `(readonly = "yes")`, `readonly = "yes"`},
		{`(readonly = "true")`, `(readonly = "true", idempotent = "true")`, "declares one trait"},
		{`(idempotency_key = "job.key")`, `(idempotency_key = "job.note")`, "names no required string"},
		{`(idempotency_key = "job.key")`, `(idempotency_key = "job.missing")`, "names no required string"},
		{`(idempotency_key = "token")`, `(idempotency_key = "absent")`, "names no required string"},
		{`(idempotency_key = "token")`, `(idempotency_key = "")`, "names no argument"},
		{`(idempotent = "true")`, `(retry = "true")`, "unknown method annotation retry"},
		{`(idempotent = "false")`, `(idempotent = "False")`, `idempotent = "False"`},
		{`(idempotent = "false")`, `(idempotent = "false", readonly = "true")`, "declares one trait"},
		{`(idempotent = "false")`, `(idempotent = "false", idempotency_key = "id")`, "declares one trait"},
		{`(idempotent = "false")`, `(idempotent = "false", idempotent = "true")`, "declared twice"},
	} {
		_, err := parse(constraintHead + strings.Replace(traitService, tc.old, tc.new, 1))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want %s got %v", tc.new, tc.want, err)
		}
	}
}

func TestNestedMethodTraitKeyRequiresEveryRecordField(t *testing.T) {
	source := constraintHead + `struct Key {1: required string value}(unknown_fields="refuse")
struct Envelope {1: required Key key}(document="true",unknown_fields="refuse")
service S {Envelope Put(1:Envelope envelope)(idempotency_key="envelope.key.value")}(wire_name="example/nested@1")`
	if _, err := parse(source); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{
		strings.Replace(source, "required Key key", `optional Key key(omit="absent")`, 1),
		strings.Replace(source, "required string value", `optional string value(omit="zero")`, 1),
		strings.Replace(source, "required string value", `required i64 value`, 1),
		strings.Replace(source, "envelope.key.value", "envelope.key.absent", 1),
		strings.Replace(source, "envelope.key.value", "envelope.key.value.extra", 1),
	} {
		if _, err := parse(changed); err == nil || !strings.Contains(err.Error(), "names no required string") {
			t.Fatalf("invalid required key path accepted: %v", err)
		}
	}
}

func TestExplicitNonIdempotentRetainsUnsafeClientsAndCompatibility(t *testing.T) {
	before, err := parse(constraintHead + strings.Replace(traitService, `(idempotent = "false")`, "", 1))
	if err != nil {
		t.Fatal(err)
	}
	after, err := parse(constraintHead + traitService)
	if err != nil {
		t.Fatal(err)
	}
	unsafe := after.Services[0].Methods[len(after.Services[0].Methods)-1]
	if !unsafe.NonIdempotent || unsafe.Trait != "" || unsafe.IdempotencyKey != "" {
		t.Fatal(unsafe)
	}
	if problems := compatibility(before, after); len(problems) != 0 {
		t.Fatal(problems)
	}
	if !reflect.DeepEqual(definitionHashes(before), definitionHashes(after)) {
		t.Fatal("unsafe declaration changed canonical ABI")
	}
	for name, generate := range map[string]func(*Definition) string{"go": genGo, "python": genPy, "javascript": genJS, "rust": genRust, "cpp": genCpp} {
		if generate(before) != generate(after) {
			t.Fatalf("%s unsafe client policy changed", name)
		}
	}
}

func TestTraitMetadataNestedKeysPreserveWireAndClients(t *testing.T) {
	const source = `struct Identity {
  1: required string key
  2: optional string note (omit = "zero")
} (unknown_fields = "refuse")
struct Submission {
  1: required Identity identity
  2: optional Identity optional_identity (omit = "absent")
} (document = "true", unknown_fields = "refuse")
service Acceptance {
  string Submit(1: Submission submission)
  string Read()
  string Cancel(1: string id)
} (wire_name = "example.jobs/acceptance@1")
`
	before, err := parse(constraintHead + source)
	if err != nil {
		t.Fatal(err)
	}
	annotated := strings.Replace(source, "Submit(1: Submission submission)", `Submit(1: Submission submission) (idempotency_key = "submission.identity.key")`, 1)
	annotated = strings.Replace(annotated, "Read()", `Read() (readonly = "true")`, 1)
	annotated = strings.Replace(annotated, "Cancel(1: string id)", `Cancel(1: string id) (idempotent = "true")`, 1)
	after, err := parse(constraintHead + annotated)
	if err != nil {
		t.Fatal(err)
	}
	if got := after.Services[0].Methods[0].IdempotencyKey; got != "submission.identity.key" {
		t.Fatal(got)
	}
	if problems := compatibility(before, after); len(problems) != 0 {
		t.Fatal(problems)
	}
	if !reflect.DeepEqual(definitionHashes(before), definitionHashes(after)) {
		t.Fatal("trait metadata changed canonical wire form")
	}
	for name, generate := range map[string]func(*Definition) string{"go": genGo, "python": genPy, "javascript": genJS, "rust": genRust, "cpp": genCpp} {
		if generate(before) != generate(after) {
			t.Fatalf("%s trait metadata changed generated calls", name)
		}
	}
	for _, path := range []string{"submission.optional_identity.key", "submission.identity.note", "submission.missing.key", "submission.identity.key.more", "submission.identity.", "submission..key"} {
		_, err := parse(constraintHead + strings.Replace(annotated, "submission.identity.key", path, 1))
		if err == nil || !strings.Contains(err.Error(), "names no required string") {
			t.Fatalf("%s: got %v", path, err)
		}
	}
}
