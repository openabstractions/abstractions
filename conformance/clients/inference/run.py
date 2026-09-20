"""Prove abstraction.inference/chat@1 clients against the runtime: complete, stream, cancel, refusal and absence in Go, C++, JavaScript, Python and Rust, with first-token latency per language."""
import argparse
from contextlib import ExitStack
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path
HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
from workspace import ROOT, environment, compiler_metadata, source_revision, dry_run_stop, layer, DRY_RUN_HELP
from sdk import installed_sdk

p = argparse.ArgumentParser(description=__doc__)
p.add_argument("--unit", action="store_true", help="run the inference Go module tests and the Rust facade crate tests on this host")
p.add_argument("--run", action="store_true", help="build the consumers and run the resolved fixture: the Go client in the fixture process, then each consumer")
p.add_argument("--no-cpp", action="store_true", help="with --run, skip installing the SDK and building the C++ consumer")
p.add_argument("--node", default="node", help="with --run, the Node.js executable for the JavaScript consumer; an empty value skips it")
p.add_argument("--no-python", action="store_true", help="with --run, skip building the shared IPC library and the Python consumer")
p.add_argument("--no-rust", action="store_true", help="with --run, skip building the static IPC library and the Rust consumer")
p.add_argument("--latency", type=Path, help="with --run, append the FIRST_TOKEN_MS lines to this file; with --measure, append the LATENCY and RTT lines")
p.add_argument("--window", action="store_true", help="run the loopback socket-owner binding tests in abstraction-identity and the gateway window tests on this host, with -race")
p.add_argument("--measure", action="store_true", help="measure long-poll Observe first-token and per-token latency on this host's native IPC (latency_test.go), with the JavaScript and Python per-call probes unless skipped")
p.add_argument('--dry-run', action='store_true', help=DRY_RUN_HELP)
p.add_argument("--prefix", type=Path, help="existing installed aggregate facade prefix that already holds abstraction_inference_api")
p.add_argument("--cmake", default="cmake")
a = p.parse_args()
if not (a.unit or a.run or a.measure or a.window or a.dry_run):
    p.print_help()
    raise SystemExit(0)
source_revision()
if a.dry_run: dry_run_stop('inference', a)
if sys.platform == "darwin":
    raise SystemExit("Program proof required; this fixture cannot claim macOS conformance")
