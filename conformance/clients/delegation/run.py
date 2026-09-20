"""Build an installed-package C++ consumer and run isolated Go delegation fixtures."""
import argparse
from contextlib import ExitStack
import os
from pathlib import Path
import subprocess
import sys
import tempfile
HERE=Path(__file__).resolve().parent
sys.path.insert(0,str(HERE.parent))
from workspace import ROOT, environment, cmake_for, certify_compiler, build_root, CERTIFIES, source_revision, dry_run_stop, DRY_RUN_HELP, CMAKE_BUILD_TYPE_RELEASE
from sdk import installed_sdk
p=argparse.ArgumentParser(description=__doc__+' Without --prefix, builds the jobs+request SDK profile from source. '+CERTIFIES)
p.add_argument('--run',action='store_true')
p.add_argument('--dry-run', action='store_true', help=DRY_RUN_HELP)
p.add_argument('--prefix',type=Path,help='existing installed facade_jobs/ipc/job_acceptance/download_request prefix (default: build one from source)')
p.add_argument('--cmake',help='CMake executable (Windows: Visual Studio bundled CMake only)')
p.add_argument('--race',action='store_true',help='run Go fixtures with race detector; configured C compiler required')
a=p.parse_args()
if not (a.run or a.dry_run):p.print_help();raise SystemExit(0)
source_revision()
if a.dry_run: dry_run_stop('delegation', a)
if a.prefix is not None and not a.prefix.is_dir():p.error('--prefix must name an installed package directory')
if sys.platform=='darwin':raise SystemExit('Program proof required; this fixture cannot claim macOS conformance')
with ExitStack() as stack:
 # A fresh short tree per run, removed afterwards; Windows may still hold a
 # file briefly, which does not fail the proof.
 build=Path(stack.enter_context(tempfile.TemporaryDirectory(prefix='dg-',dir=build_root(),ignore_cleanup_errors=True)))
 env=environment(build);a.cmake=cmake_for(env,a.cmake)
 def run(args,cwd=ROOT):
  result=subprocess.run([str(x) for x in args],cwd=cwd,env=env,capture_output=True,text=True,encoding='utf-8',timeout=120)
  print(result.stdout,end='')
  if result.returncode:raise RuntimeError(result.stderr or result.stdout)
 if a.prefix is None:
  a.prefix=stack.enter_context(installed_sdk(a.cmake,'jobs+request',env))
 run([a.cmake,'--version'])
 run([a.cmake,'-S',HERE,'-B',build/'cpp','-DCMAKE_PREFIX_PATH='+str(a.prefix.resolve()),CMAKE_BUILD_TYPE_RELEASE])
 run([a.cmake,'--build',build/'cpp','--config','Release','--parallel','4'])
 certify_compiler(build/'cpp')
 exe=build/'cpp'/('Release/delegation_consumer.exe' if os.name=='nt' else 'delegation_consumer')
 env['OA_CPP_DELEGATION_PROBE']=str(exe)
 run([exe,'--help'])
 run(['go','test',*(['-race'] if a.race else []),'-count=1','-v',HERE/'fixture_test.go',HERE/'remote_backend_test.go'])
