"""Run the OpenCode adopter's Node tests (npm test), installing their dependencies first when absent.

The tests import linked @openabstractions packages from this checkout and the
third-party closure the package's lock pins. The OA packages are not on npm, so
the install links them from openabstractions-flat/ and takes the rest from the
lock, without rewriting package.json or the lock:

  npm install --ignore-scripts --no-save --no-audit --no-fund <local package directories>

The install runs only when node_modules lacks one of the packages the tests
import, and it reaches the npm registry or cache for the third-party closure.
The links it creates under node_modules/@openabstractions (junctions on
Windows) point into openabstractions-flat/; after the tests the run removes the
links it created, the link itself and never what it points to, so a later
recursive delete of node_modules cannot follow one into the checkout.

On Windows the run first builds the @openabstractions/ipc development addon
(native/oa_ipc_node.node) from this source with scripts/build_node_addon.py.
Serve receives that build; the IPC producer downloads nothing.

Tests that need Bun skip under node --test and run by hand with `bun test`.
Exit status is npm's.

  py adopters/opencode/test/run_node_tests.py --run               install if needed, then npm test
  py adopters/opencode/test/run_node_tests.py --run --no-install  fail instead of installing

Without --run it prints this help.
"""
import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

ADOPTER = Path(__file__).resolve().parent.parent
ROOT = ADOPTER.parent.parent
# Direct and transitive @openabstractions dependencies of the adopter tests.
LINKED = (
    "openabstractions-flat/abstraction-asks/javascript",
    "openabstractions-flat/abstraction-download/javascript",
    "openabstractions-flat/abstraction-facade/javascript",
    "openabstractions-flat/abstraction-inference/javascript",
    "openabstractions-flat/abstraction-job/javascript",
    "openabstractions-flat/abstraction-identity/javascript",
    "openabstractions-flat/abstraction-logging/javascript",
    "openabstractions-flat/abstraction-rights/javascript",
    "openabstractions-flat/abstraction-router/javascript",
    "openabstractions-flat/abstraction-storage/javascript",
)


def imported(adopter=ADOPTER):
    """Packages needed by the adopter and its linked source packages.

    Absence of any of them means the install has not run since a dependency was added.
    """
    deps = set(json.loads((adopter / "package.json").read_text(encoding="utf-8")).get("dependencies", {}))
    deps.update(json.loads((ROOT / directory / "package.json").read_text(encoding="utf-8"))["name"]
                for directory in LINKED)
    return tuple(sorted(deps | {"@ai-sdk/provider", "@opencode-ai/plugin"}))


def missing():
    return [name for name in imported() if not (ADOPTER / "node_modules" / name / "package.json").is_file()]


def serve_native_environment(environment=None):
    """Receive this checkout's normal Node-test outputs for its Serve consumer."""
    sys.path.insert(0, str(ROOT / 'scripts'))
    import build_node_addon as native
    from file_boundary import ordinary_path
    if native.ROOT.resolve() != ROOT.resolve() or not native.current():
        raise ValueError('Serve requires the current source IPC addon from OpenCode node tests')
    addon = ordinary_path(native.ADDON,kind='file')
    modules = ADOPTER / 'node_modules'
    ordinary_path(modules,kind='directory')
    locked = json.loads((ADOPTER / 'package-lock.json').read_text(encoding='utf-8'))['packages']
    # The existing Go fixture copies plugin-local zod before the hoisted copy.
    # Admit that exact location when present, never a different hoisted file.
    nested_zod = '@opencode-ai/plugin/node_modules/zod'
    zod = nested_zod if os.path.lexists(modules / nested_zod) else 'zod'
    for name, location in (('@ai-sdk/provider','@ai-sdk/provider'), ('jsonc-parser','jsonc-parser'),
                           ('@opencode-ai/plugin','@opencode-ai/plugin'), ('zod',zod)):
        expected = locked.get('node_modules/' + location, {})
        if not expected.get('integrity'):
            raise ValueError('Serve requires locked OpenCode module: ' + location)
        try:
            manifest = ordinary_path(modules / location / 'package.json',kind='file')
        except FileNotFoundError as error:
            raise ValueError('Serve requires locked OpenCode module: ' + location) from error
        actual = json.loads(manifest.read_text(encoding='utf-8'))
        if actual.get('name') != name or actual.get('version') != expected.get('version'):
            raise ValueError('Serve OpenCode module differs from its lock: ' + location)
    # The prerequisite force-builds this source in the same invocation. current()
    # is only a drift check; the normal real ABI/identity probe refuses another
    # native module, including AppModel, before the dependent Go consumer.
    if native.runtime_import_owner().problems(addon) or native.validate_native_hosts(addon):
        raise ValueError('Serve IPC addon failed the normal native ABI/import admission')
    return dict(os.environ if environment is None else environment,
                ABSTRACTION_IPC_NODE=str(addon),
                OA_AI_SDK_PROVIDER_MODULES=str(modules.resolve()))


