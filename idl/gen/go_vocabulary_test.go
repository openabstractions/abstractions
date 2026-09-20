package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

const vocabularyTrapFixture = `enum Decision {1:permitted 2:unknown_action}(unknown="refuse")
enum Mode {40:fast}(unknown="refuse")
enum Cause {1:other 2:disk_full}(unknown="grant")
struct Record {1:required Decision decision 2:required Cause cause 3:optional Cause last(omit="absent")}(document="true",unknown_fields="refuse")`

// The unknown-member policy used to be exported beside the members:
// DecisionUnknown = "refuse" next to DecisionUnknownAction. Comparing a
// decision with it compiled, passed vet and was always false. The policy is
// now a property of the type, so the comparison no longer compiles.
func TestGoVocabularyPolicyIsNotAMemberLookalike(t *testing.T) {
	s, err := parse(enumHead() + vocabularyTrapFixture)
	if err != nil {
		t.Fatal(err)
	}
	gen := genGo(s)
	for _, trap := range []string{"DecisionUnknown =", "DecisionUnknown string", "CauseUnknown =", "UnknownPolicy", "var DecisionNames", "var CauseNames"} {
		if strings.Contains(gen, trap) {
			t.Errorf("generated Go still declares %q", trap)
		}
	}
	if cpp := genCpp(s); strings.Contains(cpp, "UnknownPolicy") {
		t.Error("generated C++ still declares an unknown-policy constant beside the members")
	}
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "go.mod", "module vocabulary.test\n\ngo 1.22\n")
	writeNamespaceFile(t, dir, "rec/rec.go", gen)
	build := func(main string) (string, error) {
		writeNamespaceFile(t, dir, "main.go", main)
		cmd := exec.Command("go", "vet", ".")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := build(`package main

import rec "vocabulary.test/rec"

func main() {
	var d rec.Decision = rec.DecisionPermitted
	_ = d == rec.DecisionUnknown
}
`)
	if err == nil || !strings.Contains(out, "DecisionUnknown") {
		t.Fatalf("comparing a decision with the policy compiled:\n%s", out)
	}
	for name, source := range map[string]string{
		"plain string": `package main
import rec "vocabulary.test/rec"
var _ rec.Decision = "permitted"
func main(){}`,
		"another enum": `package main
import rec "vocabulary.test/rec"
var _ rec.Decision = rec.ModeFast
func main(){}`,
	} {
		if out, err := build(source); err == nil || !strings.Contains(out, "cannot use") {
			t.Fatalf("%s assigned to a closed enum:\n%s", name, out)
		}
	}
	// An open vocabulary is typed too, keeps a word it has never heard, and
	// neither vocabulary's list can be changed by a caller.
	writeNamespaceFile(t, dir, "main_test.go", `package main

import (
	"testing"

	rec "vocabulary.test/rec"
)

func TestVocabulary(t *testing.T) {
	values := rec.DecisionValues()
	values[0] = rec.DecisionUnknownAction
	if rec.DecisionValues()[0] != rec.DecisionPermitted || len(values) != 2 {
		t.Fatal("the member list is shared with the caller")
	}
	if !rec.DecisionUnknownAction.Known() || rec.Decision(99).Known() {
		t.Fatal("Known")
	}
	if rec.DecisionPermitted.String() != "permitted" {
		t.Fatal("wire name")
	}
	if got, ok := rec.ParseDecision("unknown_action"); !ok || got != rec.DecisionUnknownAction {
		t.Fatal("parse", got, ok)
	}
	if _, ok := rec.ParseDecision("future"); ok {
		t.Fatal("parsed an unknown closed word")
	}
	if got, ok := rec.ParseMode("fast"); !ok || got != rec.ModeFast || got.String() != "fast" {
		t.Fatal("private numeric tag changed the wire word", got, ok)
	}
	v, err := rec.Decode([]byte(`+"`"+`{"decision":"permitted","cause":"novel_cause","last":"disk_full"}`+"`"+`))
	if err != nil {
		t.Fatal(err)
	}
	var cause rec.Cause = v.Cause
	if cause.Known() || string(cause) != "novel_cause" || *v.Last != rec.CauseDiskFull {
		t.Fatal(v)
	}
	if again, err := rec.Decode(rec.Encode(v)); err != nil || again.Cause != "novel_cause" {
		t.Fatal(again, err)
	}
}
`)
	if out, err := build("package main\n\nfunc main() {}\n"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cmd := exec.Command("go", "test", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// A service's declared error codes and the dispatcher's own become one
// ServiceErrorCode vocabulary, and ServiceError.Code has that type.
func TestServiceErrorCodesAreAVocabulary(t *testing.T) {
	base := enumHead() + `struct Answer {1:required string word}(document="true",unknown_fields="refuse")
const list<string> lookup_error_codes = ["storage_unavailable", "forbidden"]
service Lookup {
  Answer Find(1: string key)
} (wire_name = "example.lookup/lookup@1", error_codes = "lookup_error_codes")
`
	s, err := parse(base)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Services[0].ErrorCodes; strings.Join(got, ",") != "storage_unavailable,forbidden" {
		t.Fatalf("error codes %v", got)
	}
	gen := genGo(s)
	for _, want := range []string{"type ServiceErrorCode string", "ServiceErrorCodeHandlerError", "ServiceErrorCodeStorageUnavailable", "ServiceErrorCodeForbidden", "Code    ServiceErrorCode"} {
		if !strings.Contains(gen, want) {
			t.Errorf("generated Go lacks %q", want)
		}
	}
	for lang, body := range map[string]string{"cpp": genCpp(s), "python": genPy(s), "rust": genRust(s), "javascript": genJS(s)} {
		if !strings.Contains(strings.ToLower(strings.ReplaceAll(body, "_", "")), "serviceerrorcode") || !strings.Contains(body, "storage_unavailable") {
			t.Errorf("%s declares no ServiceErrorCode vocabulary with the declared codes", lang)
		}
	}
	for source, want := range map[string]string{
		strings.Replace(base, `error_codes = "lookup_error_codes"`, `error_codes = "missing_codes"`, 1):            "does not declare",
		strings.Replace(base, `"storage_unavailable", "forbidden"`, `"forbidden", "forbidden"`, 1):                 "distinct snake_case",
		strings.Replace(base, `"storage_unavailable", "forbidden"`, `"Storage Unavailable"`, 1):                    "distinct snake_case",
		strings.Replace(base, "struct Answer", "enum ServiceErrorCode {1:x}(unknown=\"grant\")\nstruct Answer", 1): "collision",
	} {
		if _, err := parse(source); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want an error containing %q, got %v", want, err)
		}
	}
}
