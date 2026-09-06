#!/usr/bin/env bash
# Produce the transcripts in docs/results/ that a machine with the toolchains
# can produce, and say plainly which it cannot.
#
#   scripts/results.sh [NAME ...]   write docs/results/NAME.txt (default: every runnable one)
#   scripts/results.sh --check      fail if a runnable transcript measures a tree
#                                   other than the one committed
#
# The first batch of transcripts was written by hand the day the runs happened,
# and the next day's runs were not, because nothing demanded them. So every
# transcript here names the tree it measured, and --check is the demand: a
# release step that calls it cannot carry a number nobody can attribute to a
# commit. A transcript produced with uncommitted changes under the directories
# it measures says so in its header and counts as stale.
#
# The transcripts no script can produce — a NAS on the LAN, a packet filter
# with root — carry a `# re-run` line naming what they need. --check lists
# them rather than passing them: a platform that was absent is UNPROVEN.

set -u
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/docs/results"
B="${ABSTRACTION_CPP_BUILD:-$ROOT/.build}"
PY="${ABSTRACTION_PYTHON:-}"
if [ -z "$PY" ]; then
    for c in "${LOCALAPPDATA:-}/Programs/Python/Python312/python.exe" python3 python; do
        command -v "$c" >/dev/null 2>&1 && { PY="$c"; break; }
    done
fi

RUNNABLE="BEHAVIOUR1 CAS-MIXED1 AWAKE1"
measures() {
    case "$1" in
        BEHAVIOUR1) echo "download job" ;;
        CAS-MIXED1) echo "cas" ;;
        AWAKE1)     echo "job" ;;
    esac
}
rerun() {
    case "$1" in
        BEHAVIOUR1) echo "REPLAY_CPP=<build>/Release/replay.exe scripts/behaviour-conformance.sh" ;;
        CAS-MIXED1) echo "CAS_CPP=<build>/cas/cpp/Release/test_cas.exe python cas/mixed.py 3" ;;
        AWAKE1)     echo "cd job/go && go test -count=1 -v -run '^TestHold' ." ;;
    esac
}
scope() {
    case "$1" in
        BEHAVIOUR1) echo "one machine, three implementations of one contract; proves agreement, not the platform" ;;
        CAS-MIXED1) echo "the host filesystem only; a network share is not exercised. macOS UNPROVEN" ;;
        AWAKE1)     echo "Windows reads the kernel's SystemExecutionState; Linux reads systemd-inhibit. macOS UNPROVEN" ;;
    esac
}

tree_line() {
    local d line=""
    for d in $1; do line="$line $d@$(git -C "$ROOT" rev-parse --short "HEAD:$d" 2>/dev/null || echo none)"; done
    [ -z "$(git -C "$ROOT" status --porcelain -- $1)" ] || line="$line +uncommitted"
    echo "${line# }"
}

state_line() {
    case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*)
        powershell -NoProfile -Command "$(cat <<'PS'
$b = Get-CimInstance Win32_Battery
$p = if ($b.BatteryStatus -eq 2) { 'AC' } elseif ($b) { 'battery' } else { 'AC, no battery' }
$c = if ($b) { ', ' + $b.EstimatedChargeRemaining + '%' } else { '' }
$plan = [regex]::Match((powercfg /getactivescheme), '\((.*)\)').Groups[1].Value
$os = Get-CimInstance Win32_OperatingSystem
"windows/amd64; $p$c, plan $plan; " + (Get-Volume -DriveLetter $env:SystemDrive[0]).FileSystem + '; ' + $os.Caption + ' ' + $os.Version
PS
)" | tr -d '\r' ;;
    Linux)
        local ps=unknown
        for f in /sys/class/power_supply/*/online; do [ -f "$f" ] && ps=$([ "$(cat "$f")" = 1 ] && echo AC || echo battery); done
        echo "linux/$(uname -m); $ps; $(stat -f -c %T "$ROOT"); $(uname -r)" ;;
    Darwin)
        echo "darwin/$(uname -m); $(pmset -g batt | head -1 | sed "s/Now drawing from //"); $(diskutil info / | sed -n 's/.*File System Personality: *//p'); $(sw_vers -productVersion)" ;;
    esac
}

