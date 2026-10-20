#!/usr/bin/env python3
"""Build source-pinned ComfyUI package inputs.

Legacy portable mode repacks the exact upstream v0.35.0 asset below. Custom
Desktop mode builds an offline Manager-free AMD backend from an independently
admitted complete input closure. Its backend is an intermediate, not a final
signed Desktop delivery capsule or functional/GPU qualification.

The macos-python-host component stages an explicit existing shared-library
CPython home for the customized backend. It performs no signing or installation.
"""

from __future__ import annotations

import argparse
import base64
import csv
import configparser
import contextlib
from concurrent.futures import FIRST_COMPLETED, ThreadPoolExecutor, wait
from email.parser import Parser
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import time
import zipfile

if __package__:
    from .file_boundary import extended_path as _extended, plain_path as _plain, _selected_python_executable, owned_temporary_tree, windows_build_environment, same_existing_file, relative_beneath_existing, remove_tree, ordinary_path
    from . import wheel_records, app_pair_receipt
    from .git_env import source_git_environment
else:
    from file_boundary import extended_path as _extended, plain_path as _plain, _selected_python_executable, owned_temporary_tree, windows_build_environment, same_existing_file, relative_beneath_existing, remove_tree, ordinary_path
    import wheel_records, app_pair_receipt
    from git_env import source_git_environment


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


def _macos_dependencies(image: Path, run=subprocess.run) -> tuple[list[str], list[str]]:
    """Read load commands from an actual image; preserve paths containing spaces."""
    listed = run(['/usr/bin/otool', '-L', str(image)], check=True, capture_output=True, text=True).stdout
    identity = run(['/usr/bin/otool', '-D', str(image)], check=True, capture_output=True, text=True).stdout.splitlines()[1:]
    own = {line.strip() for line in identity}
    dependencies = []
    for line in listed.splitlines()[1:]:
        value, marker, _ = line.strip().rpartition(' (compatibility version ')
        if not marker or not value:
            raise ValueError('unrecognized Mach-O dependency metadata')
        if value not in own:
            dependencies.append(value)
    commands = run(['/usr/bin/otool', '-l', str(image)], check=True, capture_output=True, text=True).stdout.splitlines()
    rpaths = []
    for index, line in enumerate(commands):
        if line.strip() == 'cmd LC_RPATH':
            match = re.fullmatch(r'\s*path (.+) \(offset \d+\)', commands[index + 2])
            if not match:
                raise ValueError('unrecognized Mach-O runtime search path')
            rpaths.append(match[1])
    return dependencies, rpaths


def stage_macos_python_host(python: Path, output: Path, *, compiler: Path = Path('/usr/bin/clang'), sign_code=None) -> dict:
    """Stage an app-owned host from an explicit existing framework/PBS Python.

    This is a dependency component of the customized backend, never a signed
    application or a substitute Comfy installation. No acquisition or mutation
    of the selected interpreter occurs. Non-system Mach-O edges become app-local
    loader-relative paths. Final nested-code signing belongs to the normal late
    signing owner; this component is not signed for distribution here.
    """
    if sys.platform != 'darwin':
        raise ValueError('the macOS Python host must be built on macOS')
    python, output = Path(python).resolve(strict=True), Path(output).absolute()
    if output.exists() or output.is_symlink():
        raise ValueError('macOS Python host output must be fresh')
    query = 'import json,sys,sysconfig; print(json.dumps(dict(prefix=sys.base_prefix,include=sysconfig.get_path("include"),libdir=sysconfig.get_config_var("LIBDIR"),library=sysconfig.get_config_var("LDLIBRARY"),framework=sysconfig.get_config_var("PYTHONFRAMEWORK"),version=list(sys.version_info[:2]))))'
    info = json.loads(subprocess.run([str(python), '-I', '-B', '-c', query], check=True, capture_output=True, text=True, timeout=30).stdout)
    if info['version'] < [3, 11]:
        raise ValueError('the customized Python host requires CPython 3.11 or newer')
    prefix = Path(info['prefix']).resolve(strict=True)
    include = Path(info['include']).resolve(strict=True)
    library = ((prefix / info['framework']) if info['framework'] else Path(info['libdir']) / info['library']).resolve(strict=True)
    if not library.is_relative_to(prefix) or not include.is_relative_to(prefix):
        raise ValueError('Python headers and shared library must belong to its selected home')
    identity = subprocess.run(['/usr/bin/otool', '-D', str(library)], check=True, capture_output=True, text=True).stdout.splitlines()[1:]
    if len(identity) != 1 or not identity[0].strip():
        raise ValueError('the selected Python has no supported shared libpython image')
    files = {}
    for parent, dirs, names in os.walk(prefix, followlinks=True):
        # Exclude installed third-party packages: the backend's admitted wheel
        # owner supplies its own packages, independently of the build account.
        dirs[:] = sorted(name for name in dirs if name not in ('site-packages', '__pycache__'))
        for name in dirs:
            folder = Path(parent) / name
            resolved = folder.resolve(strict=True)
            if not resolved.is_relative_to(prefix) or resolved in {path.resolve() for path in folder.parents}:
                raise ValueError('Python home has an external or cyclic directory link')
        for name in sorted(names):
            source = Path(parent) / name
            resolved = source.resolve(strict=True)
            if not resolved.is_relative_to(prefix) or not resolved.is_file():
                raise ValueError('Python home has an external link or non-file member')
            files[source.relative_to(prefix).as_posix()] = resolved
            if len(files) > 65536:
                raise ValueError('Python home exceeds its file bound')
    total = sum(path.stat().st_size for path in files.values())
    if total > 2 << 30 or shutil.disk_usage(output.parent).free < total * 2 + (2 << 30):
        raise ValueError('Python host staging exceeds its space bound')
    source_target = {path: 'runtime/' + name for name, path in files.items()}
    external, order, active, done, edges = {}, [], set(), set(), {}
    def visit(image):
        image = image.resolve(strict=True)
        if image in done:
            return
        if image in active:
            raise ValueError('cyclic non-system Python dependency')
        active.add(image)
        dependencies, rpaths = _macos_dependencies(image)
        edges[image] = []
        for dependency in dependencies:
            if dependency.startswith(('/usr/lib/', '/System/Library/')):
                continue
            def expand(value):
                return value.replace('@loader_path', str(image.parent)).replace('@executable_path', str(python.parent))
            if dependency.startswith('@rpath/'):
                candidates = {Path(expand(base)) / dependency[7:] for base in rpaths}
                matches = {path.resolve() for path in candidates if path.is_file()}
                if len(matches) != 1:
                    raise ValueError('Python dependency has no unique runtime search path')
                selected = matches.pop()
            else:
                selected = Path(expand(dependency))
                if not selected.is_absolute() or not selected.is_file():
                    raise ValueError('Python dependency is unavailable: ' + dependency)
                selected = selected.resolve(strict=True)
            if selected not in source_target:
                if selected not in external:
                    relative = 'dependencies/' + selected.name
                    for other, existing in external.items():
                        if existing == relative and _sha256(other) != _sha256(selected):
                            raise ValueError('Python dependency basenames collide')
                    external[selected] = relative
                    source_target[selected] = relative
                if len(external) > 256:
                    raise ValueError('Python external dependency bound exceeded')
            visit(selected)
            edges[image].append((dependency, selected))
            if selected != library and source_target[selected] not in order:
                order.append(source_target[selected])
        active.remove(image)
        done.add(image)
    # Extension modules may depend on non-system libraries even when libpython
    # itself has system-only dependencies (notably framework Python's SSL).
    for source in sorted(set(files.values())):
        with source.open('rb') as stream:
            magic = stream.read(4)
        if magic in (b'\xcf\xfa\xed\xfe', b'\xce\xfa\xed\xfe', b'\xfe\xed\xfa\xcf', b'\xfe\xed\xfa\xce', b'\xca\xfe\xba\xbe', b'\xbe\xba\xfe\xca'):
            visit(source)
    total += sum(path.stat().st_size for path in external)
    if len(files) + len(external) + 1 > 65536 or total > 2 << 30 or shutil.disk_usage(output.parent).free < total * 2 + (2 << 30):
        raise ValueError('Python complete dependency closure exceeds its staging bound')
    output.mkdir(mode=0o700)
    try:
        for name, source in files.items():
            target = output / 'runtime' / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, target)
            target.chmod(source.stat().st_mode & 0o777)
        for source, name in external.items():
            target = output / name
            target.parent.mkdir(exist_ok=True)
            shutil.copyfile(source, target)
        provenance = {}
        targets = [(source, 'runtime/' + name) for name, source in files.items()] + list(external.items())
        for source, name in targets:
            if source not in edges:
                continue
            target = output / name
            provenance[name] = dict(bytes=source.stat().st_size, sha256=_sha256(source)[1])
            for old, selected in edges[source]:
                relative = os.path.relpath(output/source_target[selected], target.parent)
                subprocess.run(['/usr/bin/install_name_tool', '-change', old, '@loader_path/' + relative, str(target)], check=True, capture_output=True, timeout=30)
            # A staged framework/PBS image has a new identity and changed bytes.
            # Remove the old seal; the ordinary code-signing owner seals it late.
            details = subprocess.run(['/usr/bin/codesign', '-dv', str(target)], capture_output=True)
            if details.returncode == 0:
                subprocess.run(['/usr/bin/codesign', '--remove-signature', str(target)], check=True, capture_output=True, timeout=30)
            remaining, _ = _macos_dependencies(target)
            if any(not value.startswith(('/usr/lib/', '/System/Library/', '@loader_path/')) for value in remaining):
                raise ValueError('a staged Python image retains a nonlocal dependency')
        header = output / 'python_dependencies.h'
        (output/'dependencies').mkdir(exist_ok=True)
        header.write_text('#define OA_PYTHON_LIBRARY ' + json.dumps(source_target[library]) + '\nstatic const char *oa_dependencies[] = {' + ','.join(json.dumps(name) for name in order) + '};\n', encoding='utf-8')
        host_source = LAUNCHER_SOURCE.parent / 'installer/python_host.c'
        subprocess.run([str(compiler), '-Wall', '-Wextra', '-Werror', '-I', str(include), '-I', str(output), str(host_source), '-o', str(output / 'python-host')], check=True, timeout=60)
        header.unlink()
        if sign_code is not None:
            for name in [*sorted(provenance), 'python-host']:
                sign_code(output/name, 'org.openabstractions.comfyui.python.' + hashlib.sha256(name.encode()).hexdigest()[:16])
        inventory = {path.relative_to(output).as_posix(): dict(bytes=path.stat().st_size, sha256=_sha256(path)[1]) for path in sorted(output.rglob('*')) if path.is_file()}
        manifest = dict(id='macos-python-host', python_version=info['version'], runtime_files=inventory, relocated_inputs=provenance)
        (output / 'manifest.json').write_text(json.dumps(manifest, sort_keys=True, separators=(',', ':')) + '\n', encoding='utf-8')
        return manifest
    except BaseException:
        shutil.rmtree(output)
        raise


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
    path = _extended(path)
    digest = hashlib.sha256()
    size = 0
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1 << 20), b""):
            size += len(block)
            digest.update(block)
    return size, digest.hexdigest()


def _runtime_file_hashes(files: list[tuple[str, Path]]) -> dict:
    """Hash the admitted inventory with at most four outstanding file reads."""
    def row(item):
        name, path = item
        return name.removeprefix('backend/'), dict(bytes=path.stat().st_size,
                                                 sha256=_sha256(path)[1])

    items = iter(files)
    result = {}
    with ThreadPoolExecutor(max_workers=4) as pool:
        pending = set()
        try:
            for item in items:
                pending.add(pool.submit(row, item))
                if len(pending) == 4:
                    break
            while pending:
                done, pending = wait(pending, return_when=FIRST_COMPLETED)
                # Observe every completed failure before submitting more work.
                completed = [future.result() for future in done]
                result.update(completed)
                for _ in done:
                    item = next(items, None)
                    if item is not None:
                        pending.add(pool.submit(row, item))
        except BaseException:
            for future in pending:
                future.cancel()
            raise
    return dict(sorted(result.items()))


def _local_runtime_json(raw):
    def closed(pairs):
        result={}
        for key,value in pairs:
            if key in result:raise ValueError('duplicate local runtime metadata key')
            result[key]=value
        return result
    return json.loads(raw,object_pairs_hook=closed)


def _local_runtime_rows(value):
    rows=value.get('runtime_files')
    if not isinstance(rows,dict) or not 0<len(rows)<=BACKEND_RUNTIME_ENTRIES:
        raise ValueError('local runtime roster exceeds bound')
    seen=set()
    for name,row in rows.items():
        if (not isinstance(name,str) or not name or '\\' in name or name.casefold() in seen):
            raise ValueError('invalid local runtime member')
        for part in name.split('/'): _safe_name(part)
        seen.add(name.casefold())
        if (not isinstance(row,dict) or set(row)!={'bytes','sha256'} or type(row['bytes']) is not int
                or not 0<=row['bytes']<=16<<30 or not isinstance(row['sha256'],str)
                or not re.fullmatch('[0-9a-f]{64}',row['sha256'])):
            raise ValueError('invalid local runtime pin')
    return rows


def receive_local_backend(directory: Path, producer_receipt: Path, output: Path, archive_tool: Path, *, verify_existing=False) -> dict:
    """Receive historical private backend DATA through the existing 7z owner."""
    directory, producer_receipt, output = map(Path,(directory,producer_receipt,output))
    value = _local_runtime_json(producer_receipt.read_bytes())
    if (value.get('schema') != 3 or value.get('source_mode') != 'private-diagnostic'
            or set(value) != {'schema','source_mode','private_provenance','input_descriptor','archive','source_pair'}):
        raise ValueError('local backend requires its original private producer receipt')
    _private_integration_context(value['private_provenance'])
    if (type(verify_existing) is not bool or output.name != 'backend' or output.is_symlink() or output.is_junction()
            or not output.is_absolute() or not output.parent.is_dir()
            or any(path.is_symlink() or path.is_junction() for path in output.parents)
            or (not output.is_dir() if verify_existing else output.exists())):
        raise ValueError('local backend requires a fresh ordinary backend staging root')
    if verify_existing:
        manifest_path = _backend_file(output, 'manifest.json')
        if not 0 < manifest_path.stat().st_size <= BACKEND_MANIFEST_BYTES:
            raise ValueError('existing local backend manifest exceeds metadata bound')
        retained_rows = _local_runtime_rows(_local_runtime_json(manifest_path.read_bytes()))
        expected_python, interrupted_python = output/'standalone-env', output/'python_embeded'
        # Refusal-only layout hint, before the expensive archive/tree pass.
        # This local metadata grants no verification: PASS still requires the
        # authenticated archive manifest and complete byte comparison below.
        if (any(name.startswith('standalone-env/') for name in retained_rows)
                and not any(name.startswith('python_embeded/') for name in retained_rows)
                and not expected_python.exists() and not expected_python.is_symlink() and not expected_python.is_junction()
                and interrupted_python.is_dir() and not interrupted_python.is_symlink() and not interrupted_python.is_junction()):
            raise ValueError('local backend interrupted refresh layout: manifest lists standalone-env/ but '
                             'standalone-env is absent and python_embeded is present; preserve the stage for '
                             'supported refresh recovery; full archive/tree verification not performed')
    members, raw = backend_archive_metadata(directory,value['archive'],archive_tool)
    manifest = _local_runtime_json(raw)
    rows = _local_runtime_rows(manifest)
    expanded = sum(row['bytes'] for row in rows.values())
    if not verify_existing and shutil.disk_usage(output.parent).free < MIN_FREE_AFTER_BUILD_BYTES + expanded + len(raw):
        raise ValueError('insufficient local backend extraction space')
    if not verify_existing:
        output.mkdir()
        with owned_temporary_tree(prefix='oa-local-backend-',parent=output.parent) as work:
            subprocess.run([str(archive_tool),'x','-y','-sccUTF-8','-bso0','-bsp0','-bse2',
                '-o'+str(_plain(output)),str(directory/value['archive']['parts'][0]['name'])],
                cwd=_plain(work),env=_windows_archive_environment(work),check=True,timeout=600)
    output=_extended(output)
    files, dirs, _, _ = _scan_tree(output.parent,output.name,backend_limits()['expanded_bytes'],selected_root=True)
    observed = {name.removeprefix('backend/'):dict(zip(('bytes','sha256'),_sha256(path)))
                for name,path in files if path != output/'manifest.json'}
    names = {name.removeprefix('backend/') for name,_ in files}
    names.update(name.removeprefix('backend/') for name in dirs if name != 'backend')
    if names != members or observed != rows or (output/'manifest.json').read_bytes() != raw:
        # Reuse the already measured comparison. Report bounded relative names,
        # never file contents, so an interrupted refresh is distinguishable.
        sample = lambda values: [name if len(name) <= 160 else name[:157]+'...'
                                 for name in sorted(values)[:8]]
        differences = {}
        for category, expected, actual in (('archive_members', members, names),
                                           ('file_roster', set(rows), set(observed))):
            missing, extra = expected-actual, actual-expected
            if missing or extra:
                differences[category] = dict(missing_count=len(missing), missing=sample(missing),
                                             extra_count=len(extra), extra=sample(extra))
        changed = {name for name in rows.keys() & observed.keys() if rows[name] != observed[name]}
        if changed:
            differences['file_pins'] = dict(changed_count=len(changed), changed=sample(changed))
        if (output/'manifest.json').read_bytes() != raw:
            differences['manifest_bytes'] = 'changed'
        raise ValueError('local backend extraction differs from verified archive and complete roster: '
                         + json.dumps(differences, sort_keys=True, separators=(',', ':')))
    app_pair_receipt.verify_archive_set(directory,value['archive'],format='7z')
    return dict(source_mode='private-local-input',retained_provenance=value['private_provenance'],
        extraction='verified-existing' if verify_existing else 'fresh',
        producer_receipt=dict(zip(('bytes','sha256'),_sha256(producer_receipt))),
        manifest=dict(bytes=len(raw),sha256=hashlib.sha256(raw).hexdigest()))


def _local_snapshot_inventory(directory):
    files, _, _, _ = _scan_tree(directory.parent, directory.name, 16 << 30, selected_root=True)
    result = {}
    for name, path in files:
        info = path.lstat()
        if info.st_nlink != 1:
            raise ValueError('local snapshot refuses hard-linked source DATA')
        result[name.removeprefix(directory.name+'/')] = (info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_nlink)
    return result


def _local_native_snapshot_inputs(backend: Path, control: Path, work: Path, sealed_receipt: Path, *,
                                  receipt_bytes: int, receipt_sha256: str, controller_source: str,
                                  reserve_bytes: int = 0) -> dict:
    """Receive sealed historical runtime DATA into a fresh owned stage.

    The pinned successful producer and its authenticated log select both complete
    manifests. Vendor members are DATA verified while copied, never code pins.
    This operation runs no runtime, archive tool, installer or dependency builder.
    """
    _require_private_source_revision(Path(__file__).resolve().parents[1], controller_source)
    if type(reserve_bytes) is not int or not 0 <= reserve_bytes <= 1 << 40:
        raise ValueError('invalid local snapshot space reserve')
    def metadata(path, size, digest, bound):
        path = _extended(ordinary_path(path, kind='file'))
        if (type(size) is not int or not 0 < size <= bound or path.stat().st_nlink != 1
                or not isinstance(digest, str) or not re.fullmatch('[0-9a-f]{64}', digest)):
            raise ValueError('invalid sealed snapshot metadata pin')
        raw = path.read_bytes() if path.stat().st_size <= bound else b''
        if (len(raw), hashlib.sha256(raw).hexdigest()) != (size, digest):
            raise ValueError('sealed snapshot metadata differs from selected pin')
        return raw
    raw = metadata(sealed_receipt, receipt_bytes, receipt_sha256, 4 << 20)
    record = _local_runtime_json(raw.splitlines()[-1])
    if (record.get('operation') != 'private_bounded_source_check' or record.get('state') != 'COMPLETE'
            or record.get('accepted') is not True or record.get('exit_code') != 0
            or record.get('inputs_unchanged') is not True
            or record.get('join', {}).get('quiescent') is not True
            or record.get('owned_handles', {}).get('handles_closed') is not True):
        raise ValueError('local snapshot requires successful closed sealed receiving')
    argv = record.get('argv', [])
    if len(argv) < 4 or argv[2:4] != ['scripts/package_comfyui_app.py', 'refresh-local-native']:
        raise ValueError('local snapshot requires the native refresh producer')
    proof_raw = metadata(Path(record['log']), record['log_bytes'], record['log_sha256'], 8 << 20)
    proof = _local_runtime_json(proof_raw.splitlines()[-1])
    if proof.get('source_mode') != 'private-local-preview':
        raise ValueError('local snapshot producer did not seal both runtimes')
    context = _local_private_context(proof.get('private_provenance', {}))
    flags = dict(zip(argv[4::2], argv[5::2]))
    if any(flags.get('--'+key.replace('_', '-')) != value for key, value in context.items()):
        raise ValueError('local snapshot provenance differs from producer command')
    work = _extended(Path(work))
    if (not work.is_absolute() or '..' in work.parts or work.exists()
            or work.is_symlink() or work.is_junction()):
        raise ValueError('local snapshot requires a fresh owned work directory')
    ordinary_path(work.parent, kind='directory')
    selected, total = {}, 0
    for role, source in (('backend', backend), ('control', control)):
        source = _extended(ordinary_path(source, kind='directory'))
        if work.resolve().is_relative_to(source.resolve()) or source.resolve().is_relative_to(work.resolve()):
            raise ValueError('local snapshot source and work overlap')
        pin = proof.get('resources', {}).get(role, {})
        manifest_raw = metadata(source/'manifest.json', pin.get('bytes'), pin.get('sha256'), BACKEND_MANIFEST_BYTES)
        rows = dict(_local_runtime_rows(_local_runtime_json(manifest_raw)))
        if 'manifest.json' in rows:
            raise ValueError('local snapshot manifest cannot roster itself')
        rows['manifest.json'] = dict(bytes=len(manifest_raw), sha256=hashlib.sha256(manifest_raw).hexdigest())
        before = _local_snapshot_inventory(source)
        if set(before) != set(rows) or any(before[name][2] != row['bytes'] for name, row in rows.items()):
            raise ValueError('local snapshot source roster or file size differs from sealed manifest')
        selected[role] = source, rows, before, pin
        total += sum(row['bytes'] for row in rows.values())
    if shutil.disk_usage(_plain(work.parent)).free < MIN_FREE_AFTER_BUILD_BYTES + reserve_bytes + total + (1 << 30):
        raise ValueError('insufficient local snapshot space after reserve and retained free-space floor')
    return work, selected, total, context, raw


def snapshot_local_native_runtime(backend: Path, control: Path, work: Path, sealed_receipt: Path, *,
                                  receipt_bytes: int, receipt_sha256: str, controller_source: str,
                                  reserve_bytes: int = 0) -> dict:
    """Receive the complete admitted sealed DATA through the existing copy owner."""
    work, selected, total, context, raw = _local_native_snapshot_inputs(backend, control, work, sealed_receipt,
        receipt_bytes=receipt_bytes, receipt_sha256=receipt_sha256, controller_source=controller_source,
        reserve_bytes=reserve_bytes)
    work.mkdir()
    for role, (source, rows, before, _) in selected.items():
        target = work/('extract/backend' if role == 'backend' else 'extract/control/oa-control')
        for name, pin in rows.items():
            ordinary_path(source.joinpath(*name.split('/')), kind='file')
            output = target.joinpath(*name.split('/'))
            output.parent.mkdir(parents=True, exist_ok=True)
            _snapshot_verified_source(source.joinpath(*name.split('/')), output, pin['bytes'], pin['sha256'])
        if _local_snapshot_inventory(source) != before:
            raise ValueError('local snapshot source changed while receiving DATA')
        actual = _local_snapshot_inventory(target)
        if set(actual) != set(rows) or any(actual[name][2] != pin['bytes'] for name, pin in rows.items()):
            raise ValueError('local snapshot destination roster differs')
    return dict(source_mode='private-local-input', controller_source=controller_source,
                retained_provenance=context, producer_receipt=dict(bytes=len(raw), sha256=receipt_sha256),
                resources={role:value[3] for role,value in selected.items()},
                files=sum(len(value[1]) for value in selected.values()), bytes=total)


