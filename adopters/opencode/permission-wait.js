// Only decoded pre-effect service refusals enter this owner. Exceptions,
// accepted operation handles and unknown outcomes retain their original owner.
export const permissionWaitMs = 90000;
export function permissionQuestions(reason) {
  const ids = [];
  for (const word of typeof reason === 'string' ? reason.split(/\s+/) : []) {
    const id = word.startsWith('rights:question_pending:') ? word.slice('rights:question_pending:'.length) : '';
    if (id && !ids.includes(id)) ids.push(id);
  }
  return ids;
}

export function permissionVerdict(observed) {
  if (observed?.outcome === 'permitted') return observed.policyRevision ? 'allowed' : 'waiting';
  if (observed?.outcome === 'denied') return observed.policyRevision ? 'blocked' : 'waiting';
  if (observed?.outcome === 'pending') return 'pending';
  if (observed?.answer?.pending || observed?.outcome !== 'answered') return 'waiting';
  if (observed.answer?.option === 'never') return 'blocked';
  if (observed.answer?.option === 'refuse') return 'refused';
  return 'waiting';
}

export function permissionProjection(verdict, ids) {
  const outcomes = {refused: 'permission_refused', blocked: 'permission_blocked'};
  const messages = {
    refused: 'The person chose not to allow this for now; OpenAbstractions can ask again in 10 minutes.',
    blocked: 'The person blocked this; they can change it on the Permissions page of the OpenAbstractions Panel.',
  };
  return {outcome: outcomes[verdict] ?? 'awaiting_permission', question_ids: ids,
    message: messages[verdict] ?? 'OpenAbstractions is waiting for your permission. Open Questions in the OpenAbstractions Panel to answer the request, then continue this operation.'};
}

export async function resolveNativePermission(options = {}, signal) {
  const [{Machine}, {PermissionApplicationClient}, {runtimeConnector}] = await Promise.all([
    import('@openabstractions/facade'), import('@openabstractions/asks'), import('./runtime-connector.js'),
  ]);
  const machine = new Machine(options.runtimeEndpoint ?? null, {
    connector: await runtimeConnector(options.connector || undefined), timeout: 35000, cancellation: signal ?? null,
    server: options.server ?? null,
  });
  return new PermissionApplicationClient(await machine.resolveService('abstraction.asks/permission@1', {scope: 'local'}));
}

export async function waitForPermission(ids, {resolvePermission, client, signal, deadline = Date.now() + permissionWaitMs} = {}) {
  if (!ids.length || signal?.aborted) return 'waiting';
  try {
    const permission = client ?? await resolvePermission({signal});
    for (const id of ids) {
      for (;;) {
        const left = deadline - Date.now();
        if (left <= 0 || signal?.aborted) return 'waiting';
        const observed = (await permission.observeQuestion(id, BigInt(Math.min(25000, left))))?.observation;
        const verdict = permissionVerdict(observed);
        if (verdict === 'pending') continue; // The service owns the bounded wait.
        if (verdict === 'allowed') break;
        return verdict;
      }
    }
    return 'allowed';
  } catch { return 'waiting'; }
}

export async function invokeWithPermission(invoke, options = {}) {
  const result = await invoke(); // A lost reply is never replayed here.
  if (result?.outcome === 'accepted' || result?.operation || result?.request
      || !['not_permitted', 'forbidden'].includes(result?.outcome)
      || !/^rights:not_granted(?:\s|$)/.test(result?.reason ?? '')) return result;
  const ids = permissionQuestions(result.reason);
  if (!ids.length) return result;
  const verdict = await waitForPermission(ids, options);
  if (verdict !== 'allowed' || options.signal?.aborted) return permissionProjection(verdict, ids);
  const continued = await invoke(); // Exactly the same closure and request.
  const nextIds = ['not_permitted', 'forbidden'].includes(continued?.outcome) ? permissionQuestions(continued.reason) : [];
  return nextIds.length ? permissionProjection('waiting', nextIds) : continued;
}

export async function waitForPermissionRequest(observed, options = {}) {
  const verdict = permissionVerdict(observed);
  if (verdict === 'allowed' || verdict === 'blocked' || verdict === 'refused') return verdict;
  return waitForPermission(permissionObservationQuestionIDs(observed), options);
}

export function permissionObservationQuestionIDs(observed) {
  return typeof observed?.answer?.id==='string'&&observed.answer.id ? [observed.answer.id] : [];
}