def is_link(path):
    """A symbolic link or a Windows junction."""
    return os.path.islink(path) or os.path.isjunction(path)


def package_links(adopter=ADOPTER):
    """The links under node_modules/@openabstractions."""
    scope = adopter / "node_modules" / "@openabstractions"
    if not scope.is_dir() or is_link(scope):
        return set()
    return {entry for entry in scope.iterdir() if is_link(entry)}


def remove_link(path):
    """Remove one link and nothing it points to: a junction goes with rmdir,
    which deletes the reparse point; a symbolic link with unlink. A real
    directory is refused."""
    if os.path.isjunction(path):
        os.rmdir(path)
    elif os.path.islink(path):
        os.unlink(path)
    else:
        raise ValueError(f"run_node_tests: {path} is not a link")


def remove_created_links(before, adopter=ADOPTER, out=sys.stderr):
    """Remove the package links that were not there before; return a status."""
    status = 0
    for link in sorted(package_links(adopter) - before):
        try:
            remove_link(link)
        except OSError as error:
            print(f"run_node_tests: could not remove the link {link}: {error}", file=out)
            status = 1
    return status


def prepare_dependencies(adopter=ADOPTER, root=ROOT, *, environment=None, linked=LINKED):
    """The ordinary scripts-disabled dependency install, without builds or tests."""
    npm = shutil.which('npm')
    if npm is None:
        raise ValueError('npm is not on PATH')
    return subprocess.run([npm, 'install', '--ignore-scripts', '--no-save', '--no-audit', '--no-fund',
                           *[str(root / directory) for directory in linked]],
                          cwd=adopter, env=environment, check=False).returncode


def receive_dependency_cache(bundle, adopter, destination, *, environment):
    """Install the immutable third-party closure in a separate test folder."""
    from npm_packages import copy_dependency_cache, npm_bundled_modules
    for name in ('package.json', 'package-lock.json', 'shrinkwrap.js'):
        current = (adopter / name).read_bytes().replace(b'\r\n', b'\n')
        frozen = (bundle / 'source' / name).read_bytes().replace(b'\r\n', b'\n')
        if current != frozen:
            raise ValueError('staged OpenCode dependency source differs: ' + name)
    manifest = json.loads((adopter / 'package.json').read_bytes())
    lock = json.loads((adopter / 'package-lock.json').read_bytes())
    packages = {name: record for name, record in lock['packages'].items() if name}
    destination.mkdir()
    home = destination / 'home'
    home.mkdir()
    environment = {k: v for k, v in environment.items()
                   if not k.upper().startswith('NPM_CONFIG_') and k.upper() not in ('NODE_PATH', 'NODE_OPTIONS')}
    for name in ('user.npmrc', 'global.npmrc'):
        (destination / name).touch()
    cache = destination / 'cache'
    environment.update(HOME=str(home), USERPROFILE=str(home), npm_config_cache=str(cache),
                       npm_config_userconfig=str(destination / 'user.npmrc'),
                       npm_config_globalconfig=str(destination / 'global.npmrc'),
                       npm_config_update_notifier='false', npm_config_offline='true')
    copy_dependency_cache(bundle, cache, packages, environment=environment)
    # The Go fixture copies the OpenAbstractions packages from pinned sibling
    # sources. npm receives the exact remaining lock, including peer entries.
    for root in (manifest, lock['packages']['']):
        root['dependencies'] = {name: version for name, version in root['dependencies'].items()
                                if not name.startswith('@openabstractions/')}
    (destination / 'package.json').write_text(json.dumps(manifest), encoding='utf-8')
    (destination / 'package-lock.json').write_text(json.dumps(lock), encoding='utf-8')
    cli = npm_bundled_modules(environment=environment).parent / 'bin/npm-cli.js'
    subprocess.run(['node', str(cli), 'ci', '--offline', '--ignore-scripts', '--omit=optional',
                    '--no-audit', '--no-fund'], cwd=destination, env=environment, check=True, timeout=180)
    for name in ('@ai-sdk/provider', '@opencode-ai/plugin', 'jsonc-parser', 'zod'):
        location = 'node_modules/' + name
        if name == 'zod' and (destination / 'node_modules/@opencode-ai/plugin/node_modules/zod').exists():
            location = 'node_modules/@opencode-ai/plugin/node_modules/zod'
        received = json.loads((destination / location / 'package.json').read_bytes())
        if received['name'] != name or received['version'] != packages[location]['version']:
            raise ValueError('staged OpenCode dependency differs: ' + location)
    return destination / 'node_modules'


