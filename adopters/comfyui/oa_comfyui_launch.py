"""Start the OA-labelled ComfyUI after proving its private Python and guarding weights.

usage: python.exe -B -s oa_comfyui_launch.py [--records SHA256] [--state DIR] [--allow-installer]
                  -- MAIN.py [ComfyUI arguments...]

Both OA launch paths run ComfyUI through this file: the application activation
recipe (monitor/internal/setup/comfy_activation.go) and the package's
run_openabstractions_cpu.bat (scripts/package_comfyui_app.py). It carries the
conditions of research/security-reviews/rocm-7.2.1-pytorch-2.9.1-python-3.12.10.md
that apply to every ComfyUI environment on PyTorch 2.9.1.

1. Site-packages matches its RECORD files. With --records, the SHA-256 of the
   set of *.dist-info/RECORD files (name and bytes of each, in name order) must
   equal the value the package build recorded; a mismatch refuses the launch
   before anything else runs. Site-packages then holds exactly the reviewed
   distributions with the reviewed bytes. Without --records the check is tamper
   evidence only: a distribution planted together with its own RECORD and
   hashes passes. Every RECORD row with a hash is checked by SHA-256 and size.
   A file with a code suffix in site-packages that no RECORD lists refuses the
   launch, and so does a link or junction there. Code suffixes include the
   GPU kernel files ROCm loads by path (.aks2, .co, .hsaco, .dat, .bc). A refusal names the file and
   ComfyUI never starts. A RECORD row may omit the hash of a data file (RECORD
   itself, a licence) and of bytecode under __pycache__; any other code file
   without a hash refuses the launch. When unhashed bytecode exists, this
   launcher points sys.pycache_prefix at a new empty private folder, and Python
   compiles from the checked sources and never reads that bytecode. The OA
   package fills in the bytecode hashes at build time.
2. The VISION 2026-10-01 engine-files pattern bounds the cost. The full hash
   runs when --state holds no record from an earlier full hash of the same
   environment, or when any entry differs from it: an extra, missing or changed
   file, by file ID, size, last-write time or change time (Windows ChangeTime,
   which SetFileTime cannot set back). Otherwise the launch compares that
   metadata only. Structural checks (unlisted code files, links, unhashed rows,
   paths outside the environment, installers) run on every launch.
3. No installer. pip or ensurepip in the environment refuses the launch, since
   ComfyUI and ComfyUI-Manager would run pip install from it.
   --allow-installer admits an environment whose review allows pip present but
   unused (the ROCm qualification venv).
4. No pickle weights. PyTorch 2.9.1 has CVE-2026-24747: a crafted .pt/.pth can
   corrupt memory and run code even through torch.load(weights_only=True).
   torch.load, torch.serialization.load, torch.jit.load and torch.export.load
   refuse every call. comfy.utils.load_torch_file, the loader every ComfyUI
   model node reads through, refuses any file that is not .safetensors or .sft
   before opening it. comfy.sd1_clip.safe_load_embed_zip refuses .pt
   embeddings. folder_paths.supported_pt_extensions drops the pickle suffixes,
   so ComfyUI lists no pickle file in its model choosers. ComfyUI core at
   v0.35.0 loads no GGUF; a GGUF file reaching load_torch_file is refused too.
   The guard covers ComfyUI core's loaders and torch's public load functions.
   A custom node's own pickle.load, numpy.load(allow_pickle=True) and
   torch.package are outside it.
5. Site-packages joins sys.path after the check. The OA package's
   python312._pth has no "import site" line, so the embedded Python starts
   without site-packages and runs no .pth file; this launcher calls
   site.addsitedir on site-packages once the check has passed. Without
   --allow-installer, an interpreter that imported site at start-up is
   refused. With --allow-installer (a venv such as the ROCm qualification
   venv) the interpreter processes pyvenv.cfg and site-packages .pth files at
   start-up, before this file, as it always has; the check then refuses an
   unlisted .pth only after it ran.
6. The checked files stay in place while ComfyUI runs (VISION 2026-10-01
   engine-files item 4). On Windows, before reading any metadata, the launch
   opens every entry the RECORD files bind (each RECORD itself, code, headers,
   GPU code objects, data) for reading with FILE_SHARE_READ only and keeps the
   handles until the process exits. Windows then refuses a write, truncation,
   rename or delete of a held file, and a rename of any folder above one;
   Python's imports and native loads open for reading and still succeed. A
   file another process holds open for writing refuses the launch. Bytecode
   RECORD lists without a hash is not held, since the launch never reads it.
   A new file can still be created beside the held ones; the next launch
   refuses it when it has a code suffix. Other systems have no share modes and
   hold nothing.
7. Every guarantee here lasts for this process. ComfyUI-Manager's restart
   calls os.execv(sys.executable, ...) with sys.argv, which this launcher set
   to main.py and its arguments, so a restarted ComfyUI runs without the
   check, the hold and the weight guard. Both OA launch paths omit
   --enable-manager, which adopters/comfyui/test_launch_pins.py enforces, so
   this affects research runs that enable Manager only; run_limits names it
   and those runs record it as a limit.

This file uses the standard library only, so the packager can run the same
check against the staged tree with another Python.
"""

