"""Prepare native Lemonade inputs or package already signed native bytes.

This app recipe emits source measurements. The release receiver authenticates
input lineage and supplies signature policy through the existing signing owner.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import shutil
import tarfile
import tempfile
import types
import xml.etree.ElementTree as ET

HERE = Path(__file__).resolve().parent
OWN_PE = ('lemond.exe', 'lemonade.exe', 'LemonadeServer.exe')
SDK_REPOS = ('abstraction-identity', 'abstraction-facade', 'abstraction-job',
             'abstraction-download', 'abstraction-logging')
HEX = re.compile(r'[0-9a-f]{64}\Z')
COMMIT = re.compile(r'[0-9a-f]{40}\Z')
UI_IDS = ('WixUiCa_X64', 'WixUI_Bmp_Up', 'WixUI_Bmp_Banner', 'WixUI_Ico_Exclam',
          'WixUI_Ico_Info', 'WixUI_Bmp_New', 'WixUI_Bmp_Dialog')
HOST_TOOLS = ('cmake', 'python', 'node', 'npm', 'wix', 'msbuild', 'git')
FIND_FLAGS = {'CMAKE_FIND_USE_SYSTEM_ENVIRONMENT_PATH': 'FALSE',
              'CMAKE_FIND_USE_CMAKE_SYSTEM_PATH': 'FALSE',
              'CMAKE_FIND_USE_PACKAGE_REGISTRY': 'FALSE',
              'CMAKE_FIND_USE_SYSTEM_PACKAGE_REGISTRY': 'FALSE'}


def read_json(path):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError('duplicate JSON key')
            result[key] = value
        return result
    return json.loads(Path(path).read_text(encoding='utf-8'), object_pairs_hook=pairs)


def source_module(path):
    """Load a selected source owner without writing bytecode into its checkout."""
    module = types.ModuleType('lemonade_selected_' + path.stem)
    module.__file__ = str(path)
    exec(compile(path.read_bytes(), str(path), 'exec'), module.__dict__)
    return module


BOUNDARY_PATH = HERE / 'file_boundary.py'
if (BOUNDARY_PATH.is_symlink() or not BOUNDARY_PATH.is_file()
        or getattr(BOUNDARY_PATH.lstat(), 'st_file_attributes', 0) & 0x400):
    raise ValueError('producer boundary helper must be a plain source file')
BOUNDARY_BYTES = BOUNDARY_PATH.read_bytes()
_boundary = types.ModuleType('lemonade_file_boundary')
exec(compile(BOUNDARY_BYTES, str(BOUNDARY_PATH), 'exec'), _boundary.__dict__)


def producer_sources():
    if BOUNDARY_PATH.is_symlink() or getattr(BOUNDARY_PATH.lstat(), 'st_file_attributes', 0) & 0x400:
        raise ValueError('linked producer boundary helper')
    if BOUNDARY_PATH.read_bytes() != BOUNDARY_BYTES:
        raise ValueError('producer boundary helper changed after load')
    return {name: pin(HERE / name) for name in ('build_native.py', 'file_boundary.py')}


def source_roles(sources_root, package, extension):
    rows = {row['id']: {key: row[key] for key in ('sha256', 'bytes')}
            for row in ui_rows(sources_root, package, extension)}
    # Shared signer root is the prepared build directory, as package() requires.
    return (tuple(('File', name, 'Release/' + name) for name in OWN_PE),
            (('Binary', 'WixUiCa_X64', rows['WixUiCa_X64']),), ())


def local_source_inputs(manifest=None):
    """Finite acquisition names from the existing source manifest, with no acquisition effects."""
    manifest = manifest or read_json(HERE / 'native-inputs.json')
    return [('upstream', manifest['upstream']['url'], manifest['upstream']['commit']),
            *((row['name'], row['url'], row['commit']) for row in manifest['native_dependencies'])]


def offline_git_environment(owner, hooks, environment=None):
    result = owner.clean_git_environment(environment)
    result = {key: value for key, value in result.items()
              if not key.upper().startswith('GIT_CONFIG')
              and key.upper() not in ('GIT_EXEC_PATH', 'GIT_TEMPLATE_DIR')}
    result.update(GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL=os.devnull,
                  GIT_CONFIG_SYSTEM=os.devnull, GIT_ALLOW_PROTOCOL='file',
                  GIT_PROTOCOL_FROM_USER='0', GIT_TERMINAL_PROMPT='0',
                  GIT_CONFIG_COUNT='2', GIT_CONFIG_KEY_0='core.hooksPath',
                  GIT_CONFIG_VALUE_0=str(hooks), GIT_CONFIG_KEY_1='init.templateDir',
                  GIT_CONFIG_VALUE_1=str(hooks))
    return result


def selected_git_environment(sources_root, hooks, environment=None):
    hooks = Path(hooks)
    for path in (hooks, *hooks.parents):
        if path.is_symlink() or getattr(path.lstat(), 'st_file_attributes', 0) & 0x400:
            raise ValueError('linked Git hooks directory')
    if not hooks.is_dir() or any(hooks.iterdir()):
        raise ValueError('owned empty Git hooks directory required')
    owner = source_module(regular(Path(sources_root) / 'redist/scripts/git_env.py',
                                  [Path(sources_root) / 'redist']))
    return offline_git_environment(owner, hooks, environment)


def local_checkout(git, cache, destination, commit, tree, *, env=None):
    """Consume immutable local objects; never consult a remote or recurse submodules."""
    cache = Path(cache).resolve(strict=True)
    if not cache.is_dir() or not COMMIT.fullmatch(commit) or not COMMIT.fullmatch(tree):
        raise ValueError('invalid local Git input')
    subprocess.run([str(git), 'init', str(destination)], check=True, env=env)
    subprocess.run([str(git), '-C', str(destination), '-c', 'protocol.file.allow=always',
                    'fetch', '--depth=1', '--no-tags', '--no-recurse-submodules',
                    cache.as_uri(), commit], check=True, env=env)
    subprocess.run([str(git), '-C', str(destination), 'checkout', '--detach', commit], check=True, env=env)
    clean_git(destination, commit=commit, tree=tree, git_tool=git, env=env)


def lws_transform(source):
    """Extract the single existing configure owner, including Windows pipe containment."""
    text = (source / 'CMakeLists.txt').read_text(encoding='utf-8')
    begin = '    # Patch the CMakeLists.txt files to remove -Werror'
    end = '    # Now add the subdirectory'
    if text.count(begin) != 1 or text.count(end) != 1:
        raise ValueError('ambiguous native LWS configure owner')
    body = text.split(begin, 1)[1].split(end, 1)[0]
    if 'windows-pipe.c' not in body or 'LWS_CMAKE_MAIN' not in body:
        raise ValueError('incomplete native LWS configure owner')
    return (begin + body).encode('utf-8')


def lws_diff(git, root, *, env=None):
    return subprocess.check_output([str(git), '-C', str(root), 'diff', '--binary',
        '--full-index', '--no-ext-diff', '--no-textconv', '--src-prefix=a/', '--dst-prefix=b/'], env=env)


def cmake_literal(value):
    delimiter = '='
    while ']' + delimiter + ']' in value:
        delimiter += '='
    return '[' + delimiter + '[' + value + ']' + delimiter + ']'


def materialize(args):
    """Reconstruct selected local source and measure inputs; acquisition belongs to caller."""
    # Validate every caller tool before the first Git dispatch or selected-source call.
    names = ('git', 'python', 'node', 'npm', 'wix')
    if any(not Path(getattr(args, name)).is_absolute() for name in names):
        raise ValueError('explicit absolute host tool required')
    tools = {name: regular(Path(getattr(args, name)), [Path(getattr(args, name)).parent]) for name in names}
    initial_pins = {name: pin(path) for name, path in tools.items()}
    # Refuse missing or foreign caller inputs before reconstructing source trees.
    environment(tools.values(), args.inputs_root / 'npm-cache')
    if not args.npm_cache.is_dir() or not inventory(args.npm_cache):
        raise ValueError('missing pinned offline npm cache')
    ui_rows(args.sources_root, args.wix_ui_package, args.wix_ui_extension)
    pins = read_json(args.pins)
    for name in (*SDK_REPOS, 'abstractions', 'redist'):
        commit = pins.get('openabstractions/' + name)
        if not isinstance(commit, str) or not COMMIT.fullmatch(commit):
            raise ValueError('missing selected current source: ' + name)
    args.inputs_root.mkdir(parents=True, exist_ok=False)
    upstream = args.inputs_root / 'upstream'
    upstream.mkdir()
    hooks = upstream / 'empty-git-hooks'
    hooks.mkdir()
    git_env = selected_git_environment(args.sources_root, hooks)
    for repo, commit in pins.items():
        if not re.fullmatch(r'openabstractions/[A-Za-z0-9._-]+', repo):
            raise ValueError('invalid selected source repository')
        clean_git(args.sources_root / repo.split('/')[1], commit=commit, git_tool=tools['git'], env=git_env)
    selected_recipe = args.sources_root / 'abstractions/adopters/lemonade'
    manifest = read_json(selected_recipe / 'native-inputs.json')
    patch = regular(selected_recipe / manifest['oa_delta']['file'], [selected_recipe])
    if pin(patch)['sha256'] != manifest['oa_delta']['sha256']:
        raise ValueError('native source patch differs')
    selector = source_module(args.sources_root / 'redist/sdk_smokes/prepare.py')
    selection = selector.msvc_toolchain()
    tools.update(cmake=Path(selection['cmake']), msbuild=Path(selection['msbuild']))
    for path in tools.values():
        if not path.is_absolute():
            raise ValueError('explicit absolute host tool required')
        regular(path, [path.parent])
    tool_pins = {name: pin(path) for name, path in tools.items()}
    if any(tool_pins[name] != initial_pins[name] for name in names):
        raise ValueError('selected host tools changed before materialization')
    source = upstream / 'lemonade'
    local_checkout(tools['git'], args.git_cache_root / 'upstream', source,
                   manifest['upstream']['commit'], manifest['upstream']['tree'], env=git_env)
    subprocess.run([str(tools['git']), '-C', str(source), 'apply', '--index', str(patch)], check=True, env=git_env)
    actual_tree = subprocess.check_output([str(tools['git']), '-C', str(source), 'write-tree'], text=True, env=git_env).strip()
    if actual_tree != manifest['oa_delta']['expected_tree']:
        raise ValueError('reconstructed native tree differs')
    # Local checkpoint gives the existing source_archive owner a clean HEAD. Tree is authority.
    subprocess.run([str(tools['git']), '-C', str(source),
        '-c', 'user.name=OA source reconstruction', '-c', 'user.email=source@openabstractions.org',
        '-c', 'commit.gpgsign=false', 'commit', '-m', 'Reconstruct selected OA native tree'], check=True, env=git_env)
    clean_git(source, tree=actual_tree, git_tool=tools['git'], env=git_env)
    deps = upstream / 'deps'
    deps.mkdir()
    for row in manifest['native_dependencies']:
        destination = deps / ('mbedtls/framework' if row['name'] == 'mbedtls-framework' else row['name'])
        local_checkout(tools['git'], args.git_cache_root / row['name'], destination, row['commit'], row['tree'], env=git_env)
        for link in row.get('root_gitlinks', []):
            recorded = subprocess.check_output([str(tools['git']), '-C', str(destination),
                'rev-parse', row['commit'] + ':' + link['path']], text=True, env=git_env).strip()
            if recorded != link['sha']:
                raise ValueError('dependency gitlink differs')
    transform = args.inputs_root / 'libwebsockets-transform.cmake'
    transform.write_bytes(lws_transform(source))
    with tempfile.TemporaryDirectory(prefix='lemonade-lws-', dir=upstream) as temporary:
        disposable = Path(temporary) / 'source'
        row = next(row for row in manifest['native_dependencies'] if row['name'] == 'libwebsockets')
        local_checkout(tools['git'], args.git_cache_root / row['name'], disposable, row['commit'], row['tree'], env=git_env)
        subprocess.run([str(tools['cmake']), '-Dlibwebsockets_SOURCE_DIR=' + str(disposable),
            '-DWIN32=TRUE', '-DLEMONADE_OA_BRANDED_BUILD=TRUE', '-P', str(transform)], check=True)
        (args.inputs_root / 'libwebsockets-configure.patch').write_bytes(lws_diff(tools['git'], disposable, env=git_env))
    # Inventory first rejects links; only the explicitly supplied offline cache is copied.
    inventory(args.npm_cache)
    shutil.copytree(args.npm_cache, args.inputs_root / 'npm-cache')
    for name, path in (('wix-ui.nupkg', args.wix_ui_package), ('WixToolset.UI.wixext.dll', args.wix_ui_extension)):
        shutil.copyfile(regular(path, [path.parent]), args.inputs_root / name)
    ui_rows(args.sources_root, args.inputs_root / 'wix-ui.nupkg', args.inputs_root / 'WixToolset.UI.wixext.dll')
    write_json(args.inputs_root / 'host-tools.json', {'schema': 1, 'tools': {name: pin(path) for name, path in tools.items()}})
    write_json(args.inputs_root / 'selected-toolchain.json', {
        'installation_path': str(selection['installation_path']),
        'installation_version': selection['installation_version'],
        'generator': selection['generator'],
        'tools': {name: dict(pin(path), path=str(path)) for name, path in tools.items()}})
    (args.inputs_root / 'toolchain.cmake').write_text(
        'set(CMAKE_GENERATOR_INSTANCE ' + cmake_literal(str(selection['installation_path']).replace('\\', '/')
            + ',version=' + selection['installation_version']) + ' CACHE STRING "" FORCE)\n'
        'set(CMAKE_MSVC_RUNTIME_LIBRARY "MultiThreaded" CACHE STRING "" FORCE)\n', encoding='utf-8')
    if {name: pin(path) for name, path in tools.items()} != tool_pins:
        raise ValueError('selected host tools changed during materialization')
    for repo, commit in pins.items():
        clean_git(args.sources_root / repo.split('/')[1], commit=commit, git_tool=tools['git'], env=git_env)
    # Caller invokes existing pinned_input_receipt after materialization; no second receipt authority.
    return {'source_root': str(source), 'deps_root': str(deps), 'generator': selection['generator']}


def relative(value):
    if (not isinstance(value, str) or not value or '\\' in value or ':' in value
            or any(part in ('', '.', '..') for part in value.split('/'))):
        raise ValueError('noncanonical relative input')
    return PurePosixPath(value)


def regular(path, roots):
    path = Path(path).absolute()
    for item in (path, *path.parents):
        if item.is_symlink() or getattr(item.lstat(), 'st_file_attributes', 0) & 0x400:
            raise ValueError('linked producer input')
    resolved = path.resolve(strict=True)
    if not resolved.is_file():
        raise ValueError('producer file outside admitted roots')
    for root in roots:
        try:
            _boundary.relative_beneath_existing(resolved, root)
            break
        except ValueError:
            continue
    else:
        raise ValueError('producer file outside admitted roots')
    return resolved


def pin(path):
    data = Path(path).read_bytes()
    return {'sha256': hashlib.sha256(data).hexdigest(), 'bytes': len(data)}


def inventory(root):
    root = Path(root)
    result = {}
    for path in sorted(root.rglob('*')):
        if path.is_symlink() or getattr(path.lstat(), 'st_file_attributes', 0) & 0x400:
            raise ValueError('linked producer inventory')
        if path.is_file():
            result[path.relative_to(root).as_posix()] = pin(regular(path, [root]))
    return result


def check_pin(value):
    if (not isinstance(value, dict) or set(value) != {'sha256', 'bytes'}
            or not isinstance(value['sha256'], str) or not HEX.fullmatch(value['sha256'])
            or type(value['bytes']) is not int or value['bytes'] < 0):
        raise ValueError('invalid input byte pin')


def received_inputs(receipt, inputs_root, sources_root, *, git_tool='git', env=None):
    if env is None:
        env = selected_git_environment(sources_root, Path(inputs_root) / 'upstream/empty-git-hooks')
    document = read_json(receipt)
    if (set(document) != {'schema', 'sources', 'build_inputs'} or document['schema'] != 1
            or not isinstance(document['sources'], dict) or not document['build_inputs']):
        raise ValueError('invalid pinned_input_receipt')
    for repo, commit in document['sources'].items():
        if not re.fullmatch(r'openabstractions/[A-Za-z0-9._-]+', repo) or not COMMIT.fullmatch(commit):
            raise ValueError('invalid selected source')
        clean_git(Path(sources_root) / repo.split('/')[1], commit=commit, git_tool=git_tool, env=env)
    for name in SDK_REPOS:
        if 'openabstractions/' + name not in document['sources']:
            raise ValueError('selected SDK source missing: ' + name)
    if 'openabstractions/abstractions' not in document['sources']:
        raise ValueError('selected charter/signing owner source missing')
    if 'openabstractions/redist' not in document['sources']:
        raise ValueError('selected public redist source missing')
    for name, expected in document['build_inputs'].items():
        check_pin(expected)
        actual = regular(Path(inputs_root) / relative(name), [inputs_root])
        if pin(actual) != expected:
            raise ValueError('received build input differs: ' + name)
    actual = {name: value for name, value in inventory(inputs_root).items()
              if name.split('/')[0] not in ('upstream', 'build')
              and (Path(inputs_root) / name).resolve() != Path(receipt).resolve()}
    if actual != document['build_inputs']:
        raise ValueError('received build input closure differs')
    return document


def clean_git(root, *, commit=None, tree=None, git_tool='git', env=None):
    def git(*args):
        return subprocess.check_output([str(git_tool), '-C', str(root), *args], text=True, env=env).strip()
    if commit is not None and git('rev-parse', 'HEAD') != commit:
        raise ValueError('source commit differs')
    if tree is not None and git('rev-parse', 'HEAD^{tree}') != tree:
        raise ValueError('native reconstructed tree differs')
    if git('status', '--porcelain=v1', '--untracked-files=all'):
        raise ValueError('source checkout is not clean')


def selected_tool(path, received, inputs_root):
    path = regular(path, [inputs_root])
    key = path.relative_to(Path(inputs_root).resolve()).as_posix()
    if received['build_inputs'].get(key) != pin(path):
        raise ValueError('tool is not a measured selected input')
    return str(path)


def host_tools(args, received):
    # The workflow selects installed tools. This measured file binds names to
    # bytes without requiring copies of installed runtime/compiler trees.
    selected_tool(args.host_tools, received, args.inputs_root)
    document = read_json(args.host_tools)
    if (set(document) != {'schema', 'tools'} or document['schema'] != 1
            or set(document['tools']) != set(HOST_TOOLS)):
        raise ValueError('invalid finite host tool bindings')
    paths, facts = {}, {}
    for name in HOST_TOOLS:
        path = Path(getattr(args, name))
        if not path.is_absolute():
            raise ValueError('host tool path must be explicit and absolute')
        path = regular(path, [path.parent])
        expected = document['tools'][name]
        check_pin(expected)
        if pin(path) != expected:
            raise ValueError('host tool differs from selected bytes: ' + name)
        paths[name] = str(path)
        facts[name] = dict(expected, path=str(path))
    return paths, facts


def write_json(path, data):
    Path(path).write_text(json.dumps(data, indent=2) + '\n', encoding='utf-8')


def run(command, *, env):
    subprocess.run([str(part) for part in command], check=True, env=env)


def environment(tools, cache):
    # Explicit build hosts only. No OA routing, credentials or ambient package roots.
    result = {key: os.environ[key] for key in ('SystemRoot', 'WINDIR', 'TEMP', 'TMP') if key in os.environ}
    tool_directories = {str(Path(t).parent) for t in tools}
    if os.name == 'nt':
        # CommonApplicationData expands SystemDrive in MSBuild FileTracker.
        # Derive it from the admitted OS directory, not an ambient drive override.
        system_root = Path(result.get('SystemRoot', ''))
        if (not system_root.is_absolute() or not re.fullmatch(r'[A-Za-z]:', system_root.drive)
                or not system_root.is_dir() or system_root.is_symlink()
                or getattr(system_root.lstat(), 'st_file_attributes', 0) & 0x400):
            raise ValueError('ordinary absolute SystemRoot directory required')
        result['SystemDrive'] = system_root.drive
        system_tools = system_root / 'System32'
        # Existing Windows web staging uses robocopy and npm's command shell.
        for name in ('robocopy.exe', 'cmd.exe'):
            tool_directories.add(str(regular(system_tools / name, [system_tools]).parent))
    result.update(PATH=os.pathsep.join(sorted(tool_directories)),
                  npm_config_cache=str(cache), npm_config_offline='true',
                  npm_config_ignore_scripts='true', npm_config_audit='false', npm_config_fund='false',
                  MSBUILDDISABLENODEREUSE='1')
    return result


def npm_cache(source, inputs_root, received, destination):
    source = Path(source).resolve()
    prefix = _boundary.relative_beneath_existing(source, inputs_root).as_posix() + '/'
    entries = {name[len(prefix):]: value for name, value in received['build_inputs'].items()
               if name.startswith(prefix)}
    if not entries:
        raise ValueError('missing pinned offline npm cache')
    for name in entries:
        target = destination / relative(name)
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(regular(source / name, [inputs_root]), target)


def ui_rows(sources_root, package, extension):
    # This shared owner authenticates the pinned package/DLL and source wix-ir.
    root = sources_root / 'redist'
    path = regular(root / 'windows/wix_packages.py', [root])
    owner = types.ModuleType('lemonade_shared_wix_packages')
    owner.__file__ = str(path)
    # Execute the exact selected source without writing bytecode into it.
    exec(compile(path.read_bytes(), str(path), 'exec'), owner.__dict__)
    selected = owner.PACKAGES['WixToolset.UI.wixext']
    if pin(extension) != {'sha256': selected['dll_sha256'], 'bytes': selected['dll_bytes']}:
        raise ValueError('selected UI extension DLL differs from package source')
    rows = owner.ui_source_resources(package, architecture='x64', required_ids=list(UI_IDS))
    return [{key: row[key] for key in ('id', 'sha256', 'bytes')} for row in rows.values()]


def cmake_build(cmake, build, env, *targets):
    command = [cmake, '--build', build, '--config', 'Release', '--parallel', '1']
    if targets:
        command += ['--target', *targets]
    run([*command, '--', '/nodeReuse:false'], env=env)


def source_export(source, out, version, python, env):
    run([python, source / 'tools/source_archive.py', '--output-dir', out,
         '--version', version], env=env)
    archive = out / ('lemonade-' + version + '.tar.gz')
    with tarfile.open(archive, 'r:gz') as package:
        members = package.getmembers()
        for member in members:
            relative(member.name.rstrip('/'))
            if not (member.isfile() or member.isdir()):
                raise ValueError('source archive contains a linked or special member')
        package.extractall(out / 'source', members=members, filter='data')
    return out / 'source' / ('lemonade-' + version), archive


def prepare(args):
    own_sources = producer_sources()
    manifest = read_json(HERE / 'native-inputs.json')
    tools, tool_facts = host_tools(args, read_json(args.input_receipt))
    git = tools['git']
    hooks = args.inputs_root / 'upstream/empty-git-hooks'
    git_env = selected_git_environment(args.sources_root, hooks)
    received = received_inputs(args.input_receipt, args.inputs_root, args.sources_root, git_tool=git, env=git_env)
    clean_git(args.source_root, tree=manifest['oa_delta']['expected_tree'], git_tool=git, env=git_env)
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=False)
    selected_tool(args.wix_ui_package, received, args.inputs_root)
    extension = selected_tool(args.wix_ui_extension, received, args.inputs_root)
    ui_rows(args.sources_root, args.wix_ui_package, args.wix_ui_extension)
    toolchain = selected_tool(args.toolchain, received, args.inputs_root)
    npm_cache(args.npm_cache, args.inputs_root, received, out / 'npm-cache')
    env = selected_git_environment(args.sources_root, hooks, environment(tools.values(), out / 'npm-cache'))
    version = manifest['upstream']['stable_tag'].removeprefix('v') + '+oa.' + args.version
    source, archive = source_export(args.source_root, out, version, tools['python'], env)
    prefix = out / 'sdk'
    sdk_builds = []
    for name in ('abstraction-facade', 'abstraction-download'):
        build = out / (name + '-build')
        command = [tools['cmake'], '-S', args.sources_root / name / 'cpp', '-B', build,
                   '-G', args.generator, '-A', 'x64', '-DCMAKE_INSTALL_PREFIX=' + str(prefix),
                   '-DCMAKE_TOOLCHAIN_FILE=' + toolchain, '-DCMAKE_MSVC_RUNTIME_LIBRARY=MultiThreaded',
                   '-DCMAKE_MAKE_PROGRAM=' + tools['msbuild'],
                   *['-D' + key + '=' + value for key, value in FIND_FLAGS.items()]]
        if name == 'abstraction-facade':
            command += ['-DABSTRACTION_FACADE_BUILD_AGGREGATE=OFF',
                        '-DABSTRACTION_FACADE_BUILD_JOBS=ON', '-DABSTRACTION_FACADE_BUILD_LOGGING=ON']
        run(command, env=env)
        cmake_build(tools['cmake'], build, env)
        run([tools['cmake'], '--install', build, '--config', 'Release'], env=env)
        sdk_builds.append(str(build.relative_to(out)))
    definitions = {'LEMONADE_OPENABSTRACTIONS': 'ON', 'LEMONADE_OA_BRANDED_BUILD': 'ON',
                   'LEMONADE_OA_PACKAGE_MODE': 'release', 'LEMONADE_OA_QA_LABEL': '',
                   'LEMONADE_OA_MSI_VERSION': args.version,
                   'LEMONADE_OA_WIX_UI_EXTENSION': extension,
                   'LEMONADE_OA_SDK_LICENSE': str(args.sources_root / 'abstraction-identity/LICENSE'),
                   'CMAKE_PREFIX_PATH': str(prefix), 'CMAKE_TOOLCHAIN_FILE': toolchain,
                   'CMAKE_MSVC_RUNTIME_LIBRARY': 'MultiThreaded',
                   'FETCHCONTENT_FULLY_DISCONNECTED': 'ON', 'FETCHCONTENT_UPDATES_DISCONNECTED': 'ON',
                   **FIND_FLAGS,
                   'CMAKE_DISABLE_FIND_PACKAGE_PkgConfig': 'TRUE',
                   'CMAKE_DISABLE_FIND_PACKAGE_nlohmann_json': 'TRUE',
                   'CMAKE_DISABLE_FIND_PACKAGE_httplib': 'TRUE',
                   'Python3_EXECUTABLE': tools['python'], 'NODE_EXECUTABLE': tools['node'],
                   'NPM_EXECUTABLE': tools['npm'], 'WIX_EXECUTABLE': tools['wix'],
                   'CMAKE_MAKE_PROGRAM': tools['msbuild'], 'BUILD_WEB_APP': 'ON',
                   'BUILD_TAURI_APP': 'OFF', 'BUILD_TESTING': 'OFF'}
    for row in manifest['native_dependencies']:
        root = args.deps_root / row.get('destination', row['name']).removeprefix('DEPS_DIR/')
        clean_git(root, commit=row['commit'], tree=row['tree'], git_tool=git, env=git_env)
        if 'destination' not in row:
            definitions['FETCHCONTENT_SOURCE_DIR_' + row['name'].upper()] = str(root.resolve())
    definitions['CLI11_INCLUDE_DIRS'] = str((args.deps_root / 'cli11/include').resolve())
    build = out / 'build'
    run([tools['cmake'], '-S', source, '-B', build, '-G', args.generator, '-A', 'x64',
         *['-D' + key + '=' + value for key, value in definitions.items()]], env=env)
    # The one intentional upstream configure transform must match a received byte pin.
    transform = lws_diff(git, args.deps_root / 'libwebsockets', env=git_env)
    expected = received['build_inputs'].get('libwebsockets-configure.patch')
    if expected != {'sha256': hashlib.sha256(transform).hexdigest(), 'bytes': len(transform)}:
        raise ValueError('unreviewed libwebsockets configure transform')
    for row in manifest['native_dependencies']:
        if row['name'] != 'libwebsockets':
            root = args.deps_root / row.get('destination', row['name']).removeprefix('DEPS_DIR/')
            clean_git(root, commit=row['commit'], tree=row['tree'], git_tool=git, env=git_env)
    cmake_build(tools['cmake'], build, env, 'lemond', 'lemonade', 'LemonadeServer', 'web-app')
    outputs = inventory(build / 'Release')
    if {name for name in outputs if name.lower().endswith(('.exe', '.dll'))} != set(OWN_PE):
        raise ValueError('native executable output roster differs')
    received_inputs(args.input_receipt, args.inputs_root, args.sources_root, git_tool=git, env=git_env)
    if host_tools(args, received)[1] != tool_facts:
        raise ValueError('host tool changed during preparation')
    sdk = inventory(prefix)
    if (not any(name.startswith('include/') for name in sdk)
            or not any(name.endswith('.lib') for name in sdk)):
        raise ValueError('SDK install lacks consumed headers/static library')
    receipt = {'schema': 1, 'id': 'lemonade', 'version': args.version,
               'source_tree': manifest['oa_delta']['expected_tree'], 'source_archive': pin(archive),
               'received_input_receipt': pin(args.input_receipt), 'selected_inputs': received,
               'wix_ui_package': pin(args.wix_ui_package), 'wix_ui_extension': pin(args.wix_ui_extension),
               'tools': tool_facts, 'producer_sources': own_sources,
               'sdk': sdk, 'payload': outputs,
               'resources': inventory(build / 'resources'), 'source_files': inventory(source),
               'package_project': pin(build / 'wix_installer_minimal.vcxproj'),
               'product_source': pin(build / 'installer/Product.wxs'),
               'cmake_cache': pin(build / 'CMakeCache.txt'),
               'source': source.relative_to(out).as_posix(), 'build': 'build', 'sdk_builds': sdk_builds,
               'own_pe': [{'table': 'File', 'id': name, 'file': 'build/Release/' + name,
                           **outputs[name]} for name in OWN_PE]}
    if producer_sources() != own_sources:
        raise ValueError('producer sources changed during preparation')
    write_json(out / 'native-receipt.json', receipt)
    return receipt


def expand(value, variables):
    for name, replacement in variables.items():
        value = value.replace('$(var.' + name + ')', str(replacement))
    if '$(' in value or re.search(r'@[A-Za-z_][A-Za-z0-9_]*@', value):
        raise ValueError('unresolved WiX source symbol')
    return value.replace('\\', '/')


def source_xml(path, variables):
    """Read this app's finite configured WiX source, never compiled MSI tables."""
    text = Path(path).read_text(encoding='utf-8')
    active, output = [True], []
    for part in re.split(r'(<\?.*?\?>)', text, flags=re.S):
        if not part.startswith('<?') or part.startswith('<?xml '):
            if all(active):
                output.append(part)
            continue
        directive = part[2:-2].strip()
        if directive.startswith('ifndef '):
            active.append(directive[7:].strip() not in variables)
        elif directive.startswith('define '):
            if all(active):
                raise ValueError('unexpected active WiX default')
        elif directive.startswith('if '):
            condition = expand(directive[3:].strip(), variables)
            match = re.fullmatch(r'([01])\s*=\s*([01])', condition)
            if not match:
                raise ValueError('unsupported WiX source condition')
            active.append(match[1] == match[2])
        elif directive == 'else' and len(active) > 1:
            active[-1] = not active[-1]
        elif directive == 'endif' and len(active) > 1:
            active.pop()
        else:
            raise ValueError('unsupported WiX source directive')
    if len(active) != 1:
        raise ValueError('unclosed WiX source condition')
    return ET.fromstring(''.join(output))


