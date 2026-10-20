#!/usr/bin/env python3
"""Build a hash-pinned, offline Windows ComfyUI integration ZIP.

The wheelhouse must contain only the platform-tagged abstraction-ipc wheel for
Windows x64. Nine pure-Python OA client wheels are built from the pinned
workspace source in a temporary copy. This script does not install into or
modify ComfyUI.
"""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tempfile
import csv
import zipfile


PURE_WHEELS = {
    "abstraction_asks_protocol-0.0.0-py3-none-any.whl",
    "abstraction_config_protocol-0.0.0-py3-none-any.whl",
    "abstraction_download_request_protocol-0.0.0-py3-none-any.whl",
    "abstraction_facade_client-0.0.0-py3-none-any.whl",
    "abstraction_inference_protocol-0.0.0-py3-none-any.whl",
    "abstraction_job_protocol-0.0.0-py3-none-any.whl",
    "abstraction_logging_protocol-0.0.0-py3-none-any.whl",
    "abstraction_model_protocol-0.0.0-py3-none-any.whl",
    "abstraction_resource_protocol-0.0.0-py3-none-any.whl",
    "abstraction_rights_protocol-0.0.0-py3-none-any.whl",
    "abstraction_router_protocol-0.0.0-py3-none-any.whl",
    "abstraction_storage_content-0.0.0-py3-none-any.whl",
}
PURE_SOURCES = {
    "abstraction_asks_protocol": "openabstractions-flat/abstraction-asks/py",
    # abstraction-facade-client[config] and [all] require it.
    "abstraction_config_protocol": "openabstractions-flat/abstraction-config/py",
    "abstraction_download_request_protocol": "openabstractions-flat/abstraction-download/py",
    "abstraction_facade_client": "openabstractions-flat/abstraction-facade/py",
    "abstraction_inference_protocol": "openabstractions-flat/abstraction-inference/py",
    "abstraction_job_protocol": "openabstractions-flat/abstraction-job/py",
    # ABSTRACTION_COMFYUI_LOGGING=service refuses to load without it (oa_logging.py).
    "abstraction_logging_protocol": "openabstractions-flat/abstraction-logging/py",
    "abstraction_model_protocol": "openabstractions-flat/abstraction-model/py",
    # The node's card extra: ABSTRACTION_COMFYUI_CARD=service leases through it (oa_card.py).
    "abstraction_resource_protocol": "openabstractions-flat/abstraction-resource/py",
    # abstraction-asks-protocol requires it: the asks codec imports abstraction.rights.
    "abstraction_rights_protocol": "openabstractions-flat/abstraction-rights/py",
    # OAVideoGenerate lists the models OA offers for video through router@1 Offer.
    "abstraction_router_protocol": "openabstractions-flat/abstraction-router/py",
    "abstraction_storage_content": "openabstractions-flat/abstraction-storage/py",
}
NATIVE_SOURCES = ["openabstractions-flat/abstraction-identity/py",
                  "openabstractions-flat/abstraction-identity/cpp"]


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def matches_revision(root, revision, paths):
    result = subprocess.run(["git", "-C", str(root), "diff", "--quiet", revision, "--", *paths], check=False)
    return result.returncode == 0


def untracked_source_files(root, paths):
    result = subprocess.run(["git", "-C", str(root), "status", "--porcelain=v1", "--untracked-files=all", "--", *paths],
                            check=True, capture_output=True, text=True)
    files = []
    generated_build_roots = {relative.replace("\\", "/") + "/build"
                             for relative in (*PURE_SOURCES.values(), NATIVE_SOURCES[0])}
    for line in result.stdout.splitlines():
        path = line[3:].replace("\\", "/")
        if any(path == build or path.startswith(build + "/") for build in generated_build_roots):
            continue
        if "__pycache__" in path.split("/") or path.endswith(".pyc"):
            continue
        if line[:2] == "??":
            files.append(path)
    return files


def copy_sdk_source(source, destination):
    return shutil.copytree(source, destination, ignore=shutil.ignore_patterns("build", "__pycache__", "*.pyc"))


