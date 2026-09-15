"""Install source Node packages and prove generated calls against production Go services.

Runs on Windows (MSVC, named pipes) and Linux (GCC, Unix sockets).
--run builds the Node-API addon against Node headers, plus node.lib on Windows,
kept in the ignored checkout folder .build/node-sdk/<version>/ (node-gyp layout).
When that folder lacks the running Node version, the bundled node-gyp fetches it
from nodejs.org and verifies it against SHASUMS256.txt; later runs reuse it
offline. Linux links the addon with undefined Node-API symbols that the loading
node process provides.
"""
import argparse, os, shutil, subprocess, sys, tempfile
from pathlib import Path
ROOT=Path(__file__).resolve().parents[3]
HERE=Path(__file__).resolve().parent
sys.path.insert(0,str(HERE.parent))
from workspace import cmake_for, certify_compiler, build_root, source_revision, resolved_executable, dry_run_stop, DRY_RUN_HELP
DEFAULT_SDK=ROOT/'.build/node-sdk'
PURE=('facade','logging','job','storage','config','rights','asks','download','model','router')
NAMES=['asks','config','download-request','facade','job-acceptance','logging','model','rights','router','storage-content']
WINDOWS=os.name=='nt'
def bundled_npm(node):
 """The npm tree shipped with this Node: beside node.exe on Windows, under lib/ in the Linux tarball."""
 return node.parent/'node_modules/npm' if WINDOWS else node.parent.parent/'lib/node_modules/npm'
def node_sdk(node,npm_env,sdk,version):
 """Return (headers, node.lib or None) for this Node version, fetching once through node-gyp."""
 release=sdk/version.lstrip('v');headers=release/'include/node';library=release/'x64/node.lib' if WINDOWS else None
 def complete():return (headers/'node_api.h').is_file() and (library is None or library.is_file())
 if not complete():
  gyp=bundled_npm(node)/'node_modules/node-gyp/bin/node-gyp.js'
  if not gyp.is_file():raise RuntimeError('bundled node-gyp not found: '+str(gyp))
  sdk.mkdir(parents=True,exist_ok=True)
  print('fetching Node',version,'headers'+(' and node.lib' if WINDOWS else ''),'through node-gyp into',sdk,flush=True)
  subprocess.run([str(node),str(gyp),'install','--devdir',str(sdk),'--target',version.lstrip('v'),'--arch','x64','--ensure'],env=npm_env,check=True,timeout=600)
 if not complete():raise RuntimeError('incomplete Node SDK at '+str(release))
 return headers,library