from __future__ import annotations

import base64
import csv
import functools
import hashlib
import importlib.util
import io
import json
import os
import stat
import sys
import sysconfig
import tempfile
import time
import zipfile

EXIT_REFUSED = 3
_HELD: list = []  # handles of the checked files, open for the life of the process
RECORD_NAME = "comfyui-environment-record.json"
RECORD_FORMAT = 1
# GPU code the ROCm runtime loads by path at the first kernel launch, measured in
# the ROCm 7.2.1 / PyTorch 2.9.1 venv: AOTriton kernel images (.aks2), rocBLAS
# and hipBLASLt code objects (.co, .hsaco) and their kernel databases (.dat),
# and the device-library LLVM bitcode the runtime compiler links (.bc).
GPU_CODE_SUFFIXES = frozenset({".aks2", ".co", ".hsaco", ".dat", ".bc"})
CODE_SUFFIXES = frozenset({".py", ".pyw", ".pyc", ".pyo", ".pyd", ".dll", ".so", ".pth",
                           ".exe", ".bat", ".cmd", ".ps1"}) | GPU_CODE_SUFFIXES
SAFE_WEIGHT_SUFFIXES = (".safetensors", ".sft")
PICKLE_SUFFIXES = (".ckpt", ".pt", ".pt2", ".pth", ".bin", ".pkl")
INSTALLERS = ("pip", "ensurepip")


class Refusal(Exception):
    """The environment differs from its wheels; the message names the file."""


class WeightRefused(RuntimeError):
    """A pickle-based weights file was refused before it was read."""


# ---------------------------------------------------------------- environment


def _prefix(root: str) -> str:
    return os.path.normcase(os.path.normpath(root)).rstrip(os.sep) + os.sep


def _display(root: str, path: str) -> str:
    """Path relative to root with forward slashes; both are normalized absolute paths."""
    if os.path.normcase(path).startswith(_prefix(root)):
        return path[len(_prefix(root)):].replace(os.sep, "/")
    return path


def _within(root: str, path: str) -> bool:
    return os.path.normcase(os.path.normpath(path)).startswith(_prefix(root))


def _is_link(info: os.stat_result) -> bool:
    return stat.S_ISLNK(info.st_mode) or bool(
        getattr(info, "st_file_attributes", 0) & getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0))