def refresh_local_native_runtime(backend: Path, control: Path, integration: Path, work: Path, *,
                                 integration_bytes: int, integration_sha256: str,
                                 private_context: dict, recovery: dict | None = None) -> dict:
    """Refresh OA code in verified historical runtime copies, retaining vendors.

    No dependency installation, Comfy/Torch import, model or backend startup
    occurs. The build interpreter installs only the selected OA wheels into a
    fresh target, as the existing integration producer does.
    """
    backend, control, work = map(_extended, (backend, control, work))
    for directory in (backend, control):
        if not directory.resolve().is_relative_to((work/'extract').resolve()):
            raise ValueError('local runtime refresh requires an owned extraction stage')
    _require_owned_stage(backend, work)
    context = _local_private_context(private_context)
    if recovery is not None:
        _require_local_refresh_recovery(recovery, context, backend, control, integration, work,
                                        integration_bytes, integration_sha256)
        # Recreate only the small authenticated OA overlay. Vendor DATA stays in place.
        for name in ('integration', 'current-oa-site', 'private-integration-wheels'):
            path = work/name
            if path.exists(): remove_tree(_plain(path))
        (work/'selected-integration.zip').unlink(missing_ok=True)
    snapshot, options, integration_manifest, _ = _freeze_backend_integration(work, integration, {},
        integration_bytes, integration_sha256, source=None, run_id=None, run_attempt=None, private_context=context,
        recovery_controller=recovery['controller_source'] if recovery is not None else None)
    retained = {}
    for role, directory in (('backend', backend), ('control', control)):
        files, _, _, _ = _scan_tree(directory.parent, directory.name, 16 << 30, selected_root=True)
        manifest_raw = (recovery[role] if recovery is not None else (directory/'manifest.json').read_bytes())
        if not 0 < len(manifest_raw) <= 16 << 20:
            raise ValueError('retained local manifest exceeds bound')
        manifest = _local_runtime_json(manifest_raw)
        _local_runtime_rows(manifest)
        actual = {name.removeprefix(directory.name+'/'):dict(zip(('bytes','sha256'),_sha256(path)))
                  for name,path in files if path != directory/'manifest.json'}
        if recovery is None and actual != manifest.get('runtime_files'):
            raise ValueError('retained local runtime differs from historical byte roster')
        if any(path.stat().st_nlink != 1 for _,path in files):
            raise ValueError('retained local runtime contains hard links')
        if recovery is not None:
            _verify_local_refresh_vendor(role, manifest, actual)
        retained[role] = dict(manifest=manifest, files=manifest['runtime_files'], current_files=actual,
            pin=dict(bytes=len(manifest_raw),sha256=hashlib.sha256(manifest_raw).hexdigest()))
    for python_root in (backend/'standalone-env',control):
        if _launcher().installer_presence(str(python_root/'Lib/site-packages'),str(python_root)):
            raise ValueError('local runtime refresh requires an already sealed interpreter without installers')
    node = backend/'ComfyUI/custom_nodes/abstraction_downloads'
    if not node.is_dir(): raise ValueError('retained backend lacks its OA integration')
    if recovery is not None:
        _verify_local_refresh_wrapper(backend, integration_manifest)
    remove_tree(_plain(node))
    integration_result = _apply_integration(backend, snapshot, work, install_wheels=False,
        archive_size=integration_bytes, archive_sha256=integration_sha256, require_wrapper=True,
        **options)
    target = work/'current-oa-site'
    subprocess.run([sys.executable, '-B', '-m', 'pip', 'install', '--no-index', '--no-compile', '--no-deps',
        '--find-links', str(_plain(work/'integration/wheels')), '--require-hashes', '--target', str(_plain(target)),
        '-r', str(_plain(work/'integration/requirements.lock'))], check=True, cwd=_plain(work),
        env=_private_python_env(work), timeout=300)
    wheel_pins = {work/'integration/wheels'/row['filename']:row['sha256'] for row in integration_manifest['wheels']}
    _fill_wheel_records(target, wheel_pins)
    retired = {}
    for role, site, prefix in (
            ('backend', backend/'standalone-env/Lib/site-packages', 'standalone-env/Lib/site-packages/'),
            ('control', control/'Lib/site-packages', 'Lib/site-packages/')):
        old = {name.removeprefix(prefix):value for name,value in retained[role]['current_files'].items() if name.startswith(prefix)}
        retired[role] = replace_local_oa_site(site, target, old, require_current=recovery is not None)
    manifest = retained['backend']['manifest']
    launcher_names = set(_launcher_inputs())
    launcher_names.update((manifest['oa_launcher']['name'].removeprefix('ComfyUI/'),
                           manifest['oa_launcher']['interpreter']['name'].removeprefix('ComfyUI/')))
    for name in launcher_names:
        for part in name.split('/'): _safe_name(part)
        path = backend/'ComfyUI'/name
        if path.exists(): path.unlink()
        parent = path.parent
        while parent != backend/'ComfyUI' and parent.exists() and not any(parent.iterdir()):
            parent.rmdir()
            parent = parent.parent
    launcher = _stage_launcher(backend/'ComfyUI')
    (backend/HELPER_CONFIG_NAME).unlink(missing_ok=True)
    helper = _stage_backend_helper(backend)
    (backend/'standalone-env').rename(backend/'python_embeded')
    seal = _seal_private_python(backend, work, embedded=False, backend_bootstrap=False)
    (backend/'python_embeded').rename(backend/'standalone-env')
    manifest.update(private_python_seal=seal, helper_python_config=helper,
        oa_integration=dict(name=integration.name, bytes=integration_bytes, sha256=integration_sha256),
        oa_launcher=dict(name='ComfyUI/'+LAUNCHER_NAME,
            bytes=len(launcher[LAUNCHER_NAME]),sha256=hashlib.sha256(launcher[LAUNCHER_NAME]).hexdigest(),
            interpreter=dict(name='ComfyUI/oa_interpreter.py',bytes=len(launcher['oa_interpreter.py']),
                sha256=hashlib.sha256(launcher['oa_interpreter.py']).hexdigest())))
    (backend/'OA-LICENSES/OpenAbstractions-LICENSE').write_bytes(integration_result['license'])
    (backend/'OA-LICENSES/OpenAbstractions-NOTICE').write_bytes(integration_result['notice'])
    (control/'oa_native_host.py').write_bytes((Path(__file__).resolve().parents[1]/'adopters/comfyui/node/oa_native_host.py').read_bytes())
    control_portable = work/'extract/control-portable'
    control_portable.mkdir(exist_ok=recovery is not None)
    (control/'manifest.json').unlink(missing_ok=recovery is not None)
    control.rename(control_portable/'python_embeded')
    _seal_private_python(control_portable, work, embedded=False, backend_bootstrap=False)
    python_pin=retained['control']['files']['python.exe']
    with _selected_python_executable(control_portable/'python_embeded/python.exe',
            (python_pin['bytes'],python_pin['sha256']),_sha256) as python:
        proof = _native_control_appmodel_check(python, control_portable/'python_embeded', work)
    component = [row for row in integration_manifest['wheels'] if row['filename'].startswith('abstraction_facade_native-')]
    if len(component) != 1 or proof['library_sha256'] != component[0]['native_library_sha256']:
        raise ValueError('local control loaded another AppModel component')
    (control_portable/'python_embeded').rename(control)
    changed, pending = {}, []
    for role, directory in (('backend', backend), ('control', control)):
        _normalize_runtime_directories(directory.parent, directory.name, 16 << 30, selected_root=True)
        files, _, _, _ = _scan_tree(directory.parent, directory.name, 16 << 30, selected_root=True)
        rows = {name.removeprefix(directory.name+'/'):dict(zip(('bytes','sha256'),_sha256(path)))
                for name,path in files if path != directory/'manifest.json'}
        allowed = set(retired[role])
        prefix = 'standalone-env/Lib/site-packages/' if role == 'backend' else 'Lib/site-packages/'
        for name, old in retained[role]['files'].items():
            oa = (name.startswith(prefix) and name.removeprefix(prefix) in allowed)
            if role == 'backend':
                oa |= (name.startswith('ComfyUI/custom_nodes/abstraction_downloads/')
                       or name in {'ComfyUI/'+item for item in launcher_names}
                       or name in {'OA-LICENSES/OpenAbstractions-LICENSE','OA-LICENSES/OpenAbstractions-NOTICE'})
            else: oa |= name == 'oa_native_host.py'
            if not oa and rows.get(name) != old:
                raise ValueError('local refresh changed retained third-party bytes: '+name)
        value = manifest if role == 'backend' else {}
        value['runtime_files'] = rows
        raw = _backend_manifest_bytes(value) if role == 'backend' else (json.dumps(value,sort_keys=True)+'\n').encode()
        pending.append((role, directory/'manifest.json', raw))
        changed[role] = dict(bytes=len(raw), sha256=hashlib.sha256(raw).hexdigest())
    # Complete both expensive rosters before publishing either manifest. Each
    # replacement is atomic; a stopped pair remains recoverable from originals.
    with owned_temporary_tree(prefix='oa-local-manifests-',parent=_plain(work)) as publication:
        for role, path, raw in pending:
            temporary = publication/(role+'-manifest.json')
            with temporary.open('xb') as stream: stream.write(raw)
            temporary.replace(path)
    return dict(source_mode='private-local-preview', private_provenance=context,
        retained={role:value['pin'] for role,value in retained.items()}, resources=changed, appmodel=proof,
        **({'recovery_controller_source':recovery['controller_source']} if recovery is not None else {}))


def _local_refresh_mutable(role, name, manifest, launcher_names, distributions):
    prefix = 'standalone-env/Lib/site-packages/' if role == 'backend' else 'Lib/site-packages/'
    if name.startswith(prefix):
        root = name.removeprefix(prefix).split('/')[0]
        if root in {'abstraction', 'appmodel', 'abstraction_facade_native'}:
            return True
        if root.endswith('.dist-info') and root.split('-')[0] in distributions:
            return True
    if role == 'control': return name == 'oa_native_host.py'
    launcher = manifest['oa_launcher']
    return (name.startswith('ComfyUI/custom_nodes/abstraction_downloads/')
            or name in launcher_names
            or name in {launcher['name'], launcher['interpreter']['name'], HELPER_CONFIG_NAME,
                        'OA-LICENSES/OpenAbstractions-LICENSE','OA-LICENSES/OpenAbstractions-NOTICE'})


def _verify_local_refresh_wrapper(backend, manifest):
    node = backend/'ComfyUI/custom_nodes/abstraction_downloads'
    expected = dict(manifest['wrapper_files'])
    expected['helper/pyvenv.cfg'] = dict(bytes=len(HELPER_CONFIG_TEMPLATE),
        sha256=hashlib.sha256(HELPER_CONFIG_TEMPLATE).hexdigest())
    home = backend/'standalone-env'
    for path in home.iterdir():
        if path.name == 'python.exe' or re.fullmatch(r'(?:python3\d*|vcruntime140(?:_\d+)?)\.dll',path.name,re.I):
            expected['helper/'+path.name] = dict(zip(('bytes','sha256'),_sha256(path)))
    spec = importlib.util.spec_from_file_location('oa_recovery_connect',node/'oa_connect.py')
    # Check all authenticated wrapper code before importing its descriptor owner.
    for name,pin in manifest['wrapper_files'].items():
        if _sha256(node.joinpath(*name.split('/'))) != (pin['bytes'],pin['sha256']):
            raise ValueError('local recovery wrapper differs from frozen integration')
    descriptor = node/'application-manifest.json'
    if not 0 < descriptor.stat().st_size <= 65536:
        raise ValueError('local recovery application descriptor exceeds bound')
    expected['application-manifest.json'] = dict(zip(('bytes','sha256'),_sha256(descriptor)))
    # Reject extra loader inputs, including cached bytecode, before importing
    # the authenticated source that owns the generated data descriptor.
    _verify_integration_wrapper(node, expected)
    connect = importlib.util.module_from_spec(spec); spec.loader.exec_module(connect)
    raw = (json.dumps(connect.manifest('{app}/helper/python.exe'),sort_keys=True)+'\n').encode()
    expected['application-manifest.json'] = dict(bytes=len(raw),sha256=hashlib.sha256(raw).hexdigest())
    _verify_integration_wrapper(node, expected)


def _verify_local_refresh_vendor(role, manifest, actual):
    original = _local_runtime_rows(manifest)
    launcher_names = {'ComfyUI/'+item for item in _launcher_inputs()} if role == 'backend' else set()
    distributions = _integration_distributions(appmodel=True)
    for name in set(original) | set(actual):
        if not _local_refresh_mutable(role, name, manifest, launcher_names, distributions) and original.get(name) != actual.get(name):
            raise ValueError('local recovery changed retained third-party bytes: '+name)


def _require_local_refresh_recovery(recovery, context, backend, control, integration, work, size, digest):
    """Admit a stopped native source-check and its original producer-owned DATA."""
    root = Path(__file__).resolve().parents[1]
    controller = recovery['controller_source']
    _require_private_source_revision(root, controller)
    owner = _integration_packager_owner()
    # The controller-only repair can change these two private implementation
    # files. Every other original product/source byte remains selected.
    changed = subprocess.check_output(['git','diff','--name-only',context['source_commit'],'--'],
                                      cwd=root,env=source_git_environment(),text=True).splitlines()
    if set(changed) - {'scripts/package_comfyui_app.py','scripts/test_package_comfyui_app.py'}:
        raise ValueError('local recovery changed original product source')
    record = recovery['receipt']
    expected = ['--backend',str(_plain(backend)),'--control',str(_plain(control)),'--work',str(_plain(work)),
                '--integration',str(_plain(integration)),'--integration-bytes',str(size),'--integration-sha256',digest,
                '--source-commit',context['source_commit'],'--native-source-commit',context['native_source_commit'],
                '--native-library-sha256',context['native_library_sha256'],
                '--appmodel-library-sha256',context['appmodel_library_sha256'],
                '--appmodel-wheel-sha256',context['appmodel_wheel_sha256']]
    argv = record.get('argv', [])
    if (record.get('operation') != 'private_bounded_source_check' or record.get('accepted') is not False
            or record.get('state') != 'FAILED' or record.get('inputs_unchanged') is not True
            or record.get('join',{}).get('quiescent') is not True
            or record.get('owned_handles',{}).get('handles_closed') is not True
            or len(argv) < 4 or argv[2:4] != ['scripts/package_comfyui_app.py','refresh-local-native']
            or argv[4:] != expected or Path(record.get('cwd','')).resolve() != root.resolve()):
        raise ValueError('local recovery requires its closed stopped source-check receipt')
    pins = {str(Path(row['path']).resolve()):(row['bytes'],row['sha256']) for row in record['inputs']}
    for name in ('scripts/package_comfyui_app.py', 'scripts/package_comfyui_integration.py'):
        raw = subprocess.check_output(['git','show',context['source_commit']+':'+name], cwd=root,
                                      env=source_git_environment())
        if pins.get(str((root/name).resolve())) != (len(raw),hashlib.sha256(raw).hexdigest()):
            raise ValueError('local recovery receipt differs from original product source')
    if pins.get(str(Path(integration).resolve())) != (size,digest) or _sha256(integration) != (size,digest):
        raise ValueError('local recovery integration differs from frozen archive')
    for role in ('backend','control'):
        _local_runtime_rows(_local_runtime_json(recovery[role]))
    if recovery['backend_pin'] != dict(bytes=len(recovery['backend']),sha256=hashlib.sha256(recovery['backend']).hexdigest()):
        raise ValueError('local recovery backend manifest differs from received archive')
    if pins.get(str(Path(recovery['control_path']).resolve())) != (len(recovery['control']),hashlib.sha256(recovery['control']).hexdigest()):
        raise ValueError('local recovery control manifest differs from frozen source')


def _local_refresh_recovery(refresh_receipt, receive_receipt, controller_source):
    _require_private_source_revision(Path(__file__).resolve().parents[1], controller_source)
    def terminal(path):
        raw = Path(path).read_bytes()
        if not 0 < len(raw) <= 4 << 20: raise ValueError('local recovery receipt exceeds bound')
        return json.loads(raw.splitlines()[-1])
    refresh, received = terminal(refresh_receipt), terminal(receive_receipt)
    if (received.get('operation') != 'private_bounded_source_check' or received.get('accepted') is not True
            or received.get('exit_code') != 0 or received.get('inputs_unchanged') is not True
            or received.get('join',{}).get('quiescent') is not True
            or received.get('owned_handles',{}).get('handles_closed') is not True):
        raise ValueError('local recovery requires successful closed backend receiving')
    argv = received.get('argv',[])
    if len(argv) != 12 or argv[2:4] != ['scripts/package_comfyui_app.py','receive-local-backend']:
        raise ValueError('local recovery backend receiving command differs')
    if argv[4::2] != ['--directory','--producer-receipt','--output','--archive-tool']:
        raise ValueError('local recovery backend receiving arguments differ')
    selected = dict(zip(argv[4::2],argv[5::2]))
    if len(refresh.get('argv',[])) < 6 or selected['--output'] != refresh['argv'][5]:
        raise ValueError('local recovery backend stage differs from receiving')
    pins = {str(Path(row['path']).resolve()):(row['bytes'],row['sha256']) for row in received['inputs']}
    producer = Path(selected['--producer-receipt'])
    tool = Path(selected['--archive-tool'])
    for path in (producer,*_archive_tool_inputs(tool).values()):
        if pins.get(str(path.resolve())) != _sha256(path):
            raise ValueError('local recovery original backend inputs changed')
    log = Path(received['log'])
    if _sha256(log) != (received['log_bytes'],received['log_sha256']):
        raise ValueError('local recovery receiving log changed')
    proof = json.loads(log.read_bytes().splitlines()[-1])
    value = _local_runtime_json(producer.read_bytes())
    if proof.get('producer_receipt') != dict(zip(('bytes','sha256'),_sha256(producer))):
        raise ValueError('local recovery backend producer differs from receiving')
    _, backend = backend_archive_metadata(Path(selected['--directory']),value['archive'],tool)
    controls = [row for row in refresh['inputs'] if Path(row['path']).name == 'manifest.json']
    if len(controls) != 1: raise ValueError('local recovery original control manifest is ambiguous')
    control_path = Path(controls[0]['path'])
    return dict(controller_source=controller_source,receipt=refresh,backend=backend,
                backend_pin=proof['manifest'],control=control_path.read_bytes(),control_path=control_path)


def replace_local_oa_site(site: Path, replacement: Path, retained_files: dict, *, require_current=False) -> list[str]:
    """Replace only historically rostered OA RECORD members in an owned copy.

    Both directories are caller-owned staging DATA. Third-party package files
    remain unchanged. All membership checks finish before the first deletion.
    """
    site, replacement = Path(site), Path(replacement)
    allowed = _integration_distributions(appmodel=True)
    package_roots = {'abstraction', 'appmodel', 'abstraction_facade_native'}
    def roster(directory, *, historical=False):
        files, _, _, _ = _scan_tree(directory.parent, directory.name, 16 << 30, selected_root=True)
        result, distributions = {}, set()
        records = sorted(directory.glob('*.dist-info/RECORD'))
        for record in records:
            metadata = record.parent/'METADATA'
            parsed = Parser().parsestr(metadata.read_text(encoding='utf-8'))
            distribution = re.sub('[-.]+', '_', parsed['Name'] or '').lower()
            if distribution not in allowed:
                continue
            if distribution in distributions:
                raise ValueError('duplicate local OA distribution')
            distributions.add(distribution)
            for row in csv.reader(io.StringIO(record.read_text(encoding='utf-8'), newline='')):
                if len(row) != 3:
                    raise ValueError('invalid local OA RECORD')
                name = row[0]
                parts = name.split('/')
                if (not name or any(part in ('', '.', '..') for part in parts)
                        or '\\' in name or ':' in name
                        or parts[0] not in package_roots | {record.parent.name}):
                    raise ValueError('local OA RECORD escapes its removal scope')
                if name.casefold() in result:
                    raise ValueError('shared or duplicate local OA RECORD member')
                path = directory.joinpath(*parts)
                for component in (path, *path.parents):
                    if component == directory.parent: break
                    if component.is_symlink() or component.is_junction():
                        raise ValueError('linked local OA RECORD member')
                info = path.lstat()
                if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
                    raise ValueError('local OA RECORD member must be ordinary')
                result[name.casefold()] = (name, path)
        permitted = (allowed, allowed-{'abstraction_facade_native'}) if historical else (allowed,)
        if distributions not in permitted:
            raise ValueError('local OA distribution set is incomplete')
        reserved = {path.relative_to(directory).as_posix().casefold() for _,path in files
                    if path.relative_to(directory).parts[0] in package_roots}
        if not reserved <= set(result):
            raise ValueError('unrecorded stale local OA file')
        return result
    old, new = roster(site,historical=True), roster(replacement)
    if require_current and ({name:_sha256(path) for name,(_,path) in old.items()}
                            != {name:_sha256(path) for name,(_,path) in new.items()}):
        raise ValueError('local recovery OA overlay differs from frozen integration')
    replacement_files, _, _, _ = _scan_tree(replacement.parent, replacement.name, 16 << 20, selected_root=True)
    replacement_members = {path.relative_to(replacement).as_posix().casefold() for _,path in replacement_files}
    if replacement_members != set(new):
        raise ValueError('local OA replacement includes foreign files')
    for name, path in old.values():
        expected = retained_files.get(name)
        if expected != dict(zip(('bytes', 'sha256'), _sha256(path))):
            raise ValueError('old OA file differs from retained runtime roster')
    for name, _ in new.values():
        target = site.joinpath(*name.split('/'))
        if target.exists() and name.casefold() not in old:
            raise ValueError('local OA replacement collides with retained data')
    parents = set()
    for _, path in old.values():
        parents.update(parent for parent in path.parents if parent.is_relative_to(site) and parent != site)
    for _, path in old.values(): path.unlink()
    for directory in sorted(parents, key=lambda p:len(p.parts), reverse=True):
        if not any(directory.iterdir()): directory.rmdir()
    for name, path in new.values():
        target = site.joinpath(*name.split('/'))
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(path, target)
        if _sha256(path) != _sha256(target):
            raise ValueError('local OA replacement copy differs')
    return sorted(name for name, _ in old.values())


def _local_native_source_identity(root, source, identity):
    owner_path = root/'adopters/comfyui/installer/build_installer.py'
    spec = importlib.util.spec_from_file_location('comfy_local_native_receiving_owner',owner_path)
    owner = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(owner)
    observed, _ = owner.private_native_source_identity(root,source,
        Path(identity.get('sdk_receipt_path','')), {name:Path(path) for name,path in identity.get('resource_tool_paths',{}).items()})
    return observed


