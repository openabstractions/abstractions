#!/usr/bin/env bash
# Do the implementations DO the same thing?
#
# spec-conformance.sh asks whether they READ a spec the same way, and answers by
# comparing the lines each one prints. Nothing asked what they do to a store over
# time, and two divergences lived under that gap for as long as they existed: one
# implementation swept a paused job into its orphan list and the others did not,
# and one honoured a pause on adoption while another downloaded straight through
# it. Neither is visible to any test that compares vocabulary. We were claiming
# conformance of behaviour and measuring conformance of words.
#
# So: a scenario goes in, a transcript comes out, and a diff is a defect. Each
# implementation applies the same scripted operations to a store of its own and
# prints what an observer would have seen after every one.
#
#   usage: behaviour-conformance.sh [scenario-name ...]
#     env: REPLAY_CPP  path to the C++ driver (default $ROOT/.build/Release/replay.exe)
#
# WHAT THE TRANSCRIPT CARRIES, and why it is this and not more.
#
# Every line pinned here is a line three languages must agree on forever, so the
# test is not "can we observe it" but "would two honest implementers diverge, and
# would one of them be wrong". The fields are the state, the epoch, whether a
# lease is live, what somebody asked for, the units done, whether an error was
# recorded, the checkpoint, and what the record declares it carries. Nothing
# else.
#
# The declaration is here because a record that names a data model it does not
# carry is wrong in a way no other field can show: the checkpoint reads the same
# whether the writer had no holes to report or had never heard of holes, and the
# declaration is the only thing that tells those apart. One implementation
# carried a stale one for as long as it existed and every transcript above was
# identical anyway.
#
# Deliberately absent:
#
#   error MESSAGES        the class of refusal is a contract; its wording is not,
#                         and pinning wording makes every improved message a
#                         cross-language breakage.
#   timestamps, ids,      three implementations cannot agree on a clock, and
#   owner strings         nothing branches on these.
#   mid-transfer progress advisory. Pinning it makes buffer sizes a contract.
#   concurrent writers    non-deterministic by construction, so no transcript can
#                         be compared. That property belongs in each language's
#                         own tests -- no lost update, no repeated epoch -- and
#                         a flaky cross-language check would only teach everyone
#                         to ignore a red line.
#
# WHAT A READER WILL REFUSE, before any scenario runs. A record names the data
# models it carries and the subset a reader must understand or refuse it, and
# every one of those names had been understood by every reader since the format
# existed -- so the refusal was unreachable, untested, and diverged three ways
# the moment a scenario forged a name nobody knew. Each driver answers --models
# with its roster, the rosters are diffed against each other and against the
# contract pages, and the names print on a green run.
#
# WHAT AN ABSENCE MEANS HERE. The roster is fixed at three. A language that
# cannot run a scenario is named, counted, and printed as a gap -- never skipped
# quietly, because a gap that reads as a pass is the defect this exists to fix.
#
# WHAT THE CONTRACT SAYS, as a fourth party. Diffing three transcripts against
# each other catches disagreement and is blind to agreed wrongness: three
# implementations transcribed from one author agree about the unwritten parts by
# descent, and a rule all three break the same way produces three identical
# transcripts and a green line. So a scenario may also carry what SHOULD happen:
#
#   # expect 6: state=cancelled
#   # expect 6: done=0
#
# One phrase per line, keyed by the step it is about, matched against that step's
# transcript line at whitespace boundaries. Partial on purpose. A whole expected
# line would re-pin the fields nobody decided -- the epoch, the exact spelling of
# an empty checkpoint -- and that is the disease, not the cure. A scenario asserts
# the fields a written rule settles and stays silent about the rest, and the
# silence is counted and printed below rather than passing for agreement.
#
# WHICH RULES NOBODY WROTE A SCENARIO FOR. Making the contract a fourth party
# did not close the gap; it moved it. A scenario can now say what SHOULD happen,
# and nothing knew which rules had no scenario at all -- so the one rule somebody
# happened to write down was checked and the rest were invisible to the very
# instrument built to check them. Same structural bias as everything above, one
# level up, and now living inside the fix for it.
#
# So every invariant on the two contract pages carries a tag, and an expectation
# cites the tag it tests:
#
#   # expect 6: terminal [JOB-T1]
#
# This script reads the tags off the pages, reads the tags the scenarios cite,
# and prints the difference. Nothing is generated from a tag -- three
# implementations generated from one file would agree by construction, which is
# the whole reason three hand-written ones are worth having. It is an accounting
# device and it costs one pass over two markdown files.
#
# A citation counts only when the scenario actually ran, its assertions held, and
# at least two implementations produced the transcript. A rule exercised by an
# implementation agreeing with itself is not exercised.
#
# AND THE CELLS NOBODY DECIDED. A scenario may also mark a step the contract does
# not settle:
#
#   # undecided 12: no page says whether renew is refused on a lapsed lease
#
# which asserts nothing and is printed. Three implementations agreeing about a
# cell no page decides is where `renew` was hiding for as long as it existed.

