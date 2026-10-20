import {readDelivery, resolveContentReader} from './content-delivery.js';

/** The speech OpenCode asks OA for: Lemonade's kokoro-v1, an American
 * English voice, WAV the provider writes itself. */
export const speechModel = 'kokoro-v1';
export const speechVoice = 'af_sky';
/** A spoken reply keeps its first 600 characters. */
export const maxSpokenCharacters = 600;
/** speech@1's own input bound (INF-S6). */
const maxSpeechCharacters = 4096;
const speechContract = 'abstraction.inference/speech@1';
const mediaTypes = new Set(['audio/wav']);
// 4096 characters of 24 kHz 16-bit mono speech stay well under this.
const maxSpeechBytes = 24 * 1024 * 1024;
const voicePattern = /^[a-z]{2}_[a-z]{1,32}$/;
const speakToggle = 'openabstractions.speak_replies';
const digestPattern = /^sha256:[0-9a-f]{64}$/;

const fencedCode = /(```|~~~)[\s\S]*?(\1|$)/g;
const inlineCode = /`[^`\n]*`/g;
const links = /!?\[([^\]]*)\]\([^)]*\)/g;
const urls = /\b(?:https?|file|ftp):\/\/\S+/gi;
// A path names a folder: a drive or UNC root, ./ ../ ~/ /, or a word
// followed by a slash or backslash.
const paths = /(?:[A-Za-z]:[\\/]|\\\\|~[\\/]|\.{1,2}[\\/]|\/)?(?:[\w.@-]+[\\/])+[\w.@-]*/g;
const markup = /^\s{0,3}(?:#{1,6}\s+|>\s?|[-*+]\s+|\d+[.)]\s+)|[*_]{1,3}(?=\S)|(?<=\S)[*_]{1,3}/gm;
const controls = /[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F-\u009F]/g;

/**
 * The part of an assistant reply worth hearing: fenced and inline code,
 * links' targets, URLs and paths are left out, Markdown markers dropped,
 * the text put in NFC without combining marks, variation selectors or
 * control characters (the Kokoros policy refuses those), and cut to `limit`
 * characters at a word boundary.
 */
export function spokenText(reply, limit = maxSpokenCharacters) {
  let text = String(reply ?? '').replace(/\r\n?/g, '\n').replace(/\t/g, ' ');
  text = text.replace(fencedCode, ' ').replace(inlineCode, ' ').replace(links, '$1').replace(urls, ' ').replace(paths, ' ');
  text = text.replace(markup, '').replace(controls, ' ');
  text = text.normalize('NFC').replace(/\p{M}/gu, '').replace(/[​-‏⁠-⁤﻿]/g, '');
  text = text.split('\n').map((line) => line.replace(/ {2,}/g, ' ').replace(/ ([.,;:!?])/g, '$1').trim()).filter(Boolean).join('\n').trim();
  const characters = Array.from(text);
  if (characters.length <= limit) return text;
  const cut = characters.slice(0, limit).join('');
  const space = cut.search(/\s\S*$/);
  return (space > 0 && limit - space < 80 ? cut.slice(0, space) : cut).trim();
}

/**
 * Starts speech@1 for text, observes it to its end and reads the delivery
 * back through the caller's content reader, verifying size, digest and WAV
 * bytes as the image tool verifies an image. Returns {bytes, delivery,
 * host, model, operation}, {refused} for an admission refusal or a failed
 * reply, or {cancelled} when signal aborts; an abandoned operation is
 * cancelled.
 */
export async function synthesizeSpeech({speech, reader, text, voice = speechVoice, signal}) {
  // Imported here, as listen.js imports its clients, so a TUI whose runtime
  // packages fail to load still installs every other feature.
  const {newSpeechRequest} = await import('@openabstractions/inference/protocol');
  const request = newSpeechRequest();
  Object.assign(request, {model: speechModel, voice, text, format: 'wav', guarantees: []});
  const admission = await speech.start(request);
  if (admission?.outcome !== 'accepted' || !admission.operation) {
    return {refused: `Speech refused: ${admission?.outcome ?? 'unavailable'} (${admission?.reason ?? ''}).`};
  }
  const operation = admission.operation;
  let ended = false;
  try {
    for (let cursor = 0n; ;) {
      if (signal?.aborted) return {cancelled: true};
      const page = await speech.observe(operation, cursor, 64n, 65536n, 25000n);
      if (page?.outcome !== 'page' || !Array.isArray(page.deltas) || BigInt(page.next) < cursor) {
        throw new Error(`Speech observation stopped: ${page?.outcome ?? 'invalid'}`);
      }
      for (const delta of page.deltas) {
        if (!delta.speechEnd) continue;
        const end = delta.speechEnd;
        ended = true;
        if (end.outcome !== 'completed') return {refused: `Speech ${end.outcome}: ${end.reason}.`};
        const bytes = await readDelivery(reader, end.delivery, {mediaTypes, maxBytes: maxSpeechBytes, what: 'Speech'});
        return {bytes, delivery: end.delivery, host: end.host, model: end.model, operation};
      }
      if (page.atEnd) throw new Error('Speech operation ended without a terminal result.');
      if (signal?.aborted) return {cancelled: true};
      cursor = BigInt(page.next);
    }
  } finally {
    if (!ended) {
      try { await speech.cancel(operation); } catch { /* the original outcome stands */ }
    }
  }
}

/** The caller's speech@1 client on this machine. */
export async function resolveSpeechClient(options, signal) {
  const [{Machine}, {SpeechClient}, {BunNativeConnector}] = await Promise.all([
    import('@openabstractions/facade'),
    import('@openabstractions/inference/protocol'),
    import('@openabstractions/ipc/bun'),
  ]);
  const machine = new Machine(options?.runtimeEndpoint ?? null, {
    connector: new BunNativeConnector(), timeout: 30000, cancellation: signal,
    server: options?.server ?? null,
  });
  return new SpeechClient(await machine.resolveService(speechContract, {scope: 'local'}));
}

function validSpeakRequest(args) {
  if (!args || Object.keys(args).some((key) => key !== 'text' && key !== 'voice')
      || typeof args.text !== 'string' || !args.text.trim() || Array.from(args.text).length > maxSpeechCharacters
      || args.voice !== undefined && (typeof args.voice !== 'string' || !voicePattern.test(args.voice))) {
    throw new TypeError(`Provide text of 1..${maxSpeechCharacters} characters and, optionally, an English Kokoro voice such as ${speechVoice}.`);
  }
}

/**
 * openabstractions_speak: the model reads a chosen text aloud through OA,
 * after the person approves this one call. The tool verifies the delivery;
 * OpenCode's TUI module plays it (installSpeak).
 */
export function createSpeakTool({tool, resolveSpeech, resolveReader}) {
  if (typeof tool !== 'function' || typeof resolveSpeech !== 'function' || typeof resolveReader !== 'function') {
    throw new TypeError('Speak tool requires speech and content-reader resolvers.');
  }
  return tool({
    description: `Read a chosen text aloud through OpenAbstractions speech (${speechModel}, an English voice). The person hears it in OpenCode. Use it when asked to read something aloud.`,
    args: {text: tool.schema.string(), voice: tool.schema.string().optional()},
    async execute(args, context) {
      validSpeakRequest(args);
      const signal = context?.abort ?? context?.signal;
      if (signal?.aborted) return 'Speech cancelled.';
      if (typeof context?.ask !== 'function') return 'Speech needs an available approval prompt.';
      const voice = args.voice ?? speechVoice;
      const characters = Array.from(args.text).length;
      await context.ask({permission: 'openabstractions_speak',
        patterns: [`Read ${characters} characters aloud with OA ${speechModel} voice ${voice}.`],
        always: [], metadata: {model: speechModel, voice, characters}});
      if (signal?.aborted) return 'Speech cancelled.';
      const speech = await resolveSpeech({signal});
      if (signal?.aborted) return 'Speech cancelled.';
      const result = await synthesizeSpeech({speech, reader: await resolveReader({signal}), text: args.text, voice, signal});
      if (result.cancelled) return 'Speech cancelled.';
      if (result.refused) return result.refused;
      return {title: 'OpenAbstractions speech',
        output: `Read ${characters} characters aloud with ${result.host}/${result.model}; SHA-256 ${result.delivery.digest}.`,
        metadata: {operation: result.operation, host: result.host, model: result.model, voice, digest: result.delivery.digest,
          mediaType: result.delivery.mediaType, bytes: result.bytes.length}};
    },
  });
}

/** The last assistant reply's visible text in a session. */
function lastReply(api, sessionID) {
  const messages = api.state?.session?.messages?.(sessionID) ?? [];
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i]?.role !== 'assistant') continue;
    return (api.state.part?.(messages[i].id) ?? [])
      .filter((part) => part?.type === 'text' && !part.synthetic && !part.ignored && typeof part.text === 'string')
      .map((part) => part.text).join('\n');
  }
  return '';
}