def assemble_local_native_preview(host: Path, backend: Path, control: Path, output: Path, *,
                                  host_receipt: dict, private_context: dict) -> dict:
    """Package complete local developer trees; supplies no hosted/signed intake.

    The person extracts this ZIP into a fresh folder and runs the existing
    native seal/check/register operations. Their Keep remains a separate act.
    Every runtime byte is checked before and after writing the stored ZIP.
    """
    context = _local_private_context(private_context)
    root = Path(__file__).resolve().parents[1]
    _require_private_source_revision(root, context['source_commit'])
    if host_receipt.get('source_mode') != 'private-local-preview':
        raise ValueError('local preview requires its private native producer')
    identity = host_receipt.get('private_source_identity')
    if not isinstance(identity,dict) or identity.get('source_commit') != context['source_commit']:
        raise ValueError('local preview native producer source differs')
    observed = _local_native_source_identity(root,context['source_commit'],identity)
    if (identity != observed or host_receipt.get('source_files') != observed['source_files']
            or host_receipt.get('graph_sources') != observed['graph_sources']
            or host_receipt.get('resource_tools') != observed['resource_tools']
            or host_receipt.get('resource_postprocess') != observed['resource_postprocess']
            or host_receipt.get('compiler') != observed['compiler']):
        raise ValueError('local preview native producer input closure differs')
    output = Path(output).absolute()
    if (output.suffix != '.zip' or output.exists() or output.is_symlink()
            or not output.parent.is_dir() or output.parent.is_symlink() or output.parent.is_junction()
            or any(p.is_symlink() or p.is_junction() for p in output.parents)):
        raise ValueError('local preview requires a fresh ordinary ZIP output')
    host = Path(host).absolute()
    info = host.lstat()
    if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or host.is_symlink() or host.is_junction()
            or any(p.is_symlink() or p.is_junction() for p in host.parents)):
        raise ValueError('local preview host must be an ordinary native input')
    measured = dict(zip(('bytes', 'sha256'), _sha256(host)))
    if host_receipt.get('native') != measured:
        raise ValueError('local preview host differs from actual native producer')
    selected = {}
    manifests = {}
    total = info.st_size
    for role, directory, limit in (('backend', _extended(Path(backend).absolute()), 16 << 30),
                                   ('control', _extended(Path(control).absolute()), app_pair_receipt.NATIVE_CONTROL_BYTES)):
        expected_root = 'oa-' + role
        if directory.name != expected_root:
            raise ValueError('local preview runtime has another root')
        files, _, size, _ = _scan_tree(directory.parent, directory.name, limit, selected_root=True)
        manifest_path = directory/'manifest.json'
        manifest_size, manifest_sha = _sha256(manifest_path)
        if not 0 < manifest_size <= app_pair_receipt.BACKEND_RUNTIME_MANIFEST_BYTES:
            raise ValueError('local preview runtime manifest exceeds bound')
        pin = dict(bytes=manifest_size, sha256=manifest_sha)
        if host_receipt.get('resources', {}).get(role) != pin:
            raise ValueError('local preview runtime differs from native embedded input')
        manifest = _local_runtime_json(manifest_path.read_bytes())
        _local_runtime_rows(manifest)
        observed = {name.removeprefix(directory.name+'/'):dict(zip(('bytes', 'sha256'), _sha256(path)))
                    for name, path in files if path != manifest_path}
        if observed != manifest.get('runtime_files'):
            raise ValueError('local preview runtime differs from complete manifest')
        if any(path.stat().st_nlink != 1 for _, path in files):
            raise ValueError('local preview runtime contains a hard link')
        selected[role] = files
        manifests[role] = dict(pin=pin, files=observed)
        total += size
    if shutil.disk_usage(output.parent).free < MIN_FREE_AFTER_BUILD_BYTES + total:
        raise ValueError('insufficient local preview output space')
    setup = root/'adopters/comfyui/installer/local-preview.cmd'
    setup_raw = setup.read_bytes()
    metadata = dict(schema=1, source_mode='private-local-preview', signing='unsigned',
        private_provenance=context, host=dict(name=app_pair_receipt.NATIVE_HOST, **measured),
        resources={name:value['pin'] for name,value in manifests.items()},
        setup=dict(name='Setup OA ComfyUI.cmd', bytes=len(setup_raw), sha256=hashlib.sha256(setup_raw).hexdigest()))
    try:
        with zipfile.ZipFile(output, 'x', compression=zipfile.ZIP_STORED, allowZip64=True) as archive:
            archive.writestr('local-preview.json', json.dumps(metadata, sort_keys=True)+'\n')
            archive.writestr('Setup OA ComfyUI.cmd', setup_raw)
            for name, path in [(app_pair_receipt.NATIVE_HOST, host), *selected['backend'], *selected['control']]:
                if path == host:
                    expected = measured
                else:
                    role, relative = name.split('/', 1)
                    item = manifests[role.removeprefix('oa-')]
                    expected = item['pin'] if relative == 'manifest.json' else item['files'][relative]
                entry = zipfile.ZipInfo(name, _ZIP_TIME)
                entry.compress_type = zipfile.ZIP_STORED
                digest, size = hashlib.sha256(), 0
                with archive.open(entry, 'w', force_zip64=True) as writer, path.open('rb') as reader:
                    for block in iter(lambda: reader.read(1 << 20), b''):
                        size += len(block)
                        if size > expected['bytes']:
                            raise ValueError('local preview member grew during assembly')
                        digest.update(block)
                        writer.write(block)
                if dict(bytes=size, sha256=digest.hexdigest()) != expected:
                    raise ValueError('local preview member changed during assembly')
        if _sha256(host) != (measured['bytes'], measured['sha256']) or setup.read_bytes() != setup_raw:
            raise ValueError('local preview native source changed during assembly')
        for role, files in selected.items():
            directory = _extended(Path(backend if role == 'backend' else control).absolute())
            observed = {name.removeprefix(directory.name+'/'):dict(zip(('bytes', 'sha256'), _sha256(path)))
                        for name,path in files if path.name != 'manifest.json' or path.parent != directory}
            after, _, _, _ = _scan_tree(directory.parent, directory.name, 16 << 30, selected_root=True)
            if observed != manifests[role]['files'] or after != files:
                raise ValueError('local preview runtime changed during assembly')
            if _sha256(directory/'manifest.json') != tuple(manifests[role]['pin'][key] for key in ('bytes','sha256')):
                raise ValueError('local preview runtime manifest changed during assembly')
        size, sha = _sha256(output)
        return dict(metadata, archive=dict(name=output.name, bytes=size, sha256=sha))
    except BaseException:
        output.unlink(missing_ok=True)
        raise


def assemble_native_archive(host: Path, source_directory: Path, corresponding_source: dict,
                            context: dict, output: Path, *, version: str,
                            notices: dict[str, Path], control: Path, archive_tool: Path,
                            backend_manifest: Path) -> dict:
    """Package admitted signed native bytes beside the external AMD resource.

    The normal train caller supplies independently authenticated API/signer
    context. The shared pair reader receives the completed package before it is
    returned; this function cannot turn a private backend into public input.
    """
    output = Path(output)
    if output.exists() or output.is_symlink():raise ValueError('native archive output must be fresh')
    if (not output.parent.is_dir() or output.parent.is_symlink() or output.parent.is_junction()
            or any(p.is_symlink() or p.is_junction() for p in output.parent.parents)
            or shutil.disk_usage(output.parent).free<MIN_FREE_AFTER_BUILD_BYTES+MAX_RELEASE_ASSET_BYTES):
        raise ValueError('insufficient ordinary native archive output space')
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?', version):
        raise ValueError('invalid native archive version')
    offer=app_pair_receipt.validate_corresponding_source(corresponding_source)
    backend=app_pair_receipt.validate_backend_resource(context['backend'])
    if offer['data_release']!=backend['data_release']:
        raise ValueError('native archive/source DATA locations differ')
    def pin(name,raw):return dict(name=name,bytes=len(raw),sha256=hashlib.sha256(raw).hexdigest())
    host=Path(host)
    info=host.lstat()
    if (not stat.S_ISREG(info.st_mode) or host.is_symlink() or host.is_junction() or info.st_nlink!=1
            or any(p.is_symlink() or p.is_junction() for p in host.parents)):
        raise ValueError('native archive host must be an ordinary signed input')
    host_raw=host.read_bytes();host_pin=pin(app_pair_receipt.NATIVE_HOST,host_raw)
    if context['producer'].get('host')!=host_pin:
        raise ValueError('native archive host differs from authenticated native intake')
    backend_manifest=Path(backend_manifest)
    if (backend_manifest.is_symlink() or backend_manifest.is_junction() or not backend_manifest.is_file()
            or backend_manifest.stat().st_size>app_pair_receipt.BACKEND_RUNTIME_MANIFEST_BYTES):
        raise ValueError('native archive requires ordinary bounded backend metadata')
    runtime=backend_manifest.read_bytes()
    expected=backend['provenance']['manifest']
    if pin(expected['name'],runtime)!=expected:
        raise ValueError('native archive runtime bytes differ from backend provenance')
    if json.loads(runtime)!=context['backend_manifest']:
        raise ValueError('native archive backend metadata differs from admitted context')
    embedded=dict(schema=1,distribution=app_pair_receipt.NATIVE_VARIANT,
        source_policy=context['source_policy'],oa_integration=context['oa_integration'],backend_manifest=expected)
    if context['producer'].get('embedded_integration')!=embedded:
        raise ValueError('native archive producer does not bind the selected integration')
    payload={host_pin['name']:host_raw,expected['name']:runtime}
    tool_inputs=_archive_tool_inputs(archive_tool)
    tool_raw=tool_inputs['7zr.exe'].read_bytes()
    payload['archive-tool/7zr.exe']=tool_raw
    supplier_notices=[]
    for name,path in tool_inputs.items():
        if name.startswith('DOC/'):
            member='archive-tool/'+name;payload[member]=path.read_bytes();supplier_notices.append(member)
    control=_extended(control)
    if control.name!=app_pair_receipt.NATIVE_CONTROL_ROOT:
        raise ValueError('native control stage has another root')
    control_files,_,control_bytes,_=_scan_tree(control.parent,control.name,MAX_RELEASE_ASSET_BYTES)
    selected_control=context['control_manifest']
    if not isinstance(selected_control,dict) or set(selected_control)!={'runtime_files'}:
        raise ValueError('native control inventory must be source-owned')
    observed_control={}
    for name,path in control_files:
        relative=name.removeprefix(control.name+'/')
        if relative=='manifest.json':
            if _sha256(path)!=(context['control_pin']['bytes'],context['control_pin']['sha256']):
                raise ValueError('native control manifest differs from signed native input')
            if json.loads(path.read_bytes())!=selected_control:raise ValueError('native control manifest contents differ')
        else:
            size,sha=_sha256(path);observed_control[relative]=dict(bytes=size,sha256=sha)
    if observed_control!=selected_control['runtime_files']:
        raise ValueError('native control files differ from admitted manifest')
    bundle=dict(id='comfyui',title='ComfyUI — OA build',
                host=dict(file=host_pin['name'],bytes=host_pin['bytes'],sha256=host_pin['sha256']),
                embedded_integration=embedded)
    payload['bundle-app.json']=(json.dumps(bundle,sort_keys=True)+'\n').encode()
    source_files=[]
    with app_pair_receipt.ArchiveSetReader(source_directory,offer['archive'],format='zip') as stream:
        with app_pair_receipt.checked_zip(stream,bounded_members=True,expanded_limit=16<<30) as sources:
            for entry in sources.infolist():
                if entry.is_dir():continue
                digest=hashlib.sha256()
                with sources.open(entry) as reader:
                    for block in iter(lambda:reader.read(1<<20),b''):digest.update(block)
                source_files.append(dict(name=entry.filename,bytes=entry.file_size,sha256=digest.hexdigest()))
    proofs=dict(context['signatures'],producer_binding=context['producer'],
                source=dict(schema=2,source_pair=offer['archive'],files=source_files))
    proof_pins={}
    for name,value in proofs.items():
        member=name+'.json';raw=(json.dumps(value,sort_keys=True)+'\n').encode()
        payload[member]=raw;proof_pins[name]=pin(member,raw)
    for name,path in notices.items():
        _safe_name(name)
        if name in payload or not app_pair_receipt.NOTICE.search(name):raise ValueError('invalid native archive notice')
        path=Path(path);info=path.lstat()
        if (not stat.S_ISREG(info.st_mode) or path.is_symlink() or path.is_junction() or info.st_nlink!=1):
            raise ValueError('native archive notice must be ordinary')
        payload[name]=path.read_bytes()
    manifest=dict(schema=3,variant=app_pair_receipt.NATIVE_VARIANT,version=version,
        producer_source_commit=context['producer']['source'],comfyui_source_commit=backend['comfyui_ref'],
        control=context['control_pin'],
        archive_tool=pin('archive-tool/7zr.exe',tool_raw),programs=app_pair_receipt.native_programs(),
        source_policy=context['source_policy'],backend_input_descriptor=context['backend_input_descriptor'],
        oa_integration=context['oa_integration'],backend=backend,corresponding_source=offer,
        embedded_integration=embedded,bundle_app=pin('bundle-app.json',payload['bundle-app.json']),
        roles=dict(host=host_pin),proofs=proof_pins,preserved_notice_paths=sorted(supplier_notices),added_notice_paths=sorted(notices))
    payload[PROVENANCE]=(json.dumps(manifest,sort_keys=True)+'\n').encode()
    if sum(map(len,payload.values()))+control_bytes>=MAX_RELEASE_ASSET_BYTES:
        raise ValueError('native archive exceeds release asset bound')
    try:
        with zipfile.ZipFile(output,'x',compression=zipfile.ZIP_STORED) as archive:
            for name,raw in sorted(payload.items()):archive.writestr(zipfile.ZipInfo(name,_ZIP_TIME),raw)
            for name,path in control_files:_write_zip_file(archive,name,path)
        if output.stat().st_size>=MAX_RELEASE_ASSET_BYTES:
            raise ValueError('native archive exceeds release asset bound')
        if _scan_tree(control.parent,control.name,MAX_RELEASE_ASSET_BYTES)[0]!=control_files:
            raise ValueError('native control topology changed during assembly')
        return app_pair_receipt.inspect_desktop_set(output,source_directory,offer['archive'],
            desktop_context=context,require_clean_paths=True)
    except BaseException:
        output.unlink(missing_ok=True)
        raise


def build_native_control(descriptor: Path, descriptor_sha256: str, integration: Path,
                         integration_size: int, integration_sha256: str, output: Path, *,
                         candidate_source: str, run_id: int, run_attempt: int,
                         integration_options: dict, adapter: Path,
                         public_sources: dict[str, Path] | None,
                         private_context: dict | None = None) -> dict:
    """Build the complete selected PBS/SDK control root through existing owners.

    The complete Python distribution is retained. AMD/Torch wheels are never
    installed into this root. The existing public integration owner authenticates
    the SDK wheel/producer/source closure before its ordinary offline install.
    """
    private = private_context is not None
    if private:
        private_context = _private_integration_context(private_context)
        if candidate_source is not None or run_id is not None or run_attempt is not None or integration_options:
            raise ValueError('private native control refuses public run identity')
    elif (not re.fullmatch('[0-9a-f]{40}',candidate_source or '') or type(run_id) is not int or run_id<=0
          or type(run_attempt) is not int or run_attempt<=0):
        raise ValueError('native control requires actual public source/run/attempt')
    value,files=_native_control_python_inputs(_extended(descriptor),descriptor_sha256)
    output=_extended(output)
    if output.name!=app_pair_receipt.NATIVE_CONTROL_ROOT or output.exists() or output.is_symlink():
        raise ValueError('native control output must be a fresh oa-control root')
    adapter=_extended(adapter);info=adapter.lstat()
    if (not stat.S_ISREG(info.st_mode) or adapter.is_symlink() or adapter.is_junction() or info.st_nlink!=1
            or not 0<info.st_size<=1<<20 or any(p.is_symlink() or p.is_junction() for p in adapter.parents)):
        raise ValueError('native control adapter must be ordinary source')
    adapter_raw=adapter.read_bytes()
    selected_python=sum(files[name].stat().st_size for name in value['python_files'])
    parent=output.parent
    if (not parent.is_dir() or parent.is_symlink() or parent.is_junction()
            or any(p.is_symlink() or p.is_junction() for p in parent.parents)
            or shutil.disk_usage(parent).free<MIN_FREE_AFTER_BUILD_BYTES+2*selected_python+(64<<20)+2*integration_size):
        raise ValueError('insufficient ordinary native control staging space')
    with owned_temporary_tree(prefix='oa-comfy-control-',parent=_plain(parent)) as temporary:
        work=_extended(temporary);extract=work/'extract';extract.mkdir()
        portable=extract/'backend';portable.mkdir()
        python_root=portable/'python_embeded';python_root.mkdir()
        input_pins={row['name']:(row['bytes'],row['sha256']) for row in value['files'] if row['name'] in files}
        python_pins={name:input_pins[name] for name in value['python_files']}
        for name in value['python_files']:
            target=python_root.joinpath(*name.removeprefix('python/').split('/'))
            target.parent.mkdir(parents=True,exist_ok=True)
            shutil.copyfile(files[name],target)
            if _sha256(target)!=python_pins[name]:raise ValueError('native control Python copy differs')
        archive,options,manifest,_=_freeze_backend_integration(work,integration,integration_options,
            integration_size,integration_sha256,source=candidate_source,run_id=run_id,run_attempt=run_attempt,
            private_context=private_context)
        components = [row for row in manifest['wheels']
                      if row['filename'].startswith('abstraction_facade_native-')]
        if len(components) != 1 or not re.fullmatch('[0-9a-f]{64}', components[0].get('native_library_sha256', '')):
            raise ValueError('native control requires its selected AppModel companion')
        _public_source_checkouts(manifest,public_sources)
        selected_adapter = (Path(__file__).resolve().parents[1]/'adopters/comfyui/node/oa_native_host.py' if private
                            else Path(public_sources['openabstractions/adopter-comfyui'])/'.installer/oa_native_host.py')
        if not same_existing_file(adapter,selected_adapter):
            raise ValueError('native control adapter is not the selected public adopter source')
        with _selected_python_executable(python_root/'python.exe',python_pins['python/python.exe'],_sha256) as python:
            _apply_integration(portable,archive,work,install_wheels=True,archive_size=integration_size,
                archive_sha256=integration_sha256,require_public=not private,require_wrapper=True,
                python_executable=python,**options)
        wheels={work/'integration/wheels'/row['filename']:row['sha256'] for row in manifest['wheels']}
        _fill_wheel_records(python_root/'Lib/site-packages',wheels)
        _seal_private_python(portable,work,embedded=False,backend_bootstrap=True)
        (python_root/'oa_native_host.py').write_bytes(adapter_raw)
        with _selected_python_executable(python_root/'python.exe',python_pins['python/python.exe'],_sha256) as python:
            proof = _native_control_appmodel_check(python, python_root, work)
        if proof.get('library_sha256') != components[0]['native_library_sha256']:
            raise ValueError('sealed native control loaded another AppModel component')
        licenses=python_root/'OA-LICENSES';licenses.mkdir(exist_ok=True)
        python_license=files[value['runtime_provenance']['python']['license']]
        shutil.copyfile(python_license,licenses/'Python-LICENSE')
        if _sha256(licenses/'Python-LICENSE') != input_pins[value['runtime_provenance']['python']['license']]:
            raise ValueError('native control Python license copy differs')
        shutil.copyfile(Path(__file__).resolve().parents[1]/'LICENSE',licenses/'OpenAbstractions-LICENSE')
        stage=work/'control-stage';stage.mkdir()
        root=stage/app_pair_receipt.NATIVE_CONTROL_ROOT;python_root.rename(root)
        if (root/'manifest.json').exists() or (root/'manifest.json').is_symlink():
            raise ValueError('selected Python source collides with native control manifest')
        _normalize_runtime_directories(stage,root.name,app_pair_receipt.NATIVE_CONTROL_BYTES)
        inventory,_,_,_=_scan_tree(stage,root.name,app_pair_receipt.NATIVE_CONTROL_BYTES)
        rows={name.removeprefix(root.name+'/'):dict(bytes=path.stat().st_size,sha256=_sha256(path)[1])
              for name,path in inventory}
        app_pair_receipt.native_control_inventory(dict(runtime_files=rows))
        raw=(json.dumps(dict(runtime_files=rows),sort_keys=True)+'\n').encode()
        if len(raw)>app_pair_receipt.BACKEND_RUNTIME_MANIFEST_BYTES or len(rows)>app_pair_receipt.NATIVE_CONTROL_FILES:
            raise ValueError('native control manifest exceeds existing runtime bounds')
        (root/'manifest.json').write_bytes(raw)
        if (adapter.read_bytes()!=adapter_raw or any(_sha256(path)!=input_pins[name] for name,path in files.items())):
            raise ValueError('native control source changed')
        root.rename(output)
        return dict(bytes=len(raw),sha256=hashlib.sha256(raw).hexdigest())


def _native_control_appmodel_check(python: Path, root: Path, work: Path) -> dict:
    """Receive the sealed control's actual core and synthetic graph before commit."""
    source = r'''
import hashlib, importlib.util, io, json, sys
from pathlib import Path
def plain(value):
    # CPython 3.12 keeps the launcher's extended-length prefix in its
    # executable-relative sys.path entry.
    value, prefix = str(value), chr(92) * 2 + '?' + chr(92)
    return Path(value[4:] if value.startswith(prefix) else value).resolve()
root = plain(sys.argv[1])
assert Path(sys.executable).samefile(root/'python.exe')
assert all(root == plain(p) or root in plain(p).parents for p in sys.path)
sys.path.insert(0, str(root/'Lib/site-packages'))
from abstraction_facade_native import library_path
from abstraction.facade import appmodel as rec
from appmodel import Registry, Refuse, Server
import appmodel
assert plain(appmodel.__file__).is_relative_to(root/'Lib/site-packages')
library = plain(library_path())
assert library.is_relative_to(root/'Lib/site-packages/abstraction_facade_native')
spec = importlib.util.spec_from_file_location('packaged_comfy_control', root/'oa_native_host.py')
adapter = importlib.util.module_from_spec(spec); spec.loader.exec_module(adapter)
registry = adapter.native_registry('packaged-control-fixture', Registry, rec, Refuse)
assert registry._lib.abi_version() == 2
requests = []
canvas = dict(nodes=[dict(id='7', type='KSampler', title='KSampler', mode='always',
    widgets=[dict(name='steps', type='int', value=20, min=1, max=10000)], inputs=[], outputs=[])],
    links=[], groups=[])
def write(unused, body, limit):
    assert limit == adapter.GRAPH_LIMIT
    request = json.loads(body); requests.append(request)
    assert request['operation'] == 'edit'
    op, = request['arguments']['ops']
    assert op == dict(op='set_widget', node='7', widget='steps', value=op['value'])
    canvas['nodes'][0]['widgets'][0]['value'] = op['value']
    graph._result(dict(type='result', id=request['id'], generation=request['generation'],
        page=request['page'], workflow=request['workflow'], outcome='applied',
        projection=dict(epoch=request['workflow'], sequence=request['sequence']+1, graph=canvas)))
graph = adapter.GraphSession(registry, io.BytesIO(), io.BytesIO(), lambda read, limit: None, write, Refuse, timeout=.1)
graph._person_projection(dict(type='projection', generation='g'*16, page='p'*16, workflow='w'*16,
    sequence=0, graph=canvas))
assert str(registry.describe().outcome) == 'page'
server = Server(registry)
def mcp(name, arguments):
    return server.handle(json.dumps(dict(jsonrpc='2.0',id=1,method='tools/call',
        params=dict(name=name,arguments=arguments))).encode())
assert 'result' in mcp('app_describe', {})
def invocation(args, revision='', key=''):
    return rec.Invocation(context=graph.context.ref(), arguments=json.dumps(args), mode='apply',
                          expected_revision=revision, idempotency_key=key)
def read(steps):
    answer = mcp('graph.read', json.loads(rec.to_json(invocation({}))))
    result = json.loads(answer['result']['content'][0]['text'])
    assert result['outcome'] == 'applied'
    view = result['effects']
    assert view['nodes'][0]['widgets'][0]['value'] == steps
    assert view['revision'] == result['revision'] == graph.context.revision
    return view['revision']
def edit(steps, revision, key):
    return invocation(dict(ops=[dict(op='set_widget', node='7', widget='steps', value=steps)]), revision, key)
before = read(20)
change = edit(21, before, 'apply')
assert str(registry.call('graph.edit', change).outcome) == 'applied'
after = read(21)
assert after != before
count = len(requests)
assert str(registry.call('graph.edit', change).outcome) == 'applied' and len(requests) == count
assert str(registry.call('graph.edit', edit(20, before, 'stale')).outcome) == 'conflict'
assert str(registry.call('graph.edit', edit(0, after, 'invalid')).outcome) == 'invalid'
assert len(requests) == count
assert str(registry.call('graph.edit', edit(20, after, 'restore')).outcome) == 'applied'
assert read(20) != after
graph.close(); registry.close(); registry.dispose()
print(json.dumps(dict(abi=2, library_sha256=hashlib.sha256(library.read_bytes()).hexdigest(),
    describe=True, read=True, apply=True, replay=True, refusal=True, restore=True, closed=True)))
'''
    result = subprocess.run([str(python), '-I', '-S', '-B', '-c', source, str(_plain(root))],
                            cwd=_plain(work), env=_private_python_env(work), capture_output=True,
                            text=True, timeout=60)
    if result.returncode or len(result.stdout) > 4096 or len(result.stderr) > 4096:
        raise ValueError('sealed native control AppModel receiving failed: ' + result.stderr[:4096])
    proof = json.loads(result.stdout)
    if proof.get('abi') != 2 or not all(proof.get(key) is True for key in
            ('describe','read','apply','replay','refusal','restore','closed')):
        raise ValueError('sealed native control AppModel receiving is incomplete')
    print(json.dumps({'control_appmodel_receiving': proof}, sort_keys=True))
    return proof