def _read_records(site: str) -> tuple[list[tuple[str, str, str, str]], str]:
    """Return (distribution, path, hash, size) rows and a digest of every RECORD."""
    digest = hashlib.sha256()
    rows: list[tuple[str, str, str, str]] = []
    for entry in sorted(os.scandir(site), key=lambda item: item.name):
        if not entry.name.endswith(".dist-info"):
            continue
        if not entry.is_dir(follow_symlinks=False):
            raise Refusal("%s is not a directory" % entry.name)
        record = os.path.join(entry.path, "RECORD")
        try:
            info = os.lstat(record)
        except FileNotFoundError:
            raise Refusal("%s has no RECORD" % entry.name) from None
        if not stat.S_ISREG(info.st_mode) or _is_link(info):
            raise Refusal("%s/RECORD is not a regular file" % entry.name)
        with open(record, "rb") as stream:
            data = stream.read()
        digest.update(entry.name.encode("utf-8") + b"\0" + data + b"\0")
        try:
            text = data.decode("utf-8")
        except UnicodeDecodeError:
            raise Refusal("%s/RECORD is not UTF-8" % entry.name) from None
        # RECORD binds itself, whether or not it lists itself.
        rows.append((entry.name, entry.name + "/RECORD", "", ""))
        for row in csv.reader(io.StringIO(text, newline="")):
            if not row:
                continue
            if len(row) != 3 or not row[0]:
                raise Refusal("%s/RECORD has a malformed row: %r" % (entry.name, row))
            rows.append((entry.name, row[0], row[1], row[2]))
    return rows, digest.hexdigest()


def _hashless_allowed(relative: str) -> bool:
    """RECORD may omit the hash of a data file, or of bytecode the launch never reads."""
    parts = relative.replace("\\", "/").split("/")
    if os.path.splitext(parts[-1])[1].lower() not in CODE_SUFFIXES:
        return True
    return len(parts) >= 2 and parts[-2] == "__pycache__" and parts[-1].endswith(".pyc")


def _walk(site: str, root: str) -> list[str]:
    """Return every file under site-packages with a code suffix; refuse links."""
    found: list[str] = []
    pending = [site]
    while pending:
        current = pending.pop()
        with os.scandir(current) as entries:
            for entry in entries:
                info = entry.stat(follow_symlinks=False)
                if _is_link(info):
                    raise Refusal("%s is a link or junction" % _display(root, entry.path))
                if stat.S_ISDIR(info.st_mode):
                    pending.append(entry.path)
                elif os.path.splitext(entry.name)[1].lower() in CODE_SUFFIXES:
                    found.append(entry.path)
    return found


def installer_presence(site: str, root: str) -> list[str]:
    """Name every pip or ensurepip in this environment's own files."""
    present = []
    for entry in os.scandir(site):
        lowered = entry.name.lower()
        if lowered in INSTALLERS or (lowered.startswith("pip-") and lowered.endswith(".dist-info")):
            present.append(_display(root, entry.path))
    for lib in ("Lib", "lib"):
        candidate = os.path.join(root, lib, "ensurepip")
        if os.path.isdir(candidate):
            present.append(_display(root, candidate))
            break
    for entry in os.scandir(root):
        lowered = entry.name.lower()
        if lowered.startswith("python3") and lowered.endswith(".zip") and entry.is_file():
            with zipfile.ZipFile(entry.path) as archive:
                if any(name.split("/", 1)[0] in INSTALLERS for name in archive.namelist()):
                    present.append(_display(root, entry.path) + "/ensurepip")
    return sorted(set(present))


def records_digest(site: str) -> str:
    """SHA-256 of the set of RECORD files: the value --records pins."""
    return _read_records(os.path.abspath(site))[1]


