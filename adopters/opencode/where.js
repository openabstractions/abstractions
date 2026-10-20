/**
 * Where each reply ran, shown in OpenCode's TUI: a person always knows
 * whether their conversation stays on this computer. The OA provider puts the
 * router's word for the answering server on each text and tool-call part
 * (providerMetadata.openabstractions.placement, from the router's host
 * table); OpenCode keeps it on the part. This module shows the latest reply's
 * place right of the prompt, for each session. Every finished assistant reply
 * sets it: one without OA's metadata came from another provider. A reply
 * from a hosted server, or from a server the router could not place, raises
 * a warning toast.
 */

/** The text right of the prompt for a reply's place. */
export function placeText(where) {
  if (!where) return '';
  if (where.placement === 'local') return 'replies: this computer';
  if (where.placement === 'hosted') return `replies: hosted (${where.host || 'unknown server'})`;
  if (where.placement === 'unknown') return 'replies: place unknown';
  if (where.placement === 'none') return 'replies: not through OpenAbstractions';
  return '';
}

/** The warning toast for a reply that left, or may have left, this computer, or null. */
export function placeToast(where) {
  if (where?.placement === 'hosted') {
    return {variant: 'warning',
      message: `This reply came from a hosted server (${where.host || 'unknown'}): the conversation left this computer.`};
  }
  if (where?.placement === 'unknown') {
    return {variant: 'warning',
      message: `OpenAbstractions could not tell where this reply ran (${where.host || 'no server named'}); it may have left this computer.`};
  }
  return null;
}

/** The OA answer metadata on an OpenCode part, or null. */
export function partPlace(part) {
  const meta = part?.metadata?.openabstractions;
  if (!meta || (part.type !== 'text' && part.type !== 'tool')) return null;
  const placement = meta.placement === 'local' || meta.placement === 'hosted' ? meta.placement : 'unknown';
  return {sessionID: part.sessionID, messageID: part.messageID, placement, host: meta.host ?? ''};
}

/** The place of a finished assistant reply that carried no OA metadata, or
 * null for a message that is not a finished reply. A failed reply answered
 * nothing, so it leaves the indicator as it was. */
export function replyWithoutPlace(info) {
  if (info?.role !== 'assistant' || typeof info.time?.completed !== 'number' || info.error) return null;
  return {sessionID: info.sessionID, messageID: info.id, placement: 'none', host: ''};
}

/** The prompt-slot text node for a session, following the latest reply. The
 * imports resolve to OpenCode's own Solid runtime inside the TUI. */
async function placeSlot(api) {
  const [{createElement, insert, setProp}, {createSignal}] = await Promise.all([
    import('@opentui/solid'), import('solid-js'),
  ]);
  const [places, setPlaces] = createSignal({});
  const render = (_context, props) => {
    const node = createElement('text');
    const colour = () => {
      const placement = places()[props?.session_id]?.placement;
      if (placement === 'local') return api.theme?.current?.success;
      if (placement === 'none') return api.theme?.current?.textMuted;
      return api.theme?.current?.warning;
    };
    setProp(node, 'fg', colour());
    insert(node, () => placeText(places()[props?.session_id]));
    return node;
  };
  api.slots.register({order: 60, slots: {session_prompt_right: render}});
  return (where) => setPlaces((current) => ({...current, [where.sessionID]: where}));
}

/**
 * Install the place indicator and the warning toasts in an OpenCode TUI.
 * `dependencies.slot` replaces the Solid slot in tests.
 */
export async function installWhere(api, _options = {}, dependencies = {}) {
  const show = await (dependencies.slot ?? placeSlot)(api);
  // Messages whose parts carried OA's metadata, and the recent replies whose
  // place is settled (a finished message can be updated again).
  const placed = new Set();
  const settled = new Set();
  const toasted = new Set();
  const offPart = api.event?.on?.('message.part.updated', (event) => {
    const where = partPlace(event?.properties?.part);
    if (!where) return;
    placed.add(where.messageID);
    show(where);
    const toast = placeToast(where);
    if (toast && !toasted.has(where.messageID)) {
      toasted.add(where.messageID);
      api.ui?.toast?.(toast);
    }
  });
  const offMessage = api.event?.on?.('message.updated', (event) => {
    const info = event?.properties?.info;
    if (info?.role !== 'assistant' || typeof info.time?.completed !== 'number' || settled.has(info.id)) return;
    settled.add(info.id);
    if (settled.size > 256) settled.delete(settled.values().next().value);
    toasted.delete(info.id);
    if (placed.delete(info.id)) return;
    const where = replyWithoutPlace(info);
    if (where) show(where);
  });
  api.lifecycle?.onDispose?.(() => {
    offPart?.();
    offMessage?.();
  });
  return {show};
}
