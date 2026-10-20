// Native mods API only. The existing kept MCP helper owns authority and inbox.
const server = 'OpenAbstractions';
// Projected from OA's private C1 receipts when the owned module is installed.
// A loaded module receives changes when Claude reloads plugins or reopens.
const backgroundDisabled = {messages: false, asks: false};
const watchTool = 'mcp__OpenAbstractions__oa_messages_watch';
const waitTool = 'mcp__OpenAbstractions__oa_messages_wait';
const sessionStartTool = 'oa_claude_session_start';
const sessionEndTool = 'oa_claude_session_end';
const wakePrompt = __OA_MESSAGES_WAKE_PROMPT__;
// ASK-R14: the fixed pointer for a question this chat's enrolled program may
// answer. It carries no question text, asker or ID.
const asksPointer = wakePrompt.asks;
const urgentWakeIntervalMs = 30000;
function validPointer(text, ticket) {
  if (typeof text !== 'string' || !validTicket(ticket) || text.length > 512) return false;
  const render = value => value.replaceAll('{ticket}', ticket);
  if (ticket.length === 64) return text === render(wakePrompt.legacy);
  if ([wakePrompt.repeat, wakePrompt.anonymous].some(value => text === render(value))) return true;
  const [prefix, suffix] = render(wakePrompt.first).split('{sender}');
  if (!text.startsWith(prefix) || !text.endsWith(suffix)) return false;
  const sender = text.slice(prefix.length, text.length - suffix.length);
  return sender.length > 0 && [...sender].length <= 32 && sender.trim() === sender && /^[\p{L}\p{N}\p{M} _-]+$/u.test(sender);
}
function validTicket(ticket) { return typeof ticket === 'string' && /^(?:[0-9a-f]{8}|[0-9a-f]{64})$/.test(ticket); }

