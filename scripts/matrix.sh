#!/usr/bin/env bash
# One grid: layer down the side, language across the top, a verdict in each cell.
#
#   scripts/matrix.sh           write docs/results/MATRIX.txt and site/coverage.html
#   scripts/matrix.sh --check   fail if either differs from what today's evidence produces
#   scripts/matrix.sh --cells   print the <layer> <language> <implementation> triples
#                               this walk finds, and nothing else
#
# --cells exists so scripts/check.sh can record a verdict against the same cell
# list this grid reads, instead of carrying a second copy of the rule for what
# counts as a cell. Two copies of that rule is how identity/ — a Go module at
# its layer root, with no go/ subdirectory — would go missing from one of them.
#
# Four things already answered a piece of this and none of them was a grid.
# MAP.md's languages column is typed by hand and tests nothing. Six harnesses
# under scripts/ each answer one question across every language at once.
# conformance/run.sh judges one driver per run and aggregates nothing.
# research/gate/series.tsv carries one number for the whole suite. So an empty
# cell and an untested cell looked identical, and cells were shipped blind.
#
# This aggregates; it is not a seventh harness and it runs nothing long. It
# reads the tree for which cells exist, and the recorded transcripts under
# docs/results/ for which cells were proven.
#
# WHY A CELL IS GREEN. A verdict is PASS only when a recorded transcript says so
# about the tree as it is now. For most of this grid that transcript is
# docs/results/GATE.txt, which scripts/check.sh --record writes: one line per
# cell, the verdict the gate reached, and a `# measures` header naming each
# layer's tree. Until that file exists a cell the gate builds and tests on every
# run still reads UNPROVEN with the reason `gate`, because a run nobody wrote
# down cannot be read back out of the repository, and calling it PASS would be
# assuming the gate ran and was green — the same defect as passing a platform
# that was absent. The reason column is where the distinctions live: `gate` is a
# cell whose result nobody has recorded, `skipped` is a cell the last recorded
# run could not judge, `unreached` is a cell nothing runs at all.
#
# NO STATE LINE, deliberately, against the rule that every measurement carries
# one. This counts files and reads transcripts written elsewhere; power source
# and plan cannot change the answer, and a battery percentage in the output
# would make --check red every hour on evidence that had not moved. The
# transcripts this reads carry their own state lines and it cites them.

set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LANGS="go python cpp"
OUT_TXT="docs/results/MATRIX.txt"
OUT_HTML="site/coverage.html"
GATE_TXT="docs/results/GATE.txt"

git() { command git -C "$ROOT" "$@"; }

# This reads the whole working tree, so it belongs to the tree that holds the
# layers. Every file it reads is optional and a missing one costs a distinction
# rather than the run: no go.work and the Go cells cannot say the gate reaches
# them, no inventory and no cell can be ABSENT. What it will not do is produce a
# grid over nothing — see the refusal below.
slurp() { [ -f "$1" ] && cat "$1" || true; }

# ---- which cells exist -------------------------------------------------
#
# A layer is a top-level directory holding an implementation. Two shapes, and
# the second is the one that breaks a naive walker: identity/ is a Go module at
# the layer root with no go/ subdirectory at all, because it was cloned in from
# the organisation where it was its own repository and nothing has moved it. A
# walker looking only for <layer>/<language>/ misses it silently, which is the
# failure this instrument exists to stop, so the shape is named rather than
# special-cased: a go.mod beside a CONTRACT.md is a layer's Go implementation
# at its root. That also keeps monitor/ out — a go.mod at its root and no
# contract, because it is a front end over another layer and not a layer.
cells() {
    local d
    for d in $(sed -n 's|^\([a-z][a-z0-9-]*\)/.*|\1|p' "$WORK/tracked" | sort -u); do
        local found=0
        for l in $LANGS; do
            if [ -d "$ROOT/$d/$l" ]; then printf '%s\t%s\t%s\n' "$d" "$l" "$d/$l"; found=1; fi
        done
        if [ "$found" = 0 ] && [ -f "$ROOT/$d/go.mod" ] && [ -f "$ROOT/$d/CONTRACT.md" ]; then
            printf '%s\t%s\t%s\n' "$d" go "$d"
        fi
    done
}

