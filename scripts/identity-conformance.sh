#!/usr/bin/env bash
# Do two implementations group model names the same way?
#
# A routing decision made from a name is only safe if every host-facing process
# on the machine draws the same boundary. Go decides for the tools; Python
# decides for the broker and for ComfyUI. If they disagree by one token, two
# applications each believe they are sharing a model with the other and the
# machine holds two.
#
# The fixture is what the four hosts on one laptop actually emitted, not names
# anybody invented.
#
#   usage: identity-conformance.sh

set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

PY="${PY:-py}"
command -v "$PY" >/dev/null 2>&1 || PY=python3
command -v "$PY" >/dev/null 2>&1 || PY=python

FIXTURE="$ROOT/model/testdata/identifiers.tsv"

(cd "$ROOT/model/go" && go run ./identity/cmd/identify) < "$FIXTURE" > "$WORK/go.txt"
(cd "$ROOT/model/python" && "$PY" identify.py) < "$FIXTURE" > "$WORK/py.txt"

grep -v '^#' "$FIXTURE" | grep -v '^$' | cut -f2,3 > "$WORK/want.txt"

n=$(wc -l < "$WORK/want.txt")
echo "identity: $n identifiers from lemonade, lm studio, ollama and comfyui"

fail=0
if ! diff -u "$WORK/want.txt" "$WORK/go.txt"; then echo "  FAIL  go disagrees with the fixture"; fail=1; fi
if ! diff -u "$WORK/want.txt" "$WORK/py.txt"; then echo "  FAIL  python disagrees with the fixture"; fail=1; fi
if ! diff -u "$WORK/go.txt" "$WORK/py.txt"; then echo "  FAIL  go and python disagree"; fail=1; fi

if [ "$fail" = 0 ]; then
  echo "  PASS  go and python agree with the fixture, byte for byte"
fi
exit "$fail"
