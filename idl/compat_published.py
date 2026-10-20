"""Compare every public definition with its last published release (CONV-V1, IPC-V2).

published.tsv names each public repository's latest private revision. The
verified module publication ledger also names accounted component releases;
its existing receipt/source provenance supplies their private revisions.
Every distinct repository/revision pair is compared, so a newer repository
baseline cannot hide an older component's published contract. For each definition the
check writes the definition and every definition it includes, as they were at
that revision, into a scratch tree, and runs the generator's `gen --compat
<published> <candidate>` (idl/LANGUAGE.md DEF-V1) against the working tree. A
definition the revision did not hold is new and has nothing to keep. A revision
the clone does not hold is an error: a comparison with nothing would pass.
Published Go client packages also retain every exported identifier's declaration
kind, read through the existing Go AST surface
owner and the historical split mapping.

  py idl/compat_published.py              build the generator and check every public definition
  py idl/compat_published.py --gen GEN    use an already built generator
  py idl/compat_published.py --help       this text

Exit 0 when every compared definition is compatible, 1 naming each refusal,
2 when the generator or Git fails or a published revision is missing.
"""
import argparse
import json
import os
import posixpath
import re
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT/'scripts') not in sys.path:
    sys.path.insert(0, str(ROOT/'scripts'))
from git_env import source_git_environment

REVISION = re.compile(r'[0-9a-f]{40}\Z')
INCLUDE = re.compile(rb'(?m)^\s*include\s+"([^"]+)"')


def published_baselines(root):
    """All latest repository and accounted component private-source pairs."""
    if str(ROOT/'scripts') not in sys.path:
        sys.path.insert(0, str(ROOT/'scripts'))
    import gosum_published
    pairs = set(published_revisions(root).items())
    ledger = root/gosum_published.LEDGER
    if not ledger.is_file():
        raise ValueError('published module ledger is missing: ' + str(ledger))
    try:
        sources = gosum_published.publication_sources(ledger)
        _, rows = gosum_published.read_ledger(ledger)
        for row in rows:
            repository = gosum_published.repository_of(row['module'])
            if row['receipt'] not in sources:
                if not any((root/'openabstractions-flat'/repository).glob('*.thrift')):
                    continue
                raise ValueError(f"{row['module']} {row['version']}: published receipt "
                                 f"{row['receipt']} lacks private source provenance")
            pairs.add((repository, sources[row['receipt']]))
    except gosum_published.RecordError as error:
        raise ValueError(str(error)) from error
    return sorted(pairs)


def published_revisions(root):
    """{repository: private revision} for each public abstraction with one."""
    out = {}
    for line in (root/'published.tsv').read_text(encoding='utf-8').splitlines():
        fields = line.split('\t')
        if len(fields) == 3 and fields[0].startswith('abstraction-') and REVISION.match(fields[2]):
            out[fields[0]] = fields[2]
    return out


def present(root, revision):
    """Whether this clone holds revision as a commit; a shallow or fresh clone may not."""
    return subprocess.run(['git', '-C', str(root), 'cat-file', '-e', f'{revision}^{{commit}}'],
                          capture_output=True, env=dict(source_git_environment(),
                              GIT_NO_LAZY_FETCH='1', GIT_TERMINAL_PROMPT='0')).returncode == 0


def show(root, revision, relative):
    """relative's bytes at revision, or None when that present revision lacks the path."""
    environment = dict(source_git_environment(), GIT_NO_LAZY_FETCH='1', GIT_TERMINAL_PROMPT='0')
    done = subprocess.run(['git', '-C', str(root), 'show', f'{revision}:{relative}'], capture_output=True,
                          env=environment)
    if done.returncode == 0:
        return done.stdout
    # A missing promised blob is not a new definition. Read only the tree to
    # distinguish a genuinely absent path from a source object we cannot read.
    entry = subprocess.run(['git', '-C', str(root), 'ls-tree', '-z', '--full-tree', revision, '--', relative],
                           capture_output=True, env=environment)
    if entry.returncode or entry.stdout:
        raise ValueError(f'cannot read historical source {relative} at {revision[:12]}')
    return None


