#!/bin/sh
# Does the code the generator emits read as the language it is written in?
#
#   sh idl/fit/fit.sh <scratchdir> [definition]        the table
#   sh idl/fit/fit.sh <scratchdir> --tsv               rows, for scripts/check.sh
#
# Two halves and they are never mixed.
#
# TOOL is the language's own community speaking, and it is the only evidence
# here worth the name: gofmt, go vet, rustfmt, rustc, python -m compileall,
# node --check. A tool that refuses the file is a HARD ZERO for that language
# and no rubric term buys it back, because a Go file gofmt will not accept is
# not Go however well it scores on our taste.
#
# RUBRIC is ours. Six terms, every one lifted from research/native-shape's
# per-language table of what a native speaker expects to be handed. It is the
# weaker kind of evidence and every line it prints says so.
#
# C++ gets UNPROVEN, not a pass: no compiler on this machine. clang-format is
# present and is deliberately NOT a gate — C++ has no canonical formatter the
# way Go has gofmt and Rust has rustfmt, so failing a file against LLVM style
# would be our taste wearing a tool's clothes. Its diff is printed as evidence.
set -eu

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
SCRATCH="${1:?usage: fit.sh <scratchdir> [definition|--tsv]}"
shift
TSV=0
DEF="$ROOT/job/job.thrift"
for a in "$@"; do
	case "$a" in
	--tsv) TSV=1 ;;
	*) DEF="$a" ;;
	esac
done

OUT="$SCRATCH/out"
RAW="$SCRATCH/rows.tsv"
mkdir -p "$SCRATCH/gocache" "$SCRATCH/gomodcache" "$SCRATCH/gotmp" "$SCRATCH/cargo" "$SCRATCH/rsout"
rm -rf "$OUT"
mkdir -p "$OUT"
: > "$RAW"

GOCACHE="$SCRATCH/gocache" GOMODCACHE="$SCRATCH/gomodcache" GOTMPDIR="$SCRATCH/gotmp"
export GOCACHE GOMODCACHE GOTMPDIR
export GOWORK=off GOFLAGS=-mod=mod CARGO_HOME="$SCRATCH/cargo"

# FIT_GEN points the run at a copy of the generator. That is how the red proof
# is taken: degrade one backend in a scratch copy, score it, and watch the rule
# fail without touching the tree.
GEN="${FIT_GEN:-$ROOT/idl/gen}"
(cd "$GEN" && go run . "$DEF" "$OUT") > "$SCRATCH/gen.log" 2>&1
printf 'module idl/out/go\n\ngo 1.26\n' > "$OUT/go/go.mod"

GO="$OUT/go/rec/rec.go"
PY="$OUT/py/rec.py"
CPP="$OUT/cpp/rec.h"
JS="$OUT/js/rec.mjs"
RS="$OUT/rs/rec.rs"

