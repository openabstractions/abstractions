// Loaded by `npm test` (node --import ./runtime-isolation.mjs --test) and by
// test/run_node_tests.py before any test file: every test resolves an
// endpoint no process serves, never the person's installed runtime. On
// 2026-11-01 a plain `node --test` run here reached the installed runtime and
// raised a real first-use permission question for node.exe on the owner's
// screen. The native runtime selection reads ABSTRACTION_RUNTIME_ENDPOINT
// before any installed registration (abstraction-identity bootstrap.hpp), and
// the facade starts no installed runtime while it is set. A test that starts
// its own fixture runtime sets the variable to that fixture for its own run.
import {tmpdir} from 'node:os';
import {join} from 'node:path';

/** The endpoint name an unserved test runtime uses for this process tree. */
export function unservedTestEndpoint(platform = process.platform, pid = process.pid) {
  const name = `oa-opencode-test-unserved-${pid}`;
  return platform === 'win32' ? `\\\\.\\pipe\\${name}` : join(tmpdir(), name + '.sock');
}

/** True for the installed per-account runtime endpoint and its well-known
 * names, which no test may select. */
export function installedRuntimeEndpoint(endpoint) {
  return /(^|[\\/])openabstractions-(user|machine)-.*runtime/i.test(String(endpoint ?? ''));
}

const current = process.env.ABSTRACTION_RUNTIME_ENDPOINT;
if (!current) process.env.ABSTRACTION_RUNTIME_ENDPOINT = unservedTestEndpoint();
else if (installedRuntimeEndpoint(current)) {
  throw new Error(`refusing to run the OpenCode adopter tests against the installed runtime endpoint ${current}`);
}
