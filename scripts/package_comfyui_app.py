#!/usr/bin/env python3
"""Repack the pinned official ComfyUI portable release for the OA ZIP installer.

This is a release-build tool. Its only production input is the exact upstream
v0.35.0 asset below; it never accepts an archive or extraction from a browser.
"""

from __future__ import annotations

import argparse
import base64
import csv
from email.parser import Parser
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import time
import zipfile

if __package__:
    from .file_boundary import extended_path as _extended, owned_temporary_tree
    from . import wheel_records
else:
    from file_boundary import extended_path as _extended, owned_temporary_tree
    import wheel_records


SOURCE_URL = "https://github.com/Comfy-Org/ComfyUI/releases/download/v0.35.0/ComfyUI_windows_portable_amd.7z"
SOURCE_COMMIT = "40c4fcdf513a4523e39d54a9d391908af8df8171"
SOURCE_CODE_URL = f"https://github.com/Comfy-Org/ComfyUI/archive/{SOURCE_COMMIT}.tar.gz"
FRONTEND_VERSION = "1.51.10"
FRONTEND_COMMIT = "e7d1c7fc6823e330fdab524610b0000394cb1dbc"
FRONTEND_SOURCE_URL = f"https://github.com/Comfy-Org/ComfyUI_frontend/archive/{FRONTEND_COMMIT}.tar.gz"
EMBEDDED_DOCS_COMMIT = "de2ae9b048bc873a613f82da52250ed76a2ccf5d"
PYGIT2_COMMIT = "ba21cf58151ec7fb5d15152e952040cef21a4009"
LIBGIT2_COMMIT = "26055f5af74ab1cf636d272e8a34315496d3f06f"
ADDITIONAL_SOURCES = {
    "embedded-docs": (EMBEDDED_DOCS_COMMIT, f"https://github.com/Comfy-Org/embedded-docs/archive/{EMBEDDED_DOCS_COMMIT}.tar.gz"),
    "pygit2": (PYGIT2_COMMIT, f"https://github.com/libgit2/pygit2/archive/{PYGIT2_COMMIT}.tar.gz"),
    "libgit2": (LIBGIT2_COMMIT, f"https://github.com/libgit2/libgit2/archive/{LIBGIT2_COMMIT}.tar.gz"),
}
SOURCE_SHA256 = "2fa7d6538d9d0cfa1cdc64debda8c49886e4745043f7806ea45e87f6bd1daa87"
SOURCE_BYTES = 1_580_620_142
INTEGRATION_SHA256 = "364fa7683e7134c9b49b73bf2834d721583b78efe69dbcc59466e90b400a5439"
INTEGRATION_BYTES = 538_485
INTEGRATION_SOURCE_COMMIT = "4de25c3b4d9dd3756f8ae4fa91e2bad93ae0c36b"
INTEGRATION_NATIVE_COMMIT = "f427ece111bb5989f8d36d381f01a022b8355563"
INTEGRATION_PACKAGER = Path(__file__).resolve().with_name("package_comfyui_integration.py")
INTEGRATION_REMEDY = (
    "rebuild it with scripts/package_comfyui_integration.py (it needs the Windows abstraction_ipc wheel), "
    "then replace INTEGRATION_SHA256, INTEGRATION_BYTES, INTEGRATION_SOURCE_COMMIT and INTEGRATION_NATIVE_COMMIT")
CPU_WHEELS = {
    "torch-2.9.1+cpu-cp312-cp312-win_amd64.whl": (110_856_378, "a4e06b4f441675d26b462123c8a83e77c55f1ec8ebc081203be2db1ea8054add", "torch==2.9.1+cpu"),
    "torchvision-0.24.1+cpu-cp312-cp312-win_amd64.whl": (4_302_124, "af904a34b563c302fa4583a4746f3108bafdc5823a67c912d6023a8fe9b01c51", "torchvision==0.24.1+cpu"),
    "torchaudio-2.9.1+cpu-cp312-cp312-win_amd64.whl": (663_086, "53dd3155b794019e8de2dc49d044f410e6bce081d23a0e6548c156f37dfc9d0d", "torchaudio==2.9.1+cpu"),
}
CPU_WHEEL_PROVENANCE = {
    "torch": ("https://download.pytorch.org/whl/cpu/torch-2.9.1%2Bcpu-cp312-cp312-win_amd64.whl", "https://github.com/pytorch/pytorch/commit/d38164a545b4a4e4e0cf73ce67173f70574890b6", "d38164a545b4a4e4e0cf73ce67173f70574890b6", "BSD-3-Clause", ("LICENSE", "NOTICE")),
    "torchvision": ("https://download.pytorch.org/whl/cpu/torchvision-0.24.1%2Bcpu-cp312-cp312-win_amd64.whl", "https://github.com/pytorch/vision/commit/d801a34632023859a0a274803d6abaf0a45d77a5", "d801a34632023859a0a274803d6abaf0a45d77a5", "BSD-3-Clause", ("LICENSE",)),
    "torchaudio": ("https://download.pytorch.org/whl/cpu/torchaudio-2.9.1%2Bcpu-cp312-cp312-win_amd64.whl", "https://github.com/pytorch/audio/commit/a224ab24a7f4797f6707051257265e223e12576f", "a224ab24a7f4797f6707051257265e223e12576f", "BSD-2-Clause", ("licenses/LICENSE",)),
}
GPU_DISTS = (
    "torch-2.9.1+rocm7.2.1.dist-info", "torchvision-0.24.1+rocm7.2.1.dist-info",
    "torchaudio-2.9.1+rocm7.2.1.dist-info", "rocm-7.2.1.dist-info",
    "rocm_sdk_core-7.2.1.dist-info", "rocm_sdk_libraries_custom-7.2.1.dist-info",
)
ARCHIVE_ROOT = "ComfyUI_windows_portable"
MAX_EXPANDED_BYTES = 8 << 30
MAX_RELEASE_ASSET_BYTES = 2 << 30
MIN_FREE_AFTER_BUILD_BYTES = 20 << 30
PROVENANCE = "OA-PROVENANCE.json"
# Both OA launch paths start ComfyUI through this file (adopters/comfyui).
LAUNCHER_SOURCE = Path(__file__).resolve().parent.parent / "adopters" / "comfyui" / "oa_comfyui_launch.py"
LAUNCHER_NAME = "oa_comfyui_launch.py"
_ZIP_TIME = (1980, 1, 1, 0, 0, 0)
_NOTICE_NAMES = ("license", "licence", "copying", "notice")


def required_free_bytes(*, minimum_free_after_build_bytes: int = MIN_FREE_AFTER_BUILD_BYTES,
                        source_bytes: int = SOURCE_BYTES,
                        max_expanded_bytes: int = MAX_EXPANDED_BYTES,
                        max_release_asset_bytes: int = MAX_RELEASE_ASSET_BYTES,
                        wheel_bytes: int = sum(size for size, _, _ in CPU_WHEELS.values())) -> int:
    """Worst-case build space beyond already-present downloaded inputs."""
    if minimum_free_after_build_bytes < 1 << 30:
        raise ValueError("free-space floor must be at least one GiB")
    working_margin = min(2 << 30, max_expanded_bytes)
    return (minimum_free_after_build_bytes + source_bytes + max_expanded_bytes +
            2 * max_release_asset_bytes + 2 * wheel_bytes + working_margin)


def _sha256(path: Path) -> tuple[int, str]:
    digest = hashlib.sha256()
    size = 0
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1 << 20), b""):
            size += len(block)
            digest.update(block)
    return size, digest.hexdigest()


def _snapshot_verified_source(path: Path, snapshot: Path, expected_size: int, expected_sha256: str) -> None:
    if path.is_symlink() or not path.is_file():
        raise ValueError("source must be a regular file")
    digest = hashlib.sha256()
    size = 0
    with path.open("rb") as source, snapshot.open("xb") as target:
        if not stat.S_ISREG(os.fstat(source.fileno()).st_mode):
            raise ValueError("source must be a regular file")
        for block in iter(lambda: source.read(1 << 20), b""):
            size += len(block)
            if size > expected_size:
                raise ValueError("official archive size mismatch")
            digest.update(block)
            target.write(block)
    if size != expected_size or digest.hexdigest() != expected_sha256:
        raise ValueError("official archive size or SHA-256 mismatch")


