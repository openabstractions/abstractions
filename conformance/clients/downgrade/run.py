"""Downgrade refusal against stores written by current sources.

Builds the previous release's managed runtime storage preflight (CheckManaged)
and provider open (OpenManaged) from published job and download modules, in a
generated module outside go.work. The previous pins come from published.tsv or
--previous-job/--previous-download. Current sources write four managed stores:
plain, retry-marked, lost-marked, and plain with an unknown storage feature added
to its owner header. Each store is judged by the previous preflight and open and
by the current `openabstractions storage check --read-only`.

Expected verdicts: the previous release passes plain and refuses the other three
by name. The current check passes plain, retry and lost and refuses unknown by
the feature's name. No store file changes and none is added. The previous open
runs on a pristine copy of each store, because a successful open recovers and
writes.

Needs Go and the network: published modules are fetched through GOPROXY. The
replace directives for current sources are generated from go.work and each
module's requirements. Build trees live under .build on Windows and in the system
temporary directory elsewhere, and are removed unless --keep is given.
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

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
from workspace import (ROOT, build_root, go_replace_closure, go_template_binary,  # noqa: E402
                       go_workspace_modules, source_revision, dry_run_stop, DRY_RUN_HELP)

JOB = 'github.com/openabstractions/abstraction-job/go'
DOWNLOAD = 'github.com/openabstractions/abstraction-download/go'
REPOSITORIES = {JOB: 'abstraction-job', DOWNLOAD: 'abstraction-download'}
UNKNOWN_FEATURE = 'abstraction.job/journal-future@9'
EXE = '.exe' if os.name == 'nt' else ''

STORES = {
    # name: (writer mode, header features after writing, previous passes, current passes)
    'plain': ('plain', [], True, True),
    'retry': ('retry', ['abstraction.job/journal-attempts@1'], False, True),
    'lost': ('lost', ['abstraction.job/journal-result-lost@1'], False, True),
    'unknown': ('plain', [UNKNOWN_FEATURE], False, False),
}


def go_env():
    env = dict(os.environ, GOWORK='off', GOFLAGS='-mod=mod')
    # Published modules must be fetched; a caller's GOPROXY choice is kept.
    if env.get('GOPROXY') == 'off':
        env.pop('GOPROXY')
    return env


def run(args, cwd, env, check=True, timeout=900):
    result = subprocess.run([str(a) for a in args], cwd=str(cwd), env=env, capture_output=True, text=True,
                            encoding='utf-8', errors='replace', timeout=timeout)
    if check and result.returncode:
        raise SystemExit('command failed: ' + ' '.join(str(a) for a in args) + '\n' + result.stdout + result.stderr)
    return result


def published_pins():
    pins = {}
    for line in (ROOT / 'published.tsv').read_text(encoding='utf-8').splitlines():
        if not line or line.startswith('>'):
            continue
        fields = line.split('\t')
        if len(fields) >= 2:
            pins[fields[0]] = fields[1]
    return pins


def resolve(module, query, env):
    """Resolve a commit or version query to a module version, or refuse by name."""
    result = run(['go', 'list', '-m', '-json', module + '@' + query], ROOT, env, check=False, timeout=300)
    if result.returncode:
        raise SystemExit('cannot resolve previous %s@%s: %s' % (module, query, (result.stderr or result.stdout).strip()))
    return json.loads(result.stdout)['Version']


def go_directive(directory):
    match = re.search(r'^go\s+(\S+)', (directory / 'go.mod').read_text(encoding='utf-8'), re.M)
    return match.group(1) if match else '1.26.0'


def build_writer(work, env):
    modules = go_workspace_modules()
    closure = go_replace_closure([JOB, DOWNLOAD], modules)
    binary = go_template_binary(HERE / 'writer.go', work, 'writer',
                                requires=[(m, 'v0.0.0-00010101000000-000000000000') for m in closure],
                                replaces=[(m, modules[m]) for m in closure], env=env,
                                go_version=go_directive(modules[JOB]))
    print('current writer: %d workspace modules replaced from go.work (%s)' % (len(closure), ', '.join(closure)))
    return binary


def build_previous(work, env, job_version, download_version):
    return go_template_binary(HERE / 'previous.go', work, 'previous',
                              requires=[(JOB, job_version), (DOWNLOAD, download_version)], env=env)


def build_current_check(work):
    binary = work / ('openabstractions' + EXE)
    env = dict(os.environ)
    env.pop('GOWORK', None)  # the in-tree go.work selects current sources
    run(['go', 'build', '-o', binary, '.'], ROOT / 'serve', env)
    return binary


def file_hashes(directory):
    return {p.relative_to(directory).as_posix(): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in sorted(directory.rglob('*')) if p.is_file()}


def add_feature(state, feature):
    header = state / 'jobs' / 'acceptance' / 'owner.json'
    owner = json.loads(header.read_text(encoding='utf-8'))
    owner['Features'] = owner.get('Features', []) + [feature]
    header.write_text(json.dumps(owner, separators=(',', ':')), encoding='utf-8')


def names(error, features):
    """A refusal is by name when it names a header feature or the Features field."""
    return any(f in error for f in features) or '"Features"' in error


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--run', action='store_true', help='build and judge; without it this help is printed')
    parser.add_argument('--dry-run', action='store_true', help=DRY_RUN_HELP)
    parser.add_argument('--previous-job', help='previous %s version or commit (default: published.tsv)' % JOB)
    parser.add_argument('--previous-download', help='previous %s version or commit (default: published.tsv)' % DOWNLOAD)
    parser.add_argument('--keep', action='store_true', help='keep the build tree and stores')
    args = parser.parse_args()
    if not (args.run or args.dry_run):
        parser.print_help()
        return 0
    source_revision()
    if args.dry_run: dry_run_stop('downgrade', args)

    env = go_env()
    pins = published_pins()
    job_query = args.previous_job or pins.get(REPOSITORIES[JOB])
    download_query = args.previous_download or pins.get(REPOSITORIES[DOWNLOAD])
    if not job_query or not download_query or '-' in (job_query, download_query):
        raise SystemExit('published.tsv has no job/download publication; pass --previous-job and --previous-download')
    job_version, download_version = resolve(JOB, job_query, env), resolve(DOWNLOAD, download_query, env)
    print('previous release: %s %s (%s), %s %s (%s)' % (JOB, job_version, job_query, DOWNLOAD, download_version, download_query))

    # Windows keeps build trees under .build for its path limits; elsewhere the
    # system temporary directory holds the built programs and stores.
    work = Path(tempfile.mkdtemp(prefix='downgrade-', dir=build_root() if os.name == 'nt' else None))
    failures = []

    def verdict(ok, text):
        print(('PASS ' if ok else 'FAIL ') + text)
        if not ok:
            failures.append(text)

    try:
        writer = build_writer(work, env)
        previous = build_previous(work, env, job_version, download_version)
        current = build_current_check(work)
        for name, (mode, features, previous_passes, current_passes) in STORES.items():
            state = work / 'stores' / name
            written = run([writer, state, mode], work, env)
            report = json.loads(written.stdout.strip().splitlines()[-1])
            if name == 'unknown':
                add_feature(state, UNKNOWN_FEATURE)
            header = json.loads((state / 'jobs' / 'acceptance' / 'owner.json').read_text(encoding='utf-8'))
            verdict(header.get('Features', []) == features,
                    '%s store header Features %s' % (name, header.get('Features', [])))
            if mode == 'lost':
                verdict(report.get('read') == 'unavailable' and report.get('state') == 'failed' and report.get('cause') == 'result_lost',
                        '%s store observed read=%s state=%s cause=%s' % (name, report.get('read'), report.get('state'), report.get('cause')))
            copy = work / 'copies' / name
            shutil.copytree(state, copy)
            before = file_hashes(state)
            copy_before = file_hashes(copy)

            out = json.loads(run([previous, state / 'jobs', 'preflight'], work, env).stdout.strip().splitlines()[-1])
            if previous_passes:
                verdict(out['ok'], '%s previous preflight passes' % name)
            else:
                verdict(not out['ok'] and names(out.get('error', ''), features),
                        '%s previous preflight refuses by name: %s' % (name, out.get('error', 'passed')))
            out = json.loads(run([previous, copy / 'jobs', 'open'], work, env).stdout.strip().splitlines()[-1])
            if previous_passes:
                verdict(out['ok'], '%s previous open passes' % name)
            else:
                verdict(not out['ok'] and names(out.get('error', ''), features),
                        '%s previous open refuses by name: %s' % (name, out.get('error', 'passed')))
                verdict(file_hashes(copy) == copy_before, '%s previous open refusal left its copy unchanged' % name)

            checked = run([current, 'storage', 'check', '--read-only', '--state-dir', state], work, dict(os.environ), check=False)
            said = (checked.stdout + checked.stderr).strip()
            if current_passes:
                verdict(checked.returncode == 0 and '(read-only)' in said,
                        '%s current read-only storage check passes: %s' % (name, said.splitlines()[0] if said else ''))
            else:
                verdict(checked.returncode != 0 and UNKNOWN_FEATURE in said,
                        '%s current read-only storage check refuses by name: %s' % (name, said))

            after = file_hashes(state)
            changed = sorted(p for p in before if after.get(p) != before[p])
            added = sorted(set(after) - set(before))
            verdict(not changed and not added,
                    '%s store files: %d unchanged, changed %s, added %s' % (name, len(before), changed, added))
    finally:
        if args.keep:
            print('kept', work)
        else:
            shutil.rmtree(work, ignore_errors=True)

    if failures:
        print('FAIL downgrade refusal: %d verdicts failed' % len(failures))
        return 1
    print('PASS downgrade refusal: previous %s/%s against stores written by current sources' % (job_version, download_version))
    return 0


if __name__ == '__main__':
    sys.exit(main())
