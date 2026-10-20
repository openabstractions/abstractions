"""Run the OpenCode adopter's Node tests (npm test), installing their dependencies first when absent.

The tests import the eight @openabstractions packages from this checkout and the
third-party closure the package's lock pins. The OA packages are not on npm, so
the install links them from openabstractions-flat/ and takes the rest from the
lock, without rewriting package.json or the lock:

  npm install --ignore-scripts --no-save --no-audit --no-fund <eight package directories>

The install runs only when node_modules lacks one of the packages the tests
import, and it reaches the npm registry or cache for the third-party closure.
The links it creates under node_modules/@openabstractions (junctions on
Windows) point into openabstractions-flat/; after the tests the run removes the
links it created, the link itself and never what it points to, so a later
recursive delete of node_modules cannot follow one into the checkout.

On Windows the run first builds the @openabstractions/ipc development addon
(native/oa_ipc_node.node) with scripts/build_node_addon.py when it is absent or
stale, so a fresh worktree passes; that build downloads nothing.

Tests that need Bun (BUN_TESTS) skip under node --test. After npm test they
run with `bun test` from the pinned Bun that get-pinned-bun.ps1 places in the
checkout's .forks/bun-1.3.14/; without that Bun they are skipped with the
reason printed. Exit status is npm's, or Bun's when npm passed.

  py adopters/opencode/test/run_node_tests.py --run               install if needed, then npm test
  py adopters/opencode/test/run_node_tests.py --run --no-install  fail instead of installing

Without --run it prints this help.
"""
import argparse
import json
import os
import shutil
import subprocess
import sys
from pathlib import Path

ADOPTER = Path(__file__).resolve().parent.parent
ROOT = ADOPTER.parent.parent
# One directory per @openabstractions dependency in package.json.
LINKED = (
    "openabstractions-flat/abstraction-asks/javascript",
    "openabstractions-flat/abstraction-facade/javascript",
    "openabstractions-flat/abstraction-inference/javascript",
    "openabstractions-flat/abstraction-identity/javascript",
    "openabstractions-flat/abstraction-logging/javascript",
    "openabstractions-flat/abstraction-rights/javascript",
    "openabstractions-flat/abstraction-router/javascript",
    "openabstractions-flat/abstraction-storage/javascript",
)


def imported(adopter=ADOPTER):
    """Packages the tests import: package.json's dependencies and the two OpenCode packages.

    Absence of any of them means the install has not run since a dependency was added.
    """
    deps = json.loads((adopter / "package.json").read_text(encoding="utf-8")).get("dependencies", {})
    return tuple(sorted(deps)) + ("@ai-sdk/provider", "@opencode-ai/plugin")


# Tests node --test skips because they need the Bun runtime OpenCode runs on.
BUN_TESTS = ("test/winmm-binding.test.mjs",)


def missing():
    return [name for name in imported() if not (ADOPTER / "node_modules" / name / "package.json").is_file()]


def is_link(path):
    """A symbolic link or a Windows junction."""
    return os.path.islink(path) or os.path.isjunction(path)


def package_links(adopter=ADOPTER):
    """The links under node_modules/@openabstractions."""
    scope = adopter / "node_modules" / "@openabstractions"
    if not scope.is_dir() or is_link(scope):
        return set()
    return {entry for entry in scope.iterdir() if is_link(entry)}


def remove_link(path):
    """Remove one link and nothing it points to: a junction goes with rmdir,
    which deletes the reparse point; a symbolic link with unlink. A real
    directory is refused."""
    if os.path.isjunction(path):
        os.rmdir(path)
    elif os.path.islink(path):
        os.unlink(path)
    else:
        raise ValueError(f"run_node_tests: {path} is not a link")


def remove_created_links(before, adopter=ADOPTER, out=sys.stderr):
    """Remove the package links that were not there before; return a status."""
    status = 0
    for link in sorted(package_links(adopter) - before):
        try:
            remove_link(link)
        except OSError as error:
            print(f"run_node_tests: could not remove the link {link}: {error}", file=out)
            status = 1
    return status


def pinned_bun(root=ROOT):
    """The pinned Bun get-pinned-bun.ps1 extracts into the checkout's .forks/."""
    return root / ".forks" / "bun-1.3.14" / ("bun.exe" if sys.platform == "win32" else "bun")


def run_bun_tests(bun, run=subprocess.run, out=sys.stdout):
    """Run BUN_TESTS with bun, or say why they are skipped; returns an exit status."""
    if not bun.is_file():
        print(f"run_node_tests: skipped {', '.join(BUN_TESTS)}: no pinned Bun at {bun}; "
              "fetch it with adopters/opencode/get-pinned-bun.ps1", file=out)
        return 0
    return run([str(bun), "test", *BUN_TESTS], cwd=ADOPTER, check=False).returncode


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0],
                                     formatter_class=argparse.RawDescriptionHelpFormatter, epilog=__doc__)
    parser.add_argument("--run", action="store_true", help="install the dependencies if absent, then run npm test")
    parser.add_argument("--no-install", action="store_true", help="with --run, fail when the dependencies are absent")
    args = parser.parse_args(argv)
    if not args.run:
        parser.print_help()
        return 0
    npm = shutil.which("npm")
    if npm is None:
        print("run_node_tests: npm is not on PATH", file=sys.stderr)
        return 2
    absent = missing()
    if absent and args.no_install:
        print("run_node_tests: node_modules lacks " + ", ".join(absent), file=sys.stderr)
        return 2
    before = package_links()
    try:
        if absent:
            install = [npm, "install", "--ignore-scripts", "--no-save", "--no-audit", "--no-fund",
                       *[str(ROOT / directory) for directory in LINKED]]
            code = subprocess.run(install, cwd=ADOPTER, check=False).returncode
            if code:
                return code
        if sys.platform == "win32":
            code = subprocess.run([sys.executable, str(ROOT / "scripts" / "build_node_addon.py"), "--run"],
                                  check=False).returncode
            if code:
                return code
        code = subprocess.run([npm, "test"], cwd=ADOPTER, check=False).returncode
        if code:
            return code
        return run_bun_tests(pinned_bun())
    finally:
        if remove_created_links(before):
            print("run_node_tests: remove the links listed above by hand (rmdir, never a recursive delete)", file=sys.stderr)


if __name__ == "__main__":
    sys.exit(main())
