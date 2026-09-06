#!/usr/bin/env bash
# Everything that has to pass, in one command.
#
# There were sixteen scripts and no way to run them all, so every session ran
# whichever ones came to mind and none of them proved the whole thing still
# worked. The cross-language harnesses are the only evidence three
# implementations agree; a harness nobody remembers to run is not evidence.
#
#   usage: scripts/check.sh [--fast] [--public]
#     --fast    skip the C++ build and the cross-language harnesses, which need
#               a compiler and take minutes. Go and Python only.
#     --public  also fetch the openabstractions repositories: prove no file
#               lives in two of them, and let the rules read the files each
#               repository authors for itself, which no rule derived from
#               split.manifest can see. Needs the network; a few seconds.
#
# Exits non-zero if anything failed, and prints what.

# Bash reads a script as it runs, so an edit landing mid-run changes the
# program that is running; one produced a syntax error nobody wrote. Read once,
# run from memory. $0 survives, so ROOT is still the live tree.
[ -n "${GATE_PINNED:-}" ] || GATE_PINNED=1 exec bash -c 'eval "$(<"$0")"' "$0" "$@"
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
T0=${EPOCHREALTIME/./}
FAST=0
PUBLIC=0
for a in "$@"; do
    case "$a" in
        --fast) FAST=1 ;;
        --public) PUBLIC=1 ;;
        *) echo "unknown flag: $a" >&2; exit 2 ;;
    esac
done

PY="${ABSTRACTION_PYTHON:-}"
if [ -z "$PY" ]; then
    for c in "$LOCALAPPDATA/Programs/Python/Python312/python.exe" python3 python; do
        command -v "$c" >/dev/null 2>&1 && { PY="$c"; break; }
    done
fi

CMAKE="${ABSTRACTION_CMAKE:-$(command -v cmake 2>/dev/null || true)}"
for c in "/c/Program Files/Microsoft Visual Studio/18/Community/Common7/IDE/CommonExtensions/Microsoft/CMake/CMake/bin/cmake.exe" \
         "/c/Program Files/CMake/bin/cmake.exe"; do
    [ -n "$CMAKE" ] || [ ! -x "$c" ] || CMAKE="$c"
done

BASE="$ROOT/scripts/check.baseline"
FAILED=()
LAP=$T0
secs() { printf -v SECS '%d.%ds' $(($1/1000000)) $(($1/100000%10)); }
lap() { local now=${EPOCHREALTIME/./}; secs $((now-LAP)); LAP=$now; }
pass() { lap; printf '  \033[32mok\033[0m    %s \033[90m%s\033[0m\n' "$1" "$SECS"; }
fail() { lap; printf '  \033[31mFAIL\033[0m  %s \033[90m%s\033[0m\n' "$1" "$SECS"; FAILED+=("$1"); }
SECTIONS=""; SECTION=""; SECTION_T0=$T0
section() {
    secs $((${EPOCHREALTIME/./} - SECTION_T0))
    [ -z "$SECTION" ] || SECTIONS="$SECTIONS $SECTION $SECS"
    SECTION="$1"; SECTION_T0=${EPOCHREALTIME/./}
    printf '\n\033[1m%s\033[0m\n' "$1"
}
pinned() {  # pinned [VAR=value ...] script [args] — read once, run from memory
    local -a e=()
    while [[ $1 =~ ^[A-Za-z_][A-Za-z0-9_]*= ]]; do e+=("$1"); shift; done
    env "${e[@]}" bash -c 'eval "$(<"$0")"' "$@"
}
modules() { (cd "$1" && go list -m -f '{{.Dir}}' 2>/dev/null) | sed 's#\\#/#g' | sed "s#^$(cd "$1" && pwd -W)/##"; }
declare -A METRIC METRIC_FROM
metric() { METRIC[$1]="$2"; METRIC_FROM[$1]="$3"; }
SERIES_KEYS="behaviour_divergences behaviour_unproven content_models invariants_cited invariants_declared
verdict_divergences citations_examined unresolved_citations shipped_dependencies
leaked_identifier_files exported_names shared_exported_names kills_fired kills_uncarried
footprint_rows footprint_untestable footprint_contradicted research_unindexed
demands_declared demands_repeated feedback_without_verdict
prose_tested_tree map_unnamed front_doors_open front_doors_published hand_rolled_stores
gate_fast_minutes gate_full_minutes gate_public_minutes"

# Output is kept and only shown for what failed. A passing run should be short
# enough to read, or it will not be read.
LOGS="$(mktemp -d)"
trap 'rm -rf "$LOGS"' EXIT

run() {  # run <label> <command...>
    local label="$1"; shift
    local log="$LOGS/${label//[^a-zA-Z0-9]/_}.log"
    if "$@" >"$log" 2>&1; then pass "$label"; else fail "$label"; echo "$log" >>"$LOGS/failed"; fi
}

section "contract"
# Every rule a page states carries a tag and a scenario cites the tag it tests.
# The behaviour harness diffs cited against declared, but it runs after every
# build, and a hard-coded tag family decided what counted as a citation, so a
# tag from a new page was invisible to it. Same diff, no family, before a
# single driver is built: seconds against the half hour the harness sits behind.
cd "$ROOT" || exit 1
RULES="$LOGS/rules"; mkdir -p "$RULES"
git ls-files --cached --others --exclude-standard \
  | grep -vE '\.(png|jpg|jpeg|zip|gguf|exe|dll)$|^scripts/check\.baseline$' > "$RULES/files"
NFILES=$(wc -l < "$RULES/files")
TAG='\[[A-Z]{2,}-[A-Z][A-Z0-9]*\]'
grep -E '^[a-z]+/README\.md$' "$RULES/files" > "$RULES/pages"
xargs -d '\n' grep -ahoE "$TAG" < "$RULES/pages" 2>/dev/null | tr -d '[]' | sort -u > "$RULES/tags-declared"
grep -vxFf "$RULES/pages" "$RULES/files" \
  | xargs -d '\n' grep -aoHE "$TAG" 2>/dev/null | tr -d '[]' | sed 's/:\([^:]*\)$/\t\1/' | sort -u > "$RULES/tags-cited"
cut -f2 "$RULES/tags-cited" | sort -u | comm -13 "$RULES/tags-declared" - > "$RULES/tags-stray"
[ "$NFILES" -gt 0 ] || fail "contract — nothing to examine; every rule below would pass on an empty tree"
if [ -s "$RULES/tags-stray" ]; then
    fail "contract tags ($(wc -l < "$RULES/tags-stray") cited and declared on no page)"
    grep -Ff <(sed 's/^/\t/' "$RULES/tags-stray") "$RULES/tags-cited" | sed 's/^/        /'
else
    pass "contract tags — $(wc -l < "$RULES/tags-declared") declared on $(wc -l < "$RULES/pages") pages, $(cut -f2 "$RULES/tags-cited" | sort -u | wc -l) cited in $(cut -f1 "$RULES/tags-cited" | sort -u | wc -l) files, every citation resolves"
fi

section "go"
printf '  pid %s\n' "$$"
while read -r d; do
    run "$d" bash -c "cd '$ROOT/$d' && go build ./... && go test ./..."
done < <(modules "$ROOT")
# -race needs cgo, and go itself reports CGO_ENABLED=0 when it cannot find a C
# compiler. parallel.go is the first concurrent code in the tree and shipped
# without ever running under the detector; a line that says so beats one that
# passes on nothing.
if [ "$(go env CGO_ENABLED 2>/dev/null)" = "1" ]; then
    for d in job/go download/go; do
        run "$d -race" bash -c "cd '$ROOT/$d' && go test -race ./..."
    done
else
    printf '  \033[33mskip\033[0m  race detector — -race needs cgo and there is no C compiler for go env CC=%s.\n' "$(go env CC 2>/dev/null)"
    printf '        WSL Ubuntu has go and gcc and lacks only libc6-dev; the Mac has clang.\n'
fi

section "head"
# What is committed, not what is here. Twice in one day a commit swept in the
# call sites of a function whose definition stayed uncommitted: every machine
# with this working tree built, and no clone could. Compiled, never run.
H="$ROOT/.build/head"
rm -rf "$H"; mkdir -p "$H"
git -C "$ROOT" archive HEAD | tar -x -C "$H"
while read -r d; do
    run "HEAD $d" bash -c "cd '$H/$d' && go build ./... && go test -count=1 -run '^$' ./..."
done < <(modules "$H")
run "HEAD python" "$PY" -m compileall -q "$H"
if [ "$FAST" = 1 ] || [ -z "$CMAKE" ]; then
    printf '  \033[33mskip\033[0m  HEAD c++ (%s)\n' "$([ "$FAST" = 1 ] && echo --fast || echo 'no cmake')"