async function openTuiAudio() {
  const {Audio} = await import('@opentui/core');
  return Audio.create({autoStart: false});
}

/**
 * The TUI half of OA speech. Command `openabstractions.speak` (slash
 * `/speak`) turns Speak replies on or off, kept in the TUI's key-value
 * store. With it on, each `session.idle` speaks the last answer: code,
 * paths and links left out, the first 600 characters, speech@1 with
 * kokoro-v1 and af_sky as WAV, the delivery read back and verified, played
 * through OpenTUI audio. A completed `openabstractions_speak` tool call is
 * played the same way from its verified delivery. A newer utterance stops
 * the one playing and cancels one still being made.
 */
export async function installSpeak(api, options = {}, deps = {}) {
  const runtime = {runtimeEndpoint: options?.runtimeEndpoint ?? null, server: options?.server ?? null};
  const resolveSpeech = deps.resolveSpeech ?? (({signal}) => resolveSpeechClient(runtime, signal));
  const resolveReader = deps.resolveReader ?? (({signal}) => resolveContentReader(runtime, signal));
  const makeAudio = deps.audio ?? openTuiAudio;
  let audio = null;
  let voice = null;
  let current = null;
  const played = new Set();

  async function play(bytes) {
    audio ??= await makeAudio();
    const sound = audio.loadSound(bytes);
    if (!sound) throw new Error('OpenTUI could not decode the speech.');
    if (voice) audio.stopVoice(voice);
    if (!audio.isStarted() && !audio.start()) throw new Error('OpenTUI audio did not start.');
    voice = audio.play(sound);
  }

  async function utter(work) {
    current?.abort();
    const controller = new AbortController();
    current = controller;
    try {
      const bytes = await work(controller.signal);
      if (bytes && !controller.signal.aborted) await play(bytes);
    } catch (error) {
      if (!controller.signal.aborted) api.ui?.toast?.({variant: 'warning', message: `Speech: ${error?.message ?? error}`});
    } finally {
      if (current === controller) current = null;
    }
  }

  async function speakReply(sessionID) {
    const text = spokenText(lastReply(api, sessionID));
    if (!text) return;
    deps.onSpoken?.(text);
    await utter(async (signal) => {
      const result = await synthesizeSpeech({speech: await resolveSpeech({signal}), reader: await resolveReader({signal}), text, signal});
      if (result.refused) throw new Error(result.refused);
      return result.bytes ?? null;
    });
  }

  async function playToolResult(part) {
    const metadata = part?.state?.metadata;
    if (part?.type !== 'tool' || part.tool !== 'openabstractions_speak' || part.state?.status !== 'completed'
        || !digestPattern.test(metadata?.digest ?? '') || played.has(part.id)) return;
    played.add(part.id);
    await utter(async (signal) => readDelivery(await resolveReader({signal}),
      {location: 'local', digest: metadata.digest, mediaType: metadata.mediaType, size: metadata.bytes},
      {mediaTypes, maxBytes: maxSpeechBytes, what: 'Speech'}));
  }

  const enabled = () => api.kv?.get?.(speakToggle, false) === true;
  const removeLayer = api.keymap?.registerLayer?.({
    commands: [{
      name: 'openabstractions.speak', title: 'Speak replies', category: 'OpenAbstractions', namespace: 'palette', slashName: 'speak',
      run() {
        const next = !enabled();
        api.kv?.set?.(speakToggle, next);
        if (!next) current?.abort();
        api.ui?.toast?.({variant: 'info', message: next ? 'Speak replies on' : 'Speak replies off'});
      },
    }],
  });
  const offIdle = api.event?.on?.('session.idle', (event) => enabled() ? speakReply(event?.properties?.sessionID) : undefined);
  const offPart = api.event?.on?.('message.part.updated', (event) => playToolResult(event?.properties?.part));
  api.lifecycle?.onDispose?.(() => {
    current?.abort();
    offIdle?.();
    offPart?.();
    removeLayer?.();
    audio?.dispose();
    audio = null;
  });
}
