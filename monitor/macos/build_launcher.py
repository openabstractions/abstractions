"""Build the Swift executable with its same-process authenticated IPC owner."""
import argparse
import csv
import hashlib
import json
import os
import re
from pathlib import Path
import subprocess
import tempfile
from git_env import source_git_environment
from file_boundary import same_existing_file


def native_source_inputs(root, roster, provenance):
    """Bind the native source roles to the selected SDK inputs.

    This is a declared source readset, not a compiler access trace or signing
    verdict. The normal producer already verifies module-provenance.json.
    """
    if not all((root, roster, provenance)):
        raise ValueError("native launcher requires explicit source root, pypi roster and module provenance")
    for path in map(Path, (root, roster, provenance)):
        if path.is_symlink() or (hasattr(path, "is_junction") and path.is_junction()):
            raise ValueError("linked native admission input")
    root, roster, provenance = map(lambda p: Path(p).resolve(), (root, roster, provenance))
    required = {"abstraction-identity", "abstraction-facade", "abstraction-download", "abstraction-router", "abstraction-rights"}
    selected = {}
    records = list(csv.reader([line for line in roster.read_text(encoding="utf-8").splitlines() if line and not line.startswith(">")], delimiter="\t"))
    if not records or records[0] != ["package", "repository", "commit", "directory"]: raise ValueError("invalid native source roster header")
    for fields in records[1:]:
        if len(fields) != 4: raise ValueError("invalid native source roster")
        _, repository, commit, _ = fields
        name = repository.removeprefix("openabstractions/")
        if name not in required: continue
        if repository != "openabstractions/" + name or not re.fullmatch(r"[0-9a-f]{40}", commit):
            raise ValueError("invalid native repository pin")
        if name in selected and selected[name] != commit: raise ValueError("conflicting native repository pins")
        selected[name] = commit
    if set(selected) != required: raise ValueError("native source roster lacks the complete generated closure")
    evidence = json.loads(provenance.read_bytes())
    if evidence.get("kind") == "prepared-module-provenance":
        records = [(row.get("repository"), row.get("commit")) for row in evidence.get("graph", {}).get("modules", [])]
    else:
        records = [(row.get("repository"), row.get("expected_commit"))
                   for module in evidence.get("modules", []) for row in module.get("selected", [])]
    for name, commit in selected.items():
        matches = {value for repository, value in records if repository == "https://github.com/openabstractions/" + name}
        if matches != {commit}: raise ValueError("native source differs from selected module provenance: " + name)
    readset = {}
    git_environment = source_git_environment()
    for name, commit in selected.items():
        checkout = root / name
        if not checkout.is_dir() or checkout.is_symlink(): raise ValueError("missing or linked native source root: " + name)
        top = subprocess.run(["git", "-C", str(checkout), "rev-parse", "--show-toplevel"], capture_output=True, text=True, check=True, env=git_environment, timeout=30).stdout.strip()
        if not same_existing_file(top, checkout): raise ValueError("native source is not its own selected repository: " + name)
        actual = subprocess.run(["git", "-C", str(checkout), "rev-parse", "HEAD"], capture_output=True, text=True, check=True, env=git_environment, timeout=30).stdout.strip()
        if actual != commit: raise ValueError("native repository moved: " + name)
        changed = subprocess.run(["git", "-C", str(checkout), "status", "--porcelain", "--untracked-files=all", "--", "cpp", "xpc_native_darwin.c", "*.h"], capture_output=True, text=True, check=True, env=git_environment, timeout=30).stdout
        if changed.strip(): raise ValueError("native source is dirty: " + name)
        paths = list((checkout / "cpp").rglob("*"))
        if name == "abstraction-identity": paths += [checkout / "xpc_native_darwin.c", *checkout.glob("*.h")]
        for path in paths:
            if path.is_symlink(): raise ValueError("linked native source input")
            if path.is_file(): readset[path.relative_to(root).as_posix()] = hashlib.sha256(path.read_bytes()).hexdigest()
    return {"repositories": selected, "files": readset,
            "roster_sha256": hashlib.sha256(roster.read_bytes()).hexdigest(),
            "provenance_sha256": hashlib.sha256(provenance.read_bytes()).hexdigest()}


