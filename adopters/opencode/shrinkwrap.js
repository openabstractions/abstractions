// Writes npm-shrinkwrap.json from package-lock.json in the current directory. scripts/npm_packages.py
// runs it in the staged copy before `npm pack --ignore-scripts`, at the packed version. npm never packs a
// package-lock.json and ignores one inside a dependency; a published package carries its pinned dependency
// tree only as npm-shrinkwrap.json. Every locked package must name an npm registry tarball and its sha512
// integrity, or packing stops.
import {readFileSync, writeFileSync} from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const REGISTRY = /^https:\/\/registry\.npmjs\.org\/\S+\.tgz$/;
const INTEGRITY = /^sha512-[A-Za-z0-9+/]+={0,2}$/;
const OWN = '@openabstractions/';
const FIELDS = ['dependencies', 'optionalDependencies', 'peerDependencies'];

export class ShrinkwrapError extends Error {}

// The requirements outside OpenAbstractions, which the lock pins; the packer repins the OA ones.
function thirdParty(dependencies = {}) {
  return Object.fromEntries(Object.entries(dependencies).filter(([name]) => !name.startsWith(OWN)));
}

export function shrinkwrap(manifest, lock) {
  if (!lock || typeof lock.packages !== 'object' || !(lock.lockfileVersion >= 2)) {
    throw new ShrinkwrapError('package-lock.json is not a lockfileVersion 2 or 3 lock');
  }
  const root = lock.packages[''] ?? {};
  if (root.name !== manifest.name) {
    throw new ShrinkwrapError(`package-lock.json belongs to ${root.name}, not ${manifest.name}`);
  }
  for (const field of FIELDS) {
    const want = JSON.stringify(thirdParty(manifest[field]));
    const have = JSON.stringify(thirdParty(root[field]));
    if (want !== have) {
      throw new ShrinkwrapError(`package-lock.json ${field} ${have} differ from package.json ${want}`);
    }
  }
  const top = {...root, version: manifest.version};
  for (const field of FIELDS) {
    if (manifest[field]) top[field] = manifest[field];
  }
  const packages = {'': top};
  for (const [location, entry] of Object.entries(lock.packages)) {
    if (location === '') continue;
    if (entry.link) {
      throw new ShrinkwrapError(`${location} is a link; a published lock names registry tarballs only`);
    }
    if (!INTEGRITY.test(String(entry.integrity ?? ''))) {
      throw new ShrinkwrapError(`${location} has no sha512 integrity in package-lock.json`);
    }
    if (!REGISTRY.test(String(entry.resolved ?? ''))) {
      throw new ShrinkwrapError(`${location} resolves to ${entry.resolved ?? 'nothing'}, not an npm registry tarball`);
    }
    packages[location] = entry;
  }
  return {name: manifest.name, version: manifest.version, lockfileVersion: lock.lockfileVersion, requires: true, packages};
}

export function writeShrinkwrap(directory) {
  const read = (name) => JSON.parse(readFileSync(path.join(directory, name), 'utf8'));
  const result = shrinkwrap(read('package.json'), read('package-lock.json'));
  writeFileSync(path.join(directory, 'npm-shrinkwrap.json'), JSON.stringify(result, null, 2) + '\n');
  return result;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const result = writeShrinkwrap(process.cwd());
    // stderr: `npm pack --json` callers parse stdout.
    console.error(`npm-shrinkwrap.json: ${Object.keys(result.packages).length - 1} locked packages`);
  } catch (error) {
    if (!(error instanceof ShrinkwrapError)) throw error;
    console.error(`npm-shrinkwrap.json refused: ${error.message}`);
    process.exit(1);
  }
}
