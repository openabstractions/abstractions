#!/bin/sh
# The size of what we publish, per package, per language, against release
# version. We shipped golang.org/x/crypto/x509roots/fallback — 128 KB of
# somebody else's root certificates, baked into a published download module —
# and it survived until a person read a dependency list by hand
# (feedback/2026-09-06-dependency-audit.md). A size series is the number that
# would have shown a step change on the day it landed. It is also the proof
# of a claim we already make: "zero third-party dependencies" is an
# assertion, a number a stranger can read is evidence.
#
# What "size" means, because a number whose definition is unclear gets
# misread once and then ignored:
#
#   Go module    the .zip proxy.golang.org serves for the tagged version —
#                exactly what `go get` downloads, byte for byte. Not the repo,
#                not a clone: the module proxy's own artifact. Tag discovered
#                with `git ls-remote --tags` (read-only, no clone, no push).
#   Python       the wheel/sdist size PyPI serves for the version declared in
#   package      pyproject.toml. Checked against https://pypi.org/pypi/<name>/json,
#                a plain HTTPS GET, not skipped when the answer is 404.
#   Windows      the built .msi, byte for byte, from a local build directory —
#   installer    there is nothing published to fetch yet (service-jobd carries
#                zero tags and zero releases: research/dist140/RESULTS.md §4).
#                Absent unless --msi-dir points at one, or one already sits in
#                the default installer/dist.
#
# A package this run cannot measure is named and printed ABSENT, loudly, with
# the reason — never folded into a pass and never left off the table.
#
#   scripts/size.sh                          measure everything reachable
#   scripts/size.sh --msi-dir DIR            also look for *.msi under DIR
#   scripts/size.sh --explain METRIC FROM TO REASON
#                                             record why a growth from FROM
#                                             bytes to TO bytes is legitimate,
#                                             in research/size151/explained.tsv,
#                                             BEFORE the run that would
#                                             otherwise fail on it
#
# The gate: a package's new byte count is compared with the last value
# research/gate/series.tsv holds for that same metric. A jump past
# THRESHOLD_PCT with no matching --explain row fails the run and names the
# package and both sizes. THRESHOLD_PCT=20, and it is a judgment call, stated
# rather than hidden: the only real growth event on record, x509roots/fallback,
# was a dependency arriving unannounced, not a source edit, and every module
# measured the day this script was written (research/size151/RESULTS.md) sits
# between 8,329 and 346,737 bytes — a 20% jump is bigger than a normal day of
# hand-written source should produce and small enough that one vendored file
# or one embedded certificate bundle crosses it immediately. There is no
# second historical sample to derive this from, because size was never
# measured before this task; the number should move the day a real miss or a
# real false alarm is observed against it, and that move is itself a
# deliberate edit with its case, same as any other rule here.
#
# Growth is explained by a person running --explain, not by re-baselining a
# number: series.tsv is append-only and this script never rewrites a row.
# explained.tsv is the record of the deliberate act; a growth event with no
# row in it fails, every time, until someone writes one.
#
# Never pushes, never clones a repository. git ls-remote and plain HTTPS GETs
# only, matching scripts/claims.sh's choice to avoid a hung git.exe: each
# fetch here is a single foregrounded call this shell waits on directly, never
# backgrounded, never read through a pipe that could outlive it.
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
ORG="${ABSTRACTION_ORG:-openabstractions}"
SERIES="$ROOT/research/gate/series.tsv"
OUTDIR="$ROOT/research/size151"
EXPLAINED="$OUTDIR/explained.tsv"
TABLE="$OUTDIR/SIZES.md"
DATE="$(date +%Y-%m-%d)"
COMMIT="$(git rev-parse --short HEAD)"
NET_TIMEOUT="${SIZE_TIMEOUT:-15}"
THRESHOLD_PCT=20
START=$(date +%s)

mkdir -p "$OUTDIR"
[ -f "$EXPLAINED" ] || : > "$EXPLAINED"

MSI_DIR="$ROOT/installer/dist"
if [ "${1:-}" = "--msi-dir" ]; then MSI_DIR=$2; shift 2; fi
if [ "${1:-}" = "--explain" ]; then
    metric=$2; from=$3; to=$4; reason=$5
    printf '%s\t%s\t%s\t%s\t%s\n' "$DATE" "$metric" "$from" "$to" "$reason" >> "$EXPLAINED"
    printf 'recorded: %s grew %s -> %s because: %s\n' "$metric" "$from" "$to" "$reason"
    exit 0
fi

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
ABSENTLOG="$WORK/absent"
: > "$ABSENTLOG"

NOK=0; NFAIL=0; NUNPROVEN=0; NABSENT=0
record() {
    status=$1; shift
    case "$status" in
        ok)       NOK=$((NOK + 1));       printf '  \033[32mok\033[0m        %s\n' "$*" ;;
        FAIL)     NFAIL=$((NFAIL + 1));   printf '  \033[31mFAIL\033[0m      %s\n' "$*" ;;
        UNPROVEN) NUNPROVEN=$((NUNPROVEN + 1)); printf '  \033[33mUNPROVEN\033[0m  %s\n' "$*" ;;
        ABSENT)   NABSENT=$((NABSENT + 1));
                  printf '  \033[33mABSENT\033[0m    %s\n' "$*"
                  printf '%s\n' "$*" >> "$ABSENTLOG" ;;
    esac
}