def _safe_name(name: str) -> None:
    if not name or name in (".", "..") or name.endswith(".") or name.endswith(" "):
        raise ValueError(f"unsafe archive member name: {name!r}")
    if any(char in name for char in '<>:"/\\|?*\0'):
        raise ValueError(f"unsafe archive member name: {name!r}")
    if name.split(".")[0].casefold() in {"con", "prn", "aux", "nul", *(f"com{i}" for i in range(1, 10)), *(f"lpt{i}" for i in range(1, 10))}:
        raise ValueError(f"Windows device archive member name: {name!r}")


def _register_name(name: str, seen: set[str]) -> None:
    folded = name.casefold()
    if folded in seen:
        raise ValueError(f"case-insensitive duplicate: {name}")
    seen.add(folded)


def _listed_members(archive: Path, root: str, tar: str) -> set[str]:
    """Reject unsafe names before bsdtar extracts this exact-hash asset.

    The fixed SHA is the archive authority. bsdtar's default extraction also
    refuses parent traversal and link-based destination escapes; never pass -P.
    """
    version = subprocess.run([tar, "--version"], check=True, capture_output=True, text=True, timeout=30)
    if "bsdtar" not in version.stdout.lower():
        raise ValueError("release extraction requires bsdtar")
    completed = subprocess.run([tar, "-tf", str(archive)], check=True, capture_output=True, text=True, timeout=300)
    names: set[str] = set()
    seen: set[str] = set()
    for raw in completed.stdout.splitlines():
        name = raw.rstrip("/").replace("\\", "/")
        parts = name.split("/")
        if not name or parts[0] != root or any(not part for part in parts):
            raise ValueError(f"unsafe archive member path: {raw!r}")
        for part in parts:
            # The official AMD payload has 3,666 Triton names containing '*'.
            # Windows bsdtar maps these to full-width characters on extraction.
            # The exact source hash and the post-extraction Windows-name check
            # bind this known transformation; path structure remains strict.
            if part in (".", "..") or "\0" in part or any(ord(char) < 32 for char in part):
                raise ValueError(f"unsafe archive member path: {raw!r}")
        _register_name(name, seen)
        names.add(name)
    if not names:
        raise ValueError("archive lacks expected portable tree")
    for name in tuple(names):
        pieces = name.split("/")
        names.update("/".join(pieces[:i]) for i in range(1, len(pieces)))
    return names


def _scan_tree(stage: Path, archive_root: str, max_bytes: int) -> tuple[list[tuple[str, Path]], list[str], int, list[str]]:
    roots = list(stage.iterdir())
    if len(roots) != 1 or roots[0].name != archive_root:
        raise ValueError("extraction has an unexpected top-level tree")
    files: list[tuple[str, Path]] = []
    dirs: list[str] = []
    notices: list[str] = []
    seen: set[str] = set()
    total = 0
    pending = [roots[0]]
    while pending:
        current = pending.pop()
        relative = current.relative_to(stage)
        parts = relative.parts
        for part in parts:
            _safe_name(part)
        name = relative.as_posix()
        _register_name(name, seen)
        info = current.lstat()
        if stat.S_ISLNK(info.st_mode) or getattr(info, "st_file_attributes", 0) & getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0):
            raise ValueError(f"link/reparse point in portable tree: {name}")
        if stat.S_ISDIR(info.st_mode):
            dirs.append(name)
            pending.extend(current.iterdir())
            continue
        if not stat.S_ISREG(info.st_mode):
            raise ValueError(f"non-file portable member: {name}")
        total += info.st_size
        if total > max_bytes:
            raise ValueError("portable tree exceeds expanded-byte ceiling")
        if any(word in current.name.casefold() for word in _NOTICE_NAMES):
            notices.append(name)
        files.append((name, current))
    if PROVENANCE.casefold() in seen or ".oa-install.json" in seen:
        raise ValueError("portable tree collides with reserved metadata")
    files.sort(key=lambda entry: entry[0])
    return files, sorted(dirs), total, sorted(notices)


def _write_zip_file(archive: zipfile.ZipFile, name: str, data: bytes | Path) -> None:
    entry = zipfile.ZipInfo(name, _ZIP_TIME)
    entry.compress_type = zipfile.ZIP_DEFLATED
    entry.external_attr = (0o100644 << 16)
    if isinstance(data, bytes):
        archive.writestr(entry, data, compress_type=zipfile.ZIP_DEFLATED, compresslevel=9)
    else:
        with data.open("rb") as source, archive.open(entry, "w", force_zip64=True) as target:
            shutil.copyfileobj(source, target, 1 << 20)


def _write_zip_dir(archive: zipfile.ZipFile, name: str) -> None:
    entry = zipfile.ZipInfo(name + "/", _ZIP_TIME)
    entry.external_attr = 0o40755 << 16
    archive.writestr(entry, b"")


def _git_head(checkout: Path) -> str:
    return subprocess.check_output(["git", "-C", str(checkout), "rev-parse", "HEAD"], text=True, timeout=30).strip()


def _private_python_env() -> dict[str, str]:
    env = os.environ.copy()
    for inherited in ("PYTHONPATH", "PYTHONHOME", "PIP_TARGET", "PIP_PREFIX", "PIP_USER"):
        env.pop(inherited, None)
    env.update(PYTHONDONTWRITEBYTECODE="1", PYTHONNOUSERSITE="1", PIP_NO_CACHE_DIR="1",
               PIP_CONFIG_FILE=os.devnull, PIP_DISABLE_PIP_VERSION_CHECK="1")
    return env


def _recorded_files(portable: Path, wanted: set[str], label: str, *, missing_bytecode_ok: bool = False) -> list[Path]:
    """Audit exact wheel ownership before removing the named distributions."""
    site = portable / "python_embeded" / "Lib" / "site-packages"
    available = {path.name for path in site.glob("*.dist-info")}
    if not wanted <= available:
        raise ValueError(f"official portable {label} distribution set differs from pin")
    ownership: dict[str, set[str]] = {}
    candidates: dict[str, Path] = {}
    portable_root = portable.resolve()
    for dist in site.glob("*.dist-info"):
        record = dist / "RECORD"
        if not record.is_file():
            if dist.name in wanted:
                raise ValueError(f"{label} distribution lacks RECORD")
            continue
        with record.open(encoding="utf-8", newline="") as stream:
            for row in csv.reader(stream):
                if not row or not row[0]:
                    raise ValueError("invalid installed wheel RECORD")
                member = (site / Path(*row[0].split("/"))).resolve()
                if not member.is_relative_to(portable_root):
                    raise ValueError("wheel RECORD path escapes portable tree")
                key = str(member).casefold()
                ownership.setdefault(key, set()).add(dist.name)
                if dist.name in wanted:
                    if missing_bytecode_ok and member.suffix == ".pyc" and not row[1] and not member.exists():
                        continue
                    if not member.is_file() or member.is_symlink():
                        raise ValueError(f"{label} wheel RECORD file is missing or linked")
                    candidates[key] = member
    for key in candidates:
        if ownership[key] - wanted:
            raise ValueError(f"{label} wheel RECORD path is shared with retained distribution")
    return list(candidates.values())


def _recorded_gpu_files(portable: Path) -> list[Path]:
    return _recorded_files(portable, set(GPU_DISTS), "GPU")


def _remove_paths(portable: Path, targets: list[Path]) -> dict[str, int]:
    removed_bytes = sum(path.stat().st_size for path in targets)
    parents: set[Path] = set()
    for path in targets:
        path.unlink()
        parents.update(parent for parent in path.parents if parent.is_relative_to(portable) and parent != portable)
    for parent in sorted(parents, key=lambda path: len(path.parts), reverse=True):
        try:
            parent.rmdir()
        except OSError:
            pass  # Retained distributions and other files keep their parent.
    return {"files": len(targets), "bytes": removed_bytes}


def _remove_gpu_files(portable: Path) -> dict[str, int]:
    return _remove_paths(portable, _recorded_gpu_files(portable))


