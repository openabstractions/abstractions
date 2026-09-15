"""Locate service-check sources in the private tree or sibling public checkouts.

No cloning, installation or source changes. A public checkout needs the relevant
abstraction repositories beside `abstractions/`; the temporary Go workspace lives
under the check's build directory. Go module downloads respect GOPROXY (off by
default for these checks).
"""
from contextlib import contextmanager
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

def source_revision():
    """Print and return the source revision this run measures.

    A git checkout reports HEAD and any uncommitted paths. An extracted tree
    (for example an archive run on WSL) reports OA_SOURCE_REVISION, or says the
    revision is unrecorded.
    """
    if (ROOT / '.git').exists():
        revision = subprocess.run(['git', 'rev-parse', 'HEAD'], cwd=ROOT, check=True,
                                  capture_output=True, text=True, timeout=60).stdout.strip()
        dirty = subprocess.run(['git', 'status', '--short'], cwd=ROOT, check=True,
                               capture_output=True, text=True, timeout=60).stdout
        print('source revision:', revision, '(uncommitted changes present)' if dirty.strip() else '', flush=True)
        return revision
    revision = os.environ.get('OA_SOURCE_REVISION', '')
    print('source revision:', revision or 'unrecorded (no .git; set OA_SOURCE_REVISION)', flush=True)
    return revision

def resolved_executable(name):
    """The file a Program proof names for a command: PATH lookup, then symlinks resolved."""
    found = shutil.which(str(name)) or str(name)
    path = Path(found)
    if not path.is_file():
        raise RuntimeError('executable not found: ' + str(name))
    return path.resolve()

DRY_RUN_HELP = 'stop after argument handling, toolchain selection and source revision; build and run nothing'

def dry_run_stop(runner, args):
    """End a --dry-run: report the toolchain a --run would select, then exit 0.

    A selection refusal is reported rather than raised: the dry run proves the
    runner reaches selection, and the host may lack the toolchain.
    """
    print('dry run', runner + ': go =', shutil.which('go') or 'unavailable: not on PATH', flush=True)
    if hasattr(args, 'cmake'):
        try:
            cmake = cmake_for(dict(os.environ), args.cmake)
        except SystemExit as refusal:
            cmake = 'unavailable: ' + str(refusal)
        print('dry run', runner + ': cmake =', cmake, flush=True)
    if hasattr(args, 'node'):
        print('dry run', runner + ': node =', shutil.which(str(args.node)) or 'unavailable: not on PATH', flush=True)
    print('DRY RUN', runner + ': stopped before building', flush=True)
    raise SystemExit(0)

# Fixture host identity and endpoints, shared by proofs that start their own
# providers. The logging service attests the caller as identity/<GOOS>, with the
# account SID in "user" on Windows and the POSIX uid in "uid" elsewhere.
ATTESTATION_BY = 'identity/' + ('windows' if os.name == 'nt' else 'darwin' if sys.platform == 'darwin' else 'linux')

def attested_principal(peer):
    """Whether a logging attestation names a principal in this platform's field."""
    if os.name == 'nt':
        return bool(peer.get('user'))
    return peer.get('uid') == os.getuid()

@contextmanager
def fixture_endpoints(names, token):
    """Private local endpoints: named pipes on Windows, Unix sockets elsewhere.

    Socket paths are length-bounded, so they live in a short private temporary
    directory that is removed on exit.
    """
    if os.name == 'nt':
        yield {name: '\\\\.\\pipe\\oa-' + token + '-' + name for name in names}
        return
    directory = tempfile.mkdtemp(prefix='oa-')
    try:
        yield {name: os.path.join(directory, name) for name in names}
    finally:
        shutil.rmtree(directory, ignore_errors=True)

def host_program(build, name):
    """Path for a Go fixture host built into build."""
    return Path(build) / (name + ('.exe' if os.name == 'nt' else ''))

def consumer_executable(build, name):
    """A CMake consumer's executable: Release/<name>.exe for Visual Studio, <name> for single-config generators."""
    return Path(build) / 'Release' / (name + '.exe') if os.name == 'nt' else Path(build) / name

MSVC_RECIPE = ('Run it from a Visual Studio 18 x64 developer environment: write a .bat that calls '
               '"C:\\Program Files\\Microsoft Visual Studio\\18\\Community\\VC\\Auxiliary\\Build\\vcvars64.bat" '
               'and then this runner, and invoke that .bat by absolute path.')

