"""Build an outside Rust consumer against an isolated installed static IPC prefix."""
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
 if os.name!='nt':raise RuntimeError('this first execution fixture is Windows/MSVC only; other platforms remain unmeasured')
 def run(argv,cwd=ROOT,env=None):subprocess.run([str(x) for x in argv],cwd=cwd,env=env,check=True,timeout=120)
 with tempfile.TemporaryDirectory(prefix='oa-rust-') as temporary:
  base=Path(temporary);prefix=base/'prefix'
  run([a.cmake,'-S',ROOT/'openabstractions-flat/abstraction-identity/cpp','-B',base/'native','-DBUILD_SHARED_LIBS=OFF','-DABSTRACTION_IPC_BUILD_TESTS=OFF',f'-DCMAKE_INSTALL_PREFIX={prefix}'])
  run([a.cmake,'--build',base/'native','--config','Release']);run([a.cmake,'--install',base/'native','--config','Release'])
  crate=base/'ipc';shutil.copytree(ROOT/'openabstractions-flat/abstraction-identity/rust',crate)
  env=dict(os.environ,OA_IPC_PREFIX=str(prefix),CARGO_TARGET_DIR=str(base/'target'))
  bad_env=dict(env,OA_IPC_PREFIX=str(base/'missing-prefix'))
  refused=subprocess.run(['cargo','check','--offline','--manifest-path',str(crate/'Cargo.toml')],cwd=base,env=bad_env,capture_output=True,text=True,timeout=120)
  assert refused.returncode!=0 and 'install the static abstraction_ipc library' in refused.stderr,refused.stderr
  run(['cargo','test','--offline','--manifest-path',crate/'Cargo.toml'],base,env)
  consumer=base/'consumer';consumer.mkdir();shutil.copyfile(HERE/'main.rs',consumer/'main.rs')
  (consumer/'Cargo.toml').write_text('[package]\nname="outside-ipc-consumer"\nversion="0.0.0"\nedition="2021"\n[dependencies]\nabstraction-ipc={path="../ipc"}\n[[bin]]\nname="consumer"\npath="main.rs"\n',encoding='utf-8')
  run(['cargo','build','--offline','--manifest-path',consumer/'Cargo.toml'],base,env)
  executable=base/'target/debug/consumer.exe';host=base/'host.exe'
  run(['go','build','-o',host,HERE/'host.go'])
  home=base/'empty-home';home.mkdir()
  child_env=dict(env,HOME=str(home),USERPROFILE=str(home),ABSTRACTION_RUNTIME_ENDPOINT='')
  expected=subprocess.check_output([str(host),'bootstrap'],env=child_env,text=True,timeout=5).strip()
  run([executable,'bootstrap',expected],home,child_env)
  override=dict(child_env,ABSTRACTION_RUNTIME_ENDPOINT='explicit-rust-bootstrap')
  run([executable,'bootstrap','explicit-rust-bootstrap'],home,override)
  endpoint=lambda:r'\\.\pipe\oa-rust-'+uuid.uuid4().hex
  run([executable,'absent',endpoint()],home,child_env)
  for mode in ('echo','oversized','header','body','oneway','cancel','timeout'):
   address=endpoint();peer_mode='quiet' if mode in ('cancel','timeout') else mode
   peer=subprocess.Popen([str(host),peer_mode,address],cwd=base,env=child_env,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
   lines=queue.Queue();threading.Thread(target=lambda:[lines.put(x.strip()) for x in peer.stdout],daemon=True).start()
   try:
    assert lines.get(timeout=5)=='READY'
    run([executable,mode,address],home,child_env)
   finally:
    if peer.poll() is None:
     try:peer.stdin.write('x');peer.stdin.flush()
     except OSError:pass # peer may have exited between poll and pipe flush
    try:peer.wait(timeout=5)
    except subprocess.TimeoutExpired:peer.kill();peer.wait();raise
    assert peer.returncode==0,peer.stderr.read()
   assert not list(home.iterdir()),'client created local provider state'
  print('PASS: installed static Rust ABI consumer, bootstrap, bytes, framing faults, cancellation and timeout; transport-only')
if __name__=='__main__':main()
