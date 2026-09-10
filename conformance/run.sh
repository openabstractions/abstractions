#!/bin/sh
set -u

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
SCENARIOS="$HERE/scenarios"
CONTRACTS="$HERE/contracts"
FIXTURE=""
FIXTURE_WHY=""
FIXTURE_PID=""
ONLY=""

usage() {
    cat <<'EOF'
usage: run.sh [options] -- <driver command ...>

  --scenarios DIR    scenario files (default: scenarios/ beside this script)
  --contracts DIR    contract pages to read invariant tags from (default:
                     contracts/ beside this script). contracts.list says which
                     pages the scenarios need and where to fetch each one; a
                     page the run needs and does not have is named, and the run
                     is incomplete rather than passed
  --only NAME        run just this scenario; repeatable
  --fixture-url URL  an already-running wire fixture, instead of starting one
  --no-fixture       do not start one; every wire scenario is unreachable

exit 0  every judged rule passed and nothing was out of reach
     1  a rule failed
     2  incomplete: a rule could not be reached, a contract page the scenarios
        cite was not supplied, or an expectation named a rule no page declares
     3  the suite could not be set up, or two contract pages declare one rule

The driver contract is DRIVER.md. Nothing here reads our source tree.
EOF
}

while [ $# -gt 0 ]; do
    case "$1" in
        --scenarios) SCENARIOS=$2; shift 2 ;;
        --contracts) CONTRACTS=$2; shift 2 ;;
        --only) ONLY="$ONLY $2"; shift 2 ;;
        --fixture-url) FIXTURE=$2; FIXTURE_WHY="given"; shift 2 ;;
        --no-fixture) FIXTURE_WHY="not wanted"; shift ;;
        -h|--help) usage; exit 0 ;;
        --) shift; break ;;
        -*) echo "run.sh: unknown option $1" >&2; usage >&2; exit 3 ;;
        *) break ;;
    esac
