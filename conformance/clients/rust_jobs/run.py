"""Install static IPC and run actual source Cargo packages against isolated Go services.

Runs on Windows (MSVC, named pipes) and Linux (GCC, Unix sockets).
"""
import argparse
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
ROOT=Path(__file__).resolve().parents[3]
HERE=Path(__file__).resolve().parent
sys.path.insert(0,str(HERE.parent))
from workspace import cmake_for, certify_compiler, build_root, CERTIFIES, source_revision, dry_run_stop, DRY_RUN_HELP, host_program

def main():
 p=argparse.ArgumentParser(description=__doc__+' '+CERTIFIES)
 p.add_argument('--run',action='store_true')
 p.add_argument('--dry-run', action='store_true', help=DRY_RUN_HELP)
 p.add_argument('--cmake',help='CMake executable (Windows: Visual Studio bundled CMake only)')
 a=p.parse_args()
 if not (a.run or a.dry_run):p.print_help();return
 source_revision()
 if a.dry_run: dry_run_stop('rust_jobs', a)
 windows=os.name=='nt'
 if not windows and not sys.platform.startswith('linux'):raise RuntimeError('this fixture measures Windows/MSVC and Linux only')
 def run(argv,cwd=ROOT,env=None):subprocess.run([str(x) for x in argv],cwd=cwd,env=env,check=True,timeout=300)
 cmake_env=dict(os.environ);cmake=cmake_for(cmake_env,a.cmake)
 with tempfile.TemporaryDirectory(prefix='rj-',dir=build_root()) as temporary:
  base=Path(temporary);prefix=base/'prefix';tree=base/'source'
  pure=base/'pure-source'
  for component,parts in [('identity',['rust-frame']),('facade',['rust','rust-jobs','rs']),('job',['rust','rs'])]:
   for part in parts:
    rel=Path('openabstractions-flat')/('abstraction-'+component)/part
    shutil.copytree(ROOT/rel,pure/rel)
  pure_env=dict(os.environ,CARGO_TARGET_DIR=str(base/'pure-target'))
  pure_env.pop('OA_IPC_PREFIX',None)
  for crate in ('rust','rust-jobs'):
   run(['cargo','test','--offline','--manifest-path',pure/'openabstractions-flat/abstraction-facade'/crate/'Cargo.toml'],pure,pure_env)
  assert not (pure/'openabstractions-flat/abstraction-identity/rust').exists()
  assert not (pure/'openabstractions-flat/abstraction-logging').exists()
  run([cmake,'-S',ROOT/'openabstractions-flat/abstraction-identity/cpp','-B',base/'native','-DBUILD_SHARED_LIBS=OFF','-DABSTRACTION_IPC_BUILD_TESTS=OFF','-DCMAKE_BUILD_TYPE=Release',f'-DCMAKE_INSTALL_PREFIX={prefix}'],ROOT,cmake_env)
  certify_compiler(base/'native')
  run([cmake,'--build',base/'native','--config','Release'],ROOT,cmake_env);run([cmake,'--install',base/'native','--config','Release'],ROOT,cmake_env)
  for component,parts in [('identity',['rust','rust-frame']),('facade',['rust','rust-native','rust-jobs','rs']),('job',['rust','rs'])]:
   for part in parts:
    rel=Path('openabstractions-flat')/('abstraction-'+component)/part
    shutil.copytree(ROOT/rel,tree/rel)
  consumer=tree/'conformance/clients/rust_jobs';shutil.copytree(HERE,consumer)
  env=dict(os.environ,OA_IPC_PREFIX=str(prefix),CARGO_TARGET_DIR=str(base/'target'))
  run(['cargo','test','--offline','--manifest-path',tree/'openabstractions-flat/abstraction-facade/rust-jobs/Cargo.toml'],tree,env)
  run(['cargo','build','--offline','--manifest-path',consumer/'Cargo.toml'],tree,env)
  executable=host_program(base/'target/debug','rust-jobs-consumer')
  env['OA_RUST_JOBS']=str(executable)
  run(['go','test','-count=1','-v',HERE/'fixture_test.go'],ROOT,env)
  print('PASS: outside current Cargo packages and installed native IPC against actual Go HTTP runtime on '+('Windows' if windows else 'Linux'))
if __name__=='__main__':main()
