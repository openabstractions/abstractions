#!/bin/sh
# The only sanctioned way to change VISION.md. Hand-editing it lost 90 lines on
# 2026-09-05 and nearly lost the whole file on 2026-09-07.
#
#   vision.sh add <section> <file>...        insert one or more entries, newest first
#   vision.sh strike <line> [--by <id>] <reason>
#                                             strike an entry in place, never delete it
#   vision.sh move <line> <section>          refile one entry, whole and unchanged
#   vision.sh section <name>                 add a new top-level section, once
#   vision.sh show [<date>] "<fragment>"     print one whole entry, with its status
#   vision.sh dead                           list every struck entry
#   vision.sh find <text>                    search the contents block
#
# Set VISION_FILE to point it at a fixture instead of the real ledger.
# Every command that writes regenerates the contents block and refuses to
# produce a file with fewer entries or fewer lines than it was given.
#
# A strike marker on the entry says whether it is live or struck. The heading it
# sits under is reported beside that as a separate fact, never as the status: a
# live decision filed under "Struck out" read as frozen, and was retired by the
# instrument whose job was to stop that.
set -eu
cd "$(dirname "$0")/.."

src=${VISION_FILE:-VISION.md}
[ -f "$src" ] || { echo "no such file: $src" >&2; exit 1; }
today=$(date +%Y-%m-%d)

count() { grep -c '^- \(\*\*\|~~\)' "$1" || true; }

install_or_die() {
  new=$1 budget=${2:-0}
  if [ "$(count "$new")" -lt "$(count "$src")" ] || [ "$(wc -l < "$new")" -lt "$(wc -l < "$src")" ]; then
    echo "refusing to write: the result is smaller than the original" >&2
    exit 1
  fi
  touched=$(diff "$src" "$new" | grep -c '^<' || true)
  if [ "$touched" -gt "$budget" ]; then
    echo "refusing to write: $touched original lines would change, at most $budget expected" >&2
    exit 1
  fi
  cp "$new" "$src"
  VISION_FILE=$src sh scripts/vision-index.sh
}

