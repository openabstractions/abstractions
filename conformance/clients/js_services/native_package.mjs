// A source-built IPC tarball must load its own installed addon without an override.
import assert from 'node:assert/strict';
import {NativeConnector, addonPath} from '@openabstractions/ipc';

assert.equal(process.env.ABSTRACTION_IPC_NODE, undefined);
assert.equal(addonPath(), './native/oa_ipc_node.node');
assert.ok(new NativeConnector().runtimeEndpoint());
console.log('PASS packed source-built IPC addon loads at the default path');
