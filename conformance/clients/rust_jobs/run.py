"""Install static IPC and run actual source Cargo packages against isolated Go services."""
import argparse
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
ROOT=Path(__file__).resolve().parents[3]
HERE=Path(__file__).resolve().parent

def main():
 p=argparse.ArgumentParser(description=__doc__)
 p.add_argument('--run',action='store_true')
 p.add_argument('--cmake',default='cmake')
 a=p.parse_args()
 if not a.run:p.print_help();return
 if os.name!='nt':raise RuntimeError('this fixture currently measures Windows/MSVC only')
 def run(argv,cwd=ROOT,env=None):subprocess.run([str(x) for x in argv],cwd=cwd,env=env,check=True,timeout=120)
 with tempfile.TemporaryDirectory(prefix='oa-rust-jobs-') as temporary:
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
  run([a.cmake,'-S',ROOT/'openabstractions-flat/abstraction-identity/cpp','-B',base/'native','-DBUILD_SHARED_LIBS=OFF','-DABSTRACTION_IPC_BUILD_TESTS=OFF',f'-DCMAKE_INSTALL_PREFIX={prefix}'])
  run([a.cmake,'--build',base/'native','--config','Release']);run([a.cmake,'--install',base/'native','--config','Release'])
  for component,parts in [('identity',['rust','rust-frame']),('facade',['rust','rust-native','rust-jobs','rs']),('job',['rust','rs'])]:
   for part in parts:
    rel=Path('openabstractions-flat')/('abstraction-'+component)/part
    shutil.copytree(ROOT/rel,tree/rel)
  consumer=tree/'conformance/clients/rust_jobs';shutil.copytree(HERE,consumer)
  env=dict(os.environ,OA_IPC_PREFIX=str(prefix),CARGO_TARGET_DIR=str(base/'target'))
  run(['cargo','test','--offline','--manifest-path',tree/'openabstractions-flat/abstraction-facade/rust-jobs/Cargo.toml'],tree,env)
  run(['cargo','build','--offline','--manifest-path',consumer/'Cargo.toml'],tree,env)
  executable=base/'target/debug/rust-jobs-consumer.exe'
  env['OA_RUST_JOBS']=str(executable)
  run(['go','test','-count=1','-v',HERE/'fixture_test.go'],ROOT,env)
  print('PASS: outside current Cargo packages and installed native IPC against actual Go HTTP runtime')
if __name__=='__main__':main()
