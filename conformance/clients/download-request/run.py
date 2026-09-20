"""Build/install download CMake package and consume its request header outside the source tree."""
import argparse, hashlib, os, shutil, subprocess, sys, tempfile
from pathlib import Path
HERE=Path(__file__).resolve().parent
sys.path.insert(0,str(HERE.parent))
from workspace import ROOT, layer, environment, cmake_for, certify_compiler, CERTIFIES, source_revision, dry_run_stop, DRY_RUN_HELP, CMAKE_BUILD_TYPE_RELEASE
p=argparse.ArgumentParser(description=__doc__+' '+CERTIFIES)
p.add_argument('--run',action='store_true')
p.add_argument('--dry-run', action='store_true', help=DRY_RUN_HELP)
p.add_argument('--cmake',help='CMake executable (Windows: Visual Studio bundled CMake only)')
p.add_argument('--generator',help='optional CMake generator')
a=p.parse_args()
if not (a.run or a.dry_run): p.print_help();raise SystemExit(0)
source_revision()
if a.dry_run: dry_run_stop('download-request', a)
b=ROOT/'.build'/'req';b.mkdir(parents=True,exist_ok=True)
env=environment(b);a.cmake=cmake_for(env,a.cmake)
def run(args):
 r=subprocess.run([str(x) for x in args],cwd=ROOT,env=env,capture_output=True,text=True,encoding='utf-8',timeout=180)
 if r.returncode: print(r.stdout+r.stderr);r.check_returncode()
 print(r.stdout[-1500:],end='')
gen=['-G',a.generator] if a.generator else []
# A fresh tree per run, removed afterwards: a cache from another toolchain
# installs no per-configuration export file for --config Release, and Windows
# may still hold a build file briefly, which does not fail the proof. The
# request consumer and Go reader keep exchanging files in b itself.
with tempfile.TemporaryDirectory(prefix='r-',dir=b,ignore_cleanup_errors=True) as temp:
 work=Path(temp);build,prefix,consumer,outside=work/'b',work/'p',work/'c',work/'outside'
 run([a.cmake,'-S',layer('abstraction-download')/'cpp','-B',build,'-DABSTRACTION_BUILD_TESTS=OFF',CMAKE_BUILD_TYPE_RELEASE,*gen])
 certify_compiler(build)
 run([a.cmake,'--build',build,'--config','Release','--parallel','4'])
 run([a.cmake,'--install',build,'--config','Release','--prefix',prefix])
 relative=Path('abstraction/download/request/rec.h')
 assert (prefix/'include'/relative).read_bytes()==(layer('abstraction-download')/'cpp'/relative).read_bytes()
 # Copy only the consumer sources outside the source checkout's include hierarchy.
 outside.mkdir()
 for name in ('CMakeLists.txt','client.cpp'): shutil.copyfile(HERE/name,outside/name)
 run([a.cmake,'-S',outside,'-B',consumer,'-DCMAKE_PREFIX_PATH='+str(prefix),CMAKE_BUILD_TYPE_RELEASE,*gen])
 certify_compiler(consumer)
 run([a.cmake,'--build',consumer,'--config','Release'])
 exe=consumer/'Release'/'request_consumer.exe' if os.name=='nt' else consumer/'request_consumer'
 run([exe,b])
 run(['go','run',HERE/'reader.go',b])
 print('Installed header SHA256',hashlib.sha256((prefix/'include'/relative).read_bytes()).hexdigest())