# ---- what the inventory declares by hand --------------------------------
#
# MAP.md is this repository's hand-written inventory of what exists, and its
# languages column is the only prior answer to "which languages does this layer
# have". It is also the reason ABSENT is in the vocabulary: a language declared
# there and not in the tree has been checked and confirmed missing, which is not
# the same as a layer that never had one. Backticked spans are stripped first,
# or logging's "Go (`logging/python/` is an empty directory)" reads as a Python
# implementation.
#
# The inventory is not published with the code, so a clone that does not have it
# still produces the grid — every empty cell then reads `undeclared` rather than
# claiming a gap is deliberate on the strength of a file nobody can see.
declared() {
    [ -f "$ROOT/MAP.md" ] || return 0
    awk -F'|' '
    NF >= 5 {
        d=$2; gsub(/[ \t]/,"",d)
        if (d !~ /^`[a-z][a-z0-9-]*\/`$/) next
        gsub(/`/,"",d); sub(/\/$/,"",d)
        langs=$4; gsub(/`[^`]*`/,"",langs)
        if (langs ~ /Go/)     print d "\tgo"
        if (langs ~ /Python/) print d "\tpython"
        if (langs ~ /C\+\+/)  print d "\tcpp"
    }' "$ROOT/MAP.md" | sort -u
}

# ---- what the gate reaches ---------------------------------------------
#
# Not whether it passed — nothing records that — only whether the cell is named
# by something the gate runs. Three different ways of being named, one per
# language, because that is how the gate is written.
reached() {  # reached <layer> <language> <impl>
    case "$2" in
        go)     [[ $'\n'"$GOWORK_TXT"$'\n' == *$'\n\t./'"$3"$'\n'* ]] ;;
        python) [[ $CHECK_TXT == *"$1/python"* ]] ;;
        cpp)    [[ $CMAKE_TXT == *"add_subdirectory($1/cpp)"* ]] ;;
    esac
}

tests_in() {  # tests_in <language> <impl> — test source files git tracks in the cell
    case "$1" in
        go)     grep -c "^$2/.*_test\.go$" "$WORK/tracked" ;;
        python) grep -c "^$2/.*/\?test_[^/]*\.py$" "$WORK/tracked" ;;
        cpp)    grep -cE "^$2/(.*/)?(test/|test_[^/]*\.(cpp|cc|h|hpp)$)" "$WORK/tracked" ;;
    esac
}

# ---- the recorded transcripts ------------------------------------------
#
# docs/results/ is the only place in the tree where a verdict is written down
# with the tree it measured beside it, and scripts/results.sh § check is the
# rule reused here: a transcript whose `# measures <dir>@<tree>` no longer
# matches measured code this tree no longer holds, so it proves what the layer
# used to do and nothing about today. Transcripts written by hand carry prose on
# that line instead of <dir>@<tree> and fall out here, which is right — they
# name hardware, not a cell.
LANG_RE_go='(^|[^a-zA-Z])[Gg][Oo]([^a-zA-Z]|$)'
LANG_RE_python='[Pp]ython'
LANG_RE_cpp='([Cc]\+\+|[Cc][Pp][Pp])'

