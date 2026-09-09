#!/bin/sh
# Everything not yet committed, everywhere it is: this tree, and every agent's
# own worktree. Agents work in isolated checkouts, so their progress is
# invisible to `git status` here until their files are taken and committed.
#
#   scripts/inflight.sh          who is holding what
#   scripts/inflight.sh --diff   the same, with the changed lines
set -eu
cd "$(dirname "$0")/.."

WT=".claude/worktrees"
DIFF=0
[ "${1:-}" = "--diff" ] && DIFF=1

name_of() {   # the task an agent id was spawned for, from the ledger
    grep -m1 -F "$1" AGENTS.md 2>/dev/null |
        awk -F'|' '{ for (i=1;i<=NF;i++) if ($i ~ /[a-z]/) { gsub(/^ +| +$/,"",$i); if (length($i)>12 && $i !~ /^[0-9]/ && $i !~ /general-purpose/) { print $i; exit } } }'
}

NHOLDERS=0
NFILES=0
show() {      # $1 path, $2 label
    n=$(git -C "$1" status --porcelain 2>/dev/null | wc -l)
    [ "$n" -eq 0 ] && return 0
    NHOLDERS=$((NHOLDERS + 1))
    NFILES=$((NFILES + n))
    printf '\n\033[1m%s\033[0m  %s file(s)\n' "$2" "$n"
    git -C "$1" status --porcelain | sed 's/^/    /'
    [ "$DIFF" = 1 ] && git -C "$1" diff --stat | sed 's/^/    /'
    return 0
}

show . "this tree"

if [ -d "$WT" ]; then
    for d in "$WT"/agent-*; do
        [ -e "$d/.git" ] || continue
        id=${d##*/agent-}
        task=$(name_of "$id")
        show "$d" "agent ${id} ${task:+— $task}"
    done
fi

printf '\nA worktree with files listed is an agent still working, or one whose\n'
printf 'files were never taken. Both look the same here; the ledger says which.\n\n'
if [ "$NHOLDERS" -eq 0 ]; then
    printf '  \033[32mOK\033[0m  nothing uncommitted anywhere\n'
else
    printf '  \033[33mIN FLIGHT\033[0m  %s file(s) held by %s tree(s) — commit or take them before staging anything\n' \
           "$NFILES" "$NHOLDERS"
fi
