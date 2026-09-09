#!/bin/sh
case "${1:-}" in
    --capabilities) echo "store"; exit 0 ;;
    --models) echo "abstraction.job/ranges@1 critical-ok"; exit 0 ;;
esac
n=0
while IFS= read -r line; do
    line=$(printf '%s' "$line" | tr -d '\r')
    case "$line" in ''|'#'*) continue ;; esac
    n=$((n + 1))
    case "$line" in
        orphans) answer="ok A B C" ;;
        state*)  answer="ok state=failed" ;;
        *)       answer="not a conflict" ;;
    esac
    printf '%02d %s -> %s\n' "$n" "$line" "$answer"
done < "$2"
