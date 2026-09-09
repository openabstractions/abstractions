#!/usr/bin/env sh
# Regenerate every generated artefact in the tree from its definition.
#
#   scripts/generate.sh                 rewrite them in place
#   scripts/generate.sh --into <dir>    write the same tree somewhere else
#   scripts/generate.sh --paths         every relative path a definition can emit
#
# scripts/generate.targets says which definition feeds which output root.
# scripts/check.sh runs --into a scratch tree and refuses any difference, so a
# definition edited without a run of this script is red rather than silent.

set -u

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TARGETS="$ROOT/scripts/generate.targets"
INTO="$ROOT"
WANT_PATHS=0

while [ $# -gt 0 ]; do
    case $1 in
        --into) [ $# -ge 2 ] || { echo "generate.sh: --into wants a directory" >&2; exit 2; }
                INTO=$2; shift 2 ;;
        --paths) WANT_PATHS=1; shift ;;
        *) echo "usage: generate.sh [--into <dir>] [--paths]" >&2; exit 2 ;;
    esac
done

# idl/gen is deliberately outside go.work — generated code links nothing, so the
# generator is its own module with no dependencies — and go run refuses to build
# a module the active workspace does not list.
gen() { (cd "$ROOT/idl/gen" && GOWORK=off go run . "$@"); }

declared() { sed 's/#.*//' "$TARGETS" | grep -v '^[[:space:]]*$'; }

[ -f "$TARGETS" ] || { echo "generate.sh: $TARGETS is missing" >&2; exit 1; }

# --paths answers "which relative paths in this tree are generated at all", which
# is what check.sh needs to spot a generated file no line declares. So it asks
# with neither the backend list nor the surface selection any target chose: a
# path a target stopped emitting is still a generated path, and a file left
# behind at one is the thing worth finding.
if [ "$WANT_PATHS" = 1 ]; then
    def=$(declared | awk 'NR==1 {print $1}')
    [ -n "$def" ] || { echo "generate.sh: generate.targets declares nothing" >&2; exit 1; }
    probe=$(mktemp -d) || exit 1
    gen "$ROOT/$def" "$probe" >/dev/null || { rm -rf "$probe"; exit 1; }
    (cd "$probe" && find . -type f) | sed 's|^\./||' | sort
    rm -rf "$probe"
    exit 0
fi

list=$(mktemp) || exit 1
declared > "$list"
status=0
made=0

while read -r def out rest; do
    if [ ! -f "$ROOT/$def" ]; then
        echo "generate.sh: $def: declared in generate.targets and not in the tree" >&2
        status=1
        continue
    fi
    if [ -z "$out" ]; then
        echo "generate.sh: $def: no output root" >&2
        status=1
        continue
    fi
    mkdir -p "$INTO/$out" || { status=1; continue; }
    if gen "$ROOT/$def" "$INTO/$out" $rest; then
        made=$((made + 1))
    else
        status=1
    fi
done < "$list"

rm -f "$list"

if [ "$made" = 0 ]; then
    echo "generate.sh: no target was generated" >&2
    status=1
fi

exit $status
