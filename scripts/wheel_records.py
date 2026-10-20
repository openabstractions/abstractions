"""Fill the RECORD hashes the OA ComfyUI launch check requires, right after pip installs.

The launch check (adopters/comfyui/oa_comfyui_launch.py) refuses a code file
whose RECORD row carries no hash. pip copies a wheel's RECORD hash only when the
row's path matches the installed file. torchvision's Windows CPU wheels
(0.24.1+cpu pinned by scripts/package_comfyui_app.py) name their files with
backslashes, so pip keeps those rows and lists every installed file again under
its forward-slash path with no hash.

fill() is the one place those hashes are written. Both callers run it right
after their pip install:

  scripts/package_comfyui_app.py        passes the wheels it installed and their
                                        pinned SHA-256; each wheel is re-hashed,
                                        and each filled file must equal the
                                        member of the wheel that installed it
  research/adoption/comfyui/prepare.py  passes trust_installed=True; its wheels
                                        come from the PyTorch index without a
                                        pin, so the installed bytes are taken as
                                        they are

A row that already carries a hash is left as it is. Rows the launch check lets
go without a hash (data files, __pycache__ bytecode) are left as they are. A
refusal writes no RECORD.
"""

from __future__ import annotations

import base64
import csv
import hashlib
import io
import os
from pathlib import Path
from typing import Callable, Mapping
import zipfile


def _digest(data: bytes) -> str:
    return "sha256=" + base64.urlsafe_b64encode(hashlib.sha256(data).digest()).decode("ascii").rstrip("=")


def _file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _wheel_index(wheels: Mapping[Path, str]) -> dict[str, Path]:
    """Re-hash each wheel against its pin; map its .dist-info directory name to it."""
    index: dict[str, Path] = {}
    for wheel, pinned in wheels.items():
        wheel = Path(wheel)
        if _file_sha256(wheel) != pinned:
            raise ValueError(f"{wheel.name} differs from its pinned SHA-256")
        with zipfile.ZipFile(wheel) as archive:
            names = {name.split("/", 1)[0] for name in archive.namelist()
                     if name.split("/", 1)[0].endswith(".dist-info")}
        if len(names) != 1:
            raise ValueError(f"{wheel.name} does not hold exactly one .dist-info directory")
        name = names.pop()
        if name in index:
            raise ValueError(f"two verified wheels install {name}")
        index[name] = wheel
    return index


def _installed(site: str, dist_info: str, relative: str) -> bytes:
    """The installed bytes of a RECORD row, by the launch check's own path rule."""
    target = os.path.normpath(os.path.join(site, relative))
    if not os.path.normcase(target).startswith(os.path.normcase(os.path.join(site, ""))):
        raise ValueError(f"{dist_info}/RECORD names a path outside site-packages: {relative}")
    try:
        with open(target, "rb") as stream:
            return stream.read()
    except OSError:
        raise ValueError(f"{dist_info}/RECORD lists {relative}, which is not installed") from None


def fill(site: Path, hashless_allowed: Callable[[str], bool], *,
         wheels: Mapping[Path, str] | None = None, trust_installed: bool = False) -> list[str]:
    """Write the hash of each installed code file whose RECORD row has none.

    hashless_allowed is the launch check's rule for rows that may go without a
    hash. Pass wheels ({wheel path: pinned SHA-256}) or trust_installed=True,
    never both. With wheels, a wheel that differs from its pin, a row its wheel
    lacks, installed bytes that differ from the wheel, or a distribution no
    wheel installed is refused. Returns the filled rows as "<dist-info>/<path>".
    """
    if (wheels is None) == (not trust_installed):
        raise ValueError("fill takes either verified wheels or trust_installed=True")
    site_text = os.path.normpath(os.path.abspath(site))
    index = _wheel_index(wheels) if wheels is not None else None
    pending: list[tuple[Path, list[list[str]]]] = []
    filled: list[str] = []
    for record in sorted(Path(site_text).glob("*.dist-info/RECORD")):
        rows = list(csv.reader(io.StringIO(record.read_text(encoding="utf-8"), newline="")))
        dist_info = record.parent.name
        archive = None
        changed = False
        try:
            for row in rows:
                if len(row) != 3 or row[1] or hashless_allowed(row[0]):
                    continue
                if index is not None:
                    wheel = index.get(dist_info)
                    if wheel is None:
                        raise ValueError(f"{dist_info}/RECORD lists {row[0]} without a hash "
                                         "and no verified wheel installed it")
                    if archive is None:
                        archive = zipfile.ZipFile(wheel)
                    try:
                        expected = archive.read(row[0])
                    except KeyError:
                        raise ValueError(f"{dist_info}/RECORD: {row[0]} is not in {wheel.name}") from None
                    data = _installed(site_text, dist_info, row[0])
                    if expected != data:
                        raise ValueError(f"{dist_info}/RECORD: {row[0]} differs from {wheel.name}")
                else:
                    data = _installed(site_text, dist_info, row[0])
                row[1] = _digest(data)
                row[2] = str(len(data))
                filled.append(f"{dist_info}/{row[0]}")
                changed = True
        finally:
            if archive is not None:
                archive.close()
        if changed:
            pending.append((record, rows))
    # Nothing is written until every row of every RECORD is proven.
    for record, rows in pending:
        output = io.StringIO(newline="")
        csv.writer(output, lineterminator="\n").writerows(rows)
        record.write_text(output.getvalue(), encoding="utf-8", newline="")
    return filled