# Emits layer, lang, rank, verdict, toolchain, attribution. The rank exists
# because a cell can hold more than one verdict — job is measured by AWAKE1 and
# by BEHAVIOUR1, download/cpp by ctest and by the published conformance suite —
# and only one reaches the grid. Sorting on the verdict word would rank DRIFTED
# first and let a stale verdict shadow a fresh one.
#
# The order is failure, then anything that could not be judged, then a pass,
# then anything drifted. SKIPPED above PASS and not below it: a cell one
# instrument proved and another could not reach is not a proven cell, and
# ranking the pass first is how a green ctest would go on hiding a conformance
# suite that was never put to the same code. Absence is reported, never passed.
transcripts() {
    local f n head rc dirs d tree now l re npass nfail
    for f in "$ROOT"/docs/results/*.txt; do
        # The grid lands in docs/results/ in the same header shape as the
        # transcripts, so without this it reads itself: a `# measures` line
        # naming every layer at its current tree, and every cell green on the
        # strength of the run that wrote it. An instrument that is its own
        # evidence proves nothing at all.
        [ "$f" = "$ROOT/$OUT_TXT" ] && continue
        # The gate record has the same header and is read per cell below, not
        # by the language-word search here, which would light up all three
        # languages for every layer it names.
        [ "$f" = "$ROOT/$GATE_TXT" ] && continue
        head="$(sed -n '1p' "$f")"
        case "$head" in '# '*' produced by '*', exit '*) ;; *) continue ;; esac
        n="$(basename "$f" .txt)"
        rc="${head##*exit }"
        dirs="$(sed -n 's/^# measures  //p' "$f")"
        for d in $dirs; do
            case "$d" in *@*) ;; *) continue ;; esac
            tree="${d##*@}"; d="${d%@*}"
            d="${d%%/*}"
            now="$(tree_at "$d")"
            for l in $LANGS; do
                eval "re=\$LANG_RE_$l"
                grep -qE "$re" "$f" || continue
                npass=$(grep -E '^[[:space:]]*PASS' "$f" | grep -cE "$re")
                nfail=$(grep -E '^[[:space:]]*FAIL' "$f" | grep -cE "$re")
                if [ "$tree" != "$now" ]; then
                    printf '%s\t%s\t3\tDRIFTED\t-\t%s recorded %s at %s@%s; the tree is %s@%s\n' \
                        "$d" "$l" "$n" "$([ "$rc" = 0 ] && echo PASS || echo FAIL)" "$d" "$tree" "$d" "$now"
                elif [ "$rc" = 0 ] && [ "$nfail" = 0 ]; then
                    printf '%s\t%s\t2\tPASS\t-\t%s, %s\n' "$d" "$l" "$n" \
                        "$([ "$npass" -gt 0 ] && echo "$npass scenarios" || echo "exit 0")"
                else
                    printf '%s\t%s\t0\tFAIL\t-\t%s, %s failed\n' "$d" "$l" "$n" "$nfail"
                fi
            done
        done
    done
}

# ---- the gate's own record ---------------------------------------------
#
# docs/results/GATE.txt, written by scripts/check.sh --record. The gate builds
# and tests most of this grid on every run and threw the result away into a
# mktemp its own trap deleted, so most of this grid read UNPROVEN for one
# reason: nobody wrote down what the gate had just proved.
#
# Read per cell, never per file. Every other transcript here is searched for a
# language word, which is right for a harness whose output says "go and python
# agreed"; this file names one implementation directory per line and a word
# search over it would make every layer green in all three languages.
#
# Three ways this record refuses to be believed, and they are the point of it:
# a header that is not the one check.sh writes, a layer whose tree has moved
# since the run, and a run taken on a tree with uncommitted changes under the
# layers it names. The last two are DRIFTED, which verdict() renders UNPROVEN
# with the reason `drifted` — the same treatment a stale harness transcript
# gets, because a record that cannot go stale is a record that lies quietly.
#
# A cell line's `why` ends in a bracketed toolchain where the gate knew one:
#
#     download/cpp   PASS   ctest under g++ 15.2.0 in WSL, 6 test runs [g++]
#
# That bracket is the answer to a question this grid printed PASS over for as
# long as it existed: which compiler. Every C++ cell meant whatever the record's
# mode happened to build — one compiler on one machine — and the grid said only
# PASS, so a column standing for a three-platform claim named nothing. A record
# written before the bracket existed leaves it empty and the cell reads `?`,
# which is the true statement about that record.
gate_record() {
    local f="$ROOT/$GATE_TXT" head mode dirty=0 d tree impl v why tool layer lang
    [ -f "$f" ] || return 0
    head="$(sed -n '1p' "$f")"
    case "$head" in
        '# GATE  produced by scripts/check.sh'*', commit '*', exit '*) ;;
        *) echo "matrix.sh: $GATE_TXT is not a record scripts/check.sh --record wrote — ignoring it" >&2; return 0 ;;
    esac
    mode="$(sed -n 's/^# mode  *//p' "$f")"
    declare -A MEAS=()
    for d in $(sed -n 's/^# measures  //p' "$f"); do
        case "$d" in +uncommitted) dirty=1; continue ;; *@*) ;; *) continue ;; esac
        MEAS[${d%@*}]="${d##*@}"
    done
    while IFS=$'\t' read -r impl v why; do
        [ -n "${CELL_OF[$impl]:-}" ] || continue
        layer="${CELL_OF[$impl]%%	*}"; lang="${CELL_OF[$impl]#*	}"
        tree="${MEAS[$layer]:-none}"
        tool=-
        case "$why" in *' ['*']') tool="${why##*[}"; tool="${tool%]}"; why="${why% [*}" ;; esac
        if [ "$dirty" = 1 ]; then
            printf '%s\t%s\t3\tDRIFTED\t-\tGATE ran with uncommitted changes under the layers it names, so it judged a tree no commit holds\n' "$layer" "$lang"
        elif [ "$tree" != "$(tree_at "$layer")" ]; then
            printf '%s\t%s\t3\tDRIFTED\t-\tGATE recorded %s at %s@%s; the tree is %s@%s\n' \
                "$layer" "$lang" "$v" "$layer" "$tree" "$layer" "$(tree_at "$layer")"
        else
            case "$v" in
                PASS) printf '%s\t%s\t2\tPASS\t%s\tGATE %s, %s\n' "$layer" "$lang" "$tool" "$mode" "$why" ;;
                FAIL) printf '%s\t%s\t0\tFAIL\t%s\tGATE %s, %s\n' "$layer" "$lang" "$tool" "$mode" "$why" ;;
                *)    printf '%s\t%s\t1\tSKIPPED\t-\tthe last recorded gate run (%s) did not judge %s: %s\n' \
                          "$layer" "$lang" "$mode" "$impl" "$why" ;;
            esac
        fi
    done < <(sed -n 's/^  \([^ ][^ ]*\) \{1,\}\(PASS\|FAIL\|UNPROVEN\) \{1,\}\(.*\)$/\1\t\2\t\3/p' "$f")
}