else
    run "HEAD c++" bash -c "'$CMAKE' -S '$H' -B '$ROOT/.build/head-cpp' -DCMAKE_BUILD_TYPE=Release && '$CMAKE' --build '$ROOT/.build/head-cpp' --config Release"
fi

section "budgets"
# The 200 ms discovery budget, which was a number in prose until something
# measured it. NOT a latency threshold: CI machines are noisy neighbours and an
# absolute threshold on one is a coin flip — measured here at 45% drift under
# load with no code change. What this gates is a same-run ratio, the absence of
# wrong answers, and the contract's behaviour. See
# research/ipc/benchmarks.txt for why, and for what is deliberately left
# ungated.
run "discovery gate" pinned "$ROOT/scripts/discovery-bench.sh" --gate

section "python"
run "job/python"            bash -c "cd '$ROOT/job/python' && '$PY' -m unittest discover -p 'test_*.py'"
run "download/python"       bash -c "cd '$ROOT/download/python' && '$PY' -m unittest discover -p 'test_*.py'"
run "adopters/comfyui"      bash -c "cd '$ROOT/adopters/comfyui' && '$PY' test_node.py"
run "model/python"          bash -c "cd '$ROOT/model/python' && '$PY' -m unittest discover -p 'test_*.py'"
run "cas/python"            bash -c "cd '$ROOT/cas/python' && '$PY' -m unittest discover -p 'test_*.py'"
# Go decides for the tools, Python decides for the broker and for ComfyUI. One
# token of disagreement and two applications each believe they are sharing a
# model with the other while the machine holds two.
run "model identity"        pinned PY="$PY" "$ROOT/scripts/identity-conformance.sh"

if [ "$FAST" = "1" ]; then
    section "skipped"
    echo "  c++ and the cross-language harnesses (--fast)"
else
    section "c++"
    # A missing compiler is not a failing test. Saying FAIL for it trains
    # everyone to ignore a red line, which is how a real failure gets through.
    if [ -z "$CMAKE" ]; then
        printf '  \033[33mskip\033[0m  cmake not on PATH — set ABSTRACTION_CMAKE or run from a\n'
        printf '        developer shell. On Windows vcvars64.bat needs the VS Installer\n'
        printf '        directory on PATH first or it half-configures silently.\n'
    else
        # Configured out of tree so a failed build never leaves artefacts in the
        # repository that a later run would mistake for a good one.
        B="${ABSTRACTION_CPP_BUILD:-$ROOT/.build}"
        CTEST="$(dirname "$CMAKE")/ctest"
        run "cmake configure"   bash -c "'$CMAKE' -S '$ROOT' -B '$B' -DCMAKE_BUILD_TYPE=Release"
        run "cmake build"       bash -c "'$CMAKE' --build '$B' --config Release"
        run "ctest"             bash -c "'$CTEST' --test-dir '$B' -C Release --output-on-failure"
    fi

    section "cross-language"
    run "job conformance"   pinned "$ROOT/scripts/xlang-job.sh"
    # The C++ reader this run just built, rather than the one a developer once
    # left in /c/jobbuild: without this the third implementation is compiled by
    # the line above and then left out of the comparison it exists for.
    SPECREAD="${B:-$ROOT/.build}/Release/specread.exe"
    [ -f "$SPECREAD" ] || SPECREAD="${B:-$ROOT/.build}/specread"
    run "download spec"     pinned SPECREAD_CPP="$SPECREAD" "$ROOT/scripts/spec-conformance.sh"
    # Three implementations must spell one proven byte set the same way, or the
    # record churns against itself and no diff of a job's history means anything.
    CPP_BUILD="${B:-$ROOT/.build}"
    JOBCTL_CPP="$CPP_BUILD/Release/jobctl.exe"
    [ -f "$JOBCTL_CPP" ] || JOBCTL_CPP="$CPP_BUILD/jobctl"
    run "checkpoint ranges" pinned JOBCTL_CPP="$JOBCTL_CPP" "$ROOT/scripts/conformance.sh" --canonical
    # The one harness that compares nothing. Six writers of three languages on
    # one file, a reader per language spinning through every rename: a lock
    # byte or a rename semantics one of them believes differently is a lost
    # update or a torn read here, and agreement everywhere above. Three runs,
    # ten to twenty seconds each on Windows where the spinning readers are the
    # cost and a tenth of a second on Linux; a foreign lock fails run one.
    TEST_CAS="$CPP_BUILD/cas/cpp/Release/test_cas.exe"
    [ -f "$TEST_CAS" ] || TEST_CAS="$CPP_BUILD/cas/cpp/test_cas"
    run "cas mixed languages" env CAS_CPP="$TEST_CAS" "$PY" "$ROOT/cas/mixed.py" 3

    # Implementations diverge today and failing on every one of them would buy
    # what the citation rule bought: a line everybody steps over. So the gate is
    # on the SET, both ways. A new divergence fails because it is not recorded;
    # a fixed one fails because the record still claims it. The set prints on a
    # green run too, because debt nobody sees is debt nobody pays.
    REPLAY_CPP="$CPP_BUILD/Release/replay.exe"
    [ -f "$REPLAY_CPP" ] || REPLAY_CPP="$CPP_BUILD/replay"
    BEH="$LOGS/behaviour.log"
    pinned REPLAY_CPP="$REPLAY_CPP" "$ROOT/scripts/behaviour-conformance.sh" >"$BEH" 2>&1
    if grep -q 'is ABSENT' "$BEH"; then
        printf '  \033[33mskip\033[0m  behaviour conformance — %s\n' "$(sed -n 's/^ *\([a-z+]*\): ABSENT — /\1 absent: /p' "$BEH" | paste -sd';' -)"
    else
        sed -n 's/^  FAIL  /divergence /p; s/^    \(.*this behaviour is UNPROVEN in .*\)$/unproven \1/p' "$BEH" \
          | sort -u > "$LOGS/beh-now"
        grep -a '^behaviour	' "$BASE" | cut -f2- | sort -u > "$LOGS/beh-was"
        metric behaviour_divergences "$(grep -c '^divergence ' "$LOGS/beh-now")" behaviour-conformance.sh
        metric behaviour_unproven "$(grep -c '^unproven ' "$LOGS/beh-now")" behaviour-conformance.sh
        # How many content-set names the three readers agree on. A record's
        # `critical` list is decided against this roster, so a number that moves
        # without a contract page moving is a reader refusing records the
        # others accept.
        metric content_models "$(sed -n 's/^  PASS  models: \([0-9]*\) names.*/\1/p' "$BEH" | tail -1)" behaviour-conformance.sh
        metric invariants_cited "$(sed -n 's/^ *\([0-9]*\) of [0-9]* invariants have a scenario.*/\1/p' "$BEH" | tail -1)" behaviour-conformance.sh
        metric invariants_declared "$(sed -n 's/^ *[0-9]* of \([0-9]*\) invariants have a scenario.*/\1/p' "$BEH" | tail -1)" behaviour-conformance.sh
        comm -23 "$LOGS/beh-now" "$LOGS/beh-was" > "$LOGS/beh-new"
        comm -13 "$LOGS/beh-now" "$LOGS/beh-was" > "$LOGS/beh-gone"
        if [ -s "$LOGS/beh-new" ]; then
            fail "behaviour conformance ($(wc -l < "$LOGS/beh-new") unrecorded)"
            sed 's/^/        /' "$LOGS/beh-new"; echo "$BEH" >>"$LOGS/failed"
        elif [ -s "$LOGS/beh-gone" ]; then
            fail "behaviour conformance ($(wc -l < "$LOGS/beh-gone") recorded and now agreeing — drop them from check.baseline)"
            sed 's/^/        /' "$LOGS/beh-gone"
        else
            pass "behaviour conformance — $(wc -l < "$LOGS/beh-now") known divergences, exactly the recorded set"
            sed 's/^/        /' "$LOGS/beh-now"
        fi
    fi

    # The harnesses above compare what the three readers ACCEPT, so nothing they
    # can see lies outside the set all three accept. What they refuse is where
    # the disagreements are, and a refusal one implementation makes alone is an
    # availability split rather than a safety win — two hosts write the record
    # and the third cannot open it. Same shape of gate as the behaviour set: on
    # the SET, both ways, and printed on a green run because a hundred and fifty
    # verdicts nobody sees is debt nobody pays.
    VER="$LOGS/verdict.log"
    pinned SPECREAD_CPP="$SPECREAD" "$ROOT/scripts/verdict-conformance.sh" >"$VER" 2>&1
    if grep -q 'need at least two implementations' "$VER"; then
        printf '  \033[33mskip\033[0m  verdict conformance — fewer than two readers here\n'
    else
        sed -n 's/^  FAIL  /divergence /p' "$VER" | sort -u > "$LOGS/ver-now"
        metric verdict_divergences "$(wc -l < "$LOGS/ver-now")" verdict-conformance.sh
        grep -a '^verdict	' "$BASE" | cut -f2- | sort -u > "$LOGS/ver-was"
        comm -23 "$LOGS/ver-now" "$LOGS/ver-was" > "$LOGS/ver-new"
        comm -13 "$LOGS/ver-now" "$LOGS/ver-was" > "$LOGS/ver-gone"
        if [ -s "$LOGS/ver-new" ]; then
            fail "verdict conformance ($(wc -l < "$LOGS/ver-new") unrecorded)"
            sed 's/^/        /' "$LOGS/ver-new"; echo "$VER" >>"$LOGS/failed"
        elif [ -s "$LOGS/ver-gone" ]; then
            fail "verdict conformance ($(wc -l < "$LOGS/ver-gone") recorded and now agreeing — drop them from check.baseline)"
            sed 's/^/        /' "$LOGS/ver-gone"
        else
            pass "verdict conformance — $(wc -l < "$LOGS/ver-now") known divergences, exactly the recorded set"
            sed 's/^/        /' "$LOGS/ver-now"
        fi
    fi

    # Everything above proves three implementations agree. This proves a
    # stranger can GET them, by each ecosystem's own door — go get, pip install,
    # find_package — from outside the tree, printing one line. A door that
    # cannot be tried here is named; it is not a pass.
    OBT="$LOGS/obtain.log"
    pinned PY="$PY" CMAKE="$CMAKE" "$ROOT/scripts/obtain-conformance.sh" >"$OBT" 2>&1; obt=$?
    metric front_doors_open "$(sed -n 's/^  doors: \([0-9]*\) of .*/\1/p' "$OBT" | tail -1)" obtain-conformance.sh
    metric front_doors_published "$(sed -n 's/^  doors: .* open, \([0-9]*\) published.*/\1/p' "$OBT" | tail -1)" obtain-conformance.sh
    if [ "$obt" -ne 0 ]; then
        fail "obtainable — $(grep -c '^  FAIL' "$OBT") front doors refused"
        grep '^  FAIL' "$OBT" | sed 's/^  FAIL  /        /'; echo "$OBT" >>"$LOGS/failed"
    elif grep -q ': ABSENT' "$OBT"; then
        printf '  \033[33mskip\033[0m  obtainable — %s; open: %s\n' \
            "$(sed -n 's/^    \([a-z+]*\): ABSENT — \(.*\)/\1 absent: \2/p' "$OBT" | paste -sd';' -)" \
            "$(sed -n 's/^  doors: //p' "$OBT")"
    else
        pass "obtainable — $(sed -n 's/^  doors: //p' "$OBT") front doors, one line: $(sed -n 's/^  go: //p' "$OBT")"
    fi