row() { printf '%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" "$5" >> "$RAW"; }
have() { command -v "$1" >/dev/null 2>&1; }

PYEXE="${ABSTRACTION_PYTHON:-}"
if [ -z "$PYEXE" ]; then
	for c in "$LOCALAPPDATA/Programs/Python/Python312/python.exe" python3 python; do
		have "$c" && { PYEXE="$c"; break; }
	done
fi
CLANGFMT=""
for c in clang-format \
	"/c/Program Files/Microsoft Visual Studio/18/Community/VC/Tools/Llvm/x64/bin/clang-format.exe"; do
	[ -n "$CLANGFMT" ] || ! have "$c" || CLANGFMT="$c"
done

# ---- the tools -----------------------------------------------------------
tool() { row tool "$1" "$2" "$3" "$4"; }

if have gofmt; then
	n=$(gofmt -l "$GO" | grep -c . || true)
	[ "$n" = 0 ] && tool go gofmt 1 "clean" || tool go gofmt 0 "gofmt would rewrite the file"
else tool go gofmt -1 "gofmt absent"; fi

if have go; then
	if (cd "$OUT/go" && go vet ./...) > "$SCRATCH/vet.log" 2>&1
	then tool go vet 1 "clean"
	else tool go vet 0 "$(head -1 "$SCRATCH/vet.log")"; fi
else tool go vet -1 "go absent"; fi

if [ -n "$PYEXE" ]; then
	if "$PYEXE" -m compileall -q "$PY" > "$SCRATCH/py.log" 2>&1
	then tool python compileall 1 "compiles"
	else tool python compileall 0 "$(head -1 "$SCRATCH/py.log")"; fi
else tool python compileall -1 "python absent"; fi

if have node; then
	if node --check "$JS" > "$SCRATCH/js.log" 2>&1
	then tool javascript node-check 1 "parses as an ES module"
	else tool javascript node-check 0 "$(head -1 "$SCRATCH/js.log")"; fi
else tool javascript node-check -1 "node absent"; fi

if have rustfmt; then
	n=$(rustfmt --edition 2021 --check "$RS" 2>/dev/null | grep -c '^Diff in' || true)
	[ "$n" = 0 ] && tool rust rustfmt 1 "clean" \
		|| tool rust rustfmt 0 "rustfmt would rewrite the file in $n places"
else tool rust rustfmt -1 "rustfmt absent"; fi

if have rustc; then
	cp "$RS" "$SCRATCH/rec_probe.rs"
	if rustc --edition 2021 --crate-type lib -D warnings \
		--out-dir "$SCRATCH/rsout" "$SCRATCH/rec_probe.rs" > "$SCRATCH/rs.log" 2>&1
	then tool rust rustc 1 "compiles with -D warnings, so rustc's own non_snake_case and non_camel_case_types lints are clean"
	else tool rust rustc 0 "$(head -1 "$SCRATCH/rs.log")"; fi
else tool rust rustc -1 "rustc absent"; fi

# No compiler on this machine and no canonical formatter in the language. Both
# halves of that sentence are load-bearing: the first is why this is UNPROVEN,
# the second is why the tool that IS here does not get to fail the build.
if [ -n "$CLANGFMT" ]; then
	n=$("$CLANGFMT" --style=LLVM "$CPP" 2>/dev/null | diff -u "$CPP" - | grep -c '^[+-][^+-]' || true)
	tool cpp compiler -1 "no C++ compiler here; clang-format LLVM style differs in $n lines, evidence only"
else
	tool cpp compiler -1 "no C++ compiler and no clang-format here"
fi

# ---- the rubric ----------------------------------------------------------
# Six terms, 0/1/2, from research/native-shape/RESULTS.txt section 1. Errors
# carries double weight because it is the term that decides whether a caller
# can use the binding at all: a Go function returns an error, a Rust function
# returns Result, and one shape emitted into five languages is code nobody in
# four of them accepts.
term() { row term "$1" "$2" "$3" "$4"; }

# 1 naming — the language's own convention for the names WE chose. Wire names
# are not judged: created_at is a JSON key and stays one.
gofields() { awk '/^type [A-Z][A-Za-z0-9]* struct \{$/ {in_s=1; next} in_s && /^\}/ {in_s=0} in_s && /^\t[A-Za-z_]/ {print $1}' "$GO"; }
n=$(gofields | grep -cvE '^[A-Z][A-Za-z0-9]*$' || true)
m=$(grep -oE '\b[A-Z][A-Za-z0-9]*(Id|Url|Uri|Http|Json|Api|Utf|Sql|Xml)\b' "$GO" | sort -u | grep -c . || true)
if [ "$n" != 0 ]; then
	term go naming 0 "record fields not in PascalCase: $n — a lower-case field is unexported, so a caller outside the package cannot read the record at all. gofmt and go vet both accept it"
elif [ "$m" != 0 ]; then
	term go naming 1 "exported names spelling an initialism as a word rather than in caps (Id, not ID): $m — Go Code Review Comments, Initialisms. gofmt and go vet cannot see this"
else
	term go naming 2 "every record field is PascalCase and no name spells an initialism in lower case"
fi

n=$(grep -oE '^def [A-Za-z_][A-Za-z0-9_]*' "$PY" | awk '{print $2}' | grep -cvE '^_?[a-z][a-z0-9_]*$' || true)
m=$(grep -oE '^class [A-Za-z_][A-Za-z0-9_]*' "$PY" | awk '{print $2}' | grep -cvE '^_?[A-Z][A-Za-z0-9]*$' || true)
[ "$((n + m))" = 0 ] && term python naming 2 "every def is snake_case and every class is CapWords — PEP 8" \
	|| term python naming 0 "$((n + m)) names break PEP 8"

n=$(grep -oE '^export (function|const) [A-Za-z_][A-Za-z0-9_]*' "$JS" | awk '{print $3}' | grep -c '_' || true)
[ "$n" = 0 ] && term javascript naming 2 "every exported name is camelCase" \
	|| term javascript naming 0 "exported names in snake_case (enc_step, enc_record): $n — JavaScript has no snake_case convention for functions and node --check cannot see it"

term rust naming 2 "rustc -D warnings above covers non_snake_case and non_camel_case_types; this is the language's own tool, not our rubric"

n=$(grep -cE '^\s+[A-Za-z_]+ [a-z_]+_;' "$CPP" || true)
[ "$n" = 0 ] && term cpp naming 2 "no identifier is mangled to dodge a keyword" \
	|| term cpp naming 1 "$n members carry a trailing underscore to dodge a C++ keyword (requires_) — forced, and it means one field has two names depending on where you stand"

# 2 namespace — the import a stranger types. native-shape section 1.4.
grep -qE '^package [a-z][a-z0-9]*$' "$GO" && term go namespace 2 "package rec" || term go namespace 0 "no package clause a caller can import"
grep -q 'import \*' "$PY" && term python namespace 0 "star import" || term python namespace 2 "a plain module, no star import, no top-level side effect"
grep -q 'module.exports' "$JS" && term javascript namespace 0 "CommonJS in an .mjs file" || term javascript namespace 2 "ES module, named exports only"
grep -qE '^(pub )?fn main' "$RS" && term rust namespace 0 "a binary, not a library" || term rust namespace 2 "every item pub, no main — includes as a module or stands as a crate"
grep -q '#pragma once' "$CPP" && grep -q '^namespace ' "$CPP" \
	&& term cpp namespace 2 "#pragma once and namespace rec" || term cpp namespace 1 "header guard or namespace missing"

# 3 errors — double weight. native-shape section 1.2.
grep -qE '^func Decode\(.*\) \(.*, error\)' "$GO" && term go errors 2 "Decode returns (*Record, error) — Go's convention, and the caller cannot ignore it without saying so" \
	|| term go errors 0 "the public decode does not return an error last"
grep -qE '^class Refusal\((ValueError|Exception|.*Error)\)' "$PY" && term python errors 2 "Refusal subclasses ValueError and is raised, not returned" \
	|| term python errors 0 "the refusal is not an exception"
grep -q 'class Refusal extends Error' "$JS" && term javascript errors 2 "Refusal extends Error and is thrown" \
	|| term javascript errors 0 "the refusal is not an Error subclass"
n=$(grep -cE '\.unwrap\(\)|\.expect\(|panic!' "$RS" || true)
if grep -qE '^pub fn decode\(.*\) -> Result<' "$RS"; then
	[ "$n" = 0 ] && term rust errors 2 "decode returns Result and nothing panics" \
		|| term rust errors 1 "decode returns Result, but $n call sites unwrap() — a Rust reader reads an unwrap as a bug the author has not found yet, and rustc will not warn"
else term rust errors 0 "decode does not return Result"; fi
grep -qE '(struct|class) Refusal : (public )?std::' "$CPP" && term cpp errors 2 "Refusal derives from a std:: exception and is thrown" \
	|| term cpp errors 1 "the thrown type does not derive from std::exception, so a catch(const std::exception&) misses it"

# 4 construction — native-shape section 1.5.
term go construction 2 "struct literal with exported fields; nothing else is idiomatic Go"
grep -q 'def __init__(self, \*\*kw)' "$PY" \
	&& term python construction 0 "__init__(**kw) with kw.get defaults: a misspelt field name is silently dropped and the record is built wrong. @dataclass is the shape a Python reader expects and the compiler-checked one" \
	|| term python construction 2 "named parameters or a dataclass"
grep -q 'export function newRecord' "$JS" && term javascript construction 2 "a factory returning an object literal" || term javascript construction 1 "no factory; the caller writes all sixteen fields"
grep -qE '#\[derive\(.*Default.*\)\]' "$RS" \
	&& term rust construction 1 "Default is derived on the document struct, so ..Default::default() compiles and invents an id — native-shape section 1.5 denies it for exactly this reason" \
	|| term rust construction 2 "struct literal, all fields named, no Default to fall through"
term cpp construction 2 "aggregate initialisation, which is what a C++ reader expects of a plain struct"

# 5 absence — missing against empty, for every field the definition marks
# omit="absent". native-shape section 1.3.
FIELDS=$(sed -n 's/^[[:space:]]*[0-9]*:[[:space:]]*optional[[:space:]]\+[A-Za-z0-9_<>, .]*[[:space:]]\+\([a-z_][a-z0-9_]*\)[[:space:]]*(.*omit = "absent".*/\1/p' "$DEF")
NABS=$(printf '%s\n' "$FIELDS" | grep -c . || true)
pascal() { printf '%s\n' "$1" | awk -F_ '{for(i=1;i<=NF;i++) printf "%s%s", toupper(substr($i,1,1)), substr($i,2); print ""}'; }
absence() {  # absence <lang> <file> <pattern builder>
	lang=$1; file=$2; kind=$3
	miss=""
	for f in $FIELDS; do
		case $kind in
		go) pat="^	$(pascal "$f") +\*" ;;
		rust) pat="^    pub $f: Option<" ;;
		cpp) pat="std::optional<[^>]*> $f;" ;;
		py) pat="self\.$f = kw\.get\(\"$f\", None\)" ;;
		js) pat="$f: null" ;;
		esac
		grep -qE "$pat" "$file" || miss="$miss $f"
	done
	nm=$(printf '%s' "$miss" | wc -w)
	if [ "$nm" = 0 ]; then term "$lang" absence 2 "all $NABS fields the definition marks absent carry the language's own optional"
	else term "$lang" absence 1 "$((NABS - nm)) of $NABS absent fields carry an optional;$miss falls back to a sentinel and cannot say missing apart from empty"; fi
}
absence go "$GO" go
absence python "$PY" py
absence javascript "$JS" js
absence rust "$RS" rust
absence cpp "$CPP" cpp

