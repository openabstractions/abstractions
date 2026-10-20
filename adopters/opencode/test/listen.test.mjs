import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import {TranscriptionClient} from '@openabstractions/inference/protocol';
import {ContentWriterClient} from '@openabstractions/storage-content';
import {createListener, indicatorText, listenCommand, maxListenMs, registerListen, transcribe, uploadContent,
  wavFromPcm16} from '../listen.js';
import {CaptureError, startCapture, winmmSymbols} from '../winmm-capture.js';
import tuiPlugin from '../tui.js';

const sha = (bytes) => `sha256:${createHash('sha256').update(bytes).digest('hex')}`;

function speech(length = 3200) {
  const pcm = new Uint8Array(length);
  for (let i = 0; i < length; i++) pcm[i] = (i * 37) & 0xff;
  return pcm;
}

/** A capture source that records `pcm`, or throws `error` when opened. */
function fakeCapture({pcm = speech(), error = null, calls = []} = {}) {
  return () => {
    calls.push('open');
    if (error) throw error;
    return {elapsedMs: () => 3200, async stop() { calls.push('stop'); return pcm; }};
  };
}

function fakeWriter(calls, overrides = {}) {
  let staged = [];
  let declared = null;
  return {
    async begin(request, digest, size) {
      calls.push(['begin', request, digest, size]);
      declared = {digest, size};
      return {outcome: 'started', upload: {handle: 'u1', digest, size, received: 0n}, limit: 16777216n};
    },
    async append(handle, offset, data) {
      calls.push(['append', handle, offset, data.length]);
      staged.push(Buffer.from(data));
      return {outcome: 'accepted', received: offset + BigInt(data.length)};
    },
    async commit(handle) {
      calls.push(['commit', handle]);
      const bytes = Buffer.concat(staged);
      staged = [];
      if (sha(bytes) !== declared.digest) return {outcome: 'mismatch', received: 0n};
      return {outcome: 'committed', stored: {digest: declared.digest, size: declared.size, evidence: 'hashed'}, received: 0n};
    },
    async abort(handle) { calls.push(['abort', handle]); return {outcome: 'aborted'}; },
    ...overrides,
  };
}

function fakeTranscription(calls, {admission = {outcome: 'accepted', operation: 'op'}, deltas = null, observe = null} = {}) {
  return {
    async start(request) { calls.push(['start', request]); return admission; },
    observe: observe ?? (async (operation, cursor) => {
      calls.push(['observe', operation, cursor]);
      return {outcome: 'page', next: 2n, atEnd: true, deltas: deltas ?? [
        {sequence: 0n, kind: 'segment', segment: {index: 0n, kind: 'segment', startMs: 0n, endMs: 3200n, text: ' Add a test for the parser. '}},
        {sequence: 1n, kind: 'end', transcriptionEnd: {outcome: 'completed', reason: '', language: 'en', durationMs: 3200n,
          host: 'lemonade', model: 'Whisper-Large-v3-Turbo'}},
      ]};
    }),
    async cancel(operation) { calls.push(['cancel', operation]); return {outcome: 'cancelled'}; },
  };
}

function listener({calls = [], capture, writer, transcription, toasts = [], prompts = [], states = [], timers = {}} = {}) {
  const value = createListener({
    capture: capture ?? fakeCapture({calls}),
    resolveWriter: async () => writer ?? fakeWriter(calls),
    resolveTranscription: async () => transcription ?? fakeTranscription(calls),
    appendPrompt: async (text) => { prompts.push(text); return {data: true}; },
    toast: (input) => toasts.push(input),
    onChange: (state) => states.push(state.phase),
    setInterval: () => 1, clearInterval: () => {}, setTimeout: () => 2, clearTimeout: () => {},
    ...timers,
  });
  return {listener: value, calls, toasts, prompts, states};
}

test('the WAV header describes 16 kHz mono PCM16 around the recorded bytes', () => {
  const pcm = speech(10);
  const wav = Buffer.from(wavFromPcm16(pcm));
  assert.equal(wav.length, 54);
  assert.equal(wav.toString('ascii', 0, 4), 'RIFF');
  assert.equal(wav.readUInt32LE(4), 46);
  assert.equal(wav.toString('ascii', 8, 16), 'WAVEfmt ');
  assert.deepEqual([wav.readUInt32LE(16), wav.readUInt16LE(20), wav.readUInt16LE(22), wav.readUInt32LE(24),
    wav.readUInt32LE(28), wav.readUInt16LE(32), wav.readUInt16LE(34)], [16, 1, 1, 16000, 32000, 2, 16]);
  assert.equal(wav.toString('ascii', 36, 40), 'data');
  assert.equal(wav.readUInt32LE(40), 10);
  assert.deepEqual(wav.subarray(44), Buffer.from(pcm));
});

