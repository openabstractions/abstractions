#!/usr/bin/env bash
# The conformance test for the job abstraction, across however many languages
# have an implementation.
#
# One job is passed around a relay. Each implementation in turn finds it as an
# orphan, claims it, advances it by one step, records what it proved, and walks
# away without releasing anything — leaving exactly what a SIGKILL leaves. The
# next implementation picks it up and must continue from the checkpoint its
# predecessor wrote, not from the beginning.
#
# The spec is a shape none of them have types for. That is deliberate: if any
# implementation has to understand the payload to carry the job, the opacity
# claim is false and the layers are not actually separable.
#
# Adding a language means adding one line to IMPLS. Nothing else here changes,
# which is the property being tested — this harness is not written against Go
# and Python, it is written against the record.
#
#   usage: conformance.sh [--canonical] [workdir]
#     workdir      default: a fresh temp dir. A fixed one under $HOME made two
#                  concurrent runs look exactly like a real disagreement.
#     --canonical  only the checkpoint canonical-form section, which needs no
#                  lease expiry and so runs in a second rather than a minute.
#   env:   JOBCTL_CPP=/path/to/jobctl   include a C++ implementation

set -u

CANONICAL_ONLY=0
if [ "${1:-}" = "--canonical" ]; then CANONICAL_ONLY=1; shift; fi

WORK="${1:-}"
[ -n "$WORK" ] || { WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT; }
REPO="$(cd "$(dirname "$0")/.." && pwd)"
export JOB_STORE="$WORK/store"