function cancelTimer(state) {
  try { state.timer?.cancel(); } catch { /* The state fence still owns late work. */ }
  state.timer = null;
}
function close(state) {
  if (!state) return;
  state.closed = true;
  cancelTimer(state);
}
function nativeBindingMetadata(binding) {
  return typeof binding?.child_instance === 'string' && binding.child_instance.length > 0 && binding.child_instance.length <= 64 &&
      typeof binding?.session_id === 'string' && binding.session_id.length > 0 && binding.session_id.length <= 256;
}
// Classic events carry the current permission_mode. Missing host APIs or an
// unknown mode leave cold start unavailable; live receiving still works.
async function hostLaunchFacts($, event) {
  if (event.permission_mode === undefined) return undefined;
  const modes = {plan:'plan', default:'default', acceptEdits:'accept_edits'};
  const mode = typeof event.permission_mode === 'string' && Object.hasOwn(modes, event.permission_mode) ? modes[event.permission_mode] : '';
  if (!mode) return null;
  try {
    const cwd = await $.session.cwd(), surfaces = await $.session.surfaces();
    const origin = surfaces[0] === 'desktop' ? 'desktop' : surfaces[0] === 'terminal' ? 'cli' : '';
    if (!origin || typeof cwd !== 'string' || !cwd || cwd.length > 4096) return null;
    return {cwd, origin, mode};
  } catch { return null; }
}
// AB-1s F2: a model calls the same listed tools. OA honors turn signals and
// wake reservations only with the key this Mod registered on the child's
// start; an older child refuses the unknown field until it advertises it.
async function adapterKey($, session) {
  const name = 'oa.messages.adapter.v1:' + JSON.stringify([session]);
  try { const kept = await $.store.get(name); if (/^[0-9a-f]{32}$/.test(kept)) return {key: kept, on: false}; } catch {}
  const bytes = new Uint8Array(16);
  try { crypto.getRandomValues(bytes); } catch { for (let n = 0; n < 16; n++) bytes[n] = Math.floor(Math.random() * 256); }
  const key = [...bytes].map(b => b.toString(16).padStart(2, '0')).join('');
  try { await $.store.set(name, key); } catch { /* A reload then registers a key this child refuses. */ }
  return {key, on: false};
}
const keyed = adapter => adapter?.on ? {adapter_key: adapter.key} : {};
async function startNativeBinding($, title, launch, adapter) {
  const plain = {chat_title:title};
  let binding = await $.mcp.call(server, sessionStartTool, {...plain, ...keyed(adapter)});
  let observed;
  try { observed = JSON.parse(binding?.content?.[0]?.text); } catch {}
  const present = adapter && !adapter.on && observed?.adapter === 'supported';
  if (adapter) adapter.on = observed?.adapter === 'accepted';
  if (!binding?.isError && observed?.session_id === await $.session.id() && (observed?.launch_facts === 'supported' && launch !== undefined || present)) {
    binding = await $.mcp.call(server, sessionStartTool, {...plain, ...(launch !== undefined ? {launch} : {}), ...(present ? {adapter_key: adapter.key} : keyed(adapter))});
    try { observed = JSON.parse(binding?.content?.[0]?.text); } catch { observed = null; }
    if (adapter) adapter.on = observed?.adapter === 'accepted';
  }
  return {binding, observed};
}
async function endNativeBinding($, binding, retire = false) {
  if (!nativeBindingMetadata(binding)) return false;
  try {
    const out = await $.mcp.call(server, sessionEndTool, {child_instance:binding.child_instance, session_id:binding.session_id, retire});
    let result; try { result = JSON.parse(out?.content?.[0]?.text); } catch {}
    return !out?.isError && result?.outcome === (retire ? 'retired' : 'ended');
  } catch { return false; }
}
function terminal(message, health = 'stopped_protocol') {
  const error = new Error(message);
  error.terminal = true;
  error.health = health;
  return error;
}
function label(cwd) {
  if (typeof cwd !== 'string' || cwd.length > 32768) return 'Claude Code';
  const leaf = cwd.split(/[\\/]/).filter(Boolean).at(-1)?.replace(/[\u0000-\u001f\u007f]/g, '').trim();
  return leaf && !/^[A-Za-z]:$/.test(leaf) ? leaf.slice(0, 64) : 'Claude Code';
}
// Classic SessionStart and UserPromptSubmit carry the host's own session
// title. It names this chat in place of the folder label, which two chats in
// one project share.
function sessionTitle(event) {
  if (typeof event?.session_title !== 'string' || event.session_title.length > 32768) return '';
  const plain = [...event.session_title].map(c => { const n = c.codePointAt(0); return n < 0x20 || n >= 0x7f && n <= 0x9f || n >= 0x2028 && n <= 0x202e || n >= 0x2066 && n <= 0x2069 ? ' ' : c; }).join('').replace(/\s+/g, ' ').trim();
  return [...plain].slice(0, 64).join('').trim();
}
// The next observation's host correlation carries the new title to OA.
function relabel($, state, title) {
  if (!title || state.label === title) return;
  state.label = title;
  const shown = state.status;
  state.status = '';
  if (shown) status($, state, shown);
}
// The person's next message in the chat resumes background messages.
const pausedStatus = 'background messages paused; send any message in this chat to resume';
function status($, state, value) {
  if (state.status === value) return;
  state.status = value;
  try { $.ui.status(`OpenAbstractions (${state.label}): ${value}`); } catch { /* Local display supplies no admission. */ }
}
function arm($, state) {
  if (state.closed || state.paused || state.running || state.timer) return;
  try {
    state.timer = $.clock.after(state.retryDelay || 1000, () => {
      state.timer = null;
      void poll($, state);
    });
  } catch { status($, state, 'background messages unavailable'); close(state); }
}
function validWake(value) { return typeof value === "string" && new TextEncoder().encode(value).length <= 128 && !/[\u0000-\u001f\u007f-\u009f]/.test(value); }
function greaterWake(a, b) {
  const x = new TextEncoder().encode(a), y = new TextEncoder().encode(b);
  for (let n = 0; n < Math.min(x.length, y.length); n++) if (x[n] !== y[n]) return x[n] > y[n];
  return x.length > y.length;
}
async function loadWake($, state) {
  const key = "oa.messages.wake.v1:" + JSON.stringify([state.session, state.context]);
  let kept;
  try { kept = await $.store.get(key); } catch { throw terminal("OA durable wake state unavailable"); }
  if (kept !== undefined && !validWake(kept)) throw terminal("OA durable wake state invalid");
  state.wakeStoreKey = key;
  if (kept && greaterWake(kept, state.highWake || "")) state.highWake = kept;
  const dropped = await $.store.get(key + ':drop');
  if (dropped != null && (!validTicket(dropped.ticket) || !validWake(dropped.wake) || !dropped.wake)) throw terminal('OA dropped wake state invalid');
  state.knownDrop = dropped || null;
}
async function rememberDrop($, state, value) { await $.store.set(state.wakeStoreKey + ':drop', value); state.knownDrop = value; }
async function claimWake($, state, wake) {
  await loadWake($, state);
  if (!wake || !greaterWake(wake, state.highWake || "")) return false;
  try { await $.store.set(state.wakeStoreKey, wake); } catch { throw terminal("OA durable wake state unavailable"); }
  state.highWake = wake;
  return true;
}
function result(raw, state) {
  if (raw?.isError) { state.healthSupported = false; return null; }
  if (!Array.isArray(raw?.content) || raw.content.length !== 1
    || raw.content[0]?.type !== 'text' || typeof raw.content[0].text !== 'string'
    || new TextEncoder().encode(raw.content[0].text).length > 4096) throw terminal('OA observation invalid');
  let value;
  try { value = JSON.parse(raw.content[0].text); } catch { throw terminal('OA observation invalid'); }
  // Capability advertisement is diagnostic only. It grants no authority and
  // avoids sending the optional input to an older helper's closed schema.
  if (value && !Array.isArray(value) && typeof value === 'object') state.healthSupported = value.health_supported === true;
  if (!value || Array.isArray(value) || typeof value !== 'object'
    || typeof value.outcome !== 'string' || typeof value.context !== 'string' || typeof value.revision !== 'string'
    || new TextEncoder().encode(value.revision).length > 128
    || !Number.isSafeInteger(value.pending_until_unix_ms) || value.pending_until_unix_ms < 0
    || ('reply_until_unix_ms' in value && (!Number.isSafeInteger(value.reply_until_unix_ms) || value.reply_until_unix_ms < 0))
    || ('reason' in value && (typeof value.reason !== 'string' || new TextEncoder().encode(value.reason).length > 128))
    || ('health_supported' in value && typeof value.health_supported !== 'boolean')
    || ['unread','pending_questions','oldest_unread_unix_ms'].some(key => key in value && (!Number.isSafeInteger(value[key]) || value[key] < 0))
    || [['awaited_replies',50],['waiting_asks',64],['answerable_asks',128]].some(([key,max]) => key in value && (!Number.isSafeInteger(value[key]) || value[key] < 0 || value[key] > max))
    || ('wake_ticket' in value && value.wake_ticket !== '' && !validTicket(value.wake_ticket))
    || ('asks_wake' in value && !validWake(value.asks_wake))
    || ('waiting_asks_wake' in value && !validWake(value.waiting_asks_wake))
    || ('oldest_asks_wake' in value && !validWake(value.oldest_asks_wake))
    || ('oldest_wake' in value && !validWake(value.oldest_wake))
    || ('wake_sender' in value && (typeof value.wake_sender !== 'string' || [...value.wake_sender].length > 32 || /[\p{Cc}\p{Cf}]/u.test(value.wake_sender)))) throw terminal('OA observation invalid');
  // Watch replies are additive. Older helpers have four core fields; newer
  // eligibility hints and unrelated future fields do not change their meaning.
  const replyUntil = value.reply_until_unix_ms ?? 0;
  if (value.outcome === 'unknown' || value.outcome === 'unavailable') {
    if (value.context !== '' || value.revision !== '' || value.pending_until_unix_ms !== 0 || replyUntil !== 0 || ('wake' in value && value.wake !== '') || (value.wake_ticket || '') !== '' || (value.asks_wake || '') !== ''
      || value.awaited_replies || value.waiting_asks || value.answerable_asks || value.waiting_asks_wake || value.oldest_wake || value.oldest_asks_wake) throw terminal('OA observation invalid');
    return null;
  }
  if (value.outcome === 'forbidden') throw terminal('OA native admission refused', 'stopped_forbidden');
  if (value.outcome !== 'page' || !validWake(value.wake) || !/^(?:native-[0-9a-f]{32}|chat-[0-9a-f]{48})$/.test(value.context)) throw terminal('OA observation invalid');
  if (value.oldest_wake && (!value.wake || greaterWake(value.oldest_wake,value.wake))) throw terminal('OA wake range invalid');
  if (value.oldest_asks_wake && (!value.asks_wake || greaterWake(value.oldest_asks_wake,value.asks_wake))) throw terminal('OA Asks wake range invalid');
  if (value.wake_pointer != null && (!value.wake_ticket || !validPointer(value.wake_pointer, value.wake_ticket))) throw terminal('OA wake pointer invalid');
  const wake = value.unread === 0 ? '' : value.wake;
  return {outcome: value.outcome, context: value.context, revision: value.revision, pending_until_unix_ms: value.pending_until_unix_ms, reply_until_unix_ms: replyUntil, wake, wake_ticket: value.wake_ticket || '', wake_pointer:value.wake_pointer || '', asks_wake:value.asks_wake || '', unread:value.unread, pending_questions:value.pending_questions, oldest_unread_unix_ms:value.oldest_unread_unix_ms,
    oldest_wake:value.oldest_wake, oldest_asks_wake:value.oldest_asks_wake, awaited_replies:value.awaited_replies, answerable_asks:value.answerable_asks, waiting_asks:value.waiting_asks, waiting_asks_wake:value.waiting_asks_wake};
}
async function reportHealth($, state, claim) {
  state.healthClaim = claim;
  if (!state.healthSupported || state.healthReported === claim || state.closed || state.paused) return;
  const current = async () => !state.closed && !state.paused && await $.session.id() === state.session;
  try {
    if (!await current()) return;
    const input = {revision: '', wait_ms: 0, health: claim};
    const approval = await $.tool.check({tool: watchTool, input});
    if (approval?.decision !== 'allow' || !await current()) return;
    // Same native tool, same connection, no target and no alternate transport.
    // Awaiting it preserves one controller; its native attempt is bounded.
    state.healthReported = claim;
    await $.mcp.call(server, 'oa_messages_watch', input);
  } catch { /* Best effort telemetry never supplies proof of OA status. */ }
}
// AB-1s 2026-11-09: end this chat's woken chain in OA; the next wake waits.
function signalTurn($, state, turn) {
  if (!state || state.closed || backgroundDisabled.messages) return;
  return state.turnSignal = (state.turnSignal || Promise.resolve()).then(async () => {
    try {
      const input = {revision: '', wait_ms: 0, turn, ...keyed(state.adapter)};
      if (await $.session.id() === state.session && (await $.tool.check({tool: watchTool, input}))?.decision === 'allow' && !state.closed) await $.mcp.call(server, 'oa_messages_watch', input);
    } catch { /* OA's lease ends it. */ }
  });
}
async function observe($, state, wait, reservation = null) {
  if (backgroundDisabled.messages) { const error = new Error('OA Messages background disabled'); error.permission = true; throw error; }
  if (state.closed || await $.session.id() !== state.session) throw terminal('OA session changed', 'stopped_context');
  // A replacement MCP child needs its own existing host correlation. Its
  // launch session must still match this Mod's conversation before Watch.
  const {binding, observed} = await startNativeBinding($, state.label, state.launchFacts, state.adapter);
  const currentSession = await $.session.id();
  if (observed?.session_id && observed.session_id !== currentSession) await endNativeBinding($, observed, true);
  if (state.closed || currentSession !== state.session) throw terminal('OA session changed', 'stopped_context');
  if (binding?.isError || observed?.outcome !== 'observed' || !nativeBindingMetadata(observed)) throw new Error('OA native admission suspended');
  if (observed?.session_id !== state.session) throw terminal('OA session changed', 'stopped_context');
  if (state.binding?.child_instance !== observed.child_instance) {
    state.healthReported = '';
    state.healthSupported = false;
  }
  state.binding = observed;
  const input = {revision: wait ? state.revision : '', wait_ms: wait};
  if (reservation) { Object.assign(input, keyed(state.adapter)); input.reserve_wake = reservation.wake; if (reservation.ticket) input.wake_ticket = reservation.ticket; }
  // This native query executes nothing and opens no permission dialog.
  let decision;
  try { decision = (await $.tool.check({tool: watchTool, input}))?.decision; } catch { /* Unknown remains blocked. */ }
  if (state.closed || await $.session.id() !== state.session) throw terminal('OA session changed', 'stopped_context');
  if (decision !== 'allow') {
    status($, state, 'background messages need permission; enable them in the Panel; OpenAbstractions status unconfirmed');
    const error = new Error('OA watch permission unavailable');
    error.permission = true;
    throw error;
  }
  const page = result(await $.mcp.call(server, 'oa_messages_watch', input), state);
  if (state.closed || await $.session.id() !== state.session) throw terminal('OA session changed', 'stopped_context');
  if (!page) throw new Error('OA native admission suspended');
  if (observed.context !== page.context) throw terminal('OA native host binding changed', 'stopped_context');
  if (state.context && page.context !== state.context) {
    // The same host conversation was confirmed above. Observation and wake
    // high-water state belong to the new native owner, never the old context.
    state.revision = '';
    state.highWake = '';
    state.knownDrop = null;
    state.healthReported = '';
    state.deliveredWake = '';
    state.asks.deliveredWake = '';
    state.asks.hintedThisTurn = false;
    state.asks.waitingDelivered = '';
  }
  state.summary = null;
  if (!page.wake || page.oldest_wake && greaterWake(page.oldest_wake, state.deliveredWake)) state.deliveredWake = '';
  if (page.answerable_asks === 0 || page.oldest_asks_wake && greaterWake(page.oldest_asks_wake, state.asks.deliveredWake)) {
    state.asks.deliveredWake = '';
    state.asks.hintedThisTurn = false;
  }
  state.context = page.context;
  state.recoverUntil = 0;
  state.retryDelay = 0;
  state.revision = page.revision;
  state.pendingUntil = Math.max(page.pending_until_unix_ms, page.reply_until_unix_ms);
  state.wake = page.wake;
  state.asks.wake = page.asks_wake;
  await loadWake($, state);
  await reportHealth($, state, 'watching');
  if (state.closed || await $.session.id() !== state.session) throw terminal('OA session changed', 'stopped_context');
  if (!reservation && page.context === state.context && page.revision === state.revision) {
    state.summary = {
      context: page.context, wake: page.wake, unread: page.unread, pending_questions: page.pending_questions, awaited_replies: page.awaited_replies,
      asks_wake: page.asks_wake, answerable_asks: page.answerable_asks, waiting_asks: page.waiting_asks, waiting_asks_wake: page.waiting_asks_wake,
    };
  }
  status($, state, 'background messages ready');
  return page;
}
async function failedObservation($, state, error) {
  if (state.closed) return;
  state.summary = null;
  if (error?.permission) return;
  if (error?.terminal) {
    await reportHealth($, state, error.health || 'stopped_protocol');
    if (!state.closed) status($, state, `background messages stopped: ${error.health || 'stopped_protocol'}; OpenAbstractions status unconfirmed`);
    close(state); return;
  }
  let now;
  try { now = await $.clock.now(); } catch { now = 1; }
  if (!state.recoverUntil) state.recoverUntil = now;
  state.retryDelay = Math.min(30000, Math.max(1000, (state.retryDelay || 500) * 2));
  await reportHealth($, state, 'recovering');
  if (state.closed || state.paused) return;
  status($, state, 'background messages recovering');
}
// ASK-R14: the chat's own Messages watch carries asks_wake, the ordered key
// of the questions and shared choices this connection's enrolled program may
// answer now. A greater key wakes an idle chat once with the fixed pointer;
// the model then lists and answers through responder@1.
async function wakeAsks($, state) {
  if (backgroundDisabled.asks || backgroundDisabled.messages || state.closed || state.paused || !state.idle || state.waking || state.asks.waking
    || state.submission.state || !state.asks.wake || !greaterWake(state.asks.wake, state.asks.high)) return;
  state.asks.waking = true;
  try {
    const page = await observe($, state, 0);
    const key = page.asks_wake, prior = state.asks.high;
    if (state.closed || state.paused || !state.idle || state.waking || state.asks.deliveredWake || state.deliveredWake || !key || !greaterWake(key, prior) || state.submission.state) return;
    state.submission.state = state;
    state.asks.high = key; state.asks.submitting = true;
    try {
      const sent = await $.prompt.submit({text: asksPointer});
      if (sent?.drop) {
        // A returned drop proves no submission; the next idle tries again.
        state.asks.high = prior;
        if (String(sent.drop).includes('stopped') || String(sent.drop).includes('removed from the queue')) { state.paused = true; status($, state, pausedStatus); }
      } else if (typeof sent?.text !== 'string') {
        state.unknownSubmit = true;
        status($, state, pausedStatus);
      } else if (page.answerable_asks > 0 || page.waiting_asks > 0) {
        state.asks.deliveredWake = key;
        state.asks.waitingDelivered = page.waiting_asks_wake || state.asks.waitingDelivered;
      }
    } catch { state.unknownSubmit = true; status($, state, pausedStatus); }
    finally { state.asks.submitting = false; state.submission.state = null; }
  } catch (error) { await failedObservation($, state, error); }
  finally { state.asks.waking = false; }
}
async function wake($, state) {
  if (state.closed || state.paused || state.waking || !state.idle || !state.wake) return;
  state.waking = true;
  try {
    if (state.turnSignal) await state.turnSignal;
    let page = await observe($, state, 0);
    const now = await $.clock.now();
    const retry = state.knownDrop;
    if (state.closed || state.paused || !state.idle || !page.wake || state.deliveredWake
      || !(greaterWake(page.wake, state.highWake || '') || retry && !greaterWake(retry.wake, page.wake)) || state.submission.state) return;
    page = await observe($, state, 0, {wake:page.wake,ticket:retry?.ticket});
    if (!validTicket(page.wake_ticket)) { if (retry) await rememberDrop($, state, null); return; }
    state.ticket = page.wake_ticket;
    if (!validPointer(page.wake_pointer, state.ticket)) throw terminal('OA reserved pointer missing');
    state.pointerText = page.wake_pointer;
    if (state.closed || state.paused || !state.idle) { await rememberDrop($, state, {ticket:state.ticket,wake:page.wake}); return; }
    state.submission.state = state;
    state.reservedPrior = retry ? '' : state.highWake || '';
    const reservedContext = state.context;
    if (!await claimWake($, state, page.wake) && !retry) { state.submission.state = null; return; }
    state.reservedWake = page.wake;
    // Clear definitive-drop evidence before entering a possibly effective call.
    // A crash or unknown result from this point remains conservatively held.
    await rememberDrop($, state, null);
    state.deliveryUnknown = false;
    state.submitting = true;
    try {
      const sent = await $.prompt.submit({text: state.pointerText});
      if (sent?.drop) {
        // A returned drop proves no submission. A lost reply remains unknown.
        if (!state.deliveryUnknown && state.context === reservedContext) await rememberDrop($, state, {ticket:state.ticket,wake:state.reservedWake});
        if (String(sent.drop).includes('stopped') || String(sent.drop).includes('removed from the queue')) {
          state.paused = true;
          status($, state, pausedStatus);
          state.notified = null;
        } else if (state.recoverUntil) state.notified = null;
      } else if (typeof sent?.text !== 'string') {
        state.unknownSubmit = true;
        status($, state, pausedStatus);
      } else if (page.unread > 0 && page.pending_questions !== undefined && state.context === reservedContext) {
        state.deliveredWake = state.reservedWake;
      }
    } catch {
      state.unknownSubmit = true;
      status($, state, pausedStatus);
    } finally { state.submitting = false; state.reservedWake = null; state.ticket = null; state.submission.state = null; }
  } catch (error) { await failedObservation($, state, error); }
  finally {
    state.waking = false;
    if (state.submission.state === state && !state.submitting) state.submission.state = null;
  }
}
// OA reports a receiver as watching only while its Wait is open. A Wait that
// held at least a second starts the next one at once, so the watch has no
// timer gap; an immediate answer keeps the timer, which bounds the loop.
const heldWaitMs = 1000;
async function clockNow($) { try { return await $.clock.now(); } catch { return null; } }
async function poll($, state) {
  if (state.closed || state.paused || state.running) return;
  state.running = true;
  let held = false;
  try {
    if (!backgroundDisabled.messages) {
      const started = await clockNow($);
      await observe($, state, 25000);
      const ended = started === null ? null : await clockNow($);
      held = ended !== null && ended - started >= heldWaitMs;
      await wake($, state);
      await wakeAsks($, state);
    }
  } catch (error) { await failedObservation($, state, error); }
  finally {
    state.running = false;
    if (held && !state.retryDelay && !state.closed && !state.paused && !state.timer) void poll($, state);
    else arm($, state);
  }
}