def _launcher():
    """The launch check both OA launch paths run, loaded from the adopter source."""
    spec = importlib.util.spec_from_file_location("oa_comfyui_launch", LAUNCHER_SOURCE)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _remove_installers(portable: Path, work: Path) -> list[str]:
    """Remove pip and ensurepip: no install or build step may run in this Python.

    Security review rocm-7.2.1-pytorch-2.9.1-python-3.12.10, condition 2. pip
    leaves by its own RECORD, after the build's last pip install.
    """
    _require_owned_stage(portable, work)
    python_root = portable / "python_embeded"
    site = python_root / "Lib" / "site-packages"
    removed: list[str] = []
    pip_dists = {path.name for path in site.glob("*.dist-info") if path.name.casefold().startswith("pip-")}
    if pip_dists:
        receipt = _remove_paths(portable, _recorded_files(portable, pip_dists, "pip", missing_bytecode_ok=True))
        for name in sorted(pip_dists):
            if (site / name).exists():
                raise ValueError(f"{name} survived removal by its RECORD")
            removed.append(f"{name} ({receipt['files']} recorded files)")
    ensurepip = python_root / "Lib" / "ensurepip"
    if ensurepip.exists():
        if not _extended(ensurepip).resolve().is_relative_to(_extended(work / "extract").resolve()):
            raise ValueError("ensurepip escapes owned extraction stage")
        shutil.rmtree(ensurepip)
        removed.append("Lib/ensurepip")
    for archive in python_root.glob("python3*.zip"):
        with zipfile.ZipFile(archive) as stdlib:
            if any(name.split("/", 1)[0] in ("pip", "ensurepip") for name in stdlib.namelist()):
                raise ValueError(f"{archive.name} carries an installer; the build does not rewrite Python's own archive")
    left = _launcher().installer_presence(str(site), str(python_root))
    if left:
        raise ValueError("private Python keeps an installer: " + ", ".join(left))
    return removed


def _fill_wheel_records(site: Path, wheels: dict[Path, str]) -> list[str]:
    """Hash the code rows pip left without one, from the verified wheels that installed them.

    wheels maps each installed wheel to its pinned SHA-256. torchvision
    0.24.1+cpu's RECORD names its files with backslashes; pip lists each
    installed file again without a hash and the launch check refuses it.
    scripts/wheel_records.py owns the fill; the research venv uses it too.
    """
    return wheel_records.fill(site, _launcher()._hashless_allowed, wheels=wheels)


def _seal_bytecode_records(site: Path) -> int:
    """Write the hash of each installed bytecode file into its RECORD row.

    pip records .pyc files without a hash. The bytes come from the pinned
    archive, so the build records them and the launch check verifies them
    instead of compiling every module at each start.
    """
    sealed = 0
    for record in sorted(site.glob("*.dist-info/RECORD")):
        with record.open(encoding="utf-8", newline="") as stream:
            rows = [row for row in csv.reader(stream) if row]
        changed = False
        for row in rows:
            if len(row) != 3:
                raise ValueError("invalid installed wheel RECORD")
            if row[1] or not row[0].endswith(".pyc"):
                continue
            target = site / Path(*row[0].split("/"))
            if not target.is_file():
                continue
            data = target.read_bytes()
            row[1] = "sha256=" + base64.urlsafe_b64encode(hashlib.sha256(data).digest()).decode().rstrip("=")
            row[2] = str(len(data))
            changed = True
            sealed += 1
        if changed:
            with record.open("w", encoding="utf-8", newline="") as stream:
                csv.writer(stream, lineterminator="\n").writerows(rows)
    return sealed


def _start_without_site(portable: Path, work: Path) -> str:
    """Drop "import site" from the embedded Python's ._pth.

    With that line the interpreter runs every site-packages .pth file before
    the launcher's check; without it the launcher adds site-packages after the
    check passes. Runs after the build's last use of the private Python.
    """
    _require_owned_stage(portable, work)
    files = sorted((portable / "python_embeded").glob("python3*._pth"))
    if len(files) != 1:
        raise ValueError("official portable ._pth layout differs from pin")
    raw = files[0].read_bytes()
    newline = b"\r\n" if b"\r\n" in raw else b"\n"
    lines = raw.decode("utf-8").splitlines()
    kept = [line for line in lines if line.strip() != "import site"]
    if len(lines) - len(kept) != 1:
        raise ValueError(f"{files[0].name} does not carry exactly one 'import site' line")
    files[0].write_bytes(newline.join(line.encode("utf-8") for line in kept) + newline)
    return f"python_embeded/{files[0].name}: removed 'import site' (site-packages joins after the launch check)"


def _seal_private_python(portable: Path, work: Path) -> dict[str, object]:
    """Leave a private Python the launch check accepts, prove it here and pin its RECORD set."""
    removed = _remove_installers(portable, work)
    root = portable / "python_embeded"
    site = root / "Lib" / "site-packages"
    sealed = _seal_bytecode_records(site)
    started = _start_without_site(portable, work)
    launcher = _launcher()
    records = launcher.records_digest(str(site))
    try:
        report = launcher.check_environment(str(site), str(root), records=records)
    except launcher.Refusal as refusal:
        raise ValueError(f"private Python fails the OA launch check: {refusal}") from None
    if report["unchecked_bytecode"]:
        raise ValueError("private Python keeps bytecode without a RECORD hash")
    return {"removed_installers": removed, "sealed_bytecode_rows": sealed, "entrypoint_change": started,
            "records_sha256": records,
            "launch_check": {"files": report["files"], "hashed": report["hashed"]}}


def _pip_check(python: Path, env: dict[str, str]) -> set[str]:
    check = subprocess.run([str(python), "-B", "-s", "-m", "pip", "check"],
                           capture_output=True, text=True, env=env, timeout=120)
    if check.returncode not in (0, 1):
        raise ValueError("private Python dependency check failed to run")
    if check.returncode == 1 and not check.stdout.strip():
        raise ValueError("private Python dependency check gave no diagnostic")
    return {line.strip() for line in check.stdout.splitlines() if line.strip() and
            line.strip() != "No broken requirements found."}


def _require_owned_stage(portable: Path, work: Path) -> None:
    if not _extended(portable).resolve().is_relative_to(_extended(work / "extract").resolve()):
        raise ValueError("CPU transformation requires the owned extraction stage")


def _cpu_wheel_receipts(portable: Path, wheel_specs: dict[str, tuple[int, str, str]]) -> list[dict[str, object]]:
    site = portable / "python_embeded" / "Lib" / "site-packages"
    receipts = []
    for name, (size, digest, requirement) in sorted(wheel_specs.items()):
        distribution, version = requirement.split("==", 1)
        wheel_url, project_url, project_commit, license_name, license_files = CPU_WHEEL_PROVENANCE[distribution]
        dist_info = site / f"{distribution}-{version}.dist-info"
        for relative in license_files:
            if not (dist_info / relative).is_file():
                raise ValueError(f"CPU wheel lacks installed license/notice: {distribution}/{relative}")
        receipts.append({"filename": name, "bytes": size, "sha256": digest,
                         "requirement": requirement, "wheel_url": wheel_url,
                         "upstream_release_source_url": project_url,
                         "upstream_release_commit": project_commit,
                         "build_commit_verified": False,
                         "license": license_name,
                         "installed_license_paths": [str((dist_info / relative).relative_to(portable)).replace('\\', '/')
                                                     for relative in license_files]})
    return receipts


