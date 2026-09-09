#!/bin/sh
# What is in this tree that git does not track, and what it costs.
#
#   scripts/housekeep.sh            what is here and how big
#   scripts/housekeep.sh --record   refresh research/ignored-repos.tsv
#
# Four gitignored directories hold 96% of the files in this working tree.
# Every full-tree walk crosses them, which is why the gate and the split are
# slow. Nothing here is deleted by this script.
set -eu
cd "$(dirname "$0")/.."

tracked=$(git ls-files | wc -l)
printf '\n  %-22s %s\n' "tracked by git" "$tracked files"

for d in forks vault .opencode .build .split .claude .gotest; do
    [ -d "$d" ] || continue
    n=$(find "$d" -type f 2>/dev/null | wc -l)
    printf '  %-22s %s files%s\n' "$d/" "$n" \
        "$(git check-ignore -q "$d" 2>/dev/null && echo '  (gitignored)' || echo '  NOT IGNORED')"
done

printf '\n  restore list: research/ignored-repos.tsv\n'

[ "${1:-}" = "--record" ] || exit 0

tmp=$(mktemp)
head -4 research/ignored-repos.tsv > "$tmp"
for d in forks/* vault; do
    [ -e "$d/.git" ] || continue
    printf '%s\t%s\t%s\t%s\t\n' "$d" \
        "$(git -C "$d" remote get-url origin 2>/dev/null)" \
        "$(git -C "$d" rev-parse --short HEAD 2>/dev/null)" \
        "$(find "$d" -type f 2>/dev/null | wc -l)" >> "$tmp"
done
mv "$tmp" research/ignored-repos.tsv
printf '  recorded\n'