async function initialize($, event, custody) {
  const current = ++custody.generation;
  try {
    const session = await $.session.id();
    const title = sessionTitle(event);
    const chatTitle = title || label(event.cwd);
    const launchFacts = await hostLaunchFacts($, event);
    if (current !== custody.generation) return;
    if (custody.state && !custody.state.closed && custody.state.session === session) {
      if (launchFacts !== undefined) custody.state.launchFacts = launchFacts;
      relabel($, custody.state, title);
      return;
    }
    close(custody.state);
    custody.state = null;
    custody.binding = null;
    const adapter = await adapterKey($, session);
    if (current !== custody.generation) return;
    if (!(backgroundDisabled.messages && backgroundDisabled.asks)) {
      let observed;
      try {
        ({observed} = await startNativeBinding($, chatTitle, launchFacts, adapter));
      } catch { /* The existing observer timer retries MCP readiness. */ }
      const currentSession = await $.session.id();
      if (observed?.session_id && observed.session_id !== currentSession) {
        await endNativeBinding($, observed, true);
        return;
      }
      if (current !== custody.generation) return;
      if (currentSession !== session) return;
      custody.binding = observed;
    }
    if (!(backgroundDisabled.messages && backgroundDisabled.asks) && typeof session === 'string' && session.length > 0 && session.length <= 256) {
      custody.state = {session, binding:custody.binding, launchFacts, adapter, submission: custody.submission, label: chatTitle, status: '', context: '', revision: '', notified: null, lastWake: -Infinity, highWake: '', wake: '', pendingUntil: 0, recoverUntil: 0, pointerText:'',
        asks: {wake: '', high: '', deliveredWake: '', waitingDelivered: '', waking: false, submitting: false},
        summary: null, deliveredWake: '', lastUrgent: -Infinity,
        idle: true, closed: false, paused: false, running: false, waking: false, submitting: false, unknownSubmit: false, timer: null};
      arm($, custody.state);
    }
  } catch {
    if (current === custody.generation) { close(custody.state); custody.state = null; }
    // Ordinary session startup stays usable without this companion.
  }
}