def _discard_relocated_torch_tools(portable: Path, work: Path) -> list[str]:
    """Drop pip launchers bound to this temporary Python path and their RECORD rows."""
    _require_owned_stage(portable, work)
    site = portable / "python_embeded" / "Lib" / "site-packages"
    expected = {"torchfrtrace.exe", "torchrun.exe"}
    found: set[str] = set()
    for distribution, (_, _, _, _, _) in CPU_WHEEL_PROVENANCE.items():
        version = next(spec[2].split("==", 1)[1] for spec in CPU_WHEELS.values()
                       if spec[2].startswith(distribution + "=="))
        record = site / f"{distribution}-{version}.dist-info" / "RECORD"
        with record.open(encoding="utf-8", newline="") as stream:
            rows = list(csv.reader(stream))
        kept = []
        for row in rows:
            if len(row) != 3:
                raise ValueError("invalid CPU wheel RECORD")
            if not row[0].startswith("../../Scripts/"):
                kept.append(row)
                continue
            name = row[0].removeprefix("../../Scripts/")
            if distribution != "torch" or name not in expected or name in found:
                raise ValueError("unexpected CPU wheel-generated script")
            tool = portable / "python_embeded" / "Scripts" / name
            data = tool.read_bytes()
            if len(data) != int(row[2]) or ("sha256=" + base64.urlsafe_b64encode(hashlib.sha256(data).digest()).decode().rstrip("=")) != row[1]:
                raise ValueError("CPU wheel-generated script differs from RECORD")
            if work.name.encode() not in data:
                raise ValueError("CPU wheel-generated script lacks expected staging path")
            tool.unlink()
            found.add(name)
        if len(kept) != len(rows):
            with record.open("w", encoding="utf-8", newline="") as stream:
                writer = csv.writer(stream, lineterminator="\n")
                writer.writerows(kept)
    if found != expected:
        raise ValueError("CPU wheel-generated script set differs from pin")
    return sorted(found)


def _convert_cpu(portable: Path, wheelhouse: Path, work: Path,
                 wheel_specs: dict[str, tuple[int, str, str]] = CPU_WHEELS) -> dict[str, object]:
    """Turn the pinned AMD portable into the labelled OA CPU frontend."""
    _require_owned_stage(portable, work)
    if {path.name for path in wheelhouse.glob("*.whl")} != set(wheel_specs):
        raise ValueError("CPU wheelhouse differs from three pinned wheels")
    private_wheels = work / "cpu-wheels"
    private_wheels.mkdir()
    for filename, (size, digest, _) in sorted(wheel_specs.items()):
        _snapshot_verified_source(wheelhouse / filename, private_wheels / filename, size, digest)
    python = portable / "python_embeded" / "python.exe"
    if not python.is_file():
        raise ValueError("official portable lacks its private Python")
    env = _private_python_env()
    baseline_issues = _pip_check(python, env)
    removed = _remove_gpu_files(portable)
    lock = work / "cpu-requirements.lock"
    lock.write_text("".join(f"{requirement} --hash=sha256:{digest}\n"
                            for _, (_, digest, requirement) in sorted(wheel_specs.items())), encoding="ascii")
    subprocess.run([str(python), "-B", "-s", "-m", "pip", "install", "--no-index", "--no-deps",
                    "--no-compile", "--find-links", str(private_wheels), "--require-hashes", "-r", str(lock)],
                   check=True, env=env, timeout=900)
    filled = _fill_wheel_records(portable / "python_embeded" / "Lib" / "site-packages",
                                 {private_wheels / filename: digest
                                  for filename, (_, digest, _) in sorted(wheel_specs.items())})
    new_issues = _pip_check(python, env) - baseline_issues
    if new_issues:
        raise ValueError("CPU conversion introduced dependency conflicts: " + "; ".join(sorted(new_issues)))
    subprocess.run([str(python), "-B", "-s", "-c", "import torch; assert torch.__version__ == '2.9.1+cpu'; "
                    "assert torch.version.hip is None and torch.version.cuda is None"],
                   check=True, env=env, timeout=120)
    for name in GPU_DISTS:
        if (portable / "python_embeded" / "Lib" / "site-packages" / name).exists():
            raise ValueError("GPU distribution metadata survived CPU conversion")
    return {"removed_gpu_recorded": removed,
            "record_hashes_filled_from_verified_wheels": filled,
            "removed_stage_bound_cli": _discard_relocated_torch_tools(portable, work),
            "cpu_wheels": _cpu_wheel_receipts(portable, wheel_specs)}


def _rewrite_cpu_entrypoints(portable: Path, work: Path) -> list[str]:
    """Replace mutable AMD launch/update entrypoints in the owned stage."""
    _require_owned_stage(portable, work)
    original_readme = portable / "README_VERY_IMPORTANT.txt"
    amd_launchers = [portable / "run_amd_gpu.bat", portable / "run_amd_gpu_enable_dynamic_vram.bat"]
    update = portable / "update"
    if not original_readme.is_file() or not all(path.is_file() for path in amd_launchers) or not update.is_dir():
        raise ValueError("official AMD launch/update layout differs from pin")
    docs = portable / "OA-DOCS"
    docs.mkdir()
    (docs / "UPSTREAM_AMD_README.txt").write_bytes(original_readme.read_bytes())
    original_readme.unlink()
    for path in amd_launchers:
        path.unlink()
    if not _extended(update).resolve().is_relative_to(_extended(work / "extract").resolve()):
        raise ValueError("updater escapes owned extraction stage")
    shutil.rmtree(update)  # exact top-level updater in the freshly owned extraction stage
    (portable / "README_OPENABSTRACTIONS_CPU.txt").write_text(
        "OpenAbstractions ComfyUI CPU frontend\n"
        "This labelled build starts ComfyUI in CPU mode. Local model execution can be slow.\n"
        "An OpenAbstractions image provider such as Lemonade may use a separate GPU.\n"
        "Use run_openabstractions_cpu.bat for manual launch or the reviewed OA app setup.\n"
        "Both start ComfyUI through oa_comfyui_launch.py, which checks the private Python\n"
        "against its wheels' RECORD hashes and loads safetensors weights only.\n"
        "This immutable build is updated by installing a newly reviewed OA package.\n"
        "The original AMD portable instructions are retained in OA-DOCS.\n",
        encoding="utf-8", newline="\r\n")
    if (portable / LAUNCHER_NAME).exists():
        raise ValueError("official portable already contains the OA launcher")
    shutil.copyfile(LAUNCHER_SOURCE, portable / LAUNCHER_NAME)
    return ["README_VERY_IMPORTANT.txt -> OA-DOCS/UPSTREAM_AMD_README.txt",
            "removed run_amd_gpu*.bat", "removed top-level update/", "added OA CPU README",
            f"added {LAUNCHER_NAME} (RECORD check, no installer, safetensors only)"]


def _write_cpu_launch(portable: Path, records: str) -> str:
    """Write the manual launcher once the RECORD set is final; it pins that set."""
    if len(records) != 64 or any(char not in "0123456789abcdef" for char in records):
        raise ValueError("RECORD-set digest must be 64 lowercase hex characters")
    (portable / "run_openabstractions_cpu.bat").write_text(
        '@echo off\r\nsetlocal\r\nset "OA_DATA=%LOCALAPPDATA%\\OpenAbstractions\\ComfyUI"\r\n'
        'for %%D in (user input output temp launch) do if not exist "%OA_DATA%\\%%D" mkdir "%OA_DATA%\\%%D"\r\n'
        f'"%~dp0python_embeded\\python.exe" -B -s "%~dp0oa_comfyui_launch.py" --records {records} '
        '--state "%OA_DATA%\\launch" -- "%~dp0ComfyUI\\main.py" --cpu --listen 127.0.0.1 '
        '--disable-auto-launch --disable-api-nodes --user-directory "%OA_DATA%\\user" --input-directory "%OA_DATA%\\input" '
        '--output-directory "%OA_DATA%\\output" --temp-directory "%OA_DATA%\\temp"\r\n',
        encoding="utf-8", newline="")
    return f"added run_openabstractions_cpu.bat (launch check pinned to RECORD set {records})"


def _append_git_source(combined: zipfile.ZipFile, checkout: Path, project: str, commit: str, work: Path) -> None:
    staged = work / (project + ".zip")
    subprocess.run(["git", "-C", str(checkout), "archive", "--format=zip",
                    f"--prefix={project}-{commit}/", "-o", str(staged), "HEAD"], check=True, timeout=600)
    with zipfile.ZipFile(staged) as component:
        for entry in component.infolist():
            with component.open(entry) as reader, combined.open(entry, "w", force_zip64=True) as writer:
                shutil.copyfileobj(reader, writer, 1 << 20)


