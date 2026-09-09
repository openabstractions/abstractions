#!/usr/bin/env bash
# Everything that has to pass, in one command.
#
# There were sixteen scripts and no way to run them all, so every session ran
# whichever ones came to mind and none of them proved the whole thing still
# worked. The cross-language harnesses are the only evidence three
# implementations agree; a harness nobody remembers to run is not evidence.
#
#   usage: scripts/check.sh [--fast] [--public] [--verbose]
#     --fast    skip the MSVC C++ build and the cross-language harnesses, which
#               take minutes. Go, Python, and the C++ under g++ in WSL, which
#               is under a minute and runs in every mode.
#     --public  also fetch the openabstractions repositories: prove no file
#               lives in two of them, and let the rules read the files each
#               repository authors for itself, which no rule derived from
#               split.manifest can see. Needs the network; a few seconds.
#     --verbose every offending path a failed rule found, instead of the first
#               six and a count. One rule listing 139 files is what made this
#               output something people scrolled past rather than read. Wired
#               through the documents section; the other sections still print
#               every offender.
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
VERBOSE=0
for a in "$@"; do
    case "$a" in
        --fast) FAST=1 ;;
        --public) PUBLIC=1 ;;
        --verbose) VERBOSE=1 ;;
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
NRAN=0
NSKIPPED=0
NUNPROVEN=0
SLOW=""
LAP=$T0
secs() { printf -v SECS '%d.%ds' $(($1/1000000)) $(($1/100000%10)); }
lap() { local now=${EPOCHREALTIME/./}; LAPUS=$((now-LAP)); secs "$LAPUS"; LAP=$now; }
ran() { NRAN=$((NRAN+1)); SLOW="$SLOW$LAPUS	$1"$'\n'; }
pass() { lap; printf '  \033[32mok\033[0m    %s \033[90m%s\033[0m\n' "$1" "$SECS"; ran "$1"; }
fail() { lap; printf '  \033[31mFAIL\033[0m  %s \033[90m%s\033[0m\n' "$1" "$SECS"; ran "$1"; FAILED+=("$1"); }
# A rule that quietly stops running is the defect this whole file is about, so
# a skip is counted and named rather than only printed.
skip() { lap; NSKIPPED=$((NSKIPPED+1)); printf '  \033[33mskip\033[0m  %s\n' "$1"; }
# Counted apart from a skip: a tool this run did not have is an inconvenience,
# a platform this run could not reach is the thing we say out loud.
unproven() { lap; NUNPROVEN=$((NUNPROVEN+1)); printf '  \033[33mUNPROVEN\033[0m  %s\n' "$1"; }
# A rule whose input set is empty passes on nothing, which is how this project
# published green for two days. Every rule that can see an empty set says so.
nothing() { fail "$1 — $2 inputs, and this rule cannot fail on fewer than $3"; }
# What a failed rule caught. A rule that listed 139 paths made the section too
# long to read, and a reader who scrolls past a failure is a reader who did not
# see it. The count is the answer; the paths are behind --verbose.
DETAIL_LINES=6
detail() {
    local n; n=$(wc -l < "$1")
    if [ "$VERBOSE" = 1 ] || [ "$n" -le "$DETAIL_LINES" ]; then
        sed 's/^/        /' "$1"
    else
        head -"$DETAIL_LINES" "$1" | sed 's/^/        /'
        printf '        \033[90mand %d more — run with --verbose\033[0m\n' "$((n - DETAIL_LINES))"
    fi
}
SECTIONS=""; SECTION=""; SECTION_T0=$T0
section() {
    local d=$((${EPOCHREALTIME/./} - SECTION_T0))
    secs "$d"
    if [ -n "$SECTION" ]; then
        SECTIONS="$SECTIONS $SECTION $SECS"
        # Minutes, not seconds: a section's seconds move on every run of a
        # loaded laptop, and a series that moves every run records noise.
        local k="gate_minutes_${SECTION//[^a-zA-Z0-9]/_}"
        metric "$k" "$((d/60000000))" check.sh
        SERIES_KEYS="$SERIES_KEYS $k"
    fi
    SECTION="$1"; SECTION_T0=${EPOCHREALTIME/./}
    printf '\n\033[1m%s\033[0m\n' "$1"
}
pinned() {  # pinned [VAR=value ...] script [args] — read once, run from memory
    local -a e=()
    while [[ $1 =~ ^[A-Za-z_][A-Za-z0-9_]*= ]]; do e+=("$1"); shift; done
    env "${e[@]}" bash -c 'eval "$(<"$0")"' "$@"
}
# GNU sed 4.9 rejects s#\\#/#g — a backslash before a non-slash delimiter is not
# an escape it will accept — so this returned nothing and the go section below
# built and tested zero modules, silently, on every run.
modules() { (cd "$1" && go list -m -f '{{.Dir}}' 2>/dev/null) | tr '\\\\' '/' | sed "s#^$(cd "$1" && pwd -W)/##"; }
declare -A METRIC METRIC_FROM
metric() { METRIC[$1]="$2"; METRIC_FROM[$1]="$3"; }
SERIES_KEYS="behaviour_divergences behaviour_unproven content_models invariants_cited invariants_declared
verdict_divergences citations_examined unresolved_citations shipped_dependencies
leaked_identifier_files exported_names shared_exported_names kills_fired kills_uncarried
footprint_rows footprint_untestable footprint_contradicted research_unindexed
demands_declared demands_repeated feedback_without_verdict discarded_errors
prose_tested_tree map_unnamed front_doors_open front_doors_published hand_rolled_stores
struck_section_unmarked
wasted_bytes_after_kill
peer_corpus_peers peer_corpus_disagreements peer_corpus_roundtrip_differs peer_corpus_wire_refused
cpp_toolchains_built pins_named pins_dead_measured
gate_fast_minutes gate_full_minutes gate_public_minutes"

# Output is kept and only shown for what failed. A passing run should be short
# enough to read, or it will not be read.
LOGS="$(mktemp -d)"
# A run that dies halfway prints a section that looks exactly like a section
# that finished, and only the exit status says otherwise — which is the one
# thing a reader scrolling 800 lines never sees.
FINISHED=0
trap 'rm -rf "$LOGS"; [ "$FINISHED" = 1 ] || printf "\n  \033[31mABORTED\033[0m — the gate stopped before its result block. Nothing above it is a pass.\n\n"' EXIT

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
grep -E '^[a-z]+/(README|CONTRACT)\.md$' "$RULES/files" > "$RULES/pages"
xargs -d '\n' grep -ahoE "$TAG" < "$RULES/pages" 2>/dev/null | tr -d '[]' | sort -u > "$RULES/tags-declared"
grep -vxFf "$RULES/pages" "$RULES/files" \
  | xargs -d '\n' grep -aoHE "$TAG" 2>/dev/null | tr -d '[]' | sed 's/:\([^:]*\)$/\t\1/' | sort -u > "$RULES/tags-cited"
cut -f2 "$RULES/tags-cited" | sort -u | comm -13 "$RULES/tags-declared" - > "$RULES/tags-stray"
if [ "$NFILES" = 0 ]; then
    nothing "contract tags" 0 1
elif [ -s "$RULES/tags-stray" ]; then
    fail "contract tags ($(wc -l < "$RULES/tags-stray") cited and declared on no page)"
    grep -Ff <(sed 's/^/\t/' "$RULES/tags-stray") "$RULES/tags-cited" > "$RULES/tags-stray-where"
    detail "$RULES/tags-stray-where"
else
    pass "contract tags — $(wc -l < "$RULES/tags-declared") declared on $(wc -l < "$RULES/pages") pages, $(cut -f2 "$RULES/tags-cited" | sort -u | wc -l) cited in $(cut -f1 "$RULES/tags-cited" | sort -u | wc -l) files, every citation resolves"
fi

# A conformance expectation names the rule it exercises, in the one place the
# notation puts it: a run of [TAG] at the end of the line. Writing that name is
# where two rules stating opposite things become visible, because there is no
# name to write.
#
# The rule above cannot stand in for this one, and deleting either as a
# duplicate loses a case the other cannot see. It accepts any */README.md as a
# declaring page, so a layer stating its rules on a README declares the very
# tags its scenarios cite; and it reads whole files, so a rule named in a
# scenario's prose counts as cited with no expectation testing it.
#
# Which documents declare a rule is idl/gen/tags.go's ruleDocuments and is read
# from there rather than restated, so the generated contract page and the gate
# cannot drift apart on the question the way the two declaring sets already
# have. The back-reference rule is its declarations(): a tag preceded by "(" is
# a pointer to a rule stated elsewhere, not a statement of one.
EXP="$LOGS/expect"; mkdir -p "$EXP"
awk '/^var ruleDocuments = \[\]string\{/ { on = 1; next }
     on && /^\}/                        { on = 0 }
     on && match($0, /"[^"]+"/)         { print substr($0, RSTART + 1, RLENGTH - 2) }' \
    "$ROOT/idl/gen/tags.go" > "$EXP/ruledocs"
: > "$EXP/pages"; NPAGES=0
while read -r d; do
    [ -f "$ROOT/$d" ] || continue
    echo "$ROOT/$d" >> "$EXP/pages"; NPAGES=$((NPAGES + 1))
