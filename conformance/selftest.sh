#!/bin/sh
set -u

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
RUN="$HERE/run.sh"
CORPUS="$HERE/scenarios"
[ -d "$CORPUS" ] || CORPUS="$HERE/../openabstractions-flat/abstraction-download/testdata/scenarios"
TOY="$HERE/selftest/capabilities.list"
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
    refused=$1; kept=$2; unreached=$3; label=$4; shift 4
    out=$("$@" 2>&1)
    r=$(printf '%s\n' "$out" | grep -c '^  FAIL  ')
    k=$(printf '%s\n' "$out" | grep -c '^  PASS  ')
    u=$(printf '%s\n' "$out" | grep -c '^  ----  ')
    if [ "$r" = "$refused" ] && [ "$k" = "$kept" ] && [ "$u" = "$unreached" ]; then
        echo "  ok    $label ($r refused, $k held, $u out of reach)"
    else
        echo "  BAD   $label: expected $refused refused, $kept held, $unreached out of reach; got $r, $k, $u"
        bad=1
    fi
}

says() {
    want=$1; label=$2; shift 2
    if "$@" 2>&1 | grep -q -- "$want"; then
        echo "  ok    $label"
    else
        echo "  BAD   $label: the run never said \"$want\""
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
counts 4 1 0 "a superset, a stray field, a negation and a reordering are all refused" \
    env CAPABILITIES_LIST="$TOY" sh "$RUN" --scenarios "$HERE/selftest" -- sh "$HERE/selftest/generous.sh"
counts 0 0 2 "an operation no capability places is out of reach, never a pass" \
    sh "$RUN" --scenarios "$HERE/selftest" -- sh "$HERE/selftest/generous.sh"
counts 0 1 1 "a driver declaring only identity is judged on identity alone" \
    sh "$RUN" --scenarios "$HERE/selftest/layers" -- sh "$HERE/selftest/identity.sh"
says "orphans: UNREACHABLE — the driver does not declare store" "and the scenario it cannot run names what it lacks" \
    sh "$RUN" --scenarios "$HERE/selftest/layers" -- sh "$HERE/selftest/identity.sh"
check 2 "a rule tag with no contract page is incomplete, never a pass" \
    sh "$RUN" --scenarios "$HERE/selftest/tagged" --contracts "$HERE/selftest/absent" \
        -- sh "$HERE/selftest/generous.sh"
check 0 "the same run, with the page that declares the tag, is green" \
    sh "$RUN" --scenarios "$HERE/selftest/tagged" --contracts "$HERE/selftest/pages" \
        -- sh "$HERE/selftest/generous.sh"

[ "$bad" = 0 ] || { echo "  RESULT: the runner does not behave as documented"; exit 1; }
echo "  RESULT: the runner distinguishes passed, failed and out of reach"
