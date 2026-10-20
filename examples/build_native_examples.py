"""Build the two Windows review examples with their real native host resources.

Uses the existing AppModel CMake owner, rc/cvtres and COFF sanitizer. Downloads
nothing. Temporary resources/builds are removed after production. Run --help.
"""
import argparse
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
from scripts.build_node_addon import appmodel_tools
from scripts.windows_native import validate_portable_windows_dll


def run(argv, **options):
    environment = dict(os.environ, GOTOOLCHAIN='local', GOPROXY='off', GOSUMDB='off',
                       GOWORK=str(ROOT/'go.work'))
    subprocess.run(list(map(str, argv)), check=True, env=environment, **options)


def build(output, go):
    tools = appmodel_tools()
    output = output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="oa-example-build-", dir=output) as temporary:
        work = Path(temporary)
        native = work / "native"
        facade = ROOT / "openabstractions-flat/abstraction-facade/cpp"
        run([tools['cmake'], '-S', facade, '-B', native, '-G', 'Ninja',
             '-DCMAKE_BUILD_TYPE=Release', '-DCMAKE_CXX_COMPILER='+str(tools['compiler']),
             '-DCMAKE_LINKER='+str(tools['linker']), '-DCMAKE_MAKE_PROGRAM='+str(tools['ninja']),
             '-DCMAKE_FIND_USE_PACKAGE_REGISTRY=FALSE', '-DCMAKE_FIND_USE_SYSTEM_PACKAGE_REGISTRY=FALSE',
             '-DABSTRACTION_FACADE_BUILD_AGGREGATE=OFF', '-DABSTRACTION_FACADE_BUILD_APPMODEL=ON',
             '-DABSTRACTION_FACADE_APPMODEL_PORTABLE_WINDOWS=ON',
             '-DAPPMODEL_DOWNLOAD_RECORDS='+str(ROOT/'openabstractions-flat/abstraction-download/cpp'),
             '-DAPPMODEL_ROUTER_RECORDS='+str(ROOT/'openabstractions-flat/abstraction-router/cpp')])
        run([tools['cmake'], '--build', native, '--target', 'abstraction_facade_appmodel', '--parallel', '2'])
        library = native/'abstraction_facade_appmodel.dll'
        validate_portable_windows_dll(library, 'win_amd64')
        rc, cvtres = shutil.which('rc.exe'), shutil.which('cvtres.exe')
        if not rc or not cvtres:
            raise ValueError('existing Windows resource toolchain required')
        for name in ('pretend-lemonade', 'logging-quickstart'):
            module = ROOT/'examples'/name
            resource = work/(name+'.res')
            syso = module/'oa_application_windows_amd64.syso'
            if syso.exists():
                raise ValueError('refusing existing example resource object: '+str(syso))
            try:
                run([rc, '/nologo', '/fo', resource, 'application.rc'], cwd=module)
                run([cvtres, '/nologo', '/machine:x64', '/out:'+str(syso), resource])
                run([go, 'run', *[str(ROOT/'monitor/icon/gen'/name) for name in ('main.go','native.go','agent_release.go')],
                     '-sanitize-coff', str(syso)], cwd=ROOT)
                run([go, '-C', module, 'build', '-o', output/(name+'.exe'), '.'], cwd=ROOT)
            finally:
                syso.unlink(missing_ok=True)
        shutil.copyfile(library, output/library.name)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--go', default='go')
    parser.add_argument('--developer-environment', action='store_true', help=argparse.SUPPRESS)
    args = parser.parse_args()
    if sys.platform != 'win32' or os.environ.get('PROCESSOR_ARCHITECTURE', '').lower() != 'amd64':
        parser.error('normal native example producer requires Windows x64')
    if not args.developer_environment:
        tools = appmodel_tools()
        with tempfile.TemporaryDirectory(prefix='oa-example-vcvars-') as temporary:
            command = Path(temporary)/'build.cmd'
            command.write_text('@echo off\ncall "'+str(tools['vcvars'])+'" >nul\n'
                'if errorlevel 1 exit /b %errorlevel%\n'+subprocess.list2cmdline([
                sys.executable, '-B', str(Path(__file__).resolve()), '--output', str(args.output.resolve()),
                '--go', args.go, '--developer-environment'])+'\nexit /b %errorlevel%\n', encoding='utf-8')
            return subprocess.run([str(tools['cmd']), '/d', '/c', str(command)]).returncode
    build(args.output, args.go)
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