step() { printf '\n--- %s ---\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*"; exit 1; }

PY=""
for cand in python3 python "py -3"; do
    if $cand -c "pass" >/dev/null 2>&1; then PY="$cand"; break; fi
done
[ -n "$PY" ] || fail "no working python"

rm -rf "$WORK"; mkdir -p "$JOB_STORE"

step "build the implementations"
(cd "$REPO/job/go" && go build -o "$WORK/jobctl-go.exe" ./cmd/jobctl) || fail "go build"

# Every implementation is reached through a one-line shim, so the harness never
# has to know whether one is a binary, an interpreter plus a script, or anything
# else. Adding a language is adding a shim. It also sidesteps the fact that this
# repository's path contains a space, which quietly breaks any scheme that keeps
# a command in a string and expands it unquoted.
mkdir -p "$WORK/bin"
shim() { # shim <name> <command...>
    local name="$1"; shift
    { printf '#!/usr/bin/env bash
exec'
      for a in "$@"; do printf ' %q' "$a"; done
      printf ' "$@"
'
    } > "$WORK/bin/$name"
    chmod +x "$WORK/bin/$name"
    IMPLS+=("$name")
}

IMPLS=()
shim go "$WORK/jobctl-go.exe"
# $PY may itself be two words ("py -3"), which is exactly why this goes through
# the shim rather than into a variable.
shim python $PY "$REPO/job/python/jobctl.py"
if [ -n "${JOBCTL_CPP:-}" ]; then
    if [ -f "$JOBCTL_CPP" ]; then
        shim cpp "$JOBCTL_CPP"
    else
        echo "JOBCTL_CPP=$JOBCTL_CPP is not there — skipping the C++ implementation"
    fi
fi

for n in "${IMPLS[@]}"; do echo "  $n: $(sed -n 2p "$WORK/bin/$n" | cut -c6-)"; done
[ "${#IMPLS[@]}" -ge 2 ] || fail "need at least two implementations to prove anything"

# run <impl-name> <args...>
run() { local n="$1"; shift; "$WORK/bin/$n" "$@"; }

# ---------------------------------------------------------------------------
# CANONICAL FORM — a checkpoint that names WHICH ranges are proven.
#
# One verified_prefix describes a single stream appending to the end of a file
# and nothing else, so a caller fetching sixteen ranged parts at once could not
# be described here at all. The set that fixes it is only worth having if three
# implementations spell the same proven bytes the same way: [[0,4],[4,8]] and
# [[0,8]] are the same bytes, so without merging on ADJACENCY as well as overlap
# the form is not canonical and comparing records byte for byte proves nothing.
# ---------------------------------------------------------------------------
checkpoint_block() {
    awk '/^  "checkpoint": \{/{f=1} f{print} f && /^  \}/{exit}' "$1"
}

canonical_form() {
    step "CANONICAL FORM — every implementation spells one proven set the same way"

    # Five spellings of exactly the same eight proven bytes. The last is what a
    # writer predating ranges leaves behind: it advanced the prefix and left
    # `verified` where it found it, and neither half is wrong.
    local SPELLINGS='{"verified":[[0,4],[4,8]]}
{"verified":[[4,8],[0,4]]}
{"verified":[[0,8]]}
{"verified":[[0,4],[2,8],[8,8]]}
{"verified_prefix":4,"verified":[[4,8]]}'

    local EXPECTED='  "checkpoint": {
    "verified_prefix": 8,
    "verified": [
      [
        0,
        8
      ]
    ]
  },'

    for NAME in "${IMPLS[@]}"; do
        local CID CLAIM E GOT
        CID=$(run "$NAME" submit --kind conformance-kind --spec '{"x":1}' --total 8) \
            || fail "$NAME: submit for the canonical-form check"
        CLAIM=$(run "$NAME" claim "$CID" --owner "$NAME-canon" --ttl 300) \
            || fail "$NAME: claim for the canonical-form check"
        E=$(echo "$CLAIM" | sed 's/.*epoch=\([0-9]*\).*/\1/')

        while IFS= read -r SPELLING; do
            run "$NAME" progress "$CID" --epoch "$E" --done 8 --checkpoint "$SPELLING" >/dev/null \
                || fail "$NAME: could not write $SPELLING"
            GOT=$(checkpoint_block "$JOB_STORE/jobs/$CID.json")
            [ "$GOT" = "$EXPECTED" ] || fail "$NAME wrote $SPELLING as
$GOT
which is not the canonical form
$EXPECTED"
        done <<EOF
$SPELLINGS
EOF
        echo "  $NAME: five spellings of the same eight bytes, one record"

        # A gap is the case a prefix cannot express at all, and the prefix must
        # stay correct beside it so a reader that ignores `verified` still works.
        run "$NAME" progress "$CID" --epoch "$E" \
            --checkpoint '{"verified":[[16,20],[0,4],[4,8]]}' >/dev/null \
            || fail "$NAME: could not write a gapped set"
        GOT=$(checkpoint_block "$JOB_STORE/jobs/$CID.json" | tr -d ' \n')
        [ "$GOT" = '"checkpoint":{"verified_prefix":8,"verified":[[0,8],[16,20]]},' ] \
            || fail "$NAME wrote a gapped set as $GOT"

        # JSON has one number type: an implementation that accepts a fractional
        # offset re-emits it and the record stops comparing equal to itself.
        if run "$NAME" progress "$CID" --epoch "$E" \
            --checkpoint '{"verified":[[0,4194304.5]]}' >/dev/null 2>&1; then
            fail "$NAME accepted a fractional offset"
        fi
        echo "  $NAME: gap kept, prefix still correct, fractional offset refused"
    done

    # And every implementation reads back what any of them wrote.
    local RID RCLAIM RE
    RID=$(run "${IMPLS[0]}" submit --kind conformance-kind --spec '{"x":1}' --total 8)
    RCLAIM=$(run "${IMPLS[0]}" claim "$RID" --owner canon-reader --ttl 300)
    RE=$(echo "$RCLAIM" | sed 's/.*epoch=\([0-9]*\).*/\1/')
    run "${IMPLS[0]}" progress "$RID" --epoch "$RE" --checkpoint '{"verified":[[4,8],[0,4]]}' >/dev/null
    for NAME in "${IMPLS[@]}"; do
        run "$NAME" show "$RID" | tr -d ' \n' | grep -q '"verified":\[\[0,8\]\]' \
            || fail "$NAME cannot read a range set written by ${IMPLS[0]}"
        # Advisory, and declared as content rather than demanded of a reader.
        run "$NAME" show "$RID" | tr -d ' \n' \
            | grep -q '"critical":\["abstraction.job/base@1"\]' \
            || fail "$NAME made the ranges model critical"
    done
    echo "  every implementation reads the set, and none of them demands it"
}

if [ "$CANONICAL_ONLY" = "1" ]; then
    canonical_form
    step "RESULT"
    echo "PASS — the canonical checkpoint form agrees across: ${IMPLS[*]}"
    exit 0
fi

step "the first implementation creates a job none of them understand"
FIRST="${IMPLS[0]}"
SPEC='{"artifact":{"digest":"sha256:abcd","size":900},"sources":[{"scheme":"smb","locator":"//nas/models/x.gguf"},{"scheme":"file","locator":"D:/models/x.gguf"}],"invented_field":{"nested":[1,2,3],"why":"no implementation has a type for this"}}'
ID=$(run "$FIRST" submit --kind conformance-kind --spec "$SPEC" --total 900) || fail "submit"
echo "created by $FIRST: $ID"

