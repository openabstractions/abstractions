#!/bin/sh
# Edit once, review the public diff, publish.
#
#   scripts/oa.sh prepare              generate every repository from HEAD,
#                                      compare with what is published, write the
#                                      exact public diff, record what was shown
#   scripts/oa.sh publish [repo]...    push what prepare showed, or refuse
#
# Two words, not one, because reading the diff takes time and the mirrors move
# while you read. prepare records the source revision, the destination head and
# a digest of the diff; publish fetches again and refuses unless all three still
# hold. What was read is what goes out, or nothing does.
#
# This script decides nothing. Every refusal is scripts/split.sh's or
# scripts/publish.sh's, printed unchanged, and the exit code is theirs.
set -u
cd "$(dirname "$0")/.."
OUT="${ABSTRACTION_SPLIT:-$PWD/.split}"
CANDIDATES="$OUT/.publish"
REVIEW="$CANDIDATES/review"
# Where publish.sh records an approval, asked of git rather than assumed:
# it lived under .split for one night while publish.sh wrote it beside the
# repository, and the review half of this script skipped every repository in
# silence. The one step whose whole job is to be read read nothing.
APPROVED="$(git rev-parse --path-format=absolute --git-common-dir)/publish/approved"

usage() { sed -n '2,8p' "$0" >&2; exit 2; }
[ $# -ge 1 ] || usage
word=$1
shift

prepare() {
    [ $# = 0 ] || usage
    rev=$(git rev-parse HEAD) || exit 1
    dirty=$(git status --porcelain | wc -l)
    printf '\033[1mcandidate\033[0m %s\n' "$rev"
    [ "$dirty" = 0 ] || printf '  \033[33mnote\033[0m     %s uncommitted path(s) in this tree are not in it\n' "$dirty"
    printf '\n'
    bash scripts/split.sh || exit $?
    printf '\n'

    repos=$(find "$OUT" -mindepth 1 -maxdepth 1 -type d ! -name '.*' -exec basename {} \; | sort)
    sh scripts/publish.sh --approve --verbose $repos
    rc=$?

    mkdir -p "$REVIEW"
    rm -f "$REVIEW"/*.diff
    printf '\n\033[1mpublic diff\033[0m  what each mirror receives, whole\n'
    n=0
    for r in $repos; do
        d="$CANDIDATES/$r"
        [ -f "$APPROVED/$r" ] || continue
        [ -d "$d/.git" ] || { printf '  \033[31m?\033[0m        %-26s no candidate at %s - scripts/publish.sh moved it and this script must follow\n' "$r" "$d"; continue; }
        out="$REVIEW/$r.diff"
        {
            git -C "$d" diff HEAD
            git -C "$d" status --porcelain -uall --no-renames | awk '/^\?\?/ { print substr($0, 4) }' |
            while IFS= read -r p; do git -C "$d" diff --no-index -- /dev/null "$p"; done
        } > "$out" 2>/dev/null
        add=$(grep -c '^+[^+]' "$out")
        del=$(grep -c '^-[^-]' "$out")
        printf '  %-26s +%-6s -%-6s %s\n' "$r" "$add" "$del" "${out#$PWD/}"
        n=$((n + 1))
    done
    printf '\n'
    if [ "$n" = 0 ]; then
        printf '  nothing to publish; nothing was pushed\n'
    else
        printf '  %s repository(ies) recorded; nothing was pushed\n' "$n"
        printf '  "approved" above means recorded, not consented - consent is the next word:\n'
        printf '  read the diffs, then: scripts/oa.sh publish\n'
    fi
    exit $rc
}

publish() {
    if [ $# = 0 ]; then exec sh scripts/publish.sh --push --all
    else exec sh scripts/publish.sh --push "$@"; fi
}

case "$word" in
prepare) prepare "$@" ;;
publish) publish "$@" ;;
*) usage ;;
esac