done < "$EXP/ruledocs"
git ls-files | grep -E '(^|/)scenarios/[^/]+\.txt$' > "$EXP/scenarios"
xargs -r -d '\n' awk '{
    line = $0
    while (match(line, /\[[A-Z]+-[A-Z]*[0-9]+\]/)) {
        if (!(RSTART > 1 && substr(line, RSTART - 1, 1) == "(")) print substr(line, RSTART + 1, RLENGTH - 2)
        line = substr(line, RSTART + RLENGTH)
    }
}' < "$EXP/pages" | sort -u > "$EXP/declared"
xargs -r -d '\n' awk '
FNR == 1 { name = FILENAME; sub(/.*\//, "", name); sub(/\.txt$/, "", name) }
/^#[ \t]*expect[ \t]+[0-9]+[ \t]*:/ {
    step = $0; sub(/^#[ \t]*expect[ \t]+/, "", step); sub(/[^0-9].*$/, "", step)
    line = $0; sub(/[ \t]+$/, "", line)
    n = 0
    while (match(line, /\[[A-Z]+-[A-Z]*[0-9]+\]$/)) {
        tag[++n] = substr(line, RSTART + 1, RLENGTH - 2)
        line = substr(line, 1, RSTART - 1); sub(/[ \t]+$/, "", line)
    }
    printf "expect\t%s\t%s\t%s\n", name, step, (n ? "tagged" : "untagged")
    for (i = 1; i <= n; i++) printf "tag\t%s\t%s\t%s\n", name, step, tag[i]
    while (match(line, /\[[A-Z]+-[A-Z]*[0-9]+\]/)) {
        printf "misplaced\t%s\t%s\t%s\n", name, step, substr(line, RSTART + 1, RLENGTH - 2)
        line = substr(line, RSTART + RLENGTH)
    }
}' < "$EXP/scenarios" > "$EXP/records"
: > "$EXP/breaks"
echo 'NEXP=0 NTAGGED=0 NDECL=0 NUNEX=0 NBREAK=0' > "$EXP/counts"
awk -F'\t' -v breaks="$EXP/breaks" -v counts="$EXP/counts" -v decl="$EXP/declared" -v base="$BASE" '
FILENAME == decl                 { known[$0] = 1; next }
FILENAME == base && $1 == "expect-tag"      { wastag[$2 "\t" $3 "\t" $4] = 1; next }
FILENAME == base && $1 == "expect-untagged" { wasbare[$2] = $3; next }
FILENAME == base                 { next }
$1 == "expect"                   { nexp++; if ($4 == "tagged") ntagged++; else bare[$2]++; next }
$1 == "misplaced"                { say($2 " step " $3 " writes [" $4 "] inside the expectation — a rule tag goes at the end of the line, where the runner reads it"); next }
$1 == "tag" {
    if ($4 in known) { exercised[$4] = 1; next }
    k = $2 "\t" $3 "\t" $4
    if (k in seen) next
    seen[k] = 1
    if (!(k in wastag)) say($2 " step " $3 " names [" $4 "], which no rule document declares")
}
END {
    for (k in wastag) if (!(k in seen)) { split(k, f, "\t"); say(f[1] " step " f[2] " no longer cites an unresolved [" f[3] "] — drop the line from check.baseline") }
    for (s in bare)   if (wasbare[s] != bare[s]) say(s " has " bare[s] " expectations naming no rule; check.baseline records " (s in wasbare ? wasbare[s] : "none") " — an expectation names the rule it exercises, and the recorded count only falls")
    for (s in wasbare) if (!(s in bare)) say(s " is recorded with " wasbare[s] " expectations naming no rule and now has none — drop the line from check.baseline")
    for (t in known) { ndecl++; if (!(t in exercised)) nunex++ }
    printf "NEXP=%d NTAGGED=%d NDECL=%d NUNEX=%d NBREAK=%d\n", nexp, ntagged, ndecl, nunex, nbreak > counts
}
function say(s) { nbreak++; print s > breaks }' "$EXP/declared" "$BASE" "$EXP/records"
. "$EXP/counts"
metric expectations_tagged "$NTAGGED" check.sh
metric expectations_untagged "$((NEXP - NTAGGED))" check.sh
metric rules_unexercised "$NUNEX" check.sh
SERIES_KEYS="$SERIES_KEYS expectations_tagged expectations_untagged rules_unexercised"
if [ "$NEXP" = 0 ] || [ "$NDECL" = 0 ]; then
    nothing "expectation tags" "$NEXP" 1
elif [ "$NBREAK" != 0 ]; then
    fail "expectation tags ($NBREAK unrecorded)"
    detail "$EXP/breaks"
else
    pass "expectation tags — $NTAGGED of $NEXP expectations name a rule, every name resolves against $NPAGES rule documents, $NUNEX of $NDECL declared rules are exercised by no expectation"
fi

section "generated"
# A generated file that has stopped matching its definition is a lie that
# compiles. site/schema.html was written by a command somebody remembered to
# type and compared against nothing, so an edit to job/job.thrift would
# have published a page describing a definition that no longer existed.
#
# The ancestor is `go generate` with a CI step asserting the tree is unchanged
# afterwards. scripts/generate.sh is the generate; this is the assertion, and it
# writes only into $LOGS — several agents are live in this tree and a rule that
# rewrites files is the shape of every sweep this project has paid for.
GENS="$LOGS/generated"; mkdir -p "$GENS"
sed 's/#.*//' "$ROOT/scripts/generate.targets" | grep -v '^[[:space:]]*$' > "$GENS/targets"
NGEN=$(wc -l < "$GENS/targets")
# Which definition a stale file should have come from: the line whose output
# root the file sits under.
gensource() { awk -v f="$1" 'index(f, $2 "/") == 1 {print $1; exit}' "$GENS/targets"; }
if [ "$NGEN" = 0 ]; then
    nothing "generated files" 0 1
elif ! sh "$ROOT/scripts/generate.sh" --into "$GENS/tree" > "$GENS/log" 2>&1 \
     || ! sh "$ROOT/scripts/generate.sh" --paths > "$GENS/paths" 2>>"$GENS/log"; then
    fail "generated files (scripts/generate.sh did not finish — a declared definition is gone, or the generator refused one)"
    echo "$GENS/log" >>"$LOGS/failed"
else
    (cd "$GENS/tree" && find . -type f) | sed 's|^\./||' | sort > "$GENS/expected"
    git ls-files > "$GENS/tracked"
    : > "$GENS/stale"
    while read -r f; do
        if ! grep -qxF "$f" "$GENS/tracked"; then
            printf '%s\tis generated and git does not track it\n' "$f" >> "$GENS/stale"
        elif ! cmp -s "$GENS/tree/$f" "$ROOT/$f"; then
            printf '%s\tno longer matches %s — run sh scripts/generate.sh\n' "$f" "$(gensource "$f")" >> "$GENS/stale"
        fi
    done < "$GENS/expected"
    # The other direction, and the reason a line left out of generate.targets
    # hides nothing: a tracked file whose path ends in one of the relative paths
    # the generator emits is generated, whoever declared it. The paths are asked
    # of the generator rather than written here, so a new backend is covered on
    # the day it lands.
    awk 'NR==FNR {p[FNR]=$0; n=FNR; next}
         { for (i = 1; i <= n; i++) {
               at = index($0, "/" p[i])
               if ($0 == p[i] || (at > 0 && at == length($0) - length(p[i]))) { print; break }
           } }' \
        "$GENS/paths" "$GENS/tracked" | sort > "$GENS/artefacts"
    comm -23 "$GENS/artefacts" "$GENS/expected" > "$GENS/undeclared"
    NPATHS=$(wc -l < "$GENS/paths")
    NART=$(wc -l < "$GENS/expected")
    if [ "$NPATHS" = 0 ] || [ "$NART" = 0 ]; then
        nothing "generated files" "$NART" 1
    elif [ -s "$GENS/stale" ] || [ -s "$GENS/undeclared" ]; then
        { cat "$GENS/stale"
          sed 's|$|\tis generated and scripts/generate.targets declares nothing that writes it|' "$GENS/undeclared"
        } > "$GENS/bad"
        fail "generated files ($(wc -l < "$GENS/stale") stale or untracked, $(wc -l < "$GENS/undeclared") committed under no declared output root)"
        detail "$GENS/bad"
    else
        # How much of a definition an artefact carries is read out of the
        # generator's own report rather than restated here: the surface list on
        # a target line and the closure the generator computes from it are one
        # fact, and two places holding it is how the two would drift.
        NSEL=$(grep -c ' of [0-9]* surfaces:' "$GENS/log")
        pass "generated files — $NART artefacts from $NGEN targets, byte-identical to a fresh run, $NSEL of them carrying a declared subset of their definition; $NPATHS emitted paths searched for undeclared ones"
    fi
fi

section "go"
printf '  pid %s\n' "$$"
# Read once and reused by platforms and by the linux run below. The list came
# back empty for two days and three whole sections silently examined nothing;
# asking go three times for the same answer only widened the window.
mapfile -t MODS < <(modules "$ROOT")
[ "${#MODS[@]}" -gt 0 ] || nothing "go modules" 0 1
for d in "${MODS[@]}"; do
    run "$d" bash -c "cd '$ROOT/$d' && go build ./... && go test ./..."
done
# -race needs cgo, and go itself reports CGO_ENABLED=0 when it cannot find a C
# compiler. parallel.go is the first concurrent code in the tree and shipped
# without ever running under the detector; a line that says so beats one that
# passes on nothing.
if [ "$(go env CGO_ENABLED 2>/dev/null)" = "1" ]; then
    for d in job/go download/go; do
        run "$d -race" bash -c "cd '$ROOT/$d' && go test -race ./..."
    done
else
    skip "race detector — -race needs cgo and there is no C compiler for go env CC=$(go env CC 2>/dev/null)."
    printf '        WSL Ubuntu has go and gcc and lacks only libc6-dev; the Mac has clang.\n'
fi

section "platforms"
# Everything above builds for the machine the gate is on, which for two days was
# the only machine anything was built for. identity/listen/listen_unix.go was
# committed and published with two unconditional infinite recursions and a type
# error, and asks imports it and download imports asks, so no part of the
# download layer built for linux at all. Nothing here noticed, because nothing
# here ever set GOOS. logging/go had a _linux test file written against an API
# that had since been replaced.
#
# go vet type-checks, which is what catches a file the build tags hide, and it
# is seconds. It is not enough on its own — `func (l unixListener) Close() error
# { return l.Close() }` compiles anywhere — so the run below is the other half.
for os in linux darwin; do
    for d in "${MODS[@]}"; do
        run "$os $d" env GOOS="$os" GOARCH=amd64 bash -c "cd '$ROOT/$d' && go vet ./..."
    done
done

# Cross-compiling proves a file parses. Only running it proves the code does
# what its name claims, and the unix socket paths, /proc, SO_PEERCRED and the
# platform's own idea of what a closed peer looks like are exactly what cannot
# be cross-compiled into evidence. WSL is the linux this machine has; when it is
# not there this says UNPROVEN and never passes, because a gate that goes green
# on an absent platform is the defect it was meant to catch.
if command -v wsl.exe >/dev/null 2>&1 && wsl.exe -e sh -c 'command -v go' >/dev/null 2>&1; then
    W="$(wsl.exe -e wslpath -a "$(cygpath -w "$ROOT" 2>/dev/null || echo "$ROOT")" 2>/dev/null | tr -d '\r')"
    for d in "${MODS[@]}"; do
        run "linux run $d" wsl.exe -e sh -c "cd '$W/$d' && go test ./... >/dev/null"
    done
else
    unproven 'linux run — no wsl.exe with go on it. Nothing on this run has'
    printf '            executed a line of the unix half of anything.\n'
fi
# macOS stays UNPROVEN and is said out loud rather than left to a green gate:
# the Mac is the owner's laptop, it is not infrastructure, and identity's
# bind_darwin, codesign_darwin, sysctl_darwin and identity_darwin have been
# type-checked above and run by nobody.
unproven 'darwin run — vet only. identity/{bind,codesign,sysctl,identity}_darwin.go'
printf '            and job/go/awake_unix.go on macOS are compiled here and executed nowhere.\n'

# Bytes not re-fetched after a kill. The only axis on which this layer beats
# curl, wget and aria2c — 11.6 MB against 3.22 GB at 8 GiB — and until now the
# only one nothing measured, while five harnesses measured bytes per second.
WB="$LOGS/wasted"
if (cd "$ROOT/download/go" && go test -run TestNothingIsFetchedTwiceAfterAKill -v -count=1 .) >"$WB" 2>&1; then
    metric wasted_bytes_after_kill "$(awk -F'\t' '/wasted_bytes/{print $2}' "$WB" | tail -1)" check.sh
    pass "wasted bytes after a kill — $(awk -F'\t' '/wasted_bytes/{print $2}' "$WB" | tail -1) of 67108864"
else
    fail "wasted bytes after a kill"; echo "$WB" >>"$LOGS/failed"
fi

section "head"
# What is committed, not what is here. Twice in one day a commit swept in the
# call sites of a function whose definition stayed uncommitted: every machine
# with this working tree built, and no clone could. Compiled, never run.
H="$ROOT/.build/head"
rm -rf "$H"; mkdir -p "$H"
git -C "$ROOT" archive HEAD | tar -x -C "$H"
mapfile -t HMODS < <(modules "$H")
[ "${#HMODS[@]}" -gt 0 ] || nothing "HEAD go modules" 0 1
for d in "${HMODS[@]}"; do
    run "HEAD $d" bash -c "cd '$H/$d' && go build ./... && go test -count=1 -run '^$' ./..."
done
run "HEAD python" "$PY" -m compileall -q "$H"
if [ "$FAST" = 1 ]; then
    skip "HEAD c++ (--fast)"
elif [ -z "$CMAKE" ]; then
    unproven 'HEAD c++ — no cmake here, so whether the committed C++ builds under MSVC is not known; the linux leg below builds the working tree, never HEAD'
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
# unittest exits 0 when discovery finds no test at all, so a renamed file, a
# moved directory or an import error in the package under test reads exactly
# like a green run. It prints the count itself; this reads it back.
pytests() {  # pytests <label> <directory>
    local log="$LOGS/py_${1//[^a-zA-Z0-9]/_}.log"
    if ! (cd "$ROOT/$2" && "$PY" -m unittest discover -p 'test_*.py') >"$log" 2>&1; then
        fail "$1"; echo "$log" >>"$LOGS/failed"
    elif grep -q '^Ran 0 tests' "$log"; then
        fail "$1 — discovery found no test to run, and unittest calls that a pass"
    else
        pass "$1 — $(sed -n 's/^Ran \([0-9]*\) tests\?.*/\1/p' "$log" | tail -1) tests"
    fi
}
pytests "job/python"        job/python
pytests "download/python"   download/python
run "adopters/comfyui"      bash -c "cd '$ROOT/adopters/comfyui' && '$PY' test_node.py"
pytests "model/python"      model/python
pytests "cas/python"        cas/python
# Go decides for the tools, Python decides for the broker and for ComfyUI. One
# token of disagreement and two applications each believe they are sharing a
# model with the other while the machine holds two.
run "model identity"        pinned PY="$PY" "$ROOT/scripts/identity-conformance.sh"

section "conformance"
# The proof this project publishes, and the one thing nothing here ran.
# conformance/ judges an implementation against the rules written on the
# contract pages, reads no source tree of ours, and reached a green verdict from
# a clean clone on 2026-09-09. Nothing in this file called it, so the gate could
# be green over a broken proof for as long as nobody thought to run it by hand.
#
# Assembled here into the shape scripts/split.manifest publishes, because that
# shape is the only one run.sh knows: fixture.py and scenarios/ beside it, the
# contract pages under contracts/. The pages are this tree's own CONTRACT.md
# files, which is what the three layer repositories serve; whether those copies
# still agree is split.sh's question, and fetching them would put the network
# into a gate that runs every few minutes.
CONF="$LOGS/conf"
mkdir -p "$CONF/scenarios" "$CONF/contracts"
cp -R "$ROOT/conformance/." "$CONF/" 2>/dev/null
cp "$ROOT/download/testdata/fixture.py" "$CONF/fixture.py" 2>/dev/null
cp "$ROOT"/download/testdata/scenarios/*.txt "$CONF/scenarios/" 2>/dev/null
cp "$ROOT/download/CONTRACT.md" "$CONF/contracts/download.md" 2>/dev/null
cp "$ROOT/job/CONTRACT.md"      "$CONF/contracts/job.md"      2>/dev/null
cp "$ROOT/watch/README.md"      "$CONF/contracts/watch.md"    2>/dev/null
: > "$CONF/absent"
for f in run.sh selftest.sh contracts.list fixture.py \
         contracts/download.md contracts/job.md contracts/watch.md; do
    [ -s "$CONF/$f" ] || printf '%s\tthe suite reads this and this tree did not supply it\n' "$f" >> "$CONF/absent"
done
NSCEN=$(ls "$CONF/scenarios"/*.txt 2>/dev/null | wc -l)
# What --fast runs instead, and it is named here rather than counted, because a
# subset nobody can read is a subset nobody can argue with. Greedy cover over
# the rule tags the scenarios cite, so ten scenarios reach most of the rules the
# whole directory reaches for a fifth of the wall time; research/gate120 holds
# the measurement. wire-ignored-range is in the list to start the fixture —
# without it the driver cannot declare wire, `wanted` is out of reach, and the
# subset could never be anything but UNPROVEN.
CONF_SUBSET="matrix recall failure-endings intent notice-quiet wanted pause-adoption foreign-sink terminal wire-ignored-range"
if [ -s "$CONF/absent" ]; then
    fail "conformance ($(wc -l < "$CONF/absent") of the suite's own inputs are missing from this tree, so it judged nothing)"
    detail "$CONF/absent"
elif [ "$NSCEN" -lt 40 ]; then
    nothing "conformance" "$NSCEN scenario" 40
elif ! (cd "$ROOT/download/go" && go build -o "$CONF/replay.exe" ./cmd/replay) >"$CONF/build.log" 2>&1; then
    fail "conformance — the reference driver will not build, so the published suite had nothing to judge"
    echo "$CONF/build.log" >>"$LOGS/failed"
else
    # A runner that calls our driver green proves nothing until it can still say
    # no. selftest.sh puts three toy drivers to it — one declaring nothing, one
    # answering ok to everything, one admitting it has no HTTP — and demands
    # not-set-up, failed and incomplete back.
    run "conformance runner" sh "$CONF/selftest.sh"

    ONLY=""; OVER="all $NSCEN scenarios"; WHOLE=1
    if [ "$FAST" = 1 ]; then
        WHOLE=0
        for s in $CONF_SUBSET; do ONLY="$ONLY --only $s"; done
        OVER="$(echo $CONF_SUBSET | wc -w) of $NSCEN scenarios: $CONF_SUBSET"
    fi
    CV="$CONF/run.log"
    sh "$CONF/run.sh" $ONLY -- "$CONF/replay.exe" >"$CV" 2>&1; cv=$?
    NRULE=$(sed -n 's/^    passed: *//p' "$CV")
    NUNREACH=$(sed -n 's/^    unreachable: *//p' "$CV")
    metric conformance_rules_kept "${NRULE:-0}" conformance/run.sh
    metric conformance_rules_unreachable "${NUNREACH:-0}" conformance/run.sh
    # Only a whole run's numbers go in the series. A subset's 52 recorded beside
    # a full run's 84 is a series that moves because a flag moved.
    [ "$WHOLE" = 1 ] && SERIES_KEYS="$SERIES_KEYS conformance_rules_kept conformance_rules_unreachable"
    case "$cv" in
    0) if [ "$WHOLE" = 1 ]; then
           pass "conformance — ${NRULE:-0} rules the contract pages state, kept over $OVER"
       else
           pass "conformance SUBSET — ${NRULE:-0} rules kept over $OVER. This is not the suite; drop --fast for that"
       fi ;;
    1) sed -n '/^  FAIL  /,/^          observed:/p' "$CV" > "$CONF/broke"
       fail "conformance ($(sed -n 's/^  [0-9]* scenarios passed, \([0-9]*\) failed.*/\1/p' "$CV") scenarios broke a rule a contract page states, over $(grep -c '^  FAIL  ' "$CV") expectations)"
       detail "$CONF/broke"; echo "$CV" >>"$LOGS/failed" ;;
    # Exit 2 is the runner refusing to call an unreached rule a pass, and its two
    # causes are not one defect. A contract page missing is this tree failing to
    # hand over a file it wrote itself, which is red. A capability the driver
    # could not declare is a toolchain absent from this machine — no python, so
    # no fixture, so no wire — and that is UNPROVEN, named, and never a pass.
    2) if grep -q 'contract pages this run needed and did not have' "$CV"; then
           fail "conformance — a contract page this tree authors reached the runner missing, and every rule citing it was judged against nothing"
           echo "$CV" >>"$LOGS/failed"
       else
           unproven "conformance — ${NUNREACH:-?} rules out of reach, judged not at all, over $OVER"
           { sed -n 's/^  fixture: */fixture /p' "$CV" | grep ABSENT
             sed -n 's/^  ----  //p' "$CV"; } > "$CONF/outofreach"
           detail "$CONF/outofreach"
       fi ;;
    *) fail "conformance — run.sh exited $cv: it could not be set up, which is this tree's fault and not an implementation's"
       echo "$CV" >>"$LOGS/failed" ;;
    esac
