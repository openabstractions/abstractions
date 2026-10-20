// This retained document schedules UI callbacks. The signed native host owns
// the shared AppModel/MCP core and validates activation before issuing a ticket.
import * as native from './sdk/abstraction-facade/javascript/js/abstraction/facade/applications/index.mjs';

const key = new URL(location.href).searchParams.get('k');
const headers = {'X-Panel-Key': key};
const frame = document.getElementById('panel-content');
const controller = new AbortController();
let active = true;
// Custom native schemes have the standard URL origin "null". Resolve against
// this retained document and compare its explicit scheme/host at completion.
const url = (path) => new URL(path, location.href);
const request = async (path, options = {}) => fetch(url(path), {...options,
  headers: {...headers, ...options.headers}, cache: 'no-store'});
const retire = () => { active = false; controller.abort(); };

async function callbackFault(id) {
  const answer = await request('/native-host/write?call=' + encodeURIComponent(id) + '&fault=1',
    {method: 'POST', headers: {'Content-Type': 'application/octet-stream'}, body: new Uint8Array()});
  if (answer.status !== 204) throw new Error('Native callback fault was refused');
}

async function* incoming() {
  while (active) {
    const response = await request('/native-host/read', {signal: controller.signal});
    if (response.status === 204) continue;
    if (!response.ok) throw new Error('Native mediation ended');
    const bytes = new Uint8Array(await response.arrayBuffer());
    if (bytes.length > 1048576) throw new Error('Native frame exceeds limit');
    const event = JSON.parse(new TextDecoder().decode(bytes));
    if (!event || typeof event.id !== 'string' || typeof event.request !== 'string' || !/^[0-9a-f]{1,16}$/.test(event.id) ||
        Object.keys(event).sort().join(',') !== 'id,request') throw new Error('Invalid native callback');
    let activation;
    try { activation = native.fromJson('ActivateRequest', new TextEncoder().encode(event.request)); }
    catch { await callbackFault(event.id); continue; }
    yield {id: event.id, activation};
  }
}

// The shared activation hook has already validated the original payload against
// the signed verb schema. The window and page supply their own readiness proof.
async function openCredentials() {
  const target = url('/credentials-page');
  target.searchParams.set('k', key);
    if (!frame.contentWindow || frame.contentWindow.location.pathname !== target.pathname) frame.src = target.href;
    const deadline = performance.now() + 10000;
    while (performance.now() < deadline) {
      const doc = frame.contentDocument;
      if (doc && frame.contentWindow?.location.protocol === target.protocol &&
          frame.contentWindow.location.host === target.host &&
          frame.contentWindow.location.pathname === target.pathname &&
          doc.documentElement.dataset.ready === '1') {
        const visible = await request('/native-host/visible');
        // Recheck after the asynchronous native UI-thread observation.
        if (visible.status === 204 && frame.contentDocument === doc &&
            doc.documentElement.dataset.ready === '1' &&
            frame.contentWindow?.location.protocol === target.protocol &&
            frame.contentWindow.location.host === target.host &&
            frame.contentWindow.location.pathname === target.pathname) {
          return {outcome: 'applied', reason: ''};
        }
        return {outcome: 'unavailable', reason: 'Panel window is unavailable'};
      }
      await new Promise(resolve => setTimeout(resolve, 25));
    }
  return {outcome: 'unavailable', reason: 'Credentials page is not ready'};
}

try {
  const response = await request('/native-host/bootstrap');
  if (!response.ok) throw new Error('Native instance is unavailable');
  // Issued callbacks retain their UI lifetime across input retirement. Their
  // final POST uses the original ticket/key and is never a new mediation call.
  const pending = new Set();
  try {
    for await (const event of incoming()) {
      const work = (async () => {
        let body;
        try {
          if (event.activation.verb !== 'open') throw new Error('Unknown Panel callback');
          body = native.toJson('HostActivationResult', await openCredentials());
        } catch { await callbackFault(event.id); return; }
        const answer = await request('/native-host/write?call=' + encodeURIComponent(event.id), {method: 'POST',
          headers: {'Content-Type': 'application/octet-stream'}, body});
        if (answer.status !== 204) throw new Error('Native callback retired');
      })();
      pending.add(work);
      work.finally(() => pending.delete(work)).catch(retire);
    }
  } finally {
    retire();
    await Promise.allSettled(pending);
  }
} catch {
  // EOF retires mediation. The ordinary content window remains user-owned.
  retire();
}
