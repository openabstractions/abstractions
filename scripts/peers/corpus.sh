#!/usr/bin/env bash
# The refusal corpus, put to the three peers an adopter actually links.
#
#   sh scripts/peers/corpus.sh [--out DIR]
#
# idl/test/run.ps1 runs idl/test/corpus against five GENERATED readers and
# reports that all five refuse every input with the same word at the same byte.
# That is agreement by descent: the readers and the corpus come from one
# definition. Nothing put the same corpus to job/go, job/python or job/cpp,
# which are the three implementations somebody links, and the first run that
# did found 12, 13 and 7 fixtures where a peer and the corpus disagree.
#
# The C++ peer is run under BOTH installed toolchains because it is two
# artefacts. The same source built by MSVC reads twenty-nine stored records and
# built by g++ refuses twelve of them: libstdc++'s system_clock cannot hold year
# one and MSVC's can, and "0001-01-01T00:00:00.000000Z" is what a Go peer writes
# for a timestamp nobody set. A run naming only "c++" cannot say which.
#
# Exit 0 when every peer's disagreement set is exactly the recorded one, every
# canonically-spelled fixture survives a decode/encode round trip byte for byte,
# and every peer moves the same records on the wire. Otherwise 1. A toolchain
# that is not on this machine is UNPROVEN and never a pass.

set -u

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CORPUS="$ROOT/idl/test/corpus"
BASE="$ROOT/scripts/check.baseline"
OUT=""
while [ $# -gt 0 ]; do
    case "$1" in
        --out) OUT=$2; shift 2 ;;
        *) echo "unknown flag: $1" >&2; exit 2 ;;
    esac
done
[ -n "$OUT" ] || OUT="$(mktemp -d)"
mkdir -p "$OUT"

PY="${ABSTRACTION_PYTHON:-}"
if [ -z "$PY" ]; then
    for c in "${LOCALAPPDATA:-}/Programs/Python/Python312/python.exe" python3 python; do
        command -v "$c" >/dev/null 2>&1 && { PY="$c"; break; }
    done
fi
VCVARS="${VCVARS:-C:\\Program Files\\Microsoft Visual Studio\\18\\Community\\VC\\Auxiliary\\Build\\vcvars64.bat}"

RECORDS_A="$ROOT/job/testdata/ranges-record.json"
RECORDS_B="$ROOT/download/testdata/records"

: > "$OUT/summary"
: > "$OUT/unproven"
: > "$OUT/new"
: > "$OUT/gone"
: > "$OUT/moved"

# An `accept.roundtrip-*` fixture is spelled the way a record is written — two
# space indent, one key per line, trailing newline — so decoding and re-encoding
# it must give back the bytes that went in [JOB-E1] [JOB-E7]. Every other
# fixture is a syntax input: the encoder re-indents it whatever it preserved, so
# comparing bytes there would only measure the indent.
#
# The name says so rather than the shape. `accept.whitespace.json` is also
# multi-line and is deliberately NOT canonical — it is the whitespace-torture
# input — and a shape test read it as a round-trip candidate and failed it in
# all four builds.
ls "$CORPUS" | grep '^accept\.roundtrip-.*\.json$' > "$OUT/canonical" || : > "$OUT/canonical"

# The peer said `ok`; the filename said which word a reader should have said.
# Only accept-or-refuse is compared: the corpus names the refusal word the
# generated readers produce, and a hand-written peer offers no such vocabulary.
record() {  # record <peer> <toolchain key> <toolchain string> <raw output file>
    local peer=$1 key=$2 chain=$3 raw=$4
    awk -F'\t' -v who="$peer/$key" -v rows="$OUT/$peer.$key.rows" \
        -v moved="$OUT/moved.$peer.$key" -v canon="$OUT/canonical" -v summary="$OUT/summary" \
        -v peer="$peer" -v key="$key" -v chain="$chain" '
        BEGIN { while ((getline l < canon) > 0) canonical[l] = 1 }
        $1 == "verdict" {
            n++
            said = ($3 == "ok") ? "ok" : "refused"
            want = ($2 ~ /^accept\./) ? "ok" : "refused"
            if ($3 == "escaped")   { d++; print who "\t" $2 "\tescaped"   > rows }
            else if (want != said) { d++; print who "\t" $2 "\tdisagrees" > rows }
        }
        $1 == "roundtrip" && ($2 in canonical) && $3 != "same" {
            rt++; print who "\t" $2 "\troundtrip" > rows
        }
        $1 == "wire" && $3 == "refused" { wr++; print who "\t" $2 "\twire-refused" > rows }
        $1 == "wire" && $3 == "moved"   { wm++; print who "\t" $2 > moved }
        END {
            printf "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\n",
                   peer, key, chain, n, d, rt, wr, wm >> summary
        }' "$raw"
    [ -f "$OUT/$peer.$key.rows" ] || : > "$OUT/$peer.$key.rows"
    sort -u -o "$OUT/$peer.$key.rows" "$OUT/$peer.$key.rows"
}

