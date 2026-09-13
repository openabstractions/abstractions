"""Build a fresh source-derived installed SDK for client conformance runners."""
from contextlib import contextmanager
from pathlib import Path
import subprocess
import tempfile
from workspace import ROOT, layer, compiler_metadata

@contextmanager
def installed_sdk(cmake, profile="aggregate", env=None):
    if profile not in ("aggregate", "jobs", "model", "model-resolution"):
        raise ValueError("unknown SDK profile")
    build_root = ROOT / ".build"
    build_root.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="sdk-", dir=build_root) as temp:
        base = Path(temp)
        native, prefix = base / "n", base / "p"
        source = layer("abstraction-model" if profile in ("model", "model-resolution") else "abstraction-facade") / "cpp"
        flags = ["-DBUILD_SHARED_LIBS=OFF", "-DABSTRACTION_IPC_BUILD_TESTS=OFF"]
        flags += ["-DCMAKE_DISABLE_FIND_PACKAGE_abstraction_" + name + "=TRUE" for name in
                  ("logging", "config", "router", "model", "download_request", "ipc", "job_acceptance")]
        if profile == "jobs":
            flags += ["-DABSTRACTION_FACADE_BUILD_AGGREGATE=OFF", "-DABSTRACTION_FACADE_BUILD_JOBS=ON"]
        for command in ([cmake,"-S",source,"-B",native,*flags],
                        [cmake,"--build",native,"--config","Release"],
                        [cmake,"--install",native,"--config","Release","--prefix",prefix]):
            subprocess.run([str(x) for x in command], cwd=ROOT, env=env, check=True, timeout=180)
        if profile == "model-resolution":
            for command in ([cmake,"-S",layer("abstraction-facade")/"cpp","-B",base/"resolution",
                             "-DABSTRACTION_FACADE_BUILD_AGGREGATE=OFF","-DABSTRACTION_FACADE_BUILD_RESOLUTION=ON",
                             "-DBUILD_SHARED_LIBS=OFF","-DABSTRACTION_IPC_BUILD_TESTS=OFF"],
                            [cmake,"--build",base/"resolution","--config","Release"],
                            [cmake,"--install",base/"resolution","--config","Release","--prefix",prefix]):
                subprocess.run([str(x) for x in command],cwd=ROOT,env=env,check=True,timeout=180)
        compiler_metadata(native)
        forbidden = {"jobs": ("logging", "config", "router", "model"),
                     "model": ("facade", "logging", "config", "router", "job"),
                     "model-resolution": ("logging", "config", "router", "job", "storage", "asks", "rights")}.get(profile, ())
        for name in forbidden:
            if (prefix/"include"/"abstraction"/name).exists():
                raise RuntimeError("minimal SDK unexpectedly installed " + name)
        yield prefix
    print("temporary source-derived SDK removed:", profile)