fi

section "c++"
# Two compilers, or the word UNPROVEN. This section used to vanish when cmake
# was absent, and it was absent from PATH on the machine the gate runs on: the
# C++ went red on macOS and Windows the first day anything built it there, a
# layer that had not compiled since morning went unnoticed for a day, and the
# harnesses below kept comparing a binary an earlier run had left in .build.
# A missing compiler is still not a failing test, but a third of every
# three-language claim resting on nothing is said out loud, and a binary this
# run did not build is handed to nobody.
CPP_BUILD="$LOGS/no-cpp-build"
NCPP=0
if [ "$FAST" = 1 ]; then
    skip "c++ msvc (--fast: minutes)"
elif [ -z "$CMAKE" ]; then
    unproven 'c++ msvc — no cmake on PATH and none under Visual Studio; set ABSTRACTION_CMAKE. The Windows half of the C++ compiled nowhere on this run.'
else
    # Configured out of tree so a failed build never leaves artefacts in the
    # repository that a later run would mistake for a good one.
    B="${ABSTRACTION_CPP_BUILD:-$ROOT/.build}"
    CTEST="$(dirname "$CMAKE")/ctest"
    NFAIL_BEFORE=${#FAILED[@]}
    run "cmake configure"   bash -c "'$CMAKE' -S '$ROOT' -B '$B' -DCMAKE_BUILD_TYPE=Release"
    run "cmake build"       bash -c "'$CMAKE' --build '$B' --config Release"
    run "ctest"             bash -c "'$CTEST' --test-dir '$B' -C Release --output-on-failure"
    [ "${#FAILED[@]}" = "$NFAIL_BEFORE" ] && { CPP_BUILD="$B"; NCPP=$((NCPP+1)); }
fi
# The second compiler, and the platform CI builds on. g++ in WSL is on this
# machine, needs no network and no laptop that might be off, and builds and
# tests the whole tree in under a minute, so it runs in every mode. The
# working tree goes over one pipe into a directory WSL owns, because /mnt/c
# is slow and a WSL /tmp does not outlive the VM's idle timeout.
if command -v wsl.exe >/dev/null 2>&1 \
   && wsl.exe -e sh -c 'command -v cmake >/dev/null && command -v g++ >/dev/null' >/dev/null 2>&1; then
    LX="$LOGS/cpp-linux.log"
    if git -C "$ROOT" ls-files -coz --exclude-standard | tar --null -T - -cf - \
       | wsl.exe -e sh -c 'd=$(mktemp -d) && tar -xf - -C "$d" && cd "$d" && cmake -S . -B build -DCMAKE_BUILD_TYPE=Release && cmake --build build -j"$(nproc)" && ctest --test-dir build --output-on-failure; rc=$?; rm -rf "$d"; exit $rc' \
       > "$LX" 2>&1; then
        pass "c++ linux — g++ $(wsl.exe -e g++ -dumpfullversion 2>/dev/null | tr -d '\r') in WSL built the working tree and passed $(sed -n 's/.*tests passed, 0 tests failed out of \([0-9]*\).*/\1/p' "$LX" | tail -1) tests"
        NCPP=$((NCPP+1))
    else
        fail "c++ linux — g++ in WSL"; echo "$LX" >>"$LOGS/failed"
    fi
else
    unproven 'c++ linux — no wsl.exe with cmake and g++ on it. The unix half of the C++ compiled nowhere on this run.'
fi
metric cpp_toolchains_built "$NCPP" check.sh

if [ "$FAST" = "1" ]; then
    section "skipped"
    skip "the cross-language harnesses (--fast)"
else
    section "cross-language"
    [ "$CPP_BUILD" != "$LOGS/no-cpp-build" ] \
      || unproven 'c++ in every harness below — no MSVC build this run, so no C++ binary is handed to them; one left in .build by an earlier run is not this tree'"'"'s'
    run "job conformance"   pinned "$ROOT/scripts/xlang-job.sh"
    # The C++ reader this run just built, rather than the one a developer once
    # left in /c/jobbuild: without this the third implementation is compiled by
    # the line above and then left out of the comparison it exists for.
    SPECREAD="$CPP_BUILD/Release/specread.exe"
    [ -f "$SPECREAD" ] || SPECREAD="$CPP_BUILD/specread"
    run "download spec"     pinned SPECREAD_CPP="$SPECREAD" "$ROOT/scripts/spec-conformance.sh"
    # Three implementations must spell one proven byte set the same way, or the
    # record churns against itself and no diff of a job's history means anything.
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
    MIX="$LOGS/cas-mixed.log"
    if env CAS_CPP="$TEST_CAS" "$PY" "$ROOT/cas/mixed.py" 3 > "$MIX" 2>&1; then
        pass "cas mixed languages"
    elif grep -q 'c++: ABSENT' "$MIX"; then
        unproven "cas mixed languages — $(grep -m1 'c++: ABSENT' "$MIX")"
    else
        fail "cas mixed languages"; echo "$MIX" >>"$LOGS/failed"
    fi

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
        skip "behaviour conformance — $(sed -n 's/^ *\([a-z+]*\): ABSENT — /\1 absent: /p' "$BEH" | paste -sd';' -)"
    elif ! grep -q ' invariants have a scenario' "$BEH"; then
        # Every other set below is guarded by its own recorded half being
        # non-empty. This one records nothing, so a harness that printed no
        # divergences because it died reads identically to one that agreed.
        fail "behaviour conformance — the harness printed no scenario count; an empty divergence set here means it never ran, not that the three agreed"
        echo "$BEH" >>"$LOGS/failed"
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
            detail "$LOGS/beh-new"; echo "$BEH" >>"$LOGS/failed"
        elif [ -s "$LOGS/beh-gone" ]; then
            fail "behaviour conformance ($(wc -l < "$LOGS/beh-gone") recorded and now agreeing — drop them from check.baseline)"
            detail "$LOGS/beh-gone"
        else
            pass "behaviour conformance — $(wc -l < "$LOGS/beh-now") known divergences, exactly the recorded set"
            detail "$LOGS/beh-now"
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
        skip 'verdict conformance — fewer than two readers here'
    else
        sed -n 's/^  FAIL  /divergence /p' "$VER" | sort -u > "$LOGS/ver-now"
        metric verdict_divergences "$(wc -l < "$LOGS/ver-now")" verdict-conformance.sh
        grep -a '^verdict	' "$BASE" | cut -f2- | sort -u > "$LOGS/ver-was"
        comm -23 "$LOGS/ver-now" "$LOGS/ver-was" > "$LOGS/ver-new"
        comm -13 "$LOGS/ver-now" "$LOGS/ver-was" > "$LOGS/ver-gone"
        if [ -s "$LOGS/ver-new" ]; then
            fail "verdict conformance ($(wc -l < "$LOGS/ver-new") unrecorded)"
            detail "$LOGS/ver-new"; echo "$VER" >>"$LOGS/failed"
        elif [ -s "$LOGS/ver-gone" ]; then
            fail "verdict conformance ($(wc -l < "$LOGS/ver-gone") recorded and now agreeing — drop them from check.baseline)"
            detail "$LOGS/ver-gone"
        else
            pass "verdict conformance — $(wc -l < "$LOGS/ver-now") known divergences, exactly the recorded set"
            detail "$LOGS/ver-now"
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
        skip "obtainable — $(sed -n 's/^    \([a-z+]*\): ABSENT — \(.*\)/\1 absent: \2/p' "$OBT" | paste -sd';' -); open: $(sed -n 's/^  doors: //p' "$OBT")"
    else
        pass "obtainable — $(sed -n 's/^  doors: //p' "$OBT") front doors, one line: $(sed -n 's/^  go: //p' "$OBT")"
    fi

    # idl/test/run.ps1 puts idl/test/corpus to five GENERATED readers and
    # reports that all five refuse every input with the same word at the same
    # byte. That is agreement by descent: the readers and the corpus come out
    # of one definition, so it proves the generator is consistent with itself.
    # Nothing put the corpus to job/go, job/python or job/cpp, which are the
    # three implementations somebody links, and the first run that did found 13,
    # 12 and 7 fixtures where a peer and the corpus disagree.
    #
    # One row per peer per TOOLCHAIN, because the C++ peer is two artefacts: the
    # same source built by MSVC reads twenty-nine stored records and built by
    # g++ refuses twelve of them, over a year-one timestamp libstdc++'s
    # system_clock cannot hold. Every C++ measurement this project published
    # before this rule was taken with one compiler and could not say which.
    PC="$LOGS/peers"; mkdir -p "$PC"
    pinned "$ROOT/scripts/peers/corpus.sh" --out "$PC" > "$PC/log" 2>&1; pcs=$?
    NPC=$(wc -l < "$PC/summary" 2>/dev/null || echo 0)
    metric peer_corpus_peers "$NPC" peers/corpus.sh
    metric peer_corpus_disagreements "$(awk -F'\t' '{s+=$5} END {print s+0}' "$PC/summary" 2>/dev/null)" peers/corpus.sh
    metric peer_corpus_roundtrip_differs "$(awk -F'\t' '{s+=$6} END {print s+0}' "$PC/summary" 2>/dev/null)" peers/corpus.sh
    metric peer_corpus_wire_refused "$(awk -F'\t' '{s+=$7} END {print s+0}' "$PC/summary" 2>/dev/null)" peers/corpus.sh
    NFIXTURES=$(ls "$ROOT/idl/test/corpus"/*.json 2>/dev/null | wc -l)
    if [ "$NPC" -lt 2 ] || [ "$NFIXTURES" -lt 50 ]; then
        nothing "peer corpus" "$NPC peer/toolchain over $NFIXTURES corpus" "2 peers and 50 fixtures"
        echo "$PC/log" >>"$LOGS/failed"
    else
        # One line per peer and toolchain, named. A peer whose row is missing
        # from the recorded set, or recorded and no longer happening, takes its
        # own line red rather than one summary line for four artefacts.
        while IFS=$'\t' read -r peer key chain n disagree rt wr wm; do
            bad=$( (grep -c "^$peer/$key	" "$PC/new" 2>/dev/null) || true )
            stale=$( (grep -c "^$peer/$key	" "$PC/gone" 2>/dev/null) || true )
            label="peer corpus $peer ($chain) — $n fixtures, $disagree recorded disagreements, $wr records refused, $wm moved"
            if [ "${bad:-0}" != 0 ] || [ "${stale:-0}" != 0 ]; then
                fail "$label — ${bad:-0} unrecorded, ${stale:-0} recorded and gone"
                grep -h "^$peer/$key	" "$PC/new" "$PC/gone" 2>/dev/null > "$PC/moved.rows"
                detail "$PC/moved.rows"
            elif [ "$rt" != 0 ]; then
                fail "$label — $rt of the accept.roundtrip-* fixtures did not come back byte for byte"
                grep "^$peer/$key	.*	roundtrip$" "$PC/$peer.$key.rows" > "$PC/roundtrip.rows"
                detail "$PC/roundtrip.rows"
            else
                pass "$label"
            fi
        done < "$PC/summary"
        while IFS=$'\t' read -r who why; do
            unproven "peer corpus $who — $why"
        done < "$PC/unproven"
        # corpus.sh also compares the SET of records every peer re-wrote. Its
        # exit code carries that and the vacuity guard; neither has a row above.
        [ "$pcs" = 0 ] || { fail "peer corpus — corpus.sh exited $pcs"; echo "$PC/log" >>"$LOGS/failed"; }
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
        grep -E 'FAIL|LEAK|every row' "$LOGS/split.log" > "$LOGS/split-bad"
        detail "$LOGS/split-bad"
    fi
else
    skip 'published tree (needs the network — pass --public)'
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
#
# GOWORK=off is half of what the stranger lacks. The other half is a cold
# proxy: this loop inherited the ambient module cache and proxy.golang.org,
# and the proxy serves a deleted version forever, so it stayed green through
# eight tags whose go.mod named versions no remote had. Our own modules are
# fetched from the repository itself, into a module cache created empty for
# this run; everything else may still come from the proxy, which is what the
# stranger's go does too.
if [ "$PUBLIC" = "1" ]; then
    S="$LOGS/standalone"
    rm -rf "$S"
    for g in "$ROOT"/.split/*/; do
        r="$(basename "$g")"
        mkdir -p "$S/$r"
        [ -d "$ROOT/.split/.org-files/$r" ] && cp -R "$ROOT/.split/.org-files/$r/." "$S/$r/"
        cp -R "$g." "$S/$r/"
    done
    mapfile -t SMODS < <(find "$S" -name go.mod | sort)
    [ "${#SMODS[@]}" -gt 0 ] || nothing "standalone build" 0 1
    COLD="$LOGS/modcache"
    COLD_W="$(cygpath -w "$COLD" 2>/dev/null || echo "$COLD")"
    for m in "${SMODS[@]}"; do
        d="$(dirname "$m")"
        run "standalone cold ${d#$S/}" env GOWORK=off GOFLAGS=-mod=readonly GONOPROXY="github.com/${ABSTRACTION_ORG:-openabstractions}" \
            GOMODCACHE="$COLD_W" GIT_TERMINAL_PROMPT=0 bash -c "cd '$d' && go build ./..."
    done
else
    skip 'standalone build of each published module (pass --public)'
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
            detail "$WC/new"
        elif [ -s "$WC/gone" ]; then
            fail "published reader ($(wc -l < "$WC/gone") recorded lines v0.1.0 no longer prints — drop them from check.baseline)"
            detail "$WC/gone"
        else
            pass "published reader — $(wc -l < "$WC/now") lines from go/v0.1.0 over the corpus and $NLIVE scenarios this tree ran, exactly the recorded set"
        fi
    else
        fail "published reader — research/wire-compat will not build against the published tag"
        echo "$WC/build.log" >>"$LOGS/failed"
    fi
else
    skip 'what go/v0.1.0 makes of what this tree writes (needs the module proxy — pass --public)'
fi

# What a stranger receives, opened. Five wheels built with a six-line METADATA
# and no licence text while every pyproject.toml declared one; a manifest was
# read by three instruments and the archive by none. scripts/package.sh builds
# the wheel and the sdist from .split, fetches the module zip the proxy serves
# and installs the C++ into scratch, and opens each. It runs after the split
# above because that is where its Python and C++ inputs come from, and it says
# ABSENT for a package nothing has published rather than passing it.
#
# scripts/package.sh and scripts/size.sh keep their own rows in
# research/gate/series.tsv and regenerate their own tables under research/;
# the series block at the end of this file writes only its own keys.
said() {  # said <log> <verdict>... — the lines a scripts/*.sh instrument printed under those verdicts
    local log="$1"; shift
    sed 's/\x1b\[[0-9;]*m//g' "$log" | grep -E "^  ($(IFS='|'; echo "$*")) " | sed 's/^  //; s/  */ /g' \
      | grep -vE '^[A-Z]+ [0-9]+ (artifact|package)\(s\) '
}
if [ "$PUBLIC" = "1" ]; then
    PK="$LOGS/package.log"
    ABSTRACTION_CMAKE="$CMAKE" sh "$ROOT/scripts/package.sh" python go $([ "$FAST" = 1 ] || echo cpp) > "$PK" 2>&1; pk=$?
    PKSUM="$(sed -n 's/^  \([0-9]* artifact(s) opened: .*\)/\1/p' "$PK" | tail -1)"
    case "$pk" in
    0) pass "packages — $PKSUM$([ "$FAST" = 1 ] && echo '; the C++ installs not opened (--fast)')"
       said "$PK" ABSENT > "$LOGS/package-absent"; detail "$LOGS/package-absent" ;;
    1) fail "packages ($(said "$PK" FAIL | wc -l) built artifacts do not carry what their manifest promises)"
       said "$PK" FAIL > "$LOGS/package-bad"; detail "$LOGS/package-bad"; echo "$PK" >>"$LOGS/failed" ;;
    2) unproven "packages — $PKSUM"
       said "$PK" UNPROVEN ABSENT > "$LOGS/package-unproven"; detail "$LOGS/package-unproven" ;;
    *) fail "packages — scripts/package.sh exited $pk before its verdict"; echo "$PK" >>"$LOGS/failed" ;;
    esac
