"""Compare-and-set over a file. Semantics in abstraction-cas/README.md."""

import contextlib
import os
import sys
import tempfile
import threading
import time


class Moved(Exception):
    """The file changed since it was read."""


class NoValue(Exception):
    """An edit returned no value where the file holds one."""


class Reentrant(Exception):
    """A write or change ran inside an edit on the same file."""


class OutsideRoot(ValueError):
    """A Placement was given its root, a path outside it, or a path in its side directory."""


class CrossVolume(OSError):
    """A Placement's side directory is on another volume than its root."""


class Refused(TimeoutError):
    """Opening the file answered access denied or a sharing violation until the
    reader's timeout. The last answer is the exception's __cause__."""


def read(path, timeout=None):
    """The whole file, or None when there is none.

    A writer's replace denies an open for an instant, so a denied open is
    retried. With timeout in seconds, a file still denied when it ends raises
    Refused; without one the retry ends after its try count.
    """
    deadline = None if timeout is None else time.monotonic() + timeout
    return _read_native(_native(path), deadline)


def write(path, base, data):
    with _locked(path):
        _replace(path, base, data)


def change(path, edit):
    _change_at(_beside(path), edit)


def sweep(path):
    """Remove the temporaries a killed writer left beside path; return how many.

    Not on the write path, where it would cost every write the size of the
    directory. Holding the lock is what makes it safe: a live writer stages
    under the lock, so every temporary beside the file is a dead one.
    """
    return _sweep_at(_beside(path))


class Placement:
    """The lock file and staged replacements of every file under root, kept in side.

    side is a directory on root's volume, so root holds only data files. For
    root/a/b the lock is side/a/b.lock, and a replacement is staged as
    side/a/b.<unique>.tmp before it is renamed over root/a/b. Every writer of a
    file must use the same placement: a writer beside the file takes another
    lock and excludes nobody.
    """

    def __init__(self, root, side):
        self.root = os.path.abspath(os.fspath(root))
        self.side = os.path.abspath(os.fspath(side))
        if _relative(self.side, self.root) is not None:
            raise ValueError("cas: side directory %s contains root %s" % (self.side, self.root))
        for directory in (self.root, self.side):
            os.makedirs(_native(directory), exist_ok=True)
        if _volume(_native(self.root)) != _volume(_native(self.side)):
            raise CrossVolume("cas: side directory %s is on another volume than root %s" % (self.side, self.root))

    def read(self, path):
        return _read_native(self._place(path).target)

    def write(self, path, base, data):
        placed = self._place(path)
        with _locked_at(placed):
            _replace_at(placed, base, data)

    def change(self, path, edit):
        _change_at(self._place(path), edit)

    def sweep(self, path):
        """Remove the temporaries a killed writer left in side for path; return how many."""
        return _sweep_at(self._place(path))

    def _place(self, path):
        target = os.path.abspath(os.fspath(path))
        rel = _relative(self.root, target)
        if rel is None or rel == os.curdir or _relative(self.side, target) is not None:
            raise OutsideRoot("cas: %s is not a file under %s outside %s" % (target, self.root, self.side))
        mirror = _native(os.path.join(self.side, rel))
        return _Placed(_native(target), mirror + ".lock", os.path.dirname(mirror))


class _Placed:
    """The files one write uses: the data file, its lock and the directory its replacement is staged in."""

    __slots__ = ("target", "lock", "stage")

    def __init__(self, target, lock, stage):
        self.target, self.lock, self.stage = target, lock, stage


def _beside(path):
    native = _native(path)
    return _Placed(native, native + ".lock", os.path.dirname(native))


def _relative(parent, child):
    """child relative to parent, or None when child is neither parent nor under it."""
    try:
        rel = os.path.relpath(child, parent)
    except ValueError:
        return None
    if rel == os.pardir or rel.startswith(os.pardir + os.sep) or os.path.isabs(rel):
        return None
    return rel


def _volume(directory):
    return os.stat(directory).st_dev


def _read_native(native, deadline=None):
    # A read races every writer's rename. While a replaced file is being deleted,
    # opening its name on Windows answers access denied or a sharing violation;
    # the rename already retries those, and the read does too, until its try
    # count or the reader's deadline ends.
    for tries in range(2001):
        try:
            return _read(native)
        except FileNotFoundError:
            return None
        except _TRANSIENT as denied:
            if tries == 2000:
                raise
            if deadline is not None and time.monotonic() >= deadline:
                raise Refused("cas: %s refused every open until the reader's timeout" % native) from denied
            if tries >= 50:
                time.sleep(0.001)


