import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {readFileSync} from 'node:fs';
import test from 'node:test';
import OpenAbstractionsPlugin from '../plugin.js';
import {createSpeakTool, installSpeak, spokenText, synthesizeSpeech} from '../speak.js';
import tuiModule, {installTui, tuiFeatures} from '../tui.js';
import {SpeechClient} from '@openabstractions/inference/protocol';

// A 16-bit mono 24 kHz WAV of four samples, as the provider writes it.
const wav = Buffer.concat([Buffer.from('RIFF\x2c\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\xc0\x5d\x00\x00\x80\xbb\x00\x00\x02\x00\x10\x00data\x08\x00\x00\x00', 'latin1'),
  Buffer.from([1, 0, 2, 0, 3, 0, 4, 0])]);
const digest = `sha256:${createHash('sha256').update(wav).digest('hex')}`;
const delivery = {location: 'local', digest, mediaType: 'audio/wav', size: BigInt(wav.length)};

function fakeTool(definition) { return definition; }
const schema = {optional() { return this; }};
fakeTool.schema = {string: () => schema, number: () => schema, boolean: () => schema,
  enum: () => schema, array: () => schema, object: () => schema};

function services(overrides = {}) {
  const calls = [];
  const speech = {
    async start(request) { calls.push(['start', request]); return {outcome: 'accepted', operation: 'op'}; },
    async observe() {
      calls.push('observe');
      return {outcome: 'page', next: 2n, atEnd: true, deltas: [
        {kind: 'audio', audio: {index: 0n, data: wav}},
        {kind: 'end', speechEnd: {outcome: 'completed', host: 'lemonade-image', model: 'kokoro-v1', delivery, characters: 5n}},
      ]};
    },
    async cancel(operation) { calls.push(['cancel', operation]); },
    ...overrides.speech,
  };
  const reader = {
    async open(value) { calls.push(['open', value]); return {outcome: 'opened', resource: {handle: 'h', digest, size: BigInt(wav.length)}}; },
    async read() { calls.push('read'); return {outcome: 'data', chunk: {offset: 0n, total: BigInt(wav.length), data: wav, eof: true}}; },
    async close() { calls.push('close'); },
    ...overrides.reader,
  };
  return {speech, reader, calls};
}

test('spoken text leaves out code blocks, inline code, paths and links, and keeps 600 characters', () => {
  const reply = [
    '## Done', 'I fixed the **failing** test in `server/speech.go` and C:\\Users\\me\\repo\\main.go.',
    '```go', 'func secret() {}', '```', 'See ./docs/README.md and https://example.com/x for [the guide](https://example.com/guide).',
    'Ready.\u2705\uFE0F',
  ].join('\n');
  const spoken = spokenText(reply);
  for (const gone of ['func secret', 'speech.go', 'main.go', 'README', 'https', '```', '`', '**', '##', '\uFE0F']) {
    assert.equal(spoken.includes(gone), false, `${gone} is spoken: ${spoken}`);
  }
  assert.match(spoken, /I fixed the failing test/);
  assert.match(spoken, /the guide/);
  assert.equal(spokenText('cafe\u0301'), 'café');
  const long = spokenText('word '.repeat(400));
  assert.ok(Array.from(long).length <= 600 && long.endsWith('word'), long.slice(-20));
  assert.equal(spokenText('```\nonly code\n```'), '');
  assert.equal(spokenText('tab\there\u0007bell'), 'tab here bell');
});

test('speech reaches OA with kokoro-v1, af_sky and wav, and the delivery is read back and verified', async () => {
  const {speech, reader, calls} = services();
  const result = await synthesizeSpeech({speech, reader, text: 'Ready.'});
  assert.deepEqual(result.bytes, wav);
  assert.equal(result.delivery.digest, digest);
  const request = calls.find((call) => Array.isArray(call) && call[0] === 'start')[1];
  assert.deepEqual({model: request.model, voice: request.voice, text: request.text, format: request.format, guarantees: request.guarantees, credential: request.credential},
    {model: 'kokoro-v1', voice: 'af_sky', text: 'Ready.', format: 'wav', guarantees: [], credential: ''});
  assert.equal(calls.at(-1), 'close');
  assert.equal(calls.some((call) => Array.isArray(call) && call[0] === 'cancel'), false);
});