def _snapshot_verified_source(path: Path, snapshot: Path, expected_size: int, expected_sha256: str) -> None:
    path, snapshot = _extended(path), _extended(snapshot)
    if path.is_symlink() or not path.is_file():
        raise ValueError("source must be a regular file")
    digest = hashlib.sha256()
    size = 0
    before = path.stat()
    identity = lambda info: (info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_nlink)
    with path.open("rb") as source, snapshot.open("xb") as target:
        held = os.fstat(source.fileno())
        if not stat.S_ISREG(held.st_mode) or identity(before) != identity(held):
            raise ValueError("source must be a regular file")
        for block in iter(lambda: source.read(1 << 20), b""):
            size += len(block)
            if size > expected_size:
                raise ValueError("official archive size mismatch")
            digest.update(block)
            target.write(block)
        if identity(held) != identity(os.fstat(source.fileno())) or identity(held) != identity(path.stat()):
            raise ValueError('source changed during verified snapshot')
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


def _scan_tree(stage: Path, archive_root: str, max_bytes: int, *, selected_root: bool = False) -> tuple[list[tuple[str, Path]], list[str], int, list[str]]:
    roots = [stage/archive_root] if selected_root else list(stage.iterdir())
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


def _normalize_runtime_directories(stage: Path, archive_root: str, max_bytes: int, *, selected_root: bool = False) -> list[str]:
    """Remove only ordinary empty staging dirs absent from file-only manifests."""
    if archive_root not in ('backend',app_pair_receipt.NATIVE_CONTROL_ROOT):
        raise ValueError('runtime normalization requires an owned backend/control staging root')
    stage=_extended(stage)
    files,dirs,_,_=_scan_tree(stage,archive_root,max_bytes,selected_root=selected_root)
    required={archive_root}
    def states(items):
        result={}
        for name,path in items:
            value=path.lstat()
            result[name]=(value.st_dev,value.st_ino,value.st_size,value.st_mtime_ns,value.st_nlink)
        return result
    before=states(files)
    for name,_ in files:
        parts=name.split('/')
        required.update('/'.join(parts[:index]) for index in range(1,len(parts)))
    removed=[]
    for name in sorted(set(dirs)-required,key=lambda value:(value.count('/'),value),reverse=True):
        path=stage.joinpath(*name.split('/'))
        component=path
        while True:
            value=component.lstat()
            if (not stat.S_ISDIR(value.st_mode) or value.st_nlink<1
                    or getattr(value,'st_file_attributes',0)&getattr(stat,'FILE_ATTRIBUTE_REPARSE_POINT',0)):
                raise ValueError('runtime directory changed or linked before normalization: '+name)
            if component==stage:break
            component=component.parent
        relative_beneath_existing(path,stage)
        if any(path.iterdir()):raise ValueError('runtime directory gained unlisted content: '+name)
        path.rmdir();removed.append(name)
    after,actual_dirs,_,_=_scan_tree(stage,archive_root,max_bytes,selected_root=selected_root)
    if states(after)!=before or set(actual_dirs)!=required:
        raise ValueError('runtime file topology changed during directory normalization')
    return removed


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
    return subprocess.check_output(["git", "-C", str(_plain(checkout)), "rev-parse", "HEAD"], text=True, timeout=30,
                                   env=source_git_environment()).strip()


def _private_python_env(temporary: Path) -> dict[str, str]:
    """Use the fixed Windows loader owner and only private build controls."""
    temporary = _plain(temporary)
    local = _extended(temporary)
    if (not temporary.is_absolute() or local.is_symlink() or local.is_junction()
            or not local.is_dir()):
        raise ValueError('private Python temporary directory must be ordinary and absolute')
    env = _windows_archive_environment(temporary)
    env.update(PYTHONDONTWRITEBYTECODE="1", PYTHONNOUSERSITE="1", PIP_NO_CACHE_DIR="1",
               PIP_CONFIG_FILE=os.devnull, PIP_DISABLE_PIP_VERSION_CHECK="1")
    return env


def _recorded_files(portable: Path, wanted: set[str], label: str, *, missing_bytecode_ok: bool = False,
                    bootstrap_omissions: dict | None = None) -> list[Path]:
    """Audit exact wheel ownership before removing the named distributions."""
    site = portable / "python_embeded" / "Lib" / "site-packages"
    available = {path.name for path in site.glob("*.dist-info")}
    if not wanted <= available:
        raise ValueError(f"official portable {label} distribution set differs from pin")
    ownership: dict[str, set[str]] = {}
    candidates: dict[str, Path] = {}
    omitted: set[str] = set()
    issues: list[str] = []
    portable_root = portable.resolve()
    for dist in site.glob("*.dist-info"):
        record = dist / "RECORD"
        if not record.is_file():
            if dist.name in wanted:
                raise ValueError(f"{label} distribution lacks RECORD")
            continue
        with record.open(encoding="utf-8", newline="") as stream:
            for row in csv.reader(stream):
                if len(row) != 3 or not row[0]:
                    issues.append(f"{dist.name}/RECORD: invalid installed wheel RECORD row {row!r}")
                    continue
                member = (site / Path(*row[0].split("/"))).resolve()
                if not member.is_relative_to(portable_root):
                    issues.append(f"{dist.name}/RECORD: wheel RECORD path escapes portable tree: {row[0]}")
                    continue
                key = str(member).casefold()
                ownership.setdefault(key, set()).add(dist.name)
                if dist.name in wanted:
                    if ((dist.name, row[0]) in (bootstrap_omissions or {})
                            and row[1:] == bootstrap_omissions[(dist.name, row[0])]
                            and not os.path.lexists(member)):
                        omitted.add(key)
                        continue
                    if missing_bytecode_ok and member.suffix == ".pyc" and not row[1] and not member.exists():
                        continue
                    if not member.is_file() or member.is_symlink():
                        issues.append(f"{label} wheel RECORD file is missing or linked: {dist.name}/RECORD: {row[0]}")
                        continue
                    candidates[key] = member
    for key in candidates.keys() | omitted:
        if ownership[key] - wanted:
            issues.append(f"{label} wheel RECORD path is shared with retained distribution: {key}")
    if issues:
        raise ValueError("\n".join(issues))
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


def _launcher_inputs():
    """Snapshot the source launcher and its exact, explicitly selected helper."""
    owner = _launcher()
    helper = LAUNCHER_SOURCE.with_name(owner.INTERPRETER_NAME)
    owner._interpreter_owner()  # Plain private sibling and immutable source pin.
    helper_bytes = helper.read_bytes()
    if hashlib.sha256(helper_bytes).hexdigest() != owner.INTERPRETER_SHA256:
        raise ValueError("consumed interpreter helper differs from source-owned bytes")
    root = Path(__file__).resolve().parent.parent
    if (root / "scripts/split.manifest").is_file():
        atomic = root / "openabstractions-flat/abstraction-cas/python/abstraction_cas.py"
        license_file = root / "LICENSE"
    else:
        # The public charter projects the same canonical inputs explicitly.
        atomic = LAUNCHER_SOURCE.parent / "oa_atomic/abstraction_cas.py"
        license_file = LAUNCHER_SOURCE.parent / "_oa_atomic_file.LICENSE"
    for source in (atomic, license_file):
        if (not source.is_file() or source.is_symlink() or source.is_junction()
                or source.stat().st_nlink != 1
                or any(p.is_symlink() or p.is_junction() for p in source.parents)):
            raise ValueError("atomic-file bootstrap input is not a plain source file")
    atomic_bytes = atomic.read_bytes()
    if hashlib.sha256(atomic_bytes).hexdigest() != owner._interpreter_owner().ATOMIC_SHA256:
        raise ValueError("consumed atomic-file helper differs from selected CAS source")
    return {LAUNCHER_NAME: LAUNCHER_SOURCE.read_bytes(), owner.INTERPRETER_NAME: helper_bytes,
            "oa_atomic/abstraction_cas.py": atomic_bytes,
            "_oa_atomic_file.LICENSE": license_file.read_bytes()}


def _stage_launcher(folder):
    inputs = _launcher_inputs()
    for name in inputs:
        if (folder / name).exists() or (folder / name).is_symlink():
            raise ValueError("upstream source collides with source-owned OA launcher/helper")
        parent = (folder / name).parent
        while parent != folder:
            if parent.exists() or parent.is_symlink() or parent.is_junction():
                raise ValueError("upstream source collides with source-owned OA bootstrap folder")
            parent = parent.parent
    for name, data in inputs.items():
        (folder / name).parent.mkdir(parents=True, exist_ok=True)
        (folder / name).write_bytes(data)
    _check_launcher(folder, inputs)
    return inputs


def _check_launcher(folder, inputs):
    if _launcher_inputs() != inputs:
        raise ValueError("launcher sources changed during packaging")
    for name, data in inputs.items():
        path = folder / name
        info = path.lstat()
        if (not stat.S_ISREG(info.st_mode) or path.is_symlink() or path.is_junction()
                or info.st_nlink != 1 or path.read_bytes() != data
                or any(p.is_symlink() or p.is_junction() for p in path.parents)):
            raise ValueError("staged launcher/helper differs from selected source")


def _remove_installers(portable: Path, work: Path, *, backend_bootstrap: bool = False) -> list[str]:
    """Remove pip and ensurepip: no install or build step may run in this Python.

    Security review rocm-7.2.1-pytorch-2.9.1-python-3.12.10, condition 2. pip
    leaves by its own RECORD, after the build's last pip install.
    """
    _require_owned_stage(portable, work)
    python_root = portable / "python_embeded"
    site = python_root / "Lib" / "site-packages"
    removed: list[str] = []
    pip_dists = {path.name for path in site.glob("*.dist-info") if path.name.casefold().startswith("pip-")}
    omissions = _backend_bootstrap_omissions(python_root) if backend_bootstrap else None
    if pip_dists:
        receipt = _remove_paths(portable, _recorded_files(portable, pip_dists, "pip", missing_bytecode_ok=True,
                                                       bootstrap_omissions=omissions))
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


def _seal_private_python(portable: Path, work: Path, *, embedded: bool = True,
                         backend_bootstrap: bool = False) -> dict[str, object]:
    """Leave a private Python the launch check accepts, prove it here and pin its RECORD set."""
    removed = _remove_installers(portable, work, backend_bootstrap=backend_bootstrap)
    root = portable / "python_embeded"
    site = root / "Lib" / "site-packages"
    sealed = _seal_bytecode_records(site)
    started = _start_without_site(portable, work) if embedded else None
    launcher = _launcher()
    records = launcher.records_digest(str(site))
    try:
        report = launcher.check_environment(str(site), str(root), records=records)
    except launcher.Refusal as refusal:
        raise ValueError(f"private Python fails the OA launch check: {refusal}") from None
    if report["unchecked_bytecode"]:
        raise ValueError("private Python keeps bytecode without a RECORD hash")
    result = {"removed_installers": removed, "sealed_bytecode_rows": sealed, "entrypoint_change": started,
            "records_sha256": records,
            "launch_check": {"files": report["files"], "hashed": report["hashed"]}}
    if not embedded:
        result["required_startup"] = "-S; OA launch check before site-packages activation"
    return result


def _pip_check(python: Path, env: dict[str, str]) -> set[str]:
    check = subprocess.run([str(python), "-B", "-s", "-m", "pip", "check"],
                           capture_output=True, text=True, env=env, cwd=env['TEMP'], timeout=120)
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
    env = _private_python_env(work)
    baseline_issues = _pip_check(python, env)
    removed = _remove_gpu_files(portable)
    lock = work / "cpu-requirements.lock"
    lock.write_text("".join(f"{requirement} --hash=sha256:{digest}\n"
                            for _, (_, digest, requirement) in sorted(wheel_specs.items())), encoding="ascii")
    subprocess.run([str(python), "-B", "-s", "-m", "pip", "install", "--no-index", "--no-deps",
                    "--no-compile", "--find-links", str(private_wheels), "--require-hashes", "-r", str(lock)],
                   check=True, env=env, cwd=work, timeout=900)
    filled = _fill_wheel_records(portable / "python_embeded" / "Lib" / "site-packages",
                                 {private_wheels / filename: digest
                                  for filename, (_, digest, _) in sorted(wheel_specs.items())})
    new_issues = _pip_check(python, env) - baseline_issues
    if new_issues:
        raise ValueError("CPU conversion introduced dependency conflicts: " + "; ".join(sorted(new_issues)))
    subprocess.run([str(python), "-B", "-s", "-c", "import torch; assert torch.__version__ == '2.9.1+cpu'; "
                    "assert torch.version.hip is None and torch.version.cuda is None"],
                   check=True, env=env, cwd=work, timeout=120)
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
    _stage_launcher(portable)
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


def _capture_git_source(checkout: Path, project: str, commit: str, work: Path,
                        *, limit: int = 8 << 30, timeout: float = 600) -> Path:
    """Capture builtin Git ZIP output without allowing excess bytes onto disk."""
    _safe_name(project)
    if not isinstance(commit, str) or not re.fullmatch(r'[0-9a-f]{40}', commit):
        raise ValueError('Git source requires an exact selected commit')
    if type(limit) is not int or limit <= 0 or timeout <= 0:
        raise ValueError('invalid Git source capture bound')
    staged = work / (project + ".zip")
    created = False
    child = subprocess.Popen(["git", "-C", str(_plain(checkout)), "archive", "--format=zip",
                              f"--prefix={project}-{commit}/", commit],
                             stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                             bufsize=0, env=source_git_environment())
    try:
        with staged.open('xb') as target:
            created = True
            # Python3.12 supports nonblocking Windows pipes. There are no reader
            # threads to outlive the child or wait indefinitely for delayed EOF.
            streams = {child.stdout: [0, limit], child.stderr: [0, 1 << 20]}
            for stream in streams: os.set_blocking(stream.fileno(), False)
            deadline = time.monotonic() + timeout
            while streams or child.poll() is None:
                if time.monotonic() >= deadline: raise subprocess.TimeoutExpired(child.args, timeout)
                progressed = False
                for stream, budget in list(streams.items()):
                    try: chunk = os.read(stream.fileno(), min(1 << 16, budget[1] - budget[0] + 1))
                    except BlockingIOError: continue
                    if not chunk:
                        del streams[stream]; continue
                    progressed = True; budget[0] += len(chunk)
                    if budget[0] > budget[1]: raise ValueError('Git source archive exceeds output bound')
                    if stream is child.stdout: target.write(chunk)
                if not progressed: time.sleep(.01)
            if child.wait() != 0: raise ValueError('Git source archive failed')
        return staged
    except BaseException:
        if child.poll() is None: child.kill()
        child.wait()
        if created: staged.unlink(missing_ok=True)
        raise
    finally:
        if child.poll() is None: child.kill()
        child.wait()
        child.stdout.close(); child.stderr.close()


def _append_git_source(combined: zipfile.ZipFile, checkout: Path, project: str, commit: str, work: Path) -> None:
    staged = _capture_git_source(checkout, project, commit, work)
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


def _public_source_checkouts(manifest: dict, checkouts: dict[str, Path] | None):
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
                ["git", "-C", str(_plain(checkout)), "status", "--porcelain=v1", "--untracked-files=all"],
                text=True, timeout=30, env=source_git_environment()).strip():
            raise ValueError("public OA integration source checkout differs: " + repository)
        project = "OA-Integration-" + repository.split("/", 1)[1]
        result.append((project, checkout, commit))
    return result


def _append_public_integration_sources(combined: zipfile.ZipFile, manifest: dict,
                                       checkouts: dict[str, Path] | None, work: Path) -> list[str]:
    result = []
    for project, checkout, commit in _public_source_checkouts(manifest, checkouts):
        _append_git_source(combined, checkout, project, commit, work)
        result.append(f'{project}-{commit}/')
    return result


def _source_member_plan(members, archives=()):
    """One closed roster for stored source ZIP admission and writing."""
    archives = list(archives)
    seen = set(); bound = 1024; rows = []
    for name, size, sha, source in members:
        if type(size) is not int or size < 0 or not re.fullmatch('[0-9a-f]{64}', sha):
            raise ValueError('invalid source member pin')
        if any(ord(char) < 32 for char in name): raise ValueError('unsafe source member name')
        for part in name.rstrip('/').split('/'): _safe_name(part)
        _register_name(name.rstrip('/'), seen)
        encoded = name.encode('utf-8')
        if len(encoded) > 65535: raise ValueError('source member name exceeds ZIP limit')
        # Fixed local/central headers, forced ZIP64 extras and descriptor, with slack.
        bound += size + 2 * len(encoded) + 200
        rows.append((name, size, sha, source))
    if bound > backend_limits()['archive_bytes']:
        raise ValueError('corresponding source exceeds logical archive bound')
    return dict(members=rows, bound=bound, archives=list(archives),
                temporary_bytes=sum(pin[0] for _, pin in archives))


def _write_source_plan(path, plan):
    """Normalize ZIP metadata and enforce the admitted ceiling before each write."""
    class Output:
        def __init__(self, raw): self.raw = raw
        def write(self, data):
            if self.raw.tell() + len(data) > plan['bound']:
                raise ValueError('source ZIP exceeds admitted bound')
            return self.raw.write(data)
        def tell(self): return self.raw.tell()
        def seek(self, *args): return self.raw.seek(*args)
        def flush(self): return self.raw.flush()
    created = False
    try:
        with contextlib.ExitStack() as stack:
            components = {}
            for source, pin in plan['archives']:
                if _sha256(source) != pin: raise ValueError('prepared Git source changed')
                components[source] = stack.enter_context(zipfile.ZipFile(source))
            raw = stack.enter_context(path.open('xb'))
            created = True
            archive = stack.enter_context(zipfile.ZipFile(Output(raw), 'w', allowZip64=True))
            for name, size, sha, source in plan['members']:
                if isinstance(source, bytes): reader = io.BytesIO(source)
                elif isinstance(source, tuple): reader = components[source[0]].open(source[1])
                else: reader = source.open('rb')
                entry = zipfile.ZipInfo(name, _ZIP_TIME)
                entry.compress_type = zipfile.ZIP_STORED
                entry.external_attr = (0o40755 if name.endswith('/') else 0o100644) << 16
                digest = hashlib.sha256(); total = 0
                with reader, archive.open(entry, 'w', force_zip64=True) as writer:
                    while True:
                        chunk = reader.read(min(1 << 20, size - total + 1))
                        if not chunk: break
                        total += len(chunk)
                        if total > size: raise ValueError('source member exceeds pin')
                        digest.update(chunk); writer.write(chunk)
                if total != size or digest.hexdigest() != sha:
                    raise ValueError('source member differs from pin')
        if path.stat().st_size > plan['bound']: raise ValueError('source ZIP exceeds admitted bound')
        for source, pin in plan['archives']:
            if _sha256(source) != pin: raise ValueError('prepared Git source changed')
    except BaseException:
        if created: path.unlink(missing_ok=True)
        raise


def _backend_source_plan(value, files, descriptor, tools, lock_raw, manifest, checkouts, work, reserve,
                         expected_sha256, private_context=None):
    descriptor_raw = descriptor.read_bytes()
    if hashlib.sha256(descriptor_raw).hexdigest() != expected_sha256:
        raise ValueError('backend descriptor changed before source plan')
    names = value['source_files'] + ['builder/scripts/build_local.py'] + value['comfyui_files']
    pins = {row['name']: row for row in value['files']}
    source_manifest = dict(schema=1, input_descriptor_sha256=expected_sha256,
        builder_commit=BACKEND_BUILDER_COMMIT, comfyui_commit=BACKEND_COMFY_COMMIT,
        files=[row for row in value['files'] if row['name'] in names])
    members = [(name, pins[name]['bytes'], pins[name]['sha256'], files[name]) for name in names]
    def add(name, source):
        size, sha = (len(source), hashlib.sha256(source).hexdigest()) if isinstance(source, bytes) else _sha256(source)
        members.append((name, size, sha, source))
        return size, sha
    for name, path in tools.items():
        if name == '7zr.exe': continue
        target = 'OA-Backend-Source/archive-tool/' + name
        size, sha = add(target, path)
        source_manifest['files'].append(dict(name=target, bytes=size, sha256=sha))
    add('OA-Backend-Source/backend-inputs.json', descriptor_raw)
    add('OA-Backend-Source/requirements.lock', lock_raw)
    add('backend-source.json', (json.dumps(source_manifest, sort_keys=True) + '\n').encode())
    add('backend-amd-v1.json', BACKEND_POLICY)
    add('backend-amd-v1.patch', BACKEND_POLICY.with_suffix('.patch'))
    launcher_inputs = _launcher_inputs()
    for name, raw in launcher_inputs.items(): add('OA-Backend-Source/' + name, raw)
    archives = []
    # Each builtin Git archive is bounded before writing. Already allocated ZIPs
    # remain in current disk usage; every retained ZIP is also recorded in T.
    selected_sources = _public_source_checkouts(manifest, checkouts)
    if private_context is not None:
        root = Path(__file__).resolve().parents[1]
        selected_sources = [('OA-Private-Source', root, private_context['source_commit'])]
        if private_context['native_source_commit'] != private_context['source_commit']:
            selected_sources.append(('OA-Native-Source', root, private_context['native_source_commit']))
    for project, checkout, commit in selected_sources:
        if shutil.disk_usage(work).free < reserve + backend_limits()['archive_bytes']:
            raise ValueError('insufficient free space for bounded Git source preflight')
        path = _capture_git_source(checkout, project, commit, work,
                                   limit=backend_limits()['archive_bytes'])
        archives.append((path, _sha256(path)))
        with app_pair_receipt.checked_zip(path, bounded_members=True) as component:
            for entry in component.infolist():
                digest = hashlib.sha256()
                with component.open(entry) as reader:
                    for chunk in iter(lambda: reader.read(1 << 20), b''): digest.update(chunk)
                members.append((entry.filename, entry.file_size, digest.hexdigest(), (path, entry.filename)))
    plan = _source_member_plan(members, archives)
    plan['launcher_inputs'] = launcher_inputs
    return plan


