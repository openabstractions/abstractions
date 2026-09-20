"""Measure the cost of one framed call from Go, C++, Python and JavaScript at an isolated runtime.

--measure builds the openabstractions runtime and one probe per client, starts
`openabstractions serve runtime --isolated <name> --state-dir <temp>` and runs
each probe as its own process. A probe makes warmup + calls identical
abstraction.facade/caller@1 Observe calls at the runtime endpoint and prints

  PERCALL <client> calls=<n> first=<ms> p50=<ms> p90=<ms> p99=<ms> max=<ms> code=<proof>

first is the process's first call; the percentiles cover the calls after
warmup. code is the proof the runtime reported for the probe's own code
signature on its first call. Clients: go (listen.FrameClient), cpp, python and
rust (the shared abstraction_ipc library), javascript-native (the Node-API
addon) and javascript-pipe (the test-only node:net connector).

Each probe runs twice where its client supports sessions (FRAMING.md
"Sessions"): <client> opens a connection per call, <client>-session keeps one.

--gate fails when a p50 passes its bound: on Windows, Python or
javascript-native on a connection per call above max(GATE_FLOOR_MS,
GATE_FACTOR x the faster of Go and C++); on a session, Go, C++ or Rust above
SESSION_NATIVE_BOUND_MS for the platform, or Python or javascript-native above
SESSION_BINDING_BOUND_MS.

On Windows the C++ probe and the Node addon certify MSVC: run from a vcvars64
developer environment. The Node addon needs Node headers and node.lib already
present in --node-sdk (python js_services/run.py --run fetches them once); this
runner fetches nothing. Build trees stay under .build/.
"""
import argparse
from contextlib import ExitStack
import os
import queue
import re
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path
HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
from workspace import (ROOT, environment, cmake_for, certify_compiler, build_root, layer, resolved_executable,
                       source_revision, dry_run_stop, DRY_RUN_HELP, CMAKE_BUILD_TYPE_RELEASE)

GATE_FLOOR_MS = 10.0
GATE_FACTOR = 3.0
# Steady-state session bounds, about three times the p50 measured 2026-09-17
# on a loaded laptop (Windows 0.15-0.17 ms; WSL, whose system calls cost tens of
# microseconds, higher), so a regression fails and load does not.
SESSION_NATIVE_BOUND_MS = {"win32": 0.6, "linux": 1.5}
SESSION_BINDING_BOUND_MS = 3.0
WINDOWS = os.name == "nt"
EXE = ".exe" if WINDOWS else ""
PERCALL = re.compile(r"(?m)^PERCALL (\S+) calls=(\d+) first=([0-9.]+) p50=([0-9.]+) p90=([0-9.]+) p99=([0-9.]+) max=([0-9.]+) code=(\S*)\r?$")

p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
p.add_argument("--measure", action="store_true", help="build the runtime and probes, run every probe and print PERCALL lines")
p.add_argument("--breakdown", action="store_true", help="time each server and client step of a Go framed call on a fresh connection, on one bound connection, and on a session served by listen.Sessions (breakdown/main.go); prints STEP lines")
p.add_argument("--calls", type=int, default=300, help="measured calls per probe (default 300)")
p.add_argument("--warmup", type=int, default=5, help="calls per probe before measuring (default 5)")
p.add_argument("--node", default="node", help="Node.js executable; an empty value skips both JavaScript probes")
p.add_argument("--node-sdk", type=Path, default=ROOT/".build"/"node-sdk", help="node-gyp devdir holding <version>/include/node (and <version>/x64/node.lib on Windows); without it only javascript-pipe runs")
p.add_argument("--no-cpp", action="store_true", help="skip the C++ probe")
p.add_argument("--no-python", action="store_true", help="skip the Python probe")
p.add_argument("--no-rust", action="store_true", help="skip the Rust probe (abstraction-ipc FrameTransport with an encoded request)")
p.add_argument("--cmake", help="CMake executable (Windows: Visual Studio's, from a vcvars64 environment)")
p.add_argument("--results", type=Path, help="append the PERCALL lines, each suffixed with the platform, to this file")
p.add_argument("--gate", action="store_true", help="fail when a gated p50 exceeds its stated bound (see above)")
p.add_argument("--dry-run", action="store_true", help=DRY_RUN_HELP)
a = p.parse_args()
if not (a.measure or a.breakdown or a.dry_run):
    p.print_help()
    raise SystemExit(0)
source_revision()
if a.dry_run:
    dry_run_stop("percall", a)
if a.calls < 1 or a.warmup < 0:
    p.error("--calls must be positive and --warmup non-negative")
if sys.platform == "darwin":
    raise SystemExit("the isolated runtime's protected calls refuse on macOS; this measurement covers Windows and Linux")

