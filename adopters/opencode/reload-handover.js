/**
 * OpenCode 1.18 snapshots its providers per instance, so the plugin reloads
 * the model list by asking OpenCode to re-create the instance for its
 * directory (instance.dispose). OpenCode starts the new instance in the same
 * process, and this module stays loaded across it. Before such a reload the
 * plugin hands its application-presence metadata owner over here, keyed by
 * directory: the old instance's dispose leaves them open, and the new
 * instance of the same directory adopts it. Per-chat helper stdin closes on
 * plugin disposal; the native chat owner retains its canonical context. A hand-over no instance
 * adopts within `adoptWithinMs` closes them, as the old dispose would have.
 */

/** How long a handed-over inbox waits for the next instance. */
export const adoptWithinMs = 30000;

const handovers = new Map();

/** Keep parts ({presence, messages}) for the next instance of directory. */
export function handOver(directory, parts, {schedule = setTimeout, unschedule = clearTimeout, within = adoptWithinMs} = {}) {
  const previous = handovers.get(directory);
  if (previous && previous.parts !== parts) close(directory, previous);
  const entry = {parts, unschedule};
  entry.timer = schedule(() => { if (handovers.get(directory) === entry) close(directory, entry); }, within);
  entry.timer?.unref?.();
  handovers.set(directory, entry);
}

/** Whether parts are being handed over for directory: the instance that
 * holds them is going away for a reload and must leave them open. */
export function handingOver(directory, parts) {
  return handovers.get(directory)?.parts === parts;
}

/** The parts handed over for directory, taken by the instance that adopts
 * them, or null. */
export function adopt(directory, {startup = false} = {}) {
  const entry = handovers.get(directory);
  if (!entry || Boolean(entry.parts.startupSnapshot) !== startup) return null;
  handovers.delete(directory);
  entry.unschedule(entry.timer);
  return entry.parts;
}

/** Take back a hand-over whose reload did not happen. */
export function withdraw(directory, parts) {
  const entry = handovers.get(directory);
  if (entry?.parts !== parts) return;
  handovers.delete(directory);
  entry.unschedule(entry.timer);
}

function close(directory, entry) {
  handovers.delete(directory);
  entry.unschedule(entry.timer);
  const {messages, presence} = entry.parts;
  void (async () => {
    try { await messages?.close(); } catch { /* closing is best effort */ }
    try { await presence?.close(); } catch { /* closing is best effort */ }
  })();
}
