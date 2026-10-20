// Private host gate. A launched popup supplies no registration or enrollment
// proof. The native Connect owner must confirm both before capability calls.
export function createConnection({required, request, check}) {
  let state = required ? 'not_connected' : 'connected', pending, checking, closed = false;
  let helperProgram = '';
  const lifetime = new AbortController();
  const accept = reply => {
    state = !closed && reply?.outcome === 'connected' && reply.hostRegistered === true && reply.helperEnrolled === true
      ? 'connected' : reply === true || reply?.outcome === 'connect_pending' ? 'connect_pending' : 'not_connected';
    helperProgram = state === 'connected' && typeof reply?.helperProgram === 'string' ? reply.helperProgram : '';
  };
  const result = () => state === 'connected' ? null : {
    outcome: state,
    reason: 'OpenCode must connect to OpenAbstractions before using its tools.',
    guidance: state === 'connect_pending'
      ? 'Finish the Connect OpenCode to OpenAbstractions question in the native popup, then try this tool again. Connecting grants no permission to message another agent.'
      : 'Open the Abstraction Panel and connect OpenCode to OpenAbstractions, then try this tool again. Connecting grants no permission to message another agent.',
  };
  const connection = {
    helperProgram: () => closed || state !== 'connected' ? '' : helperProgram,
    result,
    async start() {
      if (checking) await checking;
      if (closed || !required || pending || state === 'connected') return pending;
      state = 'connect_pending';
      pending = Promise.resolve().then(() => request(lifetime.signal)).then(accept, () => { state = 'not_connected'; });
      return pending;
    },
    async refresh(fresh = false) {
      if (closed || !required || (!fresh && state === 'connected') || typeof check !== 'function') return;
      if (!checking) checking = Promise.resolve().then(() => check(lifetime.signal, fresh)).then(accept, () => { state = 'not_connected'; })
        .finally(() => { checking = undefined; });
      return checking;
    },
    async close() { closed = true; lifetime.abort(); await Promise.allSettled([pending, checking]); },
    guard(definition) {
      return {...definition, async execute(...args) {
        await connection.start();
        await connection.refresh();
        if (closed) return JSON.stringify({outcome: 'not_connected'});
        return result() ? JSON.stringify(result()) : definition.execute(...args);
      }};
    },
  };
  return connection;
}