def main():
 p=argparse.ArgumentParser(description=__doc__,formatter_class=argparse.RawDescriptionHelpFormatter)
 p.add_argument('--run',action='store_true',help='build the native addon and run every installed fixture through NativeConnector')
 p.add_argument('--dry-run', action='store_true', help=DRY_RUN_HELP)
 p.add_argument('--pure',action='store_true',help='skip the native addon; check installed metadata and run capability calls through the test-only pipe connector')
 p.add_argument('--cmake',help='CMake executable. Windows: Visual Studio bundled cmake.exe only; --run certifies MSVC from a vcvars64 developer environment and refuses other toolchains before building. Linux: default cmake from PATH')
 p.add_argument('--node',default='node',help='Node.js executable (default: node on PATH; symlinks are resolved and its bundled npm is used)')
 p.add_argument('--node-sdk',type=Path,default=DEFAULT_SDK,help=f'node-gyp devdir holding <version>/include/node, plus <version>/x64/node.lib on Windows (default {DEFAULT_SDK}); fetched once when absent')
 p.add_argument('--race',action='store_true',help='run the Go fixture with -race (needs a configured C compiler)')
 a=p.parse_args()
 if not (a.run or a.pure or a.dry_run):p.print_help();return
 source_revision()
 if a.dry_run: dry_run_stop('js_services', a)
 if a.run and a.pure:p.error('choose --run or --pure')
 if not WINDOWS and not sys.platform.startswith('linux'):raise RuntimeError('this fixture measures Windows/MSVC and Linux only; other platforms remain unmeasured')
 def run(args,env=None,cwd=ROOT,timeout=180):subprocess.run(list(map(str,args)),cwd=cwd,env=env,check=True,timeout=timeout)
 try:node=resolved_executable(a.node)
 except RuntimeError:raise SystemExit(f'Node.js not found: {a.node!r} is not on PATH or a file. Install Node.js 18+ or pass --node <path>; this JavaScript level stays unproven on this host.')
 version=subprocess.check_output([node,'--version'],text=True).strip()
 npm=bundled_npm(node)/'bin/npm-cli.js'
 if not npm.is_file():raise RuntimeError('matching Node npm CLI not found: '+str(npm))
 if a.run:
  cmake_env=dict(os.environ);cmake=cmake_for(cmake_env,a.cmake)
 # npm's cache files can stay locked briefly after npm exits on Windows. The
 # proof has finished by then, so a leftover scratch tree under .build is not a
 # failure of it. MSBuild refuses the Temporary directory, so Windows builds under
 # the checkout's short .build root; Linux keeps the system temporary directory.
 with tempfile.TemporaryDirectory(prefix='js-',dir=build_root() if WINDOWS else None,ignore_cleanup_errors=True) as tmp:
  b=Path(tmp);sources=[ROOT/f'openabstractions-flat/abstraction-{name}/javascript' for name in PURE]
  npm_env=dict(os.environ,NPM_CONFIG_CACHE=str(b/'npm-cache'),NPM_CONFIG_USERCONFIG=str(b/'empty-npmrc'),NPM_CONFIG_GLOBALCONFIG=str(b/'empty-global-npmrc'))
  if a.run:
   headers,library=node_sdk(node,npm_env,a.node_sdk.resolve(),version)
   ipc=ROOT/'openabstractions-flat/abstraction-identity/cpp';native=ROOT/'openabstractions-flat/abstraction-identity/javascript'
   # Single-config generators on Linux take the build type at configure time.
   release=[] if WINDOWS else ['-DCMAKE_BUILD_TYPE=Release']
   run([cmake,'-S',ipc,'-B',b/'ipc','-DBUILD_SHARED_LIBS=OFF','-DABSTRACTION_IPC_BUILD_TESTS=OFF','-DCMAKE_INSTALL_PREFIX='+str(b/'prefix'),*release],cmake_env);certify_compiler(b/'ipc')
   run([cmake,'--build',b/'ipc','--config','Release'],cmake_env);run([cmake,'--install',b/'ipc','--config','Release'],cmake_env)
   run([cmake,'-S',native,'-B',b/'addon','-DCMAKE_PREFIX_PATH='+str(b/'prefix'),'-DNODE_INCLUDE_DIR='+str(headers),*(['-DNODE_IMPORT_LIBRARY='+str(library)] if library else release)],cmake_env);certify_compiler(b/'addon')
   run([cmake,'--build',b/'addon','--config','Release'],cmake_env);run([cmake,'--install',b/'addon','--config','Release','--prefix',b/'native-package'],cmake_env)
   sources.insert(0,b/'native-package')
  packages=b/'outside/node_modules/@openabstractions'
  (b/'packs').mkdir();(b/'outside').mkdir()
  for source in sources:
   run([node,npm,'pack','--offline','--ignore-scripts','--pack-destination',b/'packs',source],npm_env)
  tarballs=sorted((b/'packs').glob('*.tgz'))
  if len(tarballs)!=len(sources):raise RuntimeError(f'{len(sources)} actual package tarballs required')
  run([node,npm,'install','--offline','--ignore-scripts','--no-audit','--no-fund','--no-package-lock','--prefix',b/'outside',*tarballs],npm_env,cwd=b/'outside')
  assert sorted(p.name for p in packages.iterdir())==sorted(NAMES+(['ipc'] if a.run else []))
  for file in ('consumer.mjs','pure.mjs','services.mjs','pipe_connector.mjs','packages.mjs'):shutil.copy2(HERE/file,b/'outside'/file)
  env=dict(os.environ);env.pop('ABSTRACTION_IPC_NODE',None);env.pop('NODE_PATH',None)
  run([node,b/'outside/packages.mjs'],env,cwd=b/'outside')
  # Prove the pure package does not load the available native addon.
  env['ABSTRACTION_IPC_NODE']=str(b/'must-not-load.node')
  run([node,b/'outside/pure.mjs'],env,cwd=b/'outside')
  env.pop('ABSTRACTION_IPC_NODE');env.update(OA_JS_NODE=str(node),OA_JS_SERVICES=str(b/'outside/services.mjs'))
  if a.run:
   env.update(OA_JS_CONSUMER=str(b/'outside/consumer.mjs'),UV_THREADPOOL_SIZE='1',OA_JS_CONNECTOR='native')
   run(['go','test',*(['-race'] if a.race else []),'-count=1','-v',HERE/'fixture_test.go'],env,timeout=900)
  else:
   env['OA_JS_CONNECTOR']='pipe'
   run(['go','test',*(['-race'] if a.race else []),'-count=1','-v','-run','TestInstalledJavaScriptCapabilities',HERE/'fixture_test.go'],env,timeout=900)
 print('PASS installed source packages and generated calls against production Go services'+(' through native IPC' if a.run else ' through the test pipe connector')+'; temporary packages removed')
if __name__=='__main__':main()