else
    skip 'packages — what a stranger receives, opened (builds from .split and reads the module proxy — pass --public)'
fi

# The size of what we publish, as a series. Somebody else's 128 KB root
# certificate bundle reached a published module and was found by a person
# reading a dependency list; a byte count per package, compared with the last
# one recorded, is the number that moves on that day. scripts/size.sh reads
# the proxy's zip for the newest tag of every module and refuses a growth past
# its threshold that research/size151/explained.tsv does not account for.
if [ "$PUBLIC" = "1" ]; then
    SZ="$LOGS/size.log"
    sh "$ROOT/scripts/size.sh" > "$SZ" 2>&1; sz=$?
    SZSUM="$(sed -n 's/^  \([0-9]* package(s) measured: .*\)/\1/p' "$SZ" | tail -1)"
    case "$sz" in
    0) pass "sizes — $SZSUM"
       said "$SZ" ABSENT > "$LOGS/size-absent"; detail "$LOGS/size-absent" ;;
    1) fail "sizes ($(said "$SZ" FAIL | wc -l) packages grew past the threshold with no explanation recorded)"
       said "$SZ" FAIL > "$LOGS/size-bad"; detail "$LOGS/size-bad"; echo "$SZ" >>"$LOGS/failed" ;;
    2) unproven "sizes — $SZSUM"
       said "$SZ" UNPROVEN ABSENT > "$LOGS/size-unproven"; detail "$LOGS/size-unproven" ;;
    *) fail "sizes — scripts/size.sh exited $sz before its verdict"; echo "$SZ" >>"$LOGS/failed" ;;
    esac
