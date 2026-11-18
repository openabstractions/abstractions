#!/usr/bin/env python3
"""Prepare the pinned OpenCode-with-OA installer payload without running it.

Development tool only. The installed application requires no Python or Node.
Usage: build_installer.py --plugin TGZ --plugin-sha256 HEX --plugin-bytes N
         --parser-tgz TGZ --out DIR [--desktop-fix DIR]
The official Desktop installer is a separate pinned bootstrapper input. The
package carries no OpenCode program: its configuration helper runs on the
installed official Desktop's own Node (installer_host.c, ELECTRON_RUN_AS_NODE).
--desktop-fix names the folder holding OA's patched watcher.node and its
LICENSE (DESKTOP_FIX); it defaults to the committed desktop-fix folder beside
this owner and both files must match their pins.
Release order: --phase prepare-native, sign the two own native outputs through
the release signing owner, then --phase package with their exact path/hash/size
inputs. Packaging consumes those bytes and runs no compiler. The existing
payload/installer receipts carry integrity facts; signature authority stays with
the release signing owner. --phase all retains the ordinary private QA route.
"""
from __future__ import annotations

import argparse
import hashlib
import io
import importlib.util
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import zipfile
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[3]
readback_path = Path(__file__).resolve().with_name('readback.py')
readback_spec = importlib.util.spec_from_file_location('oa_opencode_readback', readback_path)
readback = importlib.util.module_from_spec(readback_spec)
exec(compile(readback_path.read_bytes(), str(readback_path), 'exec'), readback.__dict__)
build_app = None


def app_builder(path=None):
    """Load the existing shared owner from the local or explicit public layout."""
    global build_app
    if path is None and build_app is not None:
        return build_app
    selected = Path(path) if path is not None else ROOT/'installer/apps/build_app.py'
    if selected.name != 'build_app.py' or selected.is_symlink() or not selected.is_file():
        raise ValueError('the shared app builder must be the existing build_app.py owner')
    selected = selected.resolve(strict=True)
    if not (selected.parent/'app.wxs').is_file():
        raise ValueError('the shared app builder has no app.wxs companion')
    spec = importlib.util.spec_from_file_location('oa_opencode_app_builder', selected)
    owner = importlib.util.module_from_spec(spec)
    exec(compile(selected.read_bytes(), str(selected), 'exec'), owner.__dict__)
    build_app = owner
    return owner


NATIVE_SOURCES = ['installer_host.c', 'desktop_state.h', 'desktop_fix.h', 'desktop_preflight.c',
                  'desktop_preflight.manifest', 'integration.wxs', 'application-manifest.json']
REGISTRATION_MANIFEST = 'openabstractions-application.json'
PAYLOAD_DIRECTORY = 'LocalAppDataFolder/Programs/OpenAbstractions/apps/opencode/'
REGISTRATION_DIRECTORY = PAYLOAD_DIRECTORY
PLUGIN_INVENTORY_MEMBER = PurePosixPath('installer', 'plugin-files.json').as_posix()
DESKTOP_FIX_PREFIX = 'desktop-fix/'
SDK_NATIVE_MEMBERS = ('abstraction_ipc.dll', 'oa_ipc_node.node')
FACADE_NATIVE_MEMBERS = ('abstraction_facade_appmodel.dll', 'oa_appmodel_node.node')
APP_SOURCE_OVERRIDES = ('configure.js', 'inventory.js', 'runtime-connector.js', 'opencode-build.js',
                        'official-install.js', 'README.md', 'installer/config-lifecycle.js',
                        'installer/package.json', 'installer/package-lock.json')


def plugin_prefix(version):
    if not isinstance(version, str) or not re.fullmatch(r'\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?', version):
        raise ValueError('the plugin version is not a release version')
    return 'plugin/' + version + '/'


def payload_member(payload, relative, expected_pin):
    rows = [row for row in payload['files'] if row['path'] == PAYLOAD_DIRECTORY + relative]
    if len(rows) != 1 or any(rows[0][key] != expected_pin[key] for key in ('sha256', 'bytes')):
        raise ValueError('source-selected payload member differs: ' + relative)
    return rows[0]


def journey_payload_roles(prepared_receipt, expected_payload):
    """Join authenticated original payload facts to private fixture role IDs.

    This pure projection performs no archive, signature or source admission.
    Its caller must authenticate those originals before using the returned IDs.
    Config tools are installed code; actual user config remains a transaction
    observation. Original TGZ and composed plugin members retain separate pins.
    """
    receipt = prepared_receipt
    if (not isinstance(receipt, dict) or not isinstance(receipt.get('plugin'), dict)
            or set(receipt['plugin']) != {'version', 'sha256', 'bytes'}
            or not isinstance(receipt.get('payload'), dict)
            or not isinstance(receipt.get('app_owned_source_overrides'), dict)
            or not isinstance(receipt.get('registration_manifest'), dict)
            or not isinstance(expected_payload, dict) or type(expected_payload.get('schema')) is not int
            or expected_payload['schema'] != 1 or not isinstance(expected_payload.get('files'), list)):
        raise ValueError('incomplete original journey payload facts')
    prefix = plugin_prefix(receipt['plugin']['version'])
    if receipt.get('upstream') != UPSTREAM:
        raise ValueError('journey upstream differs from selected source')
    def pin(value):
        if (not isinstance(value, dict) or set(value) != {'sha256', 'bytes'}
                or not isinstance(value['sha256'], str) or not re.fullmatch('[0-9a-f]{64}', value['sha256'])
                or type(value['bytes']) is not int or value['bytes'] < 0):
            raise ValueError('invalid original payload pin')
        return dict(value)
    pin({key: receipt['plugin'][key] for key in ('sha256', 'bytes')})
    payload = receipt['payload']
    members = {}
    folded = set()
    for name, value in payload.items():
        if (not isinstance(name, str) or '\\' in name or ':' in name
                or any(part in ('', '.', '..') for part in name.split('/'))
                or not (name.startswith(prefix)
                        or name in (PLUGIN_INVENTORY_MEMBER, *DESKTOP_FIX['members']))):
            raise ValueError('foreign original payload member')
        if name.casefold() in folded:
            raise ValueError('aliased original payload member')
        folded.add(name.casefold())
        members[name] = pin(value)
    required = set(DESKTOP_FIX['members'])
    required.update(prefix + name for name in ('package.json', 'index.js', 'server.js', 'npm-shrinkwrap.json', *APP_SOURCE_OVERRIDES))
    native = prefix + 'node_modules/@openabstractions/ipc-win32-x64/'
    required.update(native + name for name in SDK_NATIVE_MEMBERS)
    facade_native=prefix+'node_modules/@openabstractions/facade-native-win32-x64/'
    if any(name.startswith(facade_native) for name in members):
        required.update(facade_native+name for name in FACADE_NATIVE_MEMBERS)
    if not required <= set(members):
        raise ValueError('incomplete source-selected plugin closure')
    if set(receipt['app_owned_source_overrides']) != set(APP_SOURCE_OVERRIDES):
        raise ValueError('source override inventory differs')
    for name, digest in receipt['app_owned_source_overrides'].items():
        if members[prefix + name]['sha256'] != digest:
            raise ValueError('source override pin differs')
    manifest = receipt['registration_manifest']
    if set(manifest) != {'name', 'sha256', 'bytes'} or manifest['name'] != REGISTRATION_MANIFEST:
        raise ValueError('registration manifest name differs')
    manifest_pin = pin({key: manifest[key] for key in ('sha256', 'bytes')})
    files = expected_payload['files']
    ids, paths = set(), set()
    for row in files:
        if (not isinstance(row, dict) or set(row) != {'id', 'path', 'sha256', 'bytes'} or not isinstance(row['id'], str)
                or not row['id'] or row['id'] in ids or not isinstance(row['path'], str) or row['path'] in paths):
            raise ValueError('duplicate or invalid expected File identity')
        pin({key: row[key] for key in ('sha256', 'bytes')})
        ids.add(row['id']); paths.add(row['path'])
    manifest_path = REGISTRATION_DIRECTORY + REGISTRATION_MANIFEST
    if paths != {PAYLOAD_DIRECTORY + name for name in members} | {manifest_path}:
        raise ValueError('expected File inventory differs from complete original payload')
    manifests = [row for row in files if row['path'] == manifest_path]
    if any(manifests[0][key] != manifest_pin[key] for key in ('sha256', 'bytes')):
        raise ValueError('registration manifest pin differs')
    joined = {name: dict(payload_member(expected_payload, name, value)) for name, value in sorted(members.items())}
    return {'plugin_files': {name: row for name, row in joined.items() if name.startswith(prefix)},
            'config_tools': {'configure': joined[prefix + 'configure.js'],
                             'lifecycle': joined[prefix + 'installer/config-lifecycle.js']},
            'sdk_native': {**{name:joined[native+name] for name in SDK_NATIVE_MEMBERS},
                           **{name:joined[facade_native+name] for name in FACADE_NATIVE_MEMBERS if facade_native+name in joined}},
            'members': joined, 'registration_manifest': dict(manifests[0]),
            'original_plugin': dict(receipt['plugin']), 'original_members': members}


