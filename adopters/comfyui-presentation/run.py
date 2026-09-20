"""Run or prepare an isolated ComfyUI presentation fixture using existing dependencies."""
import argparse
import ctypes
from ctypes import wintypes
import json
import os
from pathlib import Path
import shutil
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.request


def python_environment(python):
    probe = r'''import ctypes,json,os,site,sys
from ctypes import wintypes
program=os.path.realpath(getattr(sys,"_base_executable",sys.executable))
if os.name=="nt":
    kernel=ctypes.WinDLL("kernel32",use_last_error=True)
    kernel.GetCurrentProcess.restype=wintypes.HANDLE
    kernel.QueryFullProcessImageNameW.argtypes=[wintypes.HANDLE,wintypes.DWORD,wintypes.LPWSTR,ctypes.POINTER(wintypes.DWORD)]
    kernel.QueryFullProcessImageNameW.restype=wintypes.BOOL
    handle=kernel.GetCurrentProcess()
    size=wintypes.DWORD(32768)
    buffer=ctypes.create_unicode_buffer(size.value)
    if not kernel.QueryFullProcessImageNameW(handle,0,buffer,ctypes.byref(size)):
        raise ctypes.WinError()
    program=buffer.value
print(json.dumps({"program":program,"prefix":sys.prefix,"site_packages":site.getsitepackages()}))'''
    completed = subprocess.run([str(python.resolve()), "-c", probe], check=True,
                               capture_output=True, text=True, timeout=15)
    value = json.loads(completed.stdout)
    program, prefix = Path(value["program"]).resolve(), Path(value["prefix"]).resolve()
    sites = [Path(item).resolve() for item in value["site_packages"] if Path(item).is_dir()]
    if not program.is_file() or not prefix.is_dir() or not sites:
        raise RuntimeError("could not resolve the Comfy Python program and environment")
    return program, prefix, sites


def verify_base_imports(program, prefix, python_paths, library, dll_directories):
    code = r'''import json,os,site,sys
settings=json.loads(sys.argv[1])
prefix=settings["prefix"]
sys.prefix=sys.exec_prefix=prefix
handles=[os.add_dll_directory(item) for item in settings["dll_directories"]]
for item in reversed(settings["python_paths"]):
    site.addsitedir(item)
    if item in sys.path: sys.path.remove(item)
    sys.path.insert(0,item)
import aiohttp,torch
from abstraction.ipc import Library
native=Library(settings["library"])
print(json.dumps({"aiohttp":aiohttp.__version__,"torch":torch.__version__,"ipc":native._version()}))'''
    settings = json.dumps({
        "prefix": str(prefix), "python_paths": [str(item) for item in python_paths],
        "library": str(library), "dll_directories": [str(item) for item in dll_directories],
    }, separators=(",", ":"))
    completed = subprocess.run([str(program), "-I", "-c", code, settings], check=True,
                               capture_output=True, text=True, timeout=30)
    return json.loads(completed.stdout)


def write_private_json(path, value):
    raw = json.dumps(value, separators=(",", ":"))
    if len(raw.encode("utf-8")) > 16 * 1024:
        raise ValueError("OA bootstrap config exceeds 16384-byte bound")
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as output:
        output.write(raw)


def process_image(pid):
    if os.name == "nt":
        kernel = ctypes.WinDLL("kernel32", use_last_error=True)
        kernel.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
        kernel.OpenProcess.restype = wintypes.HANDLE
        kernel.QueryFullProcessImageNameW.argtypes = [
            wintypes.HANDLE, wintypes.DWORD, wintypes.LPWSTR,
            ctypes.POINTER(wintypes.DWORD)]
        kernel.QueryFullProcessImageNameW.restype = wintypes.BOOL
        kernel.CloseHandle.argtypes = [wintypes.HANDLE]
        handle = kernel.OpenProcess(0x1000, False, pid)
        if not handle:
            return None
        try:
            size = wintypes.DWORD(32768)
            buffer = ctypes.create_unicode_buffer(size.value)
            if not kernel.QueryFullProcessImageNameW(
                    handle, 0, buffer, ctypes.byref(size)):
                return None
            return Path(buffer.value).resolve()
        finally:
            kernel.CloseHandle(handle)
    try:
        return Path(os.readlink(f"/proc/{pid}/exe")).resolve()
    except OSError:
        return None


def stop_pid(pid, expected_program):
    if process_image(pid) != expected_program:
        return
    if os.name == "nt":
        subprocess.run(["taskkill", "/PID", str(pid), "/T", "/F"], check=True,
                       capture_output=True, timeout=10,
                       creationflags=subprocess.CREATE_NO_WINDOW)
    else:
        os.kill(pid, 15)


