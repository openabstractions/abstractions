import assert from 'node:assert/strict';
import test from 'node:test';
import {winmmPath, winmmSymbols} from '../winmm-capture.js';

// Binds the real winmm.dll with the module's bun:ffi signatures and calls
// nothing, so no device opens. Bun on Windows runs it:
//   bun test adopters/opencode/test/winmm-binding.test.mjs
const runnable = typeof globalThis.Bun !== 'undefined' && process.platform === 'win32';

test('winmm.dll binds every waveIn signature listen uses', {skip: runnable ? false : 'needs Bun on Windows'}, async () => {
  const {dlopen} = await import('bun:ffi');
  const library = dlopen(winmmPath(), winmmSymbols);
  try {
    for (const name of Object.keys(winmmSymbols)) assert.equal(typeof library.symbols[name], 'function', name);
  } finally {
    library.close();
  }
});
