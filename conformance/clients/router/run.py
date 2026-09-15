"""Build an installed C++ consumer and run the real framed router service.

Uses isolated fake HTTP model hosts, private endpoints and client homes. No
product is installed into the OS; CMake packages are staged under .build. Runs
on Windows (MSVC, named pipes) and Linux (GCC, Unix sockets). The fixture uses
the production service and existing router provider.
"""
import argparse
import os
from pathlib import Path
import queue
import subprocess
import sys
import threading
import uuid

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
from workspace import (ROOT, layer, environment, cmake_for, certify_compiler, source_revision, CERTIFIES,
                       DRY_RUN_HELP, dry_run_stop, fixture_endpoints, host_program, consumer_executable)

parser = argparse.ArgumentParser(description=__doc__ + '\n' + CERTIFIES)
parser.add_argument('--run', action='store_true', help='build and run the isolated C++ service proof')
parser.add_argument('--dry-run', action='store_true', help=DRY_RUN_HELP)
parser.add_argument('--cmake', help='CMake executable (Windows: Visual Studio bundled CMake only)')
if len(sys.argv) == 1:
    parser.print_help()
    raise SystemExit(0)
args = parser.parse_args()
if not (args.run or args.dry_run):
    parser.error('choose --run or --dry-run')
source_revision()
if args.dry_run: dry_run_stop('router', args)
windows = os.name == 'nt'
if not windows and not sys.platform.startswith('linux'):
    raise SystemExit('This runner proves Windows and Linux only.')
BUILD = ROOT / '.build' / 'router'
BUILD.mkdir(parents=True, exist_ok=True)
env = environment(BUILD)
cmake = cmake_for(env, args.cmake)
def run(command, cwd=ROOT, environment=env):
    result = subprocess.run(list(map(str, command)), cwd=cwd, env=environment, capture_output=True, text=True, encoding='utf-8', timeout=180)
    print(result.stdout.replace(str(ROOT), '$ROOT').replace(ROOT.as_posix(), '$ROOT'), end='')
    if result.returncode:
        raise RuntimeError(result.stdout + result.stderr)
    return result.stdout

host = host_program(BUILD, 'host')
run(['go', 'build', '-o', host, HERE / 'server.go'])
token = uuid.uuid4().hex[:8]
# Fresh short trees per run: a cache from another toolchain installs no
# per-configuration export file for --config Release.
native, resolution = BUILD / ('n-' + token), BUILD / ('r-' + token)
stage, consumer = BUILD / ('s-' + token), BUILD / ('c-' + token)
release = ['-DCMAKE_BUILD_TYPE=Release']
run([cmake, '-S', layer('abstraction-router') / 'cpp', '-B', native, '-DBUILD_SHARED_LIBS=OFF', '-DCMAKE_DISABLE_FIND_PACKAGE_abstraction_ipc=TRUE', *release])
certify_compiler(native)
run([cmake, '--build', native, '--config', 'Release'])
run([cmake, '--install', native, '--config', 'Release', '--prefix', stage])
run([cmake,'-S',layer('abstraction-facade')/'cpp','-B',resolution,'-DABSTRACTION_FACADE_BUILD_AGGREGATE=OFF','-DABSTRACTION_FACADE_BUILD_RESOLUTION=ON','-DCMAKE_DISABLE_FIND_PACKAGE_abstraction_ipc=TRUE','-DBUILD_SHARED_LIBS=OFF','-DABSTRACTION_IPC_BUILD_TESTS=OFF', *release])
run([cmake,'--build',resolution,'--config','Release'])
run([cmake,'--install',resolution,'--config','Release','--prefix',stage])
for name in ('logging','config','job','model','storage','asks','rights'):
    if (stage/'include/abstraction'/name).exists():raise RuntimeError('unrelated installed package: '+name)
run([cmake, '-S', HERE, '-B', consumer, '-DCMAKE_PREFIX_PATH=' + str(stage), *release])
certify_compiler(consumer)
run([cmake, '--build', consumer, '--config', 'Release'])
executable = consumer_executable(consumer, 'router_consumer')
for mode in ['normal', 'empty']:
    case = BUILD / uuid.uuid4().hex
    home = case / 'client-home'
    home.mkdir(parents=True)
    trace = case / 'host-requests.txt'
    with fixture_endpoints(['router', 'resolver'], uuid.uuid4().hex[:12]) as endpoints:
        # No runtime is installed for this proof, so the client expects this user's
        # fixture host program instead of installed-runtime evidence.
        client_env = dict(env, ABSTRACTION_ROUTER_ENDPOINT=endpoints['router'], ABSTRACTION_RUNTIME_ENDPOINT=endpoints['resolver'],
                          OA_ROUTER_PROOF_SERVER_PROGRAM=str(host.resolve()))
        for name in ['HOME', 'USERPROFILE', 'LOCALAPPDATA', 'APPDATA', 'XDG_CONFIG_HOME', 'XDG_CACHE_HOME']:
            client_env[name] = str(home)
        command = [str(host), '--endpoint', endpoints['router'], '--resolver', endpoints['resolver'], '--trace', str(trace)]
        if mode == 'empty': command.append('--empty')
        server = subprocess.Popen(command, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, encoding='utf-8')
        ready = queue.Queue()
        threading.Thread(target=lambda: ready.put(server.stdout.readline()), daemon=True).start()
        try:
            if not ready.get(timeout=20).startswith('router-v1: listening'):
                raise RuntimeError('host failed before readiness')
            run([executable, mode], cwd=home, environment=client_env)
            assert server.poll() is None, 'host exited with client'
            assert not list(home.rglob('*')), 'client created local state'
            methods = trace.read_text(encoding='utf-8').splitlines()
            assert all(method == 'GET' for method in methods), 'router mutated a model host'
            if mode == 'normal': assert methods, 'provider hosts were never read'
        finally:
            if server.poll() is None: server.kill()
            stdout, stderr = server.communicate(timeout=10)
            if stderr: print(stderr, end='')
        run([executable, 'absent'], cwd=home, environment=client_env)
        assert not list(home.rglob('*')), 'absent-service fallback created state'
print('PASS: installed C++ client -> generated RPC -> shared IPC -> identity-bound Go router provider')
print('Scope: Models/Hosts/Pick subset on ' + ('Windows' if windows else 'Linux') + '; no GPU-cost, HTTP-window or OS-installation claim.')