def registration_manifest():
    return app_builder().filled_manifest(Path(__file__).with_name('application-manifest.json'),'opencode',{}).encode('utf-8')


def shared_native_header():
    """The selected shared app owner supplies this native dependency."""
    owner=Path(app_builder().__file__).resolve(strict=True).parent
    header=owner/'user_token.h'
    if header.is_symlink() or header.is_junction() or not header.is_file() or header.resolve(strict=True).parent!=owner:
        raise ValueError('the selected shared app owner has no plain owned user_token.h')
    # host_inputs.h belongs to the prepared output. Quoted include search
    # must not pick an unmeasured sibling before that generated input.
    for parent in (Path(__file__).resolve().parent,owner):
        shadow=parent/'host_inputs.h'
        if shadow.exists() or shadow.is_symlink() or shadow.is_junction():
            raise ValueError('conflicting host_inputs.h outside prepared output')
    return header


def shared_records_header():
    """The shared FindRecords action (oa_records.h) beside the app owner."""
    owner=shared_native_header().parent
    header=owner/'oa_records.h'
    if header.is_symlink() or header.is_junction() or not header.is_file() or header.resolve(strict=True).parent!=owner:
        raise ValueError('the selected shared app owner has no plain owned oa_records.h')
    shadow=Path(__file__).resolve().parent/'oa_records.h'
    if shadow.exists() or shadow.is_symlink() or shadow.is_junction():
        raise ValueError('conflicting oa_records.h beside native source')
    return header


def native_identity_header():
    owner=Path(app_builder().__file__).resolve(strict=True).parent
    helper=owner/'build_extension.py'
    if not helper.is_file() or helper.is_symlink() or helper.is_junction():
        raise ValueError('selected shared owner has no identity source owner')
    spec=importlib.util.spec_from_file_location('oa_selected_native_identity_owner',helper)
    module=importlib.util.module_from_spec(spec)
    exec(compile(helper.read_bytes(),str(helper),'exec'),module.__dict__)
    selected=module.native_identity_source()
    shadow=Path(__file__).resolve().parent/'native_windows.h'
    if shadow.exists() or shadow.is_symlink() or shadow.is_junction():
        raise ValueError('conflicting native_windows.h beside native source')
    return selected


def native_source_hashes():
    source = Path(__file__).resolve().parent
    return {**{name: hashlib.sha256((source/name).read_bytes()).hexdigest() for name in NATIVE_SOURCES},
            'shared/user_token.h':hashlib.sha256(shared_native_header().read_bytes()).hexdigest(),
            'shared/oa_records.h':hashlib.sha256(shared_records_header().read_bytes()).hexdigest(),
            'shared/native_windows.h':hashlib.sha256(native_identity_header().read_bytes()).hexdigest(),
            'shared/build_extension.py':hashlib.sha256((shared_native_header().parent/'build_extension.py').read_bytes()).hexdigest(),
            'source/file_boundary.py':hashlib.sha256(file_boundary_source().read_bytes()).hexdigest()}

UPSTREAM = {
    'version': '1.18.34',
    'source': 'aec0b9a6d8898f68f923aaf08b7306d931fd9d76',
    'desktop': {'asset': 601927802, 'bytes': 126348744,
                'sha256': '403d446d6ca9a9f2edb694d0ecf64c2be7468facade100b9d6e9e65430672bd2'},
    'desktop_exe_sha256': 'b82ea814eb265c07c97df0e112f673c8be4718e00c311c154db85e06d3c1f9ac',
}


# Official Desktop releases this OA release has tested (ruling
# OPENCODE-WATCHER-PIN, 2026-11-10). desktop_state.h OA_DESKTOP_RELEASES
# carries the same pins; the preflight admits exactly these versions. The
# bundle ships UPSTREAM's installer, the first entry. Each installer is the
# publisher's Authenticode-signed asset (Anomaly Innovations, Inc).
DESKTOP_RELEASES = (
    {'version': '1.18.34',
     'installer': {'sha256': '403d446d6ca9a9f2edb694d0ecf64c2be7468facade100b9d6e9e65430672bd2', 'bytes': 126348744},
     'exe_sha256': 'b82ea814eb265c07c97df0e112f673c8be4718e00c311c154db85e06d3c1f9ac',
     'watcher_stock': {'sha256': 'a8199cf7b6c5102267a5889f80321a4cc92e631d7504c474d32a35b4d440f315', 'bytes': 518144}},
    {'version': '1.19.5',
     'installer': {'sha256': 'a14e77b5aa94a71de493d0fd4ac7a70031246ebb5cceb8ab847fc71d4a2fe62b', 'bytes': 126390832},
     'exe_sha256': '64f43dc4e75cee06c85f03361d12a7791a60873114a2420cbdd94ae7f0c5da3f',
     'watcher_stock': {'sha256': 'a8199cf7b6c5102267a5889f80321a4cc92e631d7504c474d32a35b4d440f315', 'bytes': 518144}},
)