scrub() {
    sed -e "s#$(cd "$ROOT" && pwd -W 2>/dev/null || echo "$ROOT")#<repo>#g" -e "s#$ROOT#<repo>#g" \
        -e "s#\([Uu]sers[/\\\\]\)${USERNAME:-$USER}#\1user#g" -e "s#$(hostname)#host#g" | tr -d '\r'
}

produce() {
    local n="$1" f="$OUT/$1.txt" raw; raw="$(mktemp)"
    case "$n" in
    BEHAVIOUR1)
        local replay="$B/Release/replay.exe"; [ -f "$replay" ] || replay="$B/replay"
        [ -f "$replay" ] || { echo "$n: c++ replay driver ABSENT under $B; two of three is not a transcript" >&2; return 1; }
        REPLAY_CPP="$replay" bash "$ROOT/scripts/behaviour-conformance.sh" > "$raw" 2>&1 ;;
    CAS-MIXED1)
        local cas="$B/cas/cpp/Release/test_cas.exe"; [ -f "$cas" ] || cas="$B/cas/cpp/test_cas"
        [ -f "$cas" ] || { echo "$n: c++ test_cas ABSENT under $B; two of three is not a transcript" >&2; return 1; }
        [ -n "$PY" ] || { echo "$n: no python; set ABSTRACTION_PYTHON" >&2; return 1; }
        CAS_CPP="$cas" "$PY" "$ROOT/cas/mixed.py" 3 > "$raw" 2>&1 ;;
    AWAKE1)
        (cd "$ROOT/job/go" && go test -count=1 -v -run '^TestHold' .) > "$raw" 2>&1 ;;
    *) echo "$n: not a transcript this script produces" >&2; return 1 ;;
    esac
    local rc=$?
    {
        echo "# $n  produced by scripts/results.sh on $(date +%Y-%m-%d), commit $(git -C "$ROOT" rev-parse --short HEAD), exit $rc"
        echo "# measures  $(tree_line "$(measures "$n")")"
        echo "# state     $(state_line)"
        echo "# re-run    $(rerun "$n")"
        echo "# scope     $(scope "$n")"
        echo
        scrub < "$raw"
    } > "$f"
    rm -f "$raw"
    echo "$f  exit $rc"
    return $rc
}

check() {
    local bad=0 n f want have
    for n in $RUNNABLE; do
        f="$OUT/$n.txt"
        [ -f "$f" ] || { echo "stale  $n: no transcript"; bad=1; continue; }
        have="$(sed -n 's/^# measures  //p' "$f")"
        want="$(tree_line "$(measures "$n")")"
        if [[ "$have" == *+uncommitted* ]]; then echo "stale  $n  measured a tree with uncommitted changes"; bad=1
        elif [ "$have" = "$want" ]; then echo "ok     $n  $have"
        else echo "stale  $n  measured $have, the tree is $want"; bad=1; fi
    done
    grep -l '^# re-run' "$OUT"/*.txt 2>/dev/null | while read -r f; do
        n="$(basename "$f" .txt)"
        case " $RUNNABLE " in *" $n "*) continue ;; esac
        echo "hand   $n  $(sed -n 's/^# re-run *//p' "$f")"
    done
    [ "$bad" = 0 ] || echo "a stale transcript is one a release would publish against code it never measured: run scripts/results.sh on a committed tree"
    return $bad
}

[ "${1:-}" = "--check" ] && { check; exit $?; }
mkdir -p "$OUT"
rc=0
for n in ${@:-$RUNNABLE}; do produce "$n" || rc=1; done
exit $rc
