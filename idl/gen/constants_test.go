package main

import (
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// [DEF-A6]: a scalar i32 or i64 const is one integer every backend prints.
const scalarConsts = `const i32 status_ok = 0
const i32 status_proof_unavailable = 9
const i32 default_max_frame = 1048576
const i64 lowest = -9223372036854775808
const i64 budget_ms = 5000
const string gap_message = "records dropped while the sink was unreachable"
const string escaped = "\b\f\n\r\t\\\"\u2028€\u0000a"
`

func TestScalarConstantValidation(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"const i32 x = 2147483648\n", "canonical in-range i32"},
		{"const i32 x = 01\n", "canonical in-range i32"},
		{"const i64 x = \"5\"\n", "canonical in-range i64"},
		{"const bool x = true\n", "a const is i32, i64, string, list<i32> or list<string>"},
		{"const string x = 1\n", "requires a quoted string"},
		{`const string x = "\x00"`, "invalid constant string"},
	} {
		_, e := parse(head + doc + tc.src)
		if e == nil || !strings.Contains(e.Error(), tc.want) {
			t.Fatalf("%s: want %s got %v", tc.src, tc.want, e)
		}
	}
	s, e := parse(head + doc + scalarConsts)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(genDocs(s), "<code>budget_ms</code>") {
		t.Fatal("docs lost a scalar constant")
	}
}

