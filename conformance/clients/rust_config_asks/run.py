"""Build outside Rust config and asks clients and exercise an isolated Go runtime.

Runs on Windows (MSVC, named pipes) and Linux (GCC, Unix sockets) against a
private runtime on the same host. The pure crates are tested first without the
native library or unrelated capabilities. Cargo's native link lines for the
consumer must equal the installed link-dependencies.txt.
"""
import argparse, os, shutil, subprocess, sys, tempfile
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from workspace import source_revision, dry_run_stop, DRY_RUN_HELP
ROOT = Path(__file__).resolve().parents[3]
HERE = Path(__file__).resolve().parent
CHECK = ROOT/"openabstractions-flat/abstraction-identity/cpp/check_link_dependencies.py"

def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--run", action="store_true", help="build and run the Rust config/asks consumer on this host (Windows or Linux)")
    p.add_argument('--dry-run', action='store_true', help=DRY_RUN_HELP)
    p.add_argument("--cmake", default="cmake")
    p.add_argument("--race", action="store_true", help="run the Go fixture with -race (needs a configured C compiler)")
    a = p.parse_args()
    if not (a.run or a.dry_run):
        p.print_help()
        return
    windows = os.name == "nt"
    def run(argv, env=None, cwd=ROOT):
        subprocess.run([str(x) for x in argv], cwd=cwd, env=env, check=True, timeout=600)
    source_revision()
    if a.dry_run: dry_run_stop('rust_config_asks', a)
    if not windows and not sys.platform.startswith("linux"):
        raise RuntimeError("native fixture measures Windows/MSVC and Linux only")
    with tempfile.TemporaryDirectory(prefix="oa-rust-config-asks-") as tmp:
        b = Path(tmp); tree = b/"tree"
        for component, parts in [("identity", ["rust-frame"]), ("facade", ["rust", "rust-config", "rust-asks", "rs"]), ("config", ["rust", "rs"]), ("asks", ["rust", "rs"])]:
            for part in parts:
                rel = Path("openabstractions-flat")/("abstraction-"+component)/part
                shutil.copytree(ROOT/rel, tree/rel)
        env = dict(os.environ, CARGO_TARGET_DIR=str(b/"target")); env.pop("OA_IPC_PREFIX", None)
        for crate in ("rust-config", "rust-asks"):
            run(["cargo", "test", "--offline", "--manifest-path", tree/"openabstractions-flat/abstraction-facade"/crate/"Cargo.toml"], env, tree)
        assert not (tree/"openabstractions-flat/abstraction-identity/rust").exists()
        for name in ("job", "logging", "storage", "download", "rights"):
            assert not (tree/("openabstractions-flat/abstraction-"+name)).exists()
        run([a.cmake, "-S", ROOT/"openabstractions-flat/abstraction-identity/cpp", "-B", b/"static-build", "-DBUILD_SHARED_LIBS=OFF", "-DABSTRACTION_IPC_BUILD_TESTS=OFF", "-DCMAKE_BUILD_TYPE=Release", "-DCMAKE_INSTALL_PREFIX="+str(b/"static")])
        run([a.cmake, "--build", b/"static-build", "--config", "Release"])
        run([a.cmake, "--install", b/"static-build", "--config", "Release"])
        for component, part in [("identity", "rust"), ("facade", "rust-native")]:
            rel = Path("openabstractions-flat")/("abstraction-"+component)/part
            shutil.copytree(ROOT/rel, tree/rel)
        consumer = tree/"conformance/clients/rust_config_asks"; shutil.copytree(HERE, consumer)
        env["OA_IPC_PREFIX"] = str(b/"static")
        build = subprocess.run(["cargo", "build", "-vv", "--offline", "--manifest-path", str(consumer/"Cargo.toml")], cwd=tree, env=env, capture_output=True, text=True, timeout=600)
        sys.stdout.write(build.stdout[-4000:]); sys.stderr.write(build.stderr[-4000:])
        if build.returncode:
            raise subprocess.CalledProcessError(build.returncode, "cargo build")
        (b/"cargo-build.log").write_text(build.stdout+build.stderr, encoding="utf-8")
        run([sys.executable, CHECK, "--prefix", b/"static", "--cargo-output", b/"cargo-build.log"])
        probe = b/("target/debug/config-asks-consumer"+(".exe" if windows else ""))
        run([probe, "--help"], env)
        env["OA_RUST_CONFIG_ASKS_PROBE"] = str(probe)
        run(["go", "test", *(["-race"] if a.race else []), "-v", "-count=1", HERE/"fixture_test.go"], env)
    print(f"PASS outside Rust config and asks clients on {'Windows' if windows else 'Linux'}: edit outcomes, questions and operator retirement; temporary files removed")

if __name__ == "__main__":
    main()