fi


section "published"
# The published tree equals the generated one.
#
# "Is any path duplicated" could only ever guess, because it matched on
# basenames: it paired two unrelated files both named store.go in different
# layers and counted 121 drifts across 90 files. split.sh knows the mapping,
# so the question becomes exact — generate every repository and diff it.
# Slow (fifteen SSH fetches) so it stays opt-in, and it is still the only
# check that sees a leak before the push: a commit removing a file does not
# unpublish it. It runs before the rules because it leaves the org's own files
# on disk, and the rules below are blind without them.
if [ "$PUBLIC" = "1" ]; then
    if pinned "$ROOT/scripts/split.sh" --check > "$LOGS/split.log" 2>&1; then
        pass "published tree — every repository equals what this one generates"
    else
        fail "published tree — $(grep -c '^  .*FAIL' "$LOGS/split.log") repositories differ from what this one generates"
        sed 's/^/      /' "$LOGS/split.log" | grep -E 'FAIL|LEAK|every row' | head -20
    fi
else
    printf '  \033[33mskip\033[0m  published tree (needs the network — pass --public)\n'
fi

# Every published Go module builds on its own, with nothing but the module proxy.
#
# Nobody had ever tried it. The working copy builds because one go.work wires
# twelve modules together and every import resolves to a sibling directory; a
# stranger cloning one repository has none of that, and the split exists for
# exactly that stranger. GOWORK=off is the whole difference, and it turns
# "the layers build" into a claim about what we publish rather than about this
# machine. It builds the GENERATED tree, not the org's, so a break is visible
# before the push instead of after it — generated from HEAD like the rest of
# split.sh, so an uncommitted fix still shows as broken here.
#
# The generated tree is not the whole repository, though, and for an `own` one
# it is barely any of it. abstraction-identity generates only probe/, a nested
# module whose replace points at a root that is never generated because every
# file in it is owned. Building the generated tree alone could therefore never
# go green there — the probe module was published broken, and would have been
# tagged broken, under a check that reported it as red every single run. So the
# org's own files go underneath and the generated ones on top: what a stranger
# clones after the push, with an uncommitted fix still showing as broken.
if [ "$PUBLIC" = "1" ]; then
    S="$LOGS/standalone"
    rm -rf "$S"
    for g in "$ROOT"/.split/*/; do
        r="$(basename "$g")"
        mkdir -p "$S/$r"
        [ -d "$ROOT/.split/.org-files/$r" ] && cp -R "$ROOT/.split/.org-files/$r/." "$S/$r/"
        cp -R "$g." "$S/$r/"
    done
    while read -r m; do
        d="$(dirname "$m")"
        run "standalone ${d#$S/}" env GOWORK=off bash -c "cd '$d' && go build ./..."
    done < <(find "$S" -name go.mod | sort)
else
    printf '  \033[33mskip\033[0m  standalone build of each published module (pass --public)\n'
fi

# What the PUBLISHED tag makes of a record this tree writes.
#
# Every test in the tree reads with the tree, so all of them answer the same
# question: do we still read our own records. The direction that decides whether
# a change is safe to publish is the other one, and research/wire-compat is the
# only thing that can ask it — a module pinned to go/v0.1.0, deliberately
# outside go.work, so the published reader is really running. It had been run
# twice, by hand, both times after the change had already shipped.
#
# Two halves, because the static corpus alone cannot see today's work: it is a
# photograph, and a change of meaning written this afternoon does not alter it.
# So the tree WRITES a store first — the same replay driver and the same
# scenarios the behaviour harness uses, minus the wire ones, which need the
# fixture — and v0.1.0 reads that. Change what a record means and the line the
# published reader prints about it changes the same day; the way back to green
# that keeps an adopter safe is to declare a content-set name and mark it
# critical, which turns a silent misreading into a refusal at decode.
#
# Ids and the lease probes are cut from the live half and kept on the corpus
# half. An id is a clock, and `successor`/`holder` ask whether a lease is live,
# which on a store written seconds ago is a race against a five-second TTL. The
# corpus records carry expired leases and fixed ids, so there both probes are
# facts — and they are where terminal enforcement was found. An absolute sink
# is collapsed to <foreign> for the same reason an id is: which of the two
# spellings is the foreign one is a property of the host running this, and a
# recorded line that only holds on Windows is the partition all over again.
#
# Opt-in with --public, and that is load-bearing rather than tidy: this pins a
# tag, and a pinned tag makes a mandatory gate lie the day the tag moves. The
# pin is a recorded line here too, so moving it without re-reading everything
# below fails.
if [ "$PUBLIC" = "1" ]; then
    WC="$LOGS/wire"; mkdir -p "$WC"
    if (cd "$ROOT/research/wire-compat" && GOWORK=off go build -o "$WC/reader.exe" .) >"$WC/build.log" 2>&1 &&
       (cd "$ROOT/download/go" && go build -o "$WC/replay.exe" ./cmd/replay) >>"$WC/build.log" 2>&1; then
        grep -o 'abstraction-[a-z]*/go v[0-9][^ ]*' "$ROOT/research/wire-compat/go.mod" \
          | sed 's/^/pin\t/' > "$WC/raw"
        "$WC/reader.exe" "$ROOT/download/testdata/records" | sed 's/^/corpus\t/' >> "$WC/raw"
        NLIVE=0
        for f in "$ROOT"/download/testdata/scenarios/*.txt; do
            grep -q '^# requires:.*wire' "$f" && continue
            NLIVE=$((NLIVE+1))
            rm -rf "$WC/store"
            "$WC/replay.exe" "$WC/store" "$f" >/dev/null 2>&1 || true
            "$WC/reader.exe" "$WC/store/store/jobs" \
              | sed -E "s|^|$(basename "$f" .txt)\t|; s/[0-9]{13}-[0-9a-f]{20}/ID/g; s@=[^\t]*[/\\]models[/\\]@=<foreign>/@g; s/\tsuccessor=[^\t]*//; s/\tholder=[^\t]*//" >> "$WC/raw"
        done
        sort -u "$WC/raw" > "$WC/now"
        grep -a '^wire	' "$BASE" | cut -f2- | sort -u > "$WC/was"
        comm -23 "$WC/now" "$WC/was" > "$WC/new"
        comm -13 "$WC/now" "$WC/was" > "$WC/gone"
        if [ -s "$WC/new" ]; then
            fail "published reader ($(wc -l < "$WC/new") lines v0.1.0 now prints that check.baseline does not)"
            sed 's/^/        /' "$WC/new" | head -20
        elif [ -s "$WC/gone" ]; then
            fail "published reader ($(wc -l < "$WC/gone") recorded lines v0.1.0 no longer prints — drop them from check.baseline)"
            sed 's/^/        /' "$WC/gone" | head -20
        else
            pass "published reader — $(wc -l < "$WC/now") lines from go/v0.1.0 over the corpus and $NLIVE scenarios this tree ran, exactly the recorded set"
        fi
    else
        fail "published reader — research/wire-compat will not build against the published tag"
        echo "$WC/build.log" >>"$LOGS/failed"
    fi