test('altered or remote speech content is refused and the reader closed', async () => {
  const altered = services({reader: {async read() { return {outcome: 'data', chunk: {
    offset: 0n, total: BigInt(wav.length), data: Buffer.concat([wav.subarray(0, -1), Buffer.from([9])]), eof: true}}; }}});
  await assert.rejects(synthesizeSpeech({speech: altered.speech, reader: altered.reader, text: 'Ready.'}), /digest mismatch/);
  assert.equal(altered.calls.at(-1), 'close');
  const remote = services({speech: {async observe() { return {outcome: 'page', next: 1n, atEnd: true, deltas: [
    {kind: 'end', speechEnd: {outcome: 'completed', host: 'h', model: 'kokoro-v1', delivery: {...delivery, location: 'peer'}}}]}; }}});
  await assert.rejects(synthesizeSpeech({speech: remote.speech, reader: remote.reader, text: 'Ready.'}), /unsupported/);
  assert.equal(remote.calls.includes('read'), false);
});

test('a refusal is reported, an interrupted operation is cancelled', async () => {
  const refused = services({speech: {async start() { return {outcome: 'unsupported_feature', reason: 'voice:alloy'}; }}});
  const result = await synthesizeSpeech({speech: refused.speech, reader: refused.reader, text: 'Ready.'});
  assert.match(result.refused, /unsupported_feature.*voice:alloy/);
  const controller = new AbortController();
  const interrupted = services({speech: {async observe() { controller.abort(); return {outcome: 'page', next: 0n, atEnd: false, deltas: []}; }}});
  const cancelled = await synthesizeSpeech({speech: interrupted.speech, reader: interrupted.reader, text: 'Ready.', signal: controller.signal});
  assert.equal(cancelled.cancelled, true);
  assert.deepEqual(interrupted.calls.find((call) => Array.isArray(call) && call[0] === 'cancel'), ['cancel', 'op']);
});

test('the speech request passes the generated wire serializer before dispatch', async () => {
  let encoded = false;
  const client = new SpeechClient({async exchangeFrame() { encoded = true; throw new Error('transport reached'); }});
  await assert.rejects(synthesizeSpeech({speech: client, reader: null, text: 'Ready.'}), /transport reached/);
  assert.equal(encoded, true);
});

test('the speak tool asks per call before any speech work and returns the verified delivery', async () => {
  const {speech, reader, calls} = services();
  const tool = createSpeakTool({tool: fakeTool, resolveSpeech: async () => { calls.push('resolve'); return speech; }, resolveReader: async () => reader});
  const asked = [];
  const result = await tool.execute({text: 'Read me the failing test names.'}, {ask: async (request) => { asked.push(request); calls.push('ask'); }});
  assert.equal(asked[0].permission, 'openabstractions_speak');
  assert.deepEqual(asked[0].always, []);
  assert.match(asked[0].patterns[0], /Read 31 characters aloud with OA kokoro-v1 voice af_sky/);
  assert.equal(calls[0], 'ask');
  assert.equal(result.metadata.digest, digest);
  assert.equal(result.metadata.mediaType, 'audio/wav');
  assert.equal(result.attachments, undefined);
  const refused = services();
  const strict = createSpeakTool({tool: fakeTool, resolveSpeech: async () => { refused.calls.push('resolve'); return refused.speech; }, resolveReader: async () => refused.reader});
  for (const args of [{}, {text: ''}, {text: 'x'.repeat(4097)}, {text: 'hi', voice: '../x'}, {text: 'hi', extra: 1}]) {
    await assert.rejects(strict.execute(args, {ask: async () => {}}), /Provide text/);
  }
  assert.deepEqual(refused.calls, []);
  assert.match(await strict.execute({text: 'hi'}, {}), /needs an available approval prompt/);
});