async function lifecycle($, event, next, custody) {
    await initialize($, event, custody);
    const state = custody.state;
    const admitted = await next(event);
    if (!state || state !== custody.state || state.closed || state.running || state.paused) return admitted;
    state.running = true;
    try {
      const page = await observe($, state, 0);
      if (state !== custody.state || state.closed || !(page.unread > 0) || page.pending_questions === undefined) return admitted;
      if (state.deliveredWake) return admitted;
      // Classic Mod results flatten hookSpecificOutput; contexts are an array.
      return withWakeContext(admitted, inboxSummary(page));
    } catch (error) { await failedObservation($, state, error); return admitted; }
    finally { state.running = false; arm($, state); }
}

function inboxSummary(page) {
  return wakePrompt.summary.replaceAll('{unread}', String(page.unread)).replaceAll('{pending_questions}', String(page.pending_questions));
}

function withWakeContext(admitted, hint) {
  return {...admitted, additionalContext:[...(admitted?.additionalContext || []),hint]};
}

// Consume the same admitted snapshot the background watch already obtained.
// Parallel tool completions arbitrate synchronously after their host checks.
function wakeContext(state, now, afterWait) {
  const page = state.summary;
  if (!page || page.context !== state.context) return '';
  const messages = !afterWait && !state.deliveredWake && !state.asks.hintedThisTurn && greaterWake(page.wake, state.highWake || '');
  const urgentReplies = messages && page.awaited_replies > 0;
  const asksEligible = !backgroundDisabled.asks && !state.deliveredWake && !state.asks.deliveredWake;
  const urgentAsks = asksEligible && !!page.asks_wake && page.waiting_asks > 0 && greaterWake(page.waiting_asks_wake || '', state.asks.waitingDelivered);
  const asks = urgentAsks || asksEligible && greaterWake(page.asks_wake, state.asks.high);
  const urgentAllowed = now !== null && now - state.lastUrgent >= urgentWakeIntervalMs;
  if ((urgentReplies || urgentAsks) && !urgentAllowed) return '';
  const hints = [];
  if (messages && (urgentReplies || page.unread > 0 && page.pending_questions !== undefined)) {
    state.deliveredWake = page.wake;
    hints.push(urgentReplies ? wakePrompt.awaited_replies.replaceAll('{awaited_replies}', String(page.awaited_replies)) : inboxSummary(page));
  }
  if (asks && (urgentAsks || page.answerable_asks > 0)) {
    state.asks.deliveredWake = page.asks_wake;
    state.asks.hintedThisTurn = true;
    if (urgentAsks) state.asks.waitingDelivered = page.waiting_asks_wake;
    hints.push(urgentAsks ? wakePrompt.waiting_asks.replaceAll('{waiting_asks}', String(page.waiting_asks))
      : wakePrompt.answerable_asks.replaceAll('{answerable_asks}', String(page.answerable_asks)));
  }
  if (urgentReplies || urgentAsks) state.lastUrgent = now;
  return hints.join(' ');
}