def _append_integration_source(combined: zipfile.ZipFile, integration: Path) -> list[str]:
    """Put the exact installed OA node source and its wheel source ledger beside ComfyUI source."""
    copied = []
    with zipfile.ZipFile(integration) as source:
        for name in sorted(source.namelist()):
            if not (name.startswith("node/abstraction_downloads/") or name in
                    ("manifest.json", "requirements.lock", "LICENSE", "NOTICE")):
                continue
            if name.endswith("/"):
                continue
            destination = "OA-Integration-Source/" + name
            info = zipfile.ZipInfo(destination, _ZIP_TIME)
            info.compress_type = zipfile.ZIP_DEFLATED
            info.create_system = 3
            info.external_attr = 0o100644 << 16
            combined.writestr(info, source.read(name))
            copied.append(destination)
    if "OA-Integration-Source/node/abstraction_downloads/__init__.py" not in copied or \
            "OA-Integration-Source/manifest.json" not in copied:
        raise ValueError("OA integration corresponding node source is missing")
    return copied


def _append_public_integration_sources(combined: zipfile.ZipFile, manifest: dict,
                                       checkouts: dict[str, Path] | None, work: Path) -> list[str]:
    """Carry corresponding public source for every installed OA wheel and node."""
    if manifest.get("source_mode") != "public-split":
        return []
    adopter = manifest.get("adopter_source")
    packager = manifest.get("packager_source")
    wheels = manifest.get("wheels")
    if not isinstance(adopter, dict) or not isinstance(packager, dict) or not isinstance(wheels, list) or not checkouts:
        raise ValueError("public OA integration needs exact public source checkouts")
    pins = [(adopter.get("repository"), adopter.get("commit")),
            (packager.get("repository"), packager.get("commit"))]
    pins.extend((wheel.get("repository"), wheel.get("commit")) for wheel in wheels)
    unique = {}
    for repository, commit in pins:
        if (not isinstance(repository, str) or not repository.startswith("openabstractions/")
                or not isinstance(commit, str) or not re.fullmatch(r"[0-9a-f]{40}", commit)
                or repository in unique and unique[repository] != commit):
            raise ValueError("public OA integration has inconsistent source pins")
        unique[repository] = commit
    if set(checkouts) != set(unique):
        raise ValueError("public OA integration source checkout set differs from manifest")
    result = []
    for repository, commit in sorted(unique.items()):
        checkout = checkouts[repository].resolve(strict=True)
        if _git_head(checkout) != commit or subprocess.check_output(
                ["git", "-C", str(checkout), "status", "--porcelain=v1", "--untracked-files=all"],
                text=True, timeout=30).strip():
            raise ValueError("public OA integration source checkout differs: " + repository)
        project = "OA-Integration-" + repository.split("/", 1)[1]
        _append_git_source(combined, checkout, project, commit, work)
        result.append(f"{project}-{commit}/")
    return result


def _bundled_python_inventory(portable: Path) -> dict[str, object]:
    """Record notice evidence for the Python distributions in this fixed asset."""
    site_packages = portable / "python_embeded" / "Lib" / "site-packages"
    distributions = sorted(site_packages.glob("*.dist-info"))
    copyleft: list[dict[str, object]] = []
    without_colocated_notice: list[str] = []
    for dist in distributions:
        metadata = dist / "METADATA"
        if not metadata.is_file():
            continue
        fields = Parser().parsestr(metadata.read_text(encoding="utf-8", errors="replace"), headersonly=True)
        name = fields.get("Name", dist.name)
        evidence = " ".join([fields.get("License", ""), fields.get("License-Expression", ""),
                             *fields.get_all("Classifier", [])])
        notices = [path for path in dist.rglob("*") if path.is_file() and
                   any(word in path.name.casefold() for word in _NOTICE_NAMES)]
        if not notices:
            without_colocated_notice.append(name)
        if any(word in evidence.upper() for word in ("GPL", "AGPL", "LGPL")):
            copyleft.append({"name": name, "version": fields.get("Version"),
                             "notice_files": len(notices), "license_metadata": evidence[:300]})
    return {"distribution_count": len(distributions), "copyleft_metadata": copyleft,
            "without_colocated_notice": sorted(without_colocated_notice)}


def _integration_distributions() -> set[str]:
    """The wheel distributions scripts/package_comfyui_integration.py packs."""
    spec = importlib.util.spec_from_file_location("oa_package_comfyui_integration", INTEGRATION_PACKAGER)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return set(module.PUBLIC_DISTRIBUTIONS.values())


def _check_integration_consistency(archive_path: Path) -> list[dict[str, object]]:
    """The integration ZIP agrees with itself and with its packager, read before extraction.

    Its manifest's wheels, requirements.lock and the wheels/ members name the
    same files, sizes and SHA-256 digests; exactly one wheel is the native
    abstraction_ipc wheel; and the wheels are the distributions the
    integration packager packs, so a ZIP built before a package joined it
    (abstraction-logging, abstraction-router) is refused. Returns the
    manifest's wheel entries.
    """
    def refuse(reason: str) -> ValueError:
        return ValueError("OA integration ZIP is inconsistent (%s); %s" % (reason, INTEGRATION_REMEDY))

    try:
        with zipfile.ZipFile(archive_path) as archive:
            manifest = json.loads(archive.read("manifest.json").decode("utf-8"))
            lock = archive.read("requirements.lock").decode("utf-8").splitlines()
            members = {info.filename[len("wheels/"):]: info for info in archive.infolist()
                       if info.filename.startswith("wheels/") and not info.is_dir()}
            wheels = manifest.get("wheels") if isinstance(manifest, dict) else None
            if not isinstance(wheels, list) or not wheels or not all(isinstance(w, dict) for w in wheels):
                raise refuse("its manifest lists no wheels")
            names = [w.get("filename") for w in wheels]
            if len(set(names)) != len(names) or set(names) != set(members):
                raise refuse("manifest wheels and the wheelhouse differ")
            native = [w for w in wheels if str(w["filename"]).startswith("abstraction_ipc-")]
            if len(native) != 1 or [w for w in wheels if "native_library_sha256" in w] != native:
                raise refuse("it must hold exactly one native abstraction_ipc wheel")
            packed = {str(name).split("-")[0] for name in names}
            expected_distributions = _integration_distributions()
            if packed != expected_distributions:
                raise refuse("it lacks %s and carries %s beyond the integration packager's wheels" % (
                    ", ".join(sorted(expected_distributions - packed)) or "nothing",
                    ", ".join(sorted(packed - expected_distributions)) or "nothing"))
            expected = set()
            for wheel in wheels:
                info = members[wheel["filename"]]
                digest = hashlib.sha256()
                with archive.open(info) as stream:
                    for chunk in iter(lambda: stream.read(1 << 20), b""):
                        digest.update(chunk)
                if info.file_size != wheel.get("size") or digest.hexdigest() != wheel.get("sha256"):
                    raise refuse("wheel %s differs from its manifest entry" % wheel["filename"])
                parts = str(wheel["filename"]).split("-")
                expected.add("%s==%s --hash=sha256:%s" % (parts[0].replace("_", "-"), parts[1], wheel["sha256"]))
            if len(lock) != len(wheels) or set(lock) != expected:
                raise refuse("requirements.lock and the manifest wheels differ")
    except (KeyError, UnicodeDecodeError, json.JSONDecodeError, zipfile.BadZipFile) as error:
        raise refuse("unreadable: %s" % error) from error
    return wheels