set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap '[ -n "${FIXTURE_PID:-}" ] && kill "$FIXTURE_PID" 2>/dev/null; rm -rf "$WORK"' EXIT

ROSTER="go python cpp"

agree=0
diverge=0
broke=0
GAPS=""
SILENT=""
silent=0
UNDECIDED=""
undecided=0
DECLARED="$WORK/invariants.declared"
CITED="$WORK/invariants.cited"
: > "$CITED"

echo "download behaviour conformance"

# --- the implementations, one line each ------------------------------------
#
# Reached through a shim, exactly as the other two harnesses reach theirs, so
# this one never has to know whether an implementation is a binary or an
# interpreter plus a script. It also sidesteps this repository's path containing
# a space, which quietly breaks any scheme that keeps a command in a string.
mkdir -p "$WORK/bin"
CAPS_go=""; CAPS_python=""; CAPS_cpp=""
MISSING_go=""; MISSING_python=""; MISSING_cpp=""

impl() { # impl <name> <command...>
    local name="$1"; shift
    { printf '#!/usr/bin/env bash\nexec'
      for a in "$@"; do printf ' %q' "$a"; done
      printf ' "$@"\n'
    } > "$WORK/bin/$name"
    chmod +x "$WORK/bin/$name"
    local caps
    caps="$("$WORK/bin/$name" --capabilities 2>"$WORK/caps.err")"
    if [ -z "$caps" ]; then
        eval "MISSING_$name=\"declares no capabilities (\$(head -1 "\$WORK/caps.err"))\""
        return
    fi
    eval "CAPS_$name=\"\$caps\""
}

PY=""
for cand in "py -3" python3 python; do
    if $cand -c "pass" >/dev/null 2>&1; then PY="$cand"; break; fi
done

# --- the internet, made deterministic --------------------------------------
#
# Every scenario used to read a file and no driver could make an HTTP request,
# so the wire behaviour of three independently written HTTP clients had never
# been compared at all — on the one surface that faces the internet.
#
# One fixture answers every wire scenario. A scenario names the behaviour it
# wants inside the URL, so a new wire case costs a fixture answer and a scenario
# and changes no driver in any language. The run token keeps two drivers reading
# the same behaviour out of each other's way.
#
# Started BEFORE the implementations are registered, because a driver decides
# whether it can claim `wire` by looking for this, and a capability probed
# before the server exists would say no.
FIXTURE=""
FIXTURE_PID=""
if [ -n "$PY" ]; then
    $PY "$ROOT/download/testdata/fixture.py" >"$WORK/fixture.url" 2>"$WORK/fixture.err" &
    FIXTURE_PID=$!
    for _ in $(seq 100); do
        FIXTURE="$(cat "$WORK/fixture.url" 2>/dev/null)"
        [ -n "$FIXTURE" ] && break
        sleep 0.05
    done
fi
export ABSTRACTION_FIXTURE="$FIXTURE"

if (cd "$ROOT/download/go" && go build -o "$WORK/replay-go.exe" ./cmd/replay 2>"$WORK/gobuild.err"); then
    impl go "$WORK/replay-go.exe"
else
    MISSING_go="will not build: $(head -1 "$WORK/gobuild.err")"
fi

if [ -n "$PY" ]; then
    export PYTHONPATH="$ROOT/job/python:$ROOT/watch/python"
    impl python $PY "$ROOT/download/python/replay.py"
else
    MISSING_python="no working python on this machine"
