"""Build every lifecycle input from a git archive and run the Linux lifecycle fixture.

Run as root inside a disposable Linux host with systemd as PID 1 (WSL Ubuntu):

  python3 conformance/clients/linux_lifecycle/drive.py --run --archive SOURCE.tar --work DIR [--scenarios LIST]

scripts/check.sh --linux-lifecycle calls this with a `git archive` of HEAD. It
extracts the archive and builds, with local toolchains and no module downloads:
the candidate program and the Linux package; the installed C++ prefix
(static IPC, facade jobs, download request); a shared IPC prefix; the identity,
facade, job and download-request Python packages; and the Go job client, whose
module joins the extracted go.work beside the lost-reply helper module. When the
upgrade group is selected it downloads the published redist v0.1.6 Linux amd64
tarball and SHA256SUMS from GitHub (network). It then runs run.py with the
selected scenario groups (default: all) from the extracted tree. Exit status is
run.py's.
"""
import argparse
import os
import shutil
import subprocess
import sys
import tarfile
import urllib.request
from pathlib import Path

PREDECESSOR = "https://github.com/openabstractions/redist/releases/download/v0.1.6/"
PREDECESSOR_ARCHIVE = "abstraction-0.1.6-linux-amd64.tar.gz"
PREDECESSOR_REVISION = "redist v0.1.6 c78e3ddc64efce84e07345e2449e5360fc23f5d3 (published asset)"
PROGRAMS = {"openabstractions": "./serve"}


