"""Build a config-only installed C++ SDK and exercise a private Go runtime."""
import argparse, os, subprocess, sys, tempfile
from pathlib import Path
HERE=Path(__file__).resolve().parent
sys.path.insert(0,str(HERE.parent))
from workspace import ROOT, environment, cmake_for, certify_compiler, build_root, CERTIFIES, source_revision, dry_run_stop, DRY_RUN_HELP
p=argparse.ArgumentParser(description=__doc__+' '+CERTIFIES)
p.add_argument('--run',action='store_true')
p.add_argument('--dry-run', action='store_true', help=DRY_RUN_HELP)
p.add_argument('--cmake',help='CMake executable (Windows: Visual Studio bundled CMake only)')
p.add_argument('--race',action='store_true')
a=p.parse_args()
if not (a.run or a.dry_run):p.print_help();raise SystemExit(0)
source_revision()
if a.dry_run: dry_run_stop('config_observation', a)
with tempfile.TemporaryDirectory(prefix='co-',dir=build_root()) as temp:
 b=Path(temp);env=environment(b);a.cmake=cmake_for(env,a.cmake)
 def run(args):subprocess.run([str(x) for x in args],cwd=ROOT,env=env,check=True,timeout=180)
 run([a.cmake,'-S',ROOT/'openabstractions-flat/abstraction-config/cpp','-B',b/'config','-DBUILD_SHARED_LIBS=OFF','-DABSTRACTION_IPC_BUILD_TESTS=OFF','-DCMAKE_BUILD_TYPE=Release'])
 certify_compiler(b/'config')
 run([a.cmake,'--build',b/'config','--config','Release'])
 run([a.cmake,'--install',b/'config','--config','Release','--prefix',b/'prefix'])
 source=ROOT/'openabstractions-flat/abstraction-facade/cpp' 
 run([a.cmake,'-S',source,'-B',b/'sdk','-DABSTRACTION_FACADE_BUILD_AGGREGATE=OFF','-DABSTRACTION_FACADE_BUILD_RESOLUTION=ON','-DBUILD_SHARED_LIBS=OFF','-DABSTRACTION_IPC_BUILD_TESTS=OFF','-DCMAKE_BUILD_TYPE=Release'])
 run([a.cmake,'--build',b/'sdk','--config','Release'])
 run([a.cmake,'--install',b/'sdk','--config','Release','--prefix',b/'prefix'])
 for name in ('job','asks','rights','logging','router','model','storage'):
  if (b/'prefix/include/abstraction'/name).exists():raise RuntimeError('unrelated package installed: '+name)
 run([a.cmake,'-S',HERE,'-B',b/'consumer','-DCMAKE_PREFIX_PATH='+str(b/'prefix')])
 run([a.cmake,'--build',b/'consumer','--config','Release'])
 certify_compiler(b/'consumer')
 for name in ('abstraction_config','abstraction_ipc'):
  result=subprocess.run([a.cmake,'-S',str(HERE),'-B',str(b/name),'-DCMAKE_PREFIX_PATH='+str(b/'prefix'),'-DCMAKE_DISABLE_FIND_PACKAGE_'+name+'=TRUE'],cwd=ROOT,env=env,capture_output=True,text=True,timeout=120)
  if result.returncode==0 or name not in result.stdout+result.stderr:raise RuntimeError('missing dependency not refused: '+name)
  print('missing dependency refused:',name)
 exe=b/'consumer'/('Release/config_observer_consumer.exe' if os.name=='nt' else 'config_observer_consumer')
 run([exe,'--help']);env['OA_CPP_CONFIG_OBSERVER_PROBE']=str(exe)
 run(['go','test',*(['-race'] if a.race else []),'-count=1','-v',HERE/'fixture_test.go'])
print('PASS config-only installed consumer; temporary prefix removed')
