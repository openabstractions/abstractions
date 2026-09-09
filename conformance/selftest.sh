#!/bin/sh
set -u

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
RUN="$HERE/run.sh"
CORPUS="$HERE/scenarios"
[ -d "$CORPUS" ] || CORPUS="$HERE/../download/testdata/scenarios"
bad=0

check() {
    want=$1; label=$2; shift 2
    "$@" > /dev/null 2>&1
    got=$?
    if [ "$got" = "$want" ]; then
        echo "  ok    $label (exit $got)"
    else
        echo "  BAD   $label: expected exit $want, got $got"
        bad=1
    fi
}

counts() {
    refused=$1; kept=$2; label=$3; shift 3
    out=$("$@" 2>&1)
    r=$(printf '%s\n' "$out" | grep -c '^  FAIL  ')
    k=$(printf '%s\n' "$out" | grep -c '^  PASS  ')
    if [ "$r" = "$refused" ] && [ "$k" = "$kept" ]; then
        echo "  ok    $label ($r refused, $k held)"
    else
        echo "  BAD   $label: expected $refused refused and $kept held, got $r and $k"
        bad=1
    fi
}

echo "conformance selftest"
check 3 "a driver that declares nothing is not set up" \
    sh "$RUN" --scenarios "$CORPUS" --only already-here -- sh "$HERE/selftest/mute.sh"
check 1 "a driver that answers ok to everything fails" \
    sh "$RUN" --scenarios "$CORPUS" --only already-here -- sh "$HERE/selftest/liar.sh"
check 2 "a rule out of reach is incomplete, never a pass" \
    sh "$RUN" --scenarios "$CORPUS" --only wire-plain --no-fixture -- sh "$HERE/selftest/narrow.sh"
counts 3 1 "a superset, a stray field and a negation are all refused" \
    sh "$RUN" --scenarios "$HERE/selftest" -- sh "$HERE/selftest/generous.sh"
check 2 "a rule tag with no contract page is incomplete, never a pass" \
    sh "$RUN" --scenarios "$HERE/selftest/tagged" --contracts "$HERE/selftest/absent" \
        -- sh "$HERE/selftest/generous.sh"
check 0 "the same run, with the page that declares the tag, is green" \
    sh "$RUN" --scenarios "$HERE/selftest/tagged" --contracts "$HERE/selftest/pages" \
        -- sh "$HERE/selftest/generous.sh"

[ "$bad" = 0 ] || { echo "  RESULT: the runner does not behave as documented"; exit 1; }
echo "  RESULT: the runner distinguishes passed, failed and out of reach"
