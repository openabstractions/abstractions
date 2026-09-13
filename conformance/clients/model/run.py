"""Build an installed-package C++ consumer and run isolated Go model fixtures."""
import argparse
from contextlib import ExitStack
import os
import tempfile
from pathlib import Path
import subprocess
import sys
HERE=Path(__file__).resolve().parent
sys.path.insert(0,str(HERE.parent))
from workspace import ROOT, environment, compiler_metadata
from sdk import installed_sdk
p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--run',action='store_true')
p.add_argument('--prefix',type=Path,help='existing installed facade/model/ipc prefix')
p.add_argument('--cmake',default='cmake')
p.add_argument('--direct-probe',type=Path,help='consumer built separately with MODEL_DIRECT_ONLY=ON')
p.add_argument('--generic-probe',type=Path,help='separately installed generic model consumer')
p.add_argument('--race',action='store_true',help='run Go fixtures with race detector; configured C compiler required')
a=p.parse_args()
if not a.run:p.print_help();raise SystemExit(0)
if a.prefix is not None and not a.prefix.is_dir():p.error('--prefix must name an installed package directory')
if sys.platform=='darwin':raise SystemExit('Program proof required; this fixture cannot claim macOS conformance')
with ExitStack() as stack:
 build_root=ROOT/'.build';build_root.mkdir(parents=True,exist_ok=True)
 build=Path(stack.enter_context(tempfile.TemporaryDirectory(prefix='model-consumer-',dir=build_root)))
 env=environment(build)
 def run(args,cwd=ROOT):
  result=subprocess.run([str(x) for x in args],cwd=cwd,env=env,capture_output=True,text=True,encoding='utf-8',timeout=120)
  print(result.stdout,end='')
  if result.returncode:raise RuntimeError(result.stderr or result.stdout)
 if a.prefix is None:
  a.prefix=stack.enter_context(installed_sdk(a.cmake, 'aggregate', env))
 run([a.cmake,'--version'])
 if a.direct_probe is None or a.generic_probe is None:
  direct_prefix=stack.enter_context(installed_sdk(a.cmake, 'model-resolution', env))
  run([a.cmake,'-S',HERE,'-B',build/'direct','-DMODEL_DIRECT_ONLY=ON','-DMODEL_GENERIC=ON','-DCMAKE_PREFIX_PATH='+str(direct_prefix)])
  run([a.cmake,'--build',build/'direct','--config','Release'])
  if a.direct_probe is None:a.direct_probe=build/'direct'/('Release/model_direct.exe' if os.name=='nt' else 'model_direct')
  if a.generic_probe is None:a.generic_probe=build/'direct'/('Release/model_generic.exe' if os.name=='nt' else 'model_generic')
 missing=subprocess.run([a.cmake,'-S',str(HERE),'-B',str(build/'missing-request'),
  '-DMODEL_DIRECT_ONLY=ON','-DCMAKE_PREFIX_PATH='+str(a.prefix.resolve()),
  '-DCMAKE_DISABLE_FIND_PACKAGE_abstraction_download_request=TRUE'],cwd=ROOT,env=env,capture_output=True,text=True,timeout=120)
 if missing.returncode==0 or 'abstraction_download_request' not in missing.stdout+missing.stderr:
  raise RuntimeError('missing required request package was not explicitly refused')
 print('missing named download request dependency refused')

 run([a.cmake,'-S',HERE,'-B',build/'cpp','-DCMAKE_PREFIX_PATH='+str(a.prefix.resolve())])
 run([a.cmake,'--build',build/'cpp','--config','Release','--parallel','4'])
 compiler_metadata(build/'cpp')
 exe=build/'cpp'/('Release/model_consumer.exe' if os.name=='nt' else 'model_consumer')
 env['OA_CPP_MODEL_PROBE']=str(exe)
 env['OA_CPP_MODEL_GENERIC']=str(a.generic_probe.resolve())
 env['OA_CPP_MODEL_DIRECT']=str(a.direct_probe.resolve() if a.direct_probe else build/'cpp'/('Release/model_direct.exe' if os.name=='nt' else 'model_direct'))
 run([exe,'--help'])
 run(['go','test',*(['-race'] if a.race else []),'-count=1','-v',HERE/'fixture_test.go'])