fi

CPP="${REPLAY_CPP:-$ROOT/.build/Release/replay.exe}"
[ -x "$CPP" ] || CPP="$ROOT/.build/replay"
if [ -x "$CPP" ]; then
    impl cpp "$CPP"
else
    MISSING_cpp="not built — set REPLAY_CPP, or run cmake --build to make it"
fi

for name in $ROSTER; do
    eval "caps=\$CAPS_$name; gone=\$MISSING_$name"
    if [ -n "$gone" ]; then
        echo "  $name: ABSENT — $gone"
    else
        echo "  $name: $caps"
    fi
done
if [ -n "$FIXTURE" ]; then
    echo "  fixture: $FIXTURE"
else
    echo "  fixture: ABSENT — $(head -1 "$WORK/fixture.err" 2>/dev/null || echo 'no python to run download/testdata/fixture.py')"
fi

# A scenario an implementation cannot reach is counted below as UNPROVEN, one
# behaviour at a time, and for months that was the only trace of a whole verb
# missing from a whole language: cpp printed `store` beside go's `store
# transfer` on this very line and nothing compared the two strings. A roster
# that does not declare the same capabilities is not three implementations of
# one contract, whatever the scenarios say.
present=""; sets=""
for name in $ROSTER; do
    eval "caps=\$CAPS_$name; gone=\$MISSING_$name"
    [ -n "$gone" ] && continue
    present="$present $name"
    sets="$sets$name=$(echo $caps | tr ' ' '\n' | sort | paste -sd,)
"
done
if [ "$(echo $present | wc -w)" -lt 2 ]; then
    echo "  ----  capabilities: only$present declared any — one roster agreeing with itself proves nothing"
    GAPS="$GAPS
    capabilities: fewer than two implementations declared any"
elif [ "$(echo "$sets" | sed 's/^[a-z+]*=//' | sort -u | grep -c .)" = 1 ]; then
    echo "  PASS  capabilities: identical in$present"
    agree=$((agree+1))
else
    echo "  FAIL  capabilities:$(echo "$sets" | grep . | tr '\n' ' ')"
    diverge=$((diverge+1))
fi

# --- the content-set roster, from three readers and from the page ----------
#
# A record declares the data models it carries and the subset a reader MUST
# understand or refuse it, and nothing had ever compared the three lists of
# names those refusals are decided against. Three readers that do not know the
# same names are not three implementations of one contract: one host refuses a
# record the next one works on, and every transcript below stays identical
# because no scenario can name a model somebody is missing.
#
# The page is the fourth party here as everywhere else. A name every reader
# knows and no contract page declares is a private agreement between three
# implementations by the same hand; a name a page declares and no reader knows
# is a rule a stranger is held to and we are not.
: > "$WORK/models.rosters"
declared=""
for name in $ROSTER; do
    eval "gone=\$MISSING_$name"
    [ -n "$gone" ] && continue
    "$WORK/bin/$name" --models 2>/dev/null | sort > "$WORK/models.$name"
    if [ ! -s "$WORK/models.$name" ]; then
        GAPS="$GAPS
    models: $name answers no --models roster, so its refusals are unreadable"
        continue
    fi
    declared="$declared $name"
    echo "$name=$(paste -sd, - < "$WORK/models.$name")" >> "$WORK/models.rosters"
done
if [ "$(echo $declared | wc -w)" -lt 2 ]; then
    echo "  ----  models: only$declared answered — one roster agreeing with itself proves nothing"
    GAPS="$GAPS
    models: fewer than two implementations declared a content-set roster"
elif [ "$(sed 's/^[a-z+]*=//' "$WORK/models.rosters" | sort -u | grep -c .)" != 1 ]; then
    # The disputed lines and who declared them, never three whole rosters. A
    # failure nobody can read is a failure nobody acts on, and a roster is six
    # lines of URL-shaped names.
    echo "  FAIL  models: the rosters are not one set"
    : > "$WORK/models.all"
    for name in $declared; do cat "$WORK/models.$name" >> "$WORK/models.all"; done
    for line in $(sort -u "$WORK/models.all" | tr ' ' '\036'); do
        entry="$(echo "$line" | tr '\036' ' ')"
        who=""
        for name in $declared; do
            grep -qxF "$entry" "$WORK/models.$name" && who="$who $name"
        done
        [ "$(echo $who | wc -w)" = "$(echo $declared | wc -w)" ] && continue
        echo "        $entry — only in$who"
    done
    diverge=$((diverge+1))
