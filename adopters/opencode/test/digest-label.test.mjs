// The model-label digest guard for OpenCode's picker (owner, 2026-11-01: the
// picker showed "55983c5a…f9f1 · 32k · this com…"). Every name shape a
// router offer can carry, a family or alias that is a file digest, a digest
// or commit in any part of a path, a sha256 prefix, yields a label with no
// run of 16 or more hex digits. Imports inventory.js alone, which reads no
// runtime, so scripts/model_label_digest.py runs it with no installed packages.
import assert from 'node:assert/strict';
import test from 'node:test';
import {modelName, servableChatModels} from '../inventory.js';

const digest = '55983c5a75a1ab969824077b3bb3de4146e82a9234072b48ad4e8f92ad3fe9f1';
const commit = '5bc3e238d916f48a861bac2f8a1990a0e9b7e98d';
const hex = /[0-9a-f]{16,}/i;

test('no picker label carries a digest, whatever names the router offers', () => {
  const shapes = [
    [digest, [digest]],
    [`${digest}.gguf`, [`${digest}.gguf`]],
    [`sha256:${digest}`, [`blobs/sha256-${digest}`]],
    [digest, [`models/${digest}/model.gguf`]],
    [`models--Qwen--x/snapshots/${commit}/qwen.gguf`, []],
    [digest, ['qwen3.6-35b-a3b']],
    ['qwen3.6-35b-a3b', ['qwen3.6-35b-a3b']],
  ];
  for (const [family, names] of shapes) {
    const offer = {family, names, servers: ['modelhost'], context: 32768, contextSource: 'default', input: 28672};
    for (const label of [modelName(offer, 32768), modelName(offer, 32768, () => false)]) {
      assert.doesNotMatch(label, hex, `${family} ${JSON.stringify(names)} -> ${label}`);
      assert.doesNotMatch(label, /modelhost.*·|this computer: /, label);
    }
  }
  const listed = servableChatModels(shapes.map(([family, names]) =>
    ({family, names: names.length ? names : [family], servers: ['modelhost'], context: 32768, contextSource: 'default', input: 28672})));
  for (const model of Object.values(listed)) assert.doesNotMatch(model.name, hex, model.name);
});