def _backend_reuses_snapshot_wheels(value, files):
    """The validated canonical roster is already one private pip wheelhouse."""
    selected = {wheel['file'] for wheel in value['wheels']}
    return (bool(selected) and all(Path(name).parent == Path('wheels') for name in selected)
            and {name for name in files if Path(name).parent == Path('wheels')} == selected)


def _backend_capacity(input_bytes, wheel_bytes, source_plan, integration_bytes, integration_expanded,
                      tool_bytes, reserve, *, phase='prepare'):
    limits = backend_limits()
    installation = input_bytes + wheel_bytes + limits['expanded_bytes']
    if phase == 'inputs':
        # Early input-owner lower bound; the producer later admits its exact
        # source/integration/tool plan before creating the snapshot.
        return installation + reserve
    source, archive = source_plan['bound'], limits['archive_bytes']
    phases = [('prepare', installation), ('pack', limits['expanded_bytes'] + archive),
              ('source', archive + source), ('finalize', 2 * archive + 2 * source)]
    names = [name for name, _ in phases]
    if phase not in names: raise ValueError('unknown backend capacity phase')
    return (max(size for _, size in phases[names.index(phase):]) + source_plan['temporary_bytes']
            + 2 * integration_expanded + integration_bytes + (8 << 20) + tool_bytes + reserve)


def _release_backend_phase(work, names):
    """Release only fixed direct children of this invocation's owned work tree."""
    if names not in (('inputs', 'wheels'), ('extract',)):
        raise ValueError('unknown backend temporary phase')
    for name in names:
        target = work / name
        if target.parent != work or not same_existing_file(target.parent, work):
            raise ValueError('backend temporary escaped its owner')
        remove_tree(target)


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


def _integration_distributions(*, public=False, candidate_version=None, appmodel=False) -> set[str]:
    """The wheel distributions scripts/package_comfyui_integration.py packs."""
    module = _integration_packager_owner()
    if public:
        return set(module.public_distributions(candidate_version, appmodel=appmodel).values())
    return ({name.split('-')[0] for name in module.PURE_WHEELS} | {'abstraction_ipc'} |
            ({'abstraction_facade_native'} if appmodel else set()))


def _integration_packager_owner():
    spec = importlib.util.spec_from_file_location("oa_package_comfyui_integration", INTEGRATION_PACKAGER)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _private_integration_context(value):
    required = {'source_commit', 'native_source_commit', 'native_library_sha256'}
    component = {'appmodel_library_sha256', 'appmodel_wheel_sha256'}
    if (not isinstance(value, dict) or set(value) not in (required, required | component)
            or any(not isinstance(value[key], str) or not re.fullmatch('[0-9a-f]{40}', value[key])
                   for key in ('source_commit', 'native_source_commit'))
            or not isinstance(value['native_library_sha256'], str)
            or not re.fullmatch('[0-9a-f]{64}', value['native_library_sha256'])
            or any(not isinstance(value[key], str) or not re.fullmatch('[0-9a-f]{64}', value[key])
                   for key in component if key in value)):
        raise ValueError('private developer integration requires exact source/native/DLL pins')
    return dict(value)


def _local_private_context(value):
    context = _private_integration_context(value)
    if not {'appmodel_library_sha256','appmodel_wheel_sha256'} <= set(context):
        raise ValueError('local native preview requires exact AppModel library and wheel pins')
    return context


def _private_appmodel_selected(wheels, context):
    count = sum(row['filename'].startswith('abstraction_facade_native-') for row in wheels)
    if count > 1 or bool(count) != ('appmodel_library_sha256' in context):
        raise ValueError('private AppModel dependency closure differs from selected context')


def _verify_private_integration_wheel(path, entry, context):
    ipc = entry['filename'].startswith('abstraction_ipc-')
    appmodel = entry['filename'].startswith('abstraction_facade_native-')
    native = ipc or appmodel
    expected_source = context['native_source_commit'] if ipc else context['source_commit']
    if entry.get('source_commit') != expected_source or _sha256(path) != (entry['size'], entry['sha256']):
        raise ValueError('private OA wheel source or bytes differ from selected pin')
    _integration_packager_owner().verify_wheel_archive(path, entry['filename'].split('-')[0],
                                                      'win_amd64' if native else 'any')
    if native:
        with zipfile.ZipFile(path) as archive:
            member = ('abstraction_facade_native/_native/abstraction_facade_appmodel.dll' if appmodel
                      else 'abstraction/ipc/_native/abstraction_ipc.dll')
            library = archive.read(member)
        expected_library = context.get('appmodel_library_sha256') if appmodel else context['native_library_sha256']
        if appmodel and entry['sha256'] != context.get('appmodel_wheel_sha256'):
            raise ValueError('private AppModel wheel differs from selected pin')
        if (entry.get('native_library_sha256') != expected_library
                or hashlib.sha256(library).hexdigest() != expected_library):
            raise ValueError('private OA native DLL differs from selected pin')
        if __package__:
            from .windows_native import validate_portable_windows_dll
        else:
            from windows_native import validate_portable_windows_dll
        validate_portable_windows_dll(library, 'win_amd64')


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
            component = [w for w in wheels if str(w['filename']).startswith('abstraction_facade_native-')]
            if (len(native) != 1 or len(component) > 1
                    or {w['filename'] for w in wheels if 'native_library_sha256' in w} !=
                       {w['filename'] for w in native + component}):
                raise refuse("it must hold one native IPC wheel and at most one selected AppModel companion")
            packed = {str(name).split("-")[0] for name in names}
            expected_distributions = _integration_distributions(public=manifest.get('source_mode')=='public-split',
                candidate_version=str(native[0]['filename']).split('-')[1], appmodel=bool(component))
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
                expected.add(_integration_packager_owner().wheel_requirement(wheel, appmodel=bool(component)))
            if len(lock) != len(wheels) or set(lock) != expected:
                raise refuse("requirements.lock and the manifest wheels differ")
    except (KeyError, UnicodeDecodeError, json.JSONDecodeError, zipfile.BadZipFile) as error:
        raise refuse("unreadable: %s" % error) from error
    return wheels


HELPER_CONFIG_NAME='ComfyUI/custom_nodes/abstraction_downloads/helper/pyvenv.cfg'
HELPER_CONFIG_TEMPLATE=b'home = {backend}/standalone-env\ninclude-system-site-packages = false\n'


def _stage_backend_helper(portable: Path) -> dict:
    """Stage the distinct helper before sealing; installation derives only its config."""
    node=portable/'ComfyUI/custom_nodes/abstraction_downloads'
    path=node/'oa_connect.py'
    spec=importlib.util.spec_from_file_location('oa_backend_connect_source',path)
    connect=importlib.util.module_from_spec(spec);spec.loader.exec_module(connect)
    interpreter=connect.oa_interpreter
    home=portable/'standalone-env'
    # This is admitted build DATA, not the ordinary runtime refresh API. Keep
    # the existing producer's distinct-file copy and identity checks; the
    # runtime helper now delegates refresh to its native preparer.
    selected={'python.exe':str(home/'python.exe')}
    for path in sorted(home.iterdir()):
        if interpreter._COPIED_DLL.fullmatch(path.name):selected[path.name]=str(path)
    if len(selected)>16 or not any(interpreter._PYTHON_DLL.fullmatch(name) for name in selected):
        raise ValueError('backend helper requires the admitted Python DLL closure')
    folder=node/'helper'
    if folder.exists() or folder.is_symlink() or folder.is_junction():
        raise ValueError('backend helper must be a fresh distinct source-built copy')
    folder.mkdir()
    for name,source_name in sorted(selected.items()):
        source=Path(source_name);target=folder/name
        if source.is_symlink() or source.is_junction() or source.stat().st_nlink!=1:
            raise ValueError('backend helper source must be an ordinary single-link file')
        before=_sha256(source)
        shutil.copyfile(source,target)
        if (_sha256(target)!=before or _sha256(source)!=before or target.stat().st_nlink!=1
                or same_existing_file(source,target)):
            raise ValueError('backend helper copy differs from its selected interpreter')
    if 'python.exe' not in selected:raise ValueError('backend helper requires the selected python.exe')
    config=portable/HELPER_CONFIG_NAME
    config.write_bytes(HELPER_CONFIG_TEMPLATE)
    descriptor=node/'application-manifest.json'
    if descriptor.exists():raise ValueError('backend portable application descriptor already exists')
    descriptor.write_text(json.dumps(connect.manifest('{app}/helper/python.exe'),sort_keys=True)+'\n',encoding='utf-8',newline='\n')
    return dict(name=HELPER_CONFIG_NAME,template=dict(bytes=len(HELPER_CONFIG_TEMPLATE),
                sha256=hashlib.sha256(HELPER_CONFIG_TEMPLATE).hexdigest()))


def _stage_shared_torch_pth(portable: Path, shared: dict) -> dict:
    """Stage the inert oa-pytorch.pth template and its manifest profile.

    Setup replaces the comment with the installed component's site-packages
    path before the launch helper seals the backend tree.
    """
    target = portable / SHARED_TORCH_PTH
    if target.exists():
        raise ValueError("backend already carries an oa-pytorch.pth")
    target.write_bytes(SHARED_TORCH_PTH_TEMPLATE)
    return dict(name=SHARED_TORCH_PTH, release=shared["release"], target_family=shared["target_family"],
                template=dict(bytes=len(SHARED_TORCH_PTH_TEMPLATE),
                              sha256=hashlib.sha256(SHARED_TORCH_PTH_TEMPLATE).hexdigest()))


_GRAPH_FILES={'oa_appmodel_bridge.py','web/oa_appmodel.js','web/oa_appmodel_graph.mjs'}


def _verify_native_graph_wrapper(manifest: dict) -> None:
    """The wrapper carries the whole graph bridge or none of it. The page uses
    ComfyUI's extension surface, so no frontend version or module is pinned."""
    selected=_GRAPH_FILES.intersection(manifest.get('wrapper_files',{}))
    if selected and selected!=_GRAPH_FILES:raise ValueError('native graph wrapper is incomplete')
    if 'native_graph_frontend' in manifest:raise ValueError('native graph frontend pins are retired')


def _verify_integration_wrapper(node: Path, expected: object) -> None:
    required={'__init__.py','oa_connect.py','oa_interpreter.py','oa_helper.py','oa_relay.py',
              'oa_nodes/__init__.py','oa_client/abstraction/ipc/__init__.py','web/oa_connect.js',
              'oa_atomic/abstraction_cas.py'}
    if not isinstance(expected,dict) or not required<=set(expected):
        raise ValueError('OA backend requires its complete authenticated wrapper')
    files,_,_,_=_scan_tree(node.parent,node.name,MAX_EXPANDED_BYTES,selected_root=True)
    actual={name.split('/',1)[1]:dict(bytes=path.stat().st_size,sha256=_sha256(path)[1]) for name,path in files}
    if actual!=expected:raise ValueError('OA integration wrapper files differ from manifest')
    for name,pin in expected.items():
        if (not isinstance(name,str) or not isinstance(pin,dict) or set(pin)!={'bytes','sha256'}
                or type(pin['bytes']) is not int or pin['bytes']<0
                or not isinstance(pin['sha256'],str) or not re.fullmatch('[0-9a-f]{64}',pin['sha256'])):
            raise ValueError('OA integration wrapper pin is invalid')