def wait_ready(url, deadline, *, process=None, pid_file=None):
    while time.monotonic() < deadline:
        if process is not None and process.poll() is not None:
            return False
        if pid_file is not None and not pid_file.exists():
            time.sleep(.1)
            continue
        try:
            with urllib.request.urlopen(url + "/system_stats", timeout=1):
                return True
        except OSError:
            time.sleep(.2)
    return False


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", action="store_true", help="start or prepare the bounded fixture")
    parser.add_argument("--python", type=Path, help="existing ComfyUI environment Python")
    parser.add_argument("--comfy", type=Path, help="existing ComfyUI source root")
    parser.add_argument("--frontend", type=Path, help="existing frontend static directory")
    parser.add_argument("--seconds", type=int, default=900, help="maximum fixture lifetime, 1..1800")
    parser.add_argument("--bridge", action="store_true", help="stage the local preview/read bridge")
    parser.add_argument("--oa", action="store_true", help="announce through the native OA application directory")
    parser.add_argument("--activation-fixture", action="store_true",
                        help="prepare an OA activation recipe and wait without launching it")
    parser.add_argument("--oa-library", type=Path, help="existing native abstraction IPC library")
    parser.add_argument("--oa-dll-directory", action="append", type=Path, default=[],
                        help="existing trusted Windows DLL dependency directory; repeat as needed")
    parser.add_argument("--oa-python-path", action="append", type=Path, default=[],
                        help="existing OA Python package root; repeat as needed")
    parser.add_argument("--oa-runtime-endpoint", help="optional exact local runtime endpoint")
    parser.add_argument("--oa-runtime-program", type=Path,
                        help="exact runtime program required at --oa-runtime-endpoint")
    args = parser.parse_args()
    if not args.run:
        parser.print_help()
        return
    if not args.python or not args.comfy or not args.frontend or not 1 <= args.seconds <= 1800:
        parser.error("--run requires --python, --comfy, --frontend and 1..1800 seconds")
    for path in (args.python, args.comfy / "main.py", args.frontend / "index.html"):
        if not path.is_file():
            parser.error(f"missing existing dependency: {path}")
    if args.oa:
        if not args.bridge or not args.oa_library or not args.oa_python_path:
            parser.error("--oa requires --bridge, --oa-library and at least one --oa-python-path")
        if not args.oa_library.is_file():
            parser.error(f"missing existing OA native library: {args.oa_library}")
        if len(args.oa_dll_directory) > 8:
            parser.error("--oa-dll-directory may be repeated at most 8 times")
        if args.oa_dll_directory and os.name != "nt":
            parser.error("--oa-dll-directory is supported only on Windows")
        for path in args.oa_dll_directory:
            if not path.is_dir():
                parser.error(f"missing OA DLL directory: {path}")
        for path in args.oa_python_path:
            if not path.is_dir():
                parser.error(f"missing existing OA Python package root: {path}")
        if bool(args.oa_runtime_endpoint) != bool(args.oa_runtime_program):
            parser.error("--oa-runtime-endpoint and --oa-runtime-program must be given together")
        if args.oa_runtime_program and not args.oa_runtime_program.is_file():
            parser.error(f"missing OA runtime program: {args.oa_runtime_program}")
    elif (args.oa_library or args.oa_dll_directory or args.oa_python_path or args.oa_runtime_endpoint
          or args.oa_runtime_program or args.activation_fixture):
        parser.error("OA paths, endpoint and activation fixture require --oa")

    temporary_root = Path(tempfile.gettempdir()).resolve()
    base = Path(tempfile.mkdtemp(prefix="oa-comfy-presentation-", dir=temporary_root)).resolve()
    process = None
    activation_pid = None
    expected_program = None
    pid_file = None
    try:
        source = Path(__file__).resolve().parent
        addon = base / "custom_nodes" / "oa_presentation"
        addon.mkdir(parents=True)
        shutil.copy2(source / "__init__.py", addon / "__init__.py")
        shutil.copytree(source / "web", addon / "web")
        if args.bridge:
            shutil.copytree(source / "server", addon / "server",
                            ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        origin = f"http://127.0.0.1:{port}"
        child_env = {**os.environ, "PYTHONDONTWRITEBYTECODE": "1", "PYTHONUTF8": "1"}
        for name in ("OA_COMFY_BRIDGE_TOKEN", "OA_COMFY_BRIDGE_ORIGIN", "OA_COMFY_OA_MODE"):
            child_env.pop(name, None)
        if args.bridge:
            token_file = base / "bridge-token.txt"
            descriptor = os.open(token_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(descriptor, "w", encoding="utf-8") as output:
                output.write(secrets.token_urlsafe(32))
            child_env["OA_COMFY_BRIDGE_TOKEN"] = token_file.read_text(encoding="utf-8")
            child_env["OA_COMFY_BRIDGE_ORIGIN"] = origin

        comfy_arguments = ["--cpu", "--listen", "127.0.0.1", "--port", str(port),
                           "--base-directory", str(base), "--front-end-root", str(args.frontend.resolve()),
                           "--disable-auto-launch", "--disable-api-nodes"]
        command = [str(args.python.resolve()), str((args.comfy / "main.py").resolve()), *comfy_arguments]
        if args.oa:
            expected_program, venv_prefix, site_packages = python_environment(args.python)
            python_paths = [*site_packages, *[path.resolve() for path in args.oa_python_path]]
            dll_directories = [path.resolve() for path in args.oa_dll_directory]
            imports = verify_base_imports(
                expected_program, venv_prefix, python_paths,
                args.oa_library.resolve(), dll_directories)
            environment = {
                "OA_COMFY_OA_MODE": "1",
                "OA_COMFY_BRIDGE_TOKEN": child_env["OA_COMFY_BRIDGE_TOKEN"],
                "OA_COMFY_BRIDGE_ORIGIN": origin,
                "ABSTRACTION_IPC_LIBRARY": str(args.oa_library.resolve()),
                "PYTHONDONTWRITEBYTECODE": "1",
                "PYTHONUTF8": "1",
            }
            if args.oa_runtime_endpoint:
                environment["ABSTRACTION_RUNTIME_ENDPOINT"] = args.oa_runtime_endpoint
                environment["ABSTRACTION_RUNTIME_PROGRAM"] = str(
                    args.oa_runtime_program.resolve())
            bootstrap = addon / "server" / "oa_bootstrap.py"
            config_file = base / "oa-bootstrap.json"
            pid_file = base / "oa-bootstrap.pid"
            write_private_json(config_file, {
                "schema": 1, "comfy_main": str((args.comfy / "main.py").resolve()),
                "working_directory": str(base), "arguments": comfy_arguments,
                "environment": environment, "python_paths": [str(path) for path in python_paths],
                "dll_directories": [str(path) for path in dll_directories],
                "venv_prefix": str(venv_prefix), "pid_file": str(pid_file),
            })
            command = [str(expected_program), "-I", str(bootstrap), str(config_file)]
            for name in ("OA_COMFY_BRIDGE_TOKEN", "OA_COMFY_BRIDGE_ORIGIN", "OA_COMFY_OA_MODE",
                         "ABSTRACTION_IPC_LIBRARY", "ABSTRACTION_RUNTIME_ENDPOINT",
                         "ABSTRACTION_RUNTIME_PROGRAM", "PYTHONPATH"):
                child_env.pop(name, None)
            print("OA Python environment: " + json.dumps({
                "program": str(expected_program), "venv_prefix": str(venv_prefix),
                "aiohttp": imports["aiohttp"], "torch": imports["torch"],
                "ipc": imports["ipc"],
            }), flush=True)

        print(f"Temporary fixture: {base}", flush=True)
        if args.bridge:
            print(f"Bridge credential file: {token_file}", flush=True)
        if args.activation_fixture:
            print("Activation descriptor: " + json.dumps({
                "name": "comfyui", "program": str(expected_program)}), flush=True)
            print("Activation arguments: " + json.dumps(command[1:]), flush=True)
            deadline = time.monotonic() + args.seconds
            ready_reported = False
            while time.monotonic() < deadline:
                if pid_file.exists() and activation_pid is None:
                    try:
                        activation_pid = int(pid_file.read_text(encoding="ascii"))
                    except (OSError, ValueError):
                        raise RuntimeError("invalid activation PID file")
                if not ready_reported and wait_ready(origin, min(deadline, time.monotonic() + .25), pid_file=pid_file):
                    print(f"Ready after OA activation: {origin}", flush=True)
                    ready_reported = True
                if activation_pid is not None and process_image(activation_pid) is None:
                    break
                time.sleep(.1)
        else:
            with (base / "server.log").open("wb") as log:
                process = subprocess.Popen(command, cwd=base, stdout=log, stderr=subprocess.STDOUT,
                                           env=child_env,
                                           creationflags=(subprocess.CREATE_NO_WINDOW | subprocess.CREATE_NEW_PROCESS_GROUP) if os.name == "nt" else 0)
                if not wait_ready(origin, time.monotonic() + 60, process=process):
                    raise RuntimeError((base / "server.log").read_text(errors="replace")[-12000:])
                print(f"Ready: {origin} — Ctrl+C stops and removes the fixture", flush=True)
                try:
                    process.wait(timeout=args.seconds)
                except subprocess.TimeoutExpired:
                    pass
    finally:
        if (activation_pid is None and pid_file is not None
                and expected_program is not None and args.activation_fixture):
            deadline = time.monotonic() + 1
            while time.monotonic() < deadline and not pid_file.exists():
                time.sleep(.05)
            if pid_file.exists():
                try:
                    activation_pid = int(pid_file.read_text(encoding="ascii"))
                except (OSError, ValueError):
                    pass
        if activation_pid is not None and expected_program is not None:
            stop_pid(activation_pid, expected_program)
        if process is not None and process.poll() is None:
            if os.name == "nt":
                stopped = subprocess.run(["taskkill", "/PID", str(process.pid), "/T", "/F"],
                                         capture_output=True, timeout=10,
                                         creationflags=subprocess.CREATE_NO_WINDOW)
                if stopped.returncode and process.poll() is None:
                    raise RuntimeError("could not stop fixture process tree")
            else:
                process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=10)
        if base.parent != temporary_root or not base.name.startswith("oa-comfy-presentation-"):
            raise RuntimeError(f"unexpected cleanup target: {base}")
        shutil.rmtree(base)
        print("Fixture removed", flush=True)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        pass
