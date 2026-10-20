import {createHash, randomUUID} from 'node:crypto';
import {CaptureError, bitsPerSample, channels, sampleRate, startCapture} from './winmm-capture.js';

export const listenCommand = 'openabstractions.listen';
export const listenSlash = 'listen';
export const listenKey = 'ctrl+alt+m';
export const maxListenMs = 30000;
export const transcriptionModel = 'Whisper-Large-v3-Turbo';
const writerContract = 'abstraction.storage/content-writer@1';
const transcriptionContract = 'abstraction.inference/transcription@1';
const localOnly = 'abstraction.inference/local-only@1';
const appendBytes = 65536;
const maxStalledAppends = 3;
const tickMs = 250;
/** Transcription of at most 30 s of audio that takes longer is cancelled. */
export const maxTranscriptionMs = 120000;
const observeWaitMs = 25000;

/** A refusal shown as one toast sentence; `variant` is the toast variant. */
export class ListenRefusal extends Error {
  constructor(message, variant = 'error') {
    super(message);
    this.name = 'ListenRefusal';
    this.variant = variant;
  }
}

/**
 * The grant a recording needs: OA stores it and reads it back for OpenCode's own
 * transcription under the input:audio profile, with content write and read on
 * that one resource (storage STO-W12, inference INF-V4). Refusals name it.
 */
export const inputRights = {write: 'abstraction.storage/content.write', read: 'abstraction.storage/content.read', resource: 'input:audio'};
const inputGrant = `content write and read on ${inputRights.resource}`;
const panelQuestion = 'OpenAbstractions has a question for OpenCode waiting in the Panel; answer it there and listen again.';
const storeRefusal = `OpenAbstractions does not let OpenCode store recordings; allow OpenCode ${inputGrant} in the OpenAbstractions Panel and listen again.`;
const engineMissing = `No local speech engine is ready; install ${transcriptionModel} in OpenAbstractions Setup.`;