# ---- the grid ----------------------------------------------------------

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
git ls-files > "$WORK/tracked"
# One ls-tree instead of a rev-parse per layer: on Windows each git fork costs
# most of a second, and this instrument is meant to be cheap enough to sit in
# the gate.
declare -A TREE_AT
while read -r _ _ sha name; do TREE_AT[$name]="$sha"; done < <(git ls-tree --abbrev HEAD)
tree_at() { printf '%s' "${TREE_AT[$1]:-none}"; }
cells > "$WORK/cells"
# A grid over an empty input set is the failure mode this project has already
# had: every cell would read as a deliberate gap and the page would say so.
[ -s "$WORK/cells" ] || {
    echo "matrix.sh: no <layer>/<language> implementation found under $ROOT — a grid over nothing would read as full coverage of nothing" >&2
    exit 2
}
[ "${1:-}" = "--cells" ] && { cat "$WORK/cells"; exit 0; }
declared > "$WORK/declared"
declare -A CELL_OF
while IFS=$'\t' read -r a b c; do CELL_OF[$c]="$a	$b"; done < "$WORK/cells"
{ transcripts; gate_record; } | sort -t"$(printf '\t')" -k1,1 -k2,2 -k3,3n | cut -f1,2,4,5,6 > "$WORK/evidence"
# The mode the record was written from, read here rather than inside
# gate_record(): the left side of that pipe is a subshell, so what it assigns
# does not reach the page. The page states what the mode left out, and a
# sentence about the mode that is typed by hand goes on being true until a run
# in another mode makes a liar of it under a lead saying nothing here is typed.
GATE_MODE="$(sed -n 's/^# mode  *//p' "$ROOT/$GATE_TXT" 2>/dev/null || true)"
cut -f1 "$WORK/cells" | sort -u > "$WORK/layers"

files() { [ "$1" = 1 ] && echo "1 test file" || echo "$1 test files"; }

# Read once into memory. Asking awk and git the same question once per cell put
# this over two minutes on Windows, where every fork is expensive, and a gate
# step nobody will wait for is a gate step somebody removes.
declare -A IMPL EVIDENCE DECLARED HAS_ROW
while IFS=$'\t' read -r a b c; do IMPL[$a/$b]="$c"; done < "$WORK/cells"
while IFS=$'\t' read -r a b c d e; do
    [ -n "${EVIDENCE[$a/$b]:-}" ] || EVIDENCE[$a/$b]="$c	$d	$e"
done < "$WORK/evidence"
while IFS=$'\t' read -r a b; do DECLARED[$a/$b]=1; HAS_ROW[$a]=1; done < "$WORK/declared"
GOWORK_TXT="$(slurp "$ROOT/go.work")"
CMAKE_TXT="$(slurp "$ROOT/CMakeLists.txt")"
CHECK_TXT="$(slurp "$ROOT/scripts/check.sh" | grep -E '^[[:space:]]*(pytests|run)[[:space:]]')"

# A verdict carries the toolchain that reached it, and a verdict that came from
# a record naming none carries `?`. There is no third state and there is no
# blank: a cell that says PASS and nothing else is a cell claiming a compiler,
# an interpreter and a platform it never named, which is how every C++ cell in
# this grid came to stand for one compiler on one machine in silence.
verdict() {  # verdict <layer> <lang> -> VERDICT <TAB> toolchain <TAB> reason <TAB> attribution
    local layer="$1" lang="$2" impl e v t n
    impl="${IMPL[$layer/$lang]:-}"
    if [ -z "$impl" ]; then
        if [ -n "${DECLARED[$layer/$lang]:-}" ]; then
            printf 'ABSENT\t\tdeclared\tthe inventory declares %s for %s and the tree has none\n' "$lang" "$layer"
        elif [ -n "${HAS_ROW[$layer]:-}" ]; then
            printf -- '—\t\tnone\tno %s implementation, and the inventory declares none, so the gap is deliberate\n' "$lang"
        else
            printf -- '—\t\tundeclared\tno %s implementation, and the inventory carries no row for %s, so nothing says whether the gap is deliberate\n' "$lang" "$layer"
        fi
        return
    fi
    e="${EVIDENCE[$layer/$lang]:-}"
    v="${e%%	*}"; e="${e#*	}"
    # A dash and not an empty field: bash treats a tab as IFS whitespace, so a
    # run of two collapses into one and every field after an empty one shifts
    # left. That put a whole attribution into the toolchain column.
    t="${e%%	*}"; e="${e#*	}"; [ "$t" = - ] && t=""
    case "$v" in
        PASS)    printf 'PASS\t%s\trecorded\t%s\n' "${t:-?}" "$e"; return ;;
        FAIL)    printf 'FAIL\t%s\trecorded\t%s\n' "${t:-?}" "$e"; return ;;
        DRIFTED) printf 'UNPROVEN\t\tdrifted\t%s\n' "$e"; return ;;
        SKIPPED) printf 'UNPROVEN\t\tskipped\t%s\n' "$e"; return ;;
    esac
    n="$(tests_in "$lang" "$impl")"
    if ! reached "$layer" "$lang" "$impl"; then
        printf 'UNPROVEN\t\tunreached\t%s exists with %s and nothing the gate runs names it\n' "$impl" "$(files "$n")"
    elif [ "$n" = 0 ]; then
        printf 'UNPROVEN\t\tuntested\tthe gate names %s and the cell carries no test file of its own\n' "$impl"
    else
        printf 'UNPROVEN\t\tgate\tthe gate builds and tests %s (%s) every run and records no per-cell result\n' "$impl" "$(files "$n")"
    fi
}