run_peer() {  # run_peer <peer> <key> <output file>  — instrument already run
    local peer=$1 key=$2 raw=$3
    local chain; chain=$(awk -F'\t' '$1=="toolchain"{print $2; exit}' "$raw")
    if [ -z "$chain" ]; then
        echo "$peer/$key	the instrument produced no toolchain line" >> "$OUT/unproven"
        return
    fi
    record "$peer" "$key" "$chain" "$raw"
}

# --- go ---------------------------------------------------------------------
# Run from inside job/go so the file compiles in that module's context; there is
# no second module to keep, and no go.mod outside the workspace to go stale.
if command -v go >/dev/null 2>&1; then
    if (cd "$ROOT/job/go" && go run "$ROOT/scripts/peers/verdicts.go" \
            "$CORPUS" "$RECORDS_A" "$RECORDS_B") > "$OUT/go.go.raw" 2> "$OUT/go.go.err"; then
        run_peer go go "$OUT/go.go.raw"
    else
        echo "go/go	$(tail -3 "$OUT/go.go.err" | tr '\n' ' ')" >> "$OUT/unproven"
    fi
else
    echo "go/go	no go on this machine" >> "$OUT/unproven"
fi

# --- python -----------------------------------------------------------------
if [ -n "$PY" ]; then
    if "$PY" "$ROOT/scripts/peers/verdicts.py" "$ROOT" \
            "$CORPUS" "$RECORDS_A" "$RECORDS_B" > "$OUT/python.cpython.raw" 2> "$OUT/python.cpython.err"; then
        run_peer python cpython "$OUT/python.cpython.raw"
    else
        echo "python/cpython	$(tail -3 "$OUT/python.cpython.err" | tr '\n' ' ')" >> "$OUT/unproven"
    fi
else
    echo "python/cpython	no interpreter on this machine" >> "$OUT/unproven"
fi

# --- c++, once per toolchain ------------------------------------------------
SRC_REL="scripts/peers/verdicts.cpp job/cpp/src/json.cpp job/cpp/src/record.cpp job/cpp/src/ranges.cpp"

if [ -f "$VCVARS" ] || [ -f "$(printf '%s' "$VCVARS" | sed 's|\\|/|g; s|^C:|/c|')" ]; then
    B="$OUT/msvc"; mkdir -p "$B"
    {
        echo '@echo off'
        echo "call \"$VCVARS\" >nul 2>&1"
        echo "cd /d \"%~1\""
        printf 'cl /nologo /std:c++17 /EHsc /utf-8 /O2 /I "%%~2\\job\\cpp\\include"'
        for f in $SRC_REL; do printf ' "%%~2\\%s"' "$(printf '%s' "$f" | tr '/' '\\')"; done
        printf ' /Fe:verdicts.exe\n'
    } > "$B/build.bat"
    if cmd //c "$(cygpath -w "$B/build.bat")" "$(cygpath -w "$B")" "$(cygpath -w "$ROOT")" \
            > "$B/build.log" 2>&1 && [ -x "$B/verdicts.exe" ]; then
        "$B/verdicts.exe" "$(cygpath -w "$CORPUS")" "$(cygpath -w "$RECORDS_A")" \
            "$(cygpath -w "$RECORDS_B")" > "$OUT/cpp.msvc.raw" 2>&1
        run_peer cpp msvc "$OUT/cpp.msvc.raw"
    else
        echo "cpp/msvc	build failed, see $B/build.log" >> "$OUT/unproven"
    fi
else
    echo "cpp/msvc	no vcvars64.bat at $VCVARS" >> "$OUT/unproven"
fi

# g++ is reached natively on linux and through WSL on Windows, and the peer
# built by it is a DIFFERENT artefact from the MSVC one, not the same one
# checked twice.
if command -v g++ >/dev/null 2>&1 && [ "$(uname -s 2>/dev/null)" = Linux ]; then
    B="$OUT/gcc"; mkdir -p "$B"
    GSRC=""; for f in $SRC_REL; do GSRC="$GSRC $ROOT/$f"; done
    if g++ -std=c++17 -O1 -I "$ROOT/job/cpp/include" -o "$B/verdicts" $GSRC > "$B/build.log" 2>&1; then
        "$B/verdicts" "$CORPUS" "$RECORDS_A" "$RECORDS_B" > "$OUT/cpp.gcc.raw" 2>&1
        run_peer cpp gcc "$OUT/cpp.gcc.raw"
    else
        echo "cpp/gcc	build failed, see $B/build.log" >> "$OUT/unproven"
    fi