def require_native_only_wheelhouse(wheel_paths, native_name):
    unexpected = [path.name for path in wheel_paths if path.name != native_name]
    if unexpected:
        raise ValueError("wheelhouse must contain only the pinned native IPC wheel; unexpected: " +
                         ", ".join(sorted(unexpected)))
    if not any(path.name == native_name for path in wheel_paths):
        raise ValueError(f"missing Windows x64 native wheel: {native_name}")


PUBLIC_DISTRIBUTIONS = {
    "abstraction-ipc": "abstraction_ipc",
    "abstraction-asks-protocol": "abstraction_asks_protocol",
    "abstraction-config-protocol": "abstraction_config_protocol",
    "abstraction-download-request-protocol": "abstraction_download_request_protocol",
    "abstraction-facade-client": "abstraction_facade_client",
    "abstraction-inference-protocol": "abstraction_inference_protocol",
    "abstraction-job-protocol": "abstraction_job_protocol",
    "abstraction-logging-protocol": "abstraction_logging_protocol",
    "abstraction-model-protocol": "abstraction_model_protocol",
    "abstraction-resource-protocol": "abstraction_resource_protocol",
    "abstraction-rights-protocol": "abstraction_rights_protocol",
    "abstraction-router-protocol": "abstraction_router_protocol",
    "abstraction-storage-content": "abstraction_storage_content",
}


def read_pypi_pins(path):
    rows = {}
    with Path(path).open(encoding="utf-8", newline="") as stream:
        lines = [line for line in stream if line.strip() and not line.lstrip().startswith(">")]
    reader = csv.DictReader(lines, delimiter="\t")
    if reader.fieldnames != ["package", "repository", "commit", "directory"]:
        raise ValueError("pypi.tsv must have package, repository, commit, directory columns")
    for row in reader:
        package = row["package"]
        if package in rows or package not in PUBLIC_DISTRIBUTIONS:
            raise ValueError(f"unexpected or duplicate Python package pin: {package}")
        if not re.fullmatch(r"[0-9a-f]{40}", row["commit"]):
            raise ValueError(f"invalid source commit for {package}")
        if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", row["repository"]):
            raise ValueError(f"invalid source repository for {package}")
        if row["repository"].split("/", 1)[0] != "openabstractions":
            raise ValueError(f"Python package source must be an OpenAbstractions repository: {package}")
        if not row["directory"] or None in row.values() or Path(row["directory"]).is_absolute() or ".." in Path(row["directory"]).parts:
            raise ValueError(f"invalid source directory for {package}")
        rows[package] = row
    missing = sorted(set(PUBLIC_DISTRIBUTIONS) - set(rows))
    if missing:
        raise ValueError("pypi.tsv is missing required Comfy wheels: " + ", ".join(missing))
    return rows


def verify_public_checkout(checkout, commit, directory):
    checkout = Path(checkout).resolve()
    head = subprocess.run(["git", "-C", str(checkout), "rev-parse", "HEAD"], check=True,
                          capture_output=True, text=True).stdout.strip()
    if head != commit:
        raise ValueError(f"checkout {checkout} HEAD does not match pinned commit {commit}")
    status = subprocess.run(["git", "-C", str(checkout), "status", "--porcelain=v1", "--untracked-files=all", "--", directory],
                            check=True, capture_output=True, text=True).stdout
    if status.strip():
        raise ValueError(f"pinned source directory is dirty: {checkout / directory}")
    source = checkout / directory
    if not source.exists():
        raise ValueError(f"pinned source path does not exist: {source}")


def copy_tracked_checkout(checkout, destination):
    checkout = Path(checkout).resolve()
    destination = Path(destination)
    tracked = subprocess.run(["git", "-C", str(checkout), "ls-files", "-z"], check=True,
                             capture_output=True).stdout.split(b"\0")
    for raw_path in tracked:
        if not raw_path:
            continue
        relative = Path(raw_path.decode("utf-8"))
        if relative.is_absolute() or ".." in relative.parts:
            raise ValueError(f"unsafe tracked path in public checkout: {relative}")
        source = checkout / relative
        target = destination / relative
        if source.is_symlink():
            raise ValueError(f"symlinked public package path is unsupported: {relative}")
        if source.is_file():
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, target)