else
    printf '  \033[33mskip\033[0m  what go/v0.1.0 makes of what this tree writes (needs the module proxy — pass --public)\n'
fi

section "rules"
# Rules that were broken silently because nothing could see the breach.
# Each prints what it examined: a check that passes on an empty input set is
# the failure mode this project has already had.

note() { printf '  \033[90m%s\033[0m\n' "$1"; }
cd "$ROOT" || exit 1
baseline() { grep -a "^$1	" "$BASE" 2>/dev/null | cut -f2- | sort -u; }

# ---- the repository holds every script the gate runs --------------------
# This file sourced scripts/documents.sh while that file was untracked. Sourcing
# a file that is not there returns non-zero and bash carries on, so every fresh
# clone had a gate with a whole section silently missing. Scoped to scripts/,
# where everything is either run by the gate or read by it as data: the other
# $ROOT paths in here name what a run produces, not what a clone must arrive
# with. Reads the sourced files too, or a script named only by documents.sh
# would be exactly as invisible as documents.sh was.
echo "scripts/check.sh" > "$RULES/gate-scripts"
while :; do
    { xargs -d '\n' grep -ohE '\$ROOT/scripts/[A-Za-z0-9._-]+' < "$RULES/gate-scripts" 2>/dev/null | sed 's|\$ROOT/||'
      cat "$RULES/gate-scripts"; } | sort -u > "$RULES/gate-grown"
    cmp -s "$RULES/gate-scripts" "$RULES/gate-grown" && break
    mv "$RULES/gate-grown" "$RULES/gate-scripts"
done
git ls-files scripts | sort > "$RULES/gate-tracked"
comm -23 "$RULES/gate-scripts" "$RULES/gate-tracked" > "$RULES/gate-missing"
note "$(wc -l < "$RULES/gate-scripts") files under scripts/ are named by check.sh or by something it sources, found by grepping each of them for \$ROOT/scripts/*, and checked against git ls-files scripts"
if [ -s "$RULES/gate-missing" ]; then
    fail "gate scripts ($(wc -l < "$RULES/gate-missing") the gate runs and git does not track — a fresh clone has a broken gate)"
    sed 's/^/        /' "$RULES/gate-missing"
else
    pass "gate scripts — every one is in the repository"
fi

# Any rule derived from split.manifest is blind exactly where a repository
# authors its own file — and that is where build configuration lives, so the
# CMakeLists that clones nlohmann/json into a header we publish was invisible to
# every rule here while being the one dependency we hand to every C++ adopter.
# Special-casing that file would leave the blindness. Fetching what the org
# wrote and examining it beside our own files removes it for every rule below,
# including the ones nobody has written yet.
ORG="$ROOT/.split/.org"
OWNED="$ROOT/.split/.org-files"
: > "$RULES/org-files"; : > "$RULES/pub"
rm -rf "$OWNED"
if [ -d "$ORG" ]; then
    for r in $(ls "$ORG" 2>/dev/null); do
        [ -d "$ORG/$r/.git" ] || continue
        git -C "$ORG/$r" ls-tree -r --name-only HEAD 2>/dev/null | sed "s/^/$r	/" >> "$RULES/pub"
    done
    awk '$1=="repo"{r=$2} $1=="own"{print r "\t" $2}' "$ROOT/scripts/split.manifest" > "$RULES/own"
    declare -A GLOBS=()
    while IFS=$'\t' read -r r g; do GLOBS[$r]+="$g "; done < "$RULES/own"
    set -f
    while IFS=$'\t' read -r r p; do
        for g in ${GLOBS[$r]:-}; do
            case "$p" in $g) printf '%s\t%s\n' "$r" "$p"; break ;; esac
        done
    done < "$RULES/pub" > "$RULES/own-hit"
    set +f
    cut -f1 "$RULES/own-hit" | sort -u | while read -r r; do
        mkdir -p "$OWNED/$r"
        mapfile -t P < <(awk -F'\t' -v r="$r" '$1==r {print $2}' "$RULES/own-hit")
        git -C "$ORG/$r" archive HEAD -- "${P[@]}" 2>/dev/null | tar -x -C "$OWNED/$r" \
          && printf ".split/.org-files/$r/%s\n" "${P[@]}"
    done | sort -u > "$RULES/org-files"
fi
cat "$RULES/files" "$RULES/org-files" > "$RULES/scan"
NORG=$(wc -l < "$RULES/org-files")
if [ "$NORG" -gt 0 ]; then
    note "$NORG org-authored files fetched from $(cut -f1 "$RULES/pub" | sort -u | wc -l) repositories and examined beside ours"
else
    note "no org-authored files on disk — every rule here is blind where a repository writes its own file, which is where build configuration lives (pass --public)"
fi

# ---- nothing names a path the next publish removes ----------------------
# A `drop` unpublishes a directory. What breaks is never the deletion, it is
# whatever still names it, and that is reliably a file this tree does not hold:
# addon-synology's deploy/build.sh builds the jobd command out of the download
# layer, and the release that moves that command into its own repository would
# tag the broken script permanently. The needle is the qualified path
# everywhere, and its last two segments in the org's files only — ours may still
# name the source, since the source moves rather than disappears.
: > "$RULES/gone-refs"
awk '$1=="repo"{r=$2} $1=="drop"{p=$2; sub(/\/\*$/,"",p); print r "/" p}' \
    "$ROOT/scripts/split.manifest" | sort -u > "$RULES/gone"