def prepare_dependency_cache(source, root, destination, *, linked=LINKED):
    """Prepare a fresh, explicit locked cache using the ordinary dependency owner."""
    sys.path.insert(0, str(root / 'scripts'))
    from npm_packages import extended_path
    destination = Path(destination)
    if extended_path(destination).exists():
        raise ValueError('dependency preparation requires a fresh destination')
    extended_path(destination).mkdir(parents=True)
    adopter = destination / 'source'; extended_path(adopter).mkdir()
    for name in ('package.json', 'package-lock.json', 'shrinkwrap.js'):
        shutil.copyfile(extended_path(source / name), extended_path(adopter / name))
    before = {name: extended_path(adopter / name).read_bytes() for name in ('package.json', 'package-lock.json')}
    cache = destination / 'cache'
    for name in ('user.npmrc', 'global.npmrc'):
        extended_path(destination / name).write_text('', encoding='utf-8')
    home = destination / 'home'; extended_path(home).mkdir()
    environment = {k: v for k, v in os.environ.items()
                   if not k.upper().startswith('NPM_CONFIG_') and k.upper() not in ('NODE_PATH', 'NODE_OPTIONS')}
    environment.update(HOME=str(home), USERPROFILE=str(home), npm_config_cache=str(cache),
                       npm_config_omit='optional',
                       npm_config_update_notifier='false',
                       npm_config_userconfig=str(destination / 'user.npmrc'),
                       npm_config_globalconfig=str(destination / 'global.npmrc'))
    # Reuse the canonical URL/sha512 and source/lock validator under the same
    # explicit private environment as every subsequent dependency child.
    subprocess.run(['node', 'shrinkwrap.js'], cwd=adopter, env=environment, check=True)
    wrap = json.loads(extended_path(adopter / 'npm-shrinkwrap.json').read_text(encoding='utf-8'))
    links = package_links(extended_path(adopter))
    try:
        code = prepare_dependencies(adopter, root, environment=environment, linked=linked)
        if code:
            raise ValueError('locked dependency preparation failed: ' + str(code))
        if any(extended_path(adopter / name).read_bytes() != value for name, value in before.items()):
            raise ValueError('dependency preparation changed its source manifest or lock')
        packages = {k: v for k, v in wrap['packages'].items() if k}
        # Platform-optional entries remain part of the frozen lock. Fill their
        # exact registry URLs through npm as well; no range/latest selection.
        for url in sorted({entry['resolved'] for entry in packages.values()}):
            result = subprocess.run([shutil.which('npm'), 'cache', 'add', url, '--ignore-scripts'],
                                    env=environment, cwd=adopter, check=False)
            if result.returncode:
                raise ValueError('locked archive cache preparation failed')
    finally:
        if remove_created_links(links, extended_path(adopter)):
            raise ValueError('dependency preparation link cleanup failed')
    return prime_dependency_metadata(source, destination, environment=environment)