done
[ $# -gt 0 ] || { usage >&2; exit 3; }

[ -d "$SCENARIOS" ] || { echo "run.sh: no scenario directory at $SCENARIOS" >&2; exit 3; }

WORK=$(mktemp -d) || exit 3
cleanup() {
    [ -n "$FIXTURE_PID" ] && kill "$FIXTURE_PID" 2>/dev/null
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

quoted=""
for a in "$@"; do
    quoted="$quoted '$(printf '%s' "$a" | sed "s/'/'\\\\''/g")'"
done
printf '#!/bin/sh\nexec%s "$@"\n' "$quoted" > "$WORK/driver"
chmod +x "$WORK/driver"
DRIVER="$WORK/driver"

selected() {
    [ -n "$ONLY" ] || return 0
    case " $ONLY " in *" $1 "*) return 0 ;; esac
    return 1
}

# The declared `# requires:` line is a floor, and it drifts from the file below
# it: every scenario declaring `transfer` also submits records and takes leases,
# which is `store`. A driver handed a scenario it never claimed fails a rule
# instead of being told the scenario is out of its reach, so the operations
# decide, by the table in capabilities.list, and the declared line is unioned in.
# One awk over the directory: a name and its needs per line on stdout, an
# operation the table does not place on stderr.
classify() {
    awk -v LIST="$CAPLIST" '
    BEGIN {
        while ((getline line < LIST) > 0) {
            if (line ~ /^[ \t]*(>|$)/) continue
            n = split(line, f, /[ \t]+/)
            for (i = 2; i <= n; i++)
                if (index(f[i], "=")) key[f[i]] = f[1]; else op[f[i]] = f[1]
        }
    }
    function add(c) { if (c != "" && !(c in have)) { have[c] = 1; needs = needs " " c } }
    function flush() { if (name != "") print name "\t" substr(needs, 2) }
    FNR == 1 {
        flush()
        name = FILENAME; sub(/.*\//, "", name); sub(/\.txt$/, "", name)
        needs = ""; delete have
    }
    /^# *requires:/ { s = $0; sub(/^# *requires: */, "", s); n = split(s, f, /[ \t]+/); for (i = 1; i <= n; i++) add(f[i]); next }
    /^#/ || /^[ \t]*$/ { next }
    {
        if ($1 in op) add(op[$1]); else print name "\t" $1 > "/dev/stderr"
        for (i = 2; i <= NF; i++) for (k in key) if (index($i, k) == 1) add(key[k])
    }
    END { flush() }
    ' "$@"
}

# A scenario's name comes out of its path here and everywhere below, rather
# than out of basename. Spawning a process is expensive on Windows and this
# suite opens with three passes over the whole directory, so the runner was
# spending longer working out names than running drivers.
scenario_name() { n=${1##*/}; n=${n%.txt}; }

CAPLIST=${CAPABILITIES_LIST:-$HERE/capabilities.list}
[ -f "$CAPLIST" ] || { echo "run.sh: no capabilities.list at $CAPLIST — nothing says what a scenario needs" >&2; exit 3; }
NEEDS="$WORK/needs"
: > "$NEEDS"; : > "$WORK/unplaced"
have=no
for f in "$SCENARIOS"/*.txt; do
    [ -f "$f" ] && { have=yes; break; }
done
[ "$have" = no ] || classify "$SCENARIOS"/*.txt > "$NEEDS" 2> "$WORK/unplaced"

needs_of() { awk -F'\t' -v n="$1" '$1 == n { print $2; exit }' "$NEEDS"; }
unplaced_in() { awk -F'\t' -v n="$1" '$1 == n && !s[$2]++ { printf " `%s`", $2 }' "$WORK/unplaced"; }

wanted_wire=no
for f in "$SCENARIOS"/*.txt; do
    [ -f "$f" ] || continue
    scenario_name "$f"
    selected "$n" || continue
    case " $(needs_of "$n") " in
        *" wire "*) wanted_wire=yes ;;
    esac
done

if [ -z "$FIXTURE" ] && [ -z "$FIXTURE_WHY" ] && [ "$wanted_wire" = yes ]; then
    if [ ! -f "$HERE/fixture.py" ]; then
        FIXTURE_WHY="no fixture.py beside this script"
    else
        PY=""; PYARG=""
        for c in python3 python; do
            if command -v "$c" >/dev/null 2>&1 && "$c" -c pass >/dev/null 2>&1; then PY=$c; break; fi
        done
        if [ -z "$PY" ] && command -v py >/dev/null 2>&1 && py -3 -c pass >/dev/null 2>&1; then
            PY=py; PYARG=-3
        fi
        if [ -z "$PY" ]; then
            FIXTURE_WHY="no working python 3 on PATH to run fixture.py"
        elif ! mkfifo "$WORK/url" 2>/dev/null; then
            FIXTURE_WHY="this filesystem has no fifo, so the fixture's port cannot be read without polling"
        else
            "$PY" ${PYARG:+$PYARG} "$HERE/fixture.py" > "$WORK/url" 2>"$WORK/fixture.err" &
            FIXTURE_PID=$!
            FIXTURE=$(head -n 1 < "$WORK/url" | tr -d '\r')
            [ -n "$FIXTURE" ] || FIXTURE_WHY="fixture.py printed no base URL: $(head -n 1 "$WORK/fixture.err" 2>/dev/null)"
        fi
    fi
fi
export ABSTRACTION_FIXTURE="$FIXTURE"

CAPS=$("$DRIVER" --capabilities 2>"$WORK/caps.err" | tr -d '\r' | tr '\n' ' ')
CAPS=$(printf '%s' "$CAPS" | tr -s ' ' | sed 's/^ //;s/ $//')

echo "abstraction conformance"
echo "  driver:       $*"
if [ -z "$CAPS" ]; then
    echo "  capabilities: NONE — $(head -n 1 "$WORK/caps.err" 2>/dev/null)"
    echo
    echo "  a driver that declares no capabilities has nothing this suite can judge."
    echo "  RESULT: NOT SET UP"
    exit 3
fi
echo "  capabilities: $CAPS"
echo "  scenarios:    $SCENARIOS"
if [ -n "$FIXTURE" ]; then
    echo "  fixture:      $FIXTURE"
else
    echo "  fixture:      ABSENT — ${FIXTURE_WHY:-not needed by the scenarios selected}"
fi

# The first occurrence of a tag on a page is the declaration; a later one is a
# citation, prose about the rule rather than the rule. Two sentences filed under
# one tag leave a report picking one, so a passing mention can replace what a
# rule says. scripts/behaviour-conformance.sh reads a page by the same rule.
invariants() {
    awk -v PAGE="$2" '
    function emit(block, line,   before, cut, i, tag, sent) {
        gsub(/\n/, " ", block)
        while (match(block, /\[[A-Z]+-[A-Z]*[0-9]+\]/)) {
            tag = substr(block, RSTART + 1, RLENGTH - 2)
            before = substr(block, 1, RSTART - 1)
            block = substr(block, RSTART + RLENGTH)
            if (seen[tag]++) continue
            sub(/[.:;,] *$/, "", before)
            cut = 0
            for (i = 1; i < length(before); i++)
                if (substr(before, i, 2) == ". ") cut = i + 1
            sent = substr(before, cut + 1)
            gsub(/\[[A-Z]+-[A-Z]*[0-9]+\]/, "", sent)
            gsub(/[*`~>|]/, "", sent)
            sub(/^ *#+ */, "", sent)
            sub(/^[ \t,;:-]+ */, "", sent)
            gsub(/  +/, " ", sent)
            sub(/ +$/, "", sent)
            if (length(sent) > 92) sent = substr(sent, 1, 89) "..."
            print tag "\t" sent "\t" PAGE "\t" line
        }
    }
    /^```/          { fence = !fence; emit(buf, bufline); buf = ""; next }
    fence           { next }
    /^$/            { emit(buf, bufline); buf = ""; next }
    /^[|#]/         { emit(buf, bufline); buf = ""; emit($0, FNR); next }
                    { if (buf == "") bufline = FNR; buf = buf " " $0 }
    END             { emit(buf, bufline) }
    ' "$1"
}

DECLARED="$WORK/declared"
: > "$DECLARED"
pages=""
if [ -d "$CONTRACTS" ]; then
    for p in "$CONTRACTS"/*.md; do
        [ -f "$p" ] || continue
        b=$(basename "$p")
        pages="$pages $b"
        invariants "$p" "$b" >> "$DECLARED"
    done
fi
sort -u -o "$DECLARED" "$DECLARED"
if [ -n "$pages" ]; then
    echo "  contracts:   $pages"
else
    echo "  contracts:    NONE at $CONTRACTS — the end of this run says what to fetch"
fi
echo

twice=$(cut -f1 "$DECLARED" | sort | uniq -d)
if [ -n "$twice" ]; then
    echo "  two contract pages declare the same rule, and nothing here can say which"
    echo "  of them binds. A run that picked one would print a rule nobody wrote."
    echo
    for t in $twice; do
        awk -F'\t' -v t="$t" '$1 == t { printf "      [%s] %s near line %s: %s\n", $1, $3, $4, $2 }' "$DECLARED"
    done
    echo
    echo "  RESULT: NOT SET UP"
    exit 3
fi

PASS="$WORK/tag.pass"; FAIL="$WORK/tag.fail"; UNREACH="$WORK/tag.unreach"; CITED="$WORK/tag.cited"
WHERE="$WORK/tag.where"
: > "$PASS"; : > "$FAIL"; : > "$UNREACH"; : > "$CITED"; : > "$WHERE"
n_pass=0; n_fail=0; n_unreach=0; n_silent=0; n_error=0
UNDECIDED=""

# A rule is cited by an expectation and nowhere else. Scenarios carry prose that
# names rules they do not test, and reading the whole file reports a rule as
# covered because somebody mentioned it in a comment.
cited_rules() {
    awk '
    FNR == 1 { name = FILENAME; sub(/.*\//, "", name); sub(/\.txt$/, "", name) }
    /^#[ \t]*expect[ \t]+[0-9]+[ \t]*:/ {
        step = $0
        sub(/^#[ \t]*expect[ \t]+/, "", step)
        sub(/[^0-9].*$/, "", step)
        line = $0
        sub(/[ \t]+$/, "", line)
        while (match(line, /\[[A-Z]+-[A-Z]*[0-9]+\]$/)) {
            printf "%s\t%s step %s\n", substr(line, RSTART + 1, RLENGTH - 2), name, step
            line = substr(line, 1, RSTART - 1)
            sub(/[ \t]+$/, "", line)
        }
    }' "$@"
}

# Not a tab. A tab is IFS whitespace, so `IFS=<tab> read` folds two into one, and
# an expectation with no rule tag has an empty tag field — which used to shift
# every column after it and file fragments of expectation text as rule names.
SEP=$(printf '\034')

judge() {
    awk -v SEP="$SEP" '
    function head(s) { sub(/[ \t].*$/, "", s); return s }
    function tail(s,   p) { p = index(s, " "); return p ? substr(s, p + 1) : "" }
    function toks(s, out) {
        delete out
        sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s)
        if (s == "") return 0
        if (s !~ "^" KEY) return split(s, out, /[ \t]+/)
        gsub(" " KEY, "\034&", s)
        return split(s, out, "\034 ")
    }
    function holds(want, got,   claim, body, wn, gn, i, j, open, w, g) {
        sub(/[ \t]+$/, "", want); sub(/[ \t]+$/, "", got)
        if (want == "") return 1
        if (got == "") return 0
        if (index(head(want), "=")) { claim = ""; body = want }
        else                        { claim = head(want); body = tail(want) }
        if (claim != "" && claim != head(got)) return 0
        open = (body == "..." || body ~ /[ \t]\.\.\.$/)
        if (open) sub(/([ \t]|^)\.\.\.$/, "", body)
        wn = toks(body, w)
        gn = toks(tail(got), g)
        j = 1
        for (i = 1; i <= wn; i++) {
            if (open) while (j <= gn && g[j] != w[i]) j++
            if (j > gn || g[j] != w[i]) return 0
            j++
        }
        return open || j > gn
    }
    BEGIN { KEY = "[A-Za-z_][A-Za-z0-9_.-]*=" }
    NR == FNR { answer[FNR] = $0; next }
    {
        want = substr($0, index($0, " ") + 1)
        sub(/[ \t]+$/, "", want)
        tags = ""
        while (match(want, /\[[A-Z]+-[A-Z]*[0-9]+\]$/)) {
            tags = substr(want, RSTART + 1, RLENGTH - 2) " " tags
            want = substr(want, 1, RSTART - 1)
            sub(/[ \t]+$/, "", want)
        }
        p = index(answer[$1], " -> ")
        got = p ? substr(answer[$1], p + 4) : ""
        printf "%s%s%s%s%s%s%s%s%s\n", (holds(want, got) ? "held" : "broke"), SEP, $1, SEP, tags, SEP, want, SEP, got
    }
    ' "$1" "$2"
}

run_one() {
    f=$1
    scenario_name "$f"; name=$n
    need=$(needs_of "$name")

    missing=""
    for c in $need; do
        case " $CAPS " in *" $c "*) ;; *) missing="$missing $c" ;; esac
    done
    stray=$(unplaced_in "$name")
    if [ -n "$stray" ]; then
        why="capabilities.list places$stray under no capability, so nothing says which driver may run this"
    elif [ "$missing" != "" ]; then
        why="the driver does not declare$missing"
    fi
    if [ -n "$stray" ] || [ "$missing" != "" ]; then
        echo "  ----  $name: UNREACHABLE — $why"
        cited_rules "$f" | while read -r t rest; do printf '%s\t%s\n' "$t" "$why"; done >> "$UNREACH"
        n_unreach=$((n_unreach + 1))
        return
    fi

    w="$WORK/w.$name"
    mkdir -p "$w"
    out="$WORK/t.$name"
    if ! ABSTRACTION_FIXTURE="${FIXTURE:+$FIXTURE/$name}" "$DRIVER" "$w" "$f" >"$out" 2>"$out.err"; then
        echo "  ????  $name: the driver exited non-zero — $(head -n 1 "$out.err")"
        n_error=$((n_error + 1))
        return
    fi
    if LC_ALL=C tr -d '\n' < "$out" | LC_ALL=C grep -q "$(printf '\r')"; then
        echo "  FAIL  $name: the driver writes CR — this transcript is LF only"
        cited_rules "$f" | while read -r t rest; do printf '%s\tCR in the transcript\n' "$t"; done >> "$FAIL"
        n_fail=$((n_fail + 1))
        return
    fi

    sed -n -E 's/^# *undecided +([0-9]+) *: */\1 /p' "$f" | while read -r step note; do
        printf '    %s step %s — %s\n' "$name" "$step" "$note"
    done >> "$WORK/undecided"

    if ! grep -qE '^# *expect +[0-9]' "$f"; then
        echo "  ....  $name: ran, but the scenario asserts nothing about what SHOULD happen"
        n_silent=$((n_silent + 1))
        return
    fi

    bad=0
    held=0
    sed -n -E 's/^# *expect +([0-9]+) *: */\1 /p' "$f" > "$WORK/exp"
    judge "$out" "$WORK/exp" > "$WORK/judged"
    while IFS=$SEP read -r verdict step tags want got; do
        if [ "$verdict" = held ]; then
            held=$((held + 1))
            for t in $tags; do printf '%s\t%s\n' "$t" "$name" >> "$PASS"; done
            continue
        fi
        echo "  FAIL  $name: step $step"
        echo "          expected: $want"
        echo "          observed: ${got:-<no such step>}"
        for t in $tags; do printf '%s\t%s step %s\n' "$t" "$name" "$step" >> "$FAIL"; done
        bad=1
    done < "$WORK/judged"

    if [ "$bad" = 1 ]; then
        n_fail=$((n_fail + 1))
    else
        echo "  PASS  $name: $held expectations held"
        n_pass=$((n_pass + 1))
    fi
}

: > "$WORK/undecided"
SELWHERE="$WORK/tag.selected"
: > "$SELWHERE"
# One awk over the whole directory, not two per file. What a selected scenario
# cites is the same set filtered by name, which costs one more awk rather than
# forty-six. The glob is passed through unquoted on purpose: pathname expansion
# is not field-split, so a directory whose path holds a space still arrives as
# one argument per file.
have=no
for f in "$SCENARIOS"/*.txt; do
    [ -f "$f" ] && { have=yes; break; }
done
[ "$have" = no ] || cited_rules "$SCENARIOS"/*.txt > "$WHERE"
sort -u -o "$WHERE" "$WHERE"
if [ -z "$ONLY" ]; then
    cp "$WHERE" "$SELWHERE"
else
    for s in $ONLY; do printf '%s\n' "$s"; done | sort -u > "$WORK/only"
    awk -F'\t' 'NR == FNR { sel[$0] = 1; next }
                { name = $2; sub(/ step .*/, "", name); if (name in sel) print }' \
        "$WORK/only" "$WHERE" > "$SELWHERE"
fi
sort -u -o "$SELWHERE" "$SELWHERE"
cut -f1 "$WHERE" | sort -u > "$CITED"

any=0
for f in "$SCENARIOS"/*.txt; do
    [ -f "$f" ] || continue
    scenario_name "$f"
    selected "$n" || continue
    any=1
    run_one "$f"
done
[ "$any" = 1 ] || { echo "  no scenarios matched"; exit 3; }

cut -f1 "$FAIL" | sort -u > "$WORK/failed.tags"
cut -f1 "$PASS" | sort -u | comm -23 - "$WORK/failed.tags" > "$WORK/passed.tags"
cut -f1 "$UNREACH" | sort -u | comm -23 - "$WORK/failed.tags" | comm -23 - "$WORK/passed.tags" > "$WORK/unreached.tags"

echo
echo "  rules"
echo "    passed:      $(grep -c . < "$WORK/passed.tags")"
echo "    failed:      $(grep -c . < "$WORK/failed.tags")$(sed 's/^/ /' "$WORK/failed.tags" | tr -d '\n')"
u=$(grep -c . < "$WORK/unreached.tags")
echo "    unreachable: $u"
if [ "$u" -gt 0 ]; then
    awk -F'\t' 'NR==FNR { if (!($1 in why)) why[$1]=$2; next }
                { printf "      [%s] %s\n", $1, why[$1] }' "$UNREACH" "$WORK/unreached.tags"
fi

nstray=0
if [ -s "$DECLARED" ]; then
    cut -f1 "$DECLARED" | sort -u > "$WORK/declared.tags"
    comm -23 "$CITED" "$WORK/declared.tags" > "$WORK/stray.tags"
    nstray=$(grep -c . < "$WORK/stray.tags")
    comm -13 "$CITED" "$WORK/declared.tags" > "$WORK/uncovered.tags"
    ndecl=$(grep -c . < "$WORK/declared.tags")
    ncited=$(comm -12 "$CITED" "$WORK/declared.tags" | grep -c .)
    nunc=$(grep -c . < "$WORK/uncovered.tags")
    echo
    echo "  coverage of the whole scenario directory, whatever this run selected"
    echo "    these scenarios can exercise $ncited of the $ndecl tagged rules on$pages."
    echo "    $nunc have no scenario, so no implementation can pass or fail them here."
    if [ "$nstray" -gt 0 ]; then
        echo "    $nstray cited by an expectation and declared on no page given, so the"
        echo "    expectation names a rule and this run judged it against nothing:"
        awk -F'\t' 'NR==FNR { where[$1] = where[$1] ", " $2; next }
                    { printf "      [%s] %s\n", $1, substr(where[$1], 3) }' "$WHERE" "$WORK/stray.tags"
    fi
    if [ "$nunc" -gt 0 ] && [ -z "$ONLY" ]; then
        awk -F'\t' 'NR==FNR { if (!($1 in s)) s[$1]=$2; next }
                    { printf "      [%s] %s\n", $1, s[$1] }' "$DECLARED" "$WORK/uncovered.tags"
    fi
else
    echo
    echo "  coverage: UNKNOWN — no contract page was given, so what fraction of the"
    echo "            contract these scenarios reach cannot be stated. Pass --contracts."
fi

MISSING="$WORK/missing"
LISTED="$WORK/listed"
: > "$MISSING"; : > "$LISTED"
cut -f1 "$SELWHERE" | sed 's/-.*//' > "$WORK/selprefix"
sort -u "$WORK/selprefix" > "$WORK/prefixes"
LIST=${CONTRACTS_LIST:-$HERE/contracts.list}
if [ -f "$LIST" ]; then
    while read -r prefix page url; do
        case "$prefix" in ''|'#'*|'>'*) continue ;; esac
        printf '%s\n' "$prefix" >> "$LISTED"
        grep -qx "$prefix" "$WORK/prefixes" || continue
        [ -f "$CONTRACTS/$page" ] && continue
        printf '%s\t%s\t%s\n' "$prefix" "$page" "$url" >> "$MISSING"
    done < "$LIST"
fi
sort -u -o "$LISTED" "$LISTED"
nameless=$(comm -23 "$WORK/prefixes" "$LISTED")
nmissing=$(grep -c . < "$MISSING")

if [ "$nmissing" -gt 0 ] || [ -n "$nameless" ]; then
    echo
    echo "  contract pages this run needed and did not have"
    echo "    Every expectation cites a rule. Without the page that declares it the"
    echo "    step was still compared and the RULE was judged against nothing, so"
    echo "    this run cannot say the contract is kept."
    if [ "$nmissing" -gt 0 ]; then
        awk -F'\t' -v d="$CONTRACTS" 'BEGIN { print "" }
            { printf "      [%s-*] %s/%s\n", $1, d, $2
              printf "            curl -L -o %s/%s %s\n", d, $2, $3 }' "$MISSING"
    fi
    for p in $nameless; do
        echo "      [$p-*] cited here, and contracts.list names no page for it"
    done
fi

if [ -s "$WORK/undecided" ]; then
    echo
    echo "  undecided — steps no contract page settles"
    cat "$WORK/undecided"
fi

echo
echo "  $n_pass scenarios passed, $n_fail failed, $n_unreach out of reach, $n_silent asserted nothing, $n_error broke the driver"
[ "$n_fail" = 0 ] || { echo "  RESULT: FAILED"; exit 1; }
[ "$n_error" = 0 ] || { echo "  RESULT: THE DRIVER BROKE"; exit 1; }
[ "$n_unreach" = 0 ] && [ "$u" = 0 ] && [ "$nstray" = 0 ] && [ "$nmissing" = 0 ] && [ -z "$nameless" ] \
    || { echo "  RESULT: INCOMPLETE — see unreachable, unresolved and missing above"; exit 2; }
echo "  RESULT: every rule this suite can reach, your implementation keeps"
