#!/bin/sh
# Exercises scripts/vision.sh and scripts/vision-index.sh against fixtures.
# It must never touch the real ledger; that is asserted before and after.
set -u
cd "$(dirname "$0")/.."
root=$(pwd)
real=$root/VISION.md

case "${VISION_FILE:-}" in
  "") ;;
  *) [ "$(cd "$(dirname "$VISION_FILE")" && pwd)/$(basename "$VISION_FILE")" != "$real" ] ||
       { echo "refusing to run: VISION_FILE is the real ledger" >&2; exit 1; } ;;
esac
before=$(cksum < "$real")
today=$(date +%Y-%m-%d)

tmp=${TMPDIR:-/tmp}/vision-test.$$
mkdir -p "$tmp" || exit 1
trap 'rm -rf "$tmp"' EXIT
pass=0; fail=0
ok()  { pass=$((pass + 1)); echo "ok   $1"; }
bad() { fail=$((fail + 1)); echo "FAIL $1${2:+ -- $2}"; }
check() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "want [$2] got [$3]"; fi; }
yes_() { if [ "$2" -eq 0 ]; then ok "$1"; else bad "$1" "${3:-}"; fi; }
no_()  { if [ "$2" -ne 0 ]; then ok "$1"; else bad "$1" "it should have refused"; fi; }

n=0
fixture() {
  n=$((n + 1)); f=$tmp/f$n.md
  { echo '# VISION'; echo; echo 'Preamble prose that must survive every command.'; echo; echo '---'; echo; } > "$f"
  cat >> "$f"
  echo "$f"
}
vision() { f=$1; shift; VISION_FILE=$f sh "$root/scripts/vision.sh" "$@" >/dev/null 2>"$tmp/err"; }
index()  { VISION_FILE=$1 sh "$root/scripts/vision-index.sh" >/dev/null 2>"$tmp/err"; }
rows()   { grep '^- L[0-9]' "$1"; }
title()  { rows "$1" | sed -n "${2}p" | sed 's/^- L[0-9]* //; s/^\*\*DEAD\*\* //; s/^`[^`]*` //'; }
at()     { grep -n "$2" "$1" | cut -d: -f1 | head -1; }
prose()  { awk '/^<!-- contents:begin/ { b = 1 } !b && NF { print } /^<!-- contents:end/ { b = 0 }' "$1"; }

# A strike rewrites its two title lines in place; every other prose line must
# come through untouched, so both sides are compared with the strike marks off.
plain()  { prose "$1" | sed 's/~~//g; s/^  \*\*Struck [0-9-]*: .*\*\*$//' | grep .; }
survives() {
  plain "$1" > "$tmp/want"; plain "$2" > "$tmp/have"
  while IFS= read -r l; do grep -qxF -- "$l" "$tmp/have" || echo miss; done < "$tmp/want" | grep -q miss &&
    return 1
  return 0
}
anchors_land() {
  rows "$1" | sed 's/^- L\([0-9]*\) .*/\1/' | while IFS= read -r a; do
    sed -n "${a}p" "$1" | grep -q '^- \(\*\*\|~~\)' || echo miss
  done | grep -q miss && return 1
  return 0
}

echo "-- the index, on titles that wrap"
f=$(fixture <<'EOF'
## Decisions

- **2026-09-07 · Wraps across
  two lines.** Body.

- **2026-09-07 · Wraps across
  three
  lines.** Body.

- **2026-09-07 · Wraps across
  four
  separate
  lines.** Body.
EOF
)
index "$f"
check "title wrapping two lines"   "Wraps across two lines" "$(title "$f" 1)"
check "title wrapping three lines" "Wraps across three lines" "$(title "$f" 2)"
check "title wrapping four lines"  "Wraps across four separate lines" "$(title "$f" 3)"
anchors_land "$f"; yes_ "every anchor lands on a lead line" $?

echo "-- the index, on awkward titles"
f=$(fixture <<'EOF'
## Decisions

- **2026-09-07 · A title with **bold** inside it.** Body.
EOF
)
index "$f"
check "nested bold closes the title early, as markdown itself does" \
  "A title with" "$(title "$f" 1)"

