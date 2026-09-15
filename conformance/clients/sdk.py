"""Build a fresh source-derived installed SDK for client conformance runners."""
from contextlib import contextmanager
from pathlib import Path
import subprocess
import tempfile
from workspace import layer, certify_compiler, build_root

@contextmanager
def installed_sdk(cmake, profile="aggregate", env=None):
    """Install a source-derived SDK profile into a temporary prefix under .build.

    Profiles: aggregate; jobs (facade jobs, IPC, job acceptance); jobs+request
    (jobs plus the request-only abstraction_download_request package, the prefix
    the delegation proof consumes); model; model-resolution.
    """
    if profile not in ("aggregate", "jobs", "jobs+request", "model", "model-resolution"):
        raise ValueError("unknown SDK profile")
    root = build_root()
    # Windows can hold a build file briefly after the compiler exits; a stale
    # scratch tree under .build is not a failure of the proof that used it.
    with tempfile.TemporaryDirectory(prefix="sdk-", dir=root, ignore_cleanup_errors=True) as temp:
        base = Path(temp)
        native, prefix = base / "n", base / "p"
        source = layer("abstraction-model" if profile in ("model", "model-resolution") else "abstraction-facade") / "cpp"
        # A single-configuration generator with no build type exports only a
        # noconfig target file, which an install for --config Release skips.
        flags = ["-DBUILD_SHARED_LIBS=OFF", "-DABSTRACTION_IPC_BUILD_TESTS=OFF", "-DCMAKE_BUILD_TYPE=Release"]
        flags += ["-DCMAKE_DISABLE_FIND_PACKAGE_abstraction_" + name + "=TRUE" for name in
                  ("logging", "config", "router", "model", "download_request", "ipc", "job_acceptance")]
        if profile in ("jobs", "jobs+request"):
            flags += ["-DABSTRACTION_FACADE_BUILD_AGGREGATE=OFF", "-DABSTRACTION_FACADE_BUILD_JOBS=ON"]
        subprocess.run([str(x) for x in [cmake,"-S",source,"-B",native,*flags]], cwd=root.parent, env=env, check=True, timeout=180)
        certify_compiler(native)
        for command in ([cmake,"--build",native,"--config","Release"],
                        [cmake,"--install",native,"--config","Release","--prefix",prefix]):
            subprocess.run([str(x) for x in command], cwd=root.parent, env=env, check=True, timeout=180)
        if profile == "model-resolution":
            for command in ([cmake,"-S",layer("abstraction-facade")/"cpp","-B",base/"resolution",
                             "-DABSTRACTION_FACADE_BUILD_AGGREGATE=OFF","-DABSTRACTION_FACADE_BUILD_RESOLUTION=ON",
                             "-DBUILD_SHARED_LIBS=OFF","-DABSTRACTION_IPC_BUILD_TESTS=OFF","-DCMAKE_BUILD_TYPE=Release"],
                            [cmake,"--build",base/"resolution","--config","Release"],
                            [cmake,"--install",base/"resolution","--config","Release","--prefix",prefix]):
                subprocess.run([str(x) for x in command],cwd=root.parent,env=env,check=True,timeout=180)
        if profile == "jobs+request":
            request = base / "request"
            subprocess.run([str(x) for x in [cmake, "-S", layer("abstraction-download") / "cpp", "-B", request,
                            "-DABSTRACTION_DOWNLOAD_BUILD_LEGACY=OFF", "-DABSTRACTION_BUILD_TESTS=OFF", "-DCMAKE_BUILD_TYPE=Release",
                            "-DCMAKE_PREFIX_PATH=" + str(prefix)]], cwd=root.parent, env=env, check=True, timeout=180)
            certify_compiler(request)
            for command in ([cmake, "--build", request, "--config", "Release"],
                            [cmake, "--install", request, "--config", "Release", "--prefix", prefix]):
                subprocess.run([str(x) for x in command], cwd=root.parent, env=env, check=True, timeout=180)
            if not (prefix / "include" / "abstraction" / "download" / "request" / "rec.h").is_file():
                raise RuntimeError("jobs+request SDK did not install abstraction_download_request")
        forbidden = {"jobs": ("logging", "config", "router", "model"),
                     "jobs+request": ("logging", "config", "router", "model"),
                     "model": ("facade", "logging", "config", "router", "job"),
                     "model-resolution": ("logging", "config", "router", "job", "storage", "asks", "rights")}.get(profile, ())
        for name in forbidden:
            if (prefix/"include"/"abstraction"/name).exists():
                raise RuntimeError("minimal SDK unexpectedly installed " + name)
        yield prefix
    print("temporary source-derived SDK removed:", profile)