def verify_wheel_archive(wheel, distribution, platform_tag):
    normalized = re.sub(r"[-_.]+", "-", distribution).lower()
    with zipfile.ZipFile(wheel) as archive:
        broken = archive.testzip()
        if broken:
            raise ValueError(f"wheel archive is corrupt: {wheel.name}:{broken}")
        metadata_paths = [name for name in archive.namelist() if name.endswith(".dist-info/METADATA")]
        wheel_paths = [name for name in archive.namelist() if name.endswith(".dist-info/WHEEL")]
        record_paths = [name for name in archive.namelist() if name.endswith(".dist-info/RECORD")]
        if len(metadata_paths) != 1 or len(wheel_paths) != 1 or len(record_paths) != 1:
            raise ValueError(f"wheel metadata layout is invalid: {wheel.name}")
        metadata = archive.read(metadata_paths[0]).decode("utf-8")
        wheel_metadata = archive.read(wheel_paths[0]).decode("ascii")
        name_lines = [line[6:].strip() for line in metadata.splitlines() if line.startswith("Name: ")]
        version_lines = [line[9:].strip() for line in metadata.splitlines() if line.startswith("Version: ")]
        if len(name_lines) != 1 or re.sub(r"[-_.]+", "-", name_lines[0]).lower() != normalized:
            raise ValueError(f"wheel distribution metadata does not match {distribution}")
        filename_prefix = distribution.replace("-", "_") + "-"
        filename_version = wheel.name[len(filename_prefix):].split("-", 1)[0] if wheel.name.startswith(filename_prefix) else ""
        if len(version_lines) != 1 or version_lines[0] != filename_version:
            raise ValueError(f"wheel version metadata does not match its filename: {wheel.name}")
        expected_tag = "py3-none-" + platform_tag
        if f"Tag: {expected_tag}" not in wheel_metadata.splitlines():
            raise ValueError(f"wheel platform tag does not match {platform_tag}: {wheel.name}")
        records = list(csv.reader(archive.read(record_paths[0]).decode("utf-8").splitlines()))
        record_map = {row[0]: row[1:] for row in records if row}
        members = set(archive.namelist())
        if set(record_map) != members:
            raise ValueError(f"wheel RECORD does not cover its exact archive members: {wheel.name}")
        for name, (encoded_hash, size) in record_map.items():
            if name == record_paths[0]:
                if encoded_hash or size:
                    raise ValueError(f"wheel RECORD self-entry is malformed: {wheel.name}")
                continue
            contents = archive.read(name)
            if not encoded_hash.startswith("sha256=") or encoded_hash[7:] != base64.urlsafe_b64encode(hashlib.sha256(contents).digest()).decode("ascii").rstrip("=") or size != str(len(contents)):
                raise ValueError(f"wheel RECORD hash or size mismatch: {wheel.name}:{name}")