f=$(fixture <<'EOF'
## Decisions

- **2026-09-07 · `Ticks`, em dash — dot ·, "curly", arrow
  → there, a pipe | and a # hash.** Body.
EOF
)
index "$f"
t=$(title "$f" 1)
check "one row for the punctuation entry" "1" "$(rows "$f" | wc -l | tr -d ' ')"
check "punctuation and non-ASCII survive intact" \
  'Ticks, em dash — dot , "curly", arrow → there, a pipe | and a # hash' "$t"
case "$t" in *'`'*) bad "backticks are stripped" "$t" ;; *) ok "backticks are stripped" ;; esac

f=$(fixture <<'EOF'
## Decisions

- **2026-09-07 · This title is deliberately far longer than ninety six characters so that the index has to cut it down.** Body.
EOF
)
index "$f"
t=$(title "$f" 1)
check "a title over 96 characters is cut to 96" "96" "$(printf '%s' "$t" | awk '{ print length }')"
case "$t" in *...) ok "the cut is marked with an ellipsis" ;; *) bad "the cut is marked with an ellipsis" "$t" ;; esac

f=$(fixture <<'EOF'
## Decisions

- **A title with no date at all.** Body.
EOF
)
index "$f"
check "an entry with no date is still indexed" "A title with no date at all" "$(title "$f" 1)"
grep -q '^### Undated' "$f"; yes_ "an entry with no date is grouped as undated" $?

echo "-- the index, on awkward files"
f=$(fixture <<'EOF'
## Decisions

- **2026-09-07 · First.** Body.
- **2026-09-07 · Second.** Body.
EOF
)
index "$f"
check "two entries with no blank line between them" "2" "$(rows "$f" | wc -l | tr -d ' ')"

f=$(fixture <<'EOF'
## Decisions

- **2026-09-07 · The last line of the file.** Body.
EOF
)
cp "$f" "$tmp/eof.orig"
index "$f"
check "an entry on the last line, with a trailing newline" "1" "$(rows "$f" | wc -l | tr -d ' ')"
survives "$tmp/eof.orig" "$f"; yes_ "no prose lost, trailing newline" $?

f=$tmp/nonl.md
printf '# VISION\n\n## Decisions\n\n- **2026-09-07 · The last line, unterminated.** Body.' > "$f"
cp "$f" "$tmp/nonl.orig"
index "$f"
check "an entry on the last line, no trailing newline" "1" "$(rows "$f" | wc -l | tr -d ' ')"
survives "$tmp/nonl.orig" "$f"; yes_ "no prose lost, no trailing newline" $?

f=$(fixture <<'EOF'
## Decisions

- **2026-09-07 · One.** Body.
EOF
)
cp "$f" "$tmp/first.orig"
index "$f"
grep -q '^<!-- contents:begin' "$f"; yes_ "a file that never had a block gets one" $?
survives "$tmp/first.orig" "$f"; yes_ "no prose lost inserting the first block" $?
cp "$f" "$tmp/twice.orig"
index "$f"
check "a file that already has a block keeps exactly one" "1" "$(grep -c '^<!-- contents:begin' "$f")"
cmp -s "$tmp/twice.orig" "$f"; yes_ "the index is idempotent" $?
anchors_land "$f"; yes_ "the anchor dereferences to its entry" $?

f=$(fixture <<'EOF'
Nothing in this file is an entry.
EOF
)
index "$f"; yes_ "a file with no entries at all does not crash" $? "$(cat "$tmp/err")"

echo "-- the guard"
f=$(fixture <<'EOF'
## Decisions

<!-- contents:begin -->
- **2026-09-07 · One.** Body.
- **2026-09-07 · Two.** Body.
<!-- contents:end -->
EOF
)
cp "$f" "$tmp/swallow.orig"
index "$f"; no_ "refuses a rewrite that would swallow entries" $?
cmp -s "$tmp/swallow.orig" "$f"; yes_ "the swallowing input was left alone" $?