def prepared_dependency_packages(source, destination, *, environment):
    """Read the existing canonical source/lock validator without package effects."""
    from npm_packages import extended_path
    adopter = destination / 'source'
    if package_links(extended_path(adopter)):
        raise ValueError('dependency preparation links remain')
    before = {}
    for name in ('package.json', 'package-lock.json', 'shrinkwrap.js'):
        before[name] = extended_path(source / name).read_bytes()
        if extended_path(adopter / name).read_bytes() != before[name]:
            raise ValueError('prepared dependency source differs')
    script = """import {readFileSync} from 'node:fs'; import {join} from 'node:path'; import {pathToFileURL} from 'node:url';
const dir=process.argv[1];const {shrinkwrap}=await import(pathToFileURL(join(dir,'shrinkwrap.js')));
const read=n=>JSON.parse(readFileSync(join(dir,n),'utf8'));
process.stdout.write(JSON.stringify(shrinkwrap(read('package.json'),read('package-lock.json'))));"""
    checked = subprocess.run(['node', '--input-type=module', '-e', script, str(adopter)],
                             env=environment, check=True, capture_output=True, text=True)
    wrap = json.loads(checked.stdout)
    packages = {k: v for k, v in wrap['packages'].items() if k}
    return before, packages


def prime_dependency_metadata(source, destination, *, environment, offline_only=False):
    """Prime exact locked manifests only; existing authenticated archives stay in cache."""
    from npm_packages import npm_bundled_modules, verify_cached_archives, npm_content_cache, VERSION
    _, packages = prepared_dependency_packages(source, destination, environment=environment)
    verify_cached_archives(destination / 'cache', packages, environment=environment)
    environment = dict(environment, npm_config_update_notifier='false')
    effective_cache = npm_content_cache(destination / 'cache', environment=environment)
    pacote = npm_bundled_modules(environment=environment) / 'pacote'
    script = """const p=require(process.argv[1]);
p.manifest(process.argv[2],{cache:process.argv[3],registry:'https://registry.npmjs.org',preferOffline:true,
fullMetadata:process.argv[4]==='1',offline:process.argv[5]==='1'}).then(m=>console.log(JSON.stringify({
name:m.name,version:m.version,integrity:m.dist?.integrity,tarball:m.dist?.tarball}))).catch(e=>{console.error(e.message);process.exitCode=1});"""
    seen = set()
    for location, entry in sorted(packages.items()):
        name = location.rsplit('node_modules/', 1)[-1]
        version = entry.get('version', '')
        if not location.startswith('node_modules/') or not VERSION.fullmatch(version):
            raise ValueError('dependency metadata requires a fixed locked version')
        spec = name + '@' + version
        if spec in seen:
            continue
        seen.add(spec)
        # npm's Arborist uses full manifests for resolution and abbreviated
        # manifests for lock inflation. Prime both real cache variants and
        # prove each is reusable by the same owner in offline mode.
        for full in ('0', '1'):
            for offline in (('1',) if offline_only else ('0', '1')):
                result = subprocess.run(['node', '-e', script, str(pacote), spec,
                                         str(effective_cache), full, offline],
                                        env=environment, check=True, capture_output=True, text=True)
                dist = json.loads(result.stdout)
                if (not isinstance(dist, dict) or dist.get('name') != name or dist.get('version') != version
                        or dist.get('integrity') != entry['integrity'] or dist.get('tarball') != entry['resolved']):
                    raise ValueError('dependency metadata differs from the frozen lock: ' + spec)
    return finish_dependency_cache(source, destination, environment=environment)


def finish_dependency_cache(source, destination, *, environment):
    """Admit an already-filled owned cache without reinstalling or downloading."""
    from npm_packages import cache_files, verify_cached_archives, extended_path
    before, packages = prepared_dependency_packages(source, destination, environment=environment)
    cache = destination / 'cache'
    verify_cached_archives(cache, packages, environment=environment)
    receipt = {'manifest_sha256': hashlib.sha256(before['package.json']).hexdigest(),
               'lock_sha256': hashlib.sha256(before['package-lock.json']).hexdigest(),
               'validator_sha256': hashlib.sha256(before['shrinkwrap.js']).hexdigest(),
               'packages': packages, 'files': cache_files(cache)}
    extended_path(destination / 'receipt.json').write_text(json.dumps(receipt, indent=2) + '\n', encoding='utf-8')
    return receipt


