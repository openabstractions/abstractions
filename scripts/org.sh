#!/bin/sh
# A real checkout of every openabstractions repository, in one findable place,
# and one status line each. The generated trees under .split/ are not git
# repositories and .split/.publish is hidden; neither is somewhere to work.
#
#   scripts/org.sh            status of every repository
#   scripts/org.sh --sync     clone what is missing, fetch the rest
#
# It never pushes and never changes a working tree. Uncommitted work is yours.
set -eu

DIR="${ABSTRACTION_ORG_DIR:-$HOME/Documents/openabstractions}"
ORG="${ABSTRACTION_ORG:-openabstractions}"
SYNC=0
[ "${1:-}" = "--sync" ] && SYNC=1

repos="abstractions abstraction-job abstraction-download abstraction-storage
abstraction-config abstraction-logging abstraction-model abstraction-facade
abstraction-identity abstraction-cas abstraction-asks abstraction-watch
abstraction-rights service-jobd addon-synology adopter-comfyui docker-jobd
polite-monitor research appcontainer-notes .github"

mkdir -p "$DIR"
printf '\033[1m%s\033[0m\n\n' "$DIR"
printf '  %-24s %-8s %-8s %s\n' repository local remote state

NTOTAL=0
NCLEAN=0
NMISSING=0
for r in $repos; do
    NTOTAL=$((NTOTAL + 1))
    d="$DIR/$r"
    if [ ! -d "$d/.git" ]; then
        [ "$SYNC" = 1 ] || { NMISSING=$((NMISSING + 1))
            printf '  %-24s %-8s %-8s not cloned — run --sync\n' "$r" "-" "-"; continue; }
        git clone -q "git@github.com:$ORG/$r.git" "$d" 2>/dev/null || { NMISSING=$((NMISSING + 1))
            printf '  %-24s %-8s %-8s cannot reach over SSH\n' "$r" "-" "-"; continue; }
    fi
    [ "$SYNC" = 1 ] && git -C "$d" fetch -q origin 2>/dev/null

    branch=$(git -C "$d" rev-parse --abbrev-ref HEAD 2>/dev/null || echo '?')
    dirty=$(git -C "$d" status --porcelain 2>/dev/null | wc -l)
    ahead=$(git -C "$d" rev-list --count '@{u}..HEAD' 2>/dev/null || echo 0)
    behind=$(git -C "$d" rev-list --count 'HEAD..@{u}' 2>/dev/null || echo 0)

    state=""
    [ "$dirty" -gt 0 ]  && state="$state${dirty} uncommitted  "
    [ "$ahead" -gt 0 ]  && state="$state${ahead} unpushed  "
    [ "$behind" -gt 0 ] && state="$state${behind} behind  "
    [ -z "$state" ] && state="clean"
    [ "$state" = clean ] && NCLEAN=$((NCLEAN + 1))

    printf '  %-24s %-8s %-8s %s\n' "$r" "$branch" "origin" "$state"
done

printf '\n  %s of %s clean, %s not here; nothing was pushed\n' \
       "$NCLEAN" "$NTOTAL" "$NMISSING"
if [ "$NCLEAN" = "$NTOTAL" ]; then printf '  \033[32mOK\033[0m  every repository matches its remote\n'
else printf '  \033[33mATTENTION\033[0m  %s repository(ies) hold work or are missing — the lines above say which\n' \
            "$((NTOTAL - NCLEAN))"; fi
printf '  work in %s; this script never pushes\n' "$DIR"