with ExitStack() as stack:
    build_root = ROOT/".build"
    build_root.mkdir(parents=True, exist_ok=True)
    build = Path(stack.enter_context(tempfile.TemporaryDirectory(prefix="inference-", dir=build_root, ignore_cleanup_errors=True)))
    env = environment(build)
    def run(args, cwd=ROOT, timeout=900):
        result = subprocess.run([str(x) for x in args], cwd=cwd, env=env, capture_output=True, text=True, encoding="utf-8", timeout=timeout)
        print(result.stdout, end="")
        if result.returncode:
            raise RuntimeError(result.stderr or result.stdout)
        return result.stdout
    def python_ipc():
        # The Python facade reaches the runtime through the shared abstraction_ipc library.
        native, installed = build/"ipc-shared", build/"ipc"
        run([a.cmake, "-S", layer("abstraction-identity")/"cpp", "-B", native, "-DBUILD_SHARED_LIBS=ON", "-DABSTRACTION_IPC_BUILD_TESTS=OFF", "-DCMAKE_INSTALL_PREFIX="+str(installed), "-DCMAKE_BUILD_TYPE=Release"])
        run([a.cmake, "--build", native, "--config", "Release"])
        run([a.cmake, "--install", native, "--config", "Release"])
        library = installed/("bin/abstraction_ipc.dll" if os.name == "nt" else "lib/libabstraction_ipc.so")
        env["ABSTRACTION_IPC_LIBRARY"] = str(library.resolve())
        env["PYTHONPATH"] = os.pathsep.join(str(layer(name)/"py") for name in ("abstraction-identity", "abstraction-facade", "abstraction-inference", "abstraction-job", "abstraction-storage", "abstraction-logging"))
        env["OA_PY_INFERENCE_PYTHON"] = sys.executable
    if a.window:
        # CGO_ENABLED=1 is -race's own requirement; the binding itself needs no cgo.
        window_env = dict(env, CGO_ENABLED="1")
        for cwd, pattern, package in ((layer("abstraction-identity"), "Loopback", "."), (layer("abstraction-inference")/"go", ".", "./gateway/...")):
            result = subprocess.run(["go", "test", "-race", "-count=1", "-v", "-run", pattern, package], cwd=cwd, env=window_env, capture_output=True, text=True, encoding="utf-8", timeout=900)
            print("\n".join(line for line in result.stdout.splitlines() if line.startswith(("---", "ok", "FAIL", "PASS")) or "rung " in line or "refuses" in line))
            if result.returncode:
                raise RuntimeError(result.stderr or result.stdout)
        print("PASS loopback binding and gateway window on", sys.platform)
    if a.unit:
        run(["go", "test", "-count=1", "./..."], cwd=layer("abstraction-inference")/"go")
        run(["cargo", "test", "--offline", "--quiet"], cwd=layer("abstraction-facade")/"rust-inference")
        print("PASS inference Go module and Rust facade crate tests on", sys.platform)
    if a.run:
        consumers = []
        if not a.no_cpp:
            if a.prefix is None:
                a.prefix = stack.enter_context(installed_sdk(a.cmake, "aggregate", env))
                package = build/"inference-package"
                run([a.cmake, "-S", layer("abstraction-inference")/"cpp", "-B", package, "-DCMAKE_BUILD_TYPE=Release", "-DCMAKE_PREFIX_PATH="+str(a.prefix.resolve())])
                run([a.cmake, "--build", package, "--config", "Release"])
                run([a.cmake, "--install", package, "--config", "Release", "--prefix", a.prefix])
            # The installed SDK is Release; a single-configuration generator needs the same build type.
            run([a.cmake, "-S", HERE, "-B", build/"cpp", "-DCMAKE_BUILD_TYPE=Release", "-DCMAKE_PREFIX_PATH="+str(a.prefix.resolve())])
            run([a.cmake, "--build", build/"cpp", "--config", "Release", "--parallel", "4"])
            compiler_metadata(build/"cpp")
            exe = build/"cpp"/("Release/inference_consumer.exe" if os.name == "nt" else "inference_consumer")
            if not exe.is_file():
                exe = build/"cpp"/("inference_consumer.exe" if os.name == "nt" else "inference_consumer")
            run([exe, "--help"])
            env["OA_CPP_INFERENCE_PROBE"] = str(exe.resolve())
            consumers.append("cpp")
        if a.node:
            env["OA_JS_INFERENCE_NODE"] = a.node
            consumers.append("javascript")
        if not a.no_python:
            python_ipc()
            consumers.append("python")
        if not a.no_rust:
            # The Rust facade links the static abstraction_ipc library through facade-native.
            native, installed = build/"ipc-static-build", build/"ipc-static"
            run([a.cmake, "-S", layer("abstraction-identity")/"cpp", "-B", native, "-DBUILD_SHARED_LIBS=OFF", "-DABSTRACTION_IPC_BUILD_TESTS=OFF", "-DCMAKE_INSTALL_PREFIX="+str(installed), "-DCMAKE_BUILD_TYPE=Release"])
            run([a.cmake, "--build", native, "--config", "Release"])
            run([a.cmake, "--install", native, "--config", "Release"])
            cargo_env = dict(env, OA_IPC_PREFIX=str(installed.resolve()), CARGO_TARGET_DIR=str((build/"rust-target").resolve()))
            subprocess.run(["cargo", "build", "--offline", "--release", "--manifest-path", str(HERE/"rust"/"Cargo.toml")], cwd=ROOT, env=cargo_env, check=True, timeout=900)
            env["OA_RUST_INFERENCE_PROBE"] = str((build/"rust-target"/"release"/("inference-consumer.exe" if os.name == "nt" else "inference-consumer")).resolve())
            consumers.append("rust")
        env["OA_INFERENCE_CONSUMERS"] = ",".join(consumers)
        out = run(["go", "test", "-count=1", "-v", "-timeout", "10m", "-run", "TestResolvedInference", "fixture_test.go", "audit_test.go"], cwd=HERE)
        if "cpp" in consumers or "python" in consumers:
            # The shipped runtime with its gateway window; C++ and Python read the audit Go reads.
            audit = run(["go", "test", "-count=1", "-v", "-timeout", "10m", "-run", "TestInstalledClientsReadTheSameAudit", "fixture_test.go", "audit_test.go"], cwd=HERE)
            print("\n".join(line for line in audit.splitlines() if line.startswith("PASS shared audit")))
        latencies = re.findall(r"(?m)^FIRST_TOKEN_MS \S+ [0-9.]+(?=\r?$)", out)
        if a.latency:
            with open(a.latency, "a", encoding="utf-8", newline="\n") as f:
                for line in latencies:
                    f.write(line + " " + sys.platform + "\n")
        print("PASS resolved inference with go" + "".join(", " + name for name in consumers) + " consumers")
    if a.measure:
        if a.node:
            env["OA_JS_INFERENCE_NODE"] = a.node
        if not a.no_python and "OA_PY_INFERENCE_PYTHON" not in env:
            python_ipc()
        env["OA_INFERENCE_MEASURE"] = "1"
        clock = "clock_windows_test.go" if os.name == "nt" else "clock_other_test.go"
        out = run(["go", "test", "-count=1", "-v", "-timeout", "20m", "-run", "TestStreamingLatency", "latency_test.go", clock, "fixture_test.go"], cwd=HERE, timeout=1500)
        lines = re.findall(r"(?m)^(?:LATENCY|RTT) .*$", out)
        if a.latency:
            with open(a.latency, "a", encoding="utf-8", newline="\n") as f:
                f.write("\n".join(lines) + "\n")
        print("PASS inference latency measured on", sys.platform)
