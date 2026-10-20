import assert from 'node:assert/strict';
import test from 'node:test';
import {mkdirSync, mkdtempSync, rmSync, unlinkSync, writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {findConfig, findPlugin, findProgram, officialInstalls, sandboxArguments} from '../official-install.js';

function fixture(t) {
  const root = mkdtempSync(path.join(tmpdir(), 'oa-opencode-install-'));
  t.after(() => rmSync(root, {recursive: true, force: true}));
  const home = path.join(root, 'home');
  const env = {USERPROFILE: home, APPDATA: path.join(home, 'AppData', 'Roaming')};
  const place = (...parts) => {
    const file = path.join(...parts);
    mkdirSync(path.dirname(file), {recursive: true});
    writeFileSync(file, 'fixture');
    return file;
  };
  return {root, home, env, place};
}

test('the official CLI is found at each official installer location, the install script first', (t) => {
  const cases = [
    ['install script', (f) => [f.home, '.opencode', 'bin', 'opencode.exe']],
    ['npm', (f) => [f.env.APPDATA, 'npm', 'node_modules', '@opencode', 'cli', 'bin', 'opencode.exe']],
    ['npm', (f) => [f.env.APPDATA, 'npm', 'node_modules', 'opencode-ai', 'node_modules', 'opencode-windows-x64', 'bin', 'opencode.exe']],
    ['npm', (f) => [f.env.APPDATA, 'npm', 'node_modules', 'opencode-windows-x64-baseline', 'bin', 'opencode.exe']],
    ['bun', (f) => [f.home, '.bun', 'install', 'global', 'node_modules', 'opencode-windows-x64', 'bin', 'opencode.exe']],
    ['scoop', (f) => [f.home, 'scoop', 'apps', 'opencode', 'current', 'opencode.exe']],
  ];
  for (const [method, where] of cases) {
    const f = fixture(t);
    const program = f.place(...where(f));
    assert.deepEqual(findProgram({env: f.env, path}), {method, program});
  }
  const f = fixture(t);
  f.env.OPENCODE_INSTALL_DIR = path.join(f.root, 'custom');
  f.env.ChocolateyInstall = path.join(f.root, 'choco');
  const custom = f.place(f.root, 'custom', 'opencode.exe');
  const choco = f.place(f.root, 'choco', 'lib', 'opencode', 'tools', 'opencode.exe');
  assert.deepEqual(findProgram({env: f.env, path}), {method: 'install script', program: custom});
  unlinkSync(custom);
  assert.deepEqual(findProgram({env: f.env, path}), {method: 'chocolatey', program: choco});
  assert.equal(officialInstalls({}, path).length, 0, 'no home and no variables: nothing to look at');
});

test('a named program wins; a missing install says how to get one', (t) => {
  const f = fixture(t);
  assert.throws(() => findProgram({env: f.env, path}), /OpenCode's own installer, or name opencode.exe with --program/);
  const unzipped = f.place(f.root, 'Downloads', 'opencode-windows-x64', 'opencode.exe');
  f.place(f.home, '.opencode', 'bin', 'opencode.exe');
  assert.deepEqual(findProgram({given: unzipped, env: f.env, path}), {method: 'given', program: unzipped});

  mkdirSync(path.join(f.root, 'folder.exe'));
  for (const bad of ['opencode.exe', path.join(f.root, 'missing.exe'), path.join(f.root, 'folder.exe')]) {
    assert.throws(() => findProgram({given: bad, env: f.env, path}), /--program/, bad);
  }
});

test('the plugin is found in OpenCode\'s package cache; several versions need --plugin', (t) => {
  const f = fixture(t);
  assert.throws(() => findPlugin({env: f.env, path}), /opencode plugin @openabstractions\/opencode/);
  const cache = path.join(f.home, '.cache', 'opencode', 'packages', '@openabstractions');
  const plain = path.dirname(f.place(cache, 'opencode', 'node_modules', '@openabstractions', 'opencode', 'package.json'));
  f.place(cache, 'other', 'node_modules', '@openabstractions', 'opencode', 'package.json');
  assert.equal(findPlugin({env: f.env, path}), plain);
  const versioned = path.dirname(f.place(cache, 'opencode@0.4.0', 'node_modules', '@openabstractions', 'opencode', 'package.json'));
  assert.throws(() => findPlugin({env: f.env, path}), /2 installed versions.*--plugin/);
  assert.equal(findPlugin({given: versioned, env: f.env, path}), versioned);
  assert.throws(() => findPlugin({given: f.root, env: f.env, path}), /--plugin/);
  f.env.XDG_CACHE_HOME = path.join(f.root, 'xdg');
  const moved = path.dirname(f.place(f.root, 'xdg', 'opencode', 'packages', '@openabstractions', 'opencode', 'node_modules', '@openabstractions', 'opencode', 'package.json'));
  assert.equal(findPlugin({env: f.env, path}), moved);
});

test('OpenCode\'s global config and the full sandboxed start', (t) => {
  const f = fixture(t);
  assert.throws(() => findConfig({env: f.env, path}), /configure step/);
  const jsonc = f.place(f.home, '.config', 'opencode', 'opencode.jsonc');
  assert.equal(findConfig({env: f.env, path}), jsonc);
  const json = f.place(f.home, '.config', 'opencode', 'opencode.json');
  assert.equal(findConfig({env: f.env, path}), json);
  const program = f.place(f.home, '.opencode', 'bin', 'opencode.exe');
  const plugin = path.dirname(f.place(f.home, '.cache', 'opencode', 'packages', '@openabstractions', 'opencode',
    'node_modules', '@openabstractions', 'opencode', 'package.json'));
  const project = path.join(f.root, 'project');
  assert.deepEqual(sandboxArguments({project, env: f.env, path}), {method: 'install script',
    args: ['opencode', 'open', '--project', project, '--program', program, '--plugin', plugin, '--config', json]});
  assert.throws(() => sandboxArguments({project: 'relative', env: f.env, path}), /--project/);
  f.env.OPENCODE_CONFIG = f.place(f.root, 'elsewhere.json');
  assert.equal(findConfig({env: f.env, path}), f.env.OPENCODE_CONFIG);
});
