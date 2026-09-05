#!/usr/bin/env bash
# Everything that has to pass, in one command.
#
# There were sixteen scripts and no way to run them all, so every session ran
# whichever ones came to mind and none of them proved the whole thing still
# worked. The cross-language harnesses are the only evidence three
# implementations agree; a harness nobody remembers to run is not evidence.
#
#   usage: scripts/check.sh [--fast]
#     --fast   skip the C++ build and the cross-language harnesses, which need
#              a compiler and take minutes. Go and Python only.
#
# Exits non-zero if anything failed, and prints what.

set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# The layers are separate repositories. scripts/layers.sh checks them out and
# assembles .tree with the shape the harnesses were written against; everything
# below runs against that, not against this repository.
TREE="${ABSTRACTION_TREE:-$ROOT/.tree}"
if [ ! -d "$TREE" ]; then
    echo "no $TREE -- run scripts/layers.sh first" >&2
    exit 2
fi
export ABSTRACTION_TREE="$TREE"
FAST=0
[ "${1:-}" = "--fast" ] && FAST=1

PY="${ABSTRACTION_PYTHON:-}"
if [ -z "$PY" ]; then
    for c in "$LOCALAPPDATA/Programs/Python/Python312/python.exe" python3 python; do
        command -v "$c" >/dev/null 2>&1 && { PY="$c"; break; }
    done
fi

FAILED=()
pass() { printf '  \033[32mok\033[0m    %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m  %s\n' "$1"; FAILED+=("$1"); }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }

# Output is kept and only shown for what failed. A passing run should be short
# enough to read, or it will not be read.
LOGS="$(mktemp -d)"
trap 'rm -rf "$LOGS"' EXIT

run() {  # run <label> <command...>
    local label="$1"; shift
    local log="$LOGS/$(echo "$label" | tr -c 'a-zA-Z0-9' '_').log"
    if "$@" >"$log" 2>&1; then pass "$label"; else fail "$label"; echo "$log" >>"$LOGS/failed"; fi
}

section "go"
# A layer that is not on disk is a broken checkout, not something to pass
# over. Skipping quietly is how this printed PASS while testing nothing: the
# paths still named the old single tree, every layer was absent, and the only
# thing that ran was the example.
for d in job/go download/go storage/go logging/go config/go model/go \
         abstraction/go; do
    if [ -d "$TREE/$d" ]; then
        run "$d" bash -c "cd '$TREE/$d' && go build ./... && go test ./..."
    else
        fail "$d (missing from $TREE -- run scripts/layers.sh)"
    fi
done

# The example consumer lives here rather than in a layer, because it is what
# depends on them rather than one of them.
if [ -d "$ROOT/examples/pretend-lemonade" ]; then
    run "examples/pretend-lemonade" bash -c "cd '$ROOT/examples/pretend-lemonade' && go build ./..."
fi

section "python"
run "job/python"            bash -c "cd '$TREE/job/python' && '$PY' -m unittest discover -p 'test_*.py'"
run "download/python"       bash -c "cd '$TREE/download/python' && '$PY' -m unittest discover -p 'test_*.py'"
run "adopter-comfyui"      bash -c "cd '$TREE/../.layers/adopter-comfyui' && '$PY' test_node.py"

if [ "$FAST" = "1" ]; then
    section "skipped"
    echo "  c++ and the cross-language harnesses (--fast)"
else
    section "c++"
    # A missing compiler is not a failing test. Saying FAIL for it trains
    # everyone to ignore a red line, which is how a real failure gets through.
    CMAKE="$(command -v cmake 2>/dev/null || true)"
    if [ -z "$CMAKE" ]; then
        for c in "/c/Program Files/Microsoft Visual Studio/18/Community/Common7/IDE/CommonExtensions/Microsoft/CMake/CMake/bin/cmake.exe" \
                 "/c/Program Files/CMake/bin/cmake.exe"; do
            [ -x "$c" ] && { CMAKE="$c"; break; }
        done
    fi
    if [ -z "$CMAKE" ]; then
        printf '  \033[33mskip\033[0m  cmake not on PATH — set ABSTRACTION_CMAKE or run from a\n'
        printf '        developer shell. On Windows vcvars64.bat needs the VS Installer\n'
        printf '        directory on PATH first or it half-configures silently.\n'
    else
        # Configured out of tree so a failed build never leaves artefacts in the
        # repository that a later run would mistake for a good one.
        # Each C++ layer carries its own CMakeLists now; there is no root
        # project that knows about all of them any more.
        CTEST="$(dirname "$CMAKE")/ctest"
        for layer in job storage; do
            [ -f "$TREE/$layer/CMakeLists.txt" ] || continue
            B="${ABSTRACTION_CPP_BUILD:-$ROOT/.build}/$layer"
            run "cmake $layer" bash -c "'$CMAKE' -S '$TREE/$layer' -B '$B' -DCMAKE_BUILD_TYPE=Release && '$CMAKE' --build '$B' --config Release"
            run "ctest $layer" bash -c "'$CTEST' --test-dir '$B' -C Release --output-on-failure"
        done
    fi

    section "cross-language"
    run "job conformance"   bash "$ROOT/scripts/xlang-job.sh"
    run "download spec"     bash "$ROOT/scripts/spec-conformance.sh"
fi

section "result"
if [ ${#FAILED[@]} -eq 0 ]; then
    printf '  \033[32mPASS\033[0m — everything agrees\n\n'
    exit 0
fi
printf '  \033[31m%d failed:\033[0m %s\n\n' "${#FAILED[@]}" "${FAILED[*]}"
if [ -f "$LOGS/failed" ]; then
    while read -r log; do
        printf '\033[1m--- %s ---\033[0m\n' "$(basename "$log")"
        tail -25 "$log"
        echo
    done < "$LOGS/failed"
fi
exit 1
