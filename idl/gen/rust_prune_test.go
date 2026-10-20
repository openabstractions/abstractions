package main

import (
	"strings"
	"testing"
)

func TestRustPruneKeepsWordAndMethodBoundaries(t *testing.T) {
	body := "fn helper() {}\nfn helper_extra() {}\nfn hidden() { loop_a(); }\nfn loop_a() { hidden(); }\nstruct Reader {}\nimpl<'a> Reader<'a> {\n    fn take() { self.step(); }\n    fn step() {}\n    fn bare() {}\n    fn distant() {}\n}\npub fn api(r: Reader) { helper_extra(); r.take(); bare; r. distant(); let _ = \"hidden\"; }\n"
	got := rsPrune(body)
	for _, absent := range []string{"fn helper()", "fn hidden", "fn loop_a", "fn bare", "fn distant"} {
		if strings.Contains(got, absent) {
			t.Fatalf("kept unreachable %q: %s", absent, got)
		}
	}
	for _, present := range []string{"fn helper_extra", "fn take", "fn step", "struct Reader"} {
		if !strings.Contains(got, present) {
			t.Fatalf("lost reachable %q: %s", present, got)
		}
	}
}

// Keep a representative production pruning input measurable independently of
// native compiler startup and the other backend suites.
func BenchmarkRustPruneContent(b *testing.B) {
	definition, err := loadDefinition(privateInput(b, "..", "..", "openabstractions-flat", "abstraction-storage", "content.thrift"))
	if err != nil {
		b.Fatal(err)
	}
	body := genRust(definition)
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rsPrune(body)
	}
}
