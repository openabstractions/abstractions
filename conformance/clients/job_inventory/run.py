"""Build an installed-package C++ consumer and run isolated Go job_inventory fixtures."""
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
p.add_argument('--prefix',type=Path,help='existing installed facade_jobs/ipc prefix')
p.add_argument('--cmake',default='cmake')
p.add_argument('--race',action='store_true',help='run Go fixtures with race detector; configured C compiler required')
a=p.parse_args()
if not a.run:p.print_help();raise SystemExit(0)
if a.prefix is not None and not a.prefix.is_dir():p.error('--prefix must name an installed package directory')
if sys.platform=='darwin':raise SystemExit('Program proof required; this fixture cannot claim macOS conformance')
with ExitStack() as stack:
 build_root=ROOT/'.build';build_root.mkdir(parents=True,exist_ok=True)
 build=Path(stack.enter_context(tempfile.TemporaryDirectory(prefix='job_inventory-consumer-',dir=build_root)))
 env=environment(build)
 def run(args,cwd=ROOT):
  result=subprocess.run([str(x) for x in args],cwd=cwd,env=env,capture_output=True,text=True,encoding='utf-8',timeout=120)
  print(result.stdout,end='')
  if result.returncode:raise RuntimeError(result.stderr or result.stdout)
 if a.prefix is None:
  a.prefix=stack.enter_context(installed_sdk(a.cmake, 'jobs', env))
 run([a.cmake,'--version'])
 run([a.cmake,'-S',HERE,'-B',build/'cpp','-DCMAKE_PREFIX_PATH='+str(a.prefix.resolve())])
 run([a.cmake,'--build',build/'cpp','--config','Release','--parallel','4'])
 compiler_metadata(build/'cpp')
 exe=build/'cpp'/('Release/job_inventory_consumer.exe' if os.name=='nt' else 'job_inventory_consumer')
 env['OA_CPP_INVENTORY_PROBE']=str(exe)
 run([exe,'--help'])
 run(['go','test',*(['-race'] if a.race else []),'-count=1','-v',HERE/'fixture_test.go'])
