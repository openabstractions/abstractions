"""Install static IPC and run actual source Cargo packages against isolated Go services.

Runs on Windows (MSVC, named pipes) and Linux (GCC, Unix sockets).
"""
import argparse
import os
from pathlib import Path
import queue
import shutil
import subprocess
import sys
import tempfile
import threading
import uuid
ROOT=Path(__file__).resolve().parents[3]
HERE=Path(__file__).resolve().parent
sys.path.insert(0,str(HERE.parent))
from workspace import (cmake_for, certify_compiler, build_root, CERTIFIES, source_revision, dry_run_stop, DRY_RUN_HELP,
                       fixture_endpoints, host_program)

def main():
 p=argparse.ArgumentParser(description=__doc__+' '+CERTIFIES)
 p.add_argument('--run',action='store_true')
 p.add_argument('--dry-run', action='store_true', help=DRY_RUN_HELP)
 p.add_argument('--cmake',help='CMake executable (Windows: Visual Studio bundled CMake only)')
 a=p.parse_args()
 if not (a.run or a.dry_run):p.print_help();return
 source_revision()
 if a.dry_run: dry_run_stop('rust_services', a)
 windows=os.name=='nt'
 if not windows and not sys.platform.startswith('linux'):raise RuntimeError('this fixture measures Windows/MSVC and Linux only')
 def run(argv,cwd=ROOT,env=None):subprocess.run([str(x) for x in argv],cwd=cwd,env=env,check=True,timeout=300)
 cmake_env=dict(os.environ);cmake=cmake_for(cmake_env,a.cmake)
 with tempfile.TemporaryDirectory(prefix='rsv-',dir=build_root()) as temporary:
  base=Path(temporary);prefix=base/'prefix';tree=base/'source'
  run([cmake,'-S',ROOT/'openabstractions-flat/abstraction-identity/cpp','-B',base/'native','-DBUILD_SHARED_LIBS=OFF','-DABSTRACTION_IPC_BUILD_TESTS=OFF','-DCMAKE_BUILD_TYPE=Release',f'-DCMAKE_INSTALL_PREFIX={prefix}'],ROOT,cmake_env)
  certify_compiler(base/'native')
  run([cmake,'--build',base/'native','--config','Release'],ROOT,cmake_env);run([cmake,'--install',base/'native','--config','Release'],ROOT,cmake_env)
  for component,parts in [('identity',['rust','rust-frame']),('logging',['rust','rs']),('facade',['rust','rust-native','rust-logging','rs'])]:
   for part in parts:
    rel=Path('openabstractions-flat')/('abstraction-'+component)/part
    shutil.copytree(ROOT/rel,tree/rel)
  consumer=tree/'conformance/clients/rust_services';shutil.copytree(HERE,consumer)
  env=dict(os.environ,OA_IPC_PREFIX=str(prefix),CARGO_TARGET_DIR=str(base/'target'))
  run(['cargo','test','--offline','--manifest-path',tree/'openabstractions-flat/abstraction-facade/rust/Cargo.toml'],tree,env)
  run(['cargo','build','--offline','--manifest-path',consumer/'Cargo.toml'],tree,env)
  executable=host_program(base/'target/debug','consumer');host=host_program(base,'host')
  run(['go','build','-o',host,HERE/'host.go'])
  home=base/'empty-home';home.mkdir();child_env=dict(env,HOME=str(home),USERPROFILE=str(home),OA_RUST_HISTORY_POLICY=str(base/'history-policy'))
  run([executable,'--help'],home,child_env)
  with fixture_endpoints(['absent'],'rust-service-'+uuid.uuid4().hex[:12]) as absent:
   run([executable,'absent',absent['absent']],home,child_env)
  for mode in ('runtime','cancel','forged'):
   with fixture_endpoints(['service'],'rust-service-'+uuid.uuid4().hex[:12]) as endpoints:
    endpoint=endpoints['service'];child_env['ABSTRACTION_RUNTIME_ENDPOINT']=endpoint
    peer=subprocess.Popen([str(host),'quiet' if mode=='cancel' else mode,endpoint],cwd=base,env=child_env,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
    lines=queue.Queue();threading.Thread(target=lambda:[lines.put(x.strip()) for x in peer.stdout],daemon=True).start()
    try:
     assert lines.get(timeout=5)=='READY'
     run([executable,mode,endpoint],home,child_env)
    finally:
     if peer.poll() is None:
      try:peer.stdin.write('\n');peer.stdin.flush()
      except OSError:pass
     try:peer.wait(timeout=5)
     except subprocess.TimeoutExpired:peer.kill();peer.wait();raise
     assert peer.returncode==0,peer.stderr.read()
   assert not list(home.iterdir()),'service client created local provider state'
  print('PASS: real installed static Rust service client packages, logging and shared cancellation on '+('Windows' if windows else 'Linux'))
if __name__=='__main__':main()