def source_payload(product, fragments, variables, roots, supplier, *, capture=None, native_root=None):
    trees = [source_xml(path, variables) for path in (product, *fragments)]
    directories = {}
    tag = lambda node: node.tag.rsplit('}', 1)[-1]

    def dirs(node, parent=()):
        kind = tag(node)
        if kind == 'StandardDirectory':
            parent = (node.attrib['Id'],)
        elif kind == 'Directory':
            parent = (*parent, node.attrib['Name'])
            relative('/'.join(parent))
        if kind in ('StandardDirectory', 'Directory'):
            key = node.attrib['Id']
            if key in directories and directories[key] != parent:
                raise ValueError('conflicting WiX directory ID')
            directories[key] = parent
        for child in node:
            dirs(child, parent)
    dirs(trees[0])
    # Harvested fragments may add nested directories to a declared root.
    for tree in trees[1:]:
        for node in tree.iter():
            if tag(node) == 'DirectoryRef':
                if node.attrib['Id'] not in directories:
                    raise ValueError('foreign fragment directory')
                for child in node:
                    dirs(child, directories[node.attrib['Id']])
    result = {'files': [], 'binaries': [], 'icons': []}
    seen = set()

    def walk(node, directory=()):
        kind = tag(node)
        if kind in ('StandardDirectory', 'Directory', 'DirectoryRef'):
            directory = directories[node.attrib['Id']]
        if kind in ('Component', 'ComponentGroup') and 'Directory' in node.attrib:
            directory = directories[node.attrib['Directory']]
        if kind in ('File', 'Icon'):
            key = (kind, node.attrib['Id'])
            if key in seen:
                raise ValueError('duplicate source payload ID')
            seen.add(key)
            raw = node.attrib['Source' if kind == 'File' else 'SourceFile']
            source = regular(expand(raw, variables), roots)
            if capture is not None:
                capture[source.relative_to(native_root.resolve()).as_posix()] = pin(source)
            row = dict(pin(source), id=key[1])
            if kind == 'File':
                if not directory or directory[0] not in ('LocalAppDataFolder',):
                    raise ValueError('unanchored native File destination')
                name = node.get('Name', source.name)
                relative(name)
                if '/' in name:
                    raise ValueError('invalid WiX filename')
                row['path'] = '/'.join((*directory, name))
            result['files' if kind == 'File' else 'icons'].append(row)
        for child in node:
            walk(child, directory)
    for tree in trees:
        walk(tree)
    result['binaries'] = [dict(row) for row in supplier]
    overrides = {'WixUIDialogBmp': 'WixUI_Bmp_Dialog', 'WixUIBannerBmp': 'WixUI_Bmp_Banner'}
    for node in trees[0].iter():
        if tag(node) == 'Binary':
            raise ValueError('new native Binary requires source-role review')
        if tag(node) == 'WixVariable' and node.get('Id') in overrides:
            key = overrides[node.get('Id')]
            rows = [row for row in result['binaries'] if row['id'] == key]
            if len(rows) != 1:
                raise ValueError('missing selected WiX bitmap override row')
            source = regular(expand(node.attrib['Value'], variables), roots)
            if capture is not None:
                capture[source.relative_to(native_root.resolve()).as_posix()] = pin(source)
            rows[0].update(pin(source))
    for field in result:
        if len({row['id'] for row in result[field]}) != len(result[field]):
            raise ValueError('duplicate payload row')
    if len({row['path'] for row in result['files']}) != len(result['files']):
        raise ValueError('duplicate installed path')
    return result


