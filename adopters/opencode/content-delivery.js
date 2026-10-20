import {createHash} from 'node:crypto';

const digestPattern = /^sha256:[0-9a-f]{64}$/;
const contentReaderContract = 'abstraction.storage/content-reader@1';

/** Media checks by content: each type's leading bytes. */
export const mediaMatchers = Object.freeze({
  'image/png': (bytes) => bytes.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10])),
  'image/jpeg': (bytes) => bytes.length >= 3 && bytes[0] === 255 && bytes[1] === 216 && bytes[2] === 255,
  'image/webp': (bytes) => bytes.length >= 12 && bytes.toString('ascii', 0, 4) === 'RIFF' && bytes.toString('ascii', 8, 12) === 'WEBP',
  'audio/wav': (bytes) => bytes.length >= 12 && bytes.toString('ascii', 0, 4) === 'RIFF' && bytes.toString('ascii', 8, 12) === 'WAVE',
});

/**
 * Reads one local OA delivery through the caller's content reader and
 * returns its bytes. The delivery must be local, carry a canonical digest, a
 * media type in mediaTypes and a size of 1..maxBytes. Every chunk must arrive
 * at its offset with the delivery's total; the handle is closed whatever
 * happens; the bytes must hash to the digest and match their media type.
 * `what` names the content in errors ("Image", "Speech").
 */
export async function readDelivery(reader, delivery, {mediaTypes, maxBytes, what}) {
  const limit = `${maxBytes / (1024 * 1024)} MiB`;
  if (delivery?.location !== 'local' || !digestPattern.test(delivery.digest)
      || !mediaTypes.has(delivery.mediaType) || !Number.isSafeInteger(Number(delivery.size))
      || Number(delivery.size) < 1 || Number(delivery.size) > maxBytes) {
    throw new Error(`${what} delivery is unsupported or exceeds the ${limit} tool limit.`);
  }
  const opened = await reader.open(delivery.digest);
  if (opened?.outcome !== 'opened' || opened.resource?.digest !== delivery.digest
      || Number(opened.resource.size) !== Number(delivery.size)) throw new Error(`${what} content open: ${opened?.outcome ?? 'invalid'}`);
  const resource = opened.resource;
  const parts = [];
  const hash = createHash('sha256');
  let offset = 0;
  try {
    while (offset < Number(delivery.size)) {
      const response = await reader.read(resource.handle, BigInt(offset), 65536n);
      const chunk = response?.chunk;
      const bytes = chunk?.data;
      if (response?.outcome !== 'data' || Number(chunk.offset) !== offset
          || Number(chunk.total) !== Number(delivery.size) || !(bytes instanceof Uint8Array)
          || bytes.length < 1 || offset + bytes.length > Number(delivery.size)) throw new Error(`${what} content changed during read.`);
      const copy = Buffer.from(bytes);
      parts.push(copy);
      hash.update(copy);
      offset += copy.length;
      if (chunk.eof !== (offset === Number(delivery.size))) throw new Error(`${what} content ended at the wrong offset.`);
    }
  } finally {
    await reader.close(resource.handle);
  }
  if (`sha256:${hash.digest('hex')}` !== delivery.digest) throw new Error(`${what} content digest mismatch.`);
  const bytes = Buffer.concat(parts, offset);
  const matches = mediaMatchers[delivery.mediaType];
  if (!matches || !matches(bytes)) throw new Error(`${what} bytes do not match the declared media type.`);
  return bytes;
}

/** The caller's OA content reader on this machine. */
export async function resolveContentReader(options, signal) {
  const [{Machine}, {ContentReaderClient}, {BunNativeConnector}] = await Promise.all([
    import('@openabstractions/facade'),
    import('@openabstractions/storage-content'),
    import('@openabstractions/ipc/bun'),
  ]);
  const machine = new Machine(options?.runtimeEndpoint ?? null, {
    connector: new BunNativeConnector(), timeout: 30000, cancellation: signal,
    server: options?.server ?? null,
  });
  return new ContentReaderClient(await machine.resolveService(contentReaderContract, {scope: 'local'}));
}