with ExitStack() as stack:
    build = Path(stack.enter_context(tempfile.TemporaryDirectory(prefix="percall-", dir=build_root(), ignore_cleanup_errors=True)))
    env = environment(build)

    def run(args, cwd=ROOT, timeout=900, extra=None):
        result = subprocess.run([str(x) for x in args], cwd=cwd, env=dict(env, **(extra or {})), capture_output=True,
                                text=True, encoding="utf-8", errors="replace", timeout=timeout)
        if result.returncode:
            raise RuntimeError(" ".join(map(str, args)) + "\n" + result.stdout + result.stderr)
        return result.stdout

    if a.breakdown:
        if sys.platform.startswith("linux"):
            source = Path("/sys/devices/system/clocksource/clocksource0/current_clocksource")
            print("host:", os.uname().release, "clocksource", source.read_text().strip() if source.is_file() else "unknown",
                  "cpus", os.cpu_count(), flush=True)
        tool = build/("breakdown" + EXE)
        platform = ["accept_windows.go", "clock_windows.go"] if WINDOWS else ["accept_other.go", "clock_other.go"]
        run(["go", "build", "-o", tool, "main.go", "dial.go", *platform], cwd=HERE/"breakdown")
        for mode, calls in (("fresh", a.calls), ("bound", a.calls * 10), ("session", a.calls * 10)):
            out = run([tool, mode, calls], cwd=build, timeout=600)
            for line in out.splitlines():
                print(line.rstrip(), mode, sys.platform, flush=True)
        if not a.measure:
            print("PASS per-call breakdown on", sys.platform)
            raise SystemExit(0)
    runtime = build/("openabstractions" + EXE)
    run(["go", "build", "-o", runtime, "."], cwd=ROOT/"serve")
    go_probe = build/("go_percall" + EXE)
    run(["go", "build", "-o", go_probe, "go_percall.go", "clock_windows.go" if WINDOWS else "clock_other.go"], cwd=HERE)
    probes = [("go", [go_probe], {}, ["single"]), ("go-session", [go_probe], {}, ["session"])]

    node = None
    if a.node:
        node = resolved_executable(a.node)
    version = subprocess.check_output([str(node), "--version"], text=True).strip().lstrip("v") if node else ""
    headers = a.node_sdk.resolve()/version/"include"/"node"
    library = a.node_sdk.resolve()/version/"x64"/"node.lib" if WINDOWS else None
    addon = bool(node) and (headers/"node_api.h").is_file() and (library is None or library.is_file())
    if node and not addon:
        print("javascript-native skipped: no Node", version, "headers in", a.node_sdk, flush=True)

    needs_static = not a.no_cpp or addon or not a.no_rust
    cmake = None
    if needs_static or not a.no_python:
        cmake = cmake_for(env, a.cmake)
    ipc = layer("abstraction-identity")/"cpp"
    release = [CMAKE_BUILD_TYPE_RELEASE, "-DABSTRACTION_IPC_BUILD_TESTS=OFF"]
    if needs_static:
        run([cmake, "-S", ipc, "-B", build/"ipc-static", "-DBUILD_SHARED_LIBS=OFF", "-DCMAKE_INSTALL_PREFIX="+str(build/"static"), *release])
        certify_compiler(build/"ipc-static")
        run([cmake, "--build", build/"ipc-static", "--config", "Release"])
        run([cmake, "--install", build/"ipc-static", "--config", "Release"])
    if not a.no_cpp:
        run([cmake, "-S", HERE, "-B", build/"cpp", CMAKE_BUILD_TYPE_RELEASE, "-DCMAKE_PREFIX_PATH="+str(build/"static"),
             "-DFACADE_INCLUDE_DIR="+str(layer("abstraction-facade")/"cpp")])
        certify_compiler(build/"cpp")
        run([cmake, "--build", build/"cpp", "--config", "Release"])
        exe = build/"cpp"/"Release"/("cpp_percall" + EXE)
        exe = exe if exe.is_file() else build/"cpp"/("cpp_percall" + EXE)
        probes += [("cpp", [exe], {}, ["single"]), ("cpp-session", [exe], {}, ["session"])]
    if not a.no_rust:
        # Rust has no generated caller client: the probe sends the frame the
        # Python codec encodes for Observe.
        sys.path[:0] = [str(layer("abstraction-facade")/"py"), str(layer("abstraction-identity")/"py")]
        from abstraction.facade import _codec
        request = _codec._service_request("abstraction.facade/caller@1", "Observe", _codec._service_encode(
            _codec._write_oa_caller_observe_arguments, _codec._CallerObserveArguments(), 1))
        subprocess.run(["cargo", "build", "--offline", "--release", "--manifest-path", str(HERE/"rust"/"Cargo.toml")], cwd=ROOT,
                       env=dict(env, OA_IPC_PREFIX=str((build/"static").resolve()), CARGO_TARGET_DIR=str(build/"rust-target")),
                       check=True, timeout=900, capture_output=True)
        rust = build/"rust-target"/"release"/("rust-percall" + EXE)
        probes += [("rust", [rust], {}, ["single", request.hex()]), ("rust-session", [rust], {}, ["session", request.hex()])]
    if not a.no_python:
        run([cmake, "-S", ipc, "-B", build/"ipc-shared", "-DBUILD_SHARED_LIBS=ON", "-DCMAKE_INSTALL_PREFIX="+str(build/"shared"), *release])
        certify_compiler(build/"ipc-shared")
        run([cmake, "--build", build/"ipc-shared", "--config", "Release"])
        run([cmake, "--install", build/"ipc-shared", "--config", "Release"])
        shared = build/"shared"/("bin/abstraction_ipc.dll" if WINDOWS else "lib/libabstraction_ipc.so")
        python_env = {"ABSTRACTION_IPC_LIBRARY": str(shared.resolve()),
                      "PYTHONPATH": os.pathsep.join(str(layer(name)/"py") for name in ("abstraction-identity", "abstraction-facade"))}
        probes += [("python", [sys.executable, HERE/"py_percall.py"], python_env, ["single"]),
                   ("python-session", [sys.executable, HERE/"py_percall.py"], python_env, ["session"])]
    if addon:
        native = layer("abstraction-identity")/"javascript"
        run([cmake, "-S", native, "-B", build/"addon", "-DCMAKE_PREFIX_PATH="+str(build/"static"), "-DNODE_INCLUDE_DIR="+str(headers),
             *(["-DNODE_IMPORT_LIBRARY="+str(library)] if library else []), CMAKE_BUILD_TYPE_RELEASE])
        certify_compiler(build/"addon")
        run([cmake, "--build", build/"addon", "--config", "Release"])
        run([cmake, "--install", build/"addon", "--config", "Release", "--prefix", build/"addon-package"])
        found = sorted((build/"addon-package").rglob("*.node"))
        if len(found) != 1:
            raise RuntimeError("expected one installed .node addon, found " + str(found))
        node_env = {"ABSTRACTION_IPC_NODE": str(found[0].resolve())}
        probes += [("javascript-native", [node, HERE/"js_percall.mjs", "native"], node_env, ["single"]),
                   ("javascript-native-session", [node, HERE/"js_percall.mjs", "native"], node_env, ["session"])]
    if node:
        probes.append(("javascript-pipe", [node, HERE/"js_percall.mjs", "pipe"], {}, ["single"]))

    state = build/"state"
    state.mkdir()
    name = "oa-percall-" + str(os.getpid())
    server = subprocess.Popen([str(runtime), "serve", "runtime", "--isolated", name, "--state-dir", str(state)],
                              cwd=build, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True, encoding="utf-8", errors="replace")
    stack.callback(lambda: (server.kill(), server.wait()))
    lines = queue.Queue()
    threading.Thread(target=lambda: [lines.put(line) for line in server.stderr], daemon=True).start()
    endpoint, until = None, time.monotonic() + 60
    while endpoint is None:
        try:
            line = lines.get(timeout=max(0.1, until - time.monotonic()))
        except queue.Empty:
            raise RuntimeError("the isolated runtime printed no endpoint within 60 s")
        if line.strip().startswith("ABSTRACTION_RUNTIME_ENDPOINT="):
            endpoint = line.strip().split("=", 1)[1]
    print("isolated runtime", name, "at", endpoint, flush=True)

    measured = {}
    for client, command, extra, mode in probes:
        out = run([*command, endpoint, a.calls, a.warmup, *mode], cwd=HERE, timeout=600, extra=extra)
        found = PERCALL.search(out)
        if not found or found.group(1) != client:
            raise RuntimeError(client + " printed no PERCALL line:\n" + out)
        print(found.group(0).rstrip(), sys.platform, flush=True)
        measured[client] = found
    if a.results:
        with open(a.results, "a", encoding="utf-8", newline="\n") as f:
            for found in measured.values():
                f.write(found.group(0).rstrip() + " " + sys.platform + "\n")
    if a.gate:
        failures, passed = [], []

        def p50(client):
            return float(measured[client].group(4))
        if WINDOWS:
            # A connection per call: a signed interpreter must not pay a
            # per-connection signature check again.
            references = [p50(c) for c in ("go", "cpp") if c in measured]
            bound = max(GATE_FLOOR_MS, GATE_FACTOR * min(references))
            for client in ("python", "javascript-native"):
                if client in measured:
                    (failures if p50(client) > bound else passed).append("%s p50 %.3f ms (bound %.3f)" % (client, p50(client), bound))
        # A session: the steady-state call on a bound connection.
        native_bound = SESSION_NATIVE_BOUND_MS.get("win32" if WINDOWS else "linux")
        for client in ("go-session", "cpp-session", "rust-session"):
            if client in measured:
                (failures if p50(client) > native_bound else passed).append("%s p50 %.3f ms (bound %.3f)" % (client, p50(client), native_bound))
        for client in ("python-session", "javascript-native-session"):
            if client in measured:
                (failures if p50(client) > SESSION_BINDING_BOUND_MS else passed).append(
                    "%s p50 %.3f ms (bound %.3f)" % (client, p50(client), SESSION_BINDING_BOUND_MS))
        if not failures and not passed:
            raise SystemExit("gate: no gated probe ran")
        if failures:
            raise SystemExit("FAIL per-call gate: " + "; ".join(failures))
        print("PASS per-call gate: " + "; ".join(passed))
    print("PASS per-call measurement on", sys.platform, "with", ", ".join(measured))