LAYERS="$(cat "$WORK/layers")"
declare -A CELL TOOL REASON WHY
for layer in $LAYERS; do
    for lang in $LANGS; do
        row="$(verdict "$layer" "$lang")"
        CELL[$layer/$lang]="${row%%	*}"; row="${row#*	}"
        TOOL[$layer/$lang]="${row%%	*}"; row="${row#*	}"
        REASON[$layer/$lang]="${row%%	*}"
        WHY[$layer/$lang]="${row#*	}"
        printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$layer" "$lang" \
            "${CELL[$layer/$lang]}" "${REASON[$layer/$lang]}" \
            "${WHY[$layer/$lang]}" "${TOOL[$layer/$lang]}"
    done
done > "$WORK/grid"

# What a reader sees in the grid: the verdict, and the toolchain that reached
# it. A verdict no toolchain can be attached to — no implementation, or nothing
# that judged one — carries neither.
celltext() { printf '%s%s' "${CELL[$1/$2]}" "${TOOL[$1/$2]:+ ${TOOL[$1/$2]}}"; }

n_of() { awk -F'\t' -v v="$1" '$3==v' "$WORK/grid" | wc -l | tr -d ' '; }

N_PASS=$(n_of PASS); N_FAIL=$(n_of FAIL); N_UNPROVEN=$(n_of UNPROVEN)
N_ABSENT=$(n_of ABSENT); N_NONE=$(n_of '—')
N_CELLS=$(( N_PASS + N_FAIL + N_UNPROVEN + N_ABSENT + N_NONE ))
N_IMPL=$(wc -l < "$WORK/cells" | tr -d ' ')
N_BLIND=$(( N_IMPL - N_PASS - N_FAIL ))
N_LAYERS=$(wc -l < "$WORK/layers" | tr -d ' ')

MEASURES=""
for d in $LAYERS; do MEASURES="$MEASURES $d@$(tree_at "$d")"; done
MEASURES="${MEASURES# }"
[ -z "$(git status --porcelain -- $LAYERS)" ] || MEASURES="$MEASURES +uncommitted"
measures() { printf '%s' "$MEASURES"; }

# A layer the inventory names and this walk did not find. vault/ is the one
# today: a nested repository, so its files are not in this tree's index at all.
unfound() { cut -f1 "$WORK/declared" | sort -u | comm -23 - "$WORK/layers" | paste -sd' ' -; }

# ---- docs/results/MATRIX.txt -------------------------------------------

# printf's %-Ns pads to a byte count, and the em dash a cell without an
# implementation carries is three bytes wide and one column wide, so a grid
# padded by printf comes out ragged exactly on the cells this instrument is
# most anxious that people read.
W=0
for d in $LAYERS; do [ "${#d}" -le "$W" ] || W="${#d}"; done
W=$(( W + 1 ))
SPACES='                        '
# One width for every verdict column, from the widest thing any of them holds.
# A per-language width would put the toolchain in the C++ column and nowhere
# else, and a reader would take that to mean C++ is the language with a
# toolchain question rather than the language that has two of them.
CW=8
for layer in $LAYERS; do
    for lang in $LANGS; do
        t="$(celltext "$layer" "$lang")"; t="${t//—/.}"
        [ "${#t}" -le "$CW" ] || CW="${#t}"
    done
done
CW=$(( CW + 2 ))
pad() {
    local s="$1" w="$2" n="${1//—/.}"
    printf '%s%s' "$s" "${SPACES:0:$(( w - ${#n} < 0 ? 0 : w - ${#n} ))}"
}

write_txt() {
    cat <<EOF
# MATRIX  produced by scripts/matrix.sh on $(date +%Y-%m-%d), commit $(git rev-parse --short HEAD)
# measures  $(measures)
# regenerate  sh scripts/matrix.sh   (--check refuses a grid the evidence no longer produces)
# scope     this tree only. It reads what is committed here and the transcripts
#           in docs/results/; it runs no implementation and no harness. Each
#           transcript it cites carries its own state line.

    PASS      evidence exists, was reached, and the implementation kept the rules
    FAIL      evidence exists and a rule broke
    UNPROVEN  could not be checked here — see the reason
    ABSENT    checked, and the thing is confirmed not there
    —         no implementation of this layer in this language

    Each cell also names the toolchain that reached its verdict, and ? where the
    record it came from named none. A verdict over a compiler nobody named is a
    claim about a compiler nobody named.

$(pad layer $W)$(for l in $LANGS; do pad "$l" $CW; done)
EOF
    pad "$(printf '%*s' $((W - 1)) '' | tr ' ' -)" $W
    for l in $LANGS; do pad "$(printf '%*s' $((CW - 2)) '' | tr ' ' -)" $CW; done
    printf '\n'
    local layer lang
    for layer in $LAYERS; do
        pad "$layer" $W
        for lang in $LANGS; do pad "$(celltext "$layer" "$lang")" $CW; done
        printf '\n'
    done
    cat <<EOF

$N_CELLS cells over $N_LAYERS layers: $N_PASS PASS, $N_FAIL FAIL, $N_UNPROVEN UNPROVEN, $N_ABSENT ABSENT, $N_NONE with no implementation.
$N_IMPL implementations exist and $N_BLIND of them carry no verdict attributed to this tree.

why each cell reads as it does
EOF
    local layer lang
    for layer in $LAYERS; do
        for lang in $LANGS; do
            printf '  '
            pad "$layer" $W
            pad "$lang" 8
            pad "$(celltext "$layer" "$lang")" $CW
            pad "${REASON[$layer/$lang]}" 12
            printf '%s\n' "${WHY[$layer/$lang]}"
        done
    done
    cat <<EOF

reasons
  recorded    a transcript in docs/results/ recorded this verdict for this tree
  drifted     a transcript recorded a verdict for a tree this one is no longer
  skipped     the last recorded gate run reached this cell and could not judge it
  gate        the gate runs this cell every time and no recorded run wrote down the result
  untested    the cell has no test file of its own
  unreached   nothing the gate runs names this cell
  declared    the inventory declares the language and the tree does not have it
  none        no implementation, and the inventory declares none, so the gap is deliberate
  undeclared  no implementation, and the inventory carries no row for the layer at all
EOF
    local uf; uf="$(unfound)"
    [ -z "$uf" ] || printf '\nthe inventory declares layer directories this walk did not find: %s\n' "$uf"
}