class _Inventory:
    def __init__(self, site: str, root: str, records=None) -> None:
        self.site = site
        self.root = root
        rows, self.records_digest = records if records is not None else _read_records(site)
        self.hashed: dict[str, tuple[str, int, str]] = {}  # key -> (urlsafe digest, size, display)
        self.paths: dict[str, tuple[str, str]] = {}  # key -> (path, display) of every entry the record binds
        self.unchecked: set[str] = set()  # keys of bytecode RECORD lists without a hash
        prefix = _prefix(root)
        listed: set[str] = set()
        for distribution, relative, digest, size in rows:
            target = os.path.normpath(os.path.join(site, relative))
            key = os.path.normcase(target)
            if not key.startswith(prefix):
                raise Refusal("%s/RECORD names a path outside the environment: %s" % (distribution, relative))
            display = target[len(prefix):].replace(os.sep, "/")
            listed.add(key)
            if not digest:
                if not _hashless_allowed(relative):
                    raise Refusal("%s/RECORD lists %s without a hash" % (distribution, display))
                if os.path.lexists(target):
                    self.paths[key] = (target, display)
                    if target.endswith(".pyc"):
                        self.unchecked.add(key)
                continue
            algorithm, _, value = digest.partition("=")
            if algorithm != "sha256" or not value:
                raise Refusal("%s/RECORD uses %s for %s; OA checks sha256" % (distribution, algorithm or "no hash", display))
            try:
                expected_size = int(size)
            except ValueError:
                raise Refusal("%s/RECORD has no size for %s" % (distribution, display)) from None
            previous = self.hashed.get(key)
            if previous is not None and previous[:2] != (value, expected_size):
                raise Refusal("two RECORD files disagree about %s" % display)
            self.hashed[key] = (value, expected_size, display)
            self.paths[key] = (target, display)
        for path in _walk(site, root):
            target = os.path.normpath(path)
            key = os.path.normcase(target)
            display = target[len(prefix):].replace(os.sep, "/")
            if key not in listed:
                raise Refusal("%s is not listed by any RECORD" % display)
            self.paths[key] = (target, display)

    @property
    def unchecked_bytecode(self) -> int:
        return len(self.unchecked)

    def hold(self) -> dict[str, object]:
        """Open every bound entry with read sharing only; see hold_file.

        Bytecode RECORD lists without a hash is left out: the launch points
        sys.pycache_prefix elsewhere and never reads it.
        """
        held: dict[str, object] = {}
        try:
            for key, (path, display) in sorted(self.paths.items()):
                if key not in self.unchecked:
                    handle = hold_file(path, display)
                    if handle is not None:
                        held[key] = handle
        except BaseException:
            release(held.values())
            raise
        return held

    def metadata(self, held: dict[str, object] | None = None) -> dict[str, list[int]]:
        result = {}
        for key, (path, display) in self.paths.items():
            handle = held.get(key) if held else None
            try:
                result[display] = _metadata(path) if handle is None else _handle_metadata(handle, path)
            except FileNotFoundError:
                raise Refusal("%s is listed in RECORD but missing" % display) from None
        return result

    def hash_all(self) -> None:
        for key, (expected, size, display) in sorted(self.hashed.items()):
            path = self.paths[key][0]
            try:
                info = os.lstat(path)
            except FileNotFoundError:
                raise Refusal("%s is listed in RECORD but missing" % display) from None
            if not stat.S_ISREG(info.st_mode) or _is_link(info):
                raise Refusal("%s is not a regular file" % display)
            digest = hashlib.sha256()
            total = 0
            with open(path, "rb") as stream:
                for block in iter(lambda: stream.read(1 << 20), b""):
                    total += len(block)
                    digest.update(block)
            actual = base64.urlsafe_b64encode(digest.digest()).decode("ascii").rstrip("=")
            if total != size or actual != expected:
                raise Refusal("%s differs from its RECORD hash" % display)