func TestScalarStringQuotationKeepsLegacyLiterals(t *testing.T) {
	s, err := parse(head + doc + `const list<string> raw = ["\n", "C:\path\"]
const string quoted = "\nC:\\path\\"
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Consts[len(s.Consts)-2].Strings; len(got) != 2 || got[0] != `\n` || got[1] != `C:\path\` {
		t.Fatalf("legacy strings changed: %q", got)
	}
	if got := s.Consts[len(s.Consts)-1].Strings[0]; got != "\nC:\\path\\" {
		t.Fatalf("scalar JSON string %q", got)
	}
}

// --namespace places one artefact under another namespace for one language,
// which is how a binding carries a selected subset inside its own package.
func TestNamespaceFlagPlacesOneLanguage(t *testing.T) {
	dir := t.TempDir()
	def := filepath.Join(dir, "d.thrift")
	source := strings.Replace(constraintHead, "  10: bad_binary       (stage = \"structure\")\n  11: bad_value        (stage = \"structure\")\n  12: trailing_bytes", "  10: trailing_bytes", 1)
	if err := os.WriteFile(def, []byte("namespace * example.api\n"+source+doc+scalarConsts), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	var report strings.Builder
	if err := run([]string{def, out, "python", "rust", "-only=budget_ms", "--namespace=python=example.ipc.constants"}, &report); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"py/example/ipc/constants/__init__.py", "rs/example/api/rec.rs"} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(path))); err != nil {
			t.Fatalf("%s: %v\n%s", path, err, report.String())
		}
	}
	for _, tc := range []struct{ flag, want string }{
		{"--namespace=*=a.b", "one language"},
		{"--namespace=python", "one language"},
		{"--namespace=python=a..b", "empty component"},
		{"--namespace=cobol=a.b", "unknown language"},
	} {
		err := run([]string{def, out, "python", tc.flag}, &report)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want %s got %v", tc.flag, tc.want, err)
		}
	}
	if err := run([]string{def, out, "python", "--namespace=python=a.b", "--namespace=python=c.d"}, &report); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("a second --namespace for one language: %v", err)
	}
}

func TestScalarConstantsFiveBackends(t *testing.T) {
	s, e := parse(head + doc + scalarConsts)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	for n, b := range map[string]string{"rec/rec.go": genGo(s), "rec.py": genPy(s), "rec.h": genCpp(s), "rec.rs": genRust(s), "rec.mjs": genJS(s), "go.mod": "module consts.test\n\ngo 1.22\n",
		"main.go": `package main
import("fmt";r "consts.test/rec")
func main(){var a int32=r.StatusProofUnavailable;var b int64=r.Lowest;fmt.Println(r.StatusOk,a,r.DefaultMaxFrame,b,r.BudgetMs);fmt.Println(r.GapMessage);fmt.Printf("%x\n",[]byte(r.Escaped))}
`,
		"main.py":  "import rec\nprint(rec.STATUS_OK, rec.STATUS_PROOF_UNAVAILABLE, rec.DEFAULT_MAX_FRAME, rec.LOWEST, rec.BUDGET_MS)\nprint(rec.GAP_MESSAGE)\nprint(rec.ESCAPED.encode().hex())\n",
		"main.mjs": "import * as r from './rec.mjs';\nconsole.log([r.statusOk, r.statusProofUnavailable, r.defaultMaxFrame, r.lowest, r.budgetMs].join(' '));\nconsole.log(r.gapMessage);console.log(Buffer.from(r.escaped,'utf8').toString('hex'));\n",
		"main.rs":  "mod rec;\nfn main(){let a:i32=rec::STATUS_PROOF_UNAVAILABLE;let b:i64=rec::LOWEST;println!(\"{} {} {} {} {}\",rec::STATUS_OK,a,rec::DEFAULT_MAX_FRAME,b,rec::BUDGET_MS);println!(\"{}\",rec::GAP_MESSAGE);for b in rec::ESCAPED.as_bytes(){print!(\"{:02x}\",b)}println!();}\n",
		"main.cpp": "#include \"rec.h\"\n#include <iostream>\n#include <iomanip>\nstatic_assert(rec::kStatusProofUnavailable == 9, \"constexpr\");\nint main(){std::int64_t b=rec::kLowest;std::cout<<rec::kStatusOk<<' '<<rec::kStatusProofUnavailable<<' '<<rec::kDefaultMaxFrame<<' '<<b<<' '<<rec::kBudgetMs<<'\\n'<<rec::kGapMessage<<'\\n';for(unsigned char v:rec::kEscaped)std::cout<<std::hex<<std::setw(2)<<std::setfill('0')<<static_cast<unsigned>(v);std::cout<<'\\n';}\n",
	} {
		writeNamespaceFile(t, dir, n, b)
	}
	want := "0 9 1048576 -9223372036854775808 5000\nrecords dropped while the sink was unreachable\n" + hex.EncodeToString([]byte("\b\f\n\r\t\\\"\u2028€\x00a")) + "\n"
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
			t.Fatalf("%s printed %q", name, got)
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
		exe := filepath.Join(dir, "rust.exe")
		c := exec.Command(rustServiceCompiler(t), "--edition=2021", "main.rs", "-o", exe)
		c.Dir = dir
		if out, e := c.CombinedOutput(); e != nil {
			t.Fatalf("rustc: %v\n%s", e, out)
		}
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
		c := exec.Command(p, args...)
		c.Dir = dir
		if out, e := c.CombinedOutput(); e != nil {
			t.Fatalf("%s: %v\n%s", p, e, out)
		}
		run(t, exe)
	})
}

func TestRustConstantOnlyPrivateModuleNeedsNoCodecScaffolding(t *testing.T) {
	s, err := parse(head + doc + scalarConsts)
	if err != nil {
		t.Fatal(err)
	}
	only, err := selected(s, []string{"status_ok", "status_proof_unavailable", "default_max_frame", "lowest", "budget_ms"})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"plain": genRust(only), "named": emitRustIncluded(backend{}, only)} {
		t.Run(name, func(t *testing.T) {
			for _, unused := range []string{"pub type Raw", "pub struct Refusal", "struct Reader", "fn encode", "fn decode"} {
				if strings.Contains(body, unused) {
					t.Fatalf("constant-only private module carries unused codec item %q", unused)
				}
			}
			dir := t.TempDir()
			writeNamespaceFile(t, dir, "rec.rs", body)
			writeNamespaceFile(t, dir, "main.rs", `mod rec;
fn main() {
    let status: i32 = rec::STATUS_PROOF_UNAVAILABLE;
    let lowest: i64 = rec::LOWEST;
    assert_eq!((rec::STATUS_OK, status, rec::DEFAULT_MAX_FRAME, lowest, rec::BUDGET_MS),
               (0, 9, 1048576, -9223372036854775808, 5000));
}
`)
			exe := filepath.Join(dir, "constants.exe")
			compile := exec.Command(rustServiceCompiler(t), "--edition=2021", "-D", "warnings", "main.rs", "-o", exe)
			compile.Dir = dir
			if out, err := compile.CombinedOutput(); err != nil {
				t.Fatalf("rustc -D warnings: %v\n%s", err, out)
			}
			if out, err := exec.Command(exe).CombinedOutput(); err != nil {
				t.Fatalf("constant values: %v\n%s", err, out)
			}
		})
	}
	// Adding a record retains its codecs and their actual refusal/opaque types.
	for _, needed := range []string{"pub type Raw", "pub struct Refusal", "struct Reader", "pub fn encode", "pub fn decode"} {
		if !strings.Contains(genRust(s), needed) {
			t.Fatalf("mixed record/constant definition lost %q", needed)
		}
	}
}
