/**
 * Whether the OpenAbstractions runtime answers, shared by the server plugin
 * (plugin.js) and the provider (index.js), which OpenCode loads from the same
 * package and so import one instance of this module. A lost connection (the
 * native IPC connector's Disconnected or IoError, or a resolution that found
 * no runtime) marks OA unavailable: OpenCode keeps working, OA's tools answer
 * at once that they are off, and one toast says so. Each tool call after
 * `retryMs` tries the runtime again, and every call that gets an answer from
 * the runtime marks it available, with one toast saying OA is back. Every
 * other IPC status stays the caller's error; Untrusted, a runtime whose
 * identity did not verify, reads as its own refusal.
 */

export const unavailableMessage = 'OpenAbstractions is unavailable; OA tools are off until it returns.';
export const returnedMessage = 'OpenAbstractions is back; OA tools are on again.';
export const untrustedMessage = 'OpenAbstractions stopped this call: the runtime it reached did not prove it is '
  + "this computer's OpenAbstractions. Check the OpenAbstractions installation; nothing was sent.";

// The native IPC connector's statuses (@openabstractions/ipc Status).
const disconnected = 2;
const ioError = 3;
const untrusted = 8;

/** The status of the first FrameError of the native connector (a numeric
 * status and a transferred count) in error's cause chain, or null. */
function frameStatus(error) {
  for (let current = error, depth = 0; current && depth < 8; current = current.cause, depth += 1) {
    if (typeof current.status === 'number' && 'transferred' in current) return current.status;
  }
  return null;
}

/** Whether error means the runtime is away: a lost connection (Disconnected
 * or IoError), or a runtime_unavailable resolution that no other IPC status
 * caused. */
export function runtimeUnavailable(error) {
  const status = frameStatus(error);
  if (status !== null) return status === disconnected || status === ioError;
  for (let current = error, depth = 0; current && depth < 8; current = current.cause, depth += 1) {
    if (current.name === 'ResolutionError' && current.status === 'runtime_unavailable') return true;
  }
  return false;
}

/** Whether the runtime reached did not prove its identity (Untrusted). */
export function runtimeUntrusted(error) {
  return frameStatus(error) === untrusted;
}

/** A short reason for a log or a tool reply, never a path or a secret. */
export function unavailableReason(error) {
  for (let current = error, depth = 0; current && depth < 8; current = current.cause, depth += 1) {
    if (typeof current.status === 'number' && 'transferred' in current) return `ipc status ${current.status}`;
    if (current.name === 'ResolutionError') return `resolution ${current.status}`;
  }
  return 'transport failure';
}

export class Availability {
  constructor({retryMs = 15000, now = () => Date.now()} = {}) {
    this.retryMs = retryMs;
    this.now = now;
    this.down = false;
    this.since = 0;
    this.tried = 0;
    this.notify = null;
  }

  /** Where the one toast per change goes: the server plugin sets it. */
  onChange(notify) {
    this.notify = typeof notify === 'function' ? notify : null;
  }

  /** Whether a tool call should try the runtime: always while OA answers,
   * and once per retryMs while it does not. */
  shouldTry() {
    if (!this.down) return true;
    const now = this.now();
    if (now - this.tried < this.retryMs) return false;
    this.tried = now;
    return true;
  }

  /** A transport failure: OA is unavailable from now on, said once. */
  markDown(error) {
    if (this.down) return;
    this.down = true;
    this.since = this.now();
    this.tried = this.since;
    this.#tell('warning', unavailableMessage, error);
  }

  /** An answer from the runtime: OA is available again, said once. */
  markUp() {
    if (!this.down) return;
    this.down = false;
    this.#tell('success', returnedMessage);
  }

  #tell(variant, message, error) {
    try {
      const pending = this.notify?.({variant, message, reason: error ? unavailableReason(error) : ''});
      pending?.catch?.(() => {});
    } catch {
      // A toast must not break the call that changed OA's state.
    }
  }
}

/** The one instance plugin.js and index.js share. */
export const availability = new Availability();

/**
 * Wrap a tool that calls the runtime so that a lost runtime answers the model
 * with one sentence instead of failing the call, and OA's tools stay off
 * while it is away. The tool's resolvers mark OA back when the runtime
 * answers (plugin.js), so a call that ends before reaching it, such as a
 * refused save_to, changes nothing. An Untrusted runtime fails the call with
 * its own sentence.
 */
export function guardTool(definition, state = availability) {
  const execute = definition.execute;
  return {
    ...definition,
    async execute(args, context) {
      if (!state.shouldTry()) return unavailableMessage;
      try {
        return await execute.call(definition, args, context);
      } catch (error) {
        if (runtimeUntrusted(error)) throw new Error(untrustedMessage, {cause: error});
        if (!runtimeUnavailable(error)) throw error;
        state.markDown(error);
        return `${unavailableMessage} (${unavailableReason(error)})`;
      }
    },
  };
}

/** Resolve a runtime client and mark OA back: resolution is an answer. */
export async function answered(resolving, state = availability) {
  const client = await resolving;
  state.markUp();
  return client;
}
