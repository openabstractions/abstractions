import assert from 'node:assert/strict';
import {existsSync, readFileSync} from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import test from 'node:test';
import {waitingSentence} from '../waiting.js';

// Every client prints the same sentence for a waiting word (JOB-A15): the
// rows are abstraction-job's, which its Go test and the ComfyUI card
// helper's test read too. A published checkout of this package alone has no
// abstraction-job beside it; the private workspace runs the comparison.
const root = path.resolve(fileURLToPath(new URL('.', import.meta.url)), '..', '..', '..');
const sentences = path.join(root, 'openabstractions-flat', 'abstraction-job', 'testdata', 'waiting-sentences.json');

test('a waiting word prints the sentence every person-facing client prints', {skip: !existsSync(sentences) && 'abstraction-job is not beside this package'}, () => {
  const rows = JSON.parse(readFileSync(sentences, 'utf8')).sentences;
  assert.ok(rows.length > 0, 'the sentence table has rows');
  for (const {word, sentence} of rows) {
    assert.equal(waitingSentence(word), sentence, `the sentence for ${JSON.stringify(word)}`);
  }
});
