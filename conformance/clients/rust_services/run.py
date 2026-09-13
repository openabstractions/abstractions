"""Install static IPC and run actual source Cargo packages against isolated Go services."""
import argparse
import os
from pathlib import Path
import queue
import shutil
import subprocess
import tempfile
import threading
import uuid
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
 with tempfile.TemporaryDirectory(prefix='oa-rust-services-') as temporary:
  base=Path(temporary);prefix=base/'prefix';tree=base/'source'
  run([a.cmake,'-S',ROOT/'openabstractions-flat/abstraction-identity/cpp','-B',base/'native','-DBUILD_SHARED_LIBS=OFF','-DABSTRACTION_IPC_BUILD_TESTS=OFF',f'-DCMAKE_INSTALL_PREFIX={prefix}'])
  run([a.cmake,'--build',base/'native','--config','Release']);run([a.cmake,'--install',base/'native','--config','Release'])
  for component,parts in [('identity',['rust','rust-frame']),('logging',['rust','rs']),('facade',['rust','rust-native','rust-logging','rs'])]:
   for part in parts:
    rel=Path('openabstractions-flat')/('abstraction-'+component)/part
    shutil.copytree(ROOT/rel,tree/rel)
  consumer=tree/'conformance/clients/rust_services';shutil.copytree(HERE,consumer)
  env=dict(os.environ,OA_IPC_PREFIX=str(prefix),CARGO_TARGET_DIR=str(base/'target'))
  run(['cargo','test','--offline','--manifest-path',tree/'openabstractions-flat/abstraction-facade/rust/Cargo.toml'],tree,env)
  run(['cargo','build','--offline','--manifest-path',consumer/'Cargo.toml'],tree,env)
  executable=base/'target/debug/consumer.exe';host=base/'host.exe'
  run(['go','build','-o',host,HERE/'host.go'])
  home=base/'empty-home';home.mkdir();child_env=dict(env,HOME=str(home),USERPROFILE=str(home))
  address=lambda:r'\\.\pipe\oa-rust-service-'+uuid.uuid4().hex
  run([executable,'--help'],home,child_env)
  run([executable,'absent',address()],home,child_env)
  for mode in ('runtime','cancel','forged'):
   endpoint=address();child_env['ABSTRACTION_RUNTIME_ENDPOINT']=endpoint
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
  print('PASS: real installed static Rust service client packages, logging and shared cancellation')
if __name__=='__main__':main()