def _apply_integration(portable: Path, archive_path: Path, work: Path, *, install_wheels: bool,
                       archive_size: int, archive_sha256: str, archive_name: str | None = None,
                       producer_receipt_path: Path | None = None,
                       wheel_receipt_path: Path | None = None,
                       expected_redist_source_commit: str | None = None,
                       expected_run_id: int | None = None,
                       expected_run_attempt: int | None = None,
                       require_wrapper: bool = False, require_public: bool = False,
                       private_context: dict | None = None, python_executable: Path | None = None) -> dict[str, object]:
    """Apply the one reviewed OA integration ZIP during the release build."""
    portable, work = _extended(portable), _extended(work)
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
    if private_context is not None:
        _private_integration_context(private_context)
        if require_public or public:
            raise ValueError('private developer context cannot admit public integration')
    if require_public and not public:
        raise ValueError('AMD backend requires the selected public-split OA integration')
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
          or manifest.get("source_commit") != (private_context['source_commit'] if private_context else INTEGRATION_SOURCE_COMMIT)):
        raise ValueError("OA integration manifest differs from pinned private source")
    if manifest.get("wheels") is None:
        raise ValueError("OA integration manifest has no wheels")
    if private_context is not None:
        _private_appmodel_selected(manifest['wheels'], private_context)
    native_wheels = [wheel for wheel in manifest["wheels"] if wheel["filename"].startswith("abstraction_ipc-")]
    native_commit = (native_wheels[0].get("commit") if public and len(native_wheels) == 1 else
                     native_wheels[0].get("source_commit") if len(native_wheels) == 1 else None)
    expected_native = private_context['native_source_commit'] if private_context else INTEGRATION_NATIVE_COMMIT
    if len(native_wheels) != 1 or (not public and native_commit != expected_native):
        raise ValueError("OA integration native wheel source differs from pin")
    wheel_names: set[str] = set()
    for wheel in manifest["wheels"]:
        filename = wheel["filename"]
        _safe_name(filename)
        wheel_path = unpacked / "wheels" / filename
        size, digest = _sha256(wheel_path)
        if size != wheel["size"] or digest != wheel["sha256"]:
            raise ValueError("OA integration wheel differs from lock")
        if 'native_library_sha256' in wheel and not public:
            component = filename.startswith('abstraction_facade_native-')
            selected_source = (private_context['source_commit'] if component and private_context else expected_native)
            if component and private_context is None or wheel.get('source_commit') != selected_source:
                raise ValueError('OA native wheel source differs from pin')
        if private_context is not None:
            _verify_private_integration_wheel(wheel_path, wheel, private_context)
        wheel_names.add(filename)
    if wheel_names != {path.name for path in (unpacked / "wheels").glob("*.whl")}:
        raise ValueError("OA integration wheelhouse differs from manifest")
    required = (unpacked / "requirements.lock").read_text(encoding="utf-8").splitlines()
    if len(required) != len(manifest["wheels"]) or any(" --hash=sha256:" not in line for line in required):
        raise ValueError("OA integration requirements lock is incomplete")
    node = unpacked / "node" / "abstraction_downloads"
    if not (node / "__init__.py").is_file():
        raise ValueError("OA integration lacks its reviewed ComfyUI node")
    if require_wrapper:
        _verify_integration_wrapper(node,manifest.get('wrapper_files'))
    _verify_native_graph_wrapper(manifest)
    destination = portable / "ComfyUI" / "custom_nodes" / "abstraction_downloads"
    if destination.exists():
        raise ValueError("official portable already contains the OA node")
    shutil.copytree(node, destination)
    if install_wheels:
        python = python_executable if python_executable is not None else _plain(portable / "python_embeded" / "python.exe")
        if not python.is_file():
            raise ValueError("official portable lacks its private Python")
        env = _private_python_env(work)
        subprocess.run([str(python), "-B", "-s", "-m", "pip", "install", "--no-index", "--find-links", str(_plain(unpacked / "wheels")),
                        "--require-hashes", "--no-compile", "-r", str(_plain(unpacked / "requirements.lock"))],
                       check=True, env=env, cwd=_plain(work), timeout=900)
        for record in portable.rglob("direct_url.json"):
            if record.parent.name.casefold().startswith("abstraction_"):
                raise ValueError("OA wheel installation recorded a direct source URL")
            if str(_plain(work)).casefold() in record.read_text(encoding="utf-8", errors="replace").casefold():
                raise ValueError("private Python installation recorded a temporary build path")
        site_packages = portable / "python_embeded" / "Lib" / "site-packages"
        for path_file in site_packages.glob("*.pth"):
            if str(_plain(work)).casefold() in path_file.read_text(encoding="utf-8", errors="replace").casefold():
                raise ValueError("private Python path file recorded a temporary build path")
        subprocess.run([str(python), "-B", "-s", "-c", "from abstraction.ipc import Cancellation, Library; "
                        "library = Library(); assert library._version() == 1; "
                        "signal = Cancellation(library); signal.signal(); signal.close()"],
                       check=True, env=env, cwd=_plain(work), timeout=60)
    return {"sha256": archive_sha256, "bytes": archive_size,
            "source_commit": manifest["adopter_source"]["commit"] if public else (private_context['source_commit'] if private_context else INTEGRATION_SOURCE_COMMIT),
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
            launcher_inputs = _launcher_inputs()
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
            frontend_metadata = json.loads(subprocess.check_output(["git", "-C", str(frontend_checkout), "show", "HEAD:package.json"], timeout=30, env=source_git_environment()))
            if frontend_metadata.get("version") != expected_frontend_version:
                raise ValueError("frontend source version differs from bundled package")
            frontend_license = subprocess.check_output(["git", "-C", str(frontend_checkout), "show", "HEAD:LICENSE"], timeout=30, env=source_git_environment())
            source_stage = work / "source.zip"
            subprocess.run(["git", "-C", str(checkout), "archive", "--format=zip", f"--prefix=ComfyUI-{expected_commit}/", "-o", str(source_stage), "HEAD"], check=True, timeout=300, env=source_git_environment())
            with zipfile.ZipFile(source_stage, "a", allowZip64=True) as combined:
                _append_git_source(combined, frontend_checkout, "ComfyUI_frontend", expected_frontend_commit, work)
                for project, supplied in sorted((additional_source_checkouts or {}).items()):
                    commit, _ = source_pins[project]
                    supplied = supplied.resolve(strict=True)
                    if _git_head(supplied) != commit:
                        raise ValueError(f"{project} source checkout differs from pinned commit")
                    _append_git_source(combined, supplied, project, commit, work)
                if cpu is not None:
                    for name, data in _launcher_inputs().items():
                        _write_zip_file(combined, "OA-Launcher-Source/" + name, data)
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
                            "sha256": _sha256(_extended(extracted / archive_root / LAUNCHER_NAME))[1],
                            "interpreter": {"name": "oa_interpreter.py",
                                            "sha256": _sha256(_extended(extracted / archive_root / "oa_interpreter.py"))[1]},
                            "bootstrap_sources": {name: {"bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()}
                                                  for name, data in _launcher_inputs().items() if name != LAUNCHER_NAME}}
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
        if cpu is not None:
            _check_launcher(extracted / archive_root, launcher_inputs)
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


BACKEND_BUILDER_COMMIT = "8ffa11095bf56eeef5d1697e0cf7d2a8e2c08265"
BACKEND_COMFY_COMMIT = "63be2f9b82ad0dac88a7ceaa28a9cf3154125a18"
BACKEND_BUILDER_SHA256 = "9862d7b9562da554b7cc702cae58b37ec42184bf2be0c9da6ee1ff38173292fe"
BACKEND_POLICY = Path(__file__).resolve().parents[1] / "adopters/comfyui/upstream/backend-amd-v1.json"
# ComfyUI's site-packages names the shared PyTorch engine component's
# site-packages in this file. Setup writes the line at install time; the
# backend ships the inert comment template the launch helper replaces.
SHARED_TORCH_PTH = "standalone-env/Lib/site-packages/oa-pytorch.pth"
SHARED_TORCH_PTH_TEMPLATE = b"# OpenAbstractions Setup names the shared PyTorch engine component here.\n"
# Torch, its vision/audio companions and every ROCm wheel belong to the shared
# PyTorch engine component. ComfyUI's environment never carries them.
_SHARED_TORCH_WHEEL = re.compile(r"(torch|torchvision|torchaudio|amd-torch(vision|audio)?-device-.+"
                                 r"|rocm|rocm-.+|(pytorch-)?triton(-rocm)?)")


def shared_torch_policy() -> dict:
    """The shared PyTorch component ComfyUI's backend runs its torch from."""
    shared = json.loads(BACKEND_POLICY.read_bytes())["shared_pytorch"]
    if (set(shared) != {"component", "release", "target_family", "provides", "absent"}
            or set(shared["provides"]) != {"torch", "torchvision"}):
        raise ValueError("shared PyTorch policy differs from the reviewed recipe")
    return shared


def shared_torch_distribution(name: str) -> bool:
    """Whether a distribution belongs to the shared PyTorch engine component."""
    return bool(_SHARED_TORCH_WHEEL.fullmatch(re.sub(r"[-_.]+", "-", name).lower()))


def _shared_torch_absence(line: str) -> bool:
    """Whether a pip check line names only a distribution the shared component provides."""
    match = re.fullmatch(r"\S+ \S+ requires ([A-Za-z0-9._-]+), which is not installed\.", line)
    return bool(match) and shared_torch_distribution(match.group(1))


def _backend_closure(python_version: str, rows: list) -> dict:
    shared = shared_torch_policy()
    return dict(python_version=python_version, shared=shared["provides"], absent=shared["absent"], wheels=rows)


def backend_limits():
    """Reviewed recipe budget; generic/legacy package budgets remain independent."""
    return app_pair_receipt.backend_policy_limits(json.loads(BACKEND_POLICY.read_bytes()))


def backend_producer_floor(candidate_source=None, run_id=None, run_attempt=None):
    """Disposable capacity belongs to explicit public source/run context.

    This changes the producer's working-space floor, never installation policy.
    The existing integration owner still authenticates matching receipt bytes
    before any backend build. Environment flags establish no provenance.
    """
    if candidate_source is None and run_id is None and run_attempt is None:
        return backend_limits()['reserve_bytes']
    if (not isinstance(candidate_source,str) or not re.fullmatch(r'[0-9a-f]{40}',candidate_source)
            or type(run_id) is not int or run_id<=0 or type(run_attempt) is not int or run_attempt<=0):
        raise ValueError('backend producer requires actual source/run/attempt')
    return 1<<30


def _outer_wheel_metadata(archive):
    names = [name for name in archive.namelist()
             if name.count('/') == 1 and name.endswith('.dist-info/METADATA')]
    if len(names) != 1: raise ValueError('wheel has ambiguous outer metadata')
    return names[0]


def _backend_parts(name: str) -> list[str]:
    if not isinstance(name, str) or "\\" in name or any(ord(char) < 32 for char in name):
        raise ValueError("unsafe backend input path")
    parts = name.split("/")
    for part in parts:
        _safe_name(part)
    return parts


def _backend_file(root: Path, name: str) -> Path:
    parts = _backend_parts(name)
    path = root
    for part in parts:
        path = path / part
        info = path.lstat()
        if stat.S_ISLNK(info.st_mode) or getattr(info, "st_file_attributes", 0) & 0x400:
            raise ValueError("linked backend input")
    if not path.is_file():
        raise ValueError("backend input is not a file")
    return path


def _validate_backend_closure(closure, roots):
    """Validate a frozen Windows CPython wheel map; never select packages.

    closure names the target interpreter version, the frozen wheel rows, the
    distributions the shared PyTorch engine component provides and the
    requirements ComfyUI runs without. A requirement on a provided
    distribution must admit the component's version and adds no wheel.
    """
    from pip._vendor.packaging.requirements import Requirement
    from pip._vendor.packaging.markers import Variable
    from pip._vendor.packaging.utils import canonicalize_name
    from pip._vendor.packaging.version import Version
    from pip._vendor.packaging.specifiers import SpecifierSet

    python = closure['python_version']
    target = dict(os_name='nt', sys_platform='win32', platform_system='Windows',
                  platform_machine='AMD64', implementation_name='cpython',
                  platform_python_implementation='CPython',
                  python_version='.'.join(python.split('.')[:2]),
                  python_full_version=python, implementation_version=python)
    provided = {canonicalize_name(name): Version(version) for name, version in closure['shared'].items()}
    absent = {canonicalize_name(name) for name in closure['absent']}
    selected = {}
    for row in closure['wheels']:
        name = canonicalize_name(row['name'])
        if name in provided or name in absent:
            raise ValueError('the shared PyTorch engine component owns ' + name)
        if name in selected:
            raise ValueError('duplicate frozen distribution: ' + name)
        version = Version(row['version'])
        if row['requires_python'] and Version(python) not in SpecifierSet(row['requires_python']):
            raise ValueError('incompatible target Requires-Python: ' + name)
        selected[name] = (version, row['requires_dist'],
                          {canonicalize_name(extra) for extra in row['provides_extra']})

    def marker_variables(tree):
        # packaging owns the grammar. Refuse a new/unknown AST shape explicitly.
        if isinstance(tree, list):
            result = set()
            for node in tree:
                result.update(marker_variables(node))
            return result
        if isinstance(tree, tuple) and len(tree) == 3:
            return {node.value for node in (tree[0], tree[2]) if isinstance(node, Variable)}
        if tree in ('and', 'or'):
            return set()
        raise ValueError('unsupported packaging marker representation')

    pending = [(name, '') for name in selected]
    visited = set()

    def require(text, extra=''):
        requirement = Requirement(text)
        if requirement.url:
            raise ValueError('URL requirement outside frozen closure: ' + requirement.name)
        if requirement.marker:
            variables = marker_variables(requirement.marker._markers)
            unsupported = variables - (set(target) | {'extra'})
            if unsupported:
                raise ValueError('unsupported target marker: ' + ','.join(sorted(unsupported)))
            if not requirement.marker.evaluate(dict(target, extra=extra)):
                return
        name = canonicalize_name(requirement.name)
        if name in provided:
            # The component's own closure carries its extras and ROCm wheels.
            if not requirement.specifier.contains(provided[name], prereleases=True):
                raise ValueError('shared PyTorch component version outside requirement: ' + text)
            return
        if name in absent:
            return
        if name not in selected or selected[name][0] not in requirement.specifier:
            raise ValueError('requirement outside frozen wheel closure: ' + name)
        for requested in requirement.extras:
            requested = canonicalize_name(requested)
            if requested not in selected[name][2]:
                raise ValueError('undeclared frozen extra: ' + name + '[' + requested + ']')
            pending.append((name, requested))

    for text in roots:
        text = text.split('#', 1)[0].strip()
        if text:
            require(text)
    while pending:
        name, extra = pending.pop()
        if (name, extra) in visited:
            continue
        visited.add((name, extra))
        for text in selected[name][1]:
            require(text, extra)



def _backend_input_descriptor(descriptor: Path, expected_sha256: str) -> tuple[dict, dict]:
    """Admit the complete pinned metadata independently of selected material."""
    limits = backend_limits()
    if descriptor.stat().st_size > 4 << 20:
        raise ValueError("backend descriptor exceeds bounded metadata size")
    if (descriptor.name != "backend-inputs.json" or not re.fullmatch(r"[0-9a-f]{64}", expected_sha256 or "")
            or _sha256(descriptor)[1] != expected_sha256):
        raise ValueError("backend descriptor differs from selected source input")
    _backend_file(descriptor.parent, descriptor.name)
    value = json.loads(descriptor.read_bytes())
    if (not isinstance(value, dict) or set(value) != {"schema", "profile", "builder_commit", "comfyui_commit", "files", "wheels",
                       "python_files", "comfyui_files", "source_files", "uv", "runtime_provenance"}
            or type(value['schema']) is not int or value["schema"] != 1 or value["profile"] != "win-amd-manager-free-v1"
            or value["builder_commit"] != BACKEND_BUILDER_COMMIT or value["comfyui_commit"] != BACKEND_COMFY_COMMIT):
        raise ValueError("unsupported backend input descriptor")
    if (any(not isinstance(value[key], list) for key in ('files','wheels','python_files','comfyui_files','source_files'))
            or not isinstance(value['runtime_provenance'], dict)
            or not isinstance(value['uv'], str)
            or any(any(not isinstance(name, str) for name in value[key])
                   for key in ('python_files','comfyui_files','source_files'))):
        raise ValueError('incomplete backend descriptor collections')
    pins = {}
    seen_files = set()
    for row in value["files"]:
        if (not isinstance(row, dict) or set(row) != {"name", "bytes", "sha256"}
                or not isinstance(row['name'], str) or row["name"] in pins
                or type(row["bytes"]) is not int or row["bytes"] < 0
                or not isinstance(row['sha256'], str) or not re.fullmatch(r"[0-9a-f]{64}", row["sha256"])):
            raise ValueError("incomplete backend file pin")
        _backend_parts(row['name'])
        _register_name(row["name"], seen_files)
        pins[row['name']] = row
    if sum(row["bytes"] for row in value["files"]) > limits['expanded_bytes']:
        raise ValueError("backend inputs exceed existing expanded-byte ceiling")
    if pins.get('builder/scripts/build_local.py', {}).get('sha256') != BACKEND_BUILDER_SHA256:
        raise ValueError("backend builder differs from reviewed upstream")
    referenced = {"builder/scripts/build_local.py", value["uv"]}
    for key, prefix in (("python_files", "python/"), ("comfyui_files", "ComfyUI/")):
        names = value[key]
        if not names or len(set(names)) != len(names) or any(not name.startswith(prefix) for name in names):
            raise ValueError("incomplete backend tree roster")
        referenced.update(names)
    if ("python/python.exe" not in value["python_files"] or "ComfyUI/main.py" not in value["comfyui_files"]
            or "ComfyUI/requirements.txt" not in value["comfyui_files"]):
        raise ValueError("backend lacks actual standalone Python or ComfyUI entry")
    if any(name.count('/') == 1 and name.casefold().endswith('._pth') for name in value['python_files']):
        raise ValueError("standalone backend cannot inherit embedded Python path policy")
    referenced.update(value["source_files"])
    if (not value["source_files"] or len(set(value['source_files'])) != len(value['source_files'])
            or any(not name.startswith("sources/") for name in value["source_files"])):
        raise ValueError("backend needs corresponding source and licences")
    if set(value["runtime_provenance"]) != {"python", "uv", "builder", "comfyui"}:
        raise ValueError("backend runtime/source provenance is incomplete")
    for kind, record in value["runtime_provenance"].items():
        if (not isinstance(record, dict) or set(record) != {"source", "license", "provenance", "version"}
                or any(not isinstance(record[key], str) or not record[key] for key in record)):
            raise ValueError("backend runtime provenance is incomplete")
        if any(record[key] not in value["source_files"] for key in ("source", "license", "provenance")):
            raise ValueError("backend runtime lacks corresponding source/licence/provenance")
    policy = json.loads(BACKEND_POLICY.read_bytes())
    if (value["runtime_provenance"]["python"]["version"] != policy["python_version"]
            or value["runtime_provenance"]["uv"]["version"] != "0.13.4"
            or value["runtime_provenance"]["builder"]["version"] != BACKEND_BUILDER_COMMIT
            or value["runtime_provenance"]["comfyui"]["version"] != BACKEND_COMFY_COMMIT):
        raise ValueError("backend runtime versions differ from reviewed recipe")
    distributions = {}
    for wheel in value["wheels"]:
        if (not isinstance(wheel, dict) or set(wheel) != {"file", "distribution", "version", "source", "license", "provenance"}
                or any(not isinstance(wheel[key], str) for key in wheel)):
            raise ValueError("incomplete backend wheel provenance")
        if (not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", wheel["distribution"])
                or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.+!_-]*", wheel["version"])):
            raise ValueError("unsafe frozen wheel requirement")
        dist = re.sub(r"[-_.]+", "-", wheel["distribution"]).lower()
        if "manager" in dist or dist in distributions or not wheel["version"]:
            raise ValueError("Manager or duplicate distribution in backend closure")
        if shared_torch_distribution(dist):
            raise ValueError("ComfyUI's environment refuses " + dist + "; the shared PyTorch engine component provides torch and ROCm")
        if not wheel["file"].startswith("wheels/") or not wheel["file"].endswith(".whl"):
            raise ValueError("backend accepts frozen wheels only")
        for key in ("source", "license", "provenance"):
            if wheel[key] not in value["source_files"]:
                raise ValueError("wheel lacks corresponding source/licence/provenance")
        referenced.update(wheel[key] for key in ("file", "source", "license", "provenance"))
        distributions[dist] = wheel["version"]
    minor = "".join(policy["python_version"].split(".")[:2])
    if "python/python" + minor + ".dll" not in value["python_files"]:
        raise ValueError("backend requires the exact Windows CPython " + policy["python_version"] + " layout")
    if referenced != set(pins):
        raise ValueError("backend file roster has unknown or missing members")
    return value, pins


def _receive_backend_files(descriptor: Path, pins: dict, selected) -> dict[str, Path]:
    files = {}
    for name in selected:
        path = _backend_file(descriptor.parent, name)
        row = pins[name]
        if _sha256(path) != (row['bytes'], row['sha256']):
            raise ValueError('backend input differs from frozen closure: ' + name)
        files[name] = path
    return files


def _backend_python_policy(value, files):
    """The selected standalone/bootstrap policy applies to both receiving purposes."""
    policy = json.loads(BACKEND_POLICY.read_bytes())
    pip_metadata = [name for name in value["python_files"] if re.fullmatch(r"python/Lib/site-packages/pip-[^/]+\.dist-info/METADATA", name)]
    if len(pip_metadata) != 1:
        raise ValueError("backend requires a pinned bootstrap pip")
    pip_version = Parser().parsestr(files[pip_metadata[0]].read_text(encoding="utf-8"))["Version"]
    if policy.get('bootstrap_pip_version') != '26.2.2' or pip_version != policy['bootstrap_pip_version']:
        raise ValueError("backend bootstrap pip differs from the source-selected security correction")
    for name in value["python_files"]:
        if re.fullmatch(r'python/Lib/site-packages/[^/]+\.dist-info/METADATA', name):
            metadata = Parser().parsestr(files[name].read_text(encoding="utf-8"))
            dist = re.sub(r"[-_.]+", "-", metadata["Name"] or "").lower()
            if "manager" in dist or dist != "pip":
                raise ValueError("backend bootstrap contains a preinstalled non-pip distribution")


def _native_control_python_inputs(descriptor: Path, expected_sha256: str) -> tuple[dict, dict[str, Path]]:
    """Receive only the complete control interpreter and its declared provenance.

    This supplies no receiving result for unconsumed backend material. The full
    backend entry point always receives every pinned payload and wheel RECORD.
    """
    value, pins = _backend_input_descriptor(descriptor, expected_sha256)
    python = value['runtime_provenance']['python']
    selected = set(value['python_files']) | {python[key] for key in ('source','license','provenance')}
    selected.add('builder/scripts/build_local.py')
    files = _receive_backend_files(descriptor, pins, sorted(selected))
    _backend_python_policy(value, files)
    _backend_bootstrap_omissions(files['python/python.exe'].parent, admitted=set(value['python_files']))
    return value, files


def backend_inputs(descriptor: Path, expected_sha256: str) -> tuple[dict, dict[str, Path]]:
    """Receive the complete source-selected backend closure and every wheel RECORD.

    Its caller obtains expected_sha256 from immutable producer inputs. Source,
    licence and provenance are required for every runtime and wheel; security
    and execution approval remain with the selected owning policy.
    """
    value, pins = _backend_input_descriptor(descriptor, expected_sha256)
    files = _receive_backend_files(descriptor, pins, pins)
    limits = backend_limits()
    closure_metadata = []
    expanded_wheels = 0
    for wheel in value['wheels']:
        dist = re.sub(r"[-_.]+", "-", wheel['distribution']).lower()
        with zipfile.ZipFile(files[wheel["file"]]) as archive:
            members = archive.infolist()
            names = set()
            for entry in members:
                for part in entry.filename.rstrip("/").split("/"):
                    _safe_name(part)
                _register_name(entry.filename, names)
                if stat.S_ISLNK(entry.external_attr >> 16):
                    raise ValueError("linked backend wheel member")
            expanded_wheels += sum(entry.file_size for entry in members)
            if expanded_wheels + sum(files[name].stat().st_size for name in value["python_files"] + value["comfyui_files"]) > limits['expanded_bytes']:
                raise ValueError("backend wheel exceeds expanded-byte ceiling")
            metadata = [_outer_wheel_metadata(archive)]
            if archive.getinfo(metadata[0]).file_size > 4 << 20:
                raise ValueError("backend wheel metadata exceeds bounded size")
            parsed = Parser().parsestr(archive.read(metadata[0]).decode("utf-8"))
            if (re.sub(r"[-_.]+", "-", parsed["Name"]).lower() != dist or parsed["Version"] != wheel["version"]):
                raise ValueError("wheel metadata differs from frozen closure")
            closure_metadata.append(dict(name=parsed['Name'], version=parsed['Version'],
                requires_dist=parsed.get_all('Requires-Dist', []),
                provides_extra=parsed.get_all('Provides-Extra', []),
                requires_python=parsed.get('Requires-Python', '')))
            record = metadata[0].removesuffix("METADATA") + "RECORD"
            if archive.getinfo(record).file_size > 4 << 20:
                raise ValueError("backend wheel RECORD exceeds bounded metadata size")
            rows = list(csv.reader(io.StringIO(archive.read(record).decode("utf-8"))))
            recorded = set()
            for row in rows:
                if len(row) != 3 or row[0] in recorded:
                    raise ValueError("invalid backend wheel RECORD")
                recorded.add(row[0])
                if row[0] == record and row[1:] == ["", ""]:
                    continue
                info = archive.getinfo(row[0])
                checksum = hashlib.sha256()
                with archive.open(info) as stream:
                    for block in iter(lambda: stream.read(1 << 20), b""): checksum.update(block)
                digest = "sha256=" + base64.urlsafe_b64encode(checksum.digest()).decode().rstrip("=")
                if row[1] != digest or row[2] != str(info.file_size):
                    raise ValueError("backend wheel RECORD differs from member")
            if recorded != {entry.filename for entry in members if not entry.is_dir()}:
                raise ValueError("backend wheel RECORD is not complete")
    _validate_backend_closure(_backend_closure(value['runtime_provenance']['python']['version'], closure_metadata),
                              files['ComfyUI/requirements.txt'].read_text(encoding='utf-8').splitlines())
    _backend_python_policy(value, files)
    return value, files


def _backend_builder(source: Path, work: Path):
    """Apply the reviewed two-hunk upstream recipe adaptation, without .pyc."""
    policy = json.loads(BACKEND_POLICY.read_bytes())
    patch = BACKEND_POLICY.with_name("backend-amd-v1.patch")
    if _sha256(patch)[1] != policy["patch_sha256"]:
        raise ValueError("backend builder patch differs from source policy")
    original = source.read_text(encoding="utf-8")
    # This is a closed exact-source replacement, not a general patch interpreter.
    import difflib
    patched = original.replace(policy["remove_import"], "")
    start = patched.index("def install_requirements(")
    end = patched.index("\ndef bundle_uv(", start)
    replacement = policy["install_requirements"]
    patched = patched[:start] + replacement + patched[end:]
    actual = "".join(difflib.unified_diff(original.splitlines(True), patched.splitlines(True),
                                        fromfile="a/scripts/build_local.py", tofile="b/scripts/build_local.py"))
    if actual.encode("utf-8") != patch.read_bytes():
        raise ValueError("backend patch does not apply to exact upstream source")
    import inspect
    namespace = {"__name__": "oa_frozen_standalone_builder", "__file__": str(work / "build_local.py"),
                 "_oa_closure_source": inspect.getsource(_validate_backend_closure)}
    exec(compile(patched, namespace["__file__"], "exec"), namespace)
    return namespace


def _backend_wheel_data(archive: zipfile.ZipFile, wheel_name: str) -> dict[str, tuple[int, str]]:
    """Closed spread mapping for the reviewed SymPy non-code document."""
    result = {}
    metadata = _outer_wheel_metadata(archive)
    parsed = Parser().parsestr(archive.read(metadata).decode("utf-8"))
    for entry in archive.infolist():
        parts = entry.filename.split("/")
        if entry.is_dir() or not parts[0].endswith(".data"):
            continue
        if len(parts) > 2 and parts[1] in ("purelib", "platlib"):
            continue
        expected = "sympy-1.14.0.data/data/share/man/man1/isympy.1"
        if (entry.filename != expected or parsed["Name"].lower() != "sympy"
                or parsed["Version"] != "1.14.0" or stat.S_ISLNK(entry.external_attr >> 16)):
            raise ValueError(f"unsupported backend wheel data: {wheel_name}: {entry.filename}")
        record = metadata.removesuffix("METADATA") + "RECORD"
        rows = [row for row in csv.reader(io.StringIO(archive.read(record).decode("utf-8")))
                if row and row[0] == entry.filename]
        if len(rows) != 1 or len(rows[0]) != 3 or not rows[0][1].startswith("sha256="):
            raise ValueError(f"missing backend data RECORD: {wheel_name}: {entry.filename}")
        try:
            digest = base64.b64decode(rows[0][1][7:] + "=", altchars=b"-_", validate=True).hex()
        except ValueError as error:
            raise ValueError(f"invalid backend data RECORD: {wheel_name}: {entry.filename}") from error
        if len(digest) != 64 or rows[0][2] != str(entry.file_size):
            raise ValueError(f"invalid backend data RECORD: {wheel_name}: {entry.filename}")
        result["share/man/man1/isympy.1"] = (entry.file_size, digest)
    return result


def _regular_backend_data(path: Path, root: Path) -> None:
    root, path = _extended(root), _extended(path)
    current = path
    while True:
        status = current.lstat()
        if stat.S_ISLNK(status.st_mode) or getattr(status, "st_file_attributes", 0) & getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0):
            raise ValueError("linked backend data")
        if current == root:
            break
        if current.parent == current:
            raise ValueError("backend data escapes Python root")
        current = current.parent
    if not path.is_file() or path.stat().st_nlink != 1:
        raise ValueError("backend data is not an ordinary owned file")
    relative_beneath_existing(path, root)


def _backend_manifest_bytes(manifest: dict) -> bytes:
    if not 0 < len(manifest["runtime_files"]) <= BACKEND_RUNTIME_ENTRIES:
        raise ValueError("backend runtime entry bound exceeded")
    raw = (json.dumps(manifest, sort_keys=True) + "\n").encode("utf-8")
    if len(raw) > BACKEND_MANIFEST_BYTES:
        raise ValueError("backend manifest exceeds bounded metadata size")
    return raw








# pip 26.2.2 as PBS 20250409 CPython 3.12.10 installs it (--no-compile); the
# input descriptor carries the resulting RECORD and omits the three launchers.
BACKEND_BOOTSTRAP_RECORD_SHA256 = "1be71e364864e622bb0e5dde0625123395921a120a7527702c96bea760a11e9e"
BACKEND_BOOTSTRAP_LAUNCHERS = {
    f"../../Scripts/{name}": ["sha256=ULFCEAF0rfGMdxbyVP77i61Oc8XdVGk2hEZa6lBlVy0", "108461"]
    for name in ("pip.exe", "pip3.exe", "pip3.12.exe")}


def _backend_bootstrap_omissions(python_root: Path, *, admitted: set[str] | None = None) -> dict:
    """Authenticate the selected pip's three deliberately absent console tools.

    The build uses -m pip and removes this distribution after its last install.
    This source-bound exception applies only to the selected backend bootstrap;
    ordinary repack/removal still requires every non-bytecode RECORD member.
    """
    root = _extended(python_root)
    site = root / "Lib/site-packages"
    dist = "pip-26.2.2.dist-info"
    if {p.name for p in site.glob("pip-*.dist-info")} != {dist}:
        raise ValueError("backend bootstrap pip distribution differs from selected 26.2.2")
    folder = site / dist
    header_issues = []
    for name in ("METADATA", "RECORD", "entry_points.txt"):
        try:
            _regular_backend_data(folder / name, root)
        except (OSError, ValueError) as error:
            header_issues.append(f"{dist}/{name}: {error}")
    if header_issues:
        raise ValueError("\n".join(header_issues))
    metadata = Parser().parsestr((folder / "METADATA").read_text(encoding="utf-8"))
    if metadata["Name"] != "pip" or metadata["Version"] != "26.2.2":
        raise ValueError("backend bootstrap pip metadata differs from selected 26.2.2")
    raw = (folder / "RECORD").read_bytes()
    if hashlib.sha256(raw).hexdigest() != BACKEND_BOOTSTRAP_RECORD_SHA256:
        raise ValueError(f"{dist}/RECORD differs from the selected bootstrap record pin")
    parser = configparser.ConfigParser(interpolation=None)
    parser.read_string((folder / "entry_points.txt").read_text(encoding="utf-8"))
    if (parser.sections() != ["console_scripts"] or dict(parser["console_scripts"]) !=
            {"pip": "pip._internal.cli.main:main", "pip3": "pip._internal.cli.main:main"}):
        raise ValueError("backend bootstrap pip console entry points differ from selected source")
    tools = BACKEND_BOOTSTRAP_LAUNCHERS
    found, listed, issues = set(), set(), []
    for row in csv.reader(io.StringIO(raw.decode("utf-8"))):
        if len(row) != 3:
            issues.append(f"{dist}/RECORD: malformed row {row!r}")
            continue
        if row[0] in tools:
            found.add(row[0])
            if row[1:] != tools[row[0]]:
                issues.append(f"{dist}/RECORD: generated launcher pin differs: {row[0]}")
                continue
            target = root / "Scripts" / row[0].rsplit("/", 1)[1]
            scripts = root / "Scripts"
            if os.path.lexists(scripts) and (not scripts.is_dir() or scripts.is_symlink() or scripts.is_junction()):
                issues.append(f"{dist}/RECORD: linked bootstrap Scripts directory: {row[0]}")
                continue
            if not os.path.lexists(target):
                continue
        else:
            target = site.joinpath(*row[0].split("/"))
        if row[0].endswith(".pyc") and not row[1] and not os.path.lexists(target):
            continue
        try:
            _regular_backend_data(target, root)
            relative = "python/" + relative_beneath_existing(target, root).as_posix()
            listed.add(relative)
            if admitted is not None and relative not in admitted:
                raise ValueError("file is outside selected bootstrap input roster")
            if row[1]:
                size, digest = _sha256(target)
                expected = "sha256=" + base64.urlsafe_b64encode(bytes.fromhex(digest)).decode().rstrip("=")
                if row[1:] != [expected, str(size)]:
                    raise ValueError("file differs from RECORD hash/size")
        except (OSError, ValueError) as error:
            issues.append(f"{dist}/RECORD: {row[0]}: {error}")
    if found != set(tools):
        issues.append(f"{dist}/RECORD: generated launcher set differs from selected source")
    if admitted is not None:
        code = _launcher().CODE_SUFFIXES
        for name in sorted(admitted - listed):
            if name.startswith(("python/Lib/site-packages/", "python/Scripts/")) and Path(name).suffix.lower() in code:
                issues.append(f"bootstrap code is not bound by its RECORD: {name}")
    if issues:
        raise ValueError("\n".join(issues))
    return {(dist, name): pin for name, pin in tools.items()}


def _backend_finalization_preflight(value: dict, files: dict) -> None:
    """Check selected wheel spread data before bulk staging or target dispatch.

    Pip-generated paths/sizes remain unknown until installation. The final
    complete runtime roster and serialized manifest retain their own bounds.
    """
    if "python_files" in value:
        _backend_bootstrap_omissions(files["python/python.exe"].parent, admitted=set(value["python_files"]))
    owners = set()
    for wheel in value["wheels"]:
        with zipfile.ZipFile(files[wheel["file"]]) as archive:
            data = _backend_wheel_data(archive, wheel["file"])
            if owners.intersection(data):
                raise ValueError(f"duplicate backend data ownership: {wheel}")
            owners.update(data)