# ---- site/coverage.html ------------------------------------------------

cls() { case "$1" in PASS) echo good ;; FAIL) echo bad ;; UNPROVEN) echo unproven ;; ABSENT) echo absent ;; *) echo dash ;; esac; }

write_html() {
    cat <<'EOF'
<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Open Abstractions — coverage</title>
<link rel="icon" href="brand/favicon.ico" sizes="any">
<link rel="icon" href="brand/mark-32.png" type="image/png">
<link rel="apple-touch-icon" href="brand/mark-180.png">
<link rel="stylesheet" href="style.css">
<style>
.grid td:not(:first-child) { text-align: center; white-space: nowrap; }
.grid th:not(:first-child) { text-align: center; }
.cell { font: 600 12px ui-monospace, Consolas, Menlo, monospace; }
.tool { font: 400 11px ui-monospace, Consolas, Menlo, monospace; color: var(--dim); white-space: nowrap; }
.absent { color: var(--bad); }
.dash { color: var(--dim); font-weight: 400; }
.grid tr td:first-child { font-family: ui-monospace, Consolas, Menlo, monospace; }
.why td:nth-child(1), .why td:nth-child(2), .why td:nth-child(4), .why td:nth-child(5) {
  font-family: ui-monospace, Consolas, Menlo, monospace; font-size: 13px; }
.tally { font-variant-numeric: tabular-nums; }
</style>

<main>
<nav class="site"><a class="name" href="index.html">Open Abstractions</a>
<a href="index.html">Overview</a> <a href="cases.html">Cases</a> <a href="reference.html">Reference</a> <a href="evidence.html">Evidence</a> <a href="coverage.html" aria-current="page">Coverage</a> <a href="adopt.html">Adopt</a>
<a class="right" href="https://github.com/openabstractions">github.com/openabstractions</a></nav>

<h1>Coverage</h1>
EOF
    cat <<EOF
<p class="lead">Every layer against every language, and what proved each one. Generated by
<a href="https://github.com/openabstractions/abstractions/blob/main/scripts/matrix.sh"><code>scripts/matrix.sh</code></a>
from what is committed in the tree and the transcripts in
<a href="https://github.com/openabstractions/abstractions/tree/main/docs/results"><code>docs/results/</code></a>.
Nothing on this page is typed by hand, and a grid the evidence no longer produces fails the gate rather than going stale quietly.</p>
<p class="state" data-produced>Measured at commit <code>$(git rev-parse --short HEAD)</code> on $(date +%Y-%m-%d). Layer trees: <code>$(measures)</code>.
The same grid, in the same run, is <a href="https://github.com/openabstractions/abstractions/blob/main/docs/results/MATRIX.txt"><code>docs/results/MATRIX.txt</code></a>.</p>

<div class="warn"><p><strong>$N_BLIND of the $N_IMPL implementations in this tree carry no verdict attributed to this tree.</strong>
That is the honest headline. A cell is green here only when a recorded transcript says so about the tree as it is now —
a cross-language harness transcript, or
<a href="https://github.com/openabstractions/abstractions/blob/main/docs/results/GATE.txt"><code>GATE.txt</code></a>, the per-cell
record <code>scripts/check.sh --record</code> writes. Every one of those names the commit and the layer trees it judged,
and goes back to <code>UNPROVEN</code> the moment a layer moves under it. Calling the rest green would be assuming a run
nobody can point at.</p></div>

<h2 id="grid">The grid</h2>
<p>Beside each verdict is the toolchain that reached it, and <code>?</code> where the record it came from named none.
A C++ cell is the reason this is here: a <span class="cell good">PASS</span> in the C++ column used to stand for a
three-platform claim without naming a single compiler. This grid was recorded from a <code>${GATE_MODE:-unrecorded}</code>
run, and every cell says which toolchains reached it.</p>
<div class="wrap"><table class="grid">
<tr><th>layer</th>$(for l in $LANGS; do printf '<th>%s</th>' "$l"; done)</tr>
EOF
    local layer lang v t
    for layer in $LAYERS; do
        printf '<tr><td>%s</td>' "$layer"
        for lang in $LANGS; do
            v="${CELL[$layer/$lang]}"; t="${TOOL[$layer/$lang]}"
            printf '<td><span class="cell %s">%s</span>%s</td>' \
                "$(cls "$v")" "$v" "${t:+ <span class=\"tool\">${t//&/&amp;}</span>}"
        done
        printf '</tr>\n'
    done
    cat <<EOF
</table></div>
<p class="tally"><span class="cell good">PASS</span> $N_PASS ·
<span class="cell bad">FAIL</span> $N_FAIL ·
<span class="cell unproven">UNPROVEN</span> $N_UNPROVEN ·
<span class="cell absent">ABSENT</span> $N_ABSENT ·
no implementation $N_NONE. $N_CELLS cells over $N_LAYERS layers.</p>

<h2 id="vocabulary">What a cell means</h2>
<div class="wrap"><table>
<tr><th>cell</th><th>means</th></tr>
<tr><td><span class="cell good">PASS</span></td><td>evidence exists, was reached, and the implementation kept the rules</td></tr>
<tr><td><span class="cell bad">FAIL</span></td><td>evidence exists and a rule broke</td></tr>
<tr><td><span class="cell unproven">UNPROVEN</span></td><td>could not be checked here — a toolchain absent, a platform absent, a harness skipped, or a result nobody records</td></tr>
<tr><td><span class="cell absent">ABSENT</span></td><td>checked, and the thing is confirmed not there — a language this project's own inventory claims and the tree does not have</td></tr>
<tr><td><span class="cell dash">—</span></td><td>no implementation of this layer in this language, and none is claimed, so the gap is deliberate</td></tr>
</table></div>
<p><strong><span class="cell unproven">UNPROVEN</span> and <span class="cell dash">—</span> are not the same thing and must never be read as one.</strong>
A Go-only layer has legitimately empty cells. An implementation that exists and nothing tests is a gap, and it is
written <span class="cell unproven">UNPROVEN</span> so it looks like one.</p>

<h2 id="why">Why each cell reads as it does</h2>
<p>The reason is the point of the table. <code>gate</code> is a cell the project's own gate runs on every change and no recorded
run wrote down; <code>skipped</code> is a cell the last recorded run could not judge; <code>unreached</code> is a cell nothing
runs at all. All three are <span class="cell unproven">UNPROVEN</span> and they are very different gaps.</p>
<div class="wrap"><table class="why">
<tr><th>layer</th><th>language</th><th>cell</th><th>toolchain</th><th>reason</th><th>what proved it, or what did not</th></tr>
EOF
    awk -F'\t' '{ for (i = 1; i <= 6; i++) { gsub(/&/, "\\&amp;", $i); gsub(/</, "\\&lt;", $i) }
                  printf "<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>\n", $1, $2, $3, $6, $4, $5 }' "$WORK/grid"
    cat <<EOF
