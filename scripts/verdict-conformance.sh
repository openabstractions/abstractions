#!/usr/bin/env bash
# Do the implementations REFUSE the same documents?
#
# scripts/spec-conformance.sh asks whether three readers understand a spec the
# same way, and answers it by comparing what they print. Everything it can see
# is therefore inside the set all three accept. The disagreements found on
# 2026-09-06 were all outside it: one reader refuses a repeated key the other two
# carry, one turns 1e400 into `Infinity`, the depth caps are 128, a few thousand
# and ten thousand, and the three escape policies are three different sets. Not
# one of those is visible to an instrument that only ever compares output,
# because at least one implementation produces no output at all.
#
# A divergence in the refusal set is a defect even when the strict reader is the
# safer one. Strictness is a cross-language decision or it is a bug: one
# implementation refusing what another accepts does not close a parser
# differential, it turns "the readers disagree about a value" into "two proceed
# and the third cannot open the record" — an availability split bought with a
# security fix, and the record is already written by then.
#
# So this compares the VERDICT, not the output. Two verdicts per input:
#
#   read   the reader's own answer: does it accept this spec at all
#   echo   the spec as this implementation would CARRY it in a record. Go holds
#          it as raw bytes, Python and C++ hold it parsed and re-emit it, so the
#          bytes a downstream reader sees depend on who carried them. Compared
#          compact: whitespace is the record writer's choice, escapes and number
#          spellings are not.
#
# The corpus is download/testdata/verdicts and it only grows. Any input that has
# ever produced a disagreement stays in it, so a divergence closed today cannot
# quietly reopen. Nothing in here is tuned to make the count smaller.
#
#   usage: verdict-conformance.sh
#     env: SPECREAD_CPP    path to the C++ reader (default $ROOT/.build/Release/specread.exe)
#          SPECREAD_EXTRA  one 'name=command' per line, as spec-conformance.sh takes it

set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CORPUS="$ROOT/download/testdata/verdicts"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass=0
fail=0
ok()  { echo "  PASS  $1"; pass=$((pass+1)); }
bad() { echo "  FAIL  $1"; fail=$((fail+1)); }

echo "download spec verdict conformance"

mkdir -p "$WORK/bin"
IMPLS=()
impl() { # impl <name> <command...>
    local name="$1"; shift
    { printf '#!/usr/bin/env bash\nexec'
      for a in "$@"; do printf ' %q' "$a"; done
      printf ' "$@"\n'
    } > "$WORK/bin/$name"
    chmod +x "$WORK/bin/$name"
    IMPLS+=("$name")
}

(cd "$ROOT/download/go" && go build -o "$WORK/specread-go.exe" ./cmd/specread) \
  || { echo "cannot build the Go reader"; exit 1; }
impl go "$WORK/specread-go.exe"

PY=""
for cand in "py -3" python3 python; do
    if $cand -c "pass" >/dev/null 2>&1; then PY="$cand"; break; fi
done
if [ -n "$PY" ]; then
    export PYTHONPATH="$ROOT/job/python"
    impl python $PY "$ROOT/download/python/specread.py"
else
    echo "  (no working python — that implementation is left out)"
fi

WANTED="${SPECREAD_CPP:-$ROOT/.build/Release/specread.exe}"
CPP="$WANTED"
[ -x "$CPP" ] || CPP="$ROOT/.build/specread"
if [ -x "$CPP" ]; then
    impl cpp "$CPP"
else
    echo "  ABSENT  cpp: no reader at $WANTED — set SPECREAD_CPP, or cmake --build to make one."
    echo "          This run compares fewer implementations than the roster of three."
fi

while IFS= read -r line; do
    case "$line" in ""|\#*) continue ;; esac
    eval "cmd=( ${line#*=} )"
    impl "${line%%=*}" "${cmd[@]}"
done <<< "${SPECREAD_EXTRA:-}"

echo "  implementations: ${IMPLS[*]}"
[ "${#IMPLS[@]}" -ge 2 ] || { echo "need at least two implementations to prove anything"; exit 1; }

