// Adapt existing inert reply/iterator fixtures to the provider's native
// Start/Observe seam. Production uses the generated client's actual methods.
export class FixtureMachine {
  async resolveService() { return {withWaiting() { return this; }}; }
}

const clients = new WeakMap();
export function nativeChatFixture(legacy) {
  if (typeof legacy.start === 'function') return legacy;
	if (clients.has(legacy)) return clients.get(legacy);
  let iterator, opened;
  const start = async (request, guarantees) => {
    if (legacy.startStream) {
      opened = await legacy.startStream(request, guarantees);
      iterator = opened.stream[Symbol.asyncIterator]();
      return opened.admission;
    }
    if (legacy.stream) iterator = legacy.stream(request)[Symbol.asyncIterator]();
    else {
      const reply = await legacy.complete(request);
      iterator = (async function* () {
        for (const [index, part] of (reply.message?.parts ?? []).entries()) yield {kind: 'part', index: BigInt(index), part};
        yield {kind: 'end', end: reply};
      })();
    }
    return {outcome: 'accepted', operation: 'fixture-operation'};
  };
  const client = {
    ...legacy, start, startWithGuarantees: start,
    async observe(_operation, cursor) {
      const next = await iterator.next();
      return {outcome: 'page', deltas: next.done ? [] : [next.value], next: next.done ? cursor : cursor + 1n, atEnd: next.done || next.value.kind === 'end'};
    },
    async cancel() { await opened?.cancel?.(); await iterator?.return?.(); },
  };
  clients.set(legacy, client);
  return client;
}