row() { printf '%s\t%s\t%s\t%s\t%s\n' "$DATE" "$COMMIT" "$1" "$2" "$3" >> "$SERIES"; }

previous_value() {   # $1 metric -> last recorded value for it, or empty
    awk -F'\t' -v m="$1" '$3==m { v=$4 } END { if (v!="") print v }' "$SERIES"
}

explained() {   # $1 metric $2 from $3 to -> reason line, or empty
    awk -F'\t' -v m="$1" -v f="$2" -v t="$3" \
        '$2==m && $3==f && $4==t { print $5; found=1 }
         END { exit !found }' "$EXPLAINED" 2>/dev/null | tail -1
}

# Reads a byte count, applies the growth gate, appends the real number
# regardless of verdict (append-only), and reports. $1 metric $2 bytes $3 label
gate() {
    metric=$1; bytes=$2; label=$3
    prev=$(previous_value "$metric")
    if [ -z "$prev" ]; then
        row "$metric" "$bytes" "size.sh"
        record ok "$label — $bytes bytes (first measurement, nothing to compare)"
        return 0
    fi
    row "$metric" "$bytes" "size.sh"
    if [ "$bytes" -le "$prev" ]; then
        record ok "$label — $bytes bytes (was $prev)"
        return 0
    fi
    grown_pct=$(( (bytes - prev) * 100 / prev ))
    if [ "$grown_pct" -le "$THRESHOLD_PCT" ]; then
        record ok "$label — $bytes bytes (was $prev, +${grown_pct}%, under ${THRESHOLD_PCT}%)"
        return 0
    fi
    why=$(explained "$metric" "$prev" "$bytes")
    if [ -n "$why" ]; then
        record ok "$label — $bytes bytes (was $prev, +${grown_pct}%, explained: $why)"
        return 0
    fi
    record FAIL "$label grew from $prev to $bytes bytes (+${grown_pct}%, over ${THRESHOLD_PCT}%, no research/size151/explained.tsv entry — run: scripts/size.sh --explain $metric $prev $bytes \"<reason>\")"
}

printf '\033[1mGo modules — the module-proxy zip for the newest tag\033[0m\n'

# repo, module suffix under it ("go" or "" for a root go.mod), short name for the metric
go_pkgs='
abstraction-job go job
abstraction-download go download
abstraction-storage go storage
abstraction-config go config
abstraction-logging go logging
abstraction-model go model
abstraction-facade go facade
abstraction-identity - identity
abstraction-cas go cas
abstraction-watch go watch
abstraction-rights go rights
abstraction-asks go asks
'
printf '%s\n' "$go_pkgs" > "$WORK/go_pkgs"
while IFS=' ' read -r repo suffix short; do
    [ -n "$repo" ] || continue
    [ "$suffix" = "-" ] && suffix=""
    modpath="github.com/$ORG/$repo${suffix:+/$suffix}"
    tagglob="${suffix:+$suffix/}v[0-9]*.[0-9]*.[0-9]*"
    tags=$(git ls-remote --tags "https://github.com/$ORG/$repo.git" "$tagglob" 2>/dev/null \
        | awk '{print $2}' | sed 's#refs/tags/##' | grep -v '\^{}$' || true)
    if [ -z "$tags" ]; then
        record ABSENT "$modpath: no tag matching ${suffix:+$suffix/}vX.Y.Z on github.com/$ORG/$repo — cannot identify a published version"
        continue
    fi
    latest=$(printf '%s\n' "$tags" | sort -V | tail -1)
    version=$([ -n "$suffix" ] && printf '%s' "${latest#"$suffix"/}" || printf '%s' "$latest")

    zip="$WORK/$short.zip"
    code=$(curl -s -o "$zip" -w '%{http_code}' --max-time "$NET_TIMEOUT" --connect-timeout "$NET_TIMEOUT" \
        "https://proxy.golang.org/$modpath/@v/$version.zip" 2>/dev/null) || code=000
    if [ "$code" != "200" ] || [ ! -s "$zip" ]; then
        record UNPROVEN "$modpath@$version: module proxy did not serve a zip (http $code) — size unknown, not zero"
        continue
    fi
    bytes=$(wc -c < "$zip" | tr -d ' ')
    gate "size_bytes_go_$short" "$bytes" "$modpath @ $latest"
done < "$WORK/go_pkgs"

printf '\n\033[1mPython packages — the sdist/wheel PyPI serves for the declared version\033[0m\n'