def materialize(root, revision, relative, scratch, seen=None, source_for=None):
    """Write relative and its include closure as of revision under scratch; False when relative is absent."""
    seen = set() if seen is None else seen
    if relative in seen:
        return True
    seen.add(relative)
    source = source_for(relative) if source_for else relative
    data = show(root, revision, source) if source else None
    if data is None:
        return False
    target = scratch/relative
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(data)
    for included in INCLUDE.findall(data):
        path = posixpath.normpath(posixpath.join(posixpath.dirname(relative), included.decode('utf-8')))
        if not materialize(root, revision, path, scratch, seen, source_for):
            raise ValueError(f'{relative} at {revision[:12]} includes {path}, which that revision lacks')
    return True


def projection(root, revision, scratch):
    """Use the actual historical split owner to locate public definition inputs."""
    if str(ROOT/'scripts') not in sys.path:
        sys.path.insert(0, str(ROOT/'scripts'))
    import oa
    data = show(root, revision, 'scripts/split.manifest')
    if data is None:
        raise ValueError(f'{revision[:12]} lacks scripts/split.manifest')
    manifest = scratch/'scripts'/'split.manifest'
    manifest.parent.mkdir(parents=True, exist_ok=True)
    manifest.write_bytes(data)
    repos, _ = oa.manifest(scratch)

    def source(relative, *, absent_ok=False):
        parts = relative.split('/', 2)
        if len(parts) != 3 or parts[0] != 'openabstractions-flat' or parts[1] not in repos:
            raise ValueError(f'{relative} has no published split mapping at {revision[:12]}')
        try:
            mapped = oa.source_for(repos[parts[1]], parts[2])
        except oa.Refused as error:
            raise ValueError(str(error)) from error
        if mapped is None:
            if absent_ok:
                return None
            raise ValueError(f'{relative} has no published source at {revision[:12]}')
        return mapped
    return source


def check(root, gen, run=subprocess.run):
    """(code, lines): 0 compatible, 1 a refusal, with one line per definition."""
    lines, code = [], 0
    for repository, revision in published_baselines(root):
        if not present(root, revision):
            raise ValueError(f'{repository}: published revision {revision[:12]} is not in this clone; '
                             'fetch it before comparing')
        for definition in sorted((root/'openabstractions-flat'/repository).glob('*.thrift')):
            relative = definition.relative_to(root).as_posix()
            with tempfile.TemporaryDirectory(prefix='oa-compat-') as scratch:
                source = projection(root, revision, Path(scratch))
                if not materialize(root, revision, relative, Path(scratch), source_for=source):
                    lines.append(f'new  {relative}: absent at {revision[:12]}')
                    continue
                done = run([str(gen), '--compat', str(Path(scratch)/relative), str(definition)],
                           capture_output=True, text=True, encoding='utf-8', errors='replace')
            if done.returncode:
                code = 1
                lines.append(f'FAIL {relative} against {revision[:12]}: {done.stderr.strip()}')
            else:
                lines.append(f'ok   {relative} against {revision[:12]}')
    return code, lines


def client_inventory(lines):
    """Package declarations from the existing Go AST surface owner."""
    exports = {}
    for line in lines:
        if line.startswith(('type ', 'var ', 'const ')):
            kind, name, *_ = line.split()
            exports[name] = kind
        elif line.startswith('func '):
            # Parameter names do not belong to Go's callable type identity.
            name = re.match(r'func (?:\(([^)]+)\) )?([^ (]+)', line)
            key = ((name[1] + '.') if name[1] else '') + name[2]
            exports[key] = 'func'
    return exports


def read_client_surfaces(inventory_tool, files, run=subprocess.run):
    """Read every distinct file with the existing AST tool in bounded batches."""
    # CreateProcessW allows 32,767 UTF-16 units including the trailing NUL.
    limit = 32767

    def units(arg):
        return len(subprocess.list2cmdline([arg]).encode('utf-16-le')) // 2

    executable = str(inventory_tool)
    overhead = units(executable) + 1
    used = overhead
    batch = []
    surfaces = {}

    def receive(paths):
        done = run([executable, *paths], capture_output=True, text=True, encoding='utf-8')
        if done.returncode:
            raise ValueError('Go client inventory failed: ' + done.stderr.strip())
        result = json.loads(done.stdout)
        if not isinstance(result, dict) or result.keys() != set(paths):
            raise ValueError('Go client inventory did not return the requested files')
        surfaces.update(result)

    for path in dict.fromkeys(map(str, files)):
        cost = units(path) + 1
        if overhead + cost > limit:
            raise ValueError('Go client inventory path exceeds the command line limit: ' + path)
        if used + cost > limit:
            receive(batch)
            batch = []
            used = overhead
        batch.append(path)
        used += cost
    if batch:
        receive(batch)
    return surfaces