def _apply_integration(portable: Path, archive_path: Path, work: Path, *, install_wheels: bool,
                       archive_size: int, archive_sha256: str, archive_name: str | None = None,
                       producer_receipt_path: Path | None = None,
                       wheel_receipt_path: Path | None = None,
                       expected_redist_source_commit: str | None = None,
                       expected_run_id: int | None = None,
                       expected_run_attempt: int | None = None) -> dict[str, object]:
    """Apply the one reviewed OA integration ZIP during the release build."""
    unpacked = work / "integration"
    unpacked.mkdir()
    names: set[str] = set()
    total = 0
    with zipfile.ZipFile(archive_path) as archive:
        for entry in archive.infolist():
            name = entry.filename.rstrip("/")
            parts = name.split("/")
            if not name or any(not part for part in parts):
                raise ValueError("unsafe OA integration member")
            for part in parts:
                _safe_name(part)
            _register_name(name, names)
            mode = entry.external_attr >> 16
            if stat.S_ISLNK(mode):
                raise ValueError("OA integration contains a link")
            if entry.is_dir():
                continue
            total += entry.file_size
            if total > 16 << 20:
                raise ValueError("OA integration exceeds expanded-byte ceiling")
            target = unpacked.joinpath(*parts)
            target.parent.mkdir(parents=True, exist_ok=True)
            with archive.open(entry) as source, target.open("xb") as destination:
                shutil.copyfileobj(source, destination, 1 << 20)
            if target.stat().st_size != entry.file_size:
                raise ValueError("OA integration file size mismatch")
    manifest = json.loads((unpacked / "manifest.json").read_text(encoding="utf-8"))
    public = manifest.get("source_mode") == "public-split"
    if public:
        if producer_receipt_path is None or wheel_receipt_path is None:
            raise ValueError("public OA integration requires external producer and wheel receipts")
        receipt = json.loads(producer_receipt_path.read_text(encoding="utf-8"))
        wheel_receipt_sha256 = _sha256(wheel_receipt_path)[1]
        if (not expected_redist_source_commit or not re.fullmatch(r"[0-9a-f]{40}", expected_redist_source_commit)
                or not expected_run_id or not expected_run_attempt
                or receipt.get("candidate_source") != expected_redist_source_commit
                or receipt.get("run_id") != expected_run_id
                or receipt.get("run_attempt") != expected_run_attempt
                or receipt.get("wheel_receipt_sha256") != wheel_receipt_sha256
                or receipt.get("integration") != {"name": archive_name or archive_path.name, "bytes": archive_size,
                                                   "sha256": archive_sha256}
                or receipt.get("wheels") != manifest.get("wheels")
                or receipt.get("adopter_source") != manifest.get("adopter_source")
                or receipt.get("packager_source") != manifest.get("packager_source")):
            raise ValueError("public OA integration differs from candidate source or Windows wheel job")
    elif (manifest.get("source_mode") not in (None, "private-diagnostic")
          or manifest.get("source_commit") != INTEGRATION_SOURCE_COMMIT):
        raise ValueError("OA integration manifest differs from pinned private source")
    if manifest.get("wheels") is None:
        raise ValueError("OA integration manifest has no wheels")
    native_wheels = [wheel for wheel in manifest["wheels"] if wheel["filename"].startswith("abstraction_ipc-")]
    native_commit = (native_wheels[0].get("commit") if public and len(native_wheels) == 1 else
                     native_wheels[0].get("source_commit") if len(native_wheels) == 1 else None)
    if len(native_wheels) != 1 or (not public and native_commit != INTEGRATION_NATIVE_COMMIT):
        raise ValueError("OA integration native wheel source differs from pin")
    wheel_names: set[str] = set()
    for wheel in manifest["wheels"]:
        filename = wheel["filename"]
        _safe_name(filename)
        wheel_path = unpacked / "wheels" / filename
        size, digest = _sha256(wheel_path)
        if size != wheel["size"] or digest != wheel["sha256"]:
            raise ValueError("OA integration wheel differs from lock")
        if "native_library_sha256" in wheel and not public and wheel.get("source_commit") != INTEGRATION_NATIVE_COMMIT:
            raise ValueError("OA native wheel source differs from pin")
        wheel_names.add(filename)
    if wheel_names != {path.name for path in (unpacked / "wheels").glob("*.whl")}:
        raise ValueError("OA integration wheelhouse differs from manifest")
    required = (unpacked / "requirements.lock").read_text(encoding="utf-8").splitlines()
    if len(required) != len(manifest["wheels"]) or any(" --hash=sha256:" not in line for line in required):
        raise ValueError("OA integration requirements lock is incomplete")
    node = unpacked / "node" / "abstraction_downloads"
    if not (node / "__init__.py").is_file():
        raise ValueError("OA integration lacks its reviewed ComfyUI node")
    destination = portable / "ComfyUI" / "custom_nodes" / "abstraction_downloads"
    if destination.exists():
        raise ValueError("official portable already contains the OA node")
    shutil.copytree(node, destination)
    if install_wheels:
        python = portable / "python_embeded" / "python.exe"
        if not python.is_file():
            raise ValueError("official portable lacks its private Python")
        env = os.environ.copy()
        for inherited in ("PYTHONPATH", "PYTHONHOME", "PIP_TARGET", "PIP_PREFIX", "PIP_USER"):
            env.pop(inherited, None)
        env["PYTHONDONTWRITEBYTECODE"] = "1"
        env["PYTHONNOUSERSITE"] = "1"
        env["PIP_NO_CACHE_DIR"] = "1"
        env["PIP_CONFIG_FILE"] = os.devnull
        env["PIP_DISABLE_PIP_VERSION_CHECK"] = "1"
        subprocess.run([str(python), "-B", "-s", "-m", "pip", "install", "--no-index", "--find-links", str(unpacked / "wheels"),
                        "--require-hashes", "--no-compile", "-r", str(unpacked / "requirements.lock")],
                       check=True, env=env, timeout=900)
        for record in portable.rglob("direct_url.json"):
            if record.parent.name.casefold().startswith("abstraction_"):
                raise ValueError("OA wheel installation recorded a direct source URL")
            if str(work).casefold() in record.read_text(encoding="utf-8", errors="replace").casefold():
                raise ValueError("private Python installation recorded a temporary build path")
        site_packages = portable / "python_embeded" / "Lib" / "site-packages"
        for path_file in site_packages.glob("*.pth"):
            if str(work).casefold() in path_file.read_text(encoding="utf-8", errors="replace").casefold():
                raise ValueError("private Python path file recorded a temporary build path")
        subprocess.run([str(python), "-B", "-s", "-c", "from abstraction.ipc import Cancellation, Library; "
                        "library = Library(); assert library._version() == 1; "
                        "signal = Cancellation(library); signal.signal(); signal.close()"],
                       check=True, env=env, timeout=60)
    return {"sha256": archive_sha256, "bytes": archive_size,
            "source_commit": manifest["adopter_source"]["commit"] if public else INTEGRATION_SOURCE_COMMIT,
            "native_source_commit": native_commit,
            "manifest": manifest, "license": (unpacked / "LICENSE").read_bytes(),
            "notice": (unpacked / "NOTICE").read_bytes()}