else
    skip 'sizes — the bytes of every published package against its last recorded size (reads the module proxy — pass --public)'
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
# A thing the gate runs can be a directory — scripts/obtain and scripts/unchecked
# are — and git tracks the files inside it, never the directory itself, so every
# ancestor of a tracked file counts as present.
{ git ls-files scripts
  git ls-files scripts | awk -F/ '{p=$1; for (i=2; i<NF; i++) {p=p "/" $i; print p}}'
} | sort -u > "$RULES/gate-tracked"
comm -23 "$RULES/gate-scripts" "$RULES/gate-tracked" > "$RULES/gate-missing"
note "$(wc -l < "$RULES/gate-scripts") files under scripts/ are named by check.sh or by something it sources, found by grepping each of them for \$ROOT/scripts/*, and checked against git ls-files scripts"
if [ -s "$RULES/gate-missing" ]; then
    fail "gate scripts ($(wc -l < "$RULES/gate-missing") the gate runs and git does not track — a fresh clone has a broken gate)"
    detail "$RULES/gate-missing"
else
    pass "gate scripts — every one is in the repository"
fi

# ---- a refusal returned across a package boundary is read ---------------
# go vet cannot see a discarded error, and neither could anything else here, so
# the break this project makes most often was the one nothing could fail. Both
# defects of 2026-09-07 are this shape: r.Store.Release(id, epoch) as a bare
# statement, and the unread half of a partial answer assigned to _.
#
# Recorded per callee and not per call site, because eighteen leaked leases are
# one root cause and a rule that prints eighteen lines gets switched off. The
# count is in the record, so a nineteenth still goes red, and a count that falls
# goes red too: dropping the line is how the debt is recorded as paid.
UC="$LOGS/unchecked"; mkdir -p "$UC"
if (cd "$ROOT/scripts/unchecked" && GOWORK=off go build -o "$UC/unchecked.exe" .) >"$UC/build.log" 2>&1; then
    mapfile -t UCMODS < <(modules "$ROOT")
    if "$UC/unchecked.exe" "${UCMODS[@]}" > "$UC/sites" 2>"$UC/scope"; then
        awk -F'\t' '{n[$1]++} END {for (c in n) printf "%s\t%d\n", c, n[c]}' "$UC/sites" | sort > "$UC/now"
        baseline unchecked > "$UC/was"
        # A count in the record means every change shows up in both directions of
        # a set diff, so the two are joined on the callee instead: growing is a
        # breach, and falling is a record that has stopped being true.
        awk -F'\t' 'NR==FNR {was[$1]=$2; next} {now[$1]=$2}
            END {
                for (c in now) if (!(c in was)) printf "grew\t%s\t0 -> %d\n", c, now[c]
                       else if (now[c] > was[c]) printf "grew\t%s\t%d -> %d\n", c, was[c], now[c]
                       else if (now[c] < was[c]) printf "fell\t%s\t%d -> %d\n", c, was[c], now[c]
                for (c in was) if (!(c in now)) printf "fell\t%s\t%d -> 0\n", c, was[c]
            }' "$UC/was" "$UC/now" | sort > "$UC/moved"
        grep -a '^grew	' "$UC/moved" | cut -f2- > "$UC/grew"
        grep -a '^fell	' "$UC/moved" | cut -f2- > "$UC/fell"
        metric discarded_errors "$(wc -l < "$UC/sites")" scripts/unchecked
        note "$(wc -l < "$UC/sites") calls throw away a returned error across $(tail -1 "$UC/scope"), from $(wc -l < "$UC/now") callees, $(wc -l < "$UC/was") recorded"
        if [ -s "$UC/grew" ]; then
            fail "discarded errors ($(wc -l < "$UC/grew") callees have a refusal thrown away that check.baseline does not record)"
            while IFS=$'\t' read -r c n; do
                printf '%s\t%s\n' "$c" "$n"
                awk -F'\t' -v c="$c" '$1==c {print "    " $2}' "$UC/sites"
            done < "$UC/grew" > "$UC/grew-sites"
            detail "$UC/grew-sites"
        elif [ -s "$UC/fell" ]; then
            fail "discarded errors ($(wc -l < "$UC/fell") recorded counts have stopped being true — write the new ones into check.baseline, that is the debt being paid)"
            detail "$UC/fell"
        else
            pass "discarded errors — every one is recorded and none has grown"
        fi
    else
        fail "discarded errors — scripts/unchecked examined nothing, so every count below would be zero"
        echo "$UC/scope" >>"$LOGS/failed"
    fi
