// The one sentence OpenCode prints for a waiting word from job's catalogue
// (abstraction-job JOB-A15; abstraction-resource RES-L8). The Go job package's
// WaitingSentence and adopter-comfyui's oa_card.waiting_sentence print the
// same; each is tested against abstraction-job's testdata/waiting-sentences.json.

const heldBy = 'card:held_by:';

/** The sentence for a waiting word; '' for none, "Waiting." when unrecognized. */
export function waitingSentence(word) {
  if (!word) return '';
  if (word.startsWith(heldBy) && word.length > heldBy.length) {
    return `Waiting for the graphics card: held by ${word.slice(heldBy.length)}.`;
  }
  return 'Waiting.';
}
