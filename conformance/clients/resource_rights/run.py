"""Build an installed aggregate C++ consumer and prove rights enforcement at logging history, model lookup and router services."""
import argparse
from contextlib import ExitStack
import os
import subprocess
import sys
import tempfile
from pathlib import Path
HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
from workspace import ROOT, environment, compiler_metadata, source_revision, dry_run_stop, DRY_RUN_HELP, CMAKE_BUILD_TYPE_RELEASE
from sdk import installed_sdk

p = argparse.ArgumentParser(description=__doc__)
p.add_argument("--run", action="store_true", help="install a temporary aggregate SDK, build the consumer and run the fixture")
p.add_argument('--dry-run', action='store_true', help=DRY_RUN_HELP)
p.add_argument("--prefix", type=Path, help="existing installed aggregate facade prefix")
p.add_argument("--cmake", default="cmake")
p.add_argument("--race", action="store_true", help="run the Go fixture with the race detector; configured C compiler required")
a = p.parse_args()
if not (a.run or a.dry_run):
    p.print_help()
    raise SystemExit(0)
source_revision()
if a.dry_run: dry_run_stop('resource_rights', a)
if sys.platform == "darwin":
    raise SystemExit("Program proof required; this fixture cannot claim macOS conformance")
with ExitStack() as stack:
    build_root = ROOT/".build"
    build_root.mkdir(parents=True, exist_ok=True)
    build = Path(stack.enter_context(tempfile.TemporaryDirectory(prefix="resource-rights-", dir=build_root)))
    env = environment(build)
    def run(args, cwd=ROOT):
        result = subprocess.run([str(x) for x in args], cwd=cwd, env=env, capture_output=True, text=True, encoding="utf-8", timeout=300)
        print(result.stdout, end="")
        if result.returncode:
            raise RuntimeError(result.stderr or result.stdout)
    if a.prefix is None:
        a.prefix = stack.enter_context(installed_sdk(a.cmake, "aggregate", env))
    run([a.cmake, "-S", HERE, "-B", build/"cpp", "-DCMAKE_PREFIX_PATH="+str(a.prefix.resolve()), CMAKE_BUILD_TYPE_RELEASE])
    run([a.cmake, "--build", build/"cpp", "--config", "Release", "--parallel", "4"])
    compiler_metadata(build/"cpp")
    exe = build/"cpp"/("Release/resource_rights_consumer.exe" if os.name == "nt" else "resource_rights_consumer")
    run([exe, "--help"])
    env["OA_CPP_RESOURCE_RIGHTS_PROBE"] = str(exe)
    run(["go", "test", *(["-race"] if a.race else []), "-count=1", "-v", HERE/"fixture_test.go"])
print("PASS installed C++ history, model and router rights enforcement: denied, granted, revoked, outage")
