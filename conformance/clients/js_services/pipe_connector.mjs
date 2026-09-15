// Test-only framed connector over node:net. It verifies no server identity and
// is never shipped; the installed NativeConnector is the trusted production path.
// The receiving Go service still checks this process's native Program identity.
import net from 'node:net';

export class PipeTestConnector {
  supports(scope, transport) { return scope === 'local' && transport === 'oa-framed-local@1'; }
  connect(endpoint, {deadline = null, timeout = 5000, maxFrame = 1048576} = {}) {
    const until = deadline ?? performance.now() + timeout;
    const call = (frame, reply) => new Promise((resolve, reject) => {
      if (!(frame instanceof Uint8Array) || frame.byteLength > maxFrame) return reject(new TypeError('frame must be bounded Uint8Array'));
      const left = until - performance.now();
      if (left <= 0) return reject(new Error('waiting budget exhausted'));
      const socket = net.connect(endpoint);
      let settled = false, received = Buffer.alloc(0);
      const timer = setTimeout(() => fail(new Error('timeout')), Math.ceil(left));
      const fail = (error) => { if (settled) return; settled = true; clearTimeout(timer); socket.destroy(); reject(error); };
      const succeed = (value) => { if (settled) return; settled = true; clearTimeout(timer); resolve(value); };
      socket.on('error', fail);
      socket.on('connect', () => {
        const header = Buffer.alloc(4);
        header.writeUInt32BE(frame.byteLength);
        socket.write(Buffer.concat([header, Buffer.from(frame.buffer, frame.byteOffset, frame.byteLength)]));
        if (!reply) socket.end();
      });
      socket.on('data', (chunk) => {
        if (!reply) return fail(new Error('unexpected reply to one-way frame'));
        received = Buffer.concat([received, chunk]);
        if (received.length < 4) return;
        const size = received.readUInt32BE(0);
        if (size > maxFrame) return fail(new Error('oversized reply'));
        if (received.length > 4 + size) return fail(new Error('trailing reply bytes'));
        if (received.length === 4 + size) {
          // The service waits for end-of-stream after its reply.
          socket.end();
          succeed(new Uint8Array(received.subarray(4)));
        }
      });
      socket.on('close', () => { if (reply) fail(new Error('disconnected before reply')); else succeed(undefined); });
    });
    return {exchangeFrame: (frame) => call(frame, true), writeFrame: (frame) => call(frame, false)};
  }
}
