#!/bin/sh
case "${1:-}" in
    --capabilities) echo "store transfer"; exit 0 ;;
    --models) echo "abstraction.job/ranges@1 critical-ok"; exit 0 ;;
esac
n=0
while IFS= read -r line; do
    line=$(printf '%s' "$line" | tr -d '\r')
    case "$line" in ''|'#'*) continue ;; esac
    n=$((n + 1))
    printf '%02d %s -> ok\n' "$n" "$line"
done < "$2"