def compiler_metadata(build):
    found = {}
    for source in sorted((Path(build) / 'CMakeFiles').glob('*/CMakeCXXCompiler.cmake')):
        for key, value in re.findall(r'set\(CMAKE_CXX_COMPILER_(ID|VERSION) "([^"]+)"\)', source.read_text()):
            print('C++ compiler', key, value)
            found[key] = value
    return found

def _under(path, directory):
    if not path or not directory:
        return False
    path = os.path.normcase(os.path.abspath(path))
    directory = os.path.normcase(os.path.abspath(directory))
    return path == directory or path.startswith(directory.rstrip('\\/') + os.sep)

def msvc_toolchain(env):
    """Return Visual Studio's CMake for the MSVC toolchain these Windows proofs certify.

    Refuses before any build when the environment is not a vcvars64 developer
    environment, when cl.exe is not Visual Studio's, or when an explicit CMAKE
    names another CMake. CC and CXX are removed from env: the Visual Studio
    generator selects cl itself, and a leftover MinGW value would override it.
    """
    if os.name != 'nt':
        raise SystemExit('This proof certifies MSVC on Windows only.')
    vc, vs = env.get('VCINSTALLDIR'), env.get('VSINSTALLDIR')
    cl = shutil.which('cl', path=env.get('PATH', ''))
    if not vc or not vs or not _under(cl, vc):
        raise SystemExit('This proof certifies the MSVC toolchain and found ' +
                         ('cl.exe at ' + cl if cl else 'no MSVC cl.exe') + '. ' + MSVC_RECIPE)
    bundled = Path(vs) / 'Common7' / 'IDE' / 'CommonExtensions' / 'Microsoft' / 'CMake' / 'CMake' / 'bin' / 'cmake.exe'
    cmake = env.get('CMAKE') or (str(bundled) if bundled.is_file() else None)
    if not cmake or not _under(cmake, vs):
        raise SystemExit('This proof certifies MSVC with Visual Studio\'s CMake and found CMake ' +
                         str(cmake) + '. Unset CMAKE or point it at Visual Studio\'s cmake.exe. ' + MSVC_RECIPE)
    env.pop('CC', None)
    env.pop('CXX', None)
    return cmake

def certify_msvc(build):
    """Refuse a configured build tree whose C++ compiler is not MSVC."""
    found = compiler_metadata(build)
    if found.get('ID') != 'MSVC':
        raise SystemExit('Configured C++ compiler is ' + found.get('ID', 'unknown') +
                         ' in ' + str(build) + '; this proof certifies MSVC. ' + MSVC_RECIPE)

CERTIFIES = ('On Windows this proof certifies MSVC: run it from a vcvars64 developer environment. '
             'Other toolchains are refused before building.')

def cmake_for(env, requested=None):
    """CMake for a C++ proof: MSVC-certified on Windows, else --cmake, CMAKE or cmake.

    An explicit request is recorded as CMAKE so msvc_toolchain can refuse a
    CMake outside Visual Studio.
    """
    if requested:
        env['CMAKE'] = str(requested)
    if os.name == 'nt':
        return msvc_toolchain(env)
    return env.get('CMAKE') or 'cmake'

def certify_compiler(build):
    """Print the configured compiler; on Windows refuse any compiler but MSVC."""
    if os.name == 'nt':
        certify_msvc(build)
    else:
        compiler_metadata(build)

def build_root():
    """Short, non-temporary root for fresh per-run build trees.

    MSBuild refuses output under the Temporary directory and paths past 260
    characters, so runners create short uniquely named trees here.
    """
    root = ROOT / '.build'
    root.mkdir(parents=True, exist_ok=True)
    return root

def layer(name):
    base = ROOT / 'openabstractions-flat'
    if not base.is_dir():
        base = ROOT.parent
    result = base / name
    if not result.is_dir():
        raise RuntimeError('Missing source checkout: ' + str(result))
    return result

def environment(build):
    work = ROOT / 'go.work'
    if not work.is_file():
        base = ROOT.parent
        modules = [ROOT / 'serve', ROOT / 'conformance' / 'clients' / 'fixture']
        for repository in sorted(base.glob('abstraction-*')):
            for module in (repository, repository / 'go'):
                if (module / 'go.mod').is_file():
                    modules.append(module)
        build = Path(build)
        build.mkdir(parents=True, exist_ok=True)
        work = build / 'go.work'
        work.write_text('go 1.26.0\n\nuse (\n' + ''.join(
            '  ' + json.dumps(os.path.relpath(p.resolve(), build.resolve())) + '\n' for p in modules) + ')\n', encoding='utf-8')
    return dict(os.environ, GOWORK=str(work), GOPROXY=os.environ.get('GOPROXY', 'off'))