usage() { sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

section_of() {
  awk -v s="$1" '
    /^## / { h = substr($0, 4) }
    NR == s { print h; exit }
  ' "$src" | sed -e 's/ — .*//' -e 's/ - .*//' -e 's/,.*//'
}

entry_bounds() {
  awk -v want="$1" '
    function boundary() { return /^## / || /^- (\*\*|~~)/ || (NF > 0 && !/^[ \t]/) }
    {
      if (prev > 0 && end[prev] == 0 && boundary()) end[prev] = NR - 1
      if (/^## /) prev = 0
      else if (/^- (\*\*|~~)/) { n++; start[n] = NR; end[n] = 0; prev = n }
    }
    END {
      if (prev > 0 && end[prev] == 0) end[prev] = NR
      for (i = 1; i <= n; i++) if (start[i] == want) { print start[i], end[i]; exit }
    }
  ' "$src"
}

case "${1:-}" in
add)
  [ $# -ge 3 ] || usage
  section=$2; shift 2
  for body in "$@"; do
    [ -f "$body" ] || { echo "no such file: $body" >&2; exit 1; }
    [ -s "$body" ] && grep -q '[^[:space:]]' "$body" || { echo "the entry is empty: $body" >&2; exit 1; }
    ! grep -q '^<!-- contents:' "$body" || { echo "the entry contains a contents marker" >&2; exit 1; }
    grep -q "^- \*\*$today" "$body" || echo "warning: the entry does not open with today's date" >&2
  done
  at=$(awk -v s="## $section" '$0 == s { print NR; exit }' "$src")
  [ -n "$at" ] || { echo "no section: $section" >&2; exit 1; }
  work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
  head -n "$at" "$src" > "$work/out"
  n=$#
  while [ "$n" -ge 1 ]; do
    eval "body=\${$n}"
    echo >> "$work/out"
    awk 1 "$body" >> "$work/out"
    n=$((n - 1))
  done
  tail -n +$((at + 1)) "$src" >> "$work/out"
  install_or_die "$work/out"
  ;;
section)
  [ $# -eq 2 ] || usage
  name=$2
  ! grep -q "^## $name\$" "$src" || { echo "section exists: $name" >&2; exit 1; }
  work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
  cp "$src" "$work/out"
  {
    echo
    echo "## $name"
    echo
    echo "Cases behind a \`CLAUDE.md\` rule. Not under the ratchet: a rule answers to"
    echo "evidence and reopens whenever the evidence says so. A rule that changes"
    echo "strikes its case here and adds the new one in the same commit."
  } >> "$work/out"
  install_or_die "$work/out"
  echo "added section: $name"
  ;;
strike)
  [ $# -ge 3 ] || usage
  line=$2; shift 2
  by=""
  if [ "${1:-}" = "--by" ]; then
    [ $# -ge 3 ] || usage
    by=$2; shift 2
  fi
  reason=$*
  [ -n "$reason" ] || usage
  case "$line" in *[!0-9]* | "") echo "not a line number: $line" >&2; exit 1 ;; esac
  [ "$line" -ge 1 ] && [ "$line" -le "$(wc -l < "$src")" ] || { echo "line $line is outside the file" >&2; exit 1; }
  head=$(sed -n "${line}p" "$src")
  case "$head" in
    "- **"*) ;;
    "- ~~"*) echo "already struck at line $line" >&2; exit 1 ;;
    *) echo "line $line is not the first line of an entry" >&2; exit 1 ;;
  esac
  close=$(awk -v s="$line" '
    NR < s { next }
    NR > s && /^[[:space:]]*$/ { exit }
    { t = t " " $0; if (t ~ /\*\*[^*]*\*\*/) { print NR; exit } }
  ' "$src")
  [ -n "$close" ] || { echo "cannot find where the entry title ends" >&2; exit 1; }
  marks=$(sed -n "${line},${close}p" "$src" | grep -o '\*\*' | wc -l | tr -d ' ')
  [ "$marks" -le 2 ] || echo "warning: the title holds nested bold, so it closes early here and in markdown too" >&2
  work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
  reason=$reason by=$by awk -v s="$line" -v e="$close" -v d="$today" '
    function annotate(l, n,   out, p) {
      while (n-- > 1) { p = index(l, "**"); out = out substr(l, 1, p + 1); l = substr(l, p + 2) }
      p = index(l, "**")
      return out substr(l, 1, p + 1) "~~" substr(l, p + 2)
    }
    NR == e { $0 = annotate($0, (e == s) ? 2 : 1) }
    NR == s { sub(/^- \*\*/, "- ~~**") }
    { print }
    NR == e {
      line = sprintf("  **Struck %s: %s**", d, ENVIRON["reason"])
      if (ENVIRON["by"] != "") line = line " \xe2\x86\x92 superseded by: " ENVIRON["by"]
      print line
    }
  ' "$src" > "$work/out"
  install_or_die "$work/out" 2
  echo "struck line $line"
  ;;
move)
  [ $# -eq 3 ] || usage
  line=$2; section=$3
  case "$line" in *[!0-9]* | "") echo "not a line number: $line" >&2; exit 1 ;; esac
  [ "$line" -ge 1 ] && [ "$line" -le "$(wc -l < "$src")" ] || { echo "line $line is outside the file" >&2; exit 1; }
  case "$(sed -n "${line}p" "$src")" in
    "- **"* | "- ~~"*) ;;
    *) echo "line $line is not the first line of an entry" >&2; exit 1 ;;
  esac
  bounds=$(entry_bounds "$line")
  [ -n "$bounds" ] || { echo "cannot find where the entry ends" >&2; exit 1; }
  end=${bounds#* }
  [ "$(section_of "$line")" != "$section" ] || { echo "already in section: $section" >&2; exit 1; }
  work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
  sed -n "${line},${end}p" "$src" |
    awk 'NF { last = NR } { l[NR] = $0 } END { for (i = 1; i <= last; i++) print l[i] }' > "$work/entry"
  sed "${line},${end}d" "$src" > "$work/cut"
  # A section name also appears as a heading inside the generated contents, and
  # an entry parked in there is an entry nothing can find again.
  at=$(awk -v s="## $section" '
    /^<!-- contents:begin/ { b = 1 }
    /^<!-- contents:end/ { b = 0; next }
    !b && $0 == s { print NR; exit }
  ' "$work/cut")
  [ -n "$at" ] || { echo "no section: $section" >&2; exit 1; }
  { head -n "$at" "$work/cut"; echo; cat "$work/entry"; tail -n +$((at + 1)) "$work/cut"; } > "$work/out"
  install_or_die "$work/out" $((end - line + 1))
  echo "moved line $line to $section"
  ;;
dead)
  grep -n '^- ~~' "$src" | cut -c1-160
  ;;
find)
  [ $# -eq 2 ] || usage
  awk '/^<!-- contents:begin/,/^<!-- contents:end/' "$src" | grep -i -- "$2" || echo "nothing in the contents matches"
  ;;
show)
  case $# in
    2) date=""; frag=$2 ;;
    3) date=$2; frag=$3 ;;
    *) usage ;;
  esac
  contents=$(awk '/^<!-- contents:begin/,/^<!-- contents:end/' "$src")
  pool=$(printf '%s\n' "$contents" | awk -v want="$date" '
    /^### / { active = (want == "" || $2 == want); next }
    active && /^- L[0-9]/ { print }
  ')
  matches=$(printf '%s\n' "$pool" | grep -i -- "$frag" || true)
  n=$(printf '%s\n' "$matches" | grep -c . || true)
  if [ "$n" -eq 0 ]; then
    echo "no entry matches${date:+ $date}: $frag" >&2
    exit 1
  fi
  if [ "$n" -gt 1 ]; then
    echo "ambiguous, $n candidates:" >&2
    printf '%s\n' "$matches" | cut -c1-160 >&2
    exit 1
  fi
  start=$(printf '%s\n' "$matches" | sed -n 's/^- L\([0-9]*\).*/\1/p')
  bounds=$(entry_bounds "$start")
  end=${bounds#* }
  section=$(section_of "$start")
  body=$(sed -n "${start},${end}p" "$src")
  superseded=""
  struck=0
  case "$(printf '%s\n' "$body" | sed -n '1p')" in "- ~~"*) struck=1 ;; esac
  if [ "$struck" = 0 ]; then
    status="live"
  else
    strikeline=$(printf '%s\n' "$body" | grep -m1 '\*\*Struck [0-9-]*: ' || true)
    if [ -n "$strikeline" ]; then
      reason=$(printf '%s\n' "$strikeline" | sed -e 's/^[[:space:]]*\*\*Struck \([0-9-]*\): //' -e 's/\*\*.*$//' -e 's/[[:space:]]*$//')
      when=$(printf '%s\n' "$strikeline" | sed -n 's/^[[:space:]]*\*\*Struck \([0-9-]*\):.*/\1/p')
      status="struck $when: $reason"
      superseded=$(printf '%s\n' "$strikeline" | sed -n 's/.*superseded by: //p')
    else
      status="struck (undated)"
    fi
  fi
  echo "$status"
  echo "section: $section"
  [ "$struck" = 1 ] || [ "$section" != "Struck out" ] ||
    echo "the heading disagrees: it is filed under Struck out and carries no strike marker, so it is live"
  [ -z "$superseded" ] || echo "superseded by: $superseded"
  echo
  printf '%s\n' "$body"
  ;;
*)
  usage
  ;;
esac