grep -v '^feedback/' "$RULES/scan" > "$RULES/gone-scan"
grep '^\.split/\.org-files/' "$RULES/gone-scan" > "$RULES/gone-org"
: > "$RULES/gone-needles"; : > "$RULES/gone-tails"
while read -r q; do
    [ -n "$q" ] || continue
    printf '%s\t%s\n' "$q" "$q" >> "$RULES/gone-needles"
    d="${q#*/}"; parent="${d%/*}"
    case "$d" in */*) printf '%s\t%s\n' "${parent##*/}/${d##*/}" "$q" >> "$RULES/gone-tails" ;; esac
done < "$RULES/gone"
namers() {  # namers LISTFILE TABLE — one pass over the files, every needle at once
    [ -s "$2" ] || return 0
    xargs -r -d '\n' awk -v tab="$2" '
        BEGIN { while ((getline l < tab) > 0) { split(l, f, "\t"); n++; needle[n]=f[1]; label[n]=f[2] } }
        { for (i=1; i<=n; i++) if (!((FILENAME, i) in hit) && index($0, needle[i])) { hit[FILENAME, i]=1; print label[i] "\t" FILENAME } }
    ' < "$1" 2>/dev/null >> "$RULES/gone-refs"
}
namers "$RULES/gone-scan" "$RULES/gone-needles"
namers "$RULES/gone-org" "$RULES/gone-tails"
sort -u -o "$RULES/gone-refs" "$RULES/gone-refs"
baseline unpublished-reference > "$RULES/gone-known"
comm -23 "$RULES/gone-refs" "$RULES/gone-known" > "$RULES/gone-new"
if [ ! -s "$RULES/gone" ]; then
    note "no path is being unpublished, so nothing can be left naming one"
elif [ -s "$RULES/gone-new" ]; then
    fail "$(wc -l < "$RULES/gone-new") files name a path the next publish removes"
    sed 's/^/        /' "$RULES/gone-new"
else
    pass "nothing names a path the next publish removes — $(wc -l < "$RULES/gone") removed, $(wc -l < "$RULES/gone-scan") files read"
fi

# ---- no broken citation ------------------------------------------------
# Three things are shaped like a citation and are not a link anyone can follow,
# and every one of them was reported as a defect before it was recognised: a
# manifest directive naming a path in another repository, a path pinned to a
# commit, and a dated record quoting what somebody once wrote. A rule that cries
# wolf is a rule everybody learns to step over, which is how the real ones sat
# unfixed.
baseline citation-history > "$RULES/history"
awk -v hist="$RULES/history" '
BEGIN {
    RE="[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_.-]+)+[.](md|txt|go|py|sh|html|json|thrift|lock|cpp|hpp|h|yml|yaml|cmd|ps1|patch|toml|mod)"
    while ((getline h < hist) > 0) HIST[++nh]=h
}
FNR==1 {
    dir=FILENAME; sub("/[^/]*$","",dir); if (dir==FILENAME) dir="."
    record=0; for (i=1;i<=nh;i++) if (FILENAME ~ HIST[i]) record=1
}
record { next }
FILENAME ~ /split\.manifest$/ && $1 !~ /^(file|tree|hold)$/ { next }
{
    line=$0
    gsub("[a-z]+://[^ )\"'"'"'`]*"," ",line)
    gsub("[$]{?[A-Za-z_][A-Za-z0-9_]*}?"," ",line)
    while (match(line,RE)) {
        p=substr(line,RSTART,RLENGTH); line=substr(line,RSTART+RLENGTH)
        if (substr(line,1,1) != "@") print FILENAME "\t" dir "\t" p
    }
}' $(cat "$RULES/files") 2>/dev/null | sort -u > "$RULES/cited"
NCITE=$(wc -l < "$RULES/cited")
awk -F'\t' '
NR==FNR { have[$0]=1; seg=$0; sub("/.*","",seg); top[seg]=1; next }
{
    p=$3
    if (p in have) next
    if (($2 "/" p) in have) next
    for (h in have) if (h ~ ("/" p "$")) next
    seg=p; sub("/.*","",seg)
    if (!(seg in top) && seg!="docs") next
    print $1 "\t" p
}' "$RULES/files" "$RULES/cited" | sort -u > "$RULES/broken"

# Forty of the fifty-eight recorded breaks were docs/* paths, held against a
# comment asserting they resolve in openabstractions/abstractions on nobody's
# authority but the comment's. A citation-org line is that assertion written as
# a fact — repository and path — and --public proves or disproves each one.
baseline citation-org > "$RULES/org-claim"
awk -F'\t' -v claims="$RULES/org-claim" '
BEGIN { while ((getline l < claims) > 0) { split(l, a, "\t"); ok[a[2]]=1 } }
!($2 in ok)' "$RULES/broken" > "$RULES/b2"
mv "$RULES/b2" "$RULES/broken"
baseline citation > "$RULES/known-broken"
comm -23 "$RULES/broken" "$RULES/known-broken" > "$RULES/new-broken"
comm -13 "$RULES/broken" "$RULES/known-broken" > "$RULES/fixed-broken"
: > "$RULES/org-gone"
if [ -s "$RULES/pub" ]; then
    cut -f2 "$RULES/pub" | sort -u > "$RULES/pub-paths"
    cut -f2 "$RULES/org-claim" | sort -u | comm -13 "$RULES/pub-paths" - > "$RULES/org-gone"
    # A path that happens to exist in some published repository is a candidate,
    # not a resolution: research/ publishes a logging/inventory.txt that has
    # nothing to do with the logging layer. So this suggests and never gates.
    cut -f2 "$RULES/broken" | sort -u | comm -12 "$RULES/pub-paths" - > "$RULES/org-maybe"
    [ -s "$RULES/org-maybe" ] && note "$(wc -l < "$RULES/org-maybe") unresolved paths do exist in a published repository — record as citation-org if that is the file meant: $(paste -sd' ' "$RULES/org-maybe")"
fi
metric citations_examined "$NCITE" check.sh; metric unresolved_citations "$(wc -l < "$RULES/broken")" check.sh
note "$NCITE citations in $NFILES files, $(wc -l < "$RULES/history") record patterns read as history; $(wc -l < "$RULES/broken") unresolved, $(wc -l < "$RULES/known-broken") recorded, $(wc -l < "$RULES/org-claim") resolved in a published repository$([ -s "$RULES/pub" ] && echo " and proved this run" || echo " and unverified without --public")"
if [ -s "$RULES/org-gone" ]; then
    fail "citations ($(wc -l < "$RULES/org-gone") recorded as published and not there)"
    sed 's/^/        /' "$RULES/org-gone"
elif [ -s "$RULES/new-broken" ]; then
    fail "citations ($(wc -l < "$RULES/new-broken") new)"
    sed 's/^/        /' "$RULES/new-broken"
elif [ -s "$RULES/fixed-broken" ]; then
    fail "citations ($(wc -l < "$RULES/fixed-broken") recorded breaks now resolve — drop them from check.baseline)"
    sed 's/^/        /' "$RULES/fixed-broken"
else
    pass "citations — the broken set is exactly the recorded one"
fi

# ---- no copyleft text in anything we distribute ------------------------
baseline copyleft > "$RULES/copyleft"
NPAT=$(wc -l < "$RULES/copyleft")
baseline copyleft-fixture > "$RULES/copyleft-fixture"
if [ "$NPAT" -gt 0 ] && [ -s "$RULES/copyleft-fixture" ] \
   && grep -qFf "$RULES/copyleft" "$RULES/copyleft-fixture"; then
    xargs -d '\n' grep -lFf "$RULES/copyleft" < "$RULES/scan" 2>/dev/null | sort > "$RULES/copyleft-hits"
    if [ -s "$RULES/copyleft-hits" ]; then
        fail "copyleft in distributed files"
        sed 's/^/        /' "$RULES/copyleft-hits"
    else
        pass "copyleft — $NPAT licence signatures, none in $((NFILES + NORG)) distributed files"
    fi
else
    fail "copyleft — the pattern set does not match its own fixture; the instrument is broken"
fi
# Nothing under .build/_deps is ours to push, but it is linked into what we
# ship, which is the direction the licence policy actually cares about.
find "$ROOT/.build/_deps" -maxdepth 5 -type f \
     \( -ipath '*licen[cs]e*' -o -ipath '*copying*' \) 2>/dev/null \
  | xargs -d '\n' grep -lFf "$RULES/copyleft" 2>/dev/null | sed "s#^$ROOT/##" | sort > "$RULES/vendored"
if [ -s "$RULES/vendored" ]; then
    note "vendored and linked in, not pushed by us: $(wc -l < "$RULES/vendored") copyleft licence texts"
    sed 's/^/        /' "$RULES/vendored"
fi

# ---- nothing we ship depends on anything we did not write ----------------
# Shipped is what scripts/split.manifest publishes; everything else is
# measurement, and research/discovery-bench needing go-winio to time a named
# pipe is not a supply-chain decision we make on an adopter's behalf. Deriving
# the difference from the manifest rather than a list of exceptions is the whole
# point: a directory becomes gated the day it is published, not the day somebody
# remembers to add it.
awk '$1=="tree"||$1=="file" {print $3}' "$ROOT/scripts/split.manifest" | sort -u > "$RULES/shipped"
# "Ours" is what our own modules declare themselves to be. It used to be read
# off the manifest's rewrite table, which made deleting a rewrite silently
# reclassify seven of our own layers as third-party dependencies.
git ls-files 'go.mod' '*/go.mod' | while read -r m; do sed -n 's/^module  *//p' "$ROOT/$m"; done \
  | awk -F/ 'NF>2 {print $1 "/" $2 "/"}' | sort -u > "$RULES/ours"
foreign() { # foreign <module path>  — true when it must be fetched from someone else
    case "$1" in */*) ;; *) return 1 ;; esac
    case "${1%%/*}" in *.*) ;; *) return 1 ;; esac
    while read -r o; do case "$1/" in "$o"*) return 1 ;; esac; done < "$RULES/ours"
    return 0
}
publishes() { # publishes <dir>
    while read -r s; do
        case "$1/" in "$s"/*) return 0 ;; esac
        case "$s/" in "$1"/*) return 0 ;; esac
    done < "$RULES/shipped"
    return 1
}
: > "$RULES/deps"; : > "$RULES/measured"; : > "$RULES/fetched"
record() { # record <dir> <fact>
    publishes "$1" && printf '%s\t%s\n' "$1" "$2" >> "$RULES/deps" \
                   || printf '%s\t%s\n' "$1" "$2" >> "$RULES/measured"
}
requires() { # requires <go.mod> — what one file declares, needing no source and no proxy
    go mod edit -json "$1" \
      | awk '/"Require": \[/,/^\t\]/' | sed -n 's/.*"Path": "\(.*\)".*/\1/p'
}
NMOD=0
for mod in $(git ls-files 'go.mod' '*/go.mod'); do
    d="$(dirname "$mod")"; NMOD=$((NMOD+1))
    # GOWORK=off, or every module reports the whole workspace's graph and each
    # layer looks like it depends on golang.org/x/tools. It also resolves each
    # requirement at its PUBLISHED version, so a dependency deleted here but
    # never republished is still reported, and correctly: that is what an
    # adopter fetches today.
    if ! ( cd "$ROOT/$d" && GOWORK=off go list -m all ) > "$RULES/graph" 2>&1; then
        record "$d" "does not resolve"
        requires "$ROOT/$mod" > "$RULES/graph"
    fi
    while read -r p _; do foreign "$p" && record "$d" "$p"; done < "$RULES/graph"
done
# A repository that writes its own go.mod is published by definition, and no
# rule derived from split.manifest can see the file. service-jobd has required
# go-winio for as long as this rule has existed and the rule has never once read
# it. There is no source here to build, so the require list is the whole answer:
# an indirect line only a build would add is still missed, and a direct one no
# longer is.
for mod in $(grep '/go\.mod$' "$RULES/org-files"); do
    NMOD=$((NMOD+1))
    r="$(dirname "${mod#.split/.org-files/}")"
    requires "$ROOT/$mod" \
      | while read -r p; do foreign "$p" && printf '%s\t%s\n' "$r" "$p"; done >> "$RULES/deps"
done
# A configure-time git clone is a dependency with no manifest to read it out of,
# so it is found where it is written. It is reported wherever it lives: the
# CMakeLists that fetches nlohmann/json is the one file split.manifest does not
# publish, and the header it puts that dependency into is published from here.
for cm in $(grep -E '(^|/)CMakeLists\.txt$' "$RULES/scan"); do
    sed '/^[[:space:]]*#/d' "$ROOT/$cm" | sed -n 's/.*GIT_REPOSITORY  *//p' \
      | sed 's#^[a-z]*://##; s/\.git$//' | while read -r r; do
            foreign "$r" && printf '%s\t%s\n' "$cm" "$r" >> "$RULES/fetched"
        done
done
sort -u -o "$RULES/deps" "$RULES/deps"
baseline dependency > "$RULES/known-deps"
comm -23 "$RULES/deps" "$RULES/known-deps" > "$RULES/new-deps"
comm -13 "$RULES/deps" "$RULES/known-deps" > "$RULES/gone-deps"
metric shipped_dependencies "$(wc -l < "$RULES/deps")" check.sh
note "$NMOD go modules against $(wc -l < "$RULES/shipped") published sources; $(sort -u "$RULES/measured" | wc -l) foreign dependencies in code we only measure, $(wc -l < "$RULES/known-deps") recorded in what we ship"
if [ -s "$RULES/fetched" ]; then
    note "cloned at configure time, into headers we publish: $(sort -u "$RULES/fetched" | wc -l)"
    sort -u "$RULES/fetched" | sed 's/^/        /'
fi
if [ -s "$RULES/new-deps" ]; then
    fail "dependencies ($(wc -l < "$RULES/new-deps") new in a published tree)"
    sed 's/^/        /' "$RULES/new-deps"
elif [ -s "$RULES/gone-deps" ]; then
    fail "dependencies ($(wc -l < "$RULES/gone-deps") recorded and no longer there — drop them from check.baseline)"
    sed 's/^/        /' "$RULES/gone-deps"
else
    pass "dependencies — every published module resolves to modules we wrote"
fi

# ---- no public header names somebody else's header ----------------------
# A build file that fetches a dependency costs the adopter a fetch. A header we
# publish that includes one costs every adopter of that layer the dependency
# itself, forever, whether or not they wanted it — it is in their translation
# units and in their include path. That is the sharper surface and it is one
# grep: an angle include with a path separator whose first segment is not ours.
grep -E '(^|/)include/.*\.(h|hpp)$' "$RULES/scan" > "$RULES/headers"
xargs -d '\n' grep -nE '^[[:space:]]*#include[[:space:]]*<[^>]+/[^>]+>' < "$RULES/headers" 2>/dev/null \
  | grep -v '<abstraction/' | sort > "$RULES/header-deps"
if [ -s "$RULES/header-deps" ]; then
    fail "public headers ($(wc -l < "$RULES/header-deps") reach a third-party header, and so does every adopter that includes them)"
    sed 's/^/        /' "$RULES/header-deps"
else
    pass "public headers — $(wc -l < "$RULES/headers") published headers, none includes anything but our own"
fi

# ---- one concept, one owner ---------------------------------------------
# Fourteen hundred lines implemented the same range model twice and it was found
# by a person reading two repositories side by side. A name exported by two
# published modules was the only cheap instrument pointing at it, and on its own
# it has to guess a concept from a name: Store in job and in storage really do
# differ, Timestamp in job and in logging really did not. scripts/concepts is
# what it reads to tell them apart, and every shared name has to be adjudicated
# there or this goes red. An alias to another of our packages is the fix and not
# the defect, so it is not counted as a spelling at all.
#
# The layout rule beside it asks the same question of shapes rather than names,
# because a duplicate somebody renamed answers to nothing else here. It found
# the Timestamp on its first run, with no false positive in 56 structs.
REG="$ROOT/scripts/concepts"
: > "$RULES/modtab"
for m in $(git ls-files -co --exclude-standard 'go.mod' '*/go.mod'); do
    d="${m%/go.mod}"; [ "$d" != "$m" ] || continue
    if publishes "$d"; then sfx=""; else
        case "$d" in research/*|forks/*|.split/*) continue ;; esac; sfx="-private"; fi
    printf '%s\t%s\n' "$d" "$sfx" >> "$RULES/modtab"
done
for x in exports structs exports-private structs-private packages; do : > "$RULES/$x"; done
git ls-files -co --exclude-standard '*.go' | grep -v '_test\.go$' \
  | xargs -d '\n' awk -v tab="$RULES/modtab" -v out="$RULES" '
    BEGIN { while ((getline l < tab) > 0) { split(l, f, "\t"); sfx[f[1]]=f[2]; dir[++nd]=f[1] } }
    FNR==1 {
        m=""; for (i=1; i<=nd; i++) if (index(FILENAME, dir[i] "/")==1 && length(dir[i]) > length(m)) m=dir[i]
        blk=0; inb=0; nox=0; skip=(m=="")
        ex=out "/exports" sfx[m]; st=out "/structs" sfx[m]; pk=out "/packages"
    }
    skip { next }
    {
        if ($0 ~ /^type [A-Z][A-Za-z0-9_]* struct[{].*[}]$/) {
            b=$0; sub("^[^{]*[{]","",b); sub("[}]$","",b)
            gsub("[ \t]+"," ",b); gsub("^ | $","",b); print m "\t" $2 "\t" b > st }
        else if ($0 ~ /^type [A-Z][A-Za-z0-9_]* struct [{]$/) { name=$2; body=""; inb=1 }
        else if (inb && $0 ~ /^[}]/) { print m "\t" name "\t" body > st; inb=0 }
        else if (inb) { line=$0; sub("//.*","",line); sub("`[^`]*`","",line)
                        gsub("[ \t]+"," ",line); gsub("^ | $","",line)
                        if (line!="") body=body ";" line }
    }
    nox { next }
    /^package main$/ { nox=1; next }
    /^package / { print m "\tpackage " $2 > pk; next }
    /^\)/ { blk=0; next }
    /^(type|const|var) \($/ { blk=1; next }
    /^func \(/ { next }
    { if ($1=="func"||$1=="type"||$1=="const"||$1=="var") n=$2
      else if (blk && $0 ~ /^\t[A-Z]/) n=$1
      else next
      if (n !~ /^[A-Z]/ || $0 ~ /=[ \t]*[a-z][A-Za-z0-9_]*\./) next
      sub("[([].*","",n); print m "\t" n > ex }' 2>/dev/null
for x in exports structs exports-private structs-private packages; do sort -u -o "$RULES/$x" "$RULES/$x"; done
awk -F'\t' -v reg="$REG" -v exports="$RULES/exports" '
FILENAME==reg {
    if ($1=="concept") owner[$2]=$3
    else if ($1=="spells" || $1=="split") { k=++n; kind[k]=$1; id[k]=$2; mod[k]=$3; nm[k]=$4 }
    else if ($1=="idiom") idiom[$2]=1
    else if ($1=="shape") { p=++np; pa[p]=$2; pb[p]=$3; shp[$2 "\t" $3]=p; shp[$3 "\t" $2]=p }
    next
}
FILENAME==exports { ex[$1 "\t" $2]=1; where[$2]=where[$2] " " $1; modules[$2]++; next }
{ st[++ns]=$0 }
END {
    for (i=1; i<=n; i++) {
        if (!(id[i] in owner)) print "FAIL\t" kind[i] " " id[i] " names no declared concept"
        if (!((mod[i] "\t" nm[i]) in ex))
            print "FAIL\t" mod[i] "." nm[i] " is named here and exported nowhere — drop the row, or restore the name"
        else if (kind[i]=="spells" && owner[id[i]]!=mod[i])
            print "FAIL\t" mod[i] "." nm[i] " spells " id[i] ", which " owner[id[i]] " owns — make it an alias, or record a split"
        else if (kind[i]=="split")
            print "DEBT\t" id[i] " is implemented a second time in " mod[i] " as " nm[i] "; " owner[id[i]] " owns it"
        used[id[i]]=1; named[mod[i] "\t" nm[i]]=1
    }
    for (c in owner) if (!(c in used))
        print "FAIL\tconcept " c " is owned by " owner[c] " and nothing spells it"
    for (k in ex) {
        split(k, f, "\t")
        if (modules[f[2]] < 2) continue
        if (f[2] in idiom) { hits[f[2]]++; continue }
        if (!(k in named))
            print "FAIL\t" f[2] " is exported by" where[f[2]] " and scripts/concepts does not say whether those are one concept"
    }
    for (a in idiom) if (hits[a] < 2)
        print "FAIL\tidiom " a " is used by fewer than two published modules — drop the row"
    for (i=1; i<=ns; i++) {
        split(st[i], f, "\t")
        if (f[3]=="") continue
        if ((f[3] in lastmod) && lastmod[f[3]]!=f[1]) {
            a=lastmod[f[3]] "." lastname[f[3]]; b=f[1] "." f[2]
            if ((a "\t" b) in shp) proved[shp[a "\t" b]]=1
            else print "FAIL\t" a " and " b " are one layout in two layers: collapse it, or say in a shape row why they are not one concept"
        }
        lastmod[f[3]]=f[1]; lastname[f[3]]=f[2]
    }
    for (p=1; p<=np; p++) if (!(p in proved))
        print "FAIL\tshape " pa[p] " " pb[p] " no longer share a layout — drop the row"
}' "$REG" "$RULES/exports" "$RULES/structs" | sort > "$RULES/concepts"
metric exported_names "$(wc -l < "$RULES/exports")" check.sh
metric shared_exported_names "$(cut -f2 "$RULES/exports" | sort | uniq -d | wc -l)" check.sh
note "$(wc -l < "$RULES/exports") exported names in $(cut -f1 "$RULES/exports" | sort -u | wc -l) published Go modules, $(cut -f2 "$RULES/exports" | sort | uniq -d | wc -l) exported by more than one; $(wc -l < "$RULES/structs") struct layouts compared. Go only: C++ and Python would each need their own extractor."
if grep -q '^FAIL' "$RULES/concepts"; then
    fail "concepts ($(grep -c '^FAIL' "$RULES/concepts") unadjudicated)"
    sed -n 's/^FAIL\t/        /p' "$RULES/concepts"
else
    pass "concepts — every shared name and every shared layout is named in scripts/concepts"
    sed -n 's/^DEBT\t/        known duplication: /p' "$RULES/concepts"
fi

# Four services were named in one day, unexamined, while the rule above adjudicated
# only names two published modules shared. This asks every name, published or not,
# tracked or not, the outward question at birth: where did the word come from.
cat "$RULES/exports" "$RULES/exports-private" "$RULES/packages" | sort -u > "$RULES/exports-all"
cat "$RULES/structs" "$RULES/structs-private" > "$RULES/structs-all"
baseline uncited > "$RULES/old-names"
awk -F'\t' -v reg="$REG" -v allx="$RULES/exports-all" -v known="$RULES/old-names" '
FILENAME==reg {
    if ($1=="idiom") idiom[$2]=1
    else if ($1=="spells" || $1=="split") named[$3 "\t" $4]=1
    else if ($1=="cite" || $1=="novel") { b=++nb; bm[b]=$2; bn[b]=$3; bt[b]=$4; born[$2 "\t" $3]=1 }
    next
}
FILENAME==allx { all[$1 "\t" $2]=1; next }
FILENAME==known { old[$1 "\t" $2]=1; next }
{ st[++ns]=$0 }
function near(a,b) { a=tolower(a); b=tolower(b); return a==b || a "s"==b || a==b "s" }
END {
    for (i=1; i<=nb; i++) {
        k=bm[i] "\t" bn[i]
        if (!(k in all)) { print "FAIL\t" bm[i] "." bn[i] " has a row and is exported nowhere — drop the row, or restore the name"; continue }
        if (bn[i] ~ /^package /) continue
        for (o in all) {
            split(o, f, "\t")
            if (f[1]!=bm[i] || f[2]==bn[i] || !near(bn[i], f[2]) || index(bt[i], f[2])) continue
            print "FAIL\t" bm[i] "." bn[i] " is one letter from " bm[i] "." f[2] " in the same module — rename one, or name the other in the row and say which is which"
        }
        for (j=1; j<=ns; j++) {
            split(st[j], f, "\t"); if (f[1]!=bm[i]) continue
            nfl=split(f[3], fl, ";")
            for (q=1; q<=nfl; q++) {
                split(fl[q], w, " "); fname=w[1]; sub(",$","",fname)
                rest=substr(fl[q], length(w[1])+1)
                if (fname=="" || !near(bn[i], fname) || index(bt[i], f[2] "." fname)) continue
                if (rest ~ ("(^|[^A-Za-z0-9_])" bn[i] "([^A-Za-z0-9_]|$)")) continue
                print "FAIL\t" bm[i] "." bn[i] " is one letter from the field " f[2] "." fname " in the same module — rename one, or name the field in the row and say which is which"
            }
        }
    }
    for (k in all) {
        split(k, f, "\t")
        if (f[2] in idiom || (k in named) || (k in born) || (k in old)) continue
        print "FAIL\t" f[1] "." f[2] " is a new name: add cite (where this word already means this) or novel (the established word it rejected, and why) to scripts/concepts"
    }
    for (k in old) {
        split(k, f, "\t")
        if (!(k in all)) print "FAIL\t" f[1] "." f[2] " is recorded as older than the rule and exported nowhere — drop it from check.baseline"
        else if ((k in born) || (k in named)) print "FAIL\t" f[1] "." f[2] " has a row in scripts/concepts now — drop it from check.baseline"
    }
}' "$REG" "$RULES/exports-all" "$RULES/old-names" "$RULES/structs-all" | sort > "$RULES/ancestry"
metric names_born_cited "$(grep -c '^\(cite\|novel\)	' "$REG")" check.sh
metric names_older_than_rule "$(wc -l < "$RULES/old-names")" check.sh
if grep -q '^FAIL' "$RULES/ancestry"; then
    fail "ancestry ($(grep -c '^FAIL' "$RULES/ancestry") names with no word of where they came from)"
    sed -n 's/^FAIL\t/        /p' "$RULES/ancestry"
else
    pass "ancestry — $(wc -l < "$RULES/exports-all") names in $(cut -f1 "$RULES/exports-all" | sort -u | wc -l) modules; every one born after the rule says where its word came from, $(wc -l < "$RULES/old-names") older ones recorded"
fi

# ---- one store, not seven -----------------------------------------------
# "No layer writes atomic-write again" was written one morning and two agents
# wrote it that afternoon, and the count was four by reading and six by grep,
# because a method on a struct cannot be imported and so was never found. The
# shape is one thing in every language we ship: a string literal ending in
# .tmp — a FIXED name, which is what collides the day a second writer exists —
# and a rename within fifteen lines. A unique name from CreateTemp is the other
# defect, compare-then-rename, and it is the job store's; this cannot see it.
# A one-shot writer cannot be told from a store by reading it: config.Save was
# called once, from setup, and was still the fifth. So the exemption is a
# directory and never a comment: cas/ is the primitive, storage/ renames a blob
# once under its digest, research/ is measured and not shipped. Known ones are
# recorded both ways like everything else, so a new one fails the day it is
# written and a converted one fails until its line is dropped.
grep -E '\.(go|py|cpp|cc|c|h|hpp)$' "$RULES/files" \
  | grep -vE '^(cas|storage|research|forks|\.split)/|/_vendor/|_test\.(go|py|cpp)$|(^|/)test_[^/]*\.py$|/test/' > "$RULES/store-scan"
xargs -d '\n' awk '
    FNR==1 { at=0 }
    /"[^"*]*\.tmp"/ && !/CreateTemp|mkstemp|NamedTemporaryFile/ { at=FNR }
    at && FNR-at<=15 && /\.Rename\(|os\.(rename|replace)\(|::rename\(|MoveFileEx/ { print FILENAME; at=0 }
' < "$RULES/store-scan" 2>/dev/null | sort -u > "$RULES/stores"
baseline store > "$RULES/stores-known"
comm -23 "$RULES/stores" "$RULES/stores-known" > "$RULES/stores-new"
comm -13 "$RULES/stores" "$RULES/stores-known" > "$RULES/stores-gone"
metric hand_rolled_stores "$(wc -l < "$RULES/stores")" check.sh
note "$(wc -l < "$RULES/store-scan") source files read for a fixed-name .tmp within fifteen lines of a rename, outside cas/ and storage/; $(wc -l < "$RULES/stores") found, $(wc -l < "$RULES/stores-known") recorded"
if [ -s "$RULES/stores-new" ]; then
    fail "stores ($(wc -l < "$RULES/stores-new") hand-rolled where cas.Change is one import away)"
    sed 's/^/        /' "$RULES/stores-new"
elif [ -s "$RULES/stores-gone" ]; then
    fail "stores ($(wc -l < "$RULES/stores-gone") recorded and now gone — drop them from check.baseline)"
    sed 's/^/        /' "$RULES/stores-gone"
else
    pass "stores — none but the recorded set"
    sed 's/^/        still hand-rolled: /' "$RULES/stores"
fi

# ---- no leaked identifier ----------------------------------------------
USERID="$(basename "${HOME:-/x}")"
GITNAME="$(git config user.name 2>/dev/null | tr 'A-Z ' 'a-z_')"
GITMAIL="$(git config user.email 2>/dev/null)"
LEAKRE="(/Users/|/home/|Users[\\/]+)(${USERID}|${GITNAME}|${GITNAME%%_*})\b"
LEAKRE="$LEAKRE|${USERID}@|${GITMAIL:-__none__}"
LEAKRE="$LEAKRE|\buid=[0-9]+|\b[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}\b"
LEAKRE="$LEAKRE|(hf_|ghp_|gho_|github_pat_|xox[baprs]-)[A-Za-z0-9_]{16,}|AKIA[0-9A-Z]{16}"
LEAKRE="$LEAKRE|BEGIN [A-Z ]*PRIVATE KEY"
LEAKRE="$LEAKRE|\b[A-Za-z0-9][A-Za-z0-9-]{2,}\.local\b"
LEAKRE="$LEAKRE|\b(25[0-5]|2[0-4][0-9]|1?[0-9]?[0-9])(\.(25[0-5]|2[0-4][0-9]|1?[0-9]?[0-9])){3}\b"
grep -vE '^(CLAUDE|VISION|PROGRESS|AGENTS|FOOTPRINT|MAP|DREAM|METHOD)\.md$' "$RULES/scan" > "$RULES/public"
NPUB=$(wc -l < "$RULES/public")
xargs -d '\n' grep -anIE "$LEAKRE" < "$RULES/public" 2>/dev/null \
  | grep -vE '(127\.0\.0\.1|0\.0\.0\.0|255\.255\.255\.255|localhost)' \
  | cut -d: -f1,2 | sort -u > "$RULES/leaks"
baseline leak > "$RULES/known-leaks"
cut -d: -f1 "$RULES/leaks" | sort -u > "$RULES/leak-files"
comm -23 "$RULES/leak-files" "$RULES/known-leaks" > "$RULES/new-leaks"
metric leaked_identifier_files "$(wc -l < "$RULES/leak-files")" check.sh
note "$(wc -l < "$RULES/leaks") identifier hits in $NPUB public-bound files, $(wc -l < "$RULES/leak-files") files, $(wc -l < "$RULES/known-leaks") known"
if [ -s "$RULES/new-leaks" ]; then
    fail "leaked identifiers ($(wc -l < "$RULES/new-leaks") new files)"
    while read -r f; do grep "^$f:" "$RULES/leaks" | head -3 | sed 's/^/        /'; done < "$RULES/new-leaks"
else
    pass "leaked identifiers — none outside the recorded set"
fi

# ---- documents do not grow faster than tested code ---------------------
git ls-files | tr '\n' '\0' | xargs -0 wc -l 2>/dev/null > "$RULES/sizes"
awk '$2!="total" {
    n=$1; f=$2
    if (f ~ /\.(md|txt)$/) prose+=n
    else if (f ~ /(_test\.(go|py|cpp)|\/test_|\/test\/|^test_)/) t+=n
    else if (f ~ /\.(go|py|cpp|hpp|h|sh|ps1|cmd)$/) c+=n
} END {
    printf "  \033[90mtree: %d prose lines, %d code, %d test — prose:tested = %s\033[0m\n", prose, c, t, (t ? sprintf("%.2f", prose/t) : "no tested code")
    printf "%d %d %d\n", prose, c, t > "'"$RULES/tree"'"
}' "$RULES/sizes"
read -r P C T < "$RULES/tree"
[ "$T" -gt 0 ] && metric prose_tested_tree "$(awk -v p="$P" -v t="$T" "BEGIN{printf \"%.2f\", p/t}")" check.sh
git log --numstat --pretty=format: | awk 'NF==3 && $1 ~ /^[0-9]+$/ {
    if ($3 ~ /\.(md|txt)$/) prose+=$1
    else if ($3 ~ /(_test\.(go|py|cpp)|\/test_|\/test\/)/) t+=$1
    else if ($3 ~ /\.(go|py|cpp|hpp|h|sh|ps1|cmd)$/) c+=$1
} END {
    printf "  \033[90mwritten (all commits): %d prose lines, %d code, %d test — prose:tested = %s\033[0m\n", prose, c, t, (t ? sprintf("%.2f", prose/t) : "no tested code")
    printf "  \033[90mMETHOD.md 7 asks for this number. It does not gate the build.\033[0m\n"
}'

section "documents"
# What our own documents assert about the tree and about his machines.
eval "$(<"$ROOT/scripts/documents.sh")"

section "series"
# The gate has always printed these and thrown them away, so nobody could say
# whether any of them was moving. research/gate/series.tsv already existed and
# already held the numbers recoverable WITHOUT running anything; what only a run
# knows — how many invariants a scenario actually cites, how many verdicts
# diverge — is what this adds. Writing into that file rather than a second one is
# the whole decision: a project that has three times built a ledger it then
# maintained by hand does not need a fourth.
#
# Append-only, and only when a value MOVED, so a hundred green runs do not become
# a hundred identical rows. A metric this run could not measure — --fast skips
# half of them — is written not at all, because a skipped harness recorded as 0
# reads as a fixed one.
SERIES="$ROOT/research/gate/series.tsv"
MODE=full; [ "$FAST" = 1 ] && MODE=fast; [ "$PUBLIC" = 1 ] && MODE=public
metric "gate_${MODE}_minutes" "$(( (${EPOCHREALTIME/./} - T0) / 60000000 ))" check.sh
NMOVED=0
for k in $SERIES_KEYS; do
    v="${METRIC[$k]:-}"
    [ -n "$v" ] || continue
    was="$(awk -F'\t' -v k="$k" '$3==k {v=$4} END {print v}' "$SERIES" 2>/dev/null)"
    [ "$was" = "$v" ] && continue
    printf '%s\t%s\t%s\t%s\t%s\n' "$(date +%Y-%m-%d)" \
        "$(git rev-parse --short HEAD 2>/dev/null)" "$k" "$v" "${METRIC_FROM[$k]}" >> "$SERIES"
    note "$k: ${was:-first} -> $v"
    NMOVED=$((NMOVED+1))
done
note "$NMOVED of ${#METRIC[@]} measured numbers moved and were appended to research/gate/series.tsv"

section "result"
secs $((${EPOCHREALTIME/./} - T0))
note "sections:$SECTIONS — $SECS in all"
if [ ${#FAILED[@]} -eq 0 ]; then
    printf '  \033[32mPASS\033[0m — everything agrees\n\n'
    exit 0
fi
printf '  \033[31m%d failed:\033[0m %s\n\n' "${#FAILED[@]}" "${FAILED[*]}"
if [ -f "$LOGS/failed" ]; then
    while read -r log; do
        printf '\033[1m--- %s ---\033[0m\n' "$(basename "$log")"
        tail -25 "$log"
        echo
    done < "$LOGS/failed"
fi
exit 1
