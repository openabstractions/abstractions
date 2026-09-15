// Installed npm package metadata, and dependencies derived from generated imports.
import assert from 'node:assert/strict';
import {readFileSync, readdirSync, statSync} from 'node:fs';
import {join} from 'node:path';

const modules = join(process.cwd(), 'node_modules');
const root = join(modules, '@openabstractions');
const importSpecifier = /^\s*import\s+[^'"\n]*\bfrom\s+['"]([^'"]+)['"]/gm;
const walk = (dir) => readdirSync(dir).flatMap((name) => {
  const path = join(dir, name);
  return statSync(path).isDirectory() ? walk(path) : [path];
});
const names = readdirSync(root).sort();
for (const name of names) {
  const dir = join(root, name);
  const pkg = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8'));
  const repository = pkg.repository ?? {};
  assert.equal(pkg.name, '@openabstractions/' + name);
  assert.equal(pkg.version, '0.0.0', `${name} keeps its source-development version`);
  assert.equal(pkg.license, 'Apache-2.0', name);
  assert.ok(typeof pkg.description === 'string' && pkg.description.length > 20, `${name} description`);
  assert.equal(repository.type, 'git', name);
  assert.match(repository.url ?? '', /^git\+https:\/\/github\.com\/openabstractions\/abstraction-[a-z]+\.git$/, name);
  assert.equal(repository.directory, 'javascript', name);
  const base = repository.url.slice('git+'.length, -'.git'.length);
  assert.ok(pkg.homepage?.startsWith(base), `${name} homepage`);
  assert.equal(pkg.bugs?.url, base + '/issues', name);
  const imported = new Set();
  for (const file of walk(dir).filter((path) => /\.(mjs|js)$/.test(path))) {
    for (const match of readFileSync(file, 'utf8').matchAll(importSpecifier)) {
      if (!match[1].startsWith('.') && !match[1].startsWith('node:')) imported.add(match[1]);
    }
  }
  const declared = pkg.dependencies ?? {};
  assert.deepEqual([...imported].sort(), Object.keys(declared).sort(), `${name} dependencies match generated imports`);
  for (const dependency of imported) {
    const installed = JSON.parse(readFileSync(join(modules, dependency, 'package.json'), 'utf8'));
    assert.equal(installed.version, declared[dependency], `${name} resolves ${dependency}`);
  }
}
console.log(`PASS installed metadata and dependency declarations for ${names.length} packages: ${names.join(', ')}`);