elif command -v wsl.exe >/dev/null 2>&1 && wsl.exe -e sh -c 'command -v g++' >/dev/null 2>&1; then
    W="$(wsl.exe -e wslpath -a "$(cygpath -w "$ROOT")" 2>/dev/null | tr -d '\r')"
    WB="$(wsl.exe -e wslpath -a "$(cygpath -w "$OUT")" 2>/dev/null | tr -d '\r')/gcc"
    if wsl.exe -e sh -c "mkdir -p '$WB' && g++ -std=c++17 -O1 -I '$W/job/cpp/include' -o '$WB/verdicts' '$W/scripts/peers/verdicts.cpp' '$W/job/cpp/src/json.cpp' '$W/job/cpp/src/record.cpp' '$W/job/cpp/src/ranges.cpp'" > "$OUT/gcc-build.log" 2>&1; then
        wsl.exe -e sh -c "'$WB/verdicts' '$W/idl/test/corpus' '$W/job/testdata/ranges-record.json' '$W/download/testdata/records'" > "$OUT/cpp.gcc.raw" 2>&1
        tr -d '\r' < "$OUT/cpp.gcc.raw" > "$OUT/cpp.gcc.raw.lf" && mv "$OUT/cpp.gcc.raw.lf" "$OUT/cpp.gcc.raw"
        run_peer cpp gcc "$OUT/cpp.gcc.raw"
    else
        echo "cpp/gcc	build failed, see $OUT/gcc-build.log" >> "$OUT/unproven"
    fi
else
    echo "cpp/gcc	no g++ on this machine and no wsl.exe carrying one" >> "$OUT/unproven"
fi

# --- what the record says, both ways ----------------------------------------
cat "$OUT"/*.rows 2>/dev/null | sort -u > "$OUT/observed"
grep -a "^peer-corpus	" "$BASE" 2>/dev/null | cut -f2- | sort -u > "$OUT/recorded"
comm -23 "$OUT/observed" "$OUT/recorded" > "$OUT/new"
comm -13 "$OUT/observed" "$OUT/recorded" > "$OUT/gone"

# Every peer that read the wire must have moved the same records. A record that
# moves is one whose declaration the writer re-derives [JOB-D2]; two peers
# deriving different sets is the divergence a per-peer count cannot show.
cat "$OUT"/moved.* 2>/dev/null | cut -f2 | sort -u > "$OUT/moved-any"
MOVED_SPLIT=0
for f in "$OUT"/moved.*; do
    [ -e "$f" ] || continue
    cut -f2 "$f" | sort -u > "$OUT/moved-one"
    cmp -s "$OUT/moved-one" "$OUT/moved-any" || MOVED_SPLIT=$((MOVED_SPLIT + 1))
done

NPEERS=$(wc -l < "$OUT/summary")
NFIX=$(ls "$CORPUS"/*.json 2>/dev/null | wc -l)
NCANON=$(wc -l < "$OUT/canonical")

echo
printf '%-8s %-24s %8s %13s %10s %8s %6s\n' peer toolchain fixtures disagreements roundtrip refused moved
while IFS=$'\t' read -r peer key chain n disagree rt wr wm; do
    printf '%-8s %-24s %8d %13d %10d %8d %6d\n' "$peer" "$chain" "$n" "$disagree" "$rt" "$wr" "$wm"
done < "$OUT/summary"
while IFS=$'\t' read -r who why; do
    printf 'UNPROVEN %-16s %s\n' "$who" "$why"
done < "$OUT/unproven"
echo
echo "corpus: $NFIX fixtures, $NCANON of them accept.roundtrip-* and compared byte for byte"
echo "counts: peer_corpus_peers $NPEERS"
echo "counts: peer_corpus_disagreements $(awk -F'\t' '{s+=$5} END {print s+0}' "$OUT/summary")"
echo "counts: peer_corpus_roundtrip_differs $(awk -F'\t' '{s+=$6} END {print s+0}' "$OUT/summary")"
echo "counts: peer_corpus_wire_refused $(awk -F'\t' '{s+=$7} END {print s+0}' "$OUT/summary")"

STATUS=0
# A rule whose input set is empty passes on nothing. Three peers exist and one
# of them needs no toolchain nobody has; a run that measured fewer than two, or
# a corpus that lost its fixtures, has not tested anything.
if [ "$NPEERS" -lt 2 ] || [ "$NFIX" -lt 50 ] || [ "$NCANON" -lt 1 ]; then
    echo "FAIL vacuity — $NPEERS peers over $NFIX fixtures, $NCANON canonical; this rule cannot fail on fewer than 2, 50 and 1"
    STATUS=1
fi
if [ -s "$OUT/new" ]; then
    echo "FAIL $(wc -l < "$OUT/new") peer answers scripts/check.baseline does not record:"
    sed 's/^/     /' "$OUT/new"
    STATUS=1
fi
if [ -s "$OUT/gone" ]; then
    echo "FAIL $(wc -l < "$OUT/gone") recorded peer answers that stopped happening — drop them from scripts/check.baseline:"
    sed 's/^/     /' "$OUT/gone"
    STATUS=1
fi
if [ "$MOVED_SPLIT" -gt 0 ]; then
    echo "FAIL $MOVED_SPLIT of $NPEERS peers moved a different set of stored records from the rest"
    STATUS=1
fi
[ "$STATUS" = 0 ] && echo "PASS every peer answered the corpus exactly as recorded, and moved one set of records"
exit "$STATUS"