</table></div>
<div class="wrap"><table>
<tr><th>reason</th><th>what it says about the cell</th></tr>
<tr><td><code>recorded</code></td><td>a transcript in <code>docs/results/</code> recorded this verdict for this tree</td></tr>
<tr><td><code>drifted</code></td><td>a transcript recorded a verdict, for a tree this one is no longer. It proves what the code used to do</td></tr>
<tr><td><code>skipped</code></td><td>the last recorded gate run reached this cell and could not judge it — a toolchain absent, or a mode that leaves it out</td></tr>
<tr><td><code>gate</code></td><td>the gate builds and tests this cell every run, and no recorded run has written down the result</td></tr>
<tr><td><code>untested</code></td><td>the cell has no test file of its own</td></tr>
<tr><td><code>unreached</code></td><td>nothing the gate runs names this cell</td></tr>
<tr><td><code>declared</code></td><td>the project's inventory declares the language and the tree does not have it</td></tr>
<tr><td><code>none</code></td><td>no implementation, and none claimed, so the gap is deliberate</td></tr>
<tr><td><code>undeclared</code></td><td>no implementation, and the inventory carries no row for the layer at all, so nothing says whether the gap is deliberate</td></tr>
</table></div>

<h2 id="evidence">Where the evidence comes from</h2>
<p>Six cross-language harnesses and one public suite. Five of the six say which language a result belongs to, and two of them
leave a committed transcript naming the commit they judged. The rest of the grid rests on the gate itself, which builds and
tests most of these cells on every run: <code>scripts/check.sh --record</code> writes what it reached into
<a href="https://github.com/openabstractions/abstractions/blob/main/docs/results/GATE.txt"><code>GATE.txt</code></a>, one line per
cell. A cell still reads <code>gate</code> where no recorded run has judged it.</p>
<div class="wrap"><table>
<tr><th>harness</th><th>asks</th><th>says which language?</th><th>leaves a committed transcript?</th></tr>
<tr><td><a href="https://github.com/openabstractions/abstractions/blob/main/scripts/behaviour-conformance.sh"><code>behaviour-conformance.sh</code></a></td><td>do the implementations <em>do</em> the same thing</td><td>yes — every scenario line names the languages that agreed, and an absent one is named too</td><td>yes, <a href="https://github.com/openabstractions/abstractions/blob/main/docs/results/BEHAVIOUR1.txt"><code>BEHAVIOUR1.txt</code></a></td></tr>
<tr><td><a href="https://github.com/openabstractions/abstractions/blob/main/scripts/spec-conformance.sh"><code>spec-conformance.sh</code></a></td><td>do they read a download spec the same way</td><td>yes — it prints its roster and names any implementation that did not run</td><td>no</td></tr>
<tr><td><a href="https://github.com/openabstractions/abstractions/blob/main/scripts/verdict-conformance.sh"><code>verdict-conformance.sh</code></a></td><td>do they <em>refuse</em> the same documents</td><td>yes — a divergence names the implementations on each side</td><td>no</td></tr>
<tr><td><a href="https://github.com/openabstractions/abstractions/blob/main/scripts/conformance.sh"><code>conformance.sh</code></a></td><td>can each language finish a job another started</td><td>yes — every step names the implementation that took it</td><td><a href="https://github.com/openabstractions/abstractions/blob/main/docs/results/CONFORM1.txt"><code>CONFORM1.txt</code></a>, with no header naming the tree it measured</td></tr>
<tr><td><a href="https://github.com/openabstractions/abstractions/blob/main/scripts/obtain-conformance.sh"><code>obtain-conformance.sh</code></a></td><td>can a stranger get each implementation through its own front door</td><td>yes — one line per door</td><td>no</td></tr>
<tr><td><a href="https://github.com/openabstractions/abstractions/blob/main/scripts/identity-conformance.sh"><code>identity-conformance.sh</code></a></td><td>do Go and Python group model names the same way</td><td>yes — it names the one that disagrees</td><td>no</td></tr>
<tr><td><a href="https://github.com/openabstractions/abstractions/blob/main/conformance/run.sh"><code>conformance/run.sh</code></a></td><td>does one implementation keep the rules on the contract pages</td><td>no — it judges one driver per run and the driver is the caller's choice</td><td>no</td></tr>
</table></div>
<p>The grid also reads the tree itself: which <code>&lt;layer&gt;/&lt;language&gt;/</code> directories exist, which Go
modules the workspace lists, which C++ directories the top-level build adds, which Python suites the gate names, and how
many test files each cell carries.</p>