def repack(
    source: Path,
    output: Path,
    source_output: Path | None,
    *,
    expected_size: int = SOURCE_BYTES,
    expected_sha256: str = SOURCE_SHA256,
    archive_root: str = ARCHIVE_ROOT,
    expected_commit: str | None = SOURCE_COMMIT,
    frontend_checkout: Path | None = None,
    additional_source_checkouts: dict[str, Path] | None = None,
    additional_source_pins: dict[str, tuple[str, str]] | None = None,
    cpu_wheelhouse: Path | None = None,
    integration_archive: Path | None = None,
    integration_public_source_checkouts: dict[str, Path] | None = None,
    integration_producer_receipt: Path | None = None,
    integration_wheel_receipt: Path | None = None,
    integration_redist_source_commit: str | None = None,
    integration_run_id: int | None = None,
    integration_run_attempt: int | None = None,
    install_wheels: bool = True,
    expected_integration_size: int = INTEGRATION_BYTES,
    expected_integration_sha256: str = INTEGRATION_SHA256,
    expected_frontend_commit: str = FRONTEND_COMMIT,
    expected_frontend_version: str = FRONTEND_VERSION,
    max_release_asset_bytes: int = MAX_RELEASE_ASSET_BYTES,
    max_expanded_bytes: int = MAX_EXPANDED_BYTES,
    minimum_free_after_build_bytes: int = MIN_FREE_AFTER_BUILD_BYTES,
    tar: str = "tar.exe",
) -> dict[str, object]:
    """Build new outputs; private overrides support small fixed-fixture tests."""
    source = source.absolute()
    output = output.absolute()
    if source_output is not None:
        source_output = source_output.absolute()
    if output.exists() or source_output is not None and source_output.exists():
        raise FileExistsError("output already exists")
    if source_output is not None and source_output == output:
        raise ValueError("app ZIP and source ZIP must have different paths")
    if source_output is not None and source_output.parent != output.parent:
        raise ValueError("app and source ZIP outputs must share a directory")
    if expected_commit is not None and integration_archive is None:
        raise ValueError("pinned OA integration ZIP is required")
    source_pins = additional_source_pins if additional_source_pins is not None else ADDITIONAL_SOURCES
    if expected_commit == SOURCE_COMMIT and (source_pins != ADDITIONAL_SOURCES or
                                             set(additional_source_checkouts or {}) != set(ADDITIONAL_SOURCES)):
        raise ValueError("exact embedded-docs, pygit2 and libgit2 source checkouts are required")
    if expected_commit == SOURCE_COMMIT and cpu_wheelhouse is None:
        raise ValueError("pinned CPU PyTorch wheelhouse is required")
    output.parent.mkdir(parents=True, exist_ok=True)
    if source_output is not None:
        source_output.parent.mkdir(parents=True, exist_ok=True)
    # Snapshot + expanded tree + app/source ZIPs + working margin, while keeping
    # the machine's agreed free-space floor throughout the build.
    wheel_bytes = sum(size for size, _, _ in CPU_WHEELS.values()) if cpu_wheelhouse is not None else 0
    working_margin = min(2 << 30, max_expanded_bytes)
    required = required_free_bytes(minimum_free_after_build_bytes=minimum_free_after_build_bytes,
                                   source_bytes=expected_size, max_expanded_bytes=max_expanded_bytes,
                                   max_release_asset_bytes=max_release_asset_bytes,
                                   wheel_bytes=wheel_bytes)
    if shutil.disk_usage(output.parent).free < required:
        raise OSError("insufficient free disk for fresh extraction and ZIP output")
    with owned_temporary_tree(prefix="oa-comfy-repack-", parent=output.parent) as work:
        timings: dict[str, float] = {}
        began = time.monotonic()
        snapshot = work / "verified.7z"
        _snapshot_verified_source(source, snapshot, expected_size, expected_sha256)
        integration_snapshot = None
        if integration_archive is not None:
            integration_snapshot = work / "verified-integration.zip"
            _snapshot_verified_source(integration_archive.absolute(), integration_snapshot,
                                      expected_integration_size, expected_integration_sha256)
            _check_integration_consistency(integration_snapshot)
        timings["verify_snapshot"] = round(time.monotonic() - began, 3)
        listed = _listed_members(snapshot, archive_root, tar)
        extracted = work / "extract"
        extracted.mkdir()
        began = time.monotonic()
        subprocess.run([tar, "-xf", str(snapshot), "-C", str(extracted)], check=True, capture_output=True, timeout=3600)
        timings["extract"] = round(time.monotonic() - began, 3)
        original_files, original_dirs, _, _ = _scan_tree(_extended(extracted), archive_root, max_expanded_bytes)
        if len(original_files) + len(original_dirs) != len(listed):
            raise ValueError("extracted tree member count differs from verified archive listing")
        if shutil.disk_usage(output.parent).free < minimum_free_after_build_bytes + 2 * max_release_asset_bytes + working_margin:
            raise OSError("insufficient free disk after extraction for both ZIP outputs")
        bundled_inventory = _bundled_python_inventory(_extended(extracted / archive_root))
        cpu = None
        cpu_entrypoint_changes: list[str] = []
        if cpu_wheelhouse is not None:
            began = time.monotonic()
            cpu = _convert_cpu(_extended(extracted / archive_root), cpu_wheelhouse.absolute(), work)
            cpu_entrypoint_changes = _rewrite_cpu_entrypoints(_extended(extracted / archive_root), work)
            timings["convert_cpu"] = round(time.monotonic() - began, 3)
        integration = None
        if integration_snapshot is not None:
            began = time.monotonic()
            integration = _apply_integration(_extended(extracted / archive_root), integration_snapshot, work,
                                             install_wheels=install_wheels,
                                             archive_size=expected_integration_size,
                                             archive_sha256=expected_integration_sha256,
                                             archive_name=integration_archive.name,
                                             producer_receipt_path=integration_producer_receipt,
                                             wheel_receipt_path=integration_wheel_receipt,
                                             expected_redist_source_commit=integration_redist_source_commit,
                                             expected_run_id=integration_run_id,
                                             expected_run_attempt=integration_run_attempt)
            timings["apply_integration"] = round(time.monotonic() - began, 3)
        private_python = None
        if cpu is not None:
            # After the build's last pip install: drop the installers, record
            # bytecode hashes and run the launch check on the staged tree.
            began = time.monotonic()
            private_python = _seal_private_python(_extended(extracted / archive_root), work)
            cpu_entrypoint_changes.append(private_python["entrypoint_change"])
            cpu_entrypoint_changes.append(_write_cpu_launch(_extended(extracted / archive_root),
                                                            private_python["records_sha256"]))
            timings["seal_private_python"] = round(time.monotonic() - began, 3)
        files, dirs, expanded, notices = _scan_tree(_extended(extracted), archive_root, max_expanded_bytes)
        source_sha256 = None
        frontend_license = None
        if expected_commit is not None:
            began = time.monotonic()
            checkout = extracted / archive_root / "ComfyUI"
            if _git_head(checkout) != expected_commit:
                raise ValueError("bundled ComfyUI checkout does not match pinned source commit")
            if source_output is None or frontend_checkout is None:
                raise ValueError("core and frontend corresponding source inputs/ZIP are required")
            frontend_checkout = frontend_checkout.resolve(strict=True)
            if _git_head(frontend_checkout) != expected_frontend_commit:
                raise ValueError("frontend source checkout does not match pinned v1.51.10 commit")
            frontend_metadata = json.loads(subprocess.check_output(["git", "-C", str(frontend_checkout), "show", "HEAD:package.json"], timeout=30))
            if frontend_metadata.get("version") != expected_frontend_version:
                raise ValueError("frontend source version differs from bundled package")
            frontend_license = subprocess.check_output(["git", "-C", str(frontend_checkout), "show", "HEAD:LICENSE"], timeout=30)
            source_stage = work / "source.zip"
            subprocess.run(["git", "-C", str(checkout), "archive", "--format=zip", f"--prefix=ComfyUI-{expected_commit}/", "-o", str(source_stage), "HEAD"], check=True, timeout=300)
            with zipfile.ZipFile(source_stage, "a", allowZip64=True) as combined:
                _append_git_source(combined, frontend_checkout, "ComfyUI_frontend", expected_frontend_commit, work)
                for project, supplied in sorted((additional_source_checkouts or {}).items()):
                    commit, _ = source_pins[project]
                    supplied = supplied.resolve(strict=True)
                    if _git_head(supplied) != commit:
                        raise ValueError(f"{project} source checkout differs from pinned commit")
                    _append_git_source(combined, supplied, project, commit, work)
                integration_source_paths = _append_integration_source(combined, integration_snapshot)
                integration_source_paths.extend(_append_public_integration_sources(
                    combined, integration["manifest"], integration_public_source_checkouts, work))
            _, source_sha256 = _sha256(source_stage)
            timings["source_bundle"] = round(time.monotonic() - began, 3)
        manifest = {
            "format": 1,
            "distribution": "OpenAbstractions repack of official ComfyUI Windows portable v0.35.0",
            "upstream_archive_url": SOURCE_URL,
            "upstream_archive_bytes": expected_size,
            "upstream_archive_sha256": expected_sha256,
            "comfyui_source_commit": expected_commit,
            "comfyui_source_url": SOURCE_CODE_URL,
            "comfyui_license": "GPL-3.0; bundled dependencies retain their own notices and terms",
            "frontend_package_version": expected_frontend_version if expected_commit is not None else None,
            "frontend_source_commit": expected_frontend_commit if expected_commit is not None else None,
            "frontend_source_url": FRONTEND_SOURCE_URL if expected_commit is not None else None,
            "frontend_license": "GPL-3.0" if expected_commit is not None else None,
            "additional_corresponding_sources": {
                project: {"commit": commit, "url": url}
                for project, (commit, url) in source_pins.items()
            } if additional_source_checkouts else {},
            "oa_integration_corresponding_source_paths": integration_source_paths if expected_commit is not None else [],
            "corresponding_comfyui_source_zip": source_output.name if source_output is not None else None,
            "corresponding_comfyui_source_zip_sha256": source_sha256,
            "file_count": len(files),
            "directory_count": len(dirs),
            "expanded_file_bytes": expanded,
            "preserved_notice_paths": notices,
            "upstream_python_inventory": bundled_inventory,
            "installed_python_inventory": _bundled_python_inventory(_extended(extracted / archive_root)),
            "variant": "oa-comfyui-cpu-frontend" if cpu is not None else "upstream-amd-repack-fixture",
            "cpu_conversion": cpu,
            "cpu_entrypoint_changes": cpu_entrypoint_changes,
            "private_python_seal": private_python,
            # The launch check's --records value; Setup pins it beside the artifact.
            "private_python_records_sha256": private_python["records_sha256"] if private_python else None,
            "oa_launcher": {"path": f"{archive_root}/{LAUNCHER_NAME}",
                            "sha256": _sha256(_extended(extracted / archive_root / LAUNCHER_NAME))[1]}
                           if cpu is not None else None,
            "added_notice_paths": (["OA-LICENSES/ComfyUI_frontend-LICENSE"] if frontend_license is not None else []) +
                                  (["OA-LICENSES/OpenAbstractions-LICENSE", "OA-LICENSES/OpenAbstractions-NOTICE"]
                                   if integration is not None else []),
            "oa_integration": {key: value for key, value in integration.items() if key not in ("manifest", "license", "notice")} if integration is not None else None,
            "oa_integration_manifest": integration["manifest"] if integration is not None else None,
            "installed_paths": {
                "private_python": f"{archive_root}/python_embeded/python.exe",
                "oa_node": f"{archive_root}/ComfyUI/custom_nodes/abstraction_downloads",
            } if integration is not None else None,
            "mutable_data": "launch with input, output, temp and user directories outside this immutable app tree",
        }
        app_stage = work / "app.zip"
        began = time.monotonic()
        if shutil.disk_usage(output.parent).free < minimum_free_after_build_bytes + max_release_asset_bytes + working_margin:
            raise OSError("insufficient free disk before app ZIP output")
        with zipfile.ZipFile(app_stage, "w", allowZip64=True) as archive:
            for name in dirs:
                _write_zip_dir(archive, name)
            for name, path in files:
                _write_zip_file(archive, name, path)
            if frontend_license is not None or integration is not None:
                _write_zip_dir(archive, "OA-LICENSES")
            if frontend_license is not None:
                _write_zip_file(archive, "OA-LICENSES/ComfyUI_frontend-LICENSE", frontend_license)
            if integration is not None:
                _write_zip_file(archive, "OA-LICENSES/OpenAbstractions-LICENSE", integration["license"])
                _write_zip_file(archive, "OA-LICENSES/OpenAbstractions-NOTICE", integration["notice"])
            _write_zip_file(archive, PROVENANCE, (json.dumps(manifest, sort_keys=True, separators=(",", ":")) + "\n").encode())
        timings["app_zip"] = round(time.monotonic() - began, 3)
        app_bytes = app_stage.stat().st_size
        if app_bytes >= max_release_asset_bytes:
            raise ValueError("OA app ZIP exceeds the single release asset size limit")
        _, app_sha256 = _sha256(app_stage)
        source_bytes = None
        if source_output is not None:
            source_bytes = source_stage.stat().st_size
            if source_bytes >= max_release_asset_bytes:
                raise ValueError("corresponding source ZIP exceeds the single release asset size limit")
        if shutil.disk_usage(output.parent).free < minimum_free_after_build_bytes:
            raise OSError("free-space floor reached before publication")
        if source_output is not None:
            os.replace(work / "source.zip", source_output)
        try:
            os.replace(app_stage, output)
        except Exception:
            if source_output is not None:
                source_output.unlink(missing_ok=True)
            raise
        manifest["build_timings_seconds"] = timings  # Printed only; not embedded in deterministic ZIP.
        manifest["build_outputs"] = {"app_zip_bytes": app_bytes, "app_zip_sha256": app_sha256,
                                     "source_zip_bytes": source_bytes, "source_zip_sha256": source_sha256}
        return manifest


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", type=Path, required=True, help="local pinned official .7z asset")
    parser.add_argument("--output", type=Path, required=True, help="new OA-labelled portable ZIP")
    parser.add_argument("--source-output", type=Path, required=True, help="new corresponding exact-commit source ZIP")
    parser.add_argument("--frontend-checkout", type=Path, required=True, help="local ComfyUI_frontend v1.51.10 checkout at the pinned commit")
    parser.add_argument("--embedded-docs-checkout", type=Path, required=True, help="local embedded-docs v0.5.11 checkout")
    parser.add_argument("--pygit2-checkout", type=Path, required=True, help="local pygit2 v1.20.0 checkout")
    parser.add_argument("--libgit2-checkout", type=Path, required=True, help="local libgit2 v1.9.6 checkout")
    parser.add_argument("--integration-archive", type=Path, required=True, help="local exact-hash OA integration ZIP")
    parser.add_argument("--integration-public-source-root", type=Path,
                        help="exact public OA source checkouts, one directory per repository basename")
    parser.add_argument("--integration-producer-receipt", type=Path,
                        help="external same-run producer receipt; excluded from app/source ZIP bytes")
    parser.add_argument("--integration-wheel-receipt", type=Path,
                        help="selected successful Windows wheel job receipt")
    parser.add_argument("--integration-redist-source-commit", help="public integration candidate source commit")
    parser.add_argument("--integration-run-id", type=int, help="public integration Windows wheel job run ID")
    parser.add_argument("--integration-run-attempt", type=int, help="public integration Windows wheel job attempt")
    parser.add_argument("--integration-bytes", type=int, default=INTEGRATION_BYTES, help="reviewed OA integration ZIP byte count")
    parser.add_argument("--integration-sha256", default=INTEGRATION_SHA256, help="reviewed OA integration ZIP SHA-256")
    parser.add_argument("--cpu-wheelhouse", type=Path, required=True, help="three exact-hash Windows CPython 3.12 CPU PyTorch wheels")
    parser.add_argument("--tar", default="tar.exe", help="release host's bsdtar executable")
    parser.add_argument("--minimum-free-gib", type=int, default=20,
                        help="retained free-space floor: workstation default 20 GiB; disposable runner minimum 1 GiB")
    args = parser.parse_args()
    public_sources = None
    if args.integration_public_source_root:
        with zipfile.ZipFile(args.integration_archive) as archive:
            integration_manifest = json.loads(archive.read("manifest.json"))
        repos = {integration_manifest["adopter_source"]["repository"],
                 integration_manifest["packager_source"]["repository"],
                 *(wheel["repository"] for wheel in integration_manifest["wheels"])}
        public_sources = {repo: args.integration_public_source_root / repo.split("/", 1)[1]
                          for repo in repos}
    manifest = repack(args.archive, args.output, args.source_output, frontend_checkout=args.frontend_checkout,
                      additional_source_checkouts={"embedded-docs": args.embedded_docs_checkout,
                                                   "pygit2": args.pygit2_checkout,
                                                   "libgit2": args.libgit2_checkout},
                      integration_archive=args.integration_archive,
                      integration_public_source_checkouts=public_sources,
                      integration_producer_receipt=args.integration_producer_receipt,
                      integration_wheel_receipt=args.integration_wheel_receipt,
                      integration_redist_source_commit=args.integration_redist_source_commit,
                      integration_run_id=args.integration_run_id,
                      integration_run_attempt=args.integration_run_attempt,
                      expected_integration_size=args.integration_bytes,
                      expected_integration_sha256=args.integration_sha256,
                      minimum_free_after_build_bytes=args.minimum_free_gib << 30,
                      cpu_wheelhouse=args.cpu_wheelhouse,
                      tar=args.tar)
    print(json.dumps({"app_zip": str(args.output), "source_zip": str(args.source_output),
                      "file_count": manifest["file_count"], "build_timings_seconds": manifest["build_timings_seconds"],
                      "build_outputs": manifest["build_outputs"]},
                     sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
