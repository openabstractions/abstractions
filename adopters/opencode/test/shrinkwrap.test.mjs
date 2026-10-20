import assert from 'node:assert/strict';
import {spawnSync} from 'node:child_process';
import {cpSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import test from 'node:test';

import {shrinkwrap, ShrinkwrapError} from '../shrinkwrap.js';

const adopter = fileURLToPath(new URL('..', import.meta.url));
const read = (name) => JSON.parse(readFileSync(path.join(adopter, name), 'utf8'));
const OWN = '@openabstractions/';

// The manifest npm_packages.py stages: the packed version, with OA requirements repinned to it.
function staged(version) {
  const manifest = read('package.json');
  manifest.version = version;
  manifest.dependencies = Object.fromEntries(Object.entries(manifest.dependencies)
    .map(([name, spec]) => [name, name.startsWith(OWN) ? version : spec]));
  return manifest;
}

test('the shrinkwrap carries every locked package with its integrity and the packed root', () => {
  const lock = read('package-lock.json');
  const result = shrinkwrap(staged('0.4.0'), lock);
  assert.equal(result.name, '@openabstractions/opencode');
  assert.equal(result.version, '0.4.0');
  assert.equal(result.packages[''].version, '0.4.0');
  assert.equal(result.packages[''].dependencies['@openabstractions/ipc'], '0.4.0');
  assert.equal(result.packages[''].dependencies['@ai-sdk/provider'], '3.0.16');
  const locked = Object.keys(lock.packages).filter((key) => key !== '');
  assert.deepEqual(Object.keys(result.packages).filter((key) => key !== ''), locked);
  for (const key of locked) assert.equal(result.packages[key].integrity, lock.packages[key].integrity);
  assert.ok(locked.length > 0);
});

test('a lock entry without integrity is refused by name', () => {
  const lock = read('package-lock.json');
  delete lock.packages['node_modules/@ai-sdk/provider'].integrity;
  assert.throws(() => shrinkwrap(staged('0.4.0'), lock),
    (error) => error instanceof ShrinkwrapError && error.message.includes('node_modules/@ai-sdk/provider has no sha512 integrity'));
  const weak = read('package-lock.json');
  weak.packages['node_modules/@ai-sdk/provider'].integrity = 'sha1-AAAAAAAAAAAAAAAAAAAAAAAAAAA=';
  assert.throws(() => shrinkwrap(staged('0.4.0'), weak), /no sha512 integrity/);
});

test('a link or a tarball outside the npm registry is refused', () => {
  const linked = read('package-lock.json');
  linked.packages['node_modules/@openabstractions/ipc'] = {resolved: '../ipc', link: true};
  assert.throws(() => shrinkwrap(staged('0.4.0'), linked), /is a link/);
  const git = read('package-lock.json');
  git.packages['node_modules/@ai-sdk/provider'].resolved = 'git+https://example.test/provider.git';
  assert.throws(() => shrinkwrap(staged('0.4.0'), git), /not an npm registry tarball/);
});

test('a lock written for other third-party requirements is refused', () => {
  const manifest = staged('0.4.0');
  manifest.dependencies['@ai-sdk/provider'] = '3.0.17';
  assert.throws(() => shrinkwrap(manifest, read('package-lock.json')), /differ from package\.json/);
  assert.throws(() => shrinkwrap({...staged('0.4.0'), name: 'other'}, read('package-lock.json')), /belongs to/);
  assert.throws(() => shrinkwrap(staged('0.4.0'), {lockfileVersion: 1, dependencies: {}}), /lockfileVersion 2 or 3/);
});

// scripts/npm_packages.py runs the generator in its staged copy and packs with --ignore-scripts.
test('node shrinkwrap.js writes npm-shrinkwrap.json, and refuses a lock entry without integrity', () => {
  const directory = mkdtempSync(path.join(tmpdir(), 'oa-opencode-shrinkwrap-'));
  try {
    cpSync(path.join(adopter, 'shrinkwrap.js'), path.join(directory, 'shrinkwrap.js'));
    writeFileSync(path.join(directory, 'package.json'), JSON.stringify(staged('0.4.0'), null, 2) + '\n');
    const lock = read('package-lock.json');
    writeFileSync(path.join(directory, 'package-lock.json'), JSON.stringify(lock, null, 2) + '\n');
    const run = () => spawnSync(process.execPath, ['shrinkwrap.js'], {cwd: directory, encoding: 'utf8'});
    const written = run();
    assert.equal(written.status, 0, written.stderr);
    assert.equal(written.stdout, '');
    const wrap = JSON.parse(readFileSync(path.join(directory, 'npm-shrinkwrap.json'), 'utf8'));
    assert.equal(wrap.packages[''].version, '0.4.0');
    rmSync(path.join(directory, 'npm-shrinkwrap.json'));

    delete lock.packages['node_modules/zod'].integrity;
    writeFileSync(path.join(directory, 'package-lock.json'), JSON.stringify(lock, null, 2) + '\n');
    const refused = run();
    assert.equal(refused.status, 1);
    assert.match(refused.stderr, /node_modules\/zod has no sha512 integrity/);
    assert.ok(!readdirSync(directory).includes('npm-shrinkwrap.json'));
  } finally {
    rmSync(directory, {recursive: true, force: true});
  }
});