# The Desktop crash fix: @parcel/watcher v2.5.1 (664695d) plus
# parcel-bundler/watcher#277 (d09df163), built by OA from build_commit with
# MSVC (static runtime), reproducibly: CL=/Brepro,
# _LINK_=/Brepro /PDBALTPATH:watcher.pdb, node-gyp 12.4.0 rebuild --release
# against Node 24.19.0 headers, cl 19.51.36256, node-addon-api 7.0.0 from the
# locked yarn.lock integrity. installer_host.c replaces the Desktop's stock
# watcher.node with it only over a stock hash above, or over an earlier OA
# build. LICENSE is upstream's `git show v2.5.1:LICENSE`. Review:
# research/security-reviews/parcel-watcher-2.5.1-pr277-oa-win32-x64.md.
DESKTOP_FIX = {
    'live': 'resources/app.asar.unpacked/node_modules/@parcel/watcher-win32-x64/watcher.node',
    'source': {'repository': 'https://github.com/parcel-bundler/watcher', 'tag': 'v2.5.1',
               'base': '664695dbb2488d53f782c3e2b8791c4c114731fb', 'pull': 277,
               'pull_commit': 'd09df163078cd54e422c8fbacacbf0359be3f52e',
               'build_commit': '052dc8d70871bc73d8ea1a30675f23347d144d98'},
    'members': ('desktop-fix/watcher.node', 'desktop-fix/LICENSE'),
    'files': {'watcher.node': {'sha256': '0251ee3ba7027af84a0cb8e0c2ffa1c5989eacf21bc091f95a7c5c00cc486e12', 'bytes': 528896},
              'LICENSE': {'sha256': '5cfba9d5711264d45a2431a53c79f597cef12720cc7c764db2602daa92d0bd2a', 'bytes': 1077}},
    # Earlier OA builds of the same fix. Install replaces them; Remove restores
    # the kept stock bytes over them. 9ec8ac8a carried a local PDB path.
    'earlier': ('9ec8ac8abf41c8ff2a7cc2298961f2165e14939e8e3a6d9d2aa49757697e4fed',),
    'directory': 'desktop-fix',
}


def setup_fixup_declaration():
    """Passive Setup observation and repair facts from the owned installer."""
    return {
        'opencode-official': {
            'File': DESKTOP_FIX['live'],
            'Stock': sorted({row['watcher_stock']['sha256'] for row in DESKTOP_RELEASES}),
            'Fixed': [DESKTOP_FIX['files']['watcher.node']['sha256']],
            'Versions': [row['version'] for row in DESKTOP_RELEASES],
            'ProgramSHA256': {row['version']: row['exe_sha256'] for row in DESKTOP_RELEASES},
            'Package': PAYLOAD_DIRECTORY.removeprefix('LocalAppDataFolder/').rstrip('/'),
            'PackageFile': DESKTOP_FIX['members'][0],
            'Purpose': "It stops a crash that takes OpenCode's server down.",
            'RepairUpgradeCode': '{'+app_builder().upgrade_code('opencode').upper()+'}',
        },
    }


def desktop_fix_directory():
    """The exact reviewed supplier bytes in this selected public installer."""
    return Path(__file__).resolve().parent/DESKTOP_FIX['directory']