test('the prompt-slot indicator counts listening time and caps it at 30 s', () => {
  assert.equal(indicatorText({phase: 'listening', elapsedMs: 3400}), '● listening 0:03');
  assert.equal(indicatorText({phase: 'listening', elapsedMs: 31000}), '● listening 0:30');
  assert.equal(indicatorText({phase: 'transcribing', elapsedMs: 0}), '● transcribing');
  assert.equal(indicatorText({phase: 'idle', elapsedMs: 0}), '');
});

test('two presses record, upload the WAV, transcribe locally and append the text to the prompt', async () => {
  const {listener: value, calls, toasts, prompts, states} = listener();
  await value.toggle();
  assert.equal(value.state().phase, 'listening');
  await value.toggle();
  assert.deepEqual(prompts, ['Add a test for the parser.']);
  assert.deepEqual(states, ['listening', 'transcribing', 'idle']);
  assert.equal(toasts.at(-1).variant, 'success');
  const begin = calls.find((call) => call[0] === 'begin');
  const wav = wavFromPcm16(speech());
  assert.equal(begin[2], sha(wav));
  assert.equal(begin[3], BigInt(wav.length));
  assert.match(begin[1], /^[A-Za-z0-9_-]{16,128}$/);
  const start = calls.find((call) => call[0] === 'start')[1];
  assert.deepEqual(start, {model: 'Whisper-Large-v3-Turbo', audioDigest: sha(wav), mediaType: 'audio/wav', language: '',
    timestamps: 'none', guarantees: ['abstraction.inference/local-only@1'], credential: '', extensions: {}});
  assert.deepEqual(calls.filter((call) => call[0] === 'cancel'), []);
  assert.deepEqual(calls.map((call) => Array.isArray(call) ? call[0] : call),
    ['open', 'stop', 'begin', 'append', 'commit', 'start', 'observe']);
});

test('a recording over 64 KiB uploads in ordered 64 KiB appends', async () => {
  const calls = [];
  const bytes = wavFromPcm16(speech(150000));
  const digest = await uploadContent(fakeWriter(calls), bytes, 'request-identity-0001');
  assert.equal(digest, sha(bytes));
  assert.deepEqual(calls.filter((call) => call[0] === 'append').map((call) => [call[2], call[3]]),
    [[0n, 65536], [65536n, 65536], [131072n, bytes.length - 131072]]);
});

test('an upload the store already holds is not sent again', async () => {
  const calls = [];
  const bytes = wavFromPcm16(speech());
  const writer = fakeWriter(calls, {async begin(request, digest, size) {
    calls.push('begin');
    return {outcome: 'present', stored: {digest, size, evidence: 'named'}, limit: 16777216n};
  }});
  assert.equal(await uploadContent(writer, bytes), sha(bytes));
  assert.deepEqual(calls, ['begin']);
});

test('microphone refusals become one sentence each and leave the listener idle', async () => {
  for (const [code, pattern] of [['no_microphone', /No microphone/], ['microphone_busy', /Another program/],
    ['unsupported_platform', /Windows only/], ['capture_failed', /could not be opened/]]) {
    const {listener: value, toasts, calls} = listener({capture: fakeCapture({error: new CaptureError(code, 'x')})});
    await value.toggle();
    assert.equal(value.state().phase, 'idle');
    assert.equal(toasts.length, 1);
    assert.match(toasts[0].message, pattern);
    assert.equal(toasts[0].message.split('. ').length, 1);
    assert.deepEqual(calls.filter((call) => Array.isArray(call)), []);
  }
});

test('silence from Windows names the microphone privacy setting and uploads nothing', async () => {
  const calls = [];
  const {listener: value, toasts} = listener({calls, capture: fakeCapture({pcm: new Uint8Array(3200), calls})});
  await value.toggle();
  await value.toggle();
  assert.match(toasts.at(-1).message, /Privacy & security, Microphone/);
  assert.equal(calls.some((call) => Array.isArray(call)), false);
  assert.equal(value.state().phase, 'idle');
});