f=$(fixture <<'EOF'
<!-- contents:end -->
<!-- contents:begin -->

## Decisions

- **2026-09-07 · One.** Body.
EOF
)
cp "$f" "$tmp/inv.orig"
index "$f"; no_ "refuses an inverted contents block" $?
cmp -s "$tmp/inv.orig" "$f"; yes_ "the inverted input was left alone" $?

f=$(fixture <<'EOF'
<!-- contents:begin -->
<!-- contents:end -->
<!-- contents:begin -->
<!-- contents:end -->

## Decisions

- **2026-09-07 · One.** Body.
EOF
)
cp "$f" "$tmp/two.orig"
index "$f"; no_ "refuses two contents blocks" $?
cmp -s "$tmp/two.orig" "$f"; yes_ "the doubled input was left alone" $?

f=$(fixture <<'EOF'
<!-- contents:begin -->

## Decisions

- **2026-09-07 · One.** Body.
EOF
)
index "$f"; no_ "refuses an unterminated contents block" $?

echo "-- strike"
f=$(fixture <<'EOF'
## Decisions

- **2026-09-07 · The first entry, on a single line.** Body.

- **2026-09-07 · A middle entry whose title wraps over
  two lines.** Body one.
  Body two.

- **2026-09-07 · The last entry.** Body.
EOF
)
cp "$f" "$tmp/s.orig"
index "$f"
l=$(at "$f" '· The first')
vision "$f" strike "$l" "measured false"; yes_ "strikes the very first entry" $? "$(cat "$tmp/err")"
l=$(at "$f" '· The first')
check "a single-line title is closed as well as opened" \
  "- ~~**2026-09-07 · The first entry, on a single line.**~~ Body." "$(sed -n "${l}p" "$f")"
check "the strike is annotated" "  **Struck $today: measured false**" "$(sed -n "$((l + 1))p" "$f")"

l=$(at "$f" '· A middle')
vision "$f" strike "$l" "superseded"; yes_ "strikes an entry whose title wraps" $? "$(cat "$tmp/err")"
l=$(at "$f" '· A middle')
check "the wrapping title opens on its first line" \
  "- ~~**2026-09-07 · A middle entry whose title wraps over" "$(sed -n "${l}p" "$f")"
check "the wrapping title closes on its last line" \
  "  two lines.**~~ Body one." "$(sed -n "$((l + 1))p" "$f")"
check "the annotation follows the close" "  **Struck $today: superseded**" "$(sed -n "$((l + 2))p" "$f")"

l=$(at "$f" '· The last entry')
vision "$f" strike "$l" "done"; yes_ "strikes the very last entry" $? "$(cat "$tmp/err")"
survives "$tmp/s.orig" "$f"; yes_ "three strikes removed no line of prose" $?
check "the index marks all three dead" "3" "$(grep -c '^- L.*DEAD' "$f")"

cp "$f" "$tmp/again.orig"
l=$(at "$f" '· The first')
vision "$f" strike "$l" "again"; no_ "refuses to strike an entry already struck" $?
cmp -s "$tmp/again.orig" "$f"; yes_ "the refused strike changed nothing" $?
vision "$f" strike "$((l + 1))" "wrong line"; no_ "refuses to strike a body line" $?
vision "$f" strike abc "nonsense"; no_ "refuses a line number that is not a number" $?
vision "$f" strike 99999 "off the end"; no_ "refuses a line number past the end" $?
vision "$f" strike 0 "zero"; no_ "refuses line zero" $?
cmp -s "$tmp/again.orig" "$f"; yes_ "none of the four refusals changed anything" $?

f=$(fixture <<'EOF'
## Decisions

- **2026-09-07 · A title with **bold** inside it.** Body.
EOF
)
index "$f"; l=$(at "$f" '^- \*\*')
vision "$f" strike "$l" "nested"
grep -q 'nested bold' "$tmp/err"; yes_ "warns that a nested-bold title closes early" $?
check "the nested-bold strike still balances its marks" "2" \
  "$(sed -n "$(at "$f" '^- ~~')p" "$f" | grep -o '~~' | wc -l | tr -d ' ')"