if os.name == "nt":
    import ctypes
    from ctypes import wintypes

    _kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    _CreateFileW = _kernel32.CreateFileW
    _CreateFileW.argtypes = (wintypes.LPCWSTR, wintypes.DWORD, wintypes.DWORD, wintypes.LPVOID,
                             wintypes.DWORD, wintypes.DWORD, wintypes.HANDLE)
    _CreateFileW.restype = wintypes.HANDLE
    _CloseHandle = _kernel32.CloseHandle
    _CloseHandle.argtypes = (wintypes.HANDLE,)

    class _FileTime(ctypes.Structure):
        _fields_ = (("low", wintypes.DWORD), ("high", wintypes.DWORD))

    class _HandleInfo(ctypes.Structure):
        _fields_ = (("attributes", wintypes.DWORD), ("creation", _FileTime), ("access", _FileTime),
                    ("write", _FileTime), ("volume", wintypes.DWORD), ("size_high", wintypes.DWORD),
                    ("size_low", wintypes.DWORD), ("links", wintypes.DWORD),
                    ("index_high", wintypes.DWORD), ("index_low", wintypes.DWORD))

    class _BasicInfo(ctypes.Structure):
        _fields_ = (("creation", ctypes.c_longlong), ("access", ctypes.c_longlong),
                    ("write", ctypes.c_longlong), ("change", ctypes.c_longlong),
                    ("attributes", wintypes.DWORD))

    _GetFileInformationByHandle = _kernel32.GetFileInformationByHandle
    _GetFileInformationByHandle.argtypes = (wintypes.HANDLE, ctypes.POINTER(_HandleInfo))
    _GetFileInformationByHandleEx = _kernel32.GetFileInformationByHandleEx
    _GetFileInformationByHandleEx.argtypes = (wintypes.HANDLE, ctypes.c_int, wintypes.LPVOID, wintypes.DWORD)
    _INVALID_HANDLE = ctypes.c_void_p(-1).value
    _GENERIC_READ = 0x80000000
    _FILE_READ_ATTRIBUTES = 0x80
    _FILE_SHARE_READ = 0x1
    _FILE_SHARE_ALL = 0x7
    _OPEN_EXISTING = 3
    # FILE_FLAG_BACKUP_SEMANTICS (a folder opens too) | FILE_FLAG_OPEN_REPARSE_POINT (a link opens itself).
    _OPEN_FLAGS = 0x02000000 | 0x00200000
    _ERROR_SHARING_VIOLATION = 32

    def _open(path: str, access: int, share: int):
        absolute = os.path.abspath(path)
        if not absolute.startswith("\\\\"):
            absolute = "\\\\?\\" + absolute
        handle = _CreateFileW(absolute, access, share, None, _OPEN_EXISTING, _OPEN_FLAGS, None)
        if handle == _INVALID_HANDLE or handle is None:
            error = ctypes.get_last_error()
            if error in (2, 3):
                raise FileNotFoundError(path)
            raise OSError(error, ctypes.FormatError(error), path)
        return handle

    def _handle_metadata(handle, path: str) -> list[int]:
        """Volume, file ID, size, last-write and change time, attributes."""
        info = _HandleInfo()
        basic = _BasicInfo()
        if not _GetFileInformationByHandle(handle, ctypes.byref(info)) or not \
                _GetFileInformationByHandleEx(handle, 0, ctypes.byref(basic), ctypes.sizeof(basic)):
            error = ctypes.get_last_error()
            raise OSError(error, ctypes.FormatError(error), path)
        return [info.volume, (info.index_high << 32) | info.index_low, (info.size_high << 32) | info.size_low,
                basic.write, basic.change, basic.attributes]

    def _metadata(path: str) -> list[int]:
        # Attribute access only: this open takes no part in sharing checks.
        handle = _open(path, _FILE_READ_ATTRIBUTES, _FILE_SHARE_ALL)
        try:
            return _handle_metadata(handle, path)
        finally:
            _CloseHandle(handle)

    def hold_file(path: str, display: str):
        """Open path for reading with FILE_SHARE_READ only and return the handle.

        While the handle is open Windows refuses any open for writing,
        appending or deleting, which a write, truncation, rename or delete of
        the file needs, and a rename of any folder above it. Opens for reading
        and executing (Python's import, LoadLibrary, a GPU runtime reading a
        code object) still succeed. The handle is not inheritable.
        """
        try:
            return _open(path, _GENERIC_READ, _FILE_SHARE_READ)
        except FileNotFoundError:
            raise Refusal("%s is listed in RECORD but missing" % display) from None
        except OSError as error:
            if error.errno == _ERROR_SHARING_VIOLATION:
                raise Refusal("%s is open for writing or deleting, so it cannot be held read-only for the run"
                              % display) from None
            raise Refusal("%s cannot be held open: %s" % (display, error.strerror)) from None

    def release(handles) -> None:
        for handle in handles:
            _CloseHandle(handle)
else:
    def _metadata(path: str) -> list[int]:
        info = os.lstat(path)
        return [info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns, info.st_mode]

    def _handle_metadata(handle, path: str) -> list[int]:
        return _metadata(path)

    def hold_file(path: str, display: str):
        """POSIX has no share modes: a held descriptor does not stop a write, rename or delete."""
        return None

    def release(handles) -> None:
        pass


def _load_record(state: str) -> dict | None:
    try:
        with open(os.path.join(state, RECORD_NAME), encoding="utf-8") as stream:
            record = json.load(stream)
    except (OSError, ValueError):
        return None
    return record if isinstance(record, dict) else None


