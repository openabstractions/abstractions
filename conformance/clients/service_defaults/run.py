"""Install current Python bindings in a temporary prefix and test real config/log services."""
import argparse
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[3]

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", action="store_true")
    parser.add_argument("--cmake", default="cmake")
    args = parser.parse_args()
    if not args.run:
        parser.print_help()
        return
    def command(argv, **kwargs):
        subprocess.run([str(x) for x in argv], cwd=ROOT, check=True, timeout=120, **kwargs)
    command(["git", "rev-parse", "HEAD"])
    command(["git", "status", "--short"])
    with tempfile.TemporaryDirectory(prefix="oa-defaults-") as tmp:
        base = Path(tmp)
        prefix, native = base/"prefix", base/"native"
        command([args.cmake,"-S",ROOT/"openabstractions-flat/abstraction-identity/cpp","-B",native,
                 "-DBUILD_SHARED_LIBS=ON","-DABSTRACTION_IPC_BUILD_TESTS=OFF",f"-DCMAKE_INSTALL_PREFIX={prefix}"])
        command([args.cmake,"--build",native,"--config","Release"])
        command([args.cmake,"--install",native,"--config","Release"])
        packages = []
        for name in ("identity", "logging", "config", "facade"):
            source = base/name
            shutil.copytree(ROOT/f"openabstractions-flat/abstraction-{name}/py", source)
            packages.append(source)
        installed = base/"python"
        command([sys.executable,"-m","pip","install","--no-index","--no-build-isolation","--no-deps","--target",installed,*packages])
        env = dict(os.environ, OA_SERVICE_DEFAULTS_PYTHON=sys.executable,
                   OA_SERVICE_DEFAULTS_PACKAGES=str(installed), ABSTRACTION_IPC_PREFIX=str(prefix))
        env.pop("ABSTRACTION_IPC_LIBRARY", None)
        command(["go","test","-v","-count=1","conformance/clients/service_defaults/defaults_test.go"], env=env)
    print("PASS: temporary installed bindings/native library, actual Go service tests; fixture prefix removed")

if __name__ == "__main__":
    main()