// In a catch handler next returns its held result when the hook already
// forwarded. An unforwarded OA pointer must retain the native validation gate.
function recoverGatingHook(event, next, pointerGate = false) {
  if (next.called) return next(event);
  if (pointerGate && event.origin?.kind === 'plugin' && event.origin.name === 'openabstractions') return {drop: 'OA wake validation unavailable'};
  return next(event);
}

export function register(on) {
  // A queued pointer retains custody until its host call reconciles.
  const custody = {state: null, generation: 0, submission: {state: null}};
  on('session.start', async ($, event, next) => {
    await initialize($, event, custody);
    return next(event);
  });
  // These in-process conversation changes end the old session without
  // reloading the mod or firing session.start again.
  on('classic.SessionStart', {source: ['startup', 'clear', 'resume', 'fork', 'compact']}, async ($, event, next) => {
    return lifecycle($, event, next, custody);
  }).catch(($, event, next) => recoverGatingHook(event, next));
  on('classic.UserPromptSubmit', async ($, event, next) => {
    return lifecycle($, event, next, custody);
  }).catch(($, event, next) => recoverGatingHook(event, next));
  on('classic.PostToolUse', async ($, event, next) => {
    const state = custody.state;
    const admitted = await next(event);
    if (!state?.summary || admitted?.block || admitted?.preventContinuation || event.session_id !== state.session || event.agent_id
      || await $.session.id() !== state.session) return admitted;
    const now = await clockNow($);
    if (state.submission.state || state !== custody.state || state.closed || state.paused || state.idle) return admitted;
    const hint = wakeContext(state, now, event.tool_name === waitTool);
    return hint ? withWakeContext(admitted, hint) : admitted;
  }).catch(($, event, next) => recoverGatingHook(event, next));
  on('session.end', async ($, event, next) => {
    custody.generation++;
    const binding = custody.state?.binding;
    close(custody.state);
    if (binding) await endNativeBinding($, binding);
    try { $.ui.status(undefined); } catch { /* The session is already closed. */ }
    return next(event);
  });
  on('turn.start', ($, event, next) => {
    if (custody.state && event.agentId === undefined) {
      custody.state.idle = false;
      custody.state.asks.hintedThisTurn = false;
    }
    return next(event);
  });
  on('turn.complete', ($, event, next) => {
    if (custody.state && event.agentId === undefined) {
      custody.state.idle = !event.isAborted;
      void signalTurn($, custody.state, 'ended');
      if (event.isAborted) { custody.state.paused = true; status($, custody.state, pausedStatus); cancelTimer(custody.state); }
      else { const state = custody.state; void wake($, state).then(() => wakeAsks($, state)); arm($, state); }
    }
    return next(event);
  });
  on('prompt.submit', async ($, event, next) => {
    const invocation = custody.state;
    try {
      if (event.origin?.kind === 'plugin' && event.origin.name === 'openabstractions') {
        const current = invocation;
        if (!current || custody.submission.state !== current) return {drop: 'OA wake belongs to an ended conversation'};
        const refuse = () => {
          if (current === custody.state) status($, current, 'background messages unavailable');
          close(current);
          return {drop: 'OA wake validation unavailable'};
        };
        const deliver = async () => { try { return await next(event); } catch { current.deliveryUnknown = true; return refuse(); } };
        if (event.text === asksPointer) {
          if (current.closed || current.paused || !current.idle || !current.asks.submitting || event.attachments?.length || event.context?.length) return {drop: 'OA Asks wake is no longer eligible'};
          let page;
          try { page = await observe($, current, 0); } catch (error) { await failedObservation($, current, error); return {drop: 'OA native session observation unavailable'}; }
          if (current !== custody.state || custody.submission.state !== current || current.closed || current.paused || !current.idle || !page.asks_wake || greaterWake(current.asks.high, page.asks_wake)) return {drop: 'OA Asks are no longer eligible'};
          return deliver();
        }
        if (current.closed || current.paused || !current.submitting || !validTicket(current.ticket) || event.text !== current.pointerText
          || event.attachments?.length || event.context?.length) return {drop: 'OA wake is no longer eligible'};
        try {
          let page = await observe($, current, 0);
          const now = await $.clock.now();
          if (current !== custody.state || custody.submission.state !== current || current.closed || current.paused || !current.idle || !page.wake || current.highWake !== current.reservedWake || !greaterWake(page.wake, current.reservedPrior || '') || greaterWake(current.reservedWake, page.wake)) return {drop: 'OA incoming message is no longer eligible'};
          page = await observe($, current, 0, {wake:page.wake,ticket:current.ticket});
          if (page.wake_ticket !== current.ticket || !page.wake || greaterWake(current.reservedWake,page.wake)) return {drop:'OA wake reservation is no longer current'};
          if (greaterWake(page.wake, current.reservedWake)) {
            if (!await claimWake($, current, page.wake)) return {drop: 'OA wake was already submitted'};
            current.reservedWake = page.wake;
          }
          if (current !== custody.state || current.closed || current.paused || !current.idle) return {drop: 'OA wake belongs to an ended conversation'};
          return deliver();
        } catch (error) {
          await failedObservation($, current, error);
          return {drop: 'OA native session observation unavailable'};
        }
      }
      const admitted = await next(event);
      if (invocation === custody.state && invocation && !invocation.closed && typeof admitted?.text === 'string' && !admitted.drop && event.origin?.kind === 'composer') {
        await signalTurn($, invocation, 'person');
        invocation.paused = false;
        arm($, invocation);
      }
      return admitted;
    } catch (error) {
      if (event.origin?.kind === 'plugin' && event.origin.name === 'openabstractions') {
        if (invocation === custody.state && invocation) status($, invocation, 'background messages unavailable');
        close(invocation);
        return {drop: 'OA wake validation unavailable'};
      }
      throw error;
    }
  }).catch(($, event, next) => recoverGatingHook(event, next, true));
}