def _write_record(state: str, record: dict) -> None:
    os.makedirs(state, exist_ok=True)
    handle, temporary = tempfile.mkstemp(prefix=".record-", suffix=".tmp", dir=state)
    try:
        with os.fdopen(handle, "w", encoding="utf-8") as stream:
            json.dump(record, stream, separators=(",", ":"), sort_keys=True)
        os.replace(temporary, os.path.join(state, RECORD_NAME))
    except BaseException:
        try:
            os.unlink(temporary)
        except OSError:
            pass
        raise


def _check_records_pin(digest: str, expected: str | None) -> None:
    if expected is not None and digest != expected.lower():
        raise Refusal("the set of RECORD files differs from this package's pin (%s, expected %s): a "
                      "distribution was added, removed or changed" % (digest, expected.lower()))


def check_environment(site: str, root: str, state: str | None = None, *,
                      allow_installer: bool = False, records: str | None = None,
                      hold: bool = False) -> dict[str, object]:
    """Prove site-packages against its RECORD files; raise Refusal naming the file.

    With records, the RECORD set must equal that pin before anything else is
    read. With state, a record of the last full hash lets a later launch
    compare file metadata instead; any difference runs the full hash again.
    With hold, every bound entry is opened with read sharing only before its
    metadata is read, and the report's "handles" keeps those handles open:
    the files checked are the files that stay in place until the process
    exits.
    """
    began = time.monotonic()
    site = os.path.abspath(site)
    root = os.path.abspath(root)
    if not _within(root, site):
        raise Refusal("site-packages is outside the environment root")
    rows = _read_records(site)
    _check_records_pin(rows[1], records)
    if not allow_installer:
        present = installer_presence(site, root)
        if present:
            raise Refusal("the environment has an installer (%s); OA runs ComfyUI only without pip or "
                          "ensurepip" % ", ".join(present))
    inventory = _Inventory(site, root, rows)
    held = inventory.hold() if hold else {}
    try:
        if held and _read_records(site)[1] != inventory.records_digest:
            raise Refusal("a RECORD file changed while it was being checked")
        observed = inventory.metadata(held)
        binding = {"format": RECORD_FORMAT, "root": os.path.normcase(os.path.realpath(root)),
                   "site": os.path.normcase(os.path.realpath(site)), "records": inventory.records_digest,
                   "python": sys.version}
        report = {"files": len(observed), "hashed": len(inventory.hashed),
                  "unchecked_bytecode": inventory.unchecked_bytecode, "held": len(held)}
        previous = _load_record(state) if state is not None else None
        if previous is not None and all(previous.get(key) == value for key, value in binding.items()) \
                and previous.get("files") == observed:
            report["mode"] = "metadata"
        else:
            inventory.hash_all()
            after = inventory.metadata(held)
            if after != observed:
                changed = sorted(name for name in set(after) | set(observed) if after.get(name) != observed.get(name))
                raise Refusal("%s changed while it was being checked" % changed[0])
            if state is not None:
                _write_record(state, {**binding, "files": observed})
            report["mode"] = "full"
    except BaseException:
        release(held.values())
        raise
    report["seconds"] = round(time.monotonic() - began, 3)
    if hold:
        report["handles"] = list(held.values())
    return report


# ---------------------------------------------------------------- weights


def _weight_message(target: str) -> str:
    return ("OpenAbstractions refused to load %s before reading it: this ComfyUI loads safetensors weights "
            "only. Pickle-based files (.ckpt, .pt, .pth, .bin) can run code when PyTorch 2.9.1 opens them, "
            "even with weights_only=True (CVE-2026-24747). Use a .safetensors file." % target)


def _describe(target: object) -> str:
    if isinstance(target, (str, os.PathLike)):
        return os.fspath(target)
    name = getattr(target, "name", None)
    return name if isinstance(name, str) else type(target).__name__


def refuse_unpickle(*args, **kwargs):
    raise WeightRefused(_weight_message(_describe(args[0] if args else kwargs.get("f"))))