f=$(fixture <<'EOF'
## Decisions

- **2026-09-07 · A reason with $dollars, `backticks` and a \n in it.** Body.
EOF
)
index "$f"; l=$(at "$f" '^- \*\*')
vision "$f" strike "$l" 'literal $HOME and \n and & stay put'
check "the reason is inserted literally" "  **Struck $today: literal \$HOME and \\n and & stay put**" \
  "$(sed -n "$(($(at "$f" '^- ~~') + 1))p" "$f")"

echo "-- add"
f=$(fixture <<'EOF'
## Decisions

- **2026-09-07 · One.** Body.
EOF
)
index "$f"; cp "$f" "$tmp/a.orig"
printf -- '- **%s · A newly added entry.** Its body.\n' "$today" > "$tmp/new.md"
vision "$f" add Decisions "$tmp/new.md"; yes_ "adds to an existing section" $? "$(cat "$tmp/err")"
check "the added entry lands first in its section" "- **$today · A newly added entry.** Its body." \
  "$(sed -n "$(($(at "$f" '^## Decisions$') + 2))p" "$f")"
survives "$tmp/a.orig" "$f"; yes_ "add removed no line of prose" $?

cp "$f" "$tmp/b.orig"
vision "$f" add Nowhere "$tmp/new.md"; no_ "refuses a section that does not exist" $?
: > "$tmp/empty.md"
vision "$f" add Decisions "$tmp/empty.md"; no_ "refuses an empty body" $?
printf '   \n\n' > "$tmp/blank.md"
vision "$f" add Decisions "$tmp/blank.md"; no_ "refuses a whitespace-only body" $?
printf -- '- **%s · Smuggled.** Body.\n<!-- contents:begin -->\n' "$today" > "$tmp/smug.md"
vision "$f" add Decisions "$tmp/smug.md"; no_ "refuses a body carrying a contents marker" $?
cmp -s "$tmp/b.orig" "$f"; yes_ "none of the four refusals changed anything" $?

printf -- '- **%s · No trailing newline here.** Body.' "$today" > "$tmp/nonl2.md"
vision "$f" add Decisions "$tmp/nonl2.md"; yes_ "adds a body with no trailing newline" $? "$(cat "$tmp/err")"
grep -q '^- \*\*.* · No trailing newline here\.\*\* Body\.$' "$f"
yes_ "the unterminated body did not merge into the next line" $?

printf -- '- **An undated addition.** Body.\n' > "$tmp/undated.md"
vision "$f" add Decisions "$tmp/undated.md"; yes_ "an entry with no date still adds" $? "$(cat "$tmp/err")"
grep -q "today's date" "$tmp/err"; yes_ "an entry with no date warns" $?

f=$(fixture <<'EOF'
## C++ and other (regex) [metacharacters]

- **2026-09-07 · One.** Body.
EOF
)
index "$f"
vision "$f" add 'C++ and other (regex) [metacharacters]' "$tmp/new.md"
yes_ "a section name full of metacharacters matches literally" $? "$(cat "$tmp/err")"

echo "-- find and dead"
f=$(fixture <<'EOF'
## Decisions

- **2026-09-07 · A findable decision.** Body.
EOF
)
index "$f"
VISION_FILE=$f sh "$root/scripts/vision.sh" find findable 2>/dev/null | grep -q findable
yes_ "find searches the contents block" $?
l=$(grep -n '^- \*\*' "$f" | cut -d: -f1)
vision "$f" strike "$l" "gone"
VISION_FILE=$f sh "$root/scripts/vision.sh" dead 2>/dev/null | grep -q 'findable decision'
yes_ "dead lists the struck entry" $?

echo
after=$(cksum < "$real")
if [ "$before" = "$after" ]; then ok "the real ledger was never touched"
else bad "the real ledger was never touched"; fi

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