function fakeTui({enabled = false, messages = [], parts = {}} = {}) {
  const handlers = {};
  const store = new Map([['openabstractions.speak_replies', enabled]]);
  const disposers = [];
  const toasts = [];
  let layer;
  const api = {
    kv: {ready: true, get: (key, fallback) => store.has(key) ? store.get(key) : fallback, set: (key, value) => store.set(key, value)},
    keymap: {registerLayer(value) { layer = value; return () => { layer = undefined; }; }},
    event: {on(type, handler) { handlers[type] = handler; return () => { delete handlers[type]; }; }},
    state: {session: {messages: () => messages}, part: (id) => parts[id] ?? []},
    ui: {toast: (toast) => toasts.push(toast)},
    lifecycle: {signal: new AbortController().signal, onDispose(fn) { disposers.push(fn); return () => {}; }},
  };
  return {api, handlers, store, toasts, disposers, layer: () => layer};
}

function fakeAudio() {
  const log = [];
  return {log, audio: {
    loadSound(bytes) { log.push(['load', Buffer.from(bytes)]); return {sound: 1}; },
    isStarted() { return log.some((e) => e === 'start'); },
    start() { log.push('start'); return true; },
    play(sound) { log.push(['play', sound]); return {voice: 1}; },
    stopVoice() { log.push('stop'); return true; },
    dispose() { log.push('dispose'); },
  }};
}

test('Speak replies speaks the last answer on session.idle and plays it through OpenTUI audio', async () => {
  const messages = [{id: 'u1', role: 'user'}, {id: 'a1', role: 'assistant'}];
  const parts = {a1: [{type: 'reasoning', text: 'thinking'}, {type: 'text', text: 'All tests pass in `go/server`.'}, {type: 'text', text: 'Hidden.', synthetic: true}]};
  const tui = fakeTui({messages, parts});
  const {speech, reader, calls} = services();
  const {audio, log} = fakeAudio();
  const spoken = [];
  await installSpeak(tui.api, {}, {resolveSpeech: async () => speech, resolveReader: async () => reader, audio: () => audio,
    onSpoken: (text) => spoken.push(text)});
  const command = tui.layer().commands.find((c) => c.name === 'openabstractions.speak');
  assert.equal(command.slashName, 'speak');
  await tui.handlers['session.idle']({type: 'session.idle', properties: {sessionID: 's'}});
  assert.equal(calls.length, 0, 'speaks while the setting is off');
  await command.run();
  assert.equal(tui.store.get('openabstractions.speak_replies'), true);
  assert.match(tui.toasts.at(-1).message, /on/);
  await tui.handlers['session.idle']({type: 'session.idle', properties: {sessionID: 's'}});
  assert.deepEqual(spoken, ['All tests pass in.']);
  assert.deepEqual(log.find((e) => Array.isArray(e) && e[0] === 'load')[1], wav);
  assert.ok(log.some((e) => Array.isArray(e) && e[0] === 'play'));
  for (const dispose of tui.disposers) await dispose();
  assert.equal(log.at(-1), 'dispose');
});

