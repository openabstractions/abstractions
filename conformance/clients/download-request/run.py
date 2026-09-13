"""Build/install download CMake package and consume its request header outside the source tree."""
import argparse, hashlib, os, shutil, subprocess, sys, tempfile
from pathlib import Path
HERE=Path(__file__).resolve().parent
sys.path.insert(0,str(HERE.parent))
from workspace import ROOT, layer, environment, compiler_metadata
p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--run',action='store_true')
p.add_argument('--cmake',default=os.environ.get('CMAKE') or shutil.which('cmake'))
p.add_argument('--generator',help='optional CMake generator')
a=p.parse_args()
if not a.run: p.print_help();raise SystemExit(0)
if not a.cmake: p.error('set --cmake or CMAKE')
b=ROOT/'.build'/'req';b.mkdir(parents=True,exist_ok=True)
prefix=Path(tempfile.mkdtemp(prefix='p-',dir=b))
consumer=Path(tempfile.mkdtemp(prefix='c-',dir=b))
env=environment(b)
def run(args):
 r=subprocess.run([str(x) for x in args],cwd=ROOT,env=env,capture_output=True,text=True,encoding='utf-8',timeout=180)
 if r.returncode: print(r.stdout+r.stderr);r.check_returncode()
 print(r.stdout[-1500:],end='')
gen=['-G',a.generator] if a.generator else []
run([a.cmake,'-S',layer('abstraction-download')/'cpp','-B',b/'build','-DABSTRACTION_BUILD_TESTS=OFF',*gen])
compiler_metadata(b/'build')
run([a.cmake,'--build',b/'build','--config','Release','--parallel','4'])
run([a.cmake,'--install',b/'build','--config','Release','--prefix',prefix])
relative=Path('abstraction/download/request/rec.h')
assert (prefix/'include'/relative).read_bytes()==(layer('abstraction-download')/'cpp'/relative).read_bytes()
# Copy only the consumer sources outside the source checkout's include hierarchy.
outside=b/'outside';outside.mkdir(exist_ok=True)
for name in ('CMakeLists.txt','client.cpp'): shutil.copyfile(HERE/name,outside/name)
run([a.cmake,'-S',outside,'-B',consumer,'-DCMAKE_PREFIX_PATH='+str(prefix),*gen])
run([a.cmake,'--build',consumer,'--config','Release'])
exe=consumer/'Release'/'request_consumer.exe' if os.name=='nt' else consumer/'request_consumer'
run([exe,b])
run(['go','run',HERE/'reader.go',b])
print('Installed header SHA256',hashlib.sha256((prefix/'include'/relative).read_bytes()).hexdigest())