else
    fail "discarded errors — scripts/unchecked will not build"
    echo "$UC/build.log" >>"$LOGS/failed"
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
    detail "$RULES/gone-new"
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
if [ "$NCITE" = 0 ]; then
    nothing "citations" 0 1
elif [ -s "$RULES/org-gone" ]; then
    fail "citations ($(wc -l < "$RULES/org-gone") recorded as published and not there)"
    detail "$RULES/org-gone"
elif [ -s "$RULES/new-broken" ]; then
    fail "citations ($(wc -l < "$RULES/new-broken") new)"
    detail "$RULES/new-broken"
elif [ -s "$RULES/fixed-broken" ]; then
    fail "citations ($(wc -l < "$RULES/fixed-broken") recorded breaks now resolve — drop them from check.baseline)"
    detail "$RULES/fixed-broken"
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
        detail "$RULES/copyleft-hits"
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
    detail "$RULES/vendored"
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
    sort -u -o "$RULES/fetched" "$RULES/fetched"
    note "cloned at configure time, into headers we publish: $(wc -l < "$RULES/fetched")"
    detail "$RULES/fetched"
fi
if [ "$NMOD" = 0 ]; then
    nothing "dependencies" 0 1
elif [ -s "$RULES/new-deps" ]; then
    fail "dependencies ($(wc -l < "$RULES/new-deps") new in a published tree)"
    detail "$RULES/new-deps"
elif [ -s "$RULES/gone-deps" ]; then
    fail "dependencies ($(wc -l < "$RULES/gone-deps") recorded and no longer there — drop them from check.baseline)"
    detail "$RULES/gone-deps"
else
    pass "dependencies — every published module resolves to modules we wrote"
fi

