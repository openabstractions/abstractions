package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// [DEF-E6]: a reindenting definition declares no service, and a service never
// carries opaque JSON inside a record a reindenting definition owns.
func TestOpaqueReindentStaysOutOfServices(t *testing.T) {
	reindent := strings.Replace(nestedFixture, `opaque         = "verbatim"`, `opaque         = "reindent"`, 1)
	if _, err := parse(reindent); err == nil || !strings.Contains(err.Error(), "DEF-E6") {
		t.Fatalf("a reindenting definition with a service was accepted: %v", err)
	}
	noService := strings.Replace(reindent, "service Nest { Outer Echo(1: Outer value) } (wire_name=\"example.nest/nest@1\")\n", "", 1)
	s, err := parse(noService)
	if err != nil {
		t.Fatal(err)
	}
	if !reindentsOpaque(s) {
		t.Fatal("opaque = \"reindent\" not recorded")
	}

	payload := `namespace * example.payload
encoding json {
  escape         = "minimal"
  indent         = "2"
  map_keys       = "utf8-bytes"
  numbers        = "integer-decimal"
  opaque         = "reindent"
  terminator     = "newline"
  duplicate_keys = "refuse"
  depth_limit    = "64"
}
` + refusals + `struct Box {1: required json value} (document="true", unknown_fields="refuse")
struct Plain {1: required string name} (unknown_fields="refuse")
`
	root := func(arg, result string) string {
		return "namespace * example.root\n" + head + "include \"payload.thrift\"\n" +
			"struct Holder {1: required " + arg + " held} (document=\"true\", unknown_fields=\"refuse\")\n" +
			"service Carry { " + result + " Put(1: Holder value) } (wire_name=\"example.root/carry@1\")\n"
	}
	for _, tc := range []struct {
		name, arg, result, want string
	}{
		{"argument", "payload.Box", "bool", "service argument value carries opaque JSON"},
		{"result", "payload.Plain", "payload.Box", "service return carries opaque JSON"},
		{"plain record", "payload.Plain", "payload.Plain", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeNamespaceFile(t, dir, "payload.thrift", payload)
			writeNamespaceFile(t, dir, "root.thrift", root(tc.arg, tc.result))
			_, err := loadDefinition(filepath.Join(dir, "root.thrift"))
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("a record with no opaque value was refused: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

// Every backend prelude holds the verbatim helper opaqueLayout replaces.
func TestOpaqueLayoutReplacesEveryBackend(t *testing.T) {
	s, err := parse(strings.Replace(head+`struct R {1: required json v} (document="true", unknown_fields="refuse")`+"\n", `opaque         = "verbatim"`, `opaque         = "reindent"`, 1))
	if err != nil {
		t.Fatal(err)
	}
	for lang, gen := range map[string]func(*Definition) string{"go": genGo, "python": genPy, "cpp": genCpp, "javascript": genJS, "rust": genRust} {
		out := gen(s)
		if !strings.Contains(out, `[DEF-E6] opaque = "reindent"`) || strings.Contains(out, "opaque value's bytes as they are") {
			t.Errorf("%s: the reindenting raw helper did not replace the verbatim one", lang)
		}
	}
}
