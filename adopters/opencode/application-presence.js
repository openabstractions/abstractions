import {runtimeConnector} from './runtime-connector.js';
import {randomUUID} from 'node:crypto';

export async function resolveApplications(options, signal) {
  const [{Machine}, {ApplicationsClient}] = await Promise.all([
    import('@openabstractions/facade'), import('@openabstractions/facade/protocol'),
  ]);
  const machine = new Machine(options.runtimeEndpoint ?? null, {
    connector: await runtimeConnector(), timeout: 2000, cancellation: signal,
    server: options.server ?? null,
  });
  return new ApplicationsClient(await machine.resolveService('abstraction.facade/applications@2', {scope: 'local'}));
}

// Host lifecycle events supply context identity. Tool arguments never enter here.
// The service binds native identity and enrollment; this owner never registers.
// A forbidden announce (this program is not registered as the app) calls
// refused once per owner, which asks the Panel to register the integration;
// the person confirms there.
export function createApplicationPresence({directory, resolveClient, schedule = setTimeout, unschedule = clearTimeout, refused}) {
  const contexts = new Set();
  const chats = new Map();
  let instance = '', timer, stopped = false, running = false, dirty = false, reported = false;
  let pending = Promise.resolve();
  const cancelTimer = () => { if (timer !== undefined) unschedule(timer); timer = undefined; };
  const presence = () => ({application: 'opencode', instance, interfaces: [],
    contexts: [...contexts].sort().flatMap(name => {
      const chat = chats.get(name);
      return chat ? [{name: chat.source, title: '', revision: ''}, ...(chat.context ? [{name: chat.context, title: '', revision: ''}] : [])]
        : [{name, title: '', revision: ''}];
    }), leaseMs: 30000n});
  // Reserve the full service handle bound before the first empty-instance call.
  const fits = value => Buffer.byteLength(JSON.stringify({...value, instance: 'x'.repeat(128)},
    (_, v) => typeof v === 'bigint' ? Number(v) : v)) <= 2048;
  async function synchronize() {
    running = true;
    try {
      while (dirty) {
        dirty = false;
        cancelTimer();
        if ((stopped || contexts.size === 0) && !instance) continue;
        const client = await resolveClient(AbortSignal.timeout(2000));
        if (stopped || contexts.size === 0) {
          if (instance) await client.withdraw('opencode', instance);
          instance = '';
          continue;
        }
        const result = await client.announce(presence());
        if (result?.outcome === 'applied' && typeof result.instance === 'string' && result.instance) {
          instance = result.instance;
          if (!stopped && !dirty) {
            timer = schedule(() => { timer = undefined; request(); }, 15000);
            timer?.unref?.();
          }
        } else {
          // Stale means a later host event may obtain a fresh epoch. A refusal
          // does not create a retry loop or a claim of current presence.
          instance = '';
          if (result?.outcome === 'forbidden' && !reported && typeof refused === 'function') {
            reported = true;
            try { void Promise.resolve(refused()).catch(() => {}); } catch {}
          }
        }
      }
    } catch {
      instance = '';
      cancelTimer();
    } finally {
      running = false;
      if (dirty) request();
    }
  }
  function request() {
    dirty = true;
    if (!running) pending = synchronize();
    return pending;
  }
  function observeSession(info) {
    if (stopped || typeof directory !== 'string' || !directory || info?.directory !== directory) return;
    const id = info.id;
    if (typeof id !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(id)) return;
    if (!contexts.has(id) && contexts.size >= 32) return;
    contexts.add(id);
    if (!fits(presence())) { contexts.delete(id); return; }
    return request();
  }
  return {
    // An exact-session SDK readback from a host tool context also observes a
    // resumed chat whose creation event preceded this plugin instance.
    observeSession,
    async chatSource(id) {
      if (stopped || !contexts.has(id)) return null;
      if (!chats.has(id)) {
        chats.set(id, {source: randomUUID(), context: ''});
        if (!fits(presence())) { chats.delete(id); return null; }
        await request();
      } else await pending;
      const chat = chats.get(id);
      return instance && chat && contexts.has(id) && !stopped ? {instance, context: chat.source} : null;
    },
    async declareChat(id, context) {
      const chat = chats.get(id);
      if (stopped || !chat || typeof context !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(context)
        || chat.context && chat.context !== context) return false;
      const prior = chat.context;
      chat.context = context;
      if (!fits(presence())) { chat.context = prior; return false; }
      await request();
      return !stopped && contexts.has(id) && chats.get(id) === chat && !!instance;
    },
    event({event} = {}) {
      if (event?.type === 'server.instance.disposed' && event.properties?.directory === directory) return this.close();
      if (stopped || !['session.created', 'session.updated', 'session.deleted'].includes(event?.type)) return;
      const id = event.properties?.info?.id;
      if (typeof directory !== 'string' || !directory || event.properties?.info?.directory !== directory) return;
      if (typeof id !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(id)) return;
      if (event.type === 'session.deleted') { contexts.delete(id); chats.delete(id); }
      else return observeSession(event.properties.info);
      return request();
    },
    close() { stopped = true; contexts.clear(); chats.clear(); cancelTimer(); return request(); },
    /** An OpenCode instance that adopts this presence after a reload
     * (reload-handover.js) binds its own runtime connection; the epoch and
     * the sessions it announces stay. */
    rebind(next) {
      if (next.resolveClient) resolveClient = next.resolveClient;
      if (next.refused) refused = next.refused;
    },
    settled() { return pending; },
    binding(context) { return !stopped && contexts.has(context) ? instance : ''; },
  };
}
