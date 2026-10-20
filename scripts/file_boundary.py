"""Local release-build filesystem boundaries, including Windows deep paths."""

from contextlib import contextmanager
import os
from pathlib import Path
import shutil
import stat
import sys
import tempfile


def extended_path(path: os.PathLike[str] | str) -> Path:
    absolute = Path(path).absolute()
    if os.name == "nt":
        name = str(absolute)
        if name.startswith("\\\\?\\"):
            return absolute
        if name.startswith("\\\\"):
            return Path("\\\\?\\UNC\\" + name[2:])
        return Path("\\\\?\\" + name)
    return absolute


def remove_tree(path: os.PathLike[str] | str) -> None:
    """Remove one owned tree without following links or losing deep Windows files."""
    target = extended_path(path)
    if not os.path.lexists(target):
        return
    info = target.lstat()
    if stat.S_ISLNK(info.st_mode) or getattr(info, "st_file_attributes", 0) & getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0):
        raise ValueError("refusing to recurse into a linked removal root")
    if not stat.S_ISDIR(info.st_mode):
        raise ValueError("removal root must be a directory")

    def writable(function, name, _error):
        os.chmod(name, stat.S_IWRITE | stat.S_IREAD | stat.S_IEXEC)
        function(name)

    if sys.version_info >= (3, 12):
        shutil.rmtree(target, onexc=writable)
    else:
        shutil.rmtree(target, onerror=writable)


@contextmanager
def owned_temporary_tree(*, prefix: str, parent: Path):
    path = Path(tempfile.mkdtemp(prefix=prefix, dir=parent))
    try:
        yield path
    finally:
        remove_tree(path)