def step(label, argv, cwd, env=None, timeout=900, log=None):
    print(f"==> {label}", flush=True)
    result = subprocess.run([str(a) for a in argv], cwd=cwd, env=env, capture_output=True, text=True, timeout=timeout)
    if log:
        with open(log, "a", encoding="utf-8") as out:
            out.write(f"$ {' '.join(map(str, argv))}\n{result.stdout}{result.stderr}\n")
    if result.returncode:
        sys.exit(f"{label} failed:\n{(result.stdout + result.stderr)[-3000:]}")
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--run", action="store_true", help="build inputs and run every scenario group")
    parser.add_argument("--archive", help="tar produced by git archive of the revision under test")
    parser.add_argument("--work", help="empty or disposable working directory (disk-backed, for example under /var/tmp)")
    parser.add_argument("--predecessor", help="use this predecessor tarball instead of downloading the published one")
    parser.add_argument("--predecessor-sums", help="SHA256SUMS for --predecessor")
    parser.add_argument("--scenarios", default="candidate,python,go,upgrade", help="comma list passed to run.py --scenarios")
    args = parser.parse_args()
    if not args.run:
        parser.print_help()
        return 0
    if not args.archive or not args.work:
        parser.error("--run requires --archive and --work")
    if not sys.platform.startswith("linux") or os.geteuid() != 0:
        parser.error("run as root on Linux")
    work = Path(args.work).resolve()
    if work.exists():
        shutil.rmtree(work)
    src, bins, dist, logs = work / "src", work / "bin", work / "dist", work / "build.log"
    for path in (src, bins, dist):
        path.mkdir(parents=True)
    with tarfile.open(args.archive) as tar:
        tar.extractall(src, filter="data")
    revision = subprocess.run(["git", "get-tar-commit-id"], stdin=open(args.archive, "rb"), capture_output=True, text=True).stdout.strip() or "unrecorded"
    print(f"source revision {revision}", flush=True)
    env = dict(os.environ, GOTOOLCHAIN="local", GOPROXY="off", CGO_ENABLED="0", PYTHONDONTWRITEBYTECODE="1")
    for name, package in PROGRAMS.items():
        step(f"go build {name}", ["go", "build", "-trimpath", "-o", bins / name, package], src, env, log=logs)
    # The Go job client imports the lost-reply helper through the workspace entry
    # for conformance/faults/lostreply/go; its own module joins the same go.work.
    go_client = work / "go-client"
    go_client.mkdir()
    shutil.copy2(src / "conformance/clients/linux_lifecycle/go_client/main.go", go_client / "main.go")
    (go_client / "go.mod").write_text("module lifecycle.test/goclient\n\ngo 1.26.0\n", encoding="utf-8")
    step("go work use Go job client", ["go", "work", "use", go_client], src, env, log=logs)
    step("go build Go job client", ["go", "build", "-trimpath", "-o", go_client / "lifecycle_go_client", "."], go_client,
         dict(env, GOWORK=str(src / "go.work")), log=logs)
    step("Linux package", [sys.executable, "installer/posix/build.py", "--platform", "linux", "--arch", "amd64", "--version", "0.0.0",
                           "--bin", bins, "--out", dist, "--license", "LICENSE"], src, env, log=logs)
    candidate = dist / "abstraction-0.0.0-linux-amd64.tar.gz"
    prefix, shared, python = work / "prefix", work / "ipc-shared", work / "python"
    cmake = [("static IPC", "openabstractions-flat/abstraction-identity/cpp", "b-ipc", ["-DBUILD_SHARED_LIBS=OFF", "-DABSTRACTION_IPC_BUILD_TESTS=OFF"], prefix, True),
             ("facade jobs", "openabstractions-flat/abstraction-facade/cpp", "b-facade", ["-DABSTRACTION_FACADE_BUILD_AGGREGATE=OFF", "-DABSTRACTION_FACADE_BUILD_JOBS=ON",
              "-DBUILD_SHARED_LIBS=OFF", "-DABSTRACTION_IPC_BUILD_TESTS=OFF"], prefix, True),
             ("download request", "openabstractions-flat/abstraction-download/cpp", "b-request", [], prefix, False),
             ("shared IPC", "openabstractions-flat/abstraction-identity/cpp", "b-ipc-shared", ["-DBUILD_SHARED_LIBS=ON", "-DABSTRACTION_IPC_BUILD_TESTS=OFF"], shared, True)]
    for label, source, build, options, destination, compile_ in cmake:
        step(f"configure {label}", ["cmake", "-S", source, "-B", work / build, "-DCMAKE_BUILD_TYPE=Release", f"-DCMAKE_PREFIX_PATH={prefix}",
                                     f"-DCMAKE_INSTALL_PREFIX={destination}", *options], src, env, log=logs)
        if compile_:
            step(f"build {label}", ["cmake", "--build", work / build, "-j4"], src, env, log=logs)
        step(f"install {label}", ["cmake", "--install", work / build, "--prefix", destination], src, env, log=logs)
    packages = [src / f"openabstractions-flat/abstraction-{name}/py" for name in ("identity", "facade", "job", "download")]
    step("Python packages", [sys.executable, "-m", "pip", "install", "--no-index", "--no-build-isolation", "--no-deps",
                             "--target", python, *packages], work, env, log=logs)
    predecessor, sums = args.predecessor, args.predecessor_sums
    if not predecessor and "upgrade" in args.scenarios.split(","):
        downloads = work / "predecessor"
        downloads.mkdir()
        predecessor, sums = downloads / PREDECESSOR_ARCHIVE, downloads / "SHA256SUMS"
        for name, target in ((PREDECESSOR_ARCHIVE, predecessor), ("SHA256SUMS", sums)):
            print(f"==> download {PREDECESSOR}{name}", flush=True)
            with urllib.request.urlopen(PREDECESSOR + name, timeout=120) as response, open(target, "wb") as out:
                shutil.copyfileobj(response, out)
    readable = [work, python, shared, prefix, dist, go_client] + ([Path(predecessor).parent] if predecessor else [])
    for path in readable:
        subprocess.run(["chmod", "-R", "a+rX", str(path)], check=True)
    run = [sys.executable, src / "conformance/clients/linux_lifecycle/run.py", "--run", "--candidate", candidate,
           "--prefix", prefix, "--python-packages", python, "--ipc-shared-prefix", shared,
           "--go-client", go_client / "lifecycle_go_client", "--scenarios", args.scenarios,
           "--out", work / "run", "--source-revision", revision,
           "--predecessor-revision", PREDECESSOR_REVISION if not args.predecessor else "caller-supplied predecessor"]
    if predecessor:
        run += ["--predecessor", predecessor]
    if sums:
        run += ["--predecessor-sums", sums]
    print("==> run.py", flush=True)
    return subprocess.run([str(a) for a in run], cwd=src, env=env).returncode


if __name__ == "__main__":
    sys.exit(main())
