// Inert source journey: the generated client reads the actual native router.
import assert from 'node:assert/strict';
import net from 'node:net';
import {pathToFileURL} from 'node:url';
import {createInventoryWatch, offerSignature} from '../../inventory-watch.js';

const {RouterClient} = await import(pathToFileURL(process.argv[4]));
const client = new RouterClient({exchangeFrame(frame) {
  return new Promise((resolve, reject) => {
    const socket = net.createConnection(process.argv[2]);
    let pending = Buffer.alloc(0), done = false;
    socket.setTimeout(35000, () => socket.destroy(new Error('fixture router deadline')));
    socket.on('error', reject);
    socket.on('connect', () => {
      const header = Buffer.alloc(4); header.writeUInt32BE(frame.length);
      socket.write(Buffer.concat([header, Buffer.from(frame)]));
    });
    socket.on('data', chunk => {
      pending = Buffer.concat([pending, chunk]);
      if (pending.length < 4) return;
      const length = pending.readUInt32BE(0);
      if (length > 1048576) return socket.destroy(new Error('fixture frame bound'));
      if (pending.length >= length + 4 && !done) {
        done = true; resolve(new Uint8Array(pending.subarray(4, length + 4))); socket.destroy();
      }
    });
    socket.on('close', () => { if (!done) reject(new Error('fixture router closed')); });
  });
}});
const request = {profile: 'chat', placement: 'local', reserve: 4096n, fresh: false};
const before = await client.offer(request);
assert.equal(before.outcome, 'offered');
assert.equal(before.offers.length, 0);
const headings = new Map([['local', {options: {}, placements: ['local'],
  signature: offerSignature({local: before.offers}), state: 'answered'}]]);
const watch = createInventoryWatch({headings,
  observe: (_options, _placement, cursor, wait) => client.observeOffer(request, cursor, BigInt(wait)),
  onError: error => { console.error(error); process.exitCode = 1; },
  onChange: async () => {
    const after = await client.offer(request);
    assert.ok(after.offers.some(offer => offer.servers.includes(process.argv[3])));
    watch.stop(); console.log(`inventory-sees-${process.argv[3]}`); process.exit(0);
  }});
watch.start();
console.log('inventory-watching');
