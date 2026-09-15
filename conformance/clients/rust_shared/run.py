"""Build an outside Rust consumer against an isolated installed static IPC prefix.

Runs on Windows (MSVC, named pipes) and Linux (GCC, Unix sockets). Linux runs the
crate's native verified-server test and requires it to pass. Both platforms check
that Cargo's link lines equal the installed native link-dependencies.txt.
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
CHECK=ROOT/'openabstractions-flat/abstraction-identity/cpp/check_link_dependencies.py'
sys.path.insert(0,str(HERE.parent))
from workspace import cmake_for, certify_compiler, build_root, CERTIFIES, source_revision, dry_run_stop, DRY_RUN_HELP

def main():
 p=argparse.ArgumentParser(description=__doc__+'\n'+CERTIFIES,formatter_class=argparse.RawDescriptionHelpFormatter)
 p.add_argument('--run',action='store_true',help='build, test and exercise the consumer on this host (Windows or Linux)')
 p.add_argument('--dry-run', action='store_true', help=DRY_RUN_HELP)
 p.add_argument('--cmake',help='CMake executable (Windows: Visual Studio bundled CMake only)')
 a=p.parse_args()
 if not (a.run or a.dry_run):p.print_help();return
 source_revision()
 if a.dry_run: dry_run_stop('rust_shared', a)
 windows=os.name=='nt'
 if not windows and not sys.platform.startswith('linux'):raise RuntimeError('this fixture measures Windows/MSVC and Linux only; other platforms remain unmeasured')
 def run(argv,cwd=ROOT,env=None):subprocess.run([str(x) for x in argv],cwd=cwd,env=env,check=True,timeout=300)
 def capture(argv,cwd,env):
  result=subprocess.run([str(x) for x in argv],cwd=cwd,env=env,capture_output=True,text=True,timeout=300)
  sys.stdout.write(result.stdout);sys.stderr.write(result.stderr)
  if result.returncode:raise subprocess.CalledProcessError(result.returncode,argv)
  return result.stdout+result.stderr
 cmake_env=dict(os.environ);cmake=cmake_for(cmake_env,a.cmake)
 # Windows builds under the checkout's short .build root: MSBuild refuses the
 # Temporary directory. Linux keeps the system temporary directory, whose short
 # path keeps Unix socket names under their length limit.
 with tempfile.TemporaryDirectory(prefix='rsh-',dir=build_root() if windows else None) as temporary:
  base=Path(temporary);prefix=base/'prefix'
  run([cmake,'-S',ROOT/'openabstractions-flat/abstraction-identity/cpp','-B',base/'native','-DBUILD_SHARED_LIBS=OFF','-DABSTRACTION_IPC_BUILD_TESTS=OFF','-DCMAKE_BUILD_TYPE=Release',f'-DCMAKE_INSTALL_PREFIX={prefix}'],ROOT,cmake_env)
  certify_compiler(base/'native')
  run([cmake,'--build',base/'native','--config','Release'],ROOT,cmake_env);run([cmake,'--install',base/'native','--config','Release'],ROOT,cmake_env)
  run([sys.executable,CHECK,'--self-test','--prefix',prefix])
  crate=base/'ipc';shutil.copytree(ROOT/'openabstractions-flat/abstraction-identity/rust',crate)
  # abstraction-ipc depends on the pure frame contract through ../rust-frame.
  shutil.copytree(ROOT/'openabstractions-flat/abstraction-identity/rust-frame',base/'rust-frame')
  env=dict(os.environ,OA_IPC_PREFIX=str(prefix),CARGO_TARGET_DIR=str(base/'target'))
  bad_env=dict(env,OA_IPC_PREFIX=str(base/'missing-prefix'))
  refused=subprocess.run(['cargo','check','--offline','--manifest-path',str(crate/'Cargo.toml')],cwd=base,env=bad_env,capture_output=True,text=True,timeout=300)
  assert refused.returncode!=0 and 'install the static abstraction_ipc library' in refused.stderr,refused.stderr
  tests=capture(['cargo','test','--offline','--manifest-path',crate/'Cargo.toml','--','--nocapture'],base,env)
  if not windows:
   assert 'test tests::native_verified_server_refuses_before_payload ... ok' in tests,'Linux native verified-server test did not pass'
   print('PASS Linux native verified-server test ran natively',flush=True)
  consumer=base/'consumer';consumer.mkdir();shutil.copyfile(HERE/'main.rs',consumer/'main.rs')
  (consumer/'Cargo.toml').write_text('[package]\nname="outside-ipc-consumer"\nversion="0.0.0"\nedition="2021"\n[dependencies]\nabstraction-ipc={path="../ipc"}\n[[bin]]\nname="consumer"\npath="main.rs"\n',encoding='utf-8')
  cargo_log=base/'cargo-build.log'
  cargo_log.write_text(capture(['cargo','build','-vv','--offline','--manifest-path',consumer/'Cargo.toml'],base,env),encoding='utf-8')
  run([sys.executable,CHECK,'--prefix',prefix,'--cargo-output',cargo_log])
  suffix='.exe' if windows else ''
  executable=base/('target/debug/consumer'+suffix);host=base/('host'+suffix)
  run(['go','build','-o',host,HERE/'host.go'])
  home=base/'empty-home';home.mkdir()
  child_env=dict(env,HOME=str(home),USERPROFILE=str(home),ABSTRACTION_RUNTIME_ENDPOINT='')
  expected=subprocess.check_output([str(host),'bootstrap'],env=child_env,text=True,timeout=5).strip()
  run([executable,'bootstrap',expected],home,child_env)
  override=dict(child_env,ABSTRACTION_RUNTIME_ENDPOINT='explicit-rust-bootstrap')
  run([executable,'bootstrap','explicit-rust-bootstrap'],home,override)
  sockets=base/'sockets';sockets.mkdir()
  endpoint=(lambda:r'\\.\pipe\oa-rust-'+uuid.uuid4().hex) if windows else (lambda:str(sockets/(uuid.uuid4().hex[:16]+'.sock')))
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
  print(f'PASS: installed static Rust ABI consumer on {"Windows" if windows else "Linux"}, link list agreement, bootstrap, bytes, framing faults, cancellation and timeout; transport-only')
if __name__=='__main__':main()
