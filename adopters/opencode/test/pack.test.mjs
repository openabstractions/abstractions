// What `npm pack` puts in @openabstractions/opencode is enough to run it from
// npm (`opencode plugin @openabstractions/opencode`): every module a packed
// module imports is packed or declared, each export is packed, and the IPC
// client it needs on Windows brings its native platform package.
import assert from 'node:assert/strict';
import {spawnSync} from 'node:child_process';
import {readFileSync} from 'node:fs';
import {builtinModules} from 'node:module';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import test from 'node:test';

import {shrinkwrap} from '../shrinkwrap.js';

const adopter = fileURLToPath(new URL('..', import.meta.url));
const root = path.resolve(adopter, '..', '..');
const read = (file) => JSON.parse(readFileSync(file, 'utf8'));
const manifest = read(path.join(adopter, 'package.json'));
// Modules OpenCode itself provides to the plugins it loads.
const hostProvided = new Set(['@opencode-ai/plugin', '@opentui/core', '@opentui/solid', 'solid-js']);

function packedFiles() {
  const result = spawnSync('npm', ['pack', '--dry-run', '--json', '--ignore-scripts'], {
    cwd: adopter, encoding: 'utf8', shell: process.platform === 'win32',
  });
  assert.equal(result.status, 0, result.stderr);
  return new Set(JSON.parse(result.stdout)[0].files.map((file) => file.path.replaceAll('\\', '/')));
}

const npmAvailable = spawnSync('npm', ['--version'], {shell: process.platform === 'win32'}).status === 0;

test('every packed module finds what it imports in the package or its declared dependencies', {skip: !npmAvailable && 'npm is required'}, () => {
  const files = packedFiles();
  for (const required of ['package.json', 'README.md', 'index.js', 'plugin.js', 'tui.js', 'official-install.js', 'opencode-build.js']) {
    assert.ok(files.has(required), `the package carries ${required}`);
  }
  for (const [name, target] of Object.entries(manifest.exports)) {
    assert.ok(files.has(target.default.replace(/^\.\//, '')), `export ${name} is packed`);
  }
  assert.ok(![...files].some((file) => file.startsWith('test/') || file.endsWith('.test.js')), 'no tests are packed');
  const declared = new Set([...Object.keys(manifest.dependencies ?? {}), ...Object.keys(manifest.peerDependencies ?? {})]);
  const builtins = new Set(builtinModules);
  const pattern = /(?:^|[\s;])(?:import|export)\s[^'"]*?from\s*['"]([^'"]+)['"]|import\(\s*['"]([^'"]+)['"]\s*\)/gm;
  for (const file of [...files].filter((entry) => /\.m?js$/.test(entry))) {
    const source = readFileSync(path.join(adopter, file), 'utf8');
    for (const match of source.matchAll(pattern)) {
      const specifier = match[1] ?? match[2];
      if (specifier.startsWith('.')) {
        const target = path.posix.normalize(path.posix.join(path.posix.dirname(file), specifier));
        assert.ok(files.has(target), `${file} imports ${specifier}, which is not packed`);
        continue;
      }
      if (specifier.startsWith('node:') || specifier.startsWith('bun:') || builtins.has(specifier)) continue;
      const name = specifier.startsWith('@') ? specifier.split('/').slice(0, 2).join('/') : specifier.split('/')[0];
      assert.ok(declared.has(name) || hostProvided.has(name), `${file} imports ${specifier}, which package.json does not declare`);
    }
  }
});

test('on Windows the IPC client brings its native platform package, which the shrinkwrap leaves to npm', () => {
  const ipc = read(path.join(root, 'openabstractions-flat', 'abstraction-identity', 'javascript', 'package.json'));
  assert.equal(manifest.dependencies['@openabstractions/ipc'], ipc.version);
  assert.equal(ipc.optionalDependencies['@openabstractions/ipc-win32-x64'], ipc.version);
  const platforms = readFileSync(path.join(root, 'openabstractions-flat', 'abstraction-identity', 'javascript', 'platform.js'), 'utf8');
  assert.match(platforms, /'win32-x64': 'abstraction_ipc\.dll'/);
  // The packed shrinkwrap pins only third-party packages, so npm resolves the
  // OA packages and @openabstractions/ipc's optional platform package for the
  // installing machine. A pinned OA entry would freeze one platform's tree.
  const wrap = shrinkwrap(manifest, read(path.join(adopter, 'package-lock.json')));
  const pinnedOwn = Object.keys(wrap.packages).filter((key) => key.includes('node_modules/@openabstractions/'));
  assert.deepEqual(pinnedOwn, []);
});
