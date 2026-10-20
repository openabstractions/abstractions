package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The job record references (JOB-J1, JOB-J7, [DEF-E6]).
//
// abstraction-job/testdata holds two records written by hand from the contract
// page, never by a backend. job.thrift declares opaque = "reindent", so every
// generated encoder lays a payload out again at the record's indent and writes
// those two files byte for byte, as the hand-written Go and Python peers do.
// The conformance drivers under idl/test/drivers build the records; this test
// runs them from the generated bindings and fails on each comparison
// idl/test/run.ps1 prints: each record against its reference ("reference:
// reproduces"), every record unchanged through decode and encode, the awkward
// record and the corpus verdicts identical across languages, and every corpus
// input given the word its file name states. run.ps1 keeps the
// escape = "ascii" variant.

func jobDefinition(t *testing.T) *Definition {
	t.Helper()
	src, err := os.ReadFile(privateInput(t, "..", "..", "openabstractions-flat", "abstraction-job", "job.thrift"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := parse(string(src))
	if err != nil {
		t.Fatal(err)
	}
	if !reindentsOpaque(s) {
		t.Fatalf("job.thrift declares opaque = %q; JOB-J1 compares record bytes and JOB-J7 leaves a payload's whitespace out, so it declares \"reindent\"", s.Encoding.Opaque)
	}
	return s
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestJobRecordReferences(t *testing.T) {
	s := jobDefinition(t)
	drivers, err := filepath.Abs("../test/drivers")
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := filepath.Abs("../test/corpus")
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string][]byte{
		"ranges":   readFile(t, privateInput(t, "..", "..", "openabstractions-flat", "abstraction-job", "testdata", "ranges-record.json")),
		"terminal": readFile(t, privateInput(t, "..", "..", "openabstractions-flat", "abstraction-job", "testdata", "terminal-record.json")),
	}
	// Each corpus input's name states its verdict: accept.* is ok, and any
	// other name begins with the refusal word.
	inputs := map[string]string{}
	names, err := filepath.Glob(filepath.Join(corpus, "*.json"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no corpus at %s: %v", corpus, err)
	}
	for _, n := range names {
		name := strings.TrimSuffix(filepath.Base(n), ".json")
		word := strings.SplitN(name, ".", 2)[0]
		if word == "accept" {
			word = "ok"
		}
		inputs[name] = word
	}
	dir := t.TempDir()
	res := filepath.Join(dir, "res")
	if err := os.MkdirAll(res, 0700); err != nil {
		t.Fatal(err)
	}
	command := func(t *testing.T, cwd string, args ...string) {
		t.Helper()
		c := exec.Command(args[0], args[1:]...)
		c.Dir = cwd
		c.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	langs := []struct {
		name, prefix string
		run          func(t *testing.T)
	}{
		{"go", "go", func(t *testing.T) {
			m := filepath.Join(dir, "go")
			writeNamespaceFile(t, m, "go.mod", "module idl/out/go\n\ngo 1.22\n")
			writeNamespaceFile(t, m, "rec/rec.go", genGo(s))
			writeNamespaceFile(t, m, "driver/main.go", string(readFile(t, filepath.Join(drivers, "go", "main.go"))))
			command(t, m, "go", "run", "./driver", res, corpus)
		}},
		{"python", "py", func(t *testing.T) {
			p := servicePython(t)
			m := filepath.Join(dir, "py")
			writeNamespaceFile(t, m, "_codec.py", genPy(s))
			command(t, m, p, "-B", filepath.Join(drivers, "py", "run.py"), m, res, corpus)
		}},
		{"javascript", "js", func(t *testing.T) {
			node, err := exec.LookPath("node")
			if err != nil {
				t.Skip("node unavailable: the JavaScript encoder is unproven against the references")
			}
			m := filepath.Join(dir, "js")
			writeNamespaceFile(t, m, "rec.mjs", genJS(s))
			writeNamespaceFile(t, m, "run.mjs", string(readFile(t, filepath.Join(drivers, "js", "run.mjs"))))
			command(t, m, node, "run.mjs", res, corpus)
		}},
		{"rust", "rs", func(t *testing.T) {
			rustc, err := exec.LookPath("rustc")
			if err != nil {
				t.Skip("rustc unavailable: the Rust encoder is unproven against the references")
			}
			m := filepath.Join(dir, "rs")
			writeNamespaceFile(t, m, "rec.rs", genRust(s))
			writeNamespaceFile(t, m, "main.rs", string(readFile(t, filepath.Join(drivers, "rs", "main.rs"))))
			command(t, m, rustc, "--edition", "2021", "main.rs", "-o", filepath.Join(m, "driver.exe"))
			command(t, m, filepath.Join(m, "driver.exe"), res, corpus)
		}},
		{"cpp", "cpp", func(t *testing.T) {
			cxx := os.Getenv("CXX")
			for _, candidate := range []string{"g++", "clang++"} {
				if cxx == "" {
					cxx, _ = exec.LookPath(candidate)
				}
			}
			if cxx == "" {
				t.Skip("C++ compiler (CXX, g++ or clang++) unavailable: the C++ encoder is unproven against the references")
			}
			m := filepath.Join(dir, "cpp")
			writeNamespaceFile(t, m, "rec.h", genCpp(s))
			writeNamespaceFile(t, m, "run.cpp", string(readFile(t, filepath.Join(drivers, "cpp", "run.cpp"))))
			args := []string{cxx, "-std=c++20", "run.cpp", "-o", filepath.Join(m, "driver.exe")}
			if n := strings.ToLower(filepath.Base(cxx)); n == "cl" || n == "cl.exe" {
				args = []string{cxx, "/nologo", "/std:c++20", "/EHsc", "/utf-8", "run.cpp", "/Fe:" + filepath.Join(m, "driver.exe"), "/Fo:" + filepath.Join(m, "run.obj")}
			}
			command(t, m, args...)
			// The driver loads the runtime of the compiler that built it. A
			// MinGW g++ driver started under another toolchain's PATH (Git for
			// Windows puts its own libstdc++-6.dll first) loads that DLL and
			// fails with 0xc0000139, a missing entry point.
			driver := exec.Command(filepath.Join(m, "driver.exe"), res, corpus)
			driver.Dir = m
			driver.Env = os.Environ()
			if resolved, err := exec.LookPath(cxx); err == nil {
				driver.Env = append(driver.Env, "PATH="+filepath.Dir(resolved)+string(os.PathListSeparator)+os.Getenv("PATH"))
			}
			if out, err := driver.CombinedOutput(); err != nil {
				t.Fatalf("%v: %v\n%s", driver.Args, err, out)
			}
		}},
	}
	for _, l := range langs {
		t.Run(l.name, func(t *testing.T) {
			l.run(t)
			for name, want := range refs {
				got := readFile(t, filepath.Join(res, l.prefix+"-"+name+".json"))
				if string(got) != string(want) {
					t.Errorf("%s %s record differs from abstraction-job/testdata/%s-record.json\ngot:\n%s\nwant:\n%s", l.name, name, name, got, want)
				}
			}
			for _, name := range []string{"awkward", "ranges", "terminal"} {
				first := readFile(t, filepath.Join(res, l.prefix+"-"+name+".json"))
				if back := readFile(t, filepath.Join(res, l.prefix+"-rt-"+name+".json")); string(back) != string(first) {
					t.Errorf("%s %s record changed on decode and encode:\n%s", l.name, name, back)
				}
			}
			// An accepted input's line carries its spec, which may span
			// lines; a verdict line starts with an input's name.
			seen := map[string]bool{}
			for _, line := range strings.Split(string(readFile(t, filepath.Join(res, l.prefix+"-corpus.txt"))), "\n") {
				fields := strings.SplitN(line, "\t", 3)
				want, input := inputs[fields[0]]
				if !input || len(fields) < 2 {
					continue
				}
				seen[fields[0]] = true
				if fields[1] != want {
					t.Errorf("%s said %s for %s, want %s", l.name, fields[1], fields[0], want)
				}
			}
			if len(seen) != len(inputs) {
				t.Errorf("%s gave verdicts for %d of %d corpus inputs", l.name, len(seen), len(inputs))
			}
		})
	}
	// A language whose toolchain is missing skipped and wrote nothing.
	var ran []string
	for _, l := range langs {
		if _, err := os.Stat(filepath.Join(res, l.prefix+"-corpus.txt")); err == nil {
			ran = append(ran, l.prefix)
		}
	}
	if len(ran) == 0 {
		t.Fatal("no language wrote its corpus verdicts")
	}
	for _, out := range []string{"awkward.json", "corpus.txt"} {
		for _, prefix := range ran[1:] {
			if string(readFile(t, filepath.Join(res, prefix+"-"+out))) != string(readFile(t, filepath.Join(res, ran[0]+"-"+out))) {
				t.Errorf("%s-%s disagrees with %s-%s", prefix, out, ran[0], out)
			}
		}
	}
}

// The peers corpus states a payload's canonical layout beside the input that
// spells it some other way (scripts/peers/verdicts.go reads `.roundtrip`).
// The generated Go binding writes that layout too.
func TestJobRecordWhitespaceRoundTrip(t *testing.T) {
	s := jobDefinition(t)
	in := readFile(t, "../test/corpus/accept.roundtrip-whitespace.json")
	want := readFile(t, "../test/corpus/accept.roundtrip-whitespace.json.roundtrip")
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "go.mod", "module job.test\n\ngo 1.22\n")
	writeNamespaceFile(t, dir, "rec/rec.go", genGo(s))
	writeNamespaceFile(t, dir, "main.go", `package main

import (
	"os"

	rec "job.test/rec"
)

func main() {
	in, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	v, err := rec.Decode(in)
	if err != nil {
		panic(err)
	}
	os.Stdout.Write(rec.Encode(v))
}
`)
	c := exec.Command("go", "run", ".", filepath.Join(dir, "in.json"))
	writeNamespaceFile(t, dir, "in.json", string(in))
	c.Dir = dir
	c.Env = append(os.Environ(), "GOWORK=off")
	got, err := c.Output()
	if err != nil {
		if x, ok := err.(*exec.ExitError); ok {
			t.Fatalf("%v\n%s", err, x.Stderr)
		}
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("decode then encode wrote\n%s\nwant\n%s", got, want)
	}
}