def check_clients(root):
    """Retain published client names and declaration kinds using historical splits."""
    code, lines = 0, []
    with tempfile.TemporaryDirectory(prefix='oa-client-compat-') as temporary:
        scratch = Path(temporary)
        pairs, files = [], []
        for repository, revision in published_baselines(root):
            client = root/'openabstractions-flat'/repository/'go'/'client'
            if not present(root, revision):
                raise ValueError(f'{repository}: published revision {revision[:12]} is not in this clone')
            source = projection(root, revision, scratch/'projection')
            relative = f'openabstractions-flat/{repository}/go/client/'
            prefix = source(relative, absent_ok=True)
            if prefix is None:
                continue
            names = subprocess.check_output(['git', '-C', str(root), 'ls-tree', '-r', '--name-only',
                                             revision, '--', prefix], env=source_git_environment(), text=True).splitlines()
            old = []
            for name in names:
                if not name.endswith('.go') or name.endswith('_test.go') or posixpath.dirname(name) != prefix.rstrip('/'):
                    continue
                path = scratch/revision/repository/posixpath.basename(name)
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(show(root, revision, name))
                old.append(path)
            if old:
                current = sorted(path for path in client.glob('*.go') if not path.name.endswith('_test.go'))
                pairs.append((repository, revision, old, current))
                files.extend(old + current)
        if not pairs:
            return code, lines
        inventory_tool = scratch/'idiom-surface.exe'
        done = subprocess.run(['go', 'build', '-o', str(inventory_tool), str(root/'scripts'/'idiom_surface.go')],
                              env={**os.environ, 'GOWORK': 'off'}, capture_output=True, text=True, encoding='utf-8')
        if done.returncode:
            raise ValueError('Go client inventory owner did not build: ' + done.stderr.strip())
        surfaces = read_client_surfaces(inventory_tool, files)
        for path, surface in surfaces.items():
            if surface['lines'] is None and surface['problems']:
                raise ValueError(f'cannot read Go client inventory {path}: ' + '; '.join(surface['problems']))
        for repository, revision, old, current in pairs:
            inventory = lambda paths: client_inventory(line for path in paths for line in (surfaces[str(path)]['lines'] or []))
            before, after = inventory(old), inventory(current)
            problems = [name for name, signature in before.items() if after.get(name) != signature]
            if problems:
                code = 1
                lines.append(f'FAIL {repository}/go/client against {revision[:12]}: removed or changed ' + ', '.join(sorted(problems)))
            else:
                lines.append(f'ok   {repository}/go/client against {revision[:12]} ({len(before)} published exports)')
    return code, lines


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split('\n\n')[0], epilog=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--gen', type=Path, help='a built idl/gen generator')
    args = parser.parse_args(argv)
    with tempfile.TemporaryDirectory(prefix='oa-compat-gen-') as built:
        gen = args.gen
        if gen is None:
            gen = Path(built)/'gen.exe'
            done = subprocess.run(['go', '-C', str(ROOT/'idl'/'gen'), 'build', '-o', str(gen), '.'],
                                  env={**os.environ, 'GOWORK': 'off'}, capture_output=True, text=True)
            if done.returncode:
                print(f'compat_published: the generator did not build: {done.stderr.strip()}', file=sys.stderr)
                return 2
        try:
            code, lines = check(ROOT, gen)
            client_code, client_lines = check_clients(ROOT)
            code = max(code, client_code)
            lines.extend(client_lines)
        except (OSError, ValueError) as e:
            print(f'compat_published: {e}', file=sys.stderr)
            return 2
    print('\n'.join(lines))
    return code


if __name__ == '__main__':
    sys.exit(main())
