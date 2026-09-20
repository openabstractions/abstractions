"""Build the unchanged C++ lifecycle application and check its typed refusals.

Runs on Windows (MSVC, named pipes) and Linux (GCC, Unix sockets). The
application is conformance/clients/linux_lifecycle/client.cpp, built against a
fresh source-derived jobs+request SDK exactly as the Linux lifecycle builds it.
No runtime is installed or started. `absent` runs twice with the endpoint
override: once naming a foreign listener (a production Go runtime host that is
never the selected installation) and once naming an endpoint nobody serves.
Each run must print `REFUSED resolution runtime_unavailable cause WORD reason
TEXT`, where WORD is an IPC status name. --keep DIR keeps the SDK, the
application build and the host for rerunning the application by hand.
"""
import argparse
import os
from pathlib import Path
import queue
import re
import subprocess
import sys
import threading
import uuid

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
from workspace import (build_root, build_tree, certify_compiler, cmake_for, consumer_executable, fixture_endpoints,
                       host_program, source_revision, dry_run_stop, CERTIFIES, DRY_RUN_HELP, KEEP_HELP)
from sdk import installed_sdk

ROOT = HERE.parents[2]
APPLICATION = HERE.parent / 'linux_lifecycle'
REFUSAL = re.compile(r'^REFUSED resolution (\S+) cause (\S+)(?: reason (.+))?$')
STATUS_WORDS = {'timeout', 'disconnected', 'io_error', 'invalid_argument', 'no_memory', 'internal_error',
                'cancelled', 'untrusted', 'proof_unavailable'}


def refusal(output):
    """The (status, cause, reason) of the one REFUSED line, or None."""
    lines = [REFUSAL.match(line.strip()) for line in output.splitlines() if line.startswith('REFUSED')]
    return lines[0].groups() if len(lines) == 1 and lines[0] else None


def check_refusal(output, causes):
    """Require the typed refusal: runtime_unavailable, a cause in causes and a reason."""
    found = refusal(output)
    if not found:
        raise RuntimeError('no typed refusal line in: ' + output)
    status, cause, reason = found
    if status != 'runtime_unavailable' or cause not in causes or not reason:
        raise RuntimeError(f'refusal {found!r}: want runtime_unavailable, a cause in {sorted(causes)} and a reason')
    return found


def main():
    parser = argparse.ArgumentParser(description=__doc__ + '\n' + CERTIFIES, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--run', action='store_true', help='build the application and check its refusals on this host')
    parser.add_argument('--dry-run', action='store_true', help=DRY_RUN_HELP)
    parser.add_argument('--cmake', help='CMake executable (Windows: Visual Studio bundled CMake only)')
    parser.add_argument('--keep', metavar='DIR', help=KEEP_HELP)
    args = parser.parse_args()
    if not (args.run or args.dry_run):
        parser.print_help()
        return
    source_revision()
    if args.dry_run:
        dry_run_stop('lifecycle_client', args)
    windows = os.name == 'nt'
    if not windows and not sys.platform.startswith('linux'):
        raise RuntimeError('this check measures Windows/MSVC and Linux only')
    env = dict(os.environ)
    cmake = cmake_for(env, args.cmake)

    def run(argv, cwd=ROOT, child_env=env, timeout=300):
        result = subprocess.run([str(a) for a in argv], cwd=cwd, env=child_env, capture_output=True, text=True,
                                encoding='utf-8', errors='replace', timeout=timeout)
        output = result.stdout + result.stderr
        print(output, end='', flush=True)
        if result.returncode:
            raise RuntimeError(f'{argv[0]} exited {result.returncode}')
        return result.stdout

    with build_tree('lc-', args.keep, build_root()) as base:
        with installed_sdk(cmake, 'jobs+request', env, keep=base / 'sdk' if args.keep else None) as prefix:
            app = base / 'app'
            run([cmake, '-S', APPLICATION, '-B', app, '-DCMAKE_PREFIX_PATH=' + str(prefix), '-DCMAKE_BUILD_TYPE=Release'])
            certify_compiler(app)
            run([cmake, '--build', app, '--config', 'Release'])
        executable = consumer_executable(app, 'lifecycle_consumer')
        if not executable.is_file():
            raise RuntimeError('lifecycle application not built: ' + str(executable))
        host = host_program(base, 'host')
        run(['go', 'build', '-o', host, HERE.parent / 'rust_services' / 'host.go'])
        home = base / 'home'
        home.mkdir(exist_ok=True)
        child_env = {k: v for k, v in env.items() if not k.upper().startswith('ABSTRACTION_')}
        child_env.update(HOME=str(home), USERPROFILE=str(home))
        if 'lifecycle_consumer absent' not in run([executable, '--help'], home, child_env):
            raise RuntimeError('application help missing')

        with fixture_endpoints(['foreign'], 'lc-' + uuid.uuid4().hex[:12]) as endpoints:
            foreign = endpoints['foreign']
            peer = subprocess.Popen([str(host), 'runtime', foreign], cwd=base, env=child_env, stdin=subprocess.PIPE,
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            lines = queue.Queue()
            threading.Thread(target=lambda: [lines.put(x.strip()) for x in peer.stdout], daemon=True).start()
            try:
                if lines.get(timeout=20) != 'READY':
                    raise RuntimeError('fixture host did not become ready')
                # A foreign listener: installed selection refuses it before a payload byte.
                output = run([executable, 'absent'], home, dict(child_env, ABSTRACTION_RUNTIME_ENDPOINT=foreign))
                found = check_refusal(output, {'untrusted', 'proof_unavailable'})
                print('PASS foreign listener refused:', found[0], found[1], flush=True)
            finally:
                if peer.poll() is None:
                    try:
                        peer.stdin.write('\n')
                        peer.stdin.flush()
                    except OSError:
                        pass
                try:
                    peer.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    peer.kill()
                    peer.wait()
        with fixture_endpoints(['nobody'], 'lc-' + uuid.uuid4().hex[:12]) as endpoints:
            # Nobody serves the endpoint: selection refuses without an installation,
            # and the connection fails with one.
            output = run([executable, 'absent'], home, dict(child_env, ABSTRACTION_RUNTIME_ENDPOINT=endpoints['nobody']))
            found = check_refusal(output, STATUS_WORDS)
            print('PASS absent endpoint refused:', found[0], found[1], flush=True)
    print('PASS C++ lifecycle application prints typed refusals with status, cause and reason on ' +
          ('Windows' if windows else 'Linux') + ('; build tree kept' if args.keep else ''))


if __name__ == '__main__':
    main()