# Go template programs built outside go.work.
#
# A conformance runner that needs a program pinned to published modules, or to a
# chosen set of local sources, keeps it as a template carrying //go:build ignore,
# so no workspace build compiles it. go_template_binary copies it into a
# generated module and builds it there.

def go_workspace_modules(root=None):
    """Map module path to directory for every `use` in go.work."""
    root = Path(root) if root is not None else ROOT
    text = (root / 'go.work').read_text(encoding='utf-8')
    uses = []
    for block in re.findall(r'^use\s*\((.*?)^\)', text, re.M | re.S):
        uses += [line.split('//')[0].strip() for line in block.splitlines()]
    uses += re.findall(r'^use\s+([^\s(]+)', text, re.M)
    modules = {}
    for use in filter(None, uses):
        directory = (root / use.strip('"')).resolve()
        match = re.search(r'^module\s+(\S+)', (directory / 'go.mod').read_text(encoding='utf-8'), re.M)
        if not match:
            raise SystemExit('no module line in ' + str(directory / 'go.mod'))
        modules[match.group(1)] = directory
    return modules


def go_requirements(gomod):
    """Module paths named in require directives, block or single-line."""
    text = Path(gomod).read_text(encoding='utf-8')
    required = []
    for block in re.findall(r'^require\s*\((.*?)^\)', text, re.M | re.S):
        required += [line.split('//')[0].split()[0] for line in block.splitlines() if line.split('//')[0].split()]
    required += re.findall(r'^require\s+([^\s(]+)\s+\S+', text, re.M)
    return required


def go_replace_closure(roots, modules):
    """Workspace modules reachable from roots through require directives.

    Each one needs a replace directive in a module built outside go.work that
    must compile against current sources. Refuses a root go.work does not use.
    """
    seen, stack = set(), list(roots)
    while stack:
        module = stack.pop()
        if module in seen or module not in modules:
            continue
        seen.add(module)
        stack += [r for r in go_requirements(modules[module] / 'go.mod') if r in modules]
    missing = [r for r in roots if r not in seen]
    if missing:
        raise SystemExit('go.work does not use ' + ', '.join(missing))
    return sorted(seen)


def strip_go_build_constraints(text):
    """Remove //go:build lines, and one blank line after each, above the package clause."""
    lines, kept, header, skip_blank = text.splitlines(keepends=True), [], True, False
    for line in lines:
        if header and line.startswith('package '):
            header = False
        if header and line.startswith('//go:build'):
            skip_blank = True
            continue
        if skip_blank and not line.strip():
            skip_blank = False
            continue
        skip_blank = False
        kept.append(line)
    return ''.join(kept)


def go_template_binary(template, work, name, requires, replaces=(), env=None, go_version='1.26.0'):
    """Build a Go template as main.go of a generated module outside go.work.

    requires is [(module, version)] and replaces is [(module, directory)]. The
    module lives at <work>/<name> and the binary at <work>/<name>-bin[.exe]: the
    two paths differ on every platform, whereas an unsuffixed binary named after
    its module directory makes go build write into that directory. The build
    uses GOWORK=off and -mod=mod; a GOPROXY of off is dropped, because a
    template pinned to published modules must fetch them.
    """
    import subprocess
    work = Path(work)
    module = work / name
    module.mkdir(parents=True)
    lines = ['module ' + re.sub(r'[^A-Za-z0-9]', '', name) + 'template', '', 'go ' + go_version, '']
    if requires:
        lines += ['require ('] + ['\t%s %s' % (m, v) for m, v in requires] + [')', '']
    lines += ['replace %s => %s' % (m, json.dumps(Path(d).as_posix())) for m, d in replaces]
    (module / 'go.mod').write_text('\n'.join(lines) + '\n', encoding='utf-8')
    source = strip_go_build_constraints(Path(template).read_text(encoding='utf-8'))
    (module / 'main.go').write_text(source, encoding='utf-8')
    build_env = dict(env if env is not None else os.environ, GOWORK='off', GOFLAGS='-mod=mod')
    if build_env.get('GOPROXY') == 'off':
        build_env.pop('GOPROXY')
    binary = work / (name + '-bin' + ('.exe' if os.name == 'nt' else ''))
    result = subprocess.run(['go', 'build', '-o', str(binary), '.'], cwd=str(module), env=build_env,
                            capture_output=True, text=True, encoding='utf-8', errors='replace', timeout=900)
    if result.returncode:
        raise SystemExit('go build of ' + str(template) + ' failed:\n' + result.stdout + result.stderr)
    return binary
