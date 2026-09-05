#!/usr/bin/env bash
# Do three implementations read a download SPEC the same way?
#
# scripts/conformance.sh already asks this of the job RECORD, and answers it by
# passing one record around and requiring byte-identical re-encoding. The spec
# inside that record had no such test, and could not have one of the same shape:
# the job layer refuses to look inside a spec, which is precisely what lets
# download grow mirrors and chunk manifests without a schema change in three
# languages.
#
# The opacity is right. The hole it leaves is real, and it cost something. One
# implementation wrote a digest bare; another built "sha256:" + hex and compared
# strings. The error said:
#
#     got sha256:1fc70f…  want 1fc70f…
#
# — the same digest, twice — and because a mismatch deletes the partial, a
# correct 1.5 GB download was thrown away and fetched again.
#
# So the record's conformance is by identical BYTES and the spec's is by
# identical MEANING. Each implementation prints what it understood, and they must
# all print the same thing.
#
#   usage: spec-conformance.sh
#     env: SPECREAD_CPP  path to the C++ reader (default /c/jobbuild/spec/specread.exe)

set -u

# The layers live in their own repositories now. scripts/layers.sh checks them
# out and assembles .tree, which has the single-tree shape these harnesses were
# written against. Falls back to this repository so the script still works if
# somebody arranges the tree themselves.
ROOT="${ABSTRACTION_TREE:-$(cd "$(dirname "$0")/.." && pwd)}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass=0
fail=0
ok()  { echo "  PASS  $1"; pass=$((pass+1)); }
bad() { echo "  FAIL  $1"; fail=$((fail+1)); }

PY=py
command -v py >/dev/null 2>&1 || PY=python3

echo "download spec conformance"

# --- the implementations ---------------------------------------------------
(cd "$ROOT/download/go" && go build -o "$WORK/specread-go.exe" ./cmd/specread) \
  || { echo "cannot build the Go reader"; exit 1; }

IMPLS="go python"
CPP="${SPECREAD_CPP:-/c/jobbuild/spec/specread.exe}"
if [ -x "$CPP" ]; then
    IMPLS="$IMPLS cpp"
else
    echo "  (no C++ reader at $CPP — build it to include the third implementation)"
fi
echo "  implementations: $IMPLS"

read_spec() { # read_spec <impl> <file>
    case "$1" in
        go)     "$WORK/specread-go.exe" "$2" ;;
        python) PYTHONPATH="$ROOT/job/python" "$PY" -3 "$ROOT/download/python/specread.py" "$2" ;;
        cpp)    "$CPP" "$2" ;;
    esac
}

# --- every implementation must agree about every fixture -------------------
for spec in "$ROOT"/download/testdata/specs/*.json; do
    name="$(basename "$spec" .json)"
    first=""
    agreed=1
    for impl in $IMPLS; do
        out="$(read_spec "$impl" "$spec" 2>&1)"
        if [ -z "$out" ]; then
            bad "$name: $impl produced nothing"
            agreed=0
            continue
        fi
        if [ -z "$first" ]; then
            first="$out"
            firstimpl="$impl"
            continue
        fi
        if [ "$out" != "$first" ]; then
            bad "$name: $impl and $firstimpl disagree"
            diff <(echo "$first") <(echo "$out") | sed 's/^/        /'
            agreed=0
        fi
    done
    [ "$agreed" = "1" ] && ok "$name: all implementations read it the same way"
done

# --- and the specific disagreement that cost a download --------------------
#
# canonical.json and bare-digest.json name the same artifact and differ only in
# whether the digest carries its label. Every implementation must resolve them
# to the same thing — that is the whole bug, pinned.
for impl in $IMPLS; do
    a="$(read_spec "$impl" "$ROOT/download/testdata/specs/canonical.json" | grep '^digest=')"
    b="$(read_spec "$impl" "$ROOT/download/testdata/specs/bare-digest.json" | grep '^digest=')"
    if [ "$a" = "$b" ] && [ "$a" != "digest=" ]; then
        ok "$impl: a labelled and an unlabelled digest name the same artifact"
    else
        bad "$impl: reads them as different artifacts ($a vs $b)"
    fi
done

# --- an unreadable digest must not read as "matches anything" --------------
cat > "$WORK/rubbish.json" <<'EOF'
{"artifact":{"digest":"not-a-digest"},"sources":[{"scheme":"https","locator":"https://example.invalid/x"}],"sink":{"final":"x"}}
EOF
for impl in $IMPLS; do
    if [ "$(read_spec "$impl" "$WORK/rubbish.json" | grep '^digest=')" = "digest=" ]; then
        ok "$impl: refuses to interpret a malformed digest"
    else
        bad "$impl: invented a meaning for a malformed digest"
    fi
done

echo
echo "----------------------------------------"
echo "  $pass passed, $fail failed"
[ "$fail" -eq 0 ]
