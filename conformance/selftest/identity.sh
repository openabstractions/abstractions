#!/bin/sh
case "${1:-}" in
    --capabilities) echo "identity"; exit 0 ;;
esac
n=0
while IFS= read -r line; do
    line=$(printf '%s' "$line" | tr -d '\r')
    case "$line" in ''|'#'*) continue ;; esac
    n=$((n + 1))
    case "$line" in
        ladder) answer="ok none claimed pid bound kernel signed" ;;
        *)      answer="unknown-op" ;;
    esac
    printf '%02d %s -> %s\n' "$n" "$line" "$answer"
done < "$2"