def _discard_backend_entrypoints(portable: Path, work: Path, wheels: dict[Path, str]) -> list[str]:
    """Retain authenticated whole-Python data and remove generated launchers.

    The complete standalone root is sealed and relocated with site-packages.
    Only the reviewed wheel-declared manpage may spread outside site-packages;
    generated console/gui scripts are removed with their RECORD rows.
    """
    _require_owned_stage(portable, work)
    site = portable / "python_embeded/Lib/site-packages"
    removed = []
    data_owners = {}
    for wheel, expected in wheels.items():
        if _sha256(wheel)[1] != expected:
            raise ValueError("backend wheel changed before generated-tool removal")
        with zipfile.ZipFile(wheel) as archive:
            info = _outer_wheel_metadata(archive).removesuffix('/METADATA')
            declared_data = _backend_wheel_data(archive, wheel.name)
            entry = info + "/entry_points.txt"
            parser = configparser.ConfigParser(interpolation=None)
            parser.optionxform = str
            if entry in archive.namelist(): parser.read_string(archive.read(entry).decode("utf-8"))
            allowed = {name + ".exe" for group in ("console_scripts", "gui_scripts") if parser.has_section(group)
                       for name in parser[group]}
        record = site / info / "RECORD"
        with record.open(encoding="utf-8", newline="") as stream: rows = list(csv.reader(stream))
        kept = []
        seen_data = set()
        for row in rows:
            if len(row) != 3:
                raise ValueError("invalid backend installed RECORD")
            relative = row[0][6:] if row[0].startswith("../../") else None
            if relative in declared_data:
                if relative in data_owners:
                    raise ValueError(f"duplicate backend data ownership: {wheel.name}: {row[0]}")
                # The exact admitted spread row maps two levels above Lib/site-packages.
                # Construct its canonical root-relative name without extended-path '..'.
                target = portable / "python_embeded" / relative
                _regular_backend_data(target, portable / "python_embeded")
                size, digest = _sha256(target)
                expected_size, expected_digest = declared_data[relative]
                if (size, digest) != (expected_size, expected_digest) or row[1:] != [
                        "sha256=" + base64.urlsafe_b64encode(bytes.fromhex(digest)).decode().rstrip("="), str(size)]:
                    raise ValueError(f"backend data differs from wheel/RECORD: {wheel.name}: {row[0]}")
                data_owners[relative] = wheel.name
                seen_data.add(relative)
                kept.append(row)
                continue
            path = site.joinpath(*row[0].split("/")).resolve()
            if path.is_relative_to(site.resolve()):
                kept.append(row)
                continue
            name = row[0].removeprefix("../../Scripts/")
            if not row[0].startswith("../../Scripts/") or name not in allowed:
                raise ValueError(f"backend wheel has non-site runtime data requiring explicit review: {wheel.name}: {row[0]}")
            _safe_name(name)
            target = portable / "python_embeded/Scripts" / name
            if not target.is_file() or target.is_symlink() or target.is_junction():
                raise ValueError("backend generated entrypoint is missing or linked")
            raw = target.read_bytes()
            digest = "sha256=" + base64.urlsafe_b64encode(hashlib.sha256(raw).digest()).decode().rstrip("=")
            if row[1] != digest or row[2] != str(len(raw)):
                raise ValueError("backend generated entrypoint differs from RECORD")
            target.unlink()
            removed.append(name)
        if set(declared_data) != seen_data:
            raise ValueError(f"backend declared data missing from installed RECORD: {wheel.name}")
        if kept != rows:
            with record.open("w", encoding="utf-8", newline="") as stream:
                csv.writer(stream, lineterminator="\n").writerows(kept)
    return sorted(removed)


BACKEND_MANIFEST_BYTES = app_pair_receipt.BACKEND_RUNTIME_MANIFEST_BYTES
BACKEND_RUNTIME_ENTRIES = 65536


BACKEND_VOLUME_BYTES = 1900 << 20


def finalize_backend_outputs(backend_archive: Path, source_archive: Path, directory: Path,
                             receipt_name: str, *, input_descriptor: dict,
                             candidate_source: str, run_id: int, run_attempt: int,
                             schema: int = 3, private_context: dict | None = None) -> dict:
    """Publish already-created archive DATA through the existing producer owner.

    Schema3 requires a separately admitted 7z producer. This function splits and
    hashes bytes only; it neither invokes an archiver nor certifies its format.
    Schema2 preserves the current built-ZIP path until that tool input exists.
    """
    app_pair_receipt._archive_pin(input_descriptor)
    app_pair_receipt._archive_pin({'name': receipt_name, 'bytes': 1, 'sha256': '0' * 64})
    if private_context is not None:
        _private_integration_context(private_context)
        if candidate_source is not None or run_id is not None or run_attempt is not None or schema != 3:
            raise ValueError('private backend must not claim public producer identity')
    if (type(schema) is not int or schema not in (2, 3)
            or private_context is None and (not isinstance(candidate_source, str) or not re.fullmatch(r'[0-9a-f]{40}', candidate_source)
            or type(run_id) is not int or run_id <= 0 or type(run_attempt) is not int or run_attempt <= 0)
            or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', receipt_name)
            or receipt_name.endswith('.')):
        raise ValueError('invalid backend output context')
    directory = Path(directory).absolute()
    if directory.is_symlink() or directory.resolve() != directory or not directory.is_dir():
        raise ValueError('backend output directory must be ordinary')
    sources = [Path(backend_archive).absolute(), Path(source_archive).absolute()]
    formats = ['7z', 'zip'] if schema == 3 else ['zip', 'zip']
    names = [source.name for source in sources]
    if len({name.casefold() for name in [*names, receipt_name]}) != 3:
        raise ValueError('backend output names alias')
    planned = []
    for source, format in zip(sources, formats):
        app_pair_receipt._ordinary_archive_file(source.parent, source.name)
        size, sha = _sha256(source)
        logical = {'name': source.name, 'bytes': size, 'sha256': sha}
        app_pair_receipt._archive_pin(logical)
        if not source.name.endswith('.' + format):
            raise ValueError('backend output archive format differs')
        if schema == 2:
            if size >= app_pair_receipt.ARCHIVE_PART_LIMIT:
                raise ValueError('legacy backend output exceeds asset ceiling')
            count = 1
        else:
            if size > backend_limits()['archive_bytes']:
                raise ValueError('backend logical archive exceeds recipe budget')
            count = (size + BACKEND_VOLUME_BYTES - 1) // BACKEND_VOLUME_BYTES
            if not 1 <= count <= app_pair_receipt.ARCHIVE_PART_COUNT:
                raise ValueError('backend archive exceeds part-count bound')
        part_names = [source.name] if count == 1 else [source.name + f'.{n:03d}' for n in range(1, count + 1)]
        planned.append((source, logical, format, part_names))
    destinations = [directory / name for _, _, _, names in planned for name in names]
    receipt_path = directory / receipt_name
    if len({p.name.casefold() for p in [*destinations, receipt_path]}) != len(destinations) + 1:
        raise ValueError('backend part names alias')
    if any(path.exists() or path.is_symlink() for path in [*destinations, receipt_path]):
        raise ValueError('backend output already exists')
    created = []
    result = {'schema': schema, 'input_descriptor': dict(input_descriptor),
              'candidate_source': candidate_source, 'run_id': run_id, 'run_attempt': run_attempt}
    if private_context is not None:
        result = {'schema': schema, 'input_descriptor': dict(input_descriptor),
                  'source_mode': 'private-diagnostic', 'private_provenance': dict(private_context)}
    try:
        for key, (source, logical, format, part_names) in zip(('archive', 'source_pair'), planned):
            parts = []
            complete = hashlib.sha256()
            with source.open('rb') as reader:
                for name in part_names:
                    destination = directory / name
                    hashed = hashlib.sha256()
                    size = 0
                    with destination.open('xb') as writer:
                        created.append(destination)
                        remaining = BACKEND_VOLUME_BYTES if len(part_names) > 1 else logical['bytes']
                        while remaining:
                            chunk = reader.read(min(1 << 20, remaining))
                            if not chunk:
                                break
                            writer.write(chunk)
                            hashed.update(chunk); complete.update(chunk)
                            size += len(chunk); remaining -= len(chunk)
                    parts.append({'name': name, 'bytes': size, 'sha256': hashed.hexdigest()})
                if reader.read(1) or complete.hexdigest() != logical['sha256']:
                    raise ValueError('backend source archive changed during split')
            archive = dict(logical, format=format, parts=parts)
            app_pair_receipt.verify_archive_set(directory, archive, format=format)
            result[key] = logical if schema == 2 else archive
        with receipt_path.open('x', encoding='utf-8') as writer:
            created.append(receipt_path)
            json.dump(result, writer, sort_keys=True); writer.write('\n')
        size, sha = _sha256(receipt_path)
        if private_context is None:
            app_pair_receipt.read_backend_producer(receipt_path,
                expected_receipt={'name': receipt_name, 'bytes': size, 'sha256': sha},
                expected_input=input_descriptor, candidate_source=candidate_source,
                run_id=run_id, run_attempt=run_attempt)
        elif json.loads(receipt_path.read_bytes()) != result:
            raise ValueError('private backend receipt changed during finalization')
        return result
    except BaseException:
        for path in reversed(created):
            path.unlink()
        raise


def _archive_tool_inputs(tool: Path | None) -> dict[str, Path]:
    """Admit the source-selected build tool and corresponding-source DATA."""
    if tool is None or not Path(tool).is_absolute():
        raise ValueError('AMD backend requires an explicit absolute --backend-archive-tool; PATH is not used')
    tool = _extended(tool)
    root = tool.parent
    if root.resolve() != root or root.is_symlink() or root.is_junction():
        raise ValueError('linked archive tool directory')
    policy = json.loads(BACKEND_POLICY.read_bytes())['archive_tool']
    if (set(policy) != {'version', 'name', 'bytes', 'sha256', 'url', 'source', 'notices', 'provenance'}
            or policy['version'] != '26.05' or policy['name'] != '7zr.exe' or tool.name != policy['name']
            or set(policy['source']) != {'name', 'bytes', 'sha256', 'url', 'commit'}
            or not isinstance(policy['notices'], list) or len(policy['notices']) != 3):
        raise ValueError('unsupported archive tool source policy')
    records = [{'name': policy['name'], 'bytes': policy['bytes'], 'sha256': policy['sha256']},
               {key: policy['source'][key] for key in ('name', 'bytes', 'sha256')}]
    records += [dict(record, name='notices/' + record['name']) for record in policy['notices']]
    result = {}
    for record in records:
        if (set(record) != {'name', 'bytes', 'sha256'} or type(record['bytes']) is not int
                or not 0 < record['bytes'] < (2 << 30)
                or not isinstance(record['sha256'], str) or not re.fullmatch(r'[0-9a-f]{64}', record['sha256'])):
            raise ValueError('invalid archive tool input pin')
        path = _backend_file(root, record['name'])
        if path.stat().st_nlink != 1 or _sha256(path) != (record['bytes'], record['sha256']):
            raise ValueError('archive tool/source/notice differs from selected policy')
        if record['name'].casefold() in {name.casefold() for name in result}:
            raise ValueError('archive tool inputs alias')
        result[record['name']] = path
    return result


def _windows_archive_environment(temporary: Path) -> dict[str, str]:
    """Bind loader paths to Windows' own directory, independently of ambient env."""
    return windows_build_environment(_plain(temporary))


def backend_archive_metadata(directory, archive, tool):
    """Read fixed manifest DATA only after complete ordered-part verification."""
    limits = backend_limits()
    if archive['bytes'] > limits['archive_bytes']: raise ValueError('backend logical archive exceeds recipe budget')
    app_pair_receipt.verify_archive_set(directory, archive, format='7z')
    _archive_tool_inputs(tool)
    first = Path(directory).absolute() / archive['parts'][0]['name']
    with owned_temporary_tree(prefix='oa-backend-metadata-', parent=first.parent) as work:
        env = _windows_archive_environment(work)
        def command(arguments, limit):
            _archive_tool_inputs(tool)
            with (work/'stdout').open('w+b') as output, (work/'stderr').open('w+b') as error:
                child = subprocess.Popen([str(tool), *arguments], cwd=work, env=env,
                    stdin=subprocess.DEVNULL, stdout=output, stderr=error)
                deadline = time.monotonic() + 30
                try:
                    while child.poll() is None:
                        if (time.monotonic() >= deadline or os.fstat(output.fileno()).st_size > limit
                                or os.fstat(error.fileno()).st_size > 1 << 20):
                            raise ValueError('backend metadata tool exceeds finite bounds')
                        time.sleep(.01)
                    if child.wait() != 0 or os.fstat(output.fileno()).st_size > limit or os.fstat(error.fileno()).st_size > 1 << 20:
                        raise ValueError('backend metadata tool failed or exceeded output bound')
                    _archive_tool_inputs(tool)
                    output.seek(0)
                    return output.read(limit + 1)
                finally:
                    if child.poll() is None: child.kill()
                    child.wait()
        listing = command(['l', '-slt', '-sccUTF-8', str(first)], 16 << 20).decode('utf-8')
        if '----------' not in listing: raise ValueError('backend archive listing unavailable')
        members, folded, expanded = {}, set(), 0
        for block in listing.split('----------', 1)[1].replace('\r\n', '\n').strip().split('\n\n'):
            pairs = [line.split(' = ', 1) for line in block.splitlines() if ' = ' in line]
            fields = dict(pairs)
            if len(fields) != len(pairs): raise ValueError('duplicate backend listing field')
            name = fields.get('Path', '').replace('\\', '/')
            if (not name or any(ord(char) < 32 for char in name)
                    or name.casefold() in folded or name.startswith('/') or ':' in name
                    or any(part in ('', '.', '..') for part in name.split('/'))
                    or fields.get('Encrypted') == '+' or 'Symbolic Link' in fields or 'Hard Link' in fields):
                raise ValueError('unsafe backend archive member')
            members[name] = fields
            folded.add(name.casefold())
            if not fields.get('Size', '').isdigit(): raise ValueError('backend archive member lacks size')
            expanded += int(fields['Size'])
            if expanded > limits['expanded_bytes']:
                raise ValueError('backend archive exceeds existing expanded-byte limit')
        manifest = members.get('manifest.json', {})
        if (manifest.get('Folder') == '+' or not manifest.get('Size', '').isdigit()
                or not 0 < int(manifest['Size']) <= BACKEND_MANIFEST_BYTES):
            raise ValueError('backend manifest exceeds metadata bound')
        raw = command(['x', '-so', str(first), 'manifest.json'], BACKEND_MANIFEST_BYTES)
        if len(raw) != int(manifest['Size']): raise ValueError('backend manifest size differs')
        app_pair_receipt.verify_archive_set(directory, archive, format='7z')
        return set(members), raw


def _pack_backend_archive(portable: Path, archive: Path, tool: Path) -> None:
    # Tool selection and the argv belong to this source owner. No supplier
    # find_7z(), PATH lookup, shell or caller-controlled argument list is used.
    _archive_tool_inputs(tool)
    if archive.exists() or archive.is_symlink():
        raise ValueError('backend archive already exists')
    temporary = archive.parent / 'archive-temp'
    temporary.mkdir()
    env = _windows_archive_environment(temporary)
    # Four-MiB LZMA2 chunks keep eight workers useful inside each bounded solid block.
    subprocess.run([str(_plain(tool)), 'a', '-t7z', '-m0=lzma2:c=4m', '-mx=3', '-mfb=32', '-md=4m',
                    '-ms=64m', '-mf=off', '-mmt=8', str(_plain(archive)), 'standalone-env', 'ComfyUI',
                    'manifest.json', 'requirements-amd-win.txt', 'OA-LICENSES'], cwd=_plain(portable),
                   env=env, check=True, timeout=900)
    if archive.is_symlink() or not archive.is_file():
        raise ValueError('archiver did not produce a 7z archive')
    with archive.open('rb') as stream:
        if stream.read(6) != b'7z\xbc\xaf\x27\x1c':
            raise ValueError('archiver did not produce a 7z archive')


def _require_private_source_revision(root, source):
    owner = _integration_packager_owner()
    if (_git_head(root) != source or owner.untracked_source_files(root, ['.'])
            or not owner.matches_revision(root, source, ['.'])):
        raise ValueError('private developer source differs from selected clean revision')


def _freeze_backend_integration(work,archive,options,expected_size,expected_sha256,*,source,run_id,run_attempt,
                                private_context=None, recovery_controller=None):
    if private_context is not None:
        context = _private_integration_context(private_context)
        if options or source is not None or run_id is not None or run_attempt is not None:
            raise ValueError('private developer integration refuses public receipts and run identity')
        root = Path(__file__).resolve().parents[1]
        owner = _integration_packager_owner()
        if recovery_controller is None:
            _require_private_source_revision(root,context['source_commit'])
        else:
            _require_private_source_revision(root,recovery_controller)
            changed = subprocess.check_output(['git','diff','--name-only',context['source_commit'],'--'],
                                              cwd=root,env=source_git_environment(),text=True).splitlines()
            if set(changed) - {'scripts/package_comfyui_app.py','scripts/test_package_comfyui_app.py'}:
                raise ValueError('local recovery changed original product source')
        if not owner.matches_revision(root, context['native_source_commit'], owner.NATIVE_SOURCES):
            raise ValueError('private developer source differs from selected clean revisions')
        target = work/'selected-integration.zip'
        _snapshot_verified_source(archive, target, expected_size, expected_sha256)
        _check_integration_consistency(target)
        with app_pair_receipt.checked_zip(target, bounded_members=True) as content:
            entry=content.getinfo('manifest.json')
            if entry.file_size > 4<<20: raise ValueError('integration manifest exceeds bound')
            manifest=json.loads(content.read(entry))
            if manifest.get('source_mode') != 'private-diagnostic' or manifest.get('source_commit') != context['source_commit']:
                raise ValueError('private integration differs from selected source')
            wheels=manifest['wheels']
            _private_appmodel_selected(wheels, context)
            native=[wheel for wheel in wheels if wheel['filename'].startswith('abstraction_ipc-')]
            if (len(native)!=1 or native[0].get('source_commit')!=context['native_source_commit']
                    or native[0].get('native_library_sha256')!=context['native_library_sha256']
                    or any(w.get('source_commit')!=context['source_commit'] for w in wheels if w is not native[0])):
                raise ValueError('private integration native/source pins differ')
            expanded=sum(entry.file_size for entry in content.infolist())
            if expanded > 16<<20: raise ValueError('integration exceeds expansion bound')
            wheel_root=work/'private-integration-wheels';wheel_root.mkdir()
            for wheel in wheels:
                _safe_name(wheel['filename'])
                path=wheel_root/wheel['filename']
                path.write_bytes(content.read('wheels/'+wheel['filename']))
                _verify_private_integration_wheel(path,wheel,context)
        return target, {'private_context': context}, manifest, expanded
    required={'producer_receipt_path','wheel_receipt_path','expected_redist_source_commit','expected_run_id','expected_run_attempt'}
    if (not isinstance(options,dict) or set(options)!=required
            or options['expected_redist_source_commit']!=source
            or type(options['expected_run_id']) is not int or options['expected_run_id']!=run_id
            or type(options['expected_run_attempt']) is not int or options['expected_run_attempt']!=run_attempt
            or type(expected_size) is not int or not 0<expected_size<2<<30
            or not isinstance(expected_sha256,str) or not re.fullmatch('[0-9a-f]{64}',expected_sha256)):
        raise ValueError('AMD integration requires its independent public producer identity and archive pin')
    frozen=dict(options)
    for key,name in (('producer_receipt_path','integration-producer.json'),('wheel_receipt_path','integration-wheels.json')):
        path=options[key]
        if not isinstance(path,Path) or not path.is_file() or not 0<path.stat().st_size<=4<<20:
            raise ValueError('AMD integration requires bounded external producer and wheel receipts')
        size,sha=_sha256(path);target=work/name
        _snapshot_verified_source(path,target,size,sha);frozen[key]=target
    receipt=json.loads(frozen['producer_receipt_path'].read_bytes())
    expected=dict(name=archive.name,bytes=expected_size,sha256=expected_sha256)
    actual=receipt.get('integration',{})
    if (type(receipt.get('schema')) is not int or receipt['schema']!=1 or type(receipt.get('run_id')) is not int
            or type(receipt.get('run_attempt')) is not int or receipt.get('candidate_source')!=source
            or receipt['run_id']!=run_id or receipt['run_attempt']!=run_attempt
            or not isinstance(actual,dict) or set(actual)!=set(expected) or type(actual.get('bytes')) is not int
            or actual!=expected or receipt.get('wheel_receipt_sha256')!=_sha256(frozen['wheel_receipt_path'])[1]):
        raise ValueError('AMD integration differs from its external producer receipt')
    snapshot=work/'integration.zip'
    _snapshot_verified_source(archive,snapshot,expected_size,expected_sha256)
    with app_pair_receipt.checked_zip(snapshot,bounded_members=True) as content:
        expanded=sum(row.file_size for row in content.infolist())
        if expanded>16<<20:raise ValueError('OA integration exceeds expanded-byte ceiling')
        manifest=json.loads(content.read('manifest.json'))
    if manifest.get('source_mode')!='public-split':
        raise ValueError('AMD backend requires the selected public-split OA integration')
    return snapshot,frozen,manifest,expanded