<footer>
<p>Nothing here carries an API stability promise. <code>UNPROVEN</code> is written wherever a platform has not been reached.</p>
<p><a href="index.html">Overview</a> · <a href="cases.html">Cases</a> · <a href="reference.html">Reference</a> · <a href="evidence.html">Evidence</a> · <a href="adopt.html">Adopt</a> ·
<a href="https://github.com/openabstractions">github.com/openabstractions</a></p>
</footer>
</main>
</html>
EOF
}

# ---- write, or check ---------------------------------------------------
#
# The date and the head commit are provenance and move without the evidence
# moving, so they are excluded from the comparison. What is not excluded is the
# `# measures` line: it names each layer's own tree, which changes exactly when
# that layer changes, and a grid measured against a layer that has since moved
# is stale and says so.
strip() { grep -v '^# MATRIX  produced by' | grep -v 'data-produced'; }

if [ "${1:-}" = "--check" ]; then
    bad=0
    for pair in "$OUT_TXT:write_txt" "$OUT_HTML:write_html"; do
        f="${pair%%:*}"; fn="${pair##*:}"
        if [ ! -f "$ROOT/$f" ]; then echo "stale  $f: not in the tree"; bad=1; continue; fi
        "$fn" | strip > "$WORK/want"
        strip < "$ROOT/$f" > "$WORK/have"
        if cmp -s "$WORK/want" "$WORK/have"; then echo "ok     $f"
        else echo "stale  $f  differs from what today's evidence produces:"; diff "$WORK/have" "$WORK/want" | head -20 | sed 's/^/         /'; bad=1; fi
    done
    [ "$bad" = 0 ] || echo "a committed grid the evidence no longer produces is a coverage claim nobody measured: run sh scripts/matrix.sh"
    exit "$bad"
fi

write_txt > "$ROOT/$OUT_TXT"
write_html > "$ROOT/$OUT_HTML"
echo "$OUT_TXT   $N_CELLS cells, $N_PASS PASS, $N_FAIL FAIL, $N_UNPROVEN UNPROVEN, $N_ABSENT ABSENT, $N_NONE none"
echo "$OUT_HTML  $N_BLIND of $N_IMPL implementations carry no verdict attributed to this tree"
