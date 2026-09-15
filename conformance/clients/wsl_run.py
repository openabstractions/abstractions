"""Run a conformance runner inside WSL from an extracted copy of this worktree.

A worktree's .git names a Windows path that git inside WSL cannot read, and a
runner started from /mnt/c stops at its source revision. This helper streams the
worktree's tracked and untracked, non-ignored files (git ls-files -co
--exclude-standard) over one pipe into a fresh directory WSL owns. It sets
OA_SOURCE_REVISION to HEAD, marked when uncommitted changes are present, and
runs python3 conformance/clients/<runner>/run.py with the given arguments. The
directory is removed afterwards and the runner's exit status is returned.

  python conformance/clients/wsl_run.py js_services --pure
  python conformance/clients/wsl_run.py --env GOTOOLCHAIN=local js_services --run
  python conformance/clients/wsl_run.py --dry-run router --run

--dry-run prints the file count, revision and WSL command, and checks that
wsl.exe answers, without copying anything. Arguments after the runner name go
to the runner unchanged. Run it from Windows; inside Linux call the runner directly.
"""
import argparse
import os
import shlex
import shutil
import subprocess
import sys
import tarfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]

# Extract stdin into a private directory, run the runner, clean up, keep its status.
SCRIPT = ('d=$(mktemp -d /var/tmp/oa-wsl-run.XXXXXX) || exit 1; '
          'tar -xf - -C "$d" || { rm -rf "$d"; exit 1; }; '
          'cd "$d" && python3 "conformance/clients/$1/run.py" "${@:2}"; rc=$?; '
          'cd / && rm -rf "$d"; exit $rc')


def git(*args):
    return subprocess.run(['git', *args], cwd=ROOT, check=True, capture_output=True, timeout=120).stdout


def worktree_files():
    listed = git('ls-files', '-coz', '--exclude-standard').split(b'\0')
    return [name.decode('utf-8') for name in listed if name and (ROOT / name.decode('utf-8')).is_file()]


def revision():
    head = git('rev-parse', 'HEAD').decode().strip()
    return head + (' (uncommitted changes present)' if git('status', '--short').strip() else '')


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument('--dry-run', action='store_true', help='report what would be copied and run, without copying')
    p.add_argument('--env', action='append', default=[], metavar='NAME=VALUE',
                   help='set a variable for the runner inside WSL; repeatable (for example GOTOOLCHAIN=local)')
    p.add_argument('--distribution', help='WSL distribution (default: the default distribution)')
    p.add_argument('runner', nargs='?', help='runner directory under conformance/clients, for example js_services')
    p.add_argument('args', nargs=argparse.REMAINDER, help='arguments passed to the runner')
    a = p.parse_args()
    if not a.runner:
        p.print_help()
        return 0
    if not (HERE / a.runner / 'run.py').is_file():
        p.error('no runner conformance/clients/%s/run.py' % a.runner)
    wsl = shutil.which('wsl.exe')
    if not wsl:
        raise SystemExit('wsl.exe not found: this helper runs from Windows with WSL installed.')
    env = {}
    for item in a.env:
        name, sep, value = item.partition('=')
        if not sep or not name.isidentifier():
            p.error('--env takes NAME=VALUE, got %r' % item)
        env[name] = value
    env['OA_SOURCE_REVISION'] = revision()
    files = worktree_files()
    exports = ' '.join('export %s=%s;' % (k, shlex.quote(v)) for k, v in env.items())
    command = [wsl, *(['-d', a.distribution] if a.distribution else []), '-e', 'bash', '-c',
               exports + ' ' + SCRIPT, 'wsl_run', a.runner, *a.args]
    if a.dry_run:
        probe = subprocess.run([*command[:command.index('-e')], '-e', 'bash', '-c', 'type -p python3 tar'],
                               capture_output=True, text=True, timeout=120)
        print('dry run wsl_run: %d files, OA_SOURCE_REVISION=%s' % (len(files), env['OA_SOURCE_REVISION']))
        print('dry run wsl_run: runner = conformance/clients/%s/run.py %s' % (a.runner, ' '.join(a.args)))
        print('dry run wsl_run: environment =', ', '.join('%s=%s' % kv for kv in env.items()))
        print('dry run wsl_run: wsl python3 and tar =', probe.stdout.split() if probe.returncode == 0 else 'unavailable')
        print('DRY RUN wsl_run: stopped before copying')
        return 0
    print('wsl_run: copying %d files at %s into WSL for %s' % (len(files), env['OA_SOURCE_REVISION'], a.runner), flush=True)
    proc = subprocess.Popen(command, stdin=subprocess.PIPE)
    try:
        with tarfile.open(fileobj=proc.stdin, mode='w|') as archive:
            for name in files:
                archive.add(str(ROOT / name), arcname=name, recursive=False)
        proc.stdin.close()
    except BrokenPipeError:
        pass
    return proc.wait()


if __name__ == '__main__':
    sys.exit(main())
