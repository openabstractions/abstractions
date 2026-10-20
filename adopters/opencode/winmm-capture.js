import {createRequire} from 'node:module';
import {join} from 'node:path';

/** 16 kHz mono 16-bit PCM, the input transcription engines take without resampling. */
export const sampleRate = 16000;
export const channels = 1;
export const bitsPerSample = 16;
const blockAlign = channels * bitsPerSample / 8;
const bufferMs = 100;
const bufferBytes = sampleRate * blockAlign * bufferMs / 1000;
const bufferCount = 8;
const pollMs = 25;

const WAVE_MAPPER = 0xFFFFFFFF;
const CALLBACK_NULL = 0;
const WAVE_FORMAT_PCM = 1;
const WHDR_DONE = 0x1;
const MMSYSERR_NOERROR = 0;
const MMSYSERR_BADDEVICEID = 2;
const MMSYSERR_ALLOCATED = 4;
const MMSYSERR_NODRIVER = 6;
// WAVEHDR on 64-bit Windows: lpData, dwBufferLength, dwBytesRecorded, dwUser,
// dwFlags, dwLoops, lpNext, reserved.
const WAVEHDR_SIZE = 48;
const WAVEFORMATEX_SIZE = 18;

/** The winmm.dll waveIn entry points this module calls, as bun:ffi signatures.
 * MMRESULT and UINT are u32; HWAVEIN and the header and format pointers are ptr;
 * DWORD_PTR callback and instance are u64. */
export const winmmSymbols = Object.freeze({
  waveInGetNumDevs: {args: [], returns: 'u32'},
  waveInOpen: {args: ['ptr', 'u32', 'ptr', 'u64', 'u64', 'u32'], returns: 'u32'},
  waveInPrepareHeader: {args: ['ptr', 'ptr', 'u32'], returns: 'u32'},
  waveInUnprepareHeader: {args: ['ptr', 'ptr', 'u32'], returns: 'u32'},
  waveInAddBuffer: {args: ['ptr', 'ptr', 'u32'], returns: 'u32'},
  waveInStart: {args: ['ptr'], returns: 'u32'},
  waveInReset: {args: ['ptr'], returns: 'u32'},
  waveInClose: {args: ['ptr'], returns: 'u32'},
});

/** A capture refusal with a code the listen command turns into one sentence:
 * unsupported_platform, no_microphone, microphone_busy or capture_failed. */
export class CaptureError extends Error {
  constructor(code, message) {
    super(message);
    this.name = 'CaptureError';
    this.code = code;
  }
}

/** winmm.dll by its System32 path, so the DLL search order cannot substitute another file. */
export function winmmPath() {
  return join(process.env.SystemRoot || 'C:\\Windows', 'System32', 'winmm.dll');
}

function loadFfi() {
  try {
    return createRequire(import.meta.url)('bun:ffi');
  } catch {
    throw new CaptureError('unsupported_platform', 'Microphone capture needs the Bun runtime OpenCode runs on.');
  }
}

function openError(code) {
  if (code === MMSYSERR_BADDEVICEID || code === MMSYSERR_NODRIVER) {
    return new CaptureError('no_microphone', 'No microphone is available.');
  }
  if (code === MMSYSERR_ALLOCATED) return new CaptureError('microphone_busy', 'The microphone is in use.');
  return new CaptureError('capture_failed', `waveInOpen failed with MMRESULT ${code}.`);
}

/**
 * Start recording 16 kHz mono PCM16 from the default Windows input through
 * winmm.dll's waveIn functions, polling header flags (CALLBACK_NULL) so no
 * native thread calls into JavaScript. Returns {stop(), elapsedMs()}; stop()
 * resolves to the recorded PCM bytes and closes the device.
 *
 * `ffi` ({dlopen, ptr}) and `library` exist for tests; production loads bun:ffi
 * and winmm.dll from System32.
 */
