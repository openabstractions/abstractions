#!/bin/sh
# Literal fixture expansion must retain exact source and field comparison.
set -eu
RULE=$1
eval "$(sed -n -e '/^SEP=/p' -e '/^judge() {$/,/^}$/p' "$RULE")"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
base='http://127.0.0.1:12345/scenario&literal'
cat > "$WORK/actual" <<EOF
01 run A -> ok cp={"validators":{"source":"$base/etag/64"}}
02 run A -> ok cp={"validators":{"source":"http://wrong.invalid/etag/64"}}
03 run A -> ok cp={"validators":{"source":"$base/modified/64"}}
04 run A -> ok cp={"validators":{"source":"$base/etag/64","extra":true}}
EOF
cat > "$WORK/expected" <<'EOF'
1 ok cp={"validators":{"source":"@FIXTURE@/etag/64"}}
2 ok cp={"validators":{"source":"@FIXTURE@/etag/64"}}
3 ok cp={"validators":{"source":"@FIXTURE@/etag/64"}}
4 ok cp={"validators":{"source":"@FIXTURE@/etag/64"}}
EOF
judge "$WORK/actual" "$WORK/expected" "$base" > "$WORK/judged"
[ "$(grep -c '^held' "$WORK/judged")" = 1 ]
[ "$(grep -c '^broke' "$WORK/judged")" = 3 ]
