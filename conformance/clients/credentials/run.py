"""Prove the credentials holder: Go backend unit tests, and a resolved runtime fixture with C++, JavaScript and Python consumers."""
import argparse
from contextlib import ExitStack
import os
import subprocess
import sys
import tempfile
from pathlib import Path
HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
from workspace import ROOT, environment, compiler_metadata, source_revision, dry_run_stop, layer, DRY_RUN_HELP
from sdk import installed_sdk

p = argparse.ArgumentParser(description=__doc__)
p.add_argument("--unit", action="store_true", help="run the credentials Go module tests: the platform store backends of this host")
p.add_argument("--run", action="store_true", help="install a temporary aggregate SDK plus the credentials C++ package, build the consumer and run the resolved fixture")
p.add_argument("--no-cpp", action="store_true", help="with --run, run the resolved Go fixture without building the C++ consumer")
p.add_argument("--node", default="node", help="with --run, the Node.js executable for the JavaScript consumer; an empty value skips it")
p.add_argument("--no-python", action="store_true", help="with --run, skip building the shared IPC library and the Python consumer")
p.add_argument("--no-rust", action="store_true", help="with --run, skip building the static IPC library and the Rust consumer")
p.add_argument('--dry-run', action='store_true', help=DRY_RUN_HELP)
p.add_argument("--prefix", type=Path, help="existing installed aggregate facade prefix that already holds abstraction_credentials_api")
p.add_argument("--cmake", default="cmake")
a = p.parse_args()
if not (a.unit or a.run or a.dry_run):
    p.print_help()
    raise SystemExit(0)
source_revision()
if a.dry_run: dry_run_stop('credentials', a)
if sys.platform == "darwin":
    raise SystemExit("Program proof required; this fixture cannot claim macOS conformance")
with ExitStack() as stack:
    build_root = ROOT/".build"
    build_root.mkdir(parents=True, exist_ok=True)
    build = Path(stack.enter_context(tempfile.TemporaryDirectory(prefix="credentials-", dir=build_root, ignore_cleanup_errors=True)))
    env = environment(build)
    def run(args, cwd=ROOT, timeout=600):
        result = subprocess.run([str(x) for x in args], cwd=cwd, env=env, capture_output=True, text=True, encoding="utf-8", timeout=timeout)
        print(result.stdout, end="")
        if result.returncode:
            raise RuntimeError(result.stderr or result.stdout)
    if a.unit:
        run(["go", "test", "-count=1", "-v", "./..."], cwd=layer("abstraction-credentials")/"go")
        print("PASS credentials Go module tests on", sys.platform)
    if a.run:
        if not a.no_cpp:
            if a.prefix is None:
                a.prefix = stack.enter_context(installed_sdk(a.cmake, "aggregate", env))
                package = build/"credentials-package"
                run([a.cmake, "-S", layer("abstraction-credentials")/"cpp", "-B", package, "-DCMAKE_BUILD_TYPE=Release", "-DCMAKE_PREFIX_PATH="+str(a.prefix.resolve())])
                run([a.cmake, "--build", package, "--config", "Release"])
                run([a.cmake, "--install", package, "--config", "Release", "--prefix", a.prefix])
            # The installed SDK is Release; a single-configuration generator needs the same build type.
            run([a.cmake, "-S", HERE, "-B", build/"cpp", "-DCMAKE_BUILD_TYPE=Release", "-DCMAKE_PREFIX_PATH="+str(a.prefix.resolve())])
            run([a.cmake, "--build", build/"cpp", "--config", "Release", "--parallel", "4"])
            compiler_metadata(build/"cpp")
            exe = build/"cpp"/("Release/credentials_consumer.exe" if os.name == "nt" else "credentials_consumer")
            if not exe.is_file():
                exe = build/"cpp"/("credentials_consumer.exe" if os.name == "nt" else "credentials_consumer")
            run([exe, "--help"])
            env["OA_CPP_CREDENTIALS_PROBE"] = str(exe.resolve())
        if a.node:
            env["OA_JS_CREDENTIALS_NODE"] = a.node
        if not a.no_python:
            # The Python facade reaches the runtime through the shared abstraction_ipc library.
            native, installed = build/"ipc-shared", build/"ipc"
            run([a.cmake, "-S", layer("abstraction-identity")/"cpp", "-B", native, "-DBUILD_SHARED_LIBS=ON", "-DABSTRACTION_IPC_BUILD_TESTS=OFF", "-DCMAKE_INSTALL_PREFIX="+str(installed), "-DCMAKE_BUILD_TYPE=Release"])
            run([a.cmake, "--build", native, "--config", "Release"])
            run([a.cmake, "--install", native, "--config", "Release"])
            library = installed/("bin/abstraction_ipc.dll" if os.name == "nt" else "lib/libabstraction_ipc.so")
            env["ABSTRACTION_IPC_LIBRARY"] = str(library.resolve())
            env["PYTHONPATH"] = os.pathsep.join(str(layer(name)/"py") for name in ("abstraction-identity", "abstraction-facade", "abstraction-credentials", "abstraction-job", "abstraction-storage", "abstraction-logging"))
            env["OA_PY_CREDENTIALS_PYTHON"] = sys.executable
        if not a.no_rust:
            # The Rust facade links the static abstraction_ipc library through facade-native.
            native, installed = build/"ipc-static-build", build/"ipc-static"
            run([a.cmake, "-S", layer("abstraction-identity")/"cpp", "-B", native, "-DBUILD_SHARED_LIBS=OFF", "-DABSTRACTION_IPC_BUILD_TESTS=OFF", "-DCMAKE_INSTALL_PREFIX="+str(installed), "-DCMAKE_BUILD_TYPE=Release"])
            run([a.cmake, "--build", native, "--config", "Release"])
            run([a.cmake, "--install", native, "--config", "Release"])
            cargo_env = dict(env, OA_IPC_PREFIX=str(installed.resolve()), CARGO_TARGET_DIR=str((build/"rust-target").resolve()))
            subprocess.run(["cargo", "build", "--offline", "--manifest-path", str(HERE/"rust"/"Cargo.toml")], cwd=ROOT, env=cargo_env, check=True, timeout=900)
            env["OA_RUST_CREDENTIALS_PROBE"] = str((build/"rust-target"/"debug"/("credentials-consumer.exe" if os.name == "nt" else "credentials-consumer")).resolve())
        run(["go", "test", "-count=1", "-v", "fixture_test.go"], cwd=HERE)
        consumers = [name for name, used in (("installed C++", not a.no_cpp), ("JavaScript", bool(a.node)), ("Python", not a.no_python), ("Rust", not a.no_rust)) if used]
        print("PASS resolved credentials holder" + (" with " + ", ".join(consumers) + " consumers" if consumers else ""))