def _guard_load_attribute(module) -> None:
    module.load = refuse_unpickle


def _guard_torch(module) -> None:
    module.load = refuse_unpickle
    for name in ("torch.serialization", "torch.jit", "torch.jit._serialization", "torch.export"):
        loaded = sys.modules.get(name)
        if loaded is not None:
            loaded.load = refuse_unpickle


def _guard_comfy_utils(module) -> None:
    original = getattr(module, "load_torch_file", None)
    if original is None or getattr(original, "__oa_weight_guard__", False):
        return

    @functools.wraps(original)
    def load_torch_file(ckpt, *args, **kwargs):
        name = os.fspath(ckpt) if isinstance(ckpt, (str, os.PathLike)) else None
        if not isinstance(name, str) or not name.lower().endswith(SAFE_WEIGHT_SUFFIXES):
            raise WeightRefused(_weight_message(_describe(ckpt)))
        return original(ckpt, *args, **kwargs)

    load_torch_file.__oa_weight_guard__ = True
    module.load_torch_file = load_torch_file


def _guard_sd1_clip(module) -> None:
    if hasattr(module, "safe_load_embed_zip"):
        module.safe_load_embed_zip = refuse_unpickle


def _guard_folder_paths(module) -> None:
    extensions = getattr(module, "supported_pt_extensions", None)
    if isinstance(extensions, set):
        for suffix in PICKLE_SUFFIXES:
            extensions.discard(suffix)


WEIGHT_GUARDS = {
    "torch.serialization": _guard_load_attribute,
    "torch.jit._serialization": _guard_load_attribute,
    "torch.jit": _guard_load_attribute,
    "torch.export": _guard_load_attribute,
    "torch": _guard_torch,
    "comfy.utils": _guard_comfy_utils,
    "comfy.sd1_clip": _guard_sd1_clip,
    "folder_paths": _guard_folder_paths,
}


class _PatchingLoader:
    def __init__(self, loader, patch) -> None:
        self._loader = loader
        self._patch = patch

    def create_module(self, spec):
        return self._loader.create_module(spec)

    def exec_module(self, module) -> None:
        self._loader.exec_module(module)
        self._patch(module)

    def __getattr__(self, name):
        return getattr(self._loader, name)


class _WeightGuardFinder:
    """Apply a guard to a module as soon as its own code has run."""

    def find_spec(self, name, path, target=None):
        patch = WEIGHT_GUARDS.get(name)
        if patch is None:
            return None
        for finder in sys.meta_path:
            if finder is self or not hasattr(finder, "find_spec"):
                continue
            spec = finder.find_spec(name, path, target)
            if spec is not None:
                break
        else:
            return None
        if spec.loader is not None and hasattr(spec.loader, "exec_module"):
            spec.loader = _PatchingLoader(spec.loader, patch)
        return spec


def install_weight_guard() -> None:
    if not any(isinstance(finder, _WeightGuardFinder) for finder in sys.meta_path):
        sys.meta_path.insert(0, _WeightGuardFinder())
    for name, patch in WEIGHT_GUARDS.items():
        module = sys.modules.get(name)
        if module is not None:
            patch(module)


# ---------------------------------------------------------------- launch


def _environment_paths() -> tuple[str, str]:
    paths = sysconfig.get_paths()
    if os.path.normcase(paths["purelib"]) != os.path.normcase(paths["platlib"]):
        raise Refusal("this launcher supports one site-packages directory")
    return paths["purelib"], sys.prefix


def _parse(argv: list[str]) -> tuple[str | None, str | None, bool, str, list[str]]:
    if "--" not in argv:
        raise SystemExit(__doc__)
    split = argv.index("--")
    options, rest = argv[:split], argv[split + 1:]
    state = None
    records = None
    allow_installer = False
    index = 0
    while index < len(options):
        option = options[index]
        if option == "--state" and index + 1 < len(options):
            state = options[index + 1]
            index += 2
        elif option == "--records" and index + 1 < len(options):
            records = options[index + 1]
            if len(records) != 64 or any(char not in "0123456789abcdefABCDEF" for char in records):
                raise SystemExit("--records takes the 64-hex SHA-256 of the RECORD set\n" + __doc__)
            index += 2
        elif option == "--allow-installer":
            allow_installer = True
            index += 1
        else:
            raise SystemExit("unknown option %r\n%s" % (option, __doc__))
    if not rest or not rest[0].endswith(".py"):
        raise SystemExit("name ComfyUI's main.py after --\n" + __doc__)
    return state, records, allow_installer, os.path.abspath(rest[0]), rest[1:]