# ---- every version a go.mod or a public page names is a tag the remote has
# Twelve tags were cut in one command and eight asserted a dependency graph
# that did not exist, and a tag is permanent: the proxy serves the version
# forever and a republished number is a checksum mismatch. So the versions a
# go.mod requires of our own modules are compared with `git ls-remote --tags`
# on the repository — never the proxy, which is what hid it — and so is every
# version a site/ page types beside a repository. A require with a replace
# beside it resolves to a directory and is left out; a pseudo-version names a
# commit, which no tag answers for, and is counted rather than checked. A
# published file fails; a measured one prints, because research/wire-compat
# pinned to a tag the remote no longer has is a measurement held up by a cache.
if [ "$PUBLIC" = "1" ]; then
    PIN="$LOGS/pins"; mkdir -p "$PIN"
    PINORG="${ABSTRACTION_ORG:-openabstractions}"
    : > "$PIN/named"
    for mod in $(git ls-files 'go.mod' '*/go.mod'); do
        awk -v f="$mod" -v org="github.com/$PINORG/" '
            /^replace \(/ { inrep = 1; next }   inrep && /^\)/ { inrep = 0; next }
            /^require \(/ { inreq = 1; next }   inreq && /^\)/ { inreq = 0; next }
            (inrep || /^replace /) && /=>/ { p = $1; if (p == "replace") p = $2; rep[p] = 1; next }
            (inreq || /^require /) && index($0, org) { p = $1; v = $2; if (p == "require") { p = $2; v = $3 } req[p] = v }
            END { for (p in req) if (!(p in rep)) {
                      repo = substr(p, length(org) + 1); sub("/.*", "", repo)
                      dir = substr(p, length(org) + length(repo) + 2)
                      tag = (dir == "" ? "" : dir "/") req[p]
                      kind = (req[p] ~ /[0-9]{14}-[0-9a-f]{12}$/) ? "commit" : "tag"
                      printf "%s\t%s\t%s\t%s\t%s\n", f, repo, tag, kind, p "@" req[p] } }' "$ROOT/$mod" >> "$PIN/named"
    done
    xargs -r -d '\n' grep -HoE "$PINORG/[a-z.-]+[^ <\"]*@v[0-9]+\.[0-9]+\.[0-9]+|$PINORG/[a-z.-]+/releases/tag/[^\"]+" < "$RULES/state-pages" 2>/dev/null \
      | awk -v org="$PINORG" '{
            f = $0; sub(":.*", "", f); p = substr($0, length(f) + 2); said = p
            sub("^" org "/", "", p); repo = p; sub("/.*", "", repo); rest = substr(p, length(repo) + 1)
            if (rest ~ /^\/releases\/tag\//) { tag = rest; sub("^/releases/tag/", "", tag); gsub("%2F", "/", tag) }
            else { ver = rest; sub(".*@", "", ver); dir = rest; sub("@.*", "", dir); sub("^/", "", dir); sub("/cmd/.*", "", dir)
                   tag = (dir == "" ? "" : dir "/") ver }
            printf "%s\t%s\t%s\ttag\t%s\n", f, repo, tag, said }' >> "$PIN/named"
    grep -vxFf "$GENS/expected" "$RULES/state-pages" \
      | xargs -r -d '\n' grep -nE 'v[0-9]+\.[0-9]+\.[0-9]+' 2>/dev/null | grep -vE "$PINORG/[a-z.-]+" | cut -c1-160 > "$PIN/loose"
    cut -f2 "$PIN/named" | sort -u > "$PIN/repos"
    : > "$PIN/tags"; : > "$PIN/unreached"
    while read -r r; do
        if GIT_TERMINAL_PROMPT=0 git ls-remote --tags "git@github.com:$PINORG/$r" > "$PIN/ls" 2>"$PIN/ls.err"; then
            awk -v r="$r" '{ sub("refs/tags/", "", $2); if ($2 !~ /\^\{\}$/) print r "\t" $2 }' "$PIN/ls" >> "$PIN/tags"
        else
            printf '%s\t%s\n' "$r" "$(head -1 "$PIN/ls.err")" >> "$PIN/unreached"
        fi
    done < "$PIN/repos"
    awk -F'\t' '
        FILENAME == ARGV[1] { have[$1 "\t" $2] = 1; next }
        FILENAME == ARGV[2] { gone[$1] = 1; next }
        FILENAME == ARGV[3] { ship[$0] = 1; next }
        { named++
          if ($4 == "commit") { commits++; next }
          if ($2 in gone) { unreached++; next }
          if (($1 == "") || (($2 "\t" $3) in have)) next
          published = 0; for (s in ship) if ($1 == s || index($1, s "/") == 1) published = 1
          print (published ? "dead" : "measured") "\t" $1 "\t" $5 " — not a tag on " $2 }
        END { printf "%d %d %d\n", named, commits, unreached }
    ' "$PIN/tags" "$PIN/unreached" "$RULES/shipped" "$PIN/named" > "$PIN/verdicts"
    read -r NPIN NPINCOMMIT NPINUNREACH < <(tail -1 "$PIN/verdicts")
    grep -a '^dead	' "$PIN/verdicts" | cut -f2- | sort > "$PIN/dead"
    grep -a '^measured	' "$PIN/verdicts" | cut -f2- | sort > "$PIN/measured"
    metric pins_named "$NPIN" check.sh
    metric pins_dead_measured "$(wc -l < "$PIN/measured")" check.sh
    note "$NPIN versions named in $(cut -f1 "$PIN/named" | sort -u | wc -l) files, checked against the tags of $(cut -f1 "$PIN/tags" | sort -u | wc -l) repositories over ssh; $NPINCOMMIT pinned to a commit and not checked, $(wc -l < "$PIN/measured") in measured code point at versions the remote no longer has"
    if [ -s "$PIN/unreached" ]; then
        unproven "pinned versions — $(wc -l < "$PIN/unreached") repositories could not be listed, and $NPINUNREACH versions naming them were judged not at all"
        detail "$PIN/unreached"
    fi
    if [ "$NPIN" = 0 ]; then
        nothing "pinned versions" 0 1
    elif [ -s "$PIN/loose" ]; then
        fail "pinned versions ($(wc -l < "$PIN/loose") versions typed on a public page with no repository on the line, so nothing can check them)"
        detail "$PIN/loose"
    elif [ -s "$PIN/dead" ]; then
        fail "pinned versions ($(wc -l < "$PIN/dead") named in published files and not a tag on their repository — a stranger's go get resolves them from a cache, or not at all)"
        detail "$PIN/dead"
    else
        pass "pinned versions — every version a published go.mod or page names is a tag on its repository$([ -s "$PIN/measured" ] && printf '; %s in measured code point at versions the remote no longer has' "$(wc -l < "$PIN/measured")")"
        detail "$PIN/measured"
    fi
else
    skip 'pinned versions are tags their repositories still have (git ls-remote — pass --public)'
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
NHDR=$(wc -l < "$RULES/headers")
if [ "$NHDR" = 0 ]; then
    nothing "public headers" 0 1
elif [ -s "$RULES/header-deps" ]; then
    fail "public headers ($(wc -l < "$RULES/header-deps") reach a third-party header, and so does every adopter that includes them)"
    detail "$RULES/header-deps"
else
    pass "public headers — $NHDR published headers, none includes anything but our own"
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
if [ ! -s "$RULES/exports" ]; then
    nothing "concepts" 0 1
elif grep -q '^FAIL' "$RULES/concepts"; then
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
if [ ! -s "$RULES/exports-all" ]; then
    nothing "ancestry" 0 1
elif grep -q '^FAIL' "$RULES/ancestry"; then
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
if [ ! -s "$RULES/store-scan" ]; then
    nothing "stores" 0 1
elif [ -s "$RULES/stores-new" ]; then
    fail "stores ($(wc -l < "$RULES/stores-new") hand-rolled where cas.Change is one import away)"
    detail "$RULES/stores-new"
elif [ -s "$RULES/stores-gone" ]; then
    fail "stores ($(wc -l < "$RULES/stores-gone") recorded and now gone — drop them from check.baseline)"
    detail "$RULES/stores-gone"
else
    pass "stores — none but the recorded set"
    sed 's/^/still hand-rolled: /' "$RULES/stores" > "$RULES/stores-said"
    detail "$RULES/stores-said"
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
# Recorded one way round, so unlike every other set here an empty scan is
# indistinguishable from a clean one and passes.
if [ "$NPUB" = 0 ]; then
    nothing "leaked identifiers" 0 1
elif [ -s "$RULES/new-leaks" ]; then
    fail "leaked identifiers ($(wc -l < "$RULES/new-leaks") new files)"
    while read -r f; do grep "^$f:" "$RULES/leaks" | head -3; done < "$RULES/new-leaks" > "$RULES/new-leak-lines"
    detail "$RULES/new-leak-lines"
else
    pass "leaked identifiers — none outside the recorded set"
fi

# ---- a public state line names a machine by class, never by name ---------
# A CPU model, a core count, a battery percentage and "the owner using it
# throughout" sat on site/evidence.html for days. The rule above had zero hits:
# it lists identifiers, and a hardware fingerprint is a description that
# happens to be unique. So a state paragraph on a public page has a grammar —
# the class of machine, an OS, a power source, what else loaded it, dates and
# the versions of the tools under test — and a model, a count, a percentage
# or a person fails it. Those belong in the research/ file that produced the
# number, which is where somebody reproduces it. A power plan's name stays
# allowed: the rule in CLAUDE.md asks for it, and "Balanced" names nobody.
grep -E '^site/.*\.html$' "$RULES/files" > "$RULES/state-pages"
xargs -r -d '\n' awk '
    /<p class="state"/ { on = 1; at = FNR; s = "" }
    on { s = s " " $0 }
    on && /<\/p>/ { on = 0; gsub(/[ \t]+/, " ", s); gsub(/<[^>]*>/, "", s); print FILENAME ":" at "\t" s }
' < "$RULES/state-pages" > "$RULES/state-lines" 2>/dev/null
STATERE='%|\b(Intel|AMD|Ryzen|Xeon|Core i[3579]|Apple M[0-9]|Snapdragon|EPYC|Threadripper)\b'
STATERE="$STATERE"'|\b[0-9]+ (logical |physical )?(cores?|CPUs?|threads|processors)\b'
STATERE="$STATERE"'|\b[0-9]+ ?(GB|GiB|TB)( of)? (RAM|memory)\b'
STATERE="$STATERE"'|\b(owner|agent)s?\b'
grep -E "$STATERE" "$RULES/state-lines" | cut -c1-200 > "$RULES/state-bad"
NSTATE=$(wc -l < "$RULES/state-lines")
note "$NSTATE state paragraphs on $(wc -l < "$RULES/state-pages") public pages, read against the grammar: class, OS, power, load, dates, tool versions"
if [ "$NSTATE" = 0 ]; then
    nothing "state lines" 0 1