test('OA refusals of the upload and the transcription read as one toast each', async () => {
  const cases = [
    // The upload is request input under input:audio (storage STO-W12); a
    // refusal at Begin or at publication names that one grant.
    [{writer: fakeWriter([], {async begin() { return {outcome: 'forbidden', limit: 0n}; }})}, /store recordings; allow OpenCode content write and read on input:audio/],
    [{writer: fakeWriter([], {async commit() { return {outcome: 'forbidden', received: 0n}; }})}, /store recordings; allow OpenCode content write and read on input:audio/],
    [{transcription: fakeTranscription([], {admission: {outcome: 'forbidden', reason: 'content:read:forbidden', operation: ''}})}, /read the recording; allow OpenCode content write and read on input:audio/],
    [{transcription: fakeTranscription([], {admission: {outcome: 'not_permitted', reason: 'rights:ask', operation: ''}})}, /question for OpenCode/],
    [{transcription: fakeTranscription([], {admission: {outcome: 'no_host', reason: 'router:none', operation: ''}})}, /No local speech engine/],
    [{transcription: fakeTranscription([], {admission: {outcome: 'unsupported_feature', reason: 'feature:transcription', operation: ''}})}, /No local speech engine/],
    [{transcription: fakeTranscription([], {deltas: [{sequence: 0n, kind: 'end', transcriptionEnd: {outcome: 'unavailable',
      reason: 'upstream:503', language: '', durationMs: 0n, host: 'lemonade', model: 'Whisper-Large-v3-Turbo'}}]})}, /No local speech engine/],
  ];
  for (const [overrides, pattern] of cases) {
    const {listener: value, toasts, prompts} = listener(overrides);
    await value.toggle();
    await value.toggle();
    assert.match(toasts.at(-1).message, pattern);
    assert.deepEqual(prompts, []);
    assert.equal(value.state().phase, 'idle');
  }
});

test('an interrupted observation cancels the transcription operation', async () => {
  const calls = [];
  const transcription = fakeTranscription(calls, {observe: async () => { throw new Error('pipe closed'); }});
  await assert.rejects(transcribe(transcription, sha(Buffer.from('x'))), /pipe closed/);
  assert.deepEqual(calls.at(-1), ['cancel', 'op']);
});

test('a failed append aborts the staged upload', async () => {
  const calls = [];
  const writer = fakeWriter(calls, {async append() { return {outcome: 'gap', received: 0n}; }});
  await assert.rejects(uploadContent(writer, wavFromPcm16(speech())), /stopped the recording upload/);
  assert.deepEqual(calls.at(-1), ['abort', 'u1']);
});

test('the 30-second limit stops and transcribes without a second press', async () => {
  let limit;
  let delay;
  const {listener: value, prompts} = listener({timers: {setTimeout: (fn, ms) => { limit = fn; delay = ms; return 3; }}});
  await value.toggle();
  assert.equal(delay, maxListenMs);
  limit();
  while (value.state().phase !== 'idle') await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(prompts, ['Add a test for the parser.']);
});

test('a press while transcribing says so and starts nothing', async () => {
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  const calls = [];
  const capture = () => ({elapsedMs: () => 0, async stop() { await gate; return speech(); }});
  const {listener: value, toasts} = listener({calls, capture});
  await value.toggle();
  const first = value.toggle();
  await new Promise((resolve) => setImmediate(resolve));
  await value.toggle();
  assert.match(toasts[0].message, /Still transcribing/);
  release();
  await first;
});

test('both requests pass the generated wire serializers before dispatch', async () => {
  let reached = 0;
  const transport = {async exchangeFrame() { reached++; throw new Error('transport reached'); }};
  await assert.rejects(transcribe(new TranscriptionClient(transport), sha(Buffer.from('x'))), /transport reached/);
  await assert.rejects(uploadContent(new ContentWriterClient(transport), wavFromPcm16(speech())), /transport reached/);
  assert.equal(reached, 2);
});

test('the TUI module registers /listen on Ctrl+Alt+M and closes the device on dispose', async () => {
  const layers = [];
  const disposers = [];
  const calls = [];
  const shown = [];
  const api = {
    keymap: {registerLayer(layer) { layers.push(layer); return () => calls.push('off'); }},
    client: {tui: {async appendPrompt(input) { calls.push(['append', input]); return {data: true}; }}},
    ui: {toast(input) { calls.push(['toast', input.variant]); }},
    lifecycle: {onDispose(fn) { disposers.push(fn); return () => {}; }},
  };
  await registerListen(api, {}, {
    indicator: () => (state) => shown.push(indicatorText(state)),
    capture: fakeCapture({calls}),
    resolveWriter: async () => fakeWriter(calls),
    resolveTranscription: async () => fakeTranscription(calls),
    timers: {setInterval: () => 1, clearInterval: () => {}, setTimeout: () => 2, clearTimeout: () => {}},
  });
  assert.equal(layers.length, 1);
  const [command] = layers[0].commands;
  assert.deepEqual({name: command.name, slash: command.slashName, namespace: command.namespace},
    {name: listenCommand, slash: 'listen', namespace: 'palette'});
  assert.deepEqual(layers[0].bindings, [{key: 'ctrl+alt+m', cmd: listenCommand, desc: 'Listen'}]);
  command.run();
  assert.deepEqual(shown, ['● listening 0:00']);
  await disposers[0]();
  assert.ok(calls.includes('stop'));
  assert.ok(calls.includes('off'));
  assert.equal(calls.some((call) => Array.isArray(call) && call[0] === 'append'), false);
});