# Each hop proves 100 more units and leaves without releasing.
PROVEN=0
HOP=0
for NAME in "${IMPLS[@]}" "${IMPLS[@]}"; do
    CMD="$NAME"
    HOP=$((HOP + 1))

    step "hop $HOP — $NAME finds the orphan and continues it"

    ORPH=$(run "$CMD" orphans) || fail "$NAME: orphans"
    echo "$ORPH" | grep -q "$ID" || fail "$NAME could not see the orphaned job. Saw: ${ORPH:-<nothing>}"

    CLAIM=$(run "$CMD" claim "$ID" --owner "$NAME-worker" --ttl 2) || fail "$NAME: claim"
    echo "  $CLAIM"

    EPOCH=$(echo "$CLAIM" | sed 's/.*epoch=\([0-9]*\).*/\1/')
    [ "$EPOCH" = "$HOP" ] || fail "$NAME claimed at epoch $EPOCH, expected $HOP — the epoch must rise once per claim"

    # What the predecessor proved must have arrived intact.
    GOT=$(echo "$CLAIM" | sed -n 's/.*"verified_prefix":[ ]*\([0-9]*\).*/\1/p')
    [ -n "$GOT" ] || GOT=0
    [ "$GOT" = "$PROVEN" ] || fail "$NAME inherited verified_prefix=$GOT but its predecessor proved $PROVEN"
    echo "  inherited checkpoint: $GOT"

    PROVEN=$((PROVEN + 100))
    # Written is deliberately ahead of proven: the difference is what a crash
    # loses, and no successor may trust it.
    WRITTEN=$((PROVEN + 37))
    run "$CMD" progress "$ID" --epoch "$EPOCH" --done "$WRITTEN" --checkpoint "{\"verified_prefix\":$PROVEN}" >/dev/null \
        || fail "$NAME: progress"
    echo "  wrote $WRITTEN, proved $PROVEN, then walked away without releasing"

    # A stale epoch must be refused once the next owner has taken over. Checked
    # on the next hop by the epoch assertion; here we check it directly.
    if run "$CMD" progress "$ID" --epoch 0 --done 999999 >/dev/null 2>&1; then
        fail "$NAME accepted a write presenting epoch 0"
    fi

    sleep 3   # let the lease lapse, as a killed process's lease does
done

step "every implementation reads the same final record"
for NAME in "${IMPLS[@]}"; do
    CMD="$NAME"
    OUT=$(run "$CMD" show "$ID") || fail "$NAME: show"
    echo "$OUT" | grep -q '"verified_prefix"' || fail "$NAME lost the checkpoint"
    echo "$OUT" | grep -q "invented_field" || fail "$NAME dropped part of the opaque spec"
    echo "$OUT" | grep -q '"nested"' || fail "$NAME flattened the opaque spec"
    echo "  $NAME: reads it, and the spec it never understood is still intact"
done

step "the record on disk is byte-identical whoever wrote it last"
# Every implementation re-encodes what it reads. If two disagree about key order
# or timestamp format they would churn the file against each other forever, and
# no diff of a job's history would mean anything.
BEFORE=$(cat "$JOB_STORE/jobs/$ID.json")
for NAME in "${IMPLS[@]}"; do
    CMD="$NAME"
    # Short lease, and wait it out between implementations. An earlier version
    # claimed for 30s, so every implementation after the first got LeaseHeld,
    # the epoch came back empty, the progress call failed — and the loop still
    # printed "round-trips unchanged" because none of that was checked. A test
    # that cannot fail proves nothing.
    CLAIM=$(run "$CMD" claim "$ID" --owner "$NAME-rewriter" --ttl 2) \
        || fail "$NAME could not claim for the round-trip check"
    E=$(echo "$CLAIM" | sed 's/.*epoch=\([0-9]*\).*/\1/')
    case "$E" in ''|*[!0-9]*) fail "$NAME returned no epoch from claim: $CLAIM" ;; esac

    run "$CMD" progress "$ID" --epoch "$E" --done "$((PROVEN + 37))" --checkpoint "{\"verified_prefix\":$PROVEN}" >/dev/null \
        || fail "$NAME could not write during the round-trip check"
    AFTER=$(cat "$JOB_STORE/jobs/$ID.json")
    # Timestamps legitimately move; everything else must not.
    B=$(echo "$BEFORE" | grep -v "updated_at\|expires_at\|owner\|epoch")
    A=$(echo "$AFTER" | grep -v "updated_at\|expires_at\|owner\|epoch")
    [ "$B" = "$A" ] || fail "$NAME rewrote the record differently:
$(diff <(echo "$B") <(echo "$A") | head -20)"
    echo "  $NAME: round-trips the record unchanged"
    sleep 3   # let this rewriter's lease lapse so the next one can claim
done

canonical_form

# ---------------------------------------------------------------------------
# Intent: what somebody WANTS, written by a party holding no lease.
#
# This is the half of the contract a schema cannot state. Every implementation
# must let an outsider ask, must read back what another language asked for, and
# must agree about which paused jobs a sweep may take.
#
# THAT rule has two halves and this section used to assert only one of them,
# with the sign wrong. job/README.md: a paused job is not an orphan, UNLESS its
# state is still `running` — an owner honouring a pause releases the lease and
# the record stops being running, so a record left running under nobody is an
# owner that died between the pause being asked for and it being carried out,
# and skipping that one strands the job forever because nothing may claim it and
# therefore nothing may resume or cancel it either.
#
# $ID has been running under a lapsed lease since the relay walked away from it,
# which is exactly that case, and the check here demanded the opposite. All
# three implementations were right and the instrument was wrong. So both halves
# are asserted now, each against a record actually in the state it is about.
# ---------------------------------------------------------------------------
step "INTENT — asking without a lease, across implementations"
QUIET=$(run "$FIRST" submit --kind conformance-kind --spec "$SPEC" --total 900) \
    || fail "submit a second job to pause before anybody claims it"
for NAME in "${IMPLS[@]}"; do
    CMD="$NAME"

    run "$CMD" intent "$ID" pause --by "$NAME-bystander" >/dev/null \
        || fail "$NAME: could not set an intent without holding the lease"
    run "$CMD" intent "$QUIET" pause --by "$NAME-bystander" >/dev/null \
        || fail "$NAME: could not pause a job nobody has claimed"

    for OTHER in "${IMPLS[@]}"; do
        OCMD="$OTHER"
        SEEN=$(run "$OCMD" show "$ID" | tr -d ' \n' | grep -o '"want":"[a-z]*"' | head -1)
        [ "$SEEN" = '"want":"pause"' ] \
            || fail "$OTHER cannot read the pause $NAME asked for (saw ${SEEN:-nothing})"
        # Paused and never running: a person stopped it, and adopting it would
        # start the work again seconds later.
        run "$OCMD" orphans | grep -q "$QUIET" \
            && fail "$OTHER lists a paused job as an orphan; a sweep would resume it"
        # Paused and still RUNNING with nobody holding it: the pause was asked
        # for and never carried out. This one is abandoned, and hiding it means
        # nothing may ever claim the job again.
        run "$OCMD" orphans | grep -q "$ID" \
            || fail "$OTHER hides a job left running under nobody; it could never be claimed, resumed or cancelled again"
    done
    echo "  $NAME asked for a pause with no lease; every implementation saw it, left the stopped job alone, and still offered the one nobody carried it out on"

    run "$CMD" intent "$ID" run --by "$NAME-bystander" >/dev/null || fail "$NAME: resume"
done

# And back to something everyone treats as ordinary work.
for OTHER in "${IMPLS[@]}"; do
    run "$OTHER" orphans | grep -q "$ID" \
        || fail "$OTHER does not see a resumed job as available work"
done
echo "  resumed: every implementation offers it for work again"

step "RESULT"
NAMES="${IMPLS[*]}"
cat <<EOF
PASS — implementations: $NAMES

One job went round a relay of ${#IMPLS[@]} implementations, twice. At every hop
the previous owner was gone without releasing anything, the next one found the
job by itself, inherited the checkpoint its predecessor had PROVEN rather than
the larger number it had written, and refused a write presenting a stale epoch.

Every implementation reads the same record, re-encodes it identically, and
carries a spec none of them has a type for.

None of them imports another. None is generated from a shared schema. What they
agree about is the SEMANTICS — a claim is exclusive, an epoch only increases, a
successor resumes only from what a predecessor proved — and this test is the only
thing that checks those, because no schema can state them.

How a record is represented, and how it reaches the other side, is the binding
underneath. These two happen to share a directory. That is not the contract; it
is the one binding both of them have so far.
EOF