def build_backend(descriptor: Path, expected_sha256: str, output: Path, source_output: Path,
                  receipt_output: Path, *, candidate_source: str, run_id: int, run_attempt: int,
                  integration_archive: Path, integration_options: dict,
                  public_sources: dict[str, Path] | None = None, archive_tool: Path | None = None,
                  public_source_root: Path | None = None,
                  expected_integration_size: int | None = None, expected_integration_sha256: str | None = None,
                  minimum_free_after_build_bytes: int = 20 << 30,
                  private_context: dict | None = None) -> dict:
    """Build the upstream standalone layout offline, preserving GPU wheels.

    Source closure admission is independent of the variable output receipt.
    This does not authorize execution of a newly reviewed PyTorch profile.
    """
    limits = backend_limits()
    tool_inputs = _archive_tool_inputs(archive_tool)
    if output.suffix != '.7z' or source_output.suffix != '.zip':
        raise ValueError('multipart AMD output requires backend .7z and corresponding source .zip')
    descriptor = _extended(descriptor)
    value, files = backend_inputs(descriptor, expected_sha256)
    _backend_finalization_preflight(value, files)
    floor=backend_producer_floor(candidate_source,run_id,run_attempt)
    if private_context is not None:
        private_context = _private_integration_context(private_context)
        if candidate_source is not None or run_id is not None or run_attempt is not None or public_sources is not None or public_source_root is not None:
            raise ValueError('private backend refuses public producer/source context')
    elif candidate_source is None:
        raise ValueError("backend producer requires actual source/run/attempt")
    if type(minimum_free_after_build_bytes) is not int or minimum_free_after_build_bytes < floor:
        raise ValueError('private/workstation backend must retain 20 GiB' if private_context is not None else
                         'public disposable backend must retain at least one GiB')
    outputs = [_extended(path) for path in (output, source_output, receipt_output)]
    if len(set(outputs)) != 3 or len({path.parent for path in outputs}) != 1 or any(path.exists() for path in outputs):
        raise ValueError("backend outputs must be new distinct siblings")
    parent = outputs[0].parent
    parent.mkdir(parents=True, exist_ok=True)
    # Existing inputs already occupy disk. Budget the maximum remaining phase;
    # snapshot and installed tree are released only after their last consumers.
    input_bytes = sum(path.stat().st_size for path in files.values()) + descriptor.stat().st_size
    wheel_bytes = (0 if _backend_reuses_snapshot_wheels(value, files) else
                   sum(files[wheel['file']].stat().st_size for wheel in value['wheels']))
    if (type(expected_integration_size) is not int or not 0<expected_integration_size<2<<30):
        raise ValueError('AMD integration requires its independent public producer identity and archive pin')
    # The integration owner permits at most 16 MiB expanded. Budget that bound
    # before the first snapshot; inspect only authenticated private bytes later.
    integration_expanded=16<<20
    # Admit only the bounded integration snapshot first. Public Git archives are
    # captured with their own pre-write bound before the heavy input snapshots.
    required = minimum_free_after_build_bytes + expected_integration_size + (8 << 20)
    if shutil.disk_usage(parent).free < required:
        raise ValueError("insufficient free space for bounded backend build")
    with owned_temporary_tree(prefix="oa-comfy-backend-", parent=parent) as temporary:
        work = _extended(temporary)
        integration_name=integration_archive.name
        integration_archive,integration_options,integration_manifest,integration_expanded=_freeze_backend_integration(
            work,integration_archive,integration_options,expected_integration_size,expected_integration_sha256,
            source=candidate_source,run_id=run_id,run_attempt=run_attempt,
            **({'private_context':private_context} if private_context is not None else {}))
        if public_source_root is not None:
            repositories={integration_manifest['adopter_source']['repository'],integration_manifest['packager_source']['repository'],
                          *(wheel['repository'] for wheel in integration_manifest['wheels'])}
            public_sources={repo:public_source_root/repo.split('/',1)[1] for repo in repositories}
        pins = {row['name']: row for row in value['files']}
        lock = []
        for wheel in value['wheels']:
            lock.append(f"{wheel['distribution']}=={wheel['version']} --hash=sha256:{pins[wheel['file']]['sha256']}")
        lock_raw = ('\n'.join(lock) + '\n').encode('utf-8')
        source_plan = _backend_source_plan(value, files, descriptor, tool_inputs, lock_raw,
            integration_manifest, public_sources, work, minimum_free_after_build_bytes, expected_sha256,
            **({'private_context':private_context} if private_context is not None else {}))
        required = _backend_capacity(input_bytes, wheel_bytes, source_plan, expected_integration_size,
            integration_expanded, sum(path.stat().st_size for path in tool_inputs.values()),
            minimum_free_after_build_bytes)
        # These exact files already occupy disk at this fresh measurement. Their
        # costs remain in the total plan, and are subtracted once from future IO.
        allocated = source_plan['temporary_bytes'] + expected_integration_size + sum(
            integration_options_path.stat().st_size for integration_options_path in
            ((integration_options['producer_receipt_path'], integration_options['wheel_receipt_path']) if private_context is None else ()))
        if shutil.disk_usage(parent).free < required - allocated:
            raise ValueError('insufficient free space for bounded backend build')
        def admit_phase(phase, present):
            required = _backend_capacity(input_bytes, wheel_bytes, source_plan, expected_integration_size,
                integration_expanded, sum(path.stat().st_size for path in tool_inputs.values()),
                minimum_free_after_build_bytes, phase=phase)
            if shutil.disk_usage(parent).free < required - allocated - present:
                raise ValueError('insufficient free space for backend ' + phase + ' phase')
        tool_root = work / 'archive-tool'
        for name, path in tool_inputs.items():
            target = tool_root / name; target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(path, target)
        staged_tool = tool_root / '7zr.exe'
        _archive_tool_inputs(staged_tool)
        snapshot = work / "inputs"
        snapshot.mkdir()
        for name, original in files.items():
            target = snapshot / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(original, target)
        descriptor_snapshot = snapshot / descriptor.name
        descriptor_snapshot.write_bytes(descriptor.read_bytes())
        value, files = backend_inputs(descriptor_snapshot, expected_sha256)
        portable = work / "extract" / "backend"
        portable.mkdir(parents=True)
        for key, prefix, destination in (("python_files", "python/", "python_embeded"), ("comfyui_files", "ComfyUI/", "ComfyUI")):
            for name in value[key]:
                target = portable / destination / name[len(prefix):]
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(files[name], target)
        # Manager is excluded before the first dependency operation, not uninstalled afterwards.
        (portable / "ComfyUI" / "manager_requirements.txt").unlink(missing_ok=True)
        if any("manager" in path.name.casefold() for path in (portable / "ComfyUI/custom_nodes").glob("*")):
            raise ValueError("backend source contains a Manager custom node")
        reuse_wheels = _backend_reuses_snapshot_wheels(value, files)
        wheelhouse = snapshot / "wheels" if reuse_wheels else work / "wheels"
        if not reuse_wheels:
            wheelhouse.mkdir()
        lock = []
        wheel_pins = {}
        for wheel in value["wheels"]:
            original = files[wheel["file"]]
            target = wheelhouse / original.name
            if not reuse_wheels:
                shutil.copyfile(original, target)
            digest = _sha256(target)[1]
            lock.append(f"{wheel['distribution']}=={wheel['version']} --hash=sha256:{digest}")
            wheel_pins[target] = digest
        lock_path = work / "requirements.lock"
        lock_path.write_bytes(lock_raw)
        if ('\n'.join(lock) + '\n').encode('utf-8') != lock_raw:
            raise ValueError('backend wheel lock differs from admitted source plan')
        closure_metadata = []
        for wheel in value['wheels']:
            with zipfile.ZipFile(files[wheel['file']]) as archive:
                name = _outer_wheel_metadata(archive)
                parsed = Parser().parsestr(archive.read(name).decode('utf-8'))
                closure_metadata.append(dict(name=parsed['Name'], version=parsed['Version'],
                    requires_dist=parsed.get_all('Requires-Dist', []),
                    provides_extra=parsed.get_all('Provides-Extra', []),
                    requires_python=parsed.get('Requires-Python', '')))
        lock_path.with_suffix('.json').write_text(json.dumps(_backend_closure(
            value['runtime_provenance']['python']['version'], closure_metadata)), encoding='utf-8')
        builder = _backend_builder(files["builder/scripts/build_local.py"], work)
        if (builder["HOST_OS"] != "win" or builder["platform"].machine() not in
                builder["HOST_ARCHES"][builder["PYTHON_PLATFORMS"]["win-amd"]]):
            raise ValueError("upstream AMD backend recipe requires its Windows x64 build host")
        # The actual upstream installation owner now takes only the complete local wheel lock.
        python_pin = pins['python/python.exe']
        with _selected_python_executable(portable / "python_embeded/python.exe",
                (python_pin['bytes'], python_pin['sha256']), _sha256) as python:
            builder["install_requirements"](_plain(portable), "win-amd", "requirements-amd-win.txt",
                                             _plain(lock_path), _plain(wheelhouse), python, _private_python_env(work))
            if {line for line in _pip_check(python, _private_python_env(work)) if not _shared_torch_absence(line)}:
                raise ValueError("frozen backend wheel closure has unsatisfied dependencies")
            integration_size, integration_hash = expected_integration_size,expected_integration_sha256
            integration = _apply_integration(portable, integration_archive, work, install_wheels=True,
                                             archive_size=integration_size, archive_sha256=integration_hash,
                                             archive_name=integration_name, require_wrapper=True,
                                             require_public=private_context is None, python_executable=python,
                                             **integration_options)
        notices = portable / "OA-LICENSES"
        notices.mkdir()
        (notices / "OpenAbstractions-LICENSE").write_bytes(integration["license"])
        (notices / "OpenAbstractions-NOTICE").write_bytes(integration["notice"])
        license_names = {record["license"] for record in value["runtime_provenance"].values()}
        license_names.update(wheel["license"] for wheel in value["wheels"])
        for index, name in enumerate(sorted(license_names)):
            shutil.copyfile(files[name], notices / f"upstream-{index}-LICENSE")
        site = portable / "python_embeded/Lib/site-packages"
        removed_tools = _discard_backend_entrypoints(portable, work, wheel_pins)
        _fill_wheel_records(site, wheel_pins)
        # Preserve PBS/uv virtualenv semantics. Embedded ._pth isolation is not
        # interchangeable with a genuine Desktop-created .venv. The Desktop
        # spawn owner must use the recorded before-import startup requirement.
        if list((portable / "python_embeded").glob("*._pth")):
            raise ValueError("backend Python already carries an unreviewed path policy")
        seal = _seal_private_python(portable, work, embedded=False, backend_bootstrap=True)
        launcher_target = portable / "ComfyUI" / LAUNCHER_NAME
        launcher_inputs = _stage_launcher(launcher_target.parent)
        if launcher_inputs != source_plan['launcher_inputs']:
            raise ValueError('backend launcher differs from admitted source plan')
        (portable / "python_embeded").rename(portable / "standalone-env")
        shutil.copyfile(files[value["uv"]], portable / "standalone-env/uv.exe")
        helper_python_config=_stage_backend_helper(portable)
        versions = {wheel["distribution"]: wheel["version"] for wheel in value["wheels"]}
        shared_torch = shared_torch_policy()
        # torch_version and torchvision_version name the shared component's
        # versions ComfyUI runs; it runs without torchaudio.
        versions.update(shared_torch["provides"], torchaudio="", uv="0.13.4")
        manifest = builder["build_manifest"]("win-amd", "oa-env1", BACKEND_COMFY_COMMIT,
                    BACKEND_COMFY_COMMIT, versions, "requirements-amd-win.txt",
                    {"vendor": lock_path.read_text(encoding="utf-8"), "comfyui": "", "manager": ""})
        selected_recipe=json.loads(BACKEND_POLICY.read_bytes())
        manifest.update(python_version=selected_recipe['python_version'],pbs_release=selected_recipe['pbs_release'],
                        manager=False, input_descriptor_sha256=expected_sha256, private_python_seal=seal,
                        removed_generated_tools=removed_tools,
                        oa_launcher={"name": "ComfyUI/" + LAUNCHER_NAME, "bytes": launcher_target.stat().st_size,
                                     "sha256": _sha256(launcher_target)[1],
                                     "interpreter": {"name": "ComfyUI/oa_interpreter.py",
                                                     "bytes": len(launcher_inputs["oa_interpreter.py"]),
                                                     "sha256": hashlib.sha256(launcher_inputs["oa_interpreter.py"]).hexdigest()}},
                        oa_integration={"name": integration_name, "bytes": integration_size, "sha256": integration_hash})
        manifest['helper_python_config']=helper_python_config
        manifest['pytorch_component_config']=_stage_shared_torch_pth(portable, shared_torch)
        shutil.copyfile(lock_path, portable / "requirements-amd-win.txt")
        _normalize_runtime_directories(work/'extract','backend',limits['expanded_bytes'])
        runtime_files, _, _, _ = _scan_tree(work / 'extract', 'backend', limits['expanded_bytes'])
        manifest['runtime_files'] = _runtime_file_hashes(runtime_files)
        manifest_raw = _backend_manifest_bytes(manifest)
        (portable / 'manifest.json').write_bytes(manifest_raw)
        staged, _, installed_bytes, _ = _scan_tree(work / "extract", "backend", limits['expanded_bytes'])
        staged_outputs = [work / path.name for path in outputs[:2]]
        _release_backend_phase(work, ('inputs', 'wheels'))
        tool_bytes = sum(path.stat().st_size for path in tool_inputs.values())
        admit_phase('pack', installed_bytes + tool_bytes)
        _pack_backend_archive(portable, staged_outputs[0], staged_tool)
        _check_launcher(launcher_target.parent, launcher_inputs)
        _release_backend_phase(work, ('extract',))
        admit_phase('source', staged_outputs[0].stat().st_size + tool_bytes)
        _write_source_plan(staged_outputs[1], source_plan)
        admit_phase('finalize', sum(path.stat().st_size for path in staged_outputs) + tool_bytes)
        return finalize_backend_outputs(staged_outputs[0], staged_outputs[1], parent, outputs[2].name,
            input_descriptor={'name': descriptor.name, 'bytes': descriptor.stat().st_size, 'sha256': expected_sha256},
            candidate_source=candidate_source, run_id=run_id, run_attempt=run_attempt,
            **({'private_context':private_context} if private_context is not None else {}))


def main() -> int:
    if len(sys.argv) > 1 and sys.argv[1] == 'snapshot-local-native':
        component = argparse.ArgumentParser(description='Copy sealed local backend/control DATA into fresh owned staging; verify every manifest member, no runtime or archive execution.')
        component.add_argument('--backend', type=Path, required=True)
        component.add_argument('--control', type=Path, required=True)
        component.add_argument('--work', type=Path, required=True)
        component.add_argument('--sealed-receipt', type=Path, required=True)
        component.add_argument('--receipt-bytes', type=int, required=True)
        component.add_argument('--receipt-sha256', required=True)
        component.add_argument('--controller-source', required=True)
        component.add_argument('--reserve-bytes', type=int, default=0, help='space reserved for concurrent owners, in addition to the retained 20 GiB floor and 1 GiB phase allowance')
        selected = component.parse_args(sys.argv[2:])
        print(json.dumps(snapshot_local_native_runtime(selected.backend, selected.control, selected.work,
            selected.sealed_receipt, receipt_bytes=selected.receipt_bytes, receipt_sha256=selected.receipt_sha256,
            controller_source=selected.controller_source, reserve_bytes=selected.reserve_bytes), sort_keys=True))
        return 0
    if len(sys.argv) > 1 and sys.argv[1] == 'receive-local-backend':
        component=argparse.ArgumentParser(description='Receive original private backend archive DATA into fresh owned staging; no backend execution or hosted intake.')
        component.add_argument('--directory',type=Path,required=True)
        component.add_argument('--producer-receipt',type=Path,required=True)
        component.add_argument('--output',type=Path,required=True)
        component.add_argument('--archive-tool',type=Path,required=True)
        component.add_argument('--verify-existing',action='store_true',help='read-only full archive/roster validation of an existing extracted tree; performs no extraction')
        selected=component.parse_args(sys.argv[2:])
        print(json.dumps(receive_local_backend(selected.directory,selected.producer_receipt,selected.output,selected.archive_tool,
            verify_existing=selected.verify_existing),sort_keys=True))
        return 0
    if len(sys.argv) > 1 and sys.argv[1] == 'refresh-local-native':
        component = argparse.ArgumentParser(description='Refresh current OA code in owned historical Comfy runtime copies; retain third-party bytes, no backend/model startup.')
        component.add_argument('--backend', type=Path, required=True)
        component.add_argument('--control', type=Path, required=True)
        component.add_argument('--work', type=Path, required=True)
        component.add_argument('--integration', type=Path, required=True)
        component.add_argument('--integration-bytes', type=int, required=True)
        component.add_argument('--integration-sha256', required=True)
        component.add_argument('--source-commit', required=True)
        component.add_argument('--native-source-commit', required=True)
        component.add_argument('--native-library-sha256', required=True)
        component.add_argument('--appmodel-library-sha256', required=True)
        component.add_argument('--appmodel-wheel-sha256', required=True)
        component.add_argument('--resume-refresh-receipt', type=Path)
        component.add_argument('--resume-receive-receipt', type=Path)
        component.add_argument('--recovery-controller-source')
        selected = component.parse_args(sys.argv[2:])
        recovery_values = (selected.resume_refresh_receipt, selected.resume_receive_receipt, selected.recovery_controller_source)
        if any(recovery_values) and not all(recovery_values):
            component.error('local refresh recovery requires both existing receipts and the clean controller source')
        recovery = (_local_refresh_recovery(*recovery_values) if all(recovery_values) else None)
        result = refresh_local_native_runtime(selected.backend, selected.control, selected.integration, selected.work,
            integration_bytes=selected.integration_bytes, integration_sha256=selected.integration_sha256,
            private_context=dict(source_commit=selected.source_commit, native_source_commit=selected.native_source_commit,
                native_library_sha256=selected.native_library_sha256,appmodel_library_sha256=selected.appmodel_library_sha256,
                appmodel_wheel_sha256=selected.appmodel_wheel_sha256), recovery=recovery)
        print(json.dumps(result,sort_keys=True))
        return 0
    if len(sys.argv) > 1 and sys.argv[1] == 'local-native-preview':
        component = argparse.ArgumentParser(description='Package an unsigned complete local OA Comfy preview; no hosted/signed intake or installation.')
        component.add_argument('--host', type=Path, required=True)
        component.add_argument('--host-receipt', type=Path, required=True)
        component.add_argument('--backend', type=Path, required=True)
        component.add_argument('--control', type=Path, required=True)
        component.add_argument('--output', type=Path, required=True)
        component.add_argument('--source-commit', required=True)
        component.add_argument('--native-source-commit', required=True)
        component.add_argument('--native-library-sha256', required=True)
        component.add_argument('--appmodel-library-sha256', required=True)
        component.add_argument('--appmodel-wheel-sha256', required=True)
        selected = component.parse_args(sys.argv[2:])
        result = assemble_local_native_preview(selected.host, selected.backend, selected.control, selected.output,
            host_receipt=json.loads(selected.host_receipt.read_bytes()), private_context=dict(
                source_commit=selected.source_commit, native_source_commit=selected.native_source_commit,
                native_library_sha256=selected.native_library_sha256,appmodel_library_sha256=selected.appmodel_library_sha256,
                appmodel_wheel_sha256=selected.appmodel_wheel_sha256))
        print(json.dumps(result, sort_keys=True))
        return 0
    if len(sys.argv) > 1 and sys.argv[1] == 'macos-python-host':
        component = argparse.ArgumentParser(description='Stage the existing customized macOS Python dependency component; ordinary nested-code signing remains required.')
        component.add_argument('--python', type=Path, required=True, help='explicit existing CPython 3.11+ framework or shared-library PBS interpreter')
        component.add_argument('--output', type=Path, required=True, help='fresh app-local helper directory')
        selected = component.parse_args(sys.argv[2:])
        print(json.dumps(stage_macos_python_host(selected.python, selected.output), sort_keys=True))
        return 0
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", type=Path, help="local pinned official .7z asset (legacy portable mode)")
    parser.add_argument("--backend-inputs", type=Path, help="source-selected Manager-free AMD input descriptor")
    parser.add_argument("--backend-inputs-sha256", help="immutable input descriptor pin, not an output receipt")
    parser.add_argument("--backend-receipt-output", type=Path, help="external variable backend producer receipt")
    parser.add_argument('--source-mode', choices=('public-split', 'private'), default='public-split',
                        help='AMD backend only: private is a source-pinned developer output ineligible for public intake')
    parser.add_argument('--integration-source-commit', help='private developer integration source commit')
    parser.add_argument('--integration-native-source-commit', help='private developer native wheel source commit')
    parser.add_argument('--integration-native-library-sha256', help='private developer native DLL pin')
    parser.add_argument("--backend-archive-tool", type=Path, help="absolute source-pinned 7zr.exe with adjacent source archive/notices")
    parser.add_argument("--output", type=Path, required=True, help="new AMD backend .7z logical archive or legacy portable ZIP")
    parser.add_argument("--source-output", type=Path, required=True, help="new corresponding exact-commit source ZIP")
    parser.add_argument("--frontend-checkout", type=Path, help="local ComfyUI_frontend v1.51.10 checkout at the pinned commit")
    parser.add_argument("--embedded-docs-checkout", type=Path, help="local embedded-docs v0.5.11 checkout")
    parser.add_argument("--pygit2-checkout", type=Path, help="local pygit2 v1.20.0 checkout")
    parser.add_argument("--libgit2-checkout", type=Path, help="local libgit2 v1.9.6 checkout")
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
    parser.add_argument("--integration-bytes", type=int, help="reviewed OA integration ZIP byte count; legacy mode retains its historical default")
    parser.add_argument("--integration-sha256", help="reviewed OA integration ZIP SHA-256; legacy mode retains its historical default")
    parser.add_argument("--cpu-wheelhouse", type=Path, help="legacy portable mode: three exact-hash Windows CPython 3.12 CPU PyTorch wheels")
    parser.add_argument("--tar", default="tar.exe", help="release host's bsdtar executable")
    parser.add_argument("--minimum-free-gib", type=int, default=20,
                        help="retained free-space floor: workstation default 20 GiB; disposable runner minimum 1 GiB")
    args = parser.parse_args()
    private_context = None
    private_values = (args.integration_source_commit, args.integration_native_source_commit,
                      args.integration_native_library_sha256)
    if args.source_mode == 'private':
        if (not args.backend_inputs or not all(private_values) or args.integration_public_source_root
                or args.integration_redist_source_commit or args.integration_run_id is not None
                or args.integration_run_attempt is not None or args.integration_producer_receipt or args.integration_wheel_receipt):
            parser.error('private developer backend requires exact private pins and no public source/run/receipt context')
        try:
            private_context = _private_integration_context(dict(zip(
                ('source_commit','native_source_commit','native_library_sha256'), private_values)))
        except ValueError as error: parser.error(str(error))
    elif any(private_values):
        parser.error('private integration pins require --source-mode private')
    if args.backend_inputs:
        if (not args.backend_inputs_sha256 or not args.backend_receipt_output or not args.backend_archive_tool or args.archive or args.cpu_wheelhouse
                or private_context is None and (not args.integration_redist_source_commit or not args.integration_run_id or not args.integration_run_attempt
                or not args.integration_producer_receipt or not args.integration_wheel_receipt)
                or not args.integration_bytes or not args.integration_sha256):
            parser.error("AMD backend requires source-pinned inputs, --backend-archive-tool, receipt output, public integration receipts/pin and source/run/attempt; no portable/CPU inputs")
    elif args.backend_archive_tool:
        parser.error('--backend-archive-tool is only valid with --backend-inputs')
    elif not all((args.archive, args.frontend_checkout, args.embedded_docs_checkout,
                  args.pygit2_checkout, args.libgit2_checkout, args.cpu_wheelhouse)):
        parser.error("legacy portable mode requires archive, CPU wheels and corresponding source checkouts")
    public_sources = None
    if args.integration_public_source_root and not args.backend_inputs:
        with zipfile.ZipFile(args.integration_archive) as archive:
            integration_manifest = json.loads(archive.read("manifest.json"))
        repos = {integration_manifest["adopter_source"]["repository"],
                 integration_manifest["packager_source"]["repository"],
                 *(wheel["repository"] for wheel in integration_manifest["wheels"])}
        public_sources = {repo: args.integration_public_source_root / repo.split("/", 1)[1]
                          for repo in repos}
    if args.backend_inputs:
        result = build_backend(args.backend_inputs, args.backend_inputs_sha256, args.output, args.source_output,
            args.backend_receipt_output, candidate_source=args.integration_redist_source_commit,
            run_id=args.integration_run_id, run_attempt=args.integration_run_attempt,
            integration_archive=args.integration_archive, public_sources=public_sources,
            public_source_root=args.integration_public_source_root,
            expected_integration_size=args.integration_bytes,expected_integration_sha256=args.integration_sha256,
            archive_tool=args.backend_archive_tool,
            integration_options={} if private_context is not None else {"producer_receipt_path": args.integration_producer_receipt,
                "wheel_receipt_path": args.integration_wheel_receipt,
                "expected_redist_source_commit": args.integration_redist_source_commit,
                "expected_run_id": args.integration_run_id, "expected_run_attempt": args.integration_run_attempt},
            minimum_free_after_build_bytes=args.minimum_free_gib << 30,
            private_context=private_context)
        print(json.dumps(result, sort_keys=True))
        return 0
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
                      expected_integration_size=args.integration_bytes if args.integration_bytes is not None else INTEGRATION_BYTES,
                      expected_integration_sha256=args.integration_sha256 if args.integration_sha256 is not None else INTEGRATION_SHA256,
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