def load_public_wheel_receipt(receipt_path, wheel_dir, pins, redist_source_commit, public_source_root,
                              redist_version, run_id, run_attempt):
    if not re.fullmatch(r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)", redist_version):
        raise ValueError("redist version must be unprefixed MAJOR.MINOR.PATCH")
    import py_wheels

    try:
        receipt = py_wheels.load_wheel_inputs(receipt_path, candidate_source=redist_source_commit,
                                              candidate_version=redist_version,
                                              run_id=run_id, run_attempt=run_attempt)
    except py_wheels.Refused as error:
        raise ValueError(str(error)) from error
    if not isinstance(receipt["wheels"], list) or len(receipt["wheels"]) != len(PUBLIC_DISTRIBUTIONS):
        raise ValueError("wheel receipt must contain exactly %d package wheels" % len(PUBLIC_DISTRIBUTIONS))
    wheel_root = Path(wheel_dir).resolve()
    source_root = Path(public_source_root).resolve()
    try:
        native_wheel, native_bytes, native_entry = py_wheels.verify_native_receipt_wheel(
            receipt, wheel_root, repository=pins["abstraction-ipc"]["repository"],
            commit=pins["abstraction-ipc"]["commit"], directory=pins["abstraction-ipc"]["directory"])
    except py_wheels.Refused as error:
        raise ValueError(str(error)) from error
    output = []
    seen = set()
    for entry in receipt["wheels"]:
        if not isinstance(entry, dict):
            raise ValueError("wheel receipt entry must be an object")
        name = entry["distribution"]
        if name in seen or name not in pins:
            raise ValueError(f"unexpected or duplicate receipt distribution: {name}")
        seen.add(name)
        pin = pins[name]
        if any(entry[key] != pin[key] for key in ("repository", "commit", "directory")):
            raise ValueError(f"receipt source pin differs from pypi.tsv for {name}")
        repo_name = pin["repository"].split("/")[-1]
        checkout = source_root / repo_name
        verify_public_checkout(checkout, pin["commit"], pin["directory"])
        package_version = "0.0.0"
        if name == "abstraction-ipc":
            package_version = redist_version
        expected_filename = f"{PUBLIC_DISTRIBUTIONS[name]}-{package_version}-py3-none-{'win_amd64' if name == 'abstraction-ipc' else 'any'}.whl"
        if entry["filename"] != expected_filename or Path(entry["filename"]).name != entry["filename"]:
            raise ValueError(f"unexpected wheel filename for {name}")
        if name == "abstraction-ipc":
            wheel = native_wheel
        else:
            if (type(entry.get("bytes")) is not int or entry["bytes"] <= 0 or
                    not isinstance(entry.get("sha256"), str) or not re.fullmatch(r"[0-9a-f]{64}", entry["sha256"])):
                raise ValueError(f"invalid wheel size or SHA-256 for {name}")
            wheel = wheel_root / entry["filename"]
            if not wheel.is_file() or wheel.is_symlink() or wheel.stat().st_size != entry["bytes"] or sha256(wheel) != entry["sha256"]:
                raise ValueError(f"wheel bytes do not match receipt for {name}")
            verify_wheel_archive(wheel, name, "any")
        output.append((name, wheel, entry))
    if seen != set(pins):
        raise ValueError("wheel receipt does not contain the exact pypi.tsv package set")
    return output, receipt, native_bytes


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-mode", choices=("private", "public-split"), default="private")
    parser.add_argument("--wheelhouse", type=Path, help="private diagnostic mode: native wheel only")
    parser.add_argument("--wheel-receipt", type=Path, help="public-split mode: verified Windows Python job receipt")
    parser.add_argument("--wheel-dir", type=Path, help="public-split mode: downloaded wheels named by receipt")
    parser.add_argument("--public-source-root", type=Path, help="directory containing pinned public repository checkouts")
    parser.add_argument("--redist-source-root", type=Path, help="clean redist checkout whose HEAD matches the Windows wheel receipt")
    parser.add_argument("--redist-source-commit", help="40-digit redist source commit used by the Windows wheel job")
    parser.add_argument("--redist-version", help="unprefixed candidate version from the redist workflow")
    parser.add_argument("--run-id", type=int, help="selected successful Windows Python job run id")
    parser.add_argument("--run-attempt", type=int, help="selected successful Windows Python job attempt")
    parser.add_argument("--adopter-source-root", type=Path)
    parser.add_argument("--adopter-commit", help="published adopter-comfyui source commit")
    parser.add_argument("--packager-source-commit", help="exact public abstractions commit for this producer and OA launcher")
    parser.add_argument("--producer-receipt-output", type=Path,
                        help="public mode: external same-run receipt; never included in app or source ZIP bytes")
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--source-commit", help="private diagnostic mode source pin")
    parser.add_argument("--native-source-commit", help="private diagnostic mode native source pin")
    parser.add_argument("--native-library-sha256", help="private diagnostic mode native DLL pin")
    parser.add_argument("--native-wheel-version", default="0.4.0")
    parser.add_argument("--version", default="0.4.0-candidate")
    args = parser.parse_args()

    root = Path(__file__).resolve().parents[1]
    public_mode = args.source_mode == "public-split"
    if public_mode:
        needed = (args.wheel_receipt, args.wheel_dir, args.public_source_root, args.redist_source_root,
                  args.redist_source_commit, args.redist_version, args.run_id, args.run_attempt,
                  args.adopter_source_root, args.adopter_commit, args.packager_source_commit,
                  args.producer_receipt_output)
        if not all(needed) or args.wheelhouse or args.source_commit or args.native_source_commit or args.native_library_sha256:
            parser.error("public-split requires receipt, wheel-dir, public-source-root, redist-source-root, redist-source-commit, redist-version, run-id, run-attempt, adopter-source-root and adopter-commit only")
        if any(not re.fullmatch(r"[0-9a-f]{40}", value) for value in
               (args.redist_source_commit, args.adopter_commit, args.packager_source_commit)):
            parser.error("invalid redist, adopter or packager source commit SHA")
        try:
            verify_public_checkout(root, args.packager_source_commit, ".")
            verify_public_checkout(args.redist_source_root, args.redist_source_commit, "pypi.tsv")
            public_pins = read_pypi_pins(Path(args.redist_source_root) / "pypi.tsv")
            public_wheels, receipt, native_bytes = load_public_wheel_receipt(args.wheel_receipt, args.wheel_dir,
                                                                              public_pins, args.redist_source_commit,
                                                                              args.public_source_root, args.redist_version,
                                                                              args.run_id, args.run_attempt)
            verify_public_checkout(args.adopter_source_root, args.adopter_commit, ".")
        except (ValueError, OSError, KeyError, json.JSONDecodeError, subprocess.CalledProcessError,
                zipfile.BadZipFile, UnicodeDecodeError) as error:
            parser.error(str(error))
        args.source_commit = args.redist_source_commit
        args.native_source_commit = public_pins["abstraction-ipc"]["commit"]
        args.native_library_sha256 = next(entry["native_library_sha256"] for name, _, entry in public_wheels if name == "abstraction-ipc")
    else:
        if not args.wheelhouse or not args.source_commit or not args.native_source_commit or not args.native_library_sha256:
            parser.error("private mode requires wheelhouse, source-commit, native-source-commit and native-library-sha256")
        for label, value in (("source commit", args.source_commit),
                             ("native source commit", args.native_source_commit),
                             ("native library SHA-256", args.native_library_sha256)):
            if not re.fullmatch(r"[0-9a-f]{40}" if "commit" in label else r"[0-9a-f]{64}", value):
                parser.error(f"invalid {label}: {value}")
        source_paths = ["openabstractions-flat/adopter-comfyui", *PURE_SOURCES.values()]
        untracked = untracked_source_files(root, [*source_paths, *NATIVE_SOURCES])
        if untracked:
            parser.error("untracked source files are not covered by --source-commit: " + ", ".join(untracked))
        if not matches_revision(root, args.source_commit, source_paths):
            parser.error("Comfy node or pure-Python SDK source differs from --source-commit")
        if not matches_revision(root, args.native_source_commit, NATIVE_SOURCES):
            parser.error("identity native/Python source differs from --native-source-commit")
    source = (Path(args.adopter_source_root).resolve() if public_mode else
              root / "openabstractions-flat" / "adopter-comfyui")
    notice = (source / "NOTICE" if public_mode else
              root / "research" / "org-edits" / "adopter-comfyui" / "NOTICE")
    license_file = root / "LICENSE"
    wheelhouse = args.wheelhouse.resolve() if args.wheelhouse else None
    native_name = f"abstraction_ipc-{args.native_wheel_version}-py3-none-win_amd64.whl"
    if not public_mode:
        wheel_paths = sorted(wheelhouse.glob("*.whl"))
        try:
            require_native_only_wheelhouse(wheel_paths, native_name)
        except ValueError as error:
            parser.error(str(error))
    if public_mode:
        wheels = [wheel for _, wheel, _ in public_wheels]
        native_entry = next(entry for name, _, entry in public_wheels if name == "abstraction-ipc")
        native_wheel = next(wheel for name, wheel, _ in public_wheels if name == "abstraction-ipc")
        native_library_hash = native_entry["native_library_sha256"]
    else:
        build_root = root / ".build"
        build_root.mkdir(parents=True, exist_ok=True)
        temporary = tempfile.TemporaryDirectory(prefix="comfyui-pure-wheels-", dir=build_root)
        work = Path(temporary.name)
        sources = work / "sources"
        wheel_output = work / "wheels"
        wheel_output.mkdir()
        epoch = int(subprocess.run(["git", "-C", str(root), "show", "-s", "--format=%ct", args.source_commit],
                                   check=True, capture_output=True, text=True).stdout.strip())
        env = os.environ.copy()
        env["SOURCE_DATE_EPOCH"] = str(epoch)
        env["PIP_NO_CACHE_DIR"] = "1"
        for package, relative in PURE_SOURCES.items():
            copied = sources / package
            copy_sdk_source(root / relative, copied)
            for path in sorted(copied.rglob("*"), reverse=True):
                os.utime(path, (epoch, epoch))
            os.utime(copied, (epoch, epoch))
            subprocess.run([sys.executable, "-m", "pip", "wheel", "--no-deps", "--no-build-isolation",
                            "--wheel-dir", str(wheel_output), str(copied)], check=True, env=env)
        pure = sorted(wheel_output.glob("*.whl"))
        native_wheel = wheelhouse / native_name
        if not native_wheel.is_file():
            parser.error(f"missing Windows x64 native wheel: {native_name}")
        if {p.name for p in pure} != PURE_WHEELS:
            parser.error("pure wheel build did not produce the pinned distribution set")
        wheels = pure + [native_wheel]
        native_library_hash = args.native_library_sha256
    if str(root) not in sys.path:
        sys.path.insert(0, str(root))
    try:
        from scripts.windows_native import validate_portable_windows_dll
    except ImportError as error:
        parser.error(f"shared Windows PE validator is unavailable: {error}")
    if public_mode:
        native_imports = validate_portable_windows_dll(native_bytes, "win_amd64")
    else:
        with zipfile.ZipFile(native_wheel) as archive:
            try:
                native_bytes = archive.read("abstraction/ipc/_native/abstraction_ipc.dll")
                wheel_metadata_paths = [name for name in archive.namelist()
                                        if re.fullmatch(r"abstraction_ipc-[^/]+\.dist-info/WHEEL", name)]
                if len(wheel_metadata_paths) != 1:
                    raise KeyError("expected exactly one abstraction_ipc dist-info/WHEEL")
                wheel_metadata = archive.read(wheel_metadata_paths[0]).decode("ascii")
            except KeyError as error:
                parser.error(f"native wheel is missing required platform contents: {error}")
            if hashlib.sha256(native_bytes).hexdigest() != native_library_hash:
                parser.error("native library SHA-256 does not match its pin")
            if "Tag: py3-none-win_amd64" not in wheel_metadata:
                parser.error("native IPC wheel does not declare the Windows x64 platform tag")
        native_imports = validate_portable_windows_dll(native_bytes, "win_amd64")
        subprocess.run([sys.executable, str(root / "scripts" / "py_wheels.py"), "--check", str(native_wheel)],
                       check=True)

    with tempfile.TemporaryDirectory(prefix="oa-comfyui-package-") as package_temporary:
        stage = Path(package_temporary)
        node = stage / "node" / "abstraction_downloads"
        if public_mode:
            copy_tracked_checkout(source, node)
        else:
            shutil.copytree(source, node, ignore=shutil.ignore_patterns(".git", "__pycache__", "*.pyc", "build"))
        shutil.copy2(license_file, stage / "LICENSE")
        shutil.copy2(notice, stage / "NOTICE")
        shutil.copy2(license_file, node / "LICENSE")
        shutil.copy2(notice, node / "NOTICE")
        packaged_wheels = stage / "wheels"
        packaged_wheels.mkdir()
        entries = []
        public_entry_by_filename = {entry["filename"]: entry for _, _, entry in public_wheels} if public_mode else {}
        for wheel in sorted(wheels):
            destination = packaged_wheels / wheel.name
            shutil.copy2(wheel, destination)
            entry = {"filename": wheel.name, "size": destination.stat().st_size,
                     "sha256": sha256(destination)}
            if public_mode:
                entry.update(public_entry_by_filename[wheel.name])
            else:
                entry["source_commit"] = args.source_commit
            if wheel == native_wheel:
                if not public_mode:
                    entry["source_commit"] = args.native_source_commit
                entry["native_library_sha256"] = native_library_hash
            entries.append(entry)

        requirements = []
        for entry in entries:
            parts = entry["filename"].split("-")
            requirements.append(f"{parts[0].replace('_', '-')}=={parts[1]} --hash=sha256:{entry['sha256']}")
        (stage / "requirements.lock").write_text("\n".join(requirements) + "\n", encoding="utf-8", newline="\n")

        build_root = root / ".build"
        build_root.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="comfyui-offline-verify-", dir=build_root) as verify_temporary:
            target = Path(verify_temporary) / "site-packages"
            subprocess.run([sys.executable, "-m", "pip", "install", "--no-index", "--find-links",
                            str(packaged_wheels), "--require-hashes", "--target", str(target), "-r",
                            str(stage / "requirements.lock")], check=True)
            env = os.environ.copy()
            env["PYTHONPATH"] = str(target)
            smoke = ("from abstraction.ipc import Cancellation, Library; "
                     "library = Library(); assert library._version() == 1; "
                     "signal = Cancellation(library); signal.signal(); signal.close()")
            subprocess.run([sys.executable, "-c", smoke], check=True, env=env)

        manifest = {
            "artifact": "openabstractions-comfyui-integration",
            "version": args.version,
            "source_mode": "public-split" if public_mode else "private-diagnostic",
            **({"source_commit": args.source_commit} if not public_mode else {}),
            **({"adopter_source": {
                    "repository": "openabstractions/adopter-comfyui", "commit": args.adopter_commit},
                "packager_source": {"repository": "openabstractions/abstractions",
                                    "commit": args.packager_source_commit}}
               if public_mode else {}),
            "comfyui_minimum": "0.35.0",
            "target": {"os": "windows", "architecture": "x86_64", "python": ">=3.10"},
            "license": "Apache-2.0",
            "notices": ["LICENSE", "NOTICE"],
            "build": {"python": platform.python_version(), "native_wheel_builder": "scripts/py_wheels.py",
                      "native_platform": "win_amd64", "dll_imports": list(native_imports),
                      "ipc_abi_verified": True,
                      "ipc_abi_smoke": "ABI 1 version check and cancellation allocate/signal/release"},
            "install": {"node_source": "node/abstraction_downloads",
                        "node_directory": "abstraction_downloads",
                        "destination": "ComfyUI/custom_nodes/abstraction_downloads",
                        "wheelhouse": "wheels", "requirements": "requirements.lock", "offline": True},
            "wheels": entries,
        }
        (stage / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8", newline="\n")
        output = args.output.resolve()
        output.parent.mkdir(parents=True, exist_ok=True)
        with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
            for path in sorted(p for p in stage.rglob("*") if p.is_file()):
                name = path.relative_to(stage).as_posix()
                info = zipfile.ZipInfo(name, (1980, 1, 1, 0, 0, 0))
                info.compress_type = zipfile.ZIP_DEFLATED
                info.external_attr = 0o100644 << 16
                archive.writestr(info, path.read_bytes(), compress_type=zipfile.ZIP_DEFLATED, compresslevel=9)
        with zipfile.ZipFile(output) as archive:
            broken = archive.testzip()
            if broken:
                parser.error(f"archive verification failed at {broken}")
        digest = sha256(output)
        output.with_suffix(output.suffix + ".sha256").write_text(f"{digest}  {output.name}\n", encoding="ascii")
        if public_mode:
            producer_receipt = {"schema": 1, "candidate_source": receipt["candidate_source"],
                                "candidate_version": receipt["candidate_version"],
                                "run_id": receipt["run_id"], "run_attempt": receipt["run_attempt"],
                                "wheel_receipt_sha256": sha256(args.wheel_receipt),
                                "integration": {"name": output.name, "bytes": output.stat().st_size,
                                                "sha256": digest},
                                "adopter_source": manifest["adopter_source"],
                                "packager_source": manifest["packager_source"],
                                "wheels": entries}
            args.producer_receipt_output.parent.mkdir(parents=True, exist_ok=True)
            args.producer_receipt_output.write_text(json.dumps(producer_receipt, indent=2, sort_keys=True) + "\n",
                                                    encoding="utf-8", newline="\n")
        print(json.dumps({"archive": str(output), "size": output.stat().st_size, "sha256": digest,
                          "wheels": entries}, indent=2))


if __name__ == "__main__":
    main()