INSTALLED_ENDPOINT = re.compile(r'(^|[\\/])openabstractions-(user|machine)-.*runtime', re.IGNORECASE)


def isolated_environment(environ=None, platform=sys.platform, pid=None):
    """The tests' environment: ABSTRACTION_RUNTIME_ENDPOINT names an endpoint
    no process serves (the same rule as runtime-isolation.mjs), so no test can
    reach or start the person's installed runtime. An inherited endpoint is
    kept unless it names the installed runtime, which is refused (None)."""
    environment = dict(os.environ if environ is None else environ)
    current = environment.get('ABSTRACTION_RUNTIME_ENDPOINT', '')
    if current:
        return None if INSTALLED_ENDPOINT.search(current) else environment
    name = f'oa-opencode-test-unserved-{os.getpid() if pid is None else pid}'
    environment['ABSTRACTION_RUNTIME_ENDPOINT'] = ('\\\\.\\pipe\\' + name if platform == 'win32'
                                                   else str(Path(tempfile.gettempdir()) / (name + '.sock')))
    return environment


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0],
                                     formatter_class=argparse.RawDescriptionHelpFormatter, epilog=__doc__)
    parser.add_argument("--run", action="store_true", help="install the dependencies if absent, then run npm test")
    parser.add_argument("--no-install", action="store_true", help="with --run, fail when the dependencies are absent")
    parser.add_argument("--also", action="append", default=[], metavar="FILE",
                        help="with --run, also run this adopter test file (repository-relative) that the default "
                             "npm test patterns skip, such as adopters/opencode/installer/test_config_lifecycle.mjs")
    parser.add_argument('--prepare-cache', type=Path, help='prepare only a fresh explicit locked dependency cache; no tests or native build')
    args = parser.parse_args(argv)
    if args.prepare_cache:
        prepare_dependency_cache(ADOPTER, ROOT, args.prepare_cache)
        return 0
    if not args.run:
        parser.print_help()
        return 0
    npm = shutil.which("npm")
    if npm is None:
        print("run_node_tests: npm is not on PATH", file=sys.stderr)
        return 2
    absent = missing()
    if absent and args.no_install:
        print("run_node_tests: node_modules lacks " + ", ".join(absent), file=sys.stderr)
        return 2
    before = package_links()
    try:
        if absent:
            code = prepare_dependencies()
            if code:
                return code
        if sys.platform == "win32":
            # The downstream native Serve corpus receives this actual build.
            # Timestamp freshness alone is not exact-source producer proof.
            code = subprocess.run([sys.executable, str(ROOT / "scripts" / "build_node_addon.py"), "--run", "--force"],
                                  check=False).returncode
            if code:
                return code
        environment = isolated_environment()
        if environment is None:
            print("run_node_tests: ABSTRACTION_RUNTIME_ENDPOINT names the installed runtime; the tests run only against an unserved endpoint or a fixture", file=sys.stderr)
            return 2
        code = subprocess.run([npm, "test"], cwd=ADOPTER, env=environment, check=False).returncode
        sys.path.insert(0, str(ROOT / "scripts"))
        from file_boundary import relative_beneath_existing
        for name in args.also:
            path = ROOT / name
            # The repository's file-identity owner answers whether the file
            # lies under the adopter folder (scripts/file_boundary.py).
            try:
                inside = path.is_file() and relative_beneath_existing(path, ADOPTER) is not None
            except (OSError, ValueError):
                inside = False
            if not inside:
                print("run_node_tests: no adopter test file " + name, file=sys.stderr)
                return 2
            code = subprocess.run(["node", "--import", "./runtime-isolation.mjs", "--test", str(path)], cwd=ADOPTER,
                                  env=environment, check=False).returncode or code
        return code
    finally:
        if remove_created_links(before):
            print("run_node_tests: remove the links listed above by hand (rmdir, never a recursive delete)", file=sys.stderr)


if __name__ == "__main__":
    sys.exit(main())