def _change_at(placed, edit):
    with _locked_at(placed):
        cur = _read_native(placed.target)
        nxt = edit(cur)
        if nxt != cur:
            _replace_at(placed, cur, nxt)


_UNIQUE = frozenset("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_")


def _staged_name(name, prefix):
    """Whether name is <prefix><unique>.tmp with a unique part of letters, digits
    and underscores: the names mkstemp, Go's CreateTemp and the C++ writer give a
    replacement. A dot in the unique part belongs to another file's name, such as
    <path>.lock.<unique>.tmp."""
    suffix = ".tmp"
    if len(name) <= len(prefix) + len(suffix) or not name.startswith(prefix) or not name.endswith(suffix):
        return False
    return all(c in _UNIQUE for c in name[len(prefix):-len(suffix)])


def _sweep_at(placed):
    with _locked_at(placed):
        prefix = os.path.basename(placed.target) + "."
        gone = 0
        for name in os.listdir(placed.stage):
            orphan = os.path.join(placed.stage, name)
            if not _staged_name(name, prefix) or not os.path.isfile(orphan):
                continue
            with contextlib.suppress(OSError):
                os.unlink(orphan)
                gone += 1
        return gone


_held = threading.local()


def _locked(path):
    return _locked_at(_beside(path))


@contextlib.contextmanager
def _locked_at(placed):
    holding = getattr(_held, "paths", None)
    if holding is None:
        holding = _held.paths = set()
    if placed.target in holding:
        raise Reentrant(placed.target)
    os.makedirs(os.path.dirname(placed.target), exist_ok=True)
    os.makedirs(os.path.dirname(placed.lock), exist_ok=True)
    fd = os.open(placed.lock, os.O_CREAT | os.O_RDWR, 0o600)
    holding.add(placed.target)
    try:
        _flock(fd)
        yield
    finally:
        holding.discard(placed.target)
        os.close(fd)


def _replace(path, base, data):
    _replace_at(_beside(path), base, data)


def _replace_at(placed, base, data):
    if data is None:
        raise NoValue(placed.target)
    if _read_native(placed.target) != base:
        raise Moved(placed.target)
    directory = os.path.dirname(placed.target)
    fd, tmp = tempfile.mkstemp(prefix=os.path.basename(placed.target) + ".", suffix=".tmp", dir=placed.stage)
    try:
        with os.fdopen(fd, "wb") as f:
            f.write(data)
            f.flush()
            _fsync_file(f.fileno())
        _rename_over(tmp, placed.target)
    except BaseException:
        with contextlib.suppress(OSError):
            os.unlink(tmp)
        raise
    _fsync_dir(directory)


def _rename_over(tmp, native):
    for _ in range(999):
        try:
            return _rename(tmp, native)
        except _TRANSIENT:
            pass
    _rename(tmp, native)


_MAX_PATH = 260
_DERIVED = len(".XXXXXXXX.tmp")

