"""Verify an installed C++ facade through the production Go resolver on Windows and Linux.

No arguments prints help. Uses short .build/facade paths, three unique local
endpoints (named pipes on Windows, Unix sockets on Linux), one resolver and
separate service/client homes. No OS installation or C++ server is
involved. The Go fixture composes production providers; native OS adapters are
not certified by this check.
"""
import argparse
from contextlib import ExitStack
import json
import os
from pathlib import Path
import queue
import re
import shutil
import subprocess
import sys
import threading
import time
import uuid

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
from workspace import (environment, layer, cmake_for, certify_compiler, source_revision, CERTIFIES,
                       DRY_RUN_HELP, dry_run_stop,
                       ATTESTATION_BY, attested_principal, fixture_endpoints, host_program, consumer_executable)
ROOT = HERE.parents[2]
from sdk import installed_sdk

BUILD = ROOT / '.build' / 'facade'


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def line(process, timeout=20):
    result = queue.Queue()
    threading.Thread(target=lambda: result.put(process.stdout.readline()), daemon=True).start()
    value = result.get(timeout=timeout)
    if not value:
        raise RuntimeError('process exited before readiness: ' + process.stderr.read())
    return value


def home_environment(env, path):
    result = {key: value for key, value in env.items() if not key.upper().startswith('ABSTRACTION_')}
    for variable in ('HOME', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA', 'ProgramData',
                     'XDG_CONFIG_HOME', 'XDG_DATA_HOME', 'XDG_STATE_HOME', 'XDG_CACHE_HOME'):
        result[variable] = str(path)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__ + '\n' + CERTIFIES)
    parser.add_argument('--run', action='store_true', help='build and run the isolated service/client proof')
    parser.add_argument('--dry-run', action='store_true', help=DRY_RUN_HELP)
    parser.add_argument('--toolchain', action='store_true', help='print CMake version without building/running')
    parser.add_argument('--cmake', help='CMake executable (Windows: Visual Studio bundled CMake only)')
    args = parser.parse_args()
    if args.toolchain:
        subprocess.run([cmake_for(dict(os.environ), args.cmake), '--version'], check=True)
        return
    if not (args.run or args.dry_run):
        parser.print_help()
        return
    source_revision()
    if args.dry_run: dry_run_stop('facade', args)
    windows = os.name == 'nt'
    require(windows or sys.platform.startswith('linux'), 'This proof measures Windows and Linux only')
    BUILD.mkdir(parents=True, exist_ok=True)
    env = environment(BUILD)
    cmake = cmake_for(env, args.cmake)
    host = host_program(BUILD, 'host')

    def run(command, cwd=ROOT, child_env=env, timeout=180):
        result = subprocess.run(list(map(str, command)), cwd=cwd, env=child_env,
                                capture_output=True, text=True, encoding='utf-8', timeout=timeout)
        output = result.stdout + result.stderr
        print(output.replace(str(ROOT), '$ROOT').replace(ROOT.as_posix(), '$ROOT'), end='')
        require(result.returncode == 0, 'command failed: ' + str(command[0]))
        return output

    run(['go', 'version'])
    run(['go', 'build', '-o', host, HERE / 'host.go'])
    token = uuid.uuid4().hex[:8]
    with ExitStack() as stack:
        stage = stack.enter_context(installed_sdk(cmake, 'aggregate', env))
        consumer = BUILD / ('c' + token)
        run([cmake, '-S', HERE, '-B', consumer, '-DCMAKE_PREFIX_PATH=' + str(stage), '-DCMAKE_BUILD_TYPE=Release'])
        certify_compiler(consumer)
        run([cmake, '--build', consumer, '--config', 'Release'])
        executable = consumer_executable(consumer, 'facade_consumer')
        require(executable.is_file(), 'installed facade consumer not built')

        case = BUILD / ('t' + token)
        service_home, client_home = case / 'service', case / 'client'
        service_home.mkdir(parents=True)
        client_home.mkdir()
        config_file = service_home / 'abstraction' / 'config.json'
        config_file.parent.mkdir()
        config_value = {'store': 'service-owned store', 'off': {'test-tier': 'disabled by service owner'}}
        config_file.write_text(json.dumps(config_value), encoding='utf-8')
        output = service_home / 'records.jsonl'
        host_env = home_environment(env, service_home)
        # Prove config reads caller overrides/files, not the service's run override.
        host_env['ABSTRACTION_STORE'] = 'must-not-leak-from-service-environment'
        client_env = home_environment(env, client_home)
        endpoints = stack.enter_context(fixture_endpoints(['logging', 'config', 'router', 'runtime'], 'facade-' + token))
        runtime_endpoint = endpoints.pop('runtime')
        client_env['ABSTRACTION_RUNTIME_ENDPOINT'] = runtime_endpoint
        # No runtime is installed for this proof, so default runtime selection
        # has no installation evidence. The client expects this user's fixture
        # host program instead; every resolved service must still prove it.
        client_env['OA_FACADE_PROOF_SERVER_PROGRAM'] = str(host.resolve())
        for name, variable in [('logging', 'ABSTRACTION_LOG_ENDPOINT'), ('config', 'ABSTRACTION_CONFIG_ENDPOINT'), ('router', 'ABSTRACTION_ROUTER_ENDPOINT')]:
            # Conventional endpoint construction must fail this proof. Only the
            # resolver's registration identifies the working provider endpoints.
            client_env[variable] = endpoints[name] + '-unregistered'
        processes = []
        client = None
        try:
            for name, endpoint in endpoints.items():
                command = [str(host), name, endpoint]
                if name == 'logging':
                    command.append(str(output))
                process = subprocess.Popen(command, env=host_env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                           text=True, encoding='utf-8')
                processes.append(process)
                require(line(process).strip() == 'READY ' + name, 'wrong service readiness')
            resolver = subprocess.Popen([str(host), 'resolver', runtime_endpoint,
                                         *endpoints.values()], env=host_env, stdout=subprocess.PIPE,
                                        stderr=subprocess.PIPE, text=True, encoding='utf-8')
            processes.append(resolver)
            require(line(resolver).strip() == 'READY resolver', 'wrong resolver readiness')
            client = subprocess.Popen([str(executable), 'all'], cwd=client_home, env=client_env,
                                      stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                      text=True, encoding='utf-8')
            require(line(client).startswith('CLIENT VERIFIED:'), 'facade provider assertions failed')
            until = time.monotonic() + 10
            records = []
            while time.monotonic() < until:
                try:
                    records = [json.loads(value) for value in output.read_text(encoding='utf-8').splitlines()]
                except (OSError, json.JSONDecodeError):
                    records = []
                if records:
                    break
                time.sleep(0.025)
            require(len(records) == 1, 'logging provider did not persist one facade record')
            record = records[0]
            require(record['schema'] == 1 and record['level'] == 0 and
                    record['msg'] == 'facade to Go service\nsecond line ☃' and
                    record['attrs'] == {'binding': 'facade', 'empty': ''}, 'logging values changed')
            peer = record['identity'][-1]
            require(peer['verified'] and peer['by'] == ATTESTATION_BY and attested_principal(peer) and
                    Path(peer['exe']).name.lower() == executable.name.lower(),
                    'logging caller identity incorrect: ' + json.dumps(peer))
            _, error = client.communicate('\n', timeout=15)
            require(client.returncode == 0, 'facade consumer failed: ' + error)
            require(all(p.poll() is None for p in processes), 'service died with its client')
            before = output.read_bytes()
            config_value['store'] = 'changed by service owner'
            config_file.write_text(json.dumps(config_value), encoding='utf-8')
            run([executable, 'changed'], cwd=client_home, child_env=client_env, timeout=15)
            require(not list(client_home.rglob('*')), 'facade client created local state')
            require(output.read_bytes() == before, 'configuration read changed service log')
            print('PASS: installed facade resolves logging/config/router; conventional endpoint overrides cannot bypass selection')
            resolver.kill()
            resolver.communicate(timeout=10)
            require(all(p.poll() is None for p in processes[:-1]), 'provider stopped before resolver-absence proof')
            # Point legacy endpoint overrides at live providers. An absent resolver
            # must still fail, rather than silently choosing these alternatives.
            for name, variable in [('logging', 'ABSTRACTION_LOG_ENDPOINT'), ('config', 'ABSTRACTION_CONFIG_ENDPOINT'), ('router', 'ABSTRACTION_ROUTER_ENDPOINT')]:
                client_env[variable] = endpoints[name]
            run([executable, 'absent'], cwd=client_home, child_env=client_env, timeout=15)
            require(output.read_bytes() == before, 'resolver absence sent a write to a conventional provider')
            print('PASS: absent resolver refuses all operations while conventional providers remain live')
        finally:
            if client is not None and client.poll() is None:
                client.kill()
                client.communicate(timeout=10)
            for process in processes:
                if process.poll() is None:
                    process.kill()
                process.communicate(timeout=10)
        run([executable, 'absent'], cwd=client_home, child_env=client_env, timeout=15)
        require(not list(client_home.rglob('*')), 'absent-service facade fallback created state')
        require(output.read_bytes() == before, 'absent facade changed service-owned storage')
        require(json.loads(config_file.read_text()) == config_value, 'client changed configuration provider file')
        print('PASS: service-owned state survives client exit; all six operation checks fail when services are absent; no client store')


if __name__ == '__main__':
    main()