def pe_debug_paths(data):
    """The PDB paths a PE image's CodeView debug records name."""
    import struct
    def unpack(form, offset):
        if offset < 0 or offset+struct.calcsize(form) > len(data):
            raise ValueError('truncated PE image')
        return struct.unpack_from(form, data, offset)
    if data[:2] != b'MZ':
        raise ValueError('not a PE image')
    header = unpack('<I', 0x3c)[0]
    if data[header:header+4] != b'PE\0\0':
        raise ValueError('not a PE image')
    sections, optional_size = unpack('<H', header+6)[0], unpack('<H', header+20)[0]
    optional = header+24
    magic = unpack('<H', optional)[0]
    directories = optional+(112 if magic == 0x20b else 96 if magic == 0x10b else -1)
    if directories < optional or unpack('<I', directories-4)[0] <= 6:
        return []
    rva, size = unpack('<II', directories+6*8)
    table = optional+optional_size
    def offset(address):
        for index in range(sections):
            virtual, raw_size, raw = (unpack('<I', table+index*40+12)[0], unpack('<I', table+index*40+16)[0],
                                      unpack('<I', table+index*40+20)[0])
            if virtual <= address < virtual+raw_size:
                return raw+address-virtual
        raise ValueError('PE debug directory outside its sections')
    paths = []
    if not rva or not size:
        return paths
    start = offset(rva)
    for entry in range(size//28):
        kind, length, _, pointer = unpack('<IIII', start+entry*28+12)
        record = data[pointer:pointer+length]
        if kind == 2 and record[:4] == b'RSDS' and len(record) > 24:
            paths.append(record[24:].split(b'\0', 1)[0].decode('utf-8', 'replace'))
    return paths


def refuse_builder_paths(name, data):
    """A supplier PE OA builds must not carry its builder's absolute paths
    (an account name, a checkout folder) in its debug record."""
    for path in pe_debug_paths(data):
        if re.match(r'^(?:[A-Za-z]:[\\/]|[\\/])', path):
            raise ValueError(name+' names an absolute builder path in its debug record: '+path)
    return data


def desktop_fix_payload(directory=None):
    """OA's patched watcher and its MIT licence, each checked against its pin;
    the watcher's debug record names no builder path."""
    root = Path(directory) if directory is not None else desktop_fix_directory()
    payload = {DESKTOP_FIX_PREFIX+name: pinned(root/name, pin['sha256'], pin['bytes'])
               for name, pin in DESKTOP_FIX['files'].items()}
    refuse_builder_paths(DESKTOP_FIX_PREFIX+'watcher.node', payload[DESKTOP_FIX_PREFIX+'watcher.node'])
    return payload, {'source': DESKTOP_FIX['source'], 'live': DESKTOP_FIX['live'], 'files': DESKTOP_FIX['files'],
                     'stock': sorted({row['watcher_stock']['sha256'] for row in DESKTOP_RELEASES}),
                     'earlier': list(DESKTOP_FIX['earlier'])}


def fixture_removal_policy():
    """Describe fixture-only cleanup; the receiving owner must prove new ownership."""
    return {'profile': 'nsis-current-user', 'basename': 'Uninstall OpenCode.exe'}


def source_owned_host_policy():
    """Return the existing official Desktop policy for packaging and receiving."""
    return {'name':'opencode-desktop-win-x64-1.18.34.exe',
            'sha256':UPSTREAM['desktop']['sha256'],'bytes':UPSTREAM['desktop']['bytes'],
            'arp_id':'d074f30d-5f88-5885-b075-be1348cc7676',
            'arp_version':UPSTREAM['version'],'arp_win64':True,
            'download_url':'https://github.com/anomalyco/opencode/releases/download/v1.18.34/opencode-desktop-win-x64.exe'}


def pinned(path, sha256, size):
    data = Path(path).read_bytes()
    if len(data) != size or hashlib.sha256(data).hexdigest() != sha256:
        raise ValueError(f'{path} differs from its exact size/hash pin')
    return data


def source_roles(prepared_out=None, expected_payload=None, *, retained_inputs=None):
    """Expose source-selected PE roles; the receiver admits signatures separately.

    The prepackage call selects only new native outputs. SDK members require
    admitted canonical SDK proofs, rebound as payload/<member> by the caller.
    """
    ns = '{http://wixtoolset.org/schemas/v4/wxs}'
    tree = ET.fromstring(Path(__file__).with_name('integration.wxs').read_bytes())
    binaries = [row for row in tree.iter(ns+'Binary')
                if row.get('SourceFile') == '$(var.InstallerHost)']
    if len(binaries) != 1 or not binaries[0].get('Id'):
        raise ValueError('installer host source policy differs')
    own = [('Binary', binaries[0].get('Id'), 'installer_host.dll')]
    if expected_payload is None:
        return tuple(own), (), ('desktop_preflight.exe',)
    app_builder()
    receipt = check_prepared(Path(prepared_out), **({'retained_inputs':retained_inputs} if retained_inputs is not None else {}))
    def member(relative):
        return payload_member(expected_payload, relative, receipt['payload'][relative])['id']
    native = plugin_prefix(receipt['plugin']['version'])+'node_modules/@openabstractions/ipc-win32-x64/'
    for name in SDK_NATIVE_MEMBERS:
        relative = native+name
        own.append(('File', member(relative), 'payload/'+relative))
    facade_native=plugin_prefix(receipt['plugin']['version'])+'node_modules/@openabstractions/facade-native-win32-x64/'
    if any(name.startswith(facade_native) for name in receipt['payload']):
        for name in FACADE_NATIVE_MEMBERS:
            relative=facade_native+name
            own.append(('File',member(relative),'payload/'+relative))
    supplier = []
    package='node_modules/@msgpackr-extract/msgpackr-extract-win32-x64'
    plugin_native=plugin_prefix(receipt['plugin']['version'])+'node_modules/@msgpackr-extract/msgpackr-extract-win32-x64/'
    if any(name.startswith(plugin_native) for name in receipt['payload']):
        plugin_lock_raw=(Path(__file__).parent.parent/'package-lock.json').read_bytes()
        plugin_lock=json.loads(plugin_lock_raw)
        selected=plugin_lock['packages'][package]
        origin={'application':'opencode','version':plugin_lock['packages']['node_modules/@opencode-ai/plugin']['version'],
                'lock_sha256':hashlib.sha256(plugin_lock_raw).hexdigest()}
        for name,sha in [('node.abi115.node','300b188fb8c997214cb65bf1b00729da293176c7d57035e40c2f432ea384dd89'),
                         ('node.napi.node','6958541c624784dfcebfc4f3c360c57f71cfeedd0530c2393845061729081a08')]:
            relative=plugin_native+name
            pin={'sha256':sha,'bytes':112640,'source':dict(origin,url=selected['resolved'],integrity=selected['integrity'],member='package/'+name)}
            if receipt['payload'].get(relative)!={key:pin[key] for key in ('sha256','bytes')}:
                raise ValueError('prepared plugin native member differs from source-selected upstream')
            supplier.append(('File',member(relative),pin))
    relative=DESKTOP_FIX_PREFIX+'watcher.node'
    pin={**DESKTOP_FIX['files']['watcher.node'],
         'source':dict(application='opencode',**DESKTOP_FIX['source'],member='build/Release/watcher.node')}
    if receipt['payload'].get(relative)!=DESKTOP_FIX['files']['watcher.node']:
        raise ValueError('prepared Desktop fix differs from its source-selected build')
    supplier.append(('File',member(relative),pin))
    return tuple(own), tuple(supplier), ('desktop_preflight.exe',)


def package_files(data):
    """Validate every member before returning the expanded, exact package."""
    app_builder()
    files = {}
    seen = set()
    with tarfile.open(fileobj=io.BytesIO(data), mode='r:gz') as archive:
        for member in archive.getmembers():
            if not member.isfile() or not member.name.startswith('package/'):
                raise ValueError(f'plugin member is not a package regular file: {member.name}')
            name = member.name.removeprefix('package/')
            name = '/'.join(build_app.member_segments(name))
            if name.casefold() in seen:
                raise ValueError(f'plugin case collision: {name}')
            seen.add(name.casefold())
            files[name] = archive.extractfile(member).read()
    return json.loads(files['package.json']), files


def plugin_files(data, *, bundled=True):
    manifest, files = package_files(data)
    if manifest['name'] != '@openabstractions/opencode':
        raise ValueError('the input is not the OA OpenCode package')
    if not re.fullmatch(r'\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?', manifest.get('version', '')):
        raise ValueError('the plugin version is not a release version')
    if 'runtime-connector.js' not in manifest.get('files', []):
        raise ValueError('the plugin manifest does not carry the runtime selector')
    for name in ['./server', './tui', '.']:
        value = manifest.get('exports', {}).get(name)
        entry = value if isinstance(value, str) else (value or {}).get('default')
        if not entry or entry.removeprefix('./') not in files:
            raise ValueError(f'plugin export {name} does not name a carried entry')
    for entry in ['runtime-connector.js', 'server.js', 'index.js', 'npm-shrinkwrap.json']:
        if entry not in files:
            raise ValueError(f'plugin misses {entry}')
    if not bundled:
        return manifest, files
    for dependency in manifest['bundleDependencies']:
        if f'node_modules/{dependency}/package.json' not in files:
            raise ValueError(f'plugin misses bundled {dependency}')
    for entry in ['abstraction_ipc.dll', 'oa_ipc_node.node']:
        if f'node_modules/@openabstractions/ipc-win32-x64/{entry}' not in files:
            raise ValueError(f'plugin misses its own {entry}')
    facade = files.get('node_modules/@openabstractions/facade/package.json')
    if facade is not None and '@openabstractions/facade-native-win32-x64' in json.loads(facade).get('optionalDependencies',{}):
        for entry in FACADE_NATIVE_MEMBERS:
            if 'node_modules/@openabstractions/facade-native-win32-x64/'+entry not in files:
                raise ValueError('plugin misses its own AppModel '+entry)
    return manifest, files


def bundle_plugin(plugin, digest, size, packages, dependency_cache, output):
    """Bundle the normal offline npm archive set for this app-owned MSI.

    Registry packages remain unchanged. The installer composition carries their
    exact installed closure, using npm's existing lock/cache receiving owner.
    """
    raw = pinned(plugin, digest, size)
    manifest, _ = plugin_files(raw, bundled=False)
    # The selected shared package owner supplies the matching release npm
    # receiver in both private installer/apps and public Redist/windows/apps.
    selected = Path(app_builder().__file__).resolve().parents[2]/'scripts/npm_packages.py'
    if str(selected.parent) not in sys.path:
        sys.path.insert(0,str(selected.parent))
    spec = importlib.util.spec_from_file_location('oa_opencode_npm_owner', selected)
    npm = importlib.util.module_from_spec(spec)
    exec(compile(selected.read_bytes(), str(selected), 'exec'), npm.__dict__)
    expected = npm.package_roster(Path(__file__).with_name('npm.tsv')) | {
        '@openabstractions/ipc-win32-x64', npm.FACADE_NATIVE['prefix']+'win32-x64'}
    packed, pins, names = [], {}, set()
    for path in sorted(Path(packages).glob('*.tgz')):
        if path.is_symlink() or path.is_junction() or not path.is_file():
            raise ValueError('linked or missing npm package input')
        data = path.read_bytes()
        package, _ = package_files(data)
        name = package['name']
        if name not in expected or name in names or package['version'] != manifest['version']:
            raise ValueError('installer npm archive roster or version differs')
        if name == manifest['name'] and data != raw:
            raise ValueError('installer plugin differs from the selected archive')
        names.add(name)
        packed.append((name,path,[]))
        pins[str(path.resolve())] = {'sha256':hashlib.sha256(data).hexdigest(),'bytes':len(data)}
    if names != expected:
        raise ValueError('installer npm archive closure is incomplete')
    output = Path(output).resolve()
    if output.exists():
        raise FileExistsError('bundled plugin output already exists')
    output.parent.mkdir(parents=True,exist_ok=True)
    with npm.owned_temporary_tree(prefix='oa-opencode-bundle-', parent=Path(tempfile.gettempdir())) as temporary:
        target = Path(temporary)
        for parent in target.parents:
            modules=parent/'node_modules'
            if modules.exists() or modules.is_symlink():
                raise ValueError('ambient ancestor node_modules: '+str(modules))
        environment = {key:value for key,value in os.environ.items()
                       if key.upper() not in ('NODE_PATH','NODE_OPTIONS') and not key.upper().startswith('NPM_CONFIG_')}
        for name in ('user.npmrc','global.npmrc'):
            (target/name).write_text('',encoding='utf-8')
        home=target/'home';home.mkdir()
        environment.update(HOME=str(home),USERPROFILE=str(home),npm_config_userconfig=str(target/'user.npmrc'),
            npm_config_globalconfig=str(target/'global.npmrc'),npm_config_cache=str(target/'npm-cache'),npm_config_offline='true')
        with tarfile.open(fileobj=io.BytesIO(raw),mode='r:gz') as archive:
            wrap=json.load(archive.extractfile('package/npm-shrinkwrap.json'))
        external={key:value for key,value in wrap['packages'].items() if key}
        npm.copy_dependency_cache(dependency_cache,target/'npm-cache',external,environment=environment)
        versions=npm.resolve_packed_dependencies(packed,target,environment,external)
        admitted={name:(target/name).read_bytes() for name in ('package.json','package-lock.json')}
        subprocess.run(['npm','install','--offline','--ignore-scripts','--no-save','--no-audit','--no-fund'],
                       cwd=target,env=environment,check=True,shell=os.name=='nt')
        if any((target/name).read_bytes()!=value for name,value in admitted.items()):
            raise ValueError('installer npm install changed the admitted lock')
        lock=json.loads(admitted['package-lock.json'])
        if npm.external_lock_identities(lock['packages'],versions)!=npm.external_lock_identities(external):
            raise ValueError('installer external dependency closure differs')
        npm.test_opencode_package(target,environment)
        modules=target/'node_modules'
        for path in modules.rglob('*'):
            if path.is_symlink() or path.is_junction():
                raise ValueError('installed npm closure contains a linked member')
        bundle=target/'bundle'
        shutil.copytree(npm.extended_path(modules/'@openabstractions/opencode'),npm.extended_path(bundle))
        def omit_self(directory, entries):
            return ['opencode'] if Path(directory).name=='@openabstractions' and 'opencode' in entries else []
        shutil.copytree(npm.extended_path(modules),npm.extended_path(bundle/'node_modules'),ignore=omit_self,dirs_exist_ok=True)
        composed=npm.read_package(bundle)
        # npm packs bundles only through dependency edges. The app-owned
        # closure pins its upstream peer to the already admitted installed
        # version; the ordinary published plugin archive remains unchanged.
        for name in composed.get('peerDependencies',{}):
            composed['dependencies'][name]=npm.read_package(modules/name)['version']
        composed['bundleDependencies']=sorted(set(composed['dependencies']) | set(composed.get('peerDependencies',{})))
        npm.write_package(bundle,composed)
        artifact=npm.pack(bundle,target,environment)
        bundled=artifact.read_bytes()
        plugin_files(bundled)
        for path,value in pins.items():
            pinned(path,value['sha256'],value['bytes'])
        pinned(plugin,digest,size)
        output.write_bytes(bundled)
    return {'path':str(output),'sha256':hashlib.sha256(bundled).hexdigest(),'bytes':len(bundled),
            'original_plugin':{'path':str(Path(plugin).resolve()),'sha256':digest,'bytes':size},'packages':pins}


def file_boundary_source():
    selected=ROOT/'scripts/file_boundary.py'
    if not selected.is_file() or selected.is_symlink() or selected.is_junction() or selected.stat().st_nlink!=1 or any(parent.is_symlink() or parent.is_junction() for parent in selected.parents):
        raise ValueError('missing plain selected scripts/file_boundary.py')
    return selected


def extract_payload(data, destination):
    """Give Python filesystem I/O its long-path form; keep compiler paths ordinary."""
    app_builder()
    selected=file_boundary_source();source=selected.read_bytes()
    spec=importlib.util.spec_from_file_location('oa_opencode_file_boundary',selected)
    owner=importlib.util.module_from_spec(spec);exec(compile(source,str(selected),'exec'),owner.__dict__)
    build_app.extract(data,owner.extended_path(destination))
    if file_boundary_source()!=selected or selected.read_bytes()!=source:
        raise ValueError('selected scripts/file_boundary.py changed during extraction')


def prepare(plugin, digest, size, parser_tgz, out, desktop_fix=None):
    package, files = plugin_files(pinned(plugin, digest, size))
    version = package['version']
    build_app.member_segments(version)
    payload = {plugin_prefix(version)+name: data for name, data in files.items()}
    parser_manifest, parser = package_files(pinned(parser_tgz,
        '4a0315b8671e7463bae7af7c142cdf19e9aa7ba39eb36dc2df383b8648e3cbc9', 27354))
    if parser_manifest['name'] != 'jsonc-parser' or parser_manifest['version'] != '3.3.1':
        raise ValueError('the config editor is not the upstream pinned package')
    prefix = f'plugin/{version}/'
    # App-owned source overrides are named explicitly; no b745 source equality
    # is claimed for this installer composition.
    overrides = {}
    for name in APP_SOURCE_OVERRIDES:
        raw = (Path(__file__).resolve().parent.parent / name).read_bytes()
        payload[prefix + name] = raw
        overrides[name] = hashlib.sha256(raw).hexdigest()
    for name, raw in parser.items():
        payload[prefix + 'installer/node_modules/jsonc-parser/' + name] = raw
    plugin_inventory = {name.removeprefix(prefix): {
        'bytes': len(raw), 'sha256': hashlib.sha256(raw).hexdigest(),
    } for name, raw in payload.items() if name.startswith(prefix)}
    payload[PLUGIN_INVENTORY_MEMBER] = json.dumps(plugin_inventory, sort_keys=True).encode('utf-8')
    fix_files, fix_receipt = desktop_fix_payload(desktop_fix)
    payload.update(fix_files)
    folded = set()
    for name in payload:
        build_app.member_segments(name)
        if name.casefold() in folded:
            raise ValueError(f'composed payload case collision: {name}')
        folded.add(name.casefold())
    out = Path(out)
    out.mkdir(parents=True, exist_ok=False)
    target = out / 'integration-payload.zip'
    with zipfile.ZipFile(target, 'w', zipfile.ZIP_DEFLATED) as archive:
        for name, data in payload.items():
            archive.writestr(name, data)
    receipt = {'upstream': UPSTREAM, 'desktop_fix': fix_receipt, 'plugin': {'version': version, 'sha256': digest,
               'bytes': size}, 'app_owned_source_overrides': overrides,
               'parser': {'version': '3.3.1', 'sha256': '4a0315b8671e7463bae7af7c142cdf19e9aa7ba39eb36dc2df383b8648e3cbc9'},
               'payload': {name: {'bytes': len(data), 'sha256': hashlib.sha256(data).hexdigest()}
                                         for name, data in payload.items()},
               'zip_sha256': hashlib.sha256(target.read_bytes()).hexdigest(),
               'status': 'Prepared source payload; MSI/Burn and installed lifecycle qualification pending'}
    (out / 'payload-receipt.json').write_text(json.dumps(receipt, indent=2), encoding='utf-8')
    trusted = {name: entry['sha256'] for name, entry in receipt['payload'].items()
               if name in [PLUGIN_INVENTORY_MEMBER, prefix+'package.json']
               or name in [prefix+name for name in overrides]
               or name.startswith(prefix+'installer/node_modules/jsonc-parser/')
               or name.startswith(DESKTOP_FIX_PREFIX)}
    def wide(value):
        return 'L' + json.dumps(value.replace('/', '\\'))
    header = ['#pragma once', 'typedef struct { const wchar_t *relative; const char *sha256; } OA_TRUSTED_FILE;',
              '#define OA_PAYLOAD_MAX_RELATIVE ' + str(max(len(name.encode('utf-16-le')) // 2 for name in payload)),
              '#define OA_PLUGIN_RELATIVE ' + wide(f'plugin/{version}'),
              '#define OA_HELPER_RELATIVE ' + wide(f'plugin/{version}/installer/config-lifecycle.js'),
              '#define OA_PLUGIN_INVENTORY_SHA256 L"' + trusted[PLUGIN_INVENTORY_MEMBER] + '"',
              'static const OA_TRUSTED_FILE OA_TRUSTED[] = {']
    header += ['  {'+wide(name)+', "'+digest+'"},' for name, digest in sorted(trusted.items())]
    header += ['};', '#define OA_TRUSTED_COUNT (sizeof(OA_TRUSTED)/sizeof(OA_TRUSTED[0]))']
    (out / 'host_inputs.h').write_text('\n'.join(header)+'\n', encoding='utf-8')
    manifest=registration_manifest()
    receipt['registration_manifest']={'name':REGISTRATION_MANIFEST,'sha256':hashlib.sha256(manifest).hexdigest(),'bytes':len(manifest)}
    with (out/'host_inputs.h').open('a',encoding='utf-8') as target:
        target.write('#define OA_REGISTRATION_MANIFEST_NAME '+wide(REGISTRATION_MANIFEST)+'\n')
        target.write('#define OA_REGISTRATION_MANIFEST_SHA256 "'+receipt['registration_manifest']['sha256']+'"\n')
    receipt['host_inputs_sha256'] = hashlib.sha256((out/'host_inputs.h').read_bytes()).hexdigest()
    receipt['native_source_hashes'] = native_source_hashes()
    (out / 'payload-receipt.json').write_text(json.dumps(receipt, indent=2), encoding='utf-8')
    return receipt


def build_native(out, version, cl):
    """The app's fixed native preflight and cached MSI action, without execution."""
    app_builder()
    out = Path(out).resolve()
    if not build_app.MSI_VERSION.fullmatch(version):
        raise ValueError('MSI version must be X.Y.Z')
    source = Path(__file__).resolve().parent
    shared=shared_native_header().parent
    identity=native_identity_header().parent
    if (out/'native_windows.h').exists():raise ValueError('conflicting native_windows.h in prepared output')
    source_pins=native_source_hashes()
    host = out/'installer_host.dll'
    native = [cl, '/nologo', '/LD', '/std:c17', '/W4', '/I'+str(identity), '/I'+str(shared), '/I'+str(out), '/Fo'+str(out/'installer_host.obj'),
              str(source/'installer_host.c'), '/link', '/OUT:'+str(host),
              'msi.lib','shell32.lib','shlwapi.lib','ole32.lib','uuid.lib','bcrypt.lib','advapi32.lib','userenv.lib']
    subprocess.run(native, cwd=out, check=True)
    numbers=','.join(version.split('.')+['0'])
    resource=out/'preflight-version.rc'
    resource.write_text('#include <windows.h>\n1 VERSIONINFO\nFILEVERSION '+numbers+'\nPRODUCTVERSION '+numbers+
        '\nFILEOS VOS_NT_WINDOWS32\nFILETYPE VFT_APP\nBEGIN\n BLOCK "StringFileInfo"\n BEGIN\n BLOCK "040904b0"\n BEGIN\n'+
        ' VALUE "FileDescription", "OpenCode with OA compatibility check\\0"\n VALUE "FileVersion", "'+version+'\\0"\n'+
        ' VALUE "ProductName", "OpenCode with OA\\0"\n VALUE "ProductVersion", "'+version+'\\0"\n'+
        ' VALUE "OriginalFilename", "desktop_preflight.exe\\0"\n END\n END\n BLOCK "VarFileInfo"\n BEGIN\n VALUE "Translation", 0x409, 1200\n END\nEND\n',encoding='utf-8')
    subprocess.run(['rc.exe','/nologo','/fo'+str(out/'preflight-version.res'),str(resource)],cwd=out,check=True)
    preflight=out/'desktop_preflight.exe'
    preflight_command=[cl,'/nologo','/std:c17','/W4','/I'+str(identity),'/I'+str(shared),'/I'+str(out),'/Fo'+str(out/'desktop_preflight.obj'),
        '/Fe'+str(preflight),str(source/'desktop_preflight.c'),str(out/'preflight-version.res'),'/link','/SUBSYSTEM:WINDOWS',
        '/MANIFEST:EMBED','/MANIFESTINPUT:'+str(source/'desktop_preflight.manifest'),
        'shell32.lib','shlwapi.lib','ole32.lib','uuid.lib','bcrypt.lib','advapi32.lib','user32.lib']
    subprocess.run(preflight_command,cwd=out,check=True)
    if native_source_hashes()!=source_pins:raise ValueError('native source/header changed during compilation')
    return {'host':host,'native_command':native,'preflight':preflight,'preflight_command':preflight_command}


def package_source(out):
    """Root this app's native-action fragment in the existing package."""
    namespace='http://wixtoolset.org/schemas/v4/wxs'
    tree=ET.parse(Path(app_builder().__file__).with_name('app.wxs'))
    package=tree.getroot().find('{'+namespace+'}Package')
    if package is None:raise ValueError('existing app owner has no Package')
    upgrade=package.find('{'+namespace+'}MajorUpgrade')
    if upgrade is None:raise ValueError('existing app owner has no MajorUpgrade')
    upgrade.set('Schedule','afterInstallInitialize')
    register=package.find(".//{"+namespace+"}Custom[@Action='RegisterApplication']")
    if register is None:raise ValueError('existing app owner has no registration action')
    # A kept update replaces package bytes and preserves the current app
    # registration, including a disconnect made while Windows Installer ran.
    register.set('Condition',register.get('Condition')+' AND NOT OAKEEPMAINTENANCE')
    ET.SubElement(package,'{'+namespace+'}Property',{'Id':'OAKEEPMAINTENANCE','Secure':'yes'})
    programs=package.find(".//{"+namespace+"}Directory[@Id='AppProgramsFolder']")
    component=package.find(".//{"+namespace+"}Component[@Id='ApplicationManifest']")
    if programs is None or component is None:raise ValueError('existing app manifest/directory owner differs')
    component.set('Directory','INSTALLFOLDER')
    component.find('{'+namespace+'}File').set('Name',REGISTRATION_MANIFEST)
    ET.SubElement(package,'{'+namespace+'}CustomActionRef',{'Id':'CheckOpenCodeClosed'})
    # installer_host.c carries FindRecords (oa_records.h): the package keeps
    # one native action library.
    records=package.find(".//{"+namespace+"}CustomAction[@Id='FindRecords']")
    library=package.find(".//{"+namespace+"}Binary[@Id='OaRecordsAction']")
    if records is None or library is None or records.get('BinaryRef')!='OaRecordsAction':
        raise ValueError('existing app owner has no FindRecords action')
    records.set('BinaryRef','OpenCodeInstallerHost')
    package.remove(library)
    ET.register_namespace('',namespace)
    target=out/'app-package.wxs';tree.write(target,encoding='utf-8',xml_declaration=True)
    return target


def retained_plugin_inputs(receipt):
    """Original composition facts keyed by content for a receiving directory."""
    if 'bundled_plugin' not in receipt:return {}
    composition=receipt['bundled_plugin']
    if (not isinstance(composition,dict) or not isinstance(composition.get('packages'),dict) or
            any(composition.get(key)!=receipt['plugin'][key] for key in ('sha256','bytes'))):
        raise ValueError('bundled plugin differs from the prepared plugin')
    result={}
    for value in (composition,composition['original_plugin'],*composition['packages'].values()):
        if not isinstance(value,dict):raise ValueError('invalid retained plugin input pin')
        digest,size=value.get('sha256'),value.get('bytes')
        if not isinstance(digest,str) or not re.fullmatch('[0-9a-f]{64}',digest) or type(size) is not int or size<=0:
            raise ValueError('invalid retained plugin input pin')
        pin={'sha256':digest,'bytes':size}
        if digest in result and result[digest]!=pin:raise ValueError('conflicting retained plugin input size')
        result[digest]=pin
    return result


def check_prepared(out, *, retained_inputs=None):
    receipt = json.loads((out/'payload-receipt.json').read_text(encoding='utf-8'))
    if 'bundled_plugin' in receipt:
        composition=receipt['bundled_plugin']
        if retained_inputs is None:
            pinned(composition['path'],composition['sha256'],composition['bytes'])
            original=composition['original_plugin']
            pinned(original['path'],original['sha256'],original['bytes'])
            for path,value in composition['packages'].items():
                pinned(path,value['sha256'],value['bytes'])
        else:
            selected=file_boundary_source();source=selected.read_bytes()
            spec=importlib.util.spec_from_file_location('oa_opencode_retained_input_boundary',selected)
            boundary=importlib.util.module_from_spec(spec);exec(compile(source,str(selected),'exec'),boundary.__dict__)
            directory=boundary.ordinary_path(retained_inputs,kind='directory')
            required=retained_plugin_inputs(receipt)
            if {path.name for path in directory.iterdir()}!={digest+'.tgz' for digest in required}:
                raise ValueError('retained plugin input roster differs')
            for digest,value in required.items():
                path=boundary.ordinary_path(directory/(digest+'.tgz'),kind='file')
                if path.stat().st_nlink!=1:raise ValueError('retained plugin input is hardlinked')
                pinned(path,value['sha256'],value['bytes'])
            if file_boundary_source()!=selected or selected.read_bytes()!=source:
                raise ValueError('selected scripts/file_boundary.py changed during receiving')
    raw = (out/'integration-payload.zip').read_bytes()
    if hashlib.sha256(raw).hexdigest() != receipt['zip_sha256']:
        raise ValueError('prepared payload archive differs')
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        names = archive.namelist()
        if len(names) != len(set(names)) or set(names) != set(receipt['payload']):
            raise ValueError('prepared payload member inventory differs')
        for name in names:
            build_app.member_segments(name)
            data = archive.read(name)
            if receipt['payload'][name] != {'bytes': len(data), 'sha256': hashlib.sha256(data).hexdigest()}:
                raise ValueError('prepared payload member differs')
    if receipt['upstream'] != UPSTREAM or receipt['native_source_hashes'] != native_source_hashes():
        raise ValueError('prepared source or official host closure differs')
    if 'bootstrap' in receipt:
        raise ValueError('prepared payload carries a first-use bootstrap closure this package no longer ships')
    if hashlib.sha256((out/'host_inputs.h').read_bytes()).hexdigest() != receipt['host_inputs_sha256']:
        raise ValueError('prepared native host_inputs differs')
    manifest=registration_manifest()
    if receipt.get('registration_manifest')!={'name':REGISTRATION_MANIFEST,'sha256':hashlib.sha256(manifest).hexdigest(),'bytes':len(manifest)}:
        raise ValueError('prepared registration manifest differs')
    prefix = 'plugin/'+receipt['plugin']['version']+'/'
    for name, digest in receipt['app_owned_source_overrides'].items():
        source = Path(__file__).resolve().parent.parent/name
        if hashlib.sha256(source.read_bytes()).hexdigest() != digest or receipt['payload'][prefix+name]['sha256'] != digest:
            raise ValueError('prepared app source override differs')
    return receipt


def file_pin(path):
    path = Path(path)
    return {'path': str(path.resolve()), 'sha256': hashlib.sha256(path.read_bytes()).hexdigest(), 'bytes': path.stat().st_size}


def prepare_native(out, version, cl):
    app_builder()
    out = Path(out).resolve()
    receipt = check_prepared(out)
    compiled = build_native(out, version, cl)
    receipt['prepared_native'] = {'version': version, 'host': file_pin(compiled['host']),
        'preflight': file_pin(compiled['preflight']), 'native_command': compiled['native_command'],
        'preflight_command': compiled['preflight_command']}
    (out/'payload-receipt.json').write_text(json.dumps(receipt, indent=2), encoding='utf-8')
    return compiled


def build_msi(out, version, cl, wix, qa=False, desktop=None, native_inputs=None):
    """Existing app MSI owner plus this app's cached custom-action fragment."""
    app_builder()
    out = Path(out).resolve()
    receipt = check_prepared(out)
    if not qa and receipt['plugin']['version'] != version:
        raise ValueError('release MSI and plugin must carry the same release version')
    if desktop is None:
        raise ValueError('the app installer composition requires its exact official Desktop installer')
    desktop_bytes=pinned(desktop,UPSTREAM['desktop']['sha256'],UPSTREAM['desktop']['bytes'])
    source = Path(__file__).resolve().parent
    if native_inputs is None:
        compiled = build_native(out,version,cl)
    else:
        prepared = receipt.get('prepared_native', {})
        if prepared.get('version') != version:
            raise ValueError('prepared native version differs')
        for name in ('host', 'preflight'):
            supplied = native_inputs[name]
            path = Path(supplied['path'])
            if not path.is_file() or path.is_symlink() or path.is_junction():
                raise ValueError('native package input is missing or linked')
            pinned(path, supplied['sha256'], supplied['bytes'])
        compiled = {'host': Path(native_inputs['host']['path']).resolve(),
                    'preflight': Path(native_inputs['preflight']['path']).resolve(),
                    'native_command': prepared['native_command'], 'preflight_command': prepared['preflight_command']}
    host=compiled['host'];preflight=compiled['preflight']
    payload = out/'payload'; payload.mkdir()
    extract_payload((out/'integration-payload.zip').read_bytes(), payload)
    # A clean Windows has no Visual C++ runtime: every packaged program and
    # library carries the runtime DLLs it imports (the shared owner beside
    # the app builder, installer/runtime_imports.py).
    owner_path=Path(app_builder().__file__).resolve().parent.parent/'runtime_imports.py'
    spec=importlib.util.spec_from_file_location('oa_opencode_runtime_imports',owner_path)
    runtime_imports=importlib.util.module_from_spec(spec);exec(compile(owner_path.read_bytes(),str(owner_path),'exec'),runtime_imports.__dict__)
    missing=runtime_imports.problems(payload)
    if missing:raise ValueError('packaged files lack the Visual C++ runtime they import: '+'; '.join(missing))
    manifest = out/REGISTRATION_MANIFEST
    manifest.write_bytes(registration_manifest())
    def check_registration_manifest():
        expected=receipt['registration_manifest']
        if manifest.name!=expected['name']:raise ValueError('registration manifest filename differs')
        pinned(manifest,expected['sha256'],expected['bytes'])
    check_registration_manifest()
    msi = out/f'opencode-{version}.msi'
    command = [wix,'build','-arch','x64','-d','AppId=opencode','-d','AppTitle=OpenCode with OA'+(' (QA)' if qa else ''),
               '-d','Publisher=OpenAbstractions','-d','Version='+version,'-d','UpgradeCode='+build_app.upgrade_code('opencode'),
               '-d','Payload='+str(payload),'-d','Manifest='+str(manifest),'-d','InstallerHost='+str(host),
               '-o',str(msi),str(package_source(out)),str(source/'integration.wxs')]
    subprocess.run(command,cwd=out,check=True)
    check_registration_manifest()
    if native_source_hashes()!=receipt['native_source_hashes']:raise ValueError('native source/header changed during packaging')
    if native_inputs is not None:
        for supplied in native_inputs.values():
            pinned(supplied['path'],supplied['sha256'],supplied['bytes'])
    compiled_readback=readback.verify(wix,msi,out,receipt['payload'],host,manifest,version,receipt['registration_manifest'])
    check_registration_manifest()
    host_policy=source_owned_host_policy()
    official_host=out/host_policy['name'];official_host.write_bytes(desktop_bytes)
    receipt.update(fixture_removal=fixture_removal_policy(),status='Unsigned QA installer' if qa else 'Unsigned installer; signing and release qualification pending',
                   msi={'path':str(msi),'bytes':msi.stat().st_size,'sha256':hashlib.sha256(msi.read_bytes()).hexdigest()},
                   native_command=compiled['native_command'],wix_command=command,compiled_readback={key:value for key,value in compiled_readback.items() if key!='payload'},
                   preflight={'path':str(preflight),'bytes':preflight.stat().st_size,'sha256':hashlib.sha256(preflight.read_bytes()).hexdigest(),
                              'version':version,'signed':False,'command':compiled['preflight_command']},
                   source_hashes=native_source_hashes(),
                   consumed_native={'host':file_pin(host),'preflight':file_pin(preflight),
                       'integrity_inputs_only':True,'signature_authority':'Existing release signing owner verifies policy/source/run/attempt'})
    (out/'installer-receipt.json').write_text(json.dumps(receipt,indent=2),encoding='utf-8')
    if native_inputs is not None:
        receipt['preflight'].pop('signed')
    app={'id':'opencode','title':'OpenCode with OA'+(' (QA)' if qa else ''),'file':str(msi),
         'bytes':receipt['msi']['bytes'],'sha256':receipt['msi']['sha256'],
         'host':{**{key:value for key,value in host_policy.items() if key!='name'},'file':str(official_host),
                 'preflight':{'file':str(preflight),'bytes':preflight.stat().st_size,'sha256':hashlib.sha256(preflight.read_bytes()).hexdigest()}}}
    # Existing build_bundle chain entry. Release signing changes native/MSI
    # bytes and requires the release owner to seal their final pins again.
    (out/'bundle-app.json').write_text(json.dumps(app,indent=2),encoding='utf-8')
    receipt['bundle_app_sha256']=hashlib.sha256((out/'bundle-app.json').read_bytes()).hexdigest()
    (out/'installer-receipt.json').write_text(json.dumps(receipt,indent=2),encoding='utf-8')
    return receipt


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=['all','prepare-native','package'], default='all',
                        help='all keeps legacy assembly; prepare-native stops before MSI; package consumes exact final native inputs without compiling')
    parser.add_argument('--app-builder', type=Path, help='Existing shared build_app.py in the public redist/windows/apps layout')
    parser.add_argument('--plugin', type=Path)
    parser.add_argument('--plugin-sha256')
    parser.add_argument('--plugin-bytes', type=int)
    parser.add_argument('--desktop', type=Path, help='Exact official Desktop installer, required for --version composition')
    parser.add_argument('--parser-tgz', type=Path)
    parser.add_argument('--desktop-fix', type=Path, help="Folder with OA's patched watcher.node and LICENSE (default: this installer's "+DESKTOP_FIX['directory']+')')
    parser.add_argument('--package-set',type=Path,help='Normal complete npm archive set to bundle into the installer plugin')
    parser.add_argument('--dependency-cache',type=Path,help='Existing lock-bound offline cache for --package-set')
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--version', help='Build the MSI in an existing x64 Visual Studio developer environment')
    parser.add_argument('--cl', default='cl.exe')
    parser.add_argument('--wix', default='wix')
    parser.add_argument('--qa', action='store_true', help='Explicit private QA MSI; permits a differently labelled input plugin')
    for name in ['installer-host','preflight']:
        parser.add_argument('--'+name, type=Path)
        parser.add_argument('--'+name+'-sha256')
        parser.add_argument('--'+name+'-bytes', type=int)
    if argv is None and len(sys.argv) == 1:
        parser.print_help()
        return 0
    args = parser.parse_args(argv)
    if bool(args.package_set)!=bool(args.dependency_cache):
        parser.error('--package-set and --dependency-cache must be supplied together')
    if args.phase != 'all' and not args.version:
        parser.error(args.phase+' requires --version')
    app_builder(args.app_builder)
    if args.phase != 'package':
        if any(value is None for value in [args.plugin,args.plugin_sha256,args.plugin_bytes,args.parser_tgz]):
            parser.error('preparation requires plugin/hash/bytes and parser inputs')
        plugin,digest,size=args.plugin,args.plugin_sha256,args.plugin_bytes
        composition=None
        if args.package_set is not None:
            composition=bundle_plugin(plugin,digest,size,args.package_set,args.dependency_cache,args.out.with_name(args.out.name+'-bundled-plugin.tgz'))
            plugin,digest,size=Path(composition['path']),composition['sha256'],composition['bytes']
        prepared=prepare(plugin,digest,size,args.parser_tgz,args.out,args.desktop_fix)
        if composition is not None:
            prepared['bundled_plugin']=composition
            (args.out/'payload-receipt.json').write_text(json.dumps(prepared,indent=2),encoding='utf-8')
    if args.phase == 'prepare-native':
        if not args.version:parser.error('prepare-native requires --version')
        prepare_native(args.out,args.version,args.cl)
    elif args.phase == 'package':
        if not args.version:parser.error('package requires --version')
        inputs={}
        for key,name in [('host','installer_host'),('preflight','preflight')]:
            values=[getattr(args,name),getattr(args,name+'_sha256'),getattr(args,name+'_bytes')]
            if any(value is None for value in values):parser.error('package requires exact host and preflight path/hash/bytes')
            inputs[key]=dict(zip(['path','sha256','bytes'],values))
        build_msi(args.out,args.version,args.cl,args.wix,args.qa,args.desktop,native_inputs=inputs)
    elif args.version:
        build_msi(args.out,args.version,args.cl,args.wix,args.qa,args.desktop)
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