# A reader that exits 1 has refused; a reader that exits anything else has not
# answered the question. A stack overflow, a usage error and an unhandled
# exception all reach a caller as "not this record" and are the same defect
# wearing different clothes, but they are not a refusal and are not recorded as
# one — Python's RecursionError is a RuntimeError, so a spec Go accepts leaves
# the Python implementation through a channel the contract's callers do not
# catch, and that is exactly the kind of thing folding it into "refuse" hides.
verdict() { # verdict <impl> <mode> <file>
    local name="$1" mode="$2" file="$3" status
    if [ "$mode" = echo ]; then
        "$WORK/bin/$name" --echo "$file" >"$WORK/out" 2>"$WORK/err"
    else
        "$WORK/bin/$name" "$file" >"$WORK/out" 2>"$WORK/err"
    fi
    status=$?
    case "$status" in
        0) echo accept ;;
        1) echo refuse ;;
        *) echo "crash($status)" ;;
    esac
}

# What every implementation said about one input, as one sortable line. The
# whole point of the format is that a divergence reads the same on every run and
# on every machine, so scripts/check.sh can hold the known set and fail on a new
# one AND on one that has silently stopped happening.
for file in "$CORPUS"/*; do
    name="$(basename "$file")"
    for mode in read echo; do
        line=""
        outs=""
        first=""
        agreed=1
        sawaccept=0
        for i in "${IMPLS[@]}"; do
            v="$(verdict "$i" "$mode" "$file")"
            line="$line $i=$v"
            [ -z "$first" ] && first="$v"
            [ "$v" = "$first" ] || agreed=0
            if [ "$v" = accept ]; then
                sawaccept=1
                cp "$WORK/out" "$WORK/accepted.$i"
                outs="$outs $i"
            fi
        done
        if [ "$agreed" != 1 ]; then
            bad "$name $mode: verdicts differ —$line"
            continue
        fi
        if [ "$sawaccept" = 0 ]; then
            ok "$name $mode: every implementation refuses it"
            continue
        fi
        # Agreeing to accept is half an answer. Two readers that accept a
        # document and disagree about what is in it is the older failure, and it
        # is cheap to ask here rather than leaving this corpus half-measured.
        same=1
        base=""
        for i in $outs; do
            if [ -z "$base" ]; then base="$i"; continue; fi
            cmp -s "$WORK/accepted.$base" "$WORK/accepted.$i" || same=0
        done
        if [ "$same" = 1 ]; then
            ok "$name $mode: every implementation accepts it, identically"
        else
            bad "$name $mode: all accept and the results differ —$(for i in $outs; do printf ' %s' "$i"; done)"
            for i in $outs; do
                printf '        %s: %s\n' "$i" "$(head -c 400 "$WORK/accepted.$i" | tr -d '\n')"
            done
        fi
    done
done

# The corpus only grows, and a harness that reads an empty directory is green.
n="$(find "$CORPUS" -type f | wc -l)"
if [ "$n" -ge 83 ]; then
    ok "the corpus holds $n inputs"
else
    bad "the corpus has shrunk to $n inputs — every input that has ever diverged stays in it"
fi

# And a mode a fourth implementer is never told about is a mode they fail on.
# spec-conformance.sh learned this from a stranger's reader failing every check
# for things stated in no document; the same demand applies here.
DOC="$ROOT/download/README.md"
undocumented=""
for k in "--echo" "echo=" "scripts/verdict-conformance.sh" "download/testdata/verdicts" "SPECREAD_EXTRA"; do
    grep -qF -- "$k" "$DOC" || undocumented="$undocumented <$k>"
done
if [ -z "$undocumented" ]; then
    ok "the mode and the corpus this script drives are specified in download/README.md"
else
    bad "driven here and written down nowhere:$undocumented"
fi

echo
echo "----------------------------------------"
echo "  $pass passed, $fail divergent"
[ "$fail" -eq 0 ]