export function startCapture({ffi = null, library = null, platform = process.platform,
  now = () => performance.now(), setInterval: every = setInterval, clearInterval: stopEvery = clearInterval} = {}) {
  if (platform !== 'win32') {
    throw new CaptureError('unsupported_platform', 'Microphone capture is available on Windows only.');
  }
  const binding = ffi ?? loadFfi();
  let winmm;
  try {
    winmm = binding.dlopen(library ?? winmmPath(), winmmSymbols);
  } catch {
    throw new CaptureError('capture_failed', 'winmm.dll could not be loaded.');
  }
  const api = winmm.symbols;
  // Every buffer the driver writes into is an ArrayBuffer kept referenced
  // until waveInClose returns.
  const format = new Uint8Array(new ArrayBuffer(WAVEFORMATEX_SIZE));
  const formatView = new DataView(format.buffer);
  formatView.setUint16(0, WAVE_FORMAT_PCM, true);
  formatView.setUint16(2, channels, true);
  formatView.setUint32(4, sampleRate, true);
  formatView.setUint32(8, sampleRate * blockAlign, true);
  formatView.setUint16(12, blockAlign, true);
  formatView.setUint16(14, bitsPerSample, true);
  formatView.setUint16(16, 0, true);
  const handleOut = new BigUint64Array(new ArrayBuffer(8));
  const buffers = [];
  let handle = 0;
  let prepared = 0;
  const release = () => {
    if (handle) {
      api.waveInReset(handle);
      for (let i = 0; i < prepared; i++) api.waveInUnprepareHeader(handle, buffers[i].header, WAVEHDR_SIZE);
      api.waveInClose(handle);
      handle = 0;
    }
    winmm.close?.();
  };
  try {
    if (api.waveInGetNumDevs() === 0) throw new CaptureError('no_microphone', 'No microphone is available.');
    const opened = api.waveInOpen(handleOut, WAVE_MAPPER, format, 0, 0, CALLBACK_NULL);
    if (opened !== MMSYSERR_NOERROR) throw openError(opened);
    handle = Number(handleOut[0]);
    if (!handle) throw new CaptureError('capture_failed', 'waveInOpen returned no device handle.');
    for (let i = 0; i < bufferCount; i++) {
      const data = new Uint8Array(new ArrayBuffer(bufferBytes));
      const header = new Uint8Array(new ArrayBuffer(WAVEHDR_SIZE));
      const view = new DataView(header.buffer);
      view.setBigUint64(0, BigInt(binding.ptr(data)), true);
      view.setUint32(8, bufferBytes, true);
      buffers.push({data, header, view});
      const prepare = api.waveInPrepareHeader(handle, header, WAVEHDR_SIZE);
      if (prepare !== MMSYSERR_NOERROR) throw new CaptureError('capture_failed', `waveInPrepareHeader failed with MMRESULT ${prepare}.`);
      prepared++;
      const added = api.waveInAddBuffer(handle, header, WAVEHDR_SIZE);
      if (added !== MMSYSERR_NOERROR) throw new CaptureError('capture_failed', `waveInAddBuffer failed with MMRESULT ${added}.`);
    }
    const started = api.waveInStart(handle);
    if (started !== MMSYSERR_NOERROR) throw new CaptureError('capture_failed', `waveInStart failed with MMRESULT ${started}.`);
  } catch (error) {
    release();
    throw error;
  }
  const chunks = [];
  let total = 0;
  let failure = null;
  let dropped = 0;
  const startedAt = now();
  // The driver fills buffers in the order they were added, so `queue` holds
  // them in that order: done buffers are taken from its head and requeued at
  // its tail.
  const queue = [...buffers];
  const drain = (requeue) => {
    let done = 0;
    while (queue.length > 0) {
      const buffer = queue[0];
      const flags = buffer.view.getUint32(24, true);
      if ((flags & WHDR_DONE) === 0) break;
      queue.shift();
      done++;
      const recorded = Math.min(buffer.view.getUint32(12, true), bufferBytes);
      if (recorded > 0) {
        chunks.push(buffer.data.slice(0, recorded));
        total += recorded;
      }
      buffer.view.setUint32(12, 0, true);
      buffer.view.setUint32(24, flags & ~WHDR_DONE, true);
      if (requeue && !failure) {
        const added = api.waveInAddBuffer(handle, buffer.header, WAVEHDR_SIZE);
        if (added !== MMSYSERR_NOERROR) failure = new CaptureError('capture_failed', `waveInAddBuffer failed with MMRESULT ${added}.`);
        else queue.push(buffer);
      }
    }
    // Every buffer done at once means the driver had none to fill for a while.
    if (requeue && done === bufferCount) dropped++;
  };
  const timer = every(() => drain(true), pollMs);
  let stopped = null;
  return {
    elapsedMs: () => now() - startedAt,
    /** Polls that found every buffer full: audio the driver had nowhere to put. */
    dropped: () => dropped,
    stop() {
      if (stopped) return stopped;
      stopEvery(timer);
      stopped = (async () => {
        try {
          drain(false);
          // waveInReset returns every queued buffer marked done with what it holds.
          api.waveInReset(handle);
          drain(false);
        } finally {
          release();
        }
        if (failure) throw failure;
        const pcm = new Uint8Array(total);
        let offset = 0;
        for (const chunk of chunks) {
          pcm.set(chunk, offset);
          offset += chunk.length;
        }
        return pcm;
      })();
      return stopped;
    },
  };
}