def build(source, output, arch, minimum, native_source_root=None, native_source_roster=None,
          native_provenance=None, swift_options=("-O",)):
    source, output = Path(source).resolve(), Path(output).resolve()
    here = Path(__file__).resolve().parent
    before = native_source_inputs(native_source_root, native_source_roster, native_provenance)
    launcher_files = [source, here / "NativePresence.cpp", here / "NativePresence.h", here / "NativeHost.cpp", here / "NativeHost.h", Path(__file__).resolve(), here / "git_env.py", here / "file_boundary.py"]
    launcher_pins = {path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in launcher_files}
    root = Path(native_source_root).resolve()
    for relative in ("abstraction-identity/cpp/src/client.cpp", "abstraction-identity/xpc_native_darwin.c",
                     "abstraction-facade/cpp/include/abstraction/facade/appmodel.h",
                     "abstraction-facade/cpp/src/appmodel.cpp", "abstraction-facade/cpp/src/appmodel_mcp.cpp", "abstraction-facade/cpp/src/appmodel_session.cpp",
                     "abstraction-facade/cpp/abstraction/facade/applications/rec.h",
                     "abstraction-download/cpp/abstraction/download/request/rec.h", "abstraction-router/cpp/abstraction/router/rec.h",
                     "abstraction-rights/cpp/abstraction/rights/api/rec.h"):
        if not (root / relative).is_file():
            raise FileNotFoundError("native launcher requires the reviewed C++ source closure: " + relative)
    output.parent.mkdir(parents=True, exist_ok=True)
    # Build intermediates belong to this invocation and never enter a bundle.
    with tempfile.TemporaryDirectory(prefix="oa-panel-native-build-") as temporary:
        native = Path(temporary)
        identity = root / "abstraction-identity"
        facade = root / "abstraction-facade/cpp"
        target = f"{arch}-apple-macos{minimum}"
        includes = ["-I", str(identity / "cpp/include"), "-I", str(identity / "cpp"),
                    "-I", str(facade / "include"), "-I", str(facade),
                    "-I", str(root / "abstraction-download/cpp"),
                    "-I", str(root / "abstraction-router/cpp"),
                    "-I", str(root / "abstraction-rights/cpp")]
        objects = []
        # The same native sources and link declaration as identity's CMake
        # owner. Xcode's existing compiler suffices; nothing is installed.
        for name, source_file, compiler, options in (
            ("presence", here / "NativePresence.cpp", "clang++", ["-std=c++17"]),
            ("host", here / "NativeHost.cpp", "clang++", ["-std=c++17"]),
            ("appmodel", facade / "src/appmodel.cpp", "clang++", ["-std=c++17"]),
            ("appmodel_mcp", facade / "src/appmodel_mcp.cpp", "clang++", ["-std=c++17"]),
            ("appmodel_session", facade / "src/appmodel_session.cpp", "clang++", ["-std=c++17"]),
            ("identity", identity / "cpp/src/client.cpp", "clang++", ["-std=c++17"]),
            ("xpc", identity / "xpc_native_darwin.c", "clang", ["-fblocks"]),
        ):
            object_file = native / f"{name}.o"
            subprocess.run([compiler, "-O2", "-target", target, *options, *includes,
                            "-c", str(source_file), "-o", str(object_file)], check=True)
            objects.append(str(object_file))
        links = []
        dependencies = root / "abstraction-identity/cpp/link-dependencies.txt"
        for line in dependencies.read_text(encoding="utf-8").splitlines():
            if not line.strip() or line.startswith("#"):
                continue
            platform, kind, name = line.split()
            if platform == "macos":
                links.extend(["-framework", name] if kind == "framework" else [f"-l{name}"])
        subprocess.run(["swiftc", *swift_options, "-target", f"{arch}-apple-macos{minimum}",
                        "-module-cache-path", str(native / "swift-modules"),
                        "-Xcc", "-I" + str(facade / "include"),
                        "-import-objc-header", str(here / "NativeHost.h"),
                        str(source),
                        *objects, *links, "-o", str(output)], check=True)
    if native_source_inputs(native_source_root, native_source_roster, native_provenance) != before:
        raise ValueError("native source inputs changed during compilation")
    if {path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in launcher_files} != launcher_pins:
        raise ValueError("native launcher source changed during compilation")
    before.update(launcher_files=launcher_pins, target={"arch": arch, "minimum_macos": minimum})
    output.with_name(output.name + ".native-inputs.json").write_text(json.dumps(before, sort_keys=True, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, default=Path(__file__).with_name("PanelLauncher.swift"))
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--arch", choices=("x86_64", "arm64"), required=True)
    parser.add_argument("--minimum", required=True)
    parser.add_argument("--native-source-root", type=Path, required=True)
    parser.add_argument("--native-source-roster", type=Path, required=True)
    parser.add_argument("--native-provenance", type=Path, required=True)
    args = parser.parse_args()
    build(args.source, args.output, args.arch, args.minimum, args.native_source_root,
          args.native_source_roster, args.native_provenance)
