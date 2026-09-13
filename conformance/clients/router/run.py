"""Build an installed C++ consumer and run the real framed router service.

Uses isolated fake HTTP model hosts, named endpoints and client homes. No product
is installed into the OS; CMake packages are staged under .build. Windows proof
only. The fixture uses the production service and existing router provider.
"""
import argparse
import os
from pathlib import Path
import queue
import shutil
import subprocess
import sys
import threading
import uuid

HERE = Path(__file__).resolve().parent
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--run', action='store_true', help='build and run the isolated C++ service proof')
if len(sys.argv) == 1:
    parser.print_help()
    raise SystemExit(0)
args = parser.parse_args()
if not args.run:
    parser.error('choose --run')
if os.name != 'nt':
    raise SystemExit('This runner currently proves Windows only.')
sys.path.insert(0, str(HERE.parent))
from workspace import ROOT, layer, environment, compiler_metadata
BUILD = ROOT / '.build' / 'router'
BUILD.mkdir(parents=True, exist_ok=True)
env = environment(BUILD)
cmake = os.environ.get('CMAKE') or shutil.which('cmake')
if not cmake:
    cmake = str(Path(os.environ.get('ProgramFiles', 'C:/Program Files')) / 'Microsoft Visual Studio/18/Community/Common7/IDE/CommonExtensions/Microsoft/CMake/CMake/bin/cmake.exe')
def run(command, cwd=ROOT, environment=env):
    result = subprocess.run(list(map(str, command)), cwd=cwd, env=environment, capture_output=True, text=True, encoding='utf-8', timeout=120)
    print(result.stdout.replace(str(ROOT), '$ROOT').replace(ROOT.as_posix(), '$ROOT'), end='')
    if result.returncode:
        raise RuntimeError(result.stdout + result.stderr)
    return result.stdout

run(['go', 'build', '-o', BUILD / 'host.exe', HERE / 'server.go'])
stage = BUILD / ('s-' + uuid.uuid4().hex[:8])
consumer = BUILD / ('c-' + uuid.uuid4().hex[:8])
run([cmake, '-S', layer('abstraction-router') / 'cpp', '-B', BUILD / 'native', '-DBUILD_SHARED_LIBS=OFF', '-DCMAKE_DISABLE_FIND_PACKAGE_abstraction_ipc=TRUE'])
compiler_metadata(BUILD / 'native')
run([cmake, '--build', BUILD / 'native', '--config', 'Release'])
run([cmake, '--install', BUILD / 'native', '--config', 'Release', '--prefix', stage])
run([cmake,'-S',layer('abstraction-facade')/'cpp','-B',BUILD/'resolution','-DABSTRACTION_FACADE_BUILD_AGGREGATE=OFF','-DABSTRACTION_FACADE_BUILD_RESOLUTION=ON','-DCMAKE_DISABLE_FIND_PACKAGE_abstraction_ipc=TRUE','-DBUILD_SHARED_LIBS=OFF','-DABSTRACTION_IPC_BUILD_TESTS=OFF'])
run([cmake,'--build',BUILD/'resolution','--config','Release'])
run([cmake,'--install',BUILD/'resolution','--config','Release','--prefix',stage])
for name in ('logging','config','job','model','storage','asks','rights'):
    if (stage/'include/abstraction'/name).exists():raise RuntimeError('unrelated installed package: '+name)
run([cmake, '-S', HERE, '-B', consumer, '-DCMAKE_PREFIX_PATH=' + str(stage)])
run([cmake, '--build', consumer, '--config', 'Release'])
executable = consumer / 'Release' / 'router_consumer.exe'
for mode in ['normal', 'empty']:
    case = BUILD / uuid.uuid4().hex
    home = case / 'client-home'
    home.mkdir(parents=True)
    trace = case / 'host-requests.txt'
    endpoint = r'\\.\pipe\oa-router-proof-' + uuid.uuid4().hex
    client_env = dict(env, ABSTRACTION_ROUTER_ENDPOINT=endpoint, ABSTRACTION_RUNTIME_ENDPOINT=endpoint+"-resolver")
    for name in ['HOME', 'USERPROFILE', 'LOCALAPPDATA', 'APPDATA', 'XDG_CONFIG_HOME', 'XDG_CACHE_HOME']:
        client_env[name] = str(home)
    command = [str(BUILD / 'host.exe'), '--endpoint', endpoint, '--resolver', endpoint+'-resolver', '--trace', str(trace)]
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
print('Scope: Models/Hosts/Pick subset on Windows; no GPU-cost, HTTP-window or OS-installation claim.')