test('without a prompt-slot indicator the listen command refuses to record and says why', async () => {
  const layers = [];
  const toasts = [];
  const calls = [];
  const api = {
    keymap: {registerLayer(layer) { layers.push(layer); return () => {}; }},
    client: {tui: {async appendPrompt() { return {data: true}; }}},
    ui: {toast(input) { toasts.push(input.message); }},
  };
  await registerListen(api, {keybind: 'ctrl+shift+l'}, {
    indicator: async () => { throw new Error('no Solid runtime'); },
    capture: fakeCapture({calls}),
    resolveWriter: async () => fakeWriter([]),
    resolveTranscription: async () => fakeTranscription([]),
    timers: {setInterval: () => 1, clearInterval: () => {}, setTimeout: () => 2, clearTimeout: () => {}},
  });
  assert.equal(layers[0].bindings[0].key, 'ctrl+shift+l');
  layers[0].commands[0].run();
  assert.equal(toasts.length, 1);
  assert.match(toasts[0], /recording indicator/);
  assert.deepEqual(calls, []);
});

test('a recording with dropped audio says so once in the final toast', async () => {
  const capture = () => ({elapsedMs: () => 0, dropped: () => 2, async stop() { return speech(); }});
  const {listener: value, toasts, prompts} = listener({capture});
  await value.toggle();
  await value.toggle();
  assert.deepEqual(prompts, ['Add a test for the parser.']);
  assert.equal(toasts.length, 1);
  assert.equal(toasts[0].variant, 'warning');
  assert.match(toasts[0].message, /part of the recording was lost/);
});

test('an upload that never advances on out_of_order stops after a bounded number of tries', async () => {
  const calls = [];
  const writer = fakeWriter(calls, {async append(handle, offset) { calls.push('append'); return {outcome: 'out_of_order', received: offset}; }});
  await assert.rejects(uploadContent(writer, wavFromPcm16(speech())), /stopped advancing/);
  assert.ok(calls.filter((call) => call === 'append').length <= 4);
  assert.deepEqual(calls.at(-1), ['abort', 'u1']);
});

test('a transcription past the 120-second limit is cancelled with one toast sentence', async () => {
  const calls = [];
  let clock = 0;
  const transcription = fakeTranscription(calls, {observe: async (operation, cursor, maxDeltas, maxBytes, waitMs) => {
    calls.push(['observe', waitMs]);
    clock += 60000;
    return {outcome: 'page', next: cursor, atEnd: false, deltas: []};
  }});
  await assert.rejects(transcribe(transcription, sha(Buffer.from('x')), {now: () => clock}),
    (error) => /longer than 2 minutes/.test(error.message) && error.name === 'ListenRefusal');
  assert.deepEqual(calls.at(-1), ['cancel', 'op']);
  assert.ok(calls.filter((call) => call[0] === 'observe').length <= 3);
});

test('the package exports the TUI module beside the server plugin', async () => {
  const pkg = JSON.parse(await readFile(new URL('../package.json', import.meta.url), 'utf8'));
  assert.deepEqual(pkg.exports['./tui'], {default: './tui.js'});
  for (const file of ['tui.js', 'listen.js', 'winmm-capture.js']) assert.ok(pkg.files.includes(file), file);
  assert.equal(tuiPlugin.id, 'openabstractions');
  assert.equal(typeof tuiPlugin.tui, 'function');
});

/** A winmm stand-in: ptr() hands out addresses, waveInAddBuffer queues a
 * header, and record() fills queued buffers the way the driver does. */
