"""Run a Go logging host and prove independent clients reach its service-owned file.

No arguments shows help. Builds use .build/logging-service-proof; each run uses
a fresh private output directory and isolated client home. No product is installed.
"""
import argparse
import json
import os
from pathlib import Path
import queue
import re
import shutil
import subprocess
import sys
import threading
import uuid

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
from workspace import environment, layer
ROOT = HERE.parents[2]
BUILD = ROOT / '.build' / 'logging-service-proof'

def cmake_path():
    found = os.environ.get('CMAKE') or shutil.which('cmake')
    if found:
        return found
    for edition in ('Community', 'Professional', 'Enterprise', 'BuildTools'):
        candidate = Path(os.environ.get('ProgramFiles', 'C:/Program Files')) / (
            'Microsoft Visual Studio/18/' + edition +
            '/Common7/IDE/CommonExtensions/Microsoft/CMake/CMake/bin/cmake.exe')
        if candidate.is_file():
            return str(candidate)
    raise RuntimeError('CMake not found; set CMAKE to its executable path')

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--language', choices=['go', 'cpp', 'all'], default='all')
parser.add_argument('--toolchain', action='store_true', help='print CMake version without running tests')
if len(sys.argv) == 1:
    parser.print_help()
    raise SystemExit(0)
args = parser.parse_args()
if args.toolchain:
    subprocess.run([cmake_path(), '--version'], check=True)
    raise SystemExit(0)
if os.name != 'nt':
    raise SystemExit('This service proof currently measures Windows only.')
BUILD.mkdir(parents=True, exist_ok=True)
env = environment(BUILD)

def run(command, cwd=ROOT, environment=env, timeout=120, echo=True):
    result = subprocess.run(list(map(str, command)), cwd=cwd, env=environment,
                            capture_output=True, text=True, encoding='utf-8', timeout=timeout)
    if echo:
        print(result.stdout.replace(str(ROOT), '$ROOT').replace(ROOT.as_posix(), '$ROOT'), end='')
    if result.returncode:
        raise RuntimeError(result.stdout + result.stderr)
    return result.stdout

run(['go', 'version'])
run(['go', 'build', '-o', BUILD / 'host.exe', './serve'])
languages = ['go', 'cpp'] if args.language == 'all' else [args.language]
clients = {}
if 'go' in languages:
    dependencies = run(['go', 'list', '-deps', 'github.com/openabstractions/abstraction-logging/go/client'], echo=False)
    for forbidden in ['github.com/openabstractions/abstraction-logging/go',
                      'github.com/openabstractions/abstraction-job/go',
                      'github.com/openabstractions/abstraction-download/go']:
        if forbidden in dependencies.splitlines():
            raise RuntimeError('client imports provider: ' + forbidden)
    run(['go', 'build', '-o', BUILD / 'go-client.exe', HERE / 'client.go'])
    clients['go'] = BUILD / 'go-client.exe'
if 'cpp' in languages:
    cmake = cmake_path()
    native = BUILD / 'native'
    identity = uuid.uuid4().hex
    stage = BUILD / ('stage-' + identity)
    consumer = BUILD / ('consumer-' + identity)
    run([cmake, '-S', layer('abstraction-logging') / 'cpp',
         '-B', native, '-DBUILD_SHARED_LIBS=OFF',
         '-DCMAKE_DISABLE_FIND_PACKAGE_abstraction_ipc=TRUE'])
    for compiler in sorted((native / 'CMakeFiles').glob('*/CMakeCXXCompiler.cmake')):
        for key, value in re.findall(r'set\(CMAKE_CXX_COMPILER_(ID|VERSION) "([^"]+)"\)', compiler.read_text()):
            print('C++ compiler', key, value)
    run([cmake, '--build', native, '--config', 'Release'])
    run([cmake, '--install', native, '--config', 'Release', '--prefix', stage])
    run([cmake, '-S', HERE, '-B', consumer, '-DCMAKE_PREFIX_PATH=' + str(stage)])
    run([cmake, '--build', consumer, '--config', 'Release'])
    clients['cpp'] = consumer / 'Release' / 'logging_consumer.exe'

for language, executable in clients.items():
    case = BUILD / uuid.uuid4().hex
    client_home = case / 'client-home'
    client_home.mkdir(parents=True)
    output = case / 'service' / 'records.jsonl'
    endpoint = '\\\\.\\pipe\\oa-logging-proof-' + uuid.uuid4().hex
    client_env = dict(env, ABSTRACTION_LOG_ENDPOINT=endpoint)
    for variable in ['HOME', 'USERPROFILE', 'LOCALAPPDATA', 'APPDATA', 'XDG_CONFIG_HOME',
                     'XDG_DATA_HOME', 'XDG_STATE_HOME', 'XDG_CACHE_HOME']:
        client_env[variable] = str(client_home)
    server = subprocess.Popen([str(BUILD / 'host.exe'), 'serve', 'logging',
                               '--endpoint', endpoint, '--out', str(output)],
                              env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                              text=True, encoding='utf-8')
    ready = queue.Queue()
    threading.Thread(target=lambda: ready.put(server.stdout.readline()), daemon=True).start()
    try:
        if not ready.get(timeout=15).startswith('logging: listening '):
            raise RuntimeError('host did not reach listener readiness')
        run([executable, 'write'], cwd=client_home, environment=client_env, timeout=15)
        # The client process is gone; the service and its record remain.
        if server.poll() is not None:
            raise RuntimeError('service died with its client')
        records = [json.loads(line) for line in output.read_text(encoding='utf-8').splitlines()]
        if len(records) != 1:
            raise RuntimeError('wrong record count')
        record = records[0]
        assert record['schema'] == 1 and record['level'] == 0
        assert record['msg'] == 'from ' + language + '\nsecond line ☃'
        assert record['attrs'] == {'language': language, 'empty': ''}
        peer = record['identity'][-1]
        assert len(record['identity']) == 2 and peer['hop'] == 1
        assert record['identity'][0]['by'] == 'self' and not record['identity'][0]['verified']
        assert peer['verified'] and peer['by'] == 'identity/windows'
        assert peer['user'] and Path(peer['exe']).name.lower() == executable.name.lower()
        before = output.read_bytes()
        run([executable, 'bad-schema'], cwd=client_home, environment=client_env, timeout=15)
        assert output.read_bytes() == before, 'invalid record reached provider'
        assert not list(client_home.rglob('*')), 'client created local state'
    finally:
        if server.poll() is None:
            server.kill()
        server.communicate(timeout=10)
    run([executable, 'absent'], cwd=client_home, environment=client_env, timeout=15)
    assert not list(client_home.rglob('*')), 'absent-service fallback created state'
    assert output.read_bytes() == before, 'absent client changed service storage'
    print('PASS:', language, 'independent client -> generated protocol -> shared IPC -> identity-bound Go service -> provider')
    print('PASS: client exited; record survived; schema refused; absent service errored; no client store')
