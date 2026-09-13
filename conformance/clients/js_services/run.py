"""Install source Node packages and prove generated calls over the shared C ABI."""
import argparse, hashlib, json, os, shutil, subprocess, sys, tarfile, tempfile, urllib.request
from pathlib import Path
ROOT=Path(__file__).resolve().parents[3]
HERE=Path(__file__).resolve().parent
def main():
 p=argparse.ArgumentParser(description=__doc__)
 p.add_argument('--run',action='store_true');p.add_argument('--cmake',default='cmake');p.add_argument('--node',default='node');p.add_argument('--node-sdk',type=Path,help='Previously checksum-verified Node headers/node.lib directory');p.add_argument('--race',action='store_true')
 a=p.parse_args()
 if not a.run:p.print_help();return
 if os.name!='nt':raise RuntimeError('this native source fixture is currently Windows/MSVC only')
 def run(args,env=None,cwd=ROOT):subprocess.run(list(map(str,args)),cwd=cwd,env=env,check=True,timeout=180)
 node=Path(shutil.which(a.node) or a.node).resolve();version=subprocess.check_output([node,'--version'],text=True).strip()
 with tempfile.TemporaryDirectory(prefix='oa-js-services-') as tmp:
  b=Path(tmp);sdk=a.node_sdk.resolve() if a.node_sdk else b/'node-sdk';sdk.mkdir(exist_ok=True)
  if not a.node_sdk:
   url=f'https://nodejs.org/download/release/{version}/';manifest=urllib.request.urlopen(url+'SHASUMS256.txt',timeout=30).read().decode()
   for name in [f'node-{version}-headers.tar.gz','win-x64/node.lib']:
    data=urllib.request.urlopen(url+name,timeout=60).read();digest=hashlib.sha256(data).hexdigest()
    expected=next(line.split()[0] for line in manifest.splitlines() if line.split()[-1]==name)
    if digest!=expected:raise RuntimeError('Node artifact checksum mismatch: '+name)
    path=sdk/Path(name).name;path.write_bytes(data);print(name,digest,flush=True)
    if name.endswith('.gz'):
     with tarfile.open(path) as archive:archive.extractall(sdk,filter='data')
  headers=sdk/f'node-{version}/include/node'
  ipc=ROOT/'openabstractions-flat/abstraction-identity/cpp';native=ROOT/'openabstractions-flat/abstraction-identity/javascript'
  run([a.cmake,'-S',ipc,'-B',b/'ipc','-DBUILD_SHARED_LIBS=OFF','-DABSTRACTION_IPC_BUILD_TESTS=OFF','-DCMAKE_INSTALL_PREFIX='+str(b/'prefix')]);run([a.cmake,'--build',b/'ipc','--config','Release']);run([a.cmake,'--install',b/'ipc','--config','Release'])
  packages=b/'outside/node_modules/@openabstractions'
  run([a.cmake,'-S',native,'-B',b/'addon','-DCMAKE_PREFIX_PATH='+str(b/'prefix'),'-DNODE_INCLUDE_DIR='+str(headers),'-DNODE_IMPORT_LIBRARY='+str(sdk/'node.lib')]);run([a.cmake,'--build',b/'addon','--config','Release']);run([a.cmake,'--install',b/'addon','--config','Release','--prefix',b/'native-package'])
  npm=node.parent/'node_modules/npm/bin/npm-cli.js'
  if not npm.is_file():raise RuntimeError('matching Node npm CLI not found: '+str(npm))
  npm_env=dict(os.environ,NPM_CONFIG_CACHE=str(b/'npm-cache'),NPM_CONFIG_USERCONFIG=str(b/'empty-npmrc'),NPM_CONFIG_GLOBALCONFIG=str(b/'empty-global-npmrc'))
  (b/'packs').mkdir();(b/'outside').mkdir()
  for source in [b/'native-package',ROOT/'openabstractions-flat/abstraction-facade/javascript',ROOT/'openabstractions-flat/abstraction-logging/javascript', *[ROOT/f'openabstractions-flat/abstraction-{name}/javascript' for name in ('job','storage','config','rights','asks','download')]]:
   run([node,npm,'pack','--offline','--ignore-scripts','--pack-destination',b/'packs',source],npm_env)
  tarballs=sorted((b/'packs').glob('*.tgz'))
  if len(tarballs)!=9:raise RuntimeError('nine actual package tarballs required')
  run([node,npm,'install','--offline','--ignore-scripts','--no-audit','--no-fund','--no-package-lock','--prefix',b/'outside',*tarballs],npm_env,cwd=b/'outside')
  assert sorted(p.name for p in packages.iterdir())==['asks','config','download-request','facade','ipc','job-acceptance','logging','rights','storage-content']
  for file in ('consumer.mjs','pure.mjs'):shutil.copy2(HERE/file,b/'outside'/file)
  env=dict(os.environ);env.pop('ABSTRACTION_IPC_NODE',None);env.pop('NODE_PATH',None)
  # Prove the pure package does not load the available native addon.
  env['ABSTRACTION_IPC_NODE']=str(b/'must-not-load.node')
  run([node,b/'outside/pure.mjs'],env,cwd=b/'outside')
  env.pop('ABSTRACTION_IPC_NODE');env.update(OA_JS_NODE=str(node),OA_JS_CONSUMER=str(b/'outside/consumer.mjs'),UV_THREADPOOL_SIZE='1')
  run(['go','test',*(['-race'] if a.race else []),'-count=1','-v',HERE/'fixture_test.go'],env)
 print('PASS installed source packages, native asynchronous IPC, real logging/job/storage; temporary packages/toolchain removed')
if __name__=='__main__':main()