function fakeWinmm({devices = 1, open = 0} = {}) {
  const memory = new Map();
  const queue = [];
  const calls = [];
  let next = 0x10000;
  const ffi = {
    ptr(array) { const address = next; next += 0x10000; memory.set(address, array); return address; },
    dlopen(path, symbols) {
      calls.push(['dlopen', Object.keys(symbols)]);
      return {close() { calls.push('close-library'); }, symbols: {
        waveInGetNumDevs: () => devices,
        waveInOpen(out, device, format) {
          calls.push(['open', device, Buffer.from(format).toString('hex')]);
          if (open) return open;
          out[0] = 0x77n;
          return 0;
        },
        waveInPrepareHeader: (handle, header) => { calls.push('prepare'); return 0; },
        waveInUnprepareHeader: () => { calls.push('unprepare'); return 0; },
        waveInAddBuffer: (handle, header) => { queue.push(header); return 0; },
        waveInStart: () => { calls.push('start'); return 0; },
        waveInReset: () => {
          calls.push('reset');
          for (const header of queue.splice(0)) new DataView(header.buffer).setUint32(24, 3, true);
          return 0;
        },
        waveInClose: () => { calls.push('close'); return 0; },
      }};
    },
  };
  const record = (bytes) => {
    const header = queue.shift();
    const view = new DataView(header.buffer);
    memory.get(Number(view.getBigUint64(0, true))).set(bytes);
    view.setUint32(12, bytes.length, true);
    view.setUint32(24, 3, true);
  };
  return {ffi, calls, record};
}

test('winmm capture opens 16 kHz mono PCM16, cycles its buffers and closes the device', async () => {
  const winmm = fakeWinmm();
  let poll;
  const capture = startCapture({ffi: winmm.ffi, library: 'winmm.dll', platform: 'win32',
    setInterval: (fn) => { poll = fn; return 1; }, clearInterval: () => {}});
  const open = winmm.calls.find((call) => call[0] === 'open');
  assert.equal(open[1], 0xFFFFFFFF);
  assert.equal(open[2], '01000100803e0000007d0000020010000000');
  winmm.record(new Uint8Array([1, 2, 3, 4]));
  poll();
  winmm.record(new Uint8Array([5, 6]));
  const pcm = await capture.stop();
  assert.deepEqual([...pcm], [1, 2, 3, 4, 5, 6]);
  assert.deepEqual(winmm.calls.filter((call) => typeof call === 'string'),
    [...Array(8).fill('prepare'), 'start', 'reset', 'reset', ...Array(8).fill('unprepare'), 'close', 'close-library']);
});

test('winmm capture keeps driver order when two buffers finish in one poll across the wrap', async () => {
  const winmm = fakeWinmm();
  let poll;
  const capture = startCapture({ffi: winmm.ffi, library: 'winmm.dll', platform: 'win32',
    setInterval: (fn) => { poll = fn; return 1; }, clearInterval: () => {}});
  for (let i = 0; i < 6; i++) winmm.record(new Uint8Array(0));
  poll();
  winmm.record(new Uint8Array([1]));
  poll();
  winmm.record(new Uint8Array([2]));
  winmm.record(new Uint8Array([3]));
  poll();
  winmm.record(new Uint8Array([4]));
  winmm.record(new Uint8Array([5]));
  const pcm = await capture.stop();
  assert.deepEqual([...pcm], [1, 2, 3, 4, 5]);
  assert.equal(capture.dropped(), 0);
});

test('winmm capture counts a poll that finds every buffer done as a drop', async () => {
  const winmm = fakeWinmm();
  let poll;
  const capture = startCapture({ffi: winmm.ffi, library: 'winmm.dll', platform: 'win32',
    setInterval: (fn) => { poll = fn; return 1; }, clearInterval: () => {}});
  for (let i = 0; i < 8; i++) winmm.record(new Uint8Array([i]));
  poll();
  winmm.record(new Uint8Array([8]));
  poll();
  assert.deepEqual([...await capture.stop()], [0, 1, 2, 3, 4, 5, 6, 7, 8]);
  assert.equal(capture.dropped(), 1);
});

test('winmm refusals name the missing, busy or unsupported microphone and release the library', () => {
  const none = fakeWinmm({devices: 0});
  assert.throws(() => startCapture({ffi: none.ffi, platform: 'win32'}), (error) => error.code === 'no_microphone');
  assert.equal(none.calls.at(-1), 'close-library');
  const busy = fakeWinmm({open: 4});
  assert.throws(() => startCapture({ffi: busy.ffi, platform: 'win32'}), (error) => error.code === 'microphone_busy');
  const missing = fakeWinmm({open: 2});
  assert.throws(() => startCapture({ffi: missing.ffi, platform: 'win32'}), (error) => error.code === 'no_microphone');
  assert.throws(() => startCapture({ffi: none.ffi, platform: 'linux'}), (error) => error.code === 'unsupported_platform');
  assert.deepEqual(Object.keys(winmmSymbols).sort(), ['waveInAddBuffer', 'waveInClose', 'waveInGetNumDevs', 'waveInOpen',
    'waveInPrepareHeader', 'waveInReset', 'waveInStart', 'waveInUnprepareHeader']);
});