else
    cut -d' ' -f1 "$WORK/models.$(echo $declared | cut -d' ' -f1)" | sort -u > "$WORK/models.known"
    grep -ohE 'abstraction\.[a-z]+/[a-z]+@[0-9]+' \
        "$ROOT/job/README.md" "$ROOT/download/README.md" | sort -u > "$WORK/models.page"
    unwritten="$(comm -23 "$WORK/models.known" "$WORK/models.page" | paste -sd' ' -)"
    unbuilt="$(comm -13 "$WORK/models.known" "$WORK/models.page" | paste -sd' ' -)"
    if [ -n "$unwritten$unbuilt" ]; then
        echo "  FAIL  models:${unwritten:+ read by every implementation and on no contract page: $unwritten}${unbuilt:+ declared on a contract page and read by nobody: $unbuilt}"
        diverge=$((diverge+1))
    else
        echo "  PASS  models: $(grep -c . "$WORK/models.known") names, identical in$declared and on the contract pages"
        agree=$((agree+1))
        sed 's/^/          /' "$WORK/models.$(echo $declared | cut -d' ' -f1)"
    fi
fi
echo

# --- one scenario, every implementation that can run it --------------------
#
# Independent stores, on purpose. The relay in scripts/conformance.sh passes ONE
# record between languages and proves they can continue each other's work; this
# proves something the relay cannot, which is that they would each have done the
# same thing on their own. A shared store would hide exactly that: an
# implementation that never reaches a branch inherits its predecessor's answer.
gap() { GAPS="$GAPS
    $1"; }

# invariants reads one contract page and prints "<tag><tab><the sentence it
# ends>". A page wraps its prose, so the unit is the paragraph rather than the
# line -- except a table row and a heading, which are each a claim of their own.
invariants() {
    awk '
    function emit(block,   before, cut, i, tag, sent) {
        gsub(/\n/, " ", block)
        while (match(block, /\[(JOB|DL|WATCH)-[A-Z][A-Z0-9]*\]/)) {
            tag = substr(block, RSTART + 1, RLENGTH - 2)
            before = substr(block, 1, RSTART - 1)
            sub(/[.:;,] *$/, "", before)
            cut = 0
            for (i = 1; i < length(before); i++)
                if (substr(before, i, 2) == ". ") cut = i + 1
            sent = substr(before, cut + 1)
            gsub(/\[(JOB|DL|WATCH)-[A-Z][A-Z0-9]*\]/, "", sent)
            gsub(/[*`~>|]/, "", sent)
            sub(/^ *#+ */, "", sent)
            sub(/^[ \t,;:—–-]+ */, "", sent)
            gsub(/  +/, " ", sent)
            sub(/ +$/, "", sent)
            if (length(sent) > 92) sent = substr(sent, 1, 89) "..."
            print tag "\t" sent
            block = substr(block, RSTART + RLENGTH)
        }
    }
    /^```/          { fence = !fence; emit(buf); buf = ""; next }
    fence           { next }
    /^$/            { emit(buf); buf = ""; next }
    /^[|#]/         { emit(buf); buf = ""; emit($0); next }
                    { buf = buf " " $0 }
    END             { emit(buf) }
    ' "$1"
}

: > "$DECLARED"
for page in "$ROOT/job/README.md" "$ROOT/download/README.md" "$ROOT/watch/README.md"; do
    invariants "$page" >> "$DECLARED"
done
dupes="$(cut -f1 "$DECLARED" | sort | uniq -d)"
if [ -n "$dupes" ]; then
    echo "  FAIL  a tag is declared twice on the contract pages: $(echo $dupes)"
    diverge=$((diverge+1))
fi

# against_contract checks one transcript against what the scenario says should
# have happened. Every failure is printed; the first one is not the interesting
# one when a rule moved.
against_contract() {
    local file="$1" out="$2" impl="$3" name="$4" bad=0
    local n phrase line tail
    while read -r n phrase; do
        [ -n "$n" ] || continue
        line="$(sed -n "${n}p" "$out")"
        tail="${line#* -> }"
        case " $tail " in
            *" $phrase "*) continue ;;
        esac
        echo "  FAIL  $name: $impl step $n CONTRADICTS the contract"
        echo "        expected: $phrase"
        echo "        observed: ${tail:-<no such step>}"
        bad=1
    done <<EOF
$(sed -n 's/^# *expect  *\([0-9][0-9]*\) *: */\1 /p' "$file" |
  sed 's/ *\[\(JOB\|DL\|WATCH\)-[A-Z][A-Z0-9]*\]//g')
EOF
    return $bad
}

run_scenario() {
    local file="$1"
    local name; name="$(basename "$file" .txt)"
    local need; need="$(sed -n 's/^# requires: *//p' "$file" | head -1)"
    [ -n "$need" ] || need=store

    if ! grep -q '^# *expect  *[0-9]' "$file"; then
        SILENT="$SILENT
    $name"
        silent=$((silent+1))
    fi

    local cell
    while read -r cell; do
        [ -n "$cell" ] || continue
        UNDECIDED="$UNDECIDED
    $name step $cell"
        undecided=$((undecided+1))
    done <<EOF
$(sed -n 's/^# *undecided  *\([0-9][0-9]*\) *: */\1 — /p' "$file")
EOF

    local ran="" first="" firstimpl="" ok=1 kept=1
    for impl in $ROSTER; do
        eval "caps=\$CAPS_$impl; gone=\$MISSING_$impl"
        if [ -n "$gone" ]; then
            gap "$name: $impl is ABSENT ($gone)"
            continue
        fi
        case " $caps " in
            *" $need "*) ;;
            *) gap "$name: $impl cannot do '$need' — this behaviour is UNPROVEN in $impl"
               continue ;;
        esac

        local w="$WORK/$name.$impl"
        mkdir -p "$w/store"
        local out="$WORK/t.$name.$impl"
        local base=""
        [ -n "$FIXTURE" ] && base="$FIXTURE/$name.$impl"
        if ! ABSTRACTION_FIXTURE="$base" "$WORK/bin/$impl" "$w" "$file" >"$out" 2>"$out.err"; then
            echo "  FAIL  $name: $impl exited non-zero ($(head -1 "$out.err"))"
            ok=0
            continue
        fi
        if LC_ALL=C grep -qU $'\r' "$out"; then
            echo "  FAIL  $name: $impl writes CR — this transcript is LF only"
            ok=0
            continue
        fi
        ran="$ran $impl"
        against_contract "$file" "$out" "$impl" "$name" || kept=0
        if [ -z "$first" ]; then first="$out"; firstimpl="$impl"; continue; fi
        if ! cmp -s "$first" "$out"; then
            echo "  FAIL  $name: $impl and $firstimpl DIVERGE"
            diff "$first" "$out" | sed 's/^/        /'
            ok=0
        fi
    done

    local n; n="$(echo $ran | wc -w)"
    [ "$kept" = 1 ] || broke=$((broke+1))
    [ "$ok" = 1 ] || diverge=$((diverge+1))
    if [ "$kept" != 1 ] || [ "$ok" != 1 ]; then
        return
    fi
    if [ "$n" -lt 2 ]; then
        echo "  ----  $name: only$ran ran — one implementation agreeing with itself proves nothing"
        gap "$name: fewer than two implementations ran it"
        return
    fi
    agree=$((agree+1))
    grep '^# *expect  *[0-9]' "$file" |
        grep -o '\[\(JOB\|DL\|WATCH\)-[A-Z][A-Z0-9]*\]' |
        tr -d '[]' >> "$CITED"
    local said; said="$(grep -c '^# *expect  *[0-9]' "$file")"
    if [ "$said" -gt 0 ]; then
        echo "  PASS  $name: identical transcript in$ran, $said against the contract"
    else
        echo "  PASS  $name: identical transcript in$ran, nothing asserted"
    fi
}

want="${*:-}"
for f in "$ROOT"/download/testdata/scenarios/*.txt; do
    if [ -n "$want" ]; then
        case " $want " in *" $(basename "$f" .txt) "*) ;; *) continue ;; esac
    fi
    run_scenario "$f"
done

# --- and the contract this script enforces is written down -----------------
#
# Borrowed whole from spec-conformance.sh, for the reason that harness states
# and this one inherits: three implementations transcribed from one author agree
# about the unwritten parts by descent, and a harness reads that as conformance.
# A fourth implementer is handed a page, not this file, so every field and every
# refusal token compared above must appear on that page.
DOC="$ROOT/job/README.md"
undocumented=""
for k in state epoch held want done err cp content; do
    grep -qF "\`$k\`" "$DOC" || undocumented="$undocumented $k"
done
for k in not-found lease-held stale-epoch lease-expired terminal invalid refused; do
    grep -qF "\`$k\`" "$DOC" || undocumented="$undocumented <$k>"
done
grep -qF -- '--capabilities' "$DOC" || undocumented="$undocumented <how-a-driver-declares-what-it-can-do>"
if [ -z "$undocumented" ]; then
    echo "  PASS  every field this script compares is specified in job/README.md"
    agree=$((agree+1))
else
    echo "  FAIL  compared here and written down nowhere:$undocumented"
    diverge=$((diverge+1))
fi

# --- what nobody proved ----------------------------------------------------
#
# Printed last and printed loudly. An implementation that cannot reach a
# behaviour is not an implementation that agrees about it, and every earlier
# version of a harness in this repository has let that difference disappear.
echo
if [ -n "$GAPS" ]; then
    echo "========================================"
    echo "  UNPROVEN — behaviours no full roster of three ever exercised"
    echo "$GAPS"
    echo "========================================"
fi

# A scenario that asserts nothing is pinning whatever the implementations
# happened to do on the day it was written. That is worth having and it is not
# conformance, so it is counted where the count can be read, not inferred from
# the absence of a line.
if [ -n "$SILENT" ]; then
    echo "========================================"
    echo "  SILENT — $silent scenarios assert nothing about what SHOULD happen"
    echo "$SILENT"
    echo "========================================"
fi

# A cell three implementations agree about and no page decides is agreement by
# descent, not conformance. It is printed rather than counted as a pass, because
# the alternative is what happened to `renew`: allowed in five stores, written
# down in none, and found by a person reading them side by side.
if [ -n "$UNDECIDED" ]; then
    echo "========================================"
    echo "  UNDECIDED — $undecided steps no contract page settles"
    echo "$UNDECIDED"
    echo "========================================"
fi

# And the rules nobody wrote a scenario for. This is the count the whole tagging
# convention exists to print: not a divergence, not a gap in the roster, but a
# written rule that no instrument in this repository can fail.
sort -u "$CITED" -o "$CITED"
stray="$(awk -F'\t' -v page="$DECLARED" \
    'FILENAME==page {d[$1]=1;next} !($1 in d)' "$DECLARED" "$CITED")"
if [ -n "$stray" ]; then
    echo "  FAIL  a scenario cites a tag no contract page declares:$(echo " $stray" | tr '\n' ' ')"
    diverge=$((diverge+1))
fi
tagged="$(cut -f1 "$DECLARED" | sort -u | grep -c .)"
UNEX="$(awk -F'\t' -v cited="$CITED" \
    'FILENAME==cited {c[$1]=1;next} !($1 in c) && !seen[$1]++ {printf "    [%s] %s\n", $1, $2}' \
    "$CITED" "$DECLARED")"
unexercised="$(printf '%s\n' "$UNEX" | grep -c .)"
if [ "$unexercised" -gt 0 ]; then
    echo "========================================"
    echo "  UNEXERCISED — $unexercised of $tagged invariants no scenario names"
    echo "$UNEX"
    echo "========================================"
fi

echo
echo "  $agree scenarios agreed, $diverge diverged, $broke contradicted the contract"
echo "  $((tagged - unexercised)) of $tagged invariants have a scenario, $unexercised do not"
[ "$broke" -eq 0 ] || { echo "  RESULT: CONTRACT BROKEN — all three do the same wrong thing"; exit 3; }
[ "$diverge" -eq 0 ] || { echo "  RESULT: DIVERGENCE"; exit 1; }
[ -z "$GAPS" ] || { echo "  RESULT: INCOMPLETE — see UNPROVEN above"; exit 2; }
echo "  RESULT: three implementations, one behaviour"
