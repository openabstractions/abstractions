"""Locate service-check sources in the private tree or sibling public checkouts.

No cloning, installation or source changes. A public checkout needs the relevant
abstraction repositories beside `abstractions/`; the temporary Go workspace lives
under the check's build directory. Go module downloads respect GOPROXY (off by
default for these checks).
"""
import json
import os
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

def compiler_metadata(build):
    for source in sorted((Path(build) / 'CMakeFiles').glob('*/CMakeCXXCompiler.cmake')):
        for key, value in re.findall(r'set\(CMAKE_CXX_COMPILER_(ID|VERSION) "([^"]+)"\)', source.read_text()):
            print('C++ compiler', key, value)

def layer(name):
    base = ROOT / 'openabstractions-flat'
    if not base.is_dir():
        base = ROOT.parent
    result = base / name
    if not result.is_dir():
        raise RuntimeError('Missing source checkout: ' + str(result))
    return result

def environment(build):
    work = ROOT / 'go.work'
    if not work.is_file():
        base = ROOT.parent
        modules = [ROOT / 'serve']
        for repository in sorted(base.glob('abstraction-*')):
            for module in (repository, repository / 'go'):
                if (module / 'go.mod').is_file():
                    modules.append(module)
        build = Path(build)
        build.mkdir(parents=True, exist_ok=True)
        work = build / 'go.work'
        work.write_text('go 1.26.0\n\nuse (\n' + ''.join(
            '  ' + json.dumps(os.path.relpath(p.resolve(), build.resolve())) + '\n' for p in modules) + ')\n', encoding='utf-8')
    return dict(os.environ, GOWORK=str(work), GOPROXY=os.environ.get('GOPROXY', 'off'))
