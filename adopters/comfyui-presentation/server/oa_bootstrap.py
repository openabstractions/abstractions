"""Load one private OA fixture config and run Comfy in this Python process."""

import json
import os
from pathlib import Path
import runpy
import site
import sys


MAX_CONFIG = 16 * 1024
ENVIRONMENT_KEYS = {
    "ABSTRACTION_IPC_LIBRARY",
    "ABSTRACTION_RUNTIME_ENDPOINT",
    "ABSTRACTION_RUNTIME_PROGRAM",
    "OA_COMFY_BRIDGE_ORIGIN",
    "OA_COMFY_BRIDGE_TOKEN",
    "OA_COMFY_OA_MODE",
    "PYTHONDONTWRITEBYTECODE",
    "PYTHONUTF8",
}


def load_config(filename):
    path = Path(filename)
    with path.open("rb") as source:
        raw = source.read(MAX_CONFIG + 1)
    if len(raw) > MAX_CONFIG:
        raise ValueError("OA bootstrap config exceeds 16384-byte bound")
    try:
        value = json.loads(raw)
    except (json.JSONDecodeError, UnicodeDecodeError) as exc:
        raise ValueError("OA bootstrap config is invalid JSON") from exc
    if not isinstance(value, dict) or set(value) != {
            "schema", "comfy_main", "working_directory", "arguments",
            "environment", "python_paths", "dll_directories", "venv_prefix",
            "pid_file"} or value["schema"] != 1:
        raise ValueError("OA bootstrap config has an unsupported shape")
    for name in ("comfy_main", "working_directory", "venv_prefix", "pid_file"):
        if not isinstance(value[name], str) or not value[name] or "\0" in value[name]:
            raise ValueError(f"OA bootstrap {name} is invalid")
    if not isinstance(value["arguments"], list) or len(value["arguments"]) > 64 or any(
            not isinstance(item, str) or not item or len(item.encode("utf-8")) > 4096
            for item in value["arguments"]):
        raise ValueError("OA bootstrap arguments are invalid")
    if not isinstance(value["python_paths"], list) or not 1 <= len(value["python_paths"]) <= 16 or any(
            not isinstance(item, str) or not item or "\0" in item
            for item in value["python_paths"]):
        raise ValueError("OA bootstrap Python paths are invalid")
    if not isinstance(value["dll_directories"], list) or len(value["dll_directories"]) > 8 or any(
            not isinstance(item, str) or not item or "\0" in item
            or len(item.encode("utf-8")) > 4096 for item in value["dll_directories"]):
        raise ValueError("OA bootstrap DLL directories are invalid")
    environment = value["environment"]
    if not isinstance(environment, dict) or not set(environment).issubset(ENVIRONMENT_KEYS) or any(
            not isinstance(item, str) or not item or "\0" in item or len(item.encode("utf-8")) > 4096
            for item in environment.values()):
        raise ValueError("OA bootstrap environment is invalid")
    return value


def run(config):
    main = Path(config["comfy_main"])
    working = Path(config["working_directory"])
    prefix = Path(config["venv_prefix"])
    if not main.is_file() or not working.is_dir() or not prefix.is_dir():
        raise ValueError("OA bootstrap dependency is unavailable")
    python_paths = [str(Path(item)) for item in config["python_paths"]]
    if any(not Path(item).is_dir() for item in python_paths):
        raise ValueError("OA bootstrap Python path is unavailable")
    dll_directories = [Path(item) for item in config["dll_directories"]]
    if any(not item.is_dir() for item in dll_directories):
        raise ValueError("OA bootstrap DLL directory is unavailable")
    if dll_directories and os.name != "nt":
        raise ValueError("OA bootstrap DLL directories require Windows")
    # add_dll_directory returns a closeable search-cookie. Retain every cookie
    # until Comfy exits so delayed ctypes imports use the same trusted search.
    dll_handles = [os.add_dll_directory(str(item.resolve()))
                   for item in dll_directories]
    os.environ.update(config["environment"])
    os.environ["PYTHONPATH"] = os.pathsep.join(python_paths)
    sys.prefix = str(prefix)
    sys.exec_prefix = str(prefix)
    for item in reversed(python_paths):
        site.addsitedir(item)
        if item in sys.path:
            sys.path.remove(item)
        sys.path.insert(0, item)
    # Match `python main.py`: Comfy imports packages beside its entry point.
    source_root = str(main.parent.resolve())
    if source_root in sys.path:
        sys.path.remove(source_root)
    sys.path.insert(0, source_root)
    pid_file = Path(config["pid_file"])
    descriptor = os.open(pid_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="ascii") as output:
        output.write(str(os.getpid()))
    log = (working / "server.log").open("a", encoding="utf-8", buffering=1)
    sys.stdout = log
    sys.stderr = log
    os.chdir(working)
    sys.argv = [str(main), *config["arguments"]]
    runpy.run_path(str(main), run_name="__main__")


def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: oa_bootstrap.py <private-config.json>")
    run(load_config(sys.argv[1]))


if __name__ == "__main__":
    main()
