package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// [DEF-A12]: a fully bounded record above one frame's 1 MiB is refused unless
// it is a chunk; a record with an unbounded value has no largest encoding and
// is idl/inventory.py's to hold.
const sizedRecords = `struct Doc {
  1: required string id (length = "1..64")
} (document = "true", unknown_fields = "refuse")
struct Piece {
  1: required binary bytes (length = "..1048576")
  2: required i64 offset
} (unknown_fields = "refuse")
struct Open {
  1: required string note
} (unknown_fields = "refuse")
`

func TestLargeRecordIsRefusedUnlessChunk(t *testing.T) {
	_, err := parse(constraintHead + sizedRecords)
	if err == nil || !strings.Contains(err.Error(), "record Piece encodes to as many as") || !strings.Contains(err.Error(), "chunk") {
		t.Fatalf("a 1 MiB binary passed: %v", err)
	}
	chunked := strings.Replace(sizedRecords, "} (unknown_fields = \"refuse\")\nstruct Open", "} (unknown_fields = \"refuse\", chunk = \"true\")\nstruct Open", 1)
	s, err := parse(constraintHead + chunked)
	if err != nil {
		t.Fatalf("a chunk was refused: %v", err)
	}
	if !strings.Contains(genDocs(s), "one chunk of a stream") {
		t.Fatal("the reference page lost the chunk mark")
	}
	if _, err := parse(constraintHead + strings.Replace(chunked, `chunk = "true"`, `chunk = "yes"`, 1)); err == nil || !strings.Contains(err.Error(), `chunk = "yes"`) {
		t.Fatalf("chunk = \"yes\" passed: %v", err)
	}
	small := strings.Replace(sizedRecords, `"..1048576"`, `"..4096"`, 1)
	if _, err := parse(constraintHead + small); err != nil {
		t.Fatalf("a 4 KiB record was refused: %v", err)
	}
	argument := strings.Replace(small, "struct Open {", `service Store {
  void Put(1: binary bytes (length = "..2000000"))
} (wire_name = "example.store/store@1")
struct Open {`, 1)
	if _, err := parse(constraintHead + argument); err == nil || !strings.Contains(err.Error(), "OAStorePutArguments") {
		t.Fatalf("a 2 MB argument passed: %v", err)
	}
}

func TestSizesReport(t *testing.T) {
	dir := t.TempDir()
	def := filepath.Join(dir, "d.thrift")
	src := constraintHead + strings.Replace(sizedRecords, `"..1048576"`, `"..3"`, 1)
	if err := os.WriteFile(def, []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := run([]string{"--sizes", def}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Doc\t", "Piece\t", "Open\tunbounded\tOpen.note\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("--sizes lost %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Doc\tunbounded") || strings.Contains(got, "Piece\tunbounded") {
		t.Fatalf("a bounded record read unbounded:\n%s", got)
	}
}
