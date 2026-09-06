#!/usr/bin/env bash
# Whether a stranger can obtain each implementation through its own front door.
#
# spec-conformance.sh proves three implementations agree about a record. Nothing
# ever proved anyone could get them: the Go module resolves, the C++ package
# resolves from a prefix, and Python was a file copied into an adopter with a
# byte-diff test to survive the copying. This harness is three projects under
# scripts/obtain/, each built OUTSIDE this tree with nothing but its ecosystem's
# install command, each printing one line. The lines must match. A door that
# cannot be tried here is named, never passed.
#
#   go      go get github.com/openabstractions/abstraction-download/go     the module proxy
#   python  pip install abstraction-download                              wheels built from this tree
#   cpp     find_package(abstraction CONFIG REQUIRED)                     a prefix installed from this tree
#
# Go reaches the real front door and gets what is PUBLISHED, which is why its
# line can lag the tree. Python and C++ have no published artefact, so the
# harness builds one and installs from it -- the same distance from the real
# door on both sides, and the same distance conformance keeps from a real
# download.
#
#   usage: obtain-conformance.sh [go|python|cpp ...]     default: all three
#   env:   PY, CMAKE

set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DOORS="${*:-go python cpp}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/lines"

pass=0; fail=0; published=""
ok()  { echo "  PASS  $1"; pass=$((pass+1)); }
bad() { echo "  FAIL  $1"; fail=$((fail+1)); }
ABSENT=""
absent() { ABSENT="$ABSENT
    $1: ABSENT — $2"; }
win() { cygpath -w "$1" 2>/dev/null || echo "$1"; }
last() { { grep -m3 -iE 'error|refused|cannot|no matching' "$1" || tail -3 "$1"; } | tr '\n' ' ' | cut -c1-300; }

PY="${PY:-${ABSTRACTION_PYTHON:-}}"
if [ -z "$PY" ]; then
    for c in "${LOCALAPPDATA:-}/Programs/Python/Python312/python.exe" python3 python; do
        command -v "$c" >/dev/null 2>&1 && { PY="$c"; break; }
    done
fi
CMAKE="${CMAKE:-$(command -v cmake 2>/dev/null || true)}"
if [ -z "$CMAKE" ]; then
    for c in "/c/Program Files/Microsoft Visual Studio/18/Community/Common7/IDE/CommonExtensions/Microsoft/CMake/CMake/bin/cmake.exe" \
             "/c/Program Files/CMake/bin/cmake.exe"; do
        [ -x "$c" ] && { CMAKE="$c"; break; }
    done
fi

echo "obtainability"

door_go() {
    command -v go >/dev/null 2>&1 || { absent go "no go on PATH"; return; }
    local d="$WORK/go" log="$WORK/go.log"
    mkdir -p "$d" && cp "$ROOT/scripts/obtain/go/main.go" "$d/"
    (
        cd "$d" && export GOWORK=off GOFLAGS= && go mod init stranger &&
        go get github.com/openabstractions/abstraction-download/go@latest \
               github.com/openabstractions/abstraction-job/go@latest &&
        go run . > "$WORK/lines/go"
    ) > "$log" 2>&1
    if [ -s "$WORK/lines/go" ]; then
        published="$published go"
        ok "go: go get $(sed -n 's/^\t*\(github.com\/openabstractions\/abstraction-download\/go\) \(v[^ ]*\).*/\1@\2/p' "$d/go.mod") — the published module"
    elif grep -qiE 'dial tcp|no such host|lookup .*: |timeout|connection refused|unreachable' "$log"; then
        absent go "the module proxy is unreachable: $(last "$log")"
    else
        bad "go: $(last "$log")"
    fi
}

door_python() {
    [ -n "$PY" ] && "$PY" -c 'import pip' >/dev/null 2>&1 || { absent python "no python with pip"; return; }
    local log="$WORK/python.log" src="$WORK/pysrc" wheels="$WORK/wheels" venv="$WORK/venv" d="$WORK/python"
    mkdir -p "$src" "$wheels" "$d" && cp "$ROOT/scripts/obtain/python/main.py" "$d/"
    local isolate="--no-build-isolation"
    "$PY" -c 'import setuptools' >/dev/null 2>&1 || isolate=""
    for layer in job download model; do
        [ -f "$ROOT/$layer/python/pyproject.toml" ] || { bad "python: $layer/python has no pyproject.toml — nothing to install"; return; }
        mkdir -p "$src/$layer" && cp "$ROOT/$layer/python/pyproject.toml" "$ROOT/$layer/python/"*.py "$src/$layer/"
        "$PY" -m pip wheel -q --no-deps $isolate -w "$wheels" "$src/$layer" > "$log" 2>&1 \
            || { bad "python: building the $layer wheel: $(last "$log")"; return; }
    done
    "$PY" -m venv "$venv" > "$log" 2>&1 || { bad "python: venv: $(last "$log")"; return; }
    local vpy="$venv/bin/python"; [ -x "$vpy" ] || vpy="$venv/Scripts/python.exe"
    if "$vpy" -m pip install -q --no-index --find-links "$wheels" abstraction-download abstraction-model > "$log" 2>&1 \
       && (cd "$d" && "$vpy" main.py > "$WORK/lines/python") 2>> "$log" && [ -s "$WORK/lines/python" ]; then
        ok "python: pip install abstraction-download abstraction-model — $(ls "$wheels" | tr '\n' ' ')"
    else
        bad "python: pip install --find-links <wheels> abstraction-download: $(last "$log")"
    fi
}

door_cpp() {
    [ -n "$CMAKE" ] || { absent cpp "no cmake on PATH (set CMAKE)"; return; }
    local log="$WORK/cpp.log" prefix="$WORK/prefix" build="$WORK/cpp-build" d="$WORK/cpp"
    mkdir -p "$d" && cp "$ROOT/scripts/obtain/cpp/CMakeLists.txt" "$ROOT/scripts/obtain/cpp/main.cpp" "$d/"
    # Its own build, never the gate's .build: with several agents in one tree
    # that directory is mid-rebuild as often as not, and a prefix installed from
    # it names libraries that are not there yet.
    "$CMAKE" -S "$ROOT" -B "$build" -DCMAKE_BUILD_TYPE=Release -DABSTRACTION_BUILD_TESTS=OFF -DABSTRACTION_BUILD_TOOLS=OFF > "$log" 2>&1 \
        || { bad "cpp: configuring this tree: $(last "$log")"; return; }
    "$CMAKE" --build "$build" --config Release > "$log" 2>&1 \
        || { bad "cpp: building the prefix from this tree: $(last "$log")"; return; }
    "$CMAKE" --install "$build" --config Release --prefix "$(win "$prefix")" > "$log" 2>&1 \
        || { bad "cpp: cmake --install: $(last "$log")"; return; }
    if { "$CMAKE" -S "$d" -B "$d/build" -DCMAKE_BUILD_TYPE=Release -DCMAKE_PREFIX_PATH="$(win "$prefix")" \
         && "$CMAKE" --build "$d/build" --config Release; } > "$log" 2>&1; then
        local exe="$d/build/Release/stranger.exe"; [ -f "$exe" ] || exe="$d/build/stranger"
        if (cd "$d" && "$exe" > "$WORK/lines/cpp") 2>> "$log" && [ -s "$WORK/lines/cpp" ]; then
            ok "cpp: find_package(abstraction 0.1 CONFIG REQUIRED) from a prefix installed by this tree"
        else
            bad "cpp: stranger built and did not run: $(last "$log")"
        fi
    else
        bad "cpp: find_package(abstraction) against the prefix: $(last "$log")"
    fi
}

for door in $DOORS; do "door_$door"; done

echo
present="$(for f in "$WORK/lines"/*; do [ -s "$f" ] && basename "$f"; done | tr '\n' ' ' | sed 's/ $//')"
if [ -n "$present" ]; then
    # Text-mode stdout on Windows ends the line \r\n from C++ and Python and \n
    # from Go. The line is what is compared, not the platform's newline.
    sed -i 's/\r$//' "$WORK/lines"/*
    for l in $present; do echo "  $l: $(cat "$WORK/lines/$l")"; done
    distinct="$(for l in $present; do cat "$WORK/lines/$l"; done | sort -u | wc -l)"
    if [ "$distinct" -eq 1 ]; then
        ok "one line from $(echo $present | wc -w) front doors"
    else
        bad "$distinct different lines from $(echo $present | wc -w) front doors"
    fi
fi
# Two of the three doors open onto an artefact this run built. That is the
# distance conformance keeps from a real download, and the same distance an
# adopter cannot cross: nothing Python or C++ is published anywhere.
echo "  doors: $(echo $present | wc -w) of $(echo $DOORS | wc -w) open, $(echo $published | wc -w) published:${published:- none}"

if [ -n "$ABSENT" ]; then
    echo "========================================"
    echo "  ABSENT — could not be tried here, which is not the same as working$ABSENT"
    echo "========================================"
fi
echo "----------------------------------------"
echo "  $pass passed, $fail failed — doors: ${present:-none}"
[ "$fail" -eq 0 ]