# 6 opaque — spec and checkpoint are carried verbatim, so the type must be
# bytes with byte equality. native-shape section 0.2.
grep -q '^type Raw = string' "$GO" && term go opaque 1 "Raw is an ALIAS for string: Go strings are bytes and compare by bytes, so the value survives, but an alias is not a type and nothing stops a caller passing a display string where an opaque document belongs" \
	|| term go opaque 2 "a distinct type over bytes"
grep -qE '^pub type Raw = Vec<u8>' "$RS" && term rust opaque 2 "Vec<u8>: bytes, byte equality, no reparse" || term rust opaque 1 "not a byte sequence"
grep -q 'using Raw = std::string' "$CPP" && term cpp opaque 2 "std::string: bytes, byte equality" || term cpp opaque 1 "not a byte sequence"
grep -q 'self.spec = kw.get("spec", "")' "$PY" \
	&& term python opaque 1 "spec defaults to str while raw() also accepts bytes: two types reach the same field and only one round-trips a document that is not valid UTF-8" \
	|| term python opaque 2 "bytes throughout"
grep -q 'spec: ""' "$JS" \
	&& term javascript opaque 0 "a JavaScript string is UTF-16: an unpaired surrogate inside an opaque document is replaced with U+FFFD and the document is destroyed silently. There is no byte-string type in the language" \
	|| term javascript opaque 1 "not a plain string"

# ---- the table -----------------------------------------------------------
if [ "$TSV" = 1 ]; then cat "$RAW"; else awk -f "$HERE/score.awk" "$RAW"; fi