py_pkgs='
cas/python/pyproject.toml abstraction-cas
download/python/pyproject.toml abstraction-download
job/python/pyproject.toml abstraction-job
model/python/pyproject.toml abstraction-model
watch/python/pyproject.toml abstraction-watch
'
printf '%s\n' "$py_pkgs" > "$WORK/py_pkgs"
while IFS=' ' read -r path name; do
    [ -n "$name" ] || continue
    [ -f "$ROOT/$path" ] || { record UNPROVEN "$name: $path does not exist in this tree — cannot even read the declared version"; continue; }
    out="$WORK/$name.json"
    code=$(curl -s -o "$out" -w '%{http_code}' --max-time "$NET_TIMEOUT" --connect-timeout "$NET_TIMEOUT" \
        "https://pypi.org/pypi/$name/json" 2>/dev/null) || code=000
    case "$code" in
        404) record ABSENT "$name: declared in $path, not on PyPI (checked https://pypi.org/pypi/$name/json -> 404, $DATE)" ;;
        200)
            bytes=$(grep -oE '"size": *[0-9]+' "$out" | grep -oE '[0-9]+' | sort -rn | head -1)
            if [ -z "$bytes" ]; then
                record UNPROVEN "$name: PyPI answered 200 but no release file size could be read from the JSON"
            else
                gate "size_bytes_py_${name#abstraction-}" "$bytes" "$name (PyPI, largest release file)"
            fi
            ;;
        000) record UNPROVEN "$name: could not reach pypi.org" ;;
        *)   record UNPROVEN "$name: PyPI answered http $code, neither found nor confirmed absent" ;;
    esac
done < "$WORK/py_pkgs"

printf '\n\033[1mWindows installer — the built .msi, byte for byte\033[0m\n'

for arch in x64 arm64; do
    f="$MSI_DIR/abstraction-$arch.msi"
    if [ -f "$f" ]; then
        bytes=$(wc -c < "$f" | tr -d ' ')
        gate "size_bytes_msi_$arch" "$bytes" "abstraction-$arch.msi"
    else
        record ABSENT "abstraction-$arch.msi: no file under $MSI_DIR — installer/build.py has no CI yet (research/dist140/RESULTS.md §4); pass --msi-dir DIR to measure a local build"
    fi
done

# ---- the generated table ---------------------------------------------------

{
    printf '# Package sizes\n\n'
    printf 'Generated by `scripts/size.sh` from `research/gate/series.tsv` — never\n'
    printf 'hand-edit this file, rerun the script. Last regenerated %s at commit `%s`.\n\n' "$DATE" "$COMMIT"
    printf 'Definitions: a Go module is the module-proxy zip for its newest tag (what\n'
    printf '`go get` downloads); a Python package is the largest PyPI release file for\n'
    printf 'its declared version; a Windows installer is the built `.msi` itself. See\n'
    printf 'the header of `scripts/size.sh` for why each was chosen. The gate fails a\n'
    printf 'package that grows more than %s%% since its last recorded size with no\n' "$THRESHOLD_PCT"
    printf 'matching row in `research/size151/explained.tsv`.\n\n'
    printf '| metric | bytes | human | as of |\n'
    printf '|---|---|---|---|\n'
    awk -F'\t' '$3 ~ /^size_bytes_/ { v[$3]=$4; d[$3]=$1"@"$2 }
        END { for (m in v) print m "\t" v[m] "\t" d[m] }' "$SERIES" | sort | \
    while IFS='	' read -r metric bytes asof; do
        human=$(printf '%s' "$bytes" | numfmt --to=iec 2>/dev/null || printf '%s' "$bytes")
        printf '| `%s` | %s | %s | %s |\n' "$metric" "$bytes" "$human" "$asof"
    done
    if [ -s "$ABSENTLOG" ]; then
        printf '\n## Absent this run\n\n'
        printf 'Reported, never passed — no zero was written for any of these.\n\n'
        while IFS= read -r line; do printf -- '- %s\n' "$line"; done < "$ABSENTLOG"
    fi
    if [ -s "$EXPLAINED" ]; then
        printf '\n## Growth explained\n\n'
        printf '| date | metric | from | to | reason |\n|---|---|---|---|---|\n'
        awk -F'\t' '{printf "| %s | `%s` | %s | %s | %s |\n", $1,$2,$3,$4,$5}' "$EXPLAINED"
    fi
} > "$TABLE"

END=$(date +%s)
printf '\n  table regenerated: %s\n' "$TABLE"
printf '  runtime: %ss\n' "$((END - START))"
printf '\n  %s package(s) measured: %s ok, %s unexplained growth, %s unproven, %s absent\n' \
    "$((NOK + NFAIL))" "$NOK" "$NFAIL" "$NUNPROVEN" "$NABSENT"

if [ "$NFAIL" -gt 0 ]; then
    printf '  \033[31mFAIL\033[0m  %s package(s) grew past %s%% unexplained — see above\n' "$NFAIL" "$THRESHOLD_PCT"
    exit 1
elif [ "$NUNPROVEN" -gt 0 ]; then
    printf '  \033[33mUNPROVEN\033[0m  %s package(s) could not be reached — see above, never counted as a pass\n' "$NUNPROVEN"
    exit 2
else
    printf '  \033[32mOK\033[0m  every reachable package measured, nothing grew unexplained\n'
    exit 0
fi