elif [ -s "$RULES/state-bad" ]; then
    fail "state lines ($(wc -l < "$RULES/state-bad") name a machine — a model, a count, a percentage or a person — on a public page)"
    detail "$RULES/state-bad"
else
    pass "state lines — $NSTATE public state paragraphs name a machine by class only"
fi

# ---- the generated code reads as the language it is generated into -----
# research/language-score scores our VOCABULARY per language. Nothing scored
# the code the generator actually emits, so whether the Rust reads as Rust was
# unmeasured. idl/fit/fit.sh runs each language's own tool — gofmt, go vet,
# rustfmt, rustc, compileall, node --check — and a rubric for what a tool cannot
# see. Vetoes only: a fall is red, a rise is a note.
if [ "$FAST" = 1 ]; then
    skip "generated-code fit — needs the generator and four toolchains, half a minute"
elif ! sh "$ROOT/idl/fit/fit.sh" "$LOGS/fit" > "$RULES/fit" 2>&1; then
    fail "generated-code fit (idl/fit/fit.sh did not finish)"; echo "$RULES/fit" >>"$LOGS/failed"
else
    grep '^fit_points_' "$RULES/fit" | sed 's/^fit_points_//' > "$RULES/fit-now"
    baseline fit | sort > "$RULES/fit-was"
    awk -F'\t' 'NR==FNR {was[$1]=$2; next} {now[$1]=$2}
        END { for (l in was) if (now[l] < was[l]) printf "FELL\t%s\t%d\t%d\n", l, was[l], now[l]
              else if (now[l] > was[l]) printf "ROSE\t%s\t%d\t%d\n", l, was[l], now[l] }' \
        "$RULES/fit-was" "$RULES/fit-now" | sort > "$RULES/fit-moved"
    while IFS="	" read -r l p; do metric "fit_points_$l" "$p" idl/fit/fit.sh; SERIES_KEYS="$SERIES_KEYS fit_points_$l"; done < "$RULES/fit-now"
    note "$(wc -l < "$RULES/fit-now") backends scored against their own language's tools; idl/fit/fit.sh prints the table and every reason"
    if [ ! -s "$RULES/fit-was" ]; then
        nothing "generated-code fit" 0 1
    elif grep -q '^FELL' "$RULES/fit-moved"; then
        fail "generated-code fit (backends emitting code their own language likes less than scripts/check.baseline records: $(grep -c '^FELL' "$RULES/fit-moved"))"
        awk -F'\t' '$1=="FELL" {printf "%s fell %d -> %d — name what it hurt and why before you lower the floor\n", $2, $3, $4}' "$RULES/fit-moved" > "$RULES/fit-fell"
        detail "$RULES/fit-fell"
    else
        pass "generated-code fit — no backend regressed$(grep -q '^ROSE' "$RULES/fit-moved" && printf '; %s rose, which makes the change eligible and never approved — raise the line in scripts/check.baseline once somebody has said what moved' "$(grep -c '^ROSE' "$RULES/fit-moved")")"
        awk -F'\t' '$1=="ROSE" {printf "%s rose %d -> %d\n", $2, $3, $4}' "$RULES/fit-moved" > "$RULES/fit-rose"
        detail "$RULES/fit-rose"
    fi
fi

# ---- a decision's status is written on it, not on the heading above it --
# scripts/vision.sh show read an entry's status from the section it sat in, so
# every entry filed under "Struck out" without a strike marker was reported
# frozen. The marker decides now; nothing yet stopped the filing error that made
# the two disagree, and it went unseen for months because it is invisible unless
# you diff a heading against an entry. Names each one, because the fix is per
# entry: strike it, or move it back out with scripts/vision.sh move.
VISION="${VISION_FILE:-$ROOT/VISION.md}"
awk -v f="$(basename "$VISION")" '
    /^## / { h = substr($0, 4); sub(/ [-\xe2\x80\x94] .*/, "", h); sub(/,.*/, "", h) }
    h != "Struck out" { next }
    /^- ~~/   { print "marked" }
    /^- \*\*/ { printf "unmarked\t%s:%d %.96s\n", f, NR, substr($0, 3) }
' "$VISION" > "$RULES/struck-section"
grep -a '^unmarked	' "$RULES/struck-section" | cut -f2- > "$RULES/struck-unmarked"
NSS=$(wc -l < "$RULES/struck-section")
metric struck_section_unmarked "$(wc -l < "$RULES/struck-unmarked")" scripts/check.sh
note "$NSS entries sit under $(basename "$VISION") § Struck out, $(grep -ac '^marked$' "$RULES/struck-section") of them carrying a ~~ strike marker"
if [ "$NSS" = 0 ]; then
    nothing "struck-out filing" 0 1
elif [ -s "$RULES/struck-unmarked" ]; then
    fail "struck-out filing ($(wc -l < "$RULES/struck-unmarked") filed as struck and carrying no strike marker — each one is a live decision that scripts/vision.sh once reported frozen)"
    detail "$RULES/struck-unmarked"
else
    pass "struck-out filing — every entry under Struck out carries its strike marker"
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
#
# The gate ran without this file for as long as it was untracked, because a
# source that does not run returns non-zero into nothing. Tracking it fixed the
# missing-file case; a parse error still empties the section, so the file says
# when it reached its last line and this refuses to believe it otherwise.
DOCUMENTS_RAN=0
eval "$(<"$ROOT/scripts/documents.sh")"
[ "$DOCUMENTS_RAN" = 1 ] || fail "documents — scripts/documents.sh stopped before its last rule; the rules after that point did not run at all"

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
# The one number that says whether this gate still has the teeth it had last
# week. Nothing else here notices a rule that stopped running.
metric "gate_rules_$MODE" "$NRAN" check.sh
metric "gate_skipped_$MODE" "$((NSKIPPED + NUNPROVEN))" check.sh
SERIES_KEYS="$SERIES_KEYS gate_rules_$MODE gate_skipped_$MODE"
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

# Before the result and not after it, so the last screen of an eight-hundred
# line run is the summary rather than somebody else's stack trace.
if [ -f "$LOGS/failed" ]; then
    printf '\n\033[1moutput of what failed\033[0m\n'
    while read -r log; do
        printf '\033[1m--- %s ---\033[0m\n' "$(basename "$log")"
        tail -25 "$log"
        echo
    done < "$LOGS/failed"
fi

section "result"
secs $((${EPOCHREALTIME/./} - T0))
note "sections:$SECTIONS — $SECS in all"
note "slowest rules: $(printf '%s' "$SLOW" | sort -rn | head -5 | awk -F'\t' '{printf "%s %d.%ds; ", $2, $1/1000000, $1/100000%10}')"

# The one rule about the gate itself: it still has the teeth it had last time.
# Recorded per mode as: rules <mode> <ran> <skipped>.
#
# A fall with no skip line in its place is a rule that stopped running and said
# nothing — an empty module list took eighty rules out of one run this way and
# the output only got shorter. A fall matched by a rise in skips is a tool or a
# platform that was not here, which already printed its own yellow line, and
# turning that red is how a gate gets switched off. A rise is somebody adding a
# rule, so it is a note: adding one must not require going red first.
read -r RAN_WAS SKIP_WAS <<<"$(baseline rules | awk -v m="$MODE" '$1==m {print $2, $3}')"
NOUT=$((NSKIPPED + NUNPROVEN))
if [ -z "$RAN_WAS" ]; then
    note "scripts/check.baseline records no rule count for a $MODE run — add the line: rules	$MODE	$NRAN	$NOUT"
elif [ "$NRAN" -lt "$RAN_WAS" ] && [ "$NOUT" -le "$SKIP_WAS" ]; then
    fail "gate ($((RAN_WAS - NRAN)) rules that ran on the recorded $MODE run did not run on this one, and nothing was skipped in their place)"
elif [ "$NRAN" -lt "$RAN_WAS" ]; then
    note "$((RAN_WAS - NRAN)) fewer rules than the recorded $MODE run, behind $((NOUT - SKIP_WAS)) more skipped — the yellow lines above say which"
elif [ "$NRAN" -gt "$RAN_WAS" ]; then
    note "$((NRAN - RAN_WAS)) more rules than scripts/check.baseline records for a $MODE run — write $NRAN	$NOUT into its rules	$MODE line"
fi
FINISHED=1
printf '  \033[1m%d rules ran, %d skipped, %d UNPROVEN, %d failed\033[0m\n' \
    "$NRAN" "$NSKIPPED" "$NUNPROVEN" "${#FAILED[@]}"
if [ ${#FAILED[@]} -eq 0 ]; then
    # "everything agrees" beside a non-zero UNPROVEN count is the sentence this
    # whole convention exists to refuse: a platform nobody reached did not
    # agree, it was never asked. The count stays out of the exit code — a Mac
    # that is not infrastructure cannot make every run red — and out of the
    # claim.
    if [ "$NUNPROVEN" -gt 0 ]; then
        printf '  \033[32mPASS\033[0m — everything that ran agrees; \033[33m%d UNPROVEN\033[0m above was never asked\n\n' "$NUNPROVEN"
    else
        printf '  \033[32mPASS\033[0m — everything agrees\n\n'
    fi
    exit 0
fi
printf '    \033[31mFAIL\033[0m  %s\n' "${FAILED[@]}"
echo
exit 1