def package_variables(build, source):
    return {'BuildDir': build.resolve(), 'CppSourceDir': source / 'src/cpp',
            'SourceDir': source, 'IncludeTauriApp': '0', 'IncludeWebApp': '1',
            'WebAppSourceDir': build / 'resources/web-app', 'DocsSourceDir': build / 'resources/docs',
            'OALicensesSourceDir': build / 'resources/licenses'}


def package_fragments(build):
    return [build / 'installer' / name for name in
            ('WebAppFragment.minimal.wxs', 'DocsFragment.minimal.wxs', 'OALicenses.wxs')]


def generate_package_fragments(python, build, source, env):
    script = regular(source / 'src/cpp/installer/generate_tauri_fragment.py', [source])
    for folder, target, group, root, variable, prefix in (
            ('web-app','WebAppFragment.minimal.wxs','WebAppComponents','WebAppDir','WebAppSourceDir','OpenAbstractionsLemonadeWeb'),
            ('docs','DocsFragment.minimal.wxs','DocsComponents','DocsDir','DocsSourceDir','OpenAbstractionsLemonadeDocs'),
            ('licenses','OALicenses.wxs','OALicenseComponents','OALicensesDir','OALicensesSourceDir','OpenAbstractionsLemonadeLicenses')):
        run([python, script, '--source', build / 'resources' / folder, '--output', build / 'installer' / target,
             '--component-group', group, '--root-id', root, '--path-variable', variable, '--id-prefix', prefix], env=env)