/** `● listening 0:03` while recording, `● transcribing` while OA works, '' when idle. */
export function indicatorText(state) {
  if (state.phase === 'listening') {
    const seconds = Math.min(Math.floor(state.elapsedMs / 1000), maxListenMs / 1000);
    return `● listening ${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
  }
  if (state.phase === 'transcribing') return '● transcribing';
  return '';
}

/** A canonical 44-byte-header RIFF WAVE file around 16 kHz mono PCM16 bytes. */
export function wavFromPcm16(pcm) {
  const blockAlign = channels * bitsPerSample / 8;
  const wav = new Uint8Array(44 + pcm.length);
  const view = new DataView(wav.buffer);
  const ascii = (offset, text) => { for (let i = 0; i < text.length; i++) wav[offset + i] = text.charCodeAt(i); };
  ascii(0, 'RIFF');
  view.setUint32(4, 36 + pcm.length, true);
  ascii(8, 'WAVE');
  ascii(12, 'fmt ');
  view.setUint32(16, 16, true);
  view.setUint16(20, 1, true);
  view.setUint16(22, channels, true);
  view.setUint32(24, sampleRate, true);
  view.setUint32(28, sampleRate * blockAlign, true);
  view.setUint16(32, blockAlign, true);
  view.setUint16(34, bitsPerSample, true);
  ascii(36, 'data');
  view.setUint32(40, pcm.length, true);
  wav.set(pcm, 44);
  return wav;
}

function captureRefusal(error) {
  if (!(error instanceof CaptureError)) return null;
  switch (error.code) {
    case 'unsupported_platform': return new ListenRefusal('Listening is available in OpenCode on Windows only.');
    case 'no_microphone': return new ListenRefusal('No microphone is connected to this computer.');
    case 'microphone_busy': return new ListenRefusal('Another program is using the microphone.');
    default: return new ListenRefusal('The microphone could not be opened.');
  }
}

/** Upload bytes through content-writer@1 under the caller's own identity; resolves to the digest. */
export async function uploadContent(writer, bytes, requestId = randomUUID().replaceAll('-', '')) {
  const digest = `sha256:${createHash('sha256').update(bytes).digest('hex')}`;
  const size = BigInt(bytes.length);
  const begun = await writer.begin(requestId, digest, size);
  if (begun?.outcome === 'committed' || begun?.outcome === 'present') {
    if (begun.stored?.digest !== digest) throw new Error('Content writer stored another digest.');
    return digest;
  }
  if (begun?.outcome === 'forbidden') throw new ListenRefusal(storeRefusal);
  if (begun?.outcome === 'too_large') throw new ListenRefusal('The recording is larger than OpenAbstractions accepts.');
  if (begun?.outcome === 'unavailable' || begun?.outcome === 'unsupported') {
    throw new ListenRefusal('OpenAbstractions content storage is unavailable.');
  }
  if (begun?.outcome !== 'started' || !begun.upload || begun.upload.digest !== digest || BigInt(begun.upload.size) !== size) {
    throw new ListenRefusal(`OpenAbstractions refused the recording upload (${begun?.outcome ?? 'no answer'}).`);
  }
  const upload = begun.upload;
  let offset = BigInt(upload.received);
  let stalled = 0;
  try {
    while (offset < size) {
      const end = Math.min(Number(offset) + appendBytes, bytes.length);
      const appended = await writer.append(upload.handle, offset, bytes.subarray(Number(offset), end));
      if (appended?.outcome !== 'accepted' && appended?.outcome !== 'out_of_order') {
        throw new ListenRefusal(`OpenAbstractions stopped the recording upload (${appended?.outcome ?? 'no answer'}).`);
      }
      const received = BigInt(appended.received);
      if (received < 0n || received > size || (appended.outcome === 'accepted' && received !== BigInt(end))) {
        throw new Error('Content writer reported an inconsistent offset.');
      }
      // out_of_order names the offset the store expects; one that repeats
      // without progress is a store that will not take this upload.
      stalled = received === offset ? stalled + 1 : 0;
      if (stalled >= maxStalledAppends) throw new ListenRefusal('The recording upload stopped advancing in OpenAbstractions.');
      offset = received;
    }
    const committed = await writer.commit(upload.handle);
    // OA decides the verified recording once more before it keeps it (STO-W11).
    if (committed?.outcome === 'forbidden') throw new ListenRefusal(storeRefusal);
    if (committed?.outcome !== 'committed' || committed.stored?.digest !== digest) {
      throw new ListenRefusal(`OpenAbstractions did not keep the recording (${committed?.outcome ?? 'no answer'}).`);
    }
    return digest;
  } catch (error) {
    try { await writer.abort(upload.handle); } catch { /* the original failure stays authoritative */ }
    throw error;
  }
}

function admissionRefusal(admission) {
  switch (admission?.outcome) {
    case 'not_permitted': return new ListenRefusal(panelQuestion, 'warning');
    case 'no_host':
    case 'unsupported_feature':
    case 'unavailable': return new ListenRefusal(engineMissing);
    case 'forbidden': return new ListenRefusal(`OpenAbstractions did not let the speech engine read the recording; allow OpenCode ${inputGrant} in the OpenAbstractions Panel.`);
    case 'budget_exceeded': return new ListenRefusal('OpenAbstractions refused transcription: the usage limit is reached.');
    case 'exhausted': return new ListenRefusal('OpenAbstractions is busy with other transcriptions; try again shortly.');
    default: return new ListenRefusal(`OpenAbstractions refused transcription (${admission?.outcome ?? 'no answer'}).`);
  }
}

function endRefusal(end) {
  if (end.outcome === 'not_permitted') return new ListenRefusal(panelQuestion, 'warning');
  if (end.outcome === 'cancelled') return new ListenRefusal('Transcription was cancelled.', 'info');
  if (end.outcome === 'unavailable' || end.outcome === 'no_host') return new ListenRefusal(engineMissing);
  return new ListenRefusal(`Transcription ended without text (${end.outcome}).`);
}

/** Start transcription@1 on a stored WAV digest and read its segment text to the end. */
export async function transcribe(transcription, digest, {signal = null, now = () => performance.now()} = {}) {
  const deadline = now() + maxTranscriptionMs;
  const admission = await transcription.start({
    model: transcriptionModel, audioDigest: digest, mediaType: 'audio/wav', language: '',
    timestamps: 'none', guarantees: [localOnly], credential: '', extensions: {},
  });
  if (admission?.outcome !== 'accepted' || !admission.operation) throw admissionRefusal(admission);
  const operation = admission.operation;
  let complete = false;
  try {
    const segments = [];
    let cursor = 0n;
    for (;;) {
      if (signal?.aborted) throw new ListenRefusal('Transcription was cancelled.', 'info');
      const remaining = deadline - now();
      if (remaining <= 0) {
        throw new ListenRefusal('Transcription took longer than 2 minutes and was cancelled; try a shorter recording.');
      }
      const wait = BigInt(Math.max(1, Math.min(observeWaitMs, Math.ceil(remaining))));
      const page = await transcription.observe(operation, cursor, 64n, 65536n, wait);
      if (page?.outcome !== 'page' || !Array.isArray(page.deltas) || BigInt(page.next) < cursor) {
        throw new Error(`Transcription observation stopped: ${page?.outcome ?? 'invalid'}`);
      }
      for (const delta of page.deltas) {
        if (delta.kind === 'segment' && delta.segment?.kind === 'segment') segments.push(delta.segment.text);
        if (delta.transcriptionEnd) {
          complete = true;
          const end = delta.transcriptionEnd;
          if (end.outcome !== 'completed') throw endRefusal(end);
          return {text: segments.join(' ').replace(/\s+/g, ' ').trim(), operation,
            host: end.host, model: end.model, durationMs: Number(end.durationMs)};
        }
      }
      if (page.atEnd || (BigInt(page.next) === cursor && page.deltas.length)) {
        throw new Error('Transcription ended without a terminal result.');
      }
      cursor = BigInt(page.next);
    }
  } finally {
    if (!complete) {
      try { await transcription.cancel(operation); } catch { /* the original failure stays authoritative */ }
    }
  }
}

function silent(pcm) {
  for (let i = 0; i < pcm.length; i++) if (pcm[i] !== 0) return false;
  return true;
}

/**
 * The listen command's state machine. toggle() starts recording, and a second
 * toggle() or the 30-second limit stops it, uploads the WAV, transcribes it and
 * appends the text to the prompt. Every refusal becomes one toast; nothing
 * throws out of toggle().
 */
export function createListener({capture = startCapture, resolveWriter, resolveTranscription, appendPrompt, toast,
  onChange = () => {}, setTimeout: after = setTimeout, clearTimeout: cancelAfter = clearTimeout,
  setInterval: every = setInterval, clearInterval: stopEvery = clearInterval}) {
  if (typeof resolveWriter !== 'function' || typeof resolveTranscription !== 'function'
      || typeof appendPrompt !== 'function' || typeof toast !== 'function') {
    throw new TypeError('Listen needs content-writer and transcription resolvers, appendPrompt and toast.');
  }
  let state = {phase: 'idle', elapsedMs: 0};
  let session = null;
  let limit = null;
  let ticker = null;
  let running = null;
  const controller = new AbortController();
  const set = (next) => {
    state = next;
    onChange(state);
  };
  const say = (message, variant = 'info') => {
    try { toast({variant, message}); } catch { /* a toast must not break the command */ }
  };

  async function finish() {
    const current = session;
    session = null;
    cancelAfter(limit);
    stopEvery(ticker);
    set({phase: 'transcribing', elapsedMs: current.elapsedMs()});
    try {
      const pcm = await current.stop();
      const lost = current.dropped?.() ?? 0;
      if (pcm.length === 0) throw new ListenRefusal('The microphone recorded nothing.');
      if (silent(pcm)) {
        throw new ListenRefusal('Windows gave OpenCode only silence; allow microphone access for desktop apps in Settings, Privacy & security, Microphone.');
      }
      const signal = controller.signal;
      // The WAV is request input (STO-T7): OA holds it for ten minutes and while
      // the transcription that names it runs, then removes it.
      const digest = await uploadContent(await resolveWriter({signal}), wavFromPcm16(pcm));
      if (signal.aborted) return;
      const result = await transcribe(await resolveTranscription({signal}), digest, {signal});
      if (!result.text) throw new ListenRefusal('No speech was recognised in the recording.', 'warning');
      const appended = await appendPrompt(result.text);
      if (appended?.error) throw new ListenRefusal('OpenCode did not accept the transcribed text.');
      if (lost > 0) say('Transcribed text is in the prompt, but part of the recording was lost; check it before pressing Enter.', 'warning');
      else say('Transcribed text is in the prompt; review it and press Enter.', 'success');
    } catch (error) {
      if (error instanceof ListenRefusal) say(error.message, error.variant);
      else say('OpenAbstractions could not transcribe the recording; check the OpenAbstractions Panel.', 'error');
    } finally {
      set({phase: 'idle', elapsedMs: 0});
    }
  }

  function begin() {
    try {
      session = capture();
    } catch (error) {
      session = null;
      const refusal = captureRefusal(error) ?? new ListenRefusal('The microphone could not be opened.');
      say(refusal.message, refusal.variant);
      return;
    }
    set({phase: 'listening', elapsedMs: 0});
    ticker = every(() => {
      if (session) set({phase: 'listening', elapsedMs: session.elapsedMs()});
    }, tickMs);
    limit = after(() => { if (session) running = finish(); }, maxListenMs);
  }

  return {
    state: () => state,
    /** Start, or stop and transcribe; resolves when this press's own work is done. */
    async toggle() {
      if (state.phase === 'listening' && session) {
        running = finish();
        return running;
      }
      if (state.phase === 'transcribing') {
        say('Still transcribing the last recording.');
        return undefined;
      }
      begin();
      return undefined;
    },
    /** Close the device and abandon any transcription in flight. */
    async dispose() {
      controller.abort();
      cancelAfter(limit);
      stopEvery(ticker);
      const current = session;
      session = null;
      if (current) {
        try { await current.stop(); } catch { /* disposal releases the device either way */ }
      }
      await running;
    },
  };
}

async function resolveService(options, contract, signal, load) {
  const [{Machine}, {BunNativeConnector}, Client] = await Promise.all([
    import('@openabstractions/facade'), import('@openabstractions/ipc/bun'), load(),
  ]);
  const machine = new Machine(options?.runtimeEndpoint ?? null, {
    connector: new BunNativeConnector(), timeout: 30000, cancellation: signal ?? null,
    server: options?.server ?? null,
  });
  return new Client(await machine.resolveService(contract, {scope: 'local'}));
}

/** A prompt-slot text node whose content follows the listener state. The
 * imports resolve to OpenCode's own Solid runtime inside the TUI. */
async function indicatorSlot(api) {
  const [{createElement, insert, setProp}, {createSignal}] = await Promise.all([
    import('@opentui/solid'), import('solid-js'),
  ]);
  const [text, setText] = createSignal('');
  const render = () => {
    const node = createElement('text');
    setProp(node, 'fg', api.theme?.current?.error);
    insert(node, text);
    return node;
  };
  api.slots.register({order: 50, slots: {session_prompt_right: render, home_prompt_right: render}});
  return (state) => setText(indicatorText(state));
}

/**
 * Register `openabstractions.listen` (Ctrl+Alt+M, /listen) and its prompt-slot
 * indicator in an OpenCode TUI. `dependencies` replaces capture, the OA service
 * resolvers and the indicator in tests.
 */
export async function registerListen(api, options = {}, dependencies = {}) {
  const key = typeof options.keybind === 'string' && options.keybind ? options.keybind : listenKey;
  // Recording the microphone is never silent: without the prompt-slot
  // indicator the command refuses to record.
  let show = null;
  try {
    show = await (dependencies.indicator ?? indicatorSlot)(api);
  } catch {
    show = null;
  }
  const listener = createListener({
    capture: dependencies.capture ?? (() => startCapture()),
    resolveWriter: dependencies.resolveWriter ?? (({signal}) => resolveService(options, writerContract, signal,
      async () => (await import('@openabstractions/storage-content')).ContentWriterClient)),
    resolveTranscription: dependencies.resolveTranscription ?? (({signal}) => resolveService(options, transcriptionContract,
      signal, async () => (await import('@openabstractions/inference/protocol')).TranscriptionClient)),
    appendPrompt: (text) => api.client.tui.appendPrompt({text}),
    toast: (input) => api.ui.toast(input),
    onChange: (state) => show?.(state),
    ...(dependencies.timers ?? {}),
  });
  const off = api.keymap.registerLayer({
    commands: [{
      namespace: 'palette', name: listenCommand, title: 'Listen', category: 'OpenAbstractions',
      desc: 'Dictate into the prompt through a local OpenAbstractions speech engine',
      slashName: listenSlash,
      run() {
        if (!show) {
          api.ui.toast({variant: 'error', message: 'Listening is off because this OpenCode could not show the recording indicator in the prompt.'});
          return;
        }
        void listener.toggle();
      },
    }],
    bindings: [{key, cmd: listenCommand, desc: 'Listen'}],
  });
  api.lifecycle?.onDispose?.(async () => {
    off?.();
    await listener.dispose();
  });
  return listener;
}