def command(python, main, arguments=(), *, state=None, records: str | None = None,
            allow_installer: bool = False) -> list[str]:
    """The argv that starts ComfyUI's main through this launcher with python.

    Runners that start ComfyUI build their command here, so they launch it the
    way the OA package and the activation recipe do.
    """
    options = []
    if state is not None:
        options += ["--state", os.fspath(state)]
    if records:
        options += ["--records", records]
    if allow_installer:
        options.append("--allow-installer")
    return [os.fspath(python), "-B", "-s", os.path.abspath(__file__), *options, "--", os.fspath(main),
            *(os.fspath(item) if isinstance(item, os.PathLike) else str(item) for item in arguments)]


MANAGER_RESTART_LIMIT = ("ComfyUI-Manager is enabled: its restart re-executes main.py with os.execv, and the "
                         "restarted ComfyUI runs without the OA launcher's RECORD check, held files and weight "
                         "guard")


def run_limits(arguments) -> list[str]:
    """Limits of the launcher's guarantees for a run of ComfyUI with these arguments."""
    if any(str(item).startswith("--enable-manager") for item in arguments):
        return [MANAGER_RESTART_LIMIT]
    return []


def _empty_bytecode_folder() -> str:
    """A new private folder no bytecode can be planted in ahead of this run."""
    import atexit
    import shutil
    folder = tempfile.mkdtemp(prefix="oa-comfyui-no-bytecode-")
    atexit.register(shutil.rmtree, folder, True)
    return folder


def main(argv: list[str] | None = None) -> int:
    if argv is None:
        argv = sys.argv[1:]
    if not argv or argv[0] in ("-h", "--help"):
        print(__doc__)
        return 0
    state, records, allow_installer, entry, arguments = _parse(argv)
    try:
        site_packages, root = _environment_paths()
        if records is not None:
            _check_records_pin(records_digest(site_packages), records)
        if not allow_installer and not sys.flags.no_site:
            raise Refusal("this Python imported site at start-up, so site-packages .pth files ran before "
                          "the check; the OA package's python312._pth has no 'import site' line")
        if not allow_installer:
            for name in INSTALLERS:
                spec = importlib.util.find_spec(name)
                if spec is not None:
                    raise Refusal("%s is importable from %s; OA runs ComfyUI only without pip or ensurepip"
                                  % (name, spec.origin))
        report = check_environment(site_packages, root, state, allow_installer=allow_installer, records=records,
                                   hold=True)
    except Refusal as refusal:
        print("OpenAbstractions refused to start ComfyUI: %s" % refusal, file=sys.stderr, flush=True)
        return EXIT_REFUSED
    # Open until this process, and ComfyUI running in it, exits.
    _HELD.extend(report.pop("handles", []))
    if report["unchecked_bytecode"]:
        # RECORD carries no hash for this bytecode: compile from the checked
        # sources and never read it.
        sys.dont_write_bytecode = True
        sys.pycache_prefix = _empty_bytecode_folder()
    install_weight_guard()
    if sys.flags.no_site:
        # The checked site-packages joins sys.path now; its .pth files run here.
        import site
        site.addsitedir(site_packages)
        site.setquit()
        site.setcopyright()
        site.sethelper()
    print("[OpenAbstractions] private Python matches its wheels (%s check of %d files in %.1f s, %d held "
          "read-only until exit); safetensors weights only"
          % (report["mode"], report["files"], report["seconds"], report["held"]), file=sys.stderr, flush=True)
    sys.argv = [entry, *arguments]
    directory = os.path.dirname(entry)
    if directory not in sys.path:
        sys.path.insert(0, directory)
    import runpy
    runpy.run_path(entry, run_name="__main__")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
