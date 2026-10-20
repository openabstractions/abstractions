import test from 'node:test';
import assert from 'node:assert/strict';
import {plainModelName, servableChatModels} from '../inventory.js';

test('catalogue title changes display and preserves model keys and limits', () => {
  const offer = {family:'family-key', title:'A friendly / model', names:['exact-alias'], servers:['engine'], context:32768n, contextSource:'effective', input:28672n};
  const titled = servableChatModels([offer]);
  const fallback = servableChatModels([{...offer, title:''}]);
  assert.deepEqual(Object.keys(titled), ['family-key']);
  assert.equal(titled['family-key'].name, 'A friendly / model');
  assert.deepEqual({...titled['family-key'], name:''}, {...fallback['family-key'], name:''});
  assert.equal(plainModelName({...offer, title:''}), 'family-key');
});