def check_package_inputs(root, inputs):
    if not isinstance(inputs, dict) or not inputs:
        raise ValueError('missing measured package inputs')
    for name, expected in inputs.items():
        path = regular(root / relative(name), [root])
        if path.stat().st_nlink != 1 or pin(path) != expected:
            raise ValueError('measured package input differs')


def package_wix(wix, build, source, extension, output, env, *, runner=None):
    """The single package-only WiX invocation for hosted and local callers."""
    variables = package_variables(build, source)
    (run if runner is None else runner)([wix, 'build', '-arch', 'x64', '-ext', extension,
        '-intermediateFolder', build / 'wix-package',
        *[item for key, value in variables.items() for item in ('-d', key + '=' + str(value))],
        '-out', output, build / 'installer/Product.wxs', *package_fragments(build)], env=env)


def package(args):
    packet_paths = [args.input_receipt, args.signatures, args.wix_ui_package, args.wix_ui_extension,
                    args.native_root / 'native-receipt.json']
    packet_pins = [pin(path) for path in packet_paths]
    tools, tool_facts = host_tools(args, read_json(args.input_receipt))
    git = tools['git']
    hooks = args.inputs_root / 'upstream/empty-git-hooks'
    git_env = selected_git_environment(args.sources_root, hooks)
    received = received_inputs(args.input_receipt, args.inputs_root, args.sources_root, git_tool=git, env=git_env)
    native = read_json(args.native_root / 'native-receipt.json')
    if native.get('producer_sources') != producer_sources():
        raise ValueError('prepared producer sources differ')
    if native['version'] != args.version or native['received_input_receipt'] != pin(args.input_receipt):
        raise ValueError('native preparation/input receipt differs')
    if native['selected_inputs'] != received or inventory(args.native_root / 'sdk') != native['sdk']:
        raise ValueError('selected SDK inputs differ')
    if tool_facts != native['tools']:
        raise ValueError('prepared tool selection differs')
    signatures = read_json(args.signatures)
    proofs = {row['name']: row for row in signatures['files']}
    if signatures.get('schema') != 1 or signatures.get('result') != 'pass' or len(proofs) != len(signatures['files']):
        raise ValueError('invalid received signature measurements')
    build = args.native_root / relative(native['build'])
    if (pin(build / 'wix_installer_minimal.vcxproj') != native['package_project']
            or pin(build / 'installer/Product.wxs') != native['product_source']
            or pin(build / 'CMakeCache.txt') != native['cmake_cache']
            or inventory(build / 'resources') != native['resources']):
        raise ValueError('prepared package inputs differ')
    before = inventory(build / 'Release')
    if set(proofs) != {'Release/' + name for name in OWN_PE}:
        raise ValueError('received own PE roster differs')
    for name, expected in native['payload'].items():
        actual = before.get(name)
        if name in OWN_PE:
            if actual != {key: proofs['Release/' + name][key] for key in ('sha256', 'bytes')}:
                raise ValueError('signed PE differs from received proof')
        elif actual != expected:
            raise ValueError('prepared non-PE payload changed')
    if set(before) != set(native['payload']):
        raise ValueError('native payload grew after preparation')
    selected_tool(args.wix_ui_package, received, args.inputs_root)
    selected_tool(args.wix_ui_extension, received, args.inputs_root)
    if pin(args.wix_ui_package) != native['wix_ui_package'] or pin(args.wix_ui_extension) != native['wix_ui_extension']:
        raise ValueError('prepared WiX UI selection differs')
    binaries = ui_rows(args.sources_root, args.wix_ui_package, args.wix_ui_extension)
    if {row['id'] for row in binaries} != set(UI_IDS) or len(binaries) != len(UI_IDS):
        raise ValueError('selected WiX Binary source roster differs')
    source = args.native_root / relative(native['source'])
    unchanged_source = inventory(source)
    if unchanged_source != native['source_files']:
        raise ValueError('native source changed after preparation')
    env = selected_git_environment(args.sources_root, hooks, environment(tools.values(), args.native_root / 'npm-cache'))
    generate_package_fragments(tools['python'], build, source, env)
    msi_name = ('lemonade-' + read_json(HERE / 'native-inputs.json')['upstream']['stable_tag'].removeprefix('v')
                + '-oa-' + args.version + '-windows-x64.msi')
    inputs = {path.relative_to(args.native_root).as_posix(): pin(path)
              for path in [build / 'installer/Product.wxs', *package_fragments(build)]}
    variables = package_variables(build, source)
    payload = source_payload(build / 'installer/Product.wxs', package_fragments(build), variables,
                             [source, build], binaries, capture=inputs, native_root=args.native_root)
    check_package_inputs(args.native_root, inputs)
    package_wix(tools['wix'], build, source, args.wix_ui_extension, source / msi_name, env)
    check_package_inputs(args.native_root, inputs)
    source_after = inventory(source)
    for generated in (msi_name, str(Path(msi_name).with_suffix('.wixpdb'))):
        source_after.pop(generated, None)
    if (inventory(build / 'Release') != before or source_after != unchanged_source
            or pin(build / 'installer/Product.wxs') != native['product_source']
            or inventory(build / 'resources') != native['resources']):
        raise ValueError('packaging altered a prepared or signed input')
    msi = source / msi_name
    args.out.mkdir(parents=True, exist_ok=False)
    target = args.out / msi.name
    target.write_bytes(regular(msi, [source]).read_bytes())
    received_inputs(args.input_receipt, args.inputs_root, args.sources_root, git_tool=git, env=git_env)
    if (native['producer_sources'] != producer_sources()
            or [pin(path) for path in packet_paths] != packet_pins or host_tools(args, received)[1] != tool_facts):
        raise ValueError('producer receipt changed during packaging')
    app = {'id': 'lemonade', 'title': 'Lemonade (OA build)', 'file': target.name, **pin(target)}
    write_json(args.out / 'bundle-app.json', app)
    receipt = {'schema': 1, 'id': 'lemonade', 'version': args.version, 'platform': 'windows/amd64',
               'status': 'Unsigned MSI; independent source/signature/readback admission required',
               'received_input_receipt': pin(args.input_receipt),
               'native_receipt': pin(args.native_root / 'native-receipt.json'),
               'signatures_receipt': pin(args.signatures),
               'package_inputs': inputs,
               'expected_payload': {'schema': 1, 'msi': dict(pin(target), name=target.name), **payload},
               'bundle_app_sha256': pin(args.out / 'bundle-app.json')['sha256']}
    write_json(args.out / 'installer-receipt.json', receipt)
    return receipt


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='phase')
    material = sub.add_parser('materialize', help='offline reconstruction from caller-selected local inputs')
    for option in ('pins', 'sources-root', 'git-cache-root', 'inputs-root', 'npm-cache',
                   'wix-ui-package', 'wix-ui-extension', 'git', 'python', 'node', 'npm', 'wix'):
        material.add_argument('--' + option, type=Path, required=True)
    for phase in ('prepare-native', 'package'):
        command = sub.add_parser(phase)
        for option in ('input-receipt', 'inputs-root', 'sources-root', 'out', 'cmake', 'python',
                       'node', 'npm', 'wix', 'msbuild', 'git', 'host-tools', 'npm-cache', 'wix-ui-package', 'wix-ui-extension'):
            command.add_argument('--' + option, type=Path, required=True)
        command.add_argument('--version', required=True)
        if phase == 'prepare-native':
            for option in ('source-root', 'deps-root', 'toolchain'):
                command.add_argument('--' + option, type=Path, required=True)
            command.add_argument('--generator', default='Visual Studio 18 2026')
        else:
            for option in ('native-root', 'signatures'):
                command.add_argument('--' + option, type=Path, required=True)
    args = parser.parse_args(argv)
    if not args.phase:
        parser.print_help()
        return 0
    if args.phase == 'materialize':
        print(json.dumps(materialize(args), sort_keys=True))
        return 0
    if not re.fullmatch(r'0\.8\.(0|[1-9][0-9]*)', args.version):
        raise ValueError('native recipe requires the selected 0.8 Plan version')
    (prepare if args.phase == 'prepare-native' else package)(args)
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