test('a completed openabstractions_speak tool part plays its verified delivery once', async () => {
  const tui = fakeTui();
  const {speech, reader, calls} = services();
  const {audio, log} = fakeAudio();
  await installSpeak(tui.api, {}, {resolveSpeech: async () => speech, resolveReader: async () => reader, audio: () => audio});
  const part = {id: 'p1', type: 'tool', tool: 'openabstractions_speak', state: {status: 'completed',
    metadata: {digest, mediaType: 'audio/wav', bytes: wav.length}}};
  await tui.handlers['message.part.updated']({type: 'message.part.updated', properties: {sessionID: 's', part}});
  await tui.handlers['message.part.updated']({type: 'message.part.updated', properties: {sessionID: 's', part}});
  assert.equal(log.filter((e) => Array.isArray(e) && e[0] === 'play').length, 1);
  assert.equal(calls.some((call) => Array.isArray(call) && call[0] === 'start'), false, 'the TUI started a second synthesis');
  await tui.handlers['message.part.updated']({type: 'message.part.updated', properties: {sessionID: 's',
    part: {...part, id: 'p2', tool: 'bash'}}});
  assert.equal(log.filter((e) => Array.isArray(e) && e[0] === 'play').length, 1);
});

test('one TUI module installs dictation and Speak replies, and one failing leaves the other', async () => {
  const installed = [];
  const toasts = [];
  const api = {ui: {toast: (toast) => toasts.push(toast)}};
  await installTui(api, {keybind: 'ctrl+alt+m'}, {
    listen: async () => async (_api, options) => { installed.push(['listen', options.keybind]); throw new Error('no indicator'); },
    speak: async () => async (_api, options) => { installed.push(['speak', options.keybind]); },
  });
  assert.deepEqual(installed, [['listen', 'ctrl+alt+m'], ['speak', 'ctrl+alt+m']]);
  assert.match(toasts[0].message, /listen is unavailable: no indicator/);
  const tui = fakeTui();
  const names = [];
  tui.api.keymap.registerLayer = (layer) => { names.push(...layer.commands.map((c) => c.name)); return () => {}; };
  await installTui(tui.api, {}, {listen: async () => async (a) => a.keymap.registerLayer({commands: [{name: 'openabstractions.listen'}]}),
    speak: async () => async (a, o) => installSpeak(a, o, {audio: () => fakeAudio().audio})});
  assert.deepEqual(names, ['openabstractions.listen', 'openabstractions.speak']);
});

test('a Speak replies module that fails to import leaves dictation installed', async () => {
  const installed = [];
  const toasts = [];
  await installTui({ui: {toast: (toast) => toasts.push(toast)}}, {}, {
    speak: async () => (await import('../speak-module-that-does-not-load.js')).installSpeak,
    listen: async () => async () => { installed.push('listen'); },
  });
  assert.deepEqual(installed, ['listen']);
  assert.equal(toasts.length, 1);
  assert.match(toasts[0].message, /speak is unavailable/);
  // The shipped module imports each feature inside its own attempt.
  const source = readFileSync(new URL('../tui.js', import.meta.url), 'utf8');
  assert.doesNotMatch(source, /^import .* from '\.\/(listen|speak)\.js'/m);
  assert.deepEqual(Object.keys(tuiFeatures), ['listen', 'speak', 'where', 'models']);
  const speakSource = readFileSync(new URL('../speak.js', import.meta.url), 'utf8');
  assert.doesNotMatch(speakSource, /^import .* from '@openabstractions\/inference/m);
});

test('the TUI module and the registered speak tool are wired to the configured runtime', async () => {
  assert.equal(typeof tuiModule.tui, 'function');
  assert.equal(tuiModule.server, undefined);
  const resolved = [];
  const plugin = await OpenAbstractionsPlugin({client: {}}, {providerId: 'oa-fixture', tools: {speak: true}}, {
    tool: fakeTool, readInventory: async () => ({offers: []}),
    resolveSpeechClient: async (options) => { resolved.push(options.runtimeEndpoint); throw new Error('stop after resolution'); },
  });
  await plugin.config({provider: {'oa-fixture': {
    npm: '@openabstractions/opencode', options: {runtimeEndpoint: 'fixture-runtime', scope: 'local'}, models: {},
  }}});
  await assert.rejects(plugin.tool.openabstractions_speak.execute({text: 'Hello'}, {ask: async () => {}}), /stop after resolution/);
  assert.deepEqual(resolved, ['fixture-runtime']);
});