if sys.platform == "win32":
    import ctypes
    import msvcrt
    from ctypes import wintypes

    _k32 = ctypes.WinDLL("kernel32", use_last_error=True)
    _k32.CreateFileW.argtypes = [wintypes.LPCWSTR, wintypes.DWORD, wintypes.DWORD, wintypes.LPVOID,
                                 wintypes.DWORD, wintypes.DWORD, wintypes.HANDLE]
    _k32.CreateFileW.restype = wintypes.HANDLE
    _k32.CloseHandle.argtypes = [wintypes.HANDLE]
    _k32.FlushFileBuffers.argtypes = [wintypes.HANDLE]
    _k32.LockFileEx.argtypes = [wintypes.HANDLE, wintypes.DWORD, wintypes.DWORD, wintypes.DWORD,
                                wintypes.DWORD, wintypes.LPVOID]
    _k32.SetFileInformationByHandle.argtypes = [wintypes.HANDLE, ctypes.c_int, wintypes.LPVOID, wintypes.DWORD]

    _GENERIC_READ, _GENERIC_WRITE, _DELETE = 0x80000000, 0x40000000, 0x00010000
    _SHARE_ALL, _OPEN_EXISTING, _BACKUP_SEMANTICS = 0x7, 3, 0x02000000
    _INVALID_HANDLE = wintypes.HANDLE(-1).value
    _EXCLUSIVE_LOCK = 2
    _FILE_RENAME_INFO_EX = 22
    _REPLACE_IF_EXISTS, _POSIX_SEMANTICS = 1, 2
    _ERROR_INVALID_FUNCTION, _ERROR_ACCESS_DENIED = 1, 5
    _ERROR_NOT_SUPPORTED, _ERROR_INVALID_PARAMETER = 50, 87
    _NO_DIRECTORY_FLUSH = (_ERROR_INVALID_FUNCTION, _ERROR_ACCESS_DENIED, _ERROR_NOT_SUPPORTED)
    _TRANSIENT = PermissionError

    def _native(path):
        p = os.path.abspath(os.fspath(path))
        if len(p) + _DERIVED < _MAX_PATH or p.startswith("\\\\?\\"):
            return p
        return "\\\\?\\UNC" + p[1:] if p.startswith("\\\\") else "\\\\?\\" + p

    def _open_shared(path, access, flags=0):
        h = _k32.CreateFileW(path, access, _SHARE_ALL, None, _OPEN_EXISTING, flags, None)
        if h == _INVALID_HANDLE:
            raise ctypes.WinError(ctypes.get_last_error())
        return h

    def _read(path):
        fd = msvcrt.open_osfhandle(_open_shared(path, _GENERIC_READ), os.O_RDONLY)
        with os.fdopen(fd, "rb") as f:
            return f.read()

    def _flock(fd):
        overlapped = ctypes.create_string_buffer(32)
        if not _k32.LockFileEx(msvcrt.get_osfhandle(fd), _EXCLUSIVE_LOCK, 0, 1, 0, overlapped):
            raise ctypes.WinError(ctypes.get_last_error())

    _fsync_file = os.fsync

    def _fsync_dir(directory):
        try:
            h = _open_shared(directory, _GENERIC_WRITE, _BACKUP_SEMANTICS)
        except OSError as e:
            if e.winerror in _NO_DIRECTORY_FLUSH:
                return
            raise
        try:
            if not _k32.FlushFileBuffers(h):
                err = ctypes.get_last_error()
                if err not in _NO_DIRECTORY_FLUSH:
                    raise ctypes.WinError(err)
        finally:
            _k32.CloseHandle(h)

    def _rename_info(target):
        units = len(target.encode("utf-16-le")) // 2

        class Info(ctypes.Structure):
            _fields_ = [("Flags", wintypes.DWORD), ("RootDirectory", wintypes.HANDLE),
                        ("FileNameLength", wintypes.DWORD), ("FileName", wintypes.WCHAR * (units + 1))]
        return Info(_REPLACE_IF_EXISTS | _POSIX_SEMANTICS, None, units * 2, target)

    def _rename(tmp, native):
        h = _open_shared(tmp, _DELETE)
        try:
            info = _rename_info(native)
            err = 0 if _k32.SetFileInformationByHandle(h, _FILE_RENAME_INFO_EX, ctypes.byref(info),
                                                         ctypes.sizeof(info)) else ctypes.get_last_error()
        finally:
            _k32.CloseHandle(h)
        if err in (_ERROR_NOT_SUPPORTED, _ERROR_INVALID_PARAMETER):
            os.replace(tmp, native)
        elif err:
            raise ctypes.WinError(err)

else:
    import errno
    import fcntl

    _TRANSIENT = ()
    _NO_DIRECTORY_FLUSH = (errno.EINVAL, errno.ENOTSUP)
    _NO_FULL_FSYNC = (errno.ENOTSUP,)

    def _native(path):
        return os.path.abspath(os.fspath(path))

    def _read(path):
        with open(path, "rb") as f:
            return f.read()

    def _flock(fd):
        fcntl.flock(fd, fcntl.LOCK_EX)

    def _fsync_file(fd):
        # macOS fsync leaves data in the drive cache; F_FULLFSYNC flushes it.
        # A volume that refuses F_FULLFSYNC with ENOTSUP gets the plain fsync, as in Go.
        full = getattr(fcntl, "F_FULLFSYNC", None)
        if full is not None:
            try:
                fcntl.fcntl(fd, full)
                return
            except OSError as e:
                if e.errno not in _NO_FULL_FSYNC:
                    raise
        os.fsync(fd)

    def _fsync_dir(directory):
        fd = os.open(directory, os.O_RDONLY)
        try:
            _fsync_file(fd)
        except OSError as e:
            if e.errno not in _NO_DIRECTORY_FLUSH:
                raise
        finally:
            os.close(fd)

    def _rename(tmp, native):
        os.replace(tmp, native)
