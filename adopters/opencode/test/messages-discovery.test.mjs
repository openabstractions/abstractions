import test from 'node:test';
import assert from 'node:assert/strict';
import {tool} from '@opencode-ai/plugin/tool';
import {newInboxPage} from '@openabstractions/facade/messages';
import Plugin from '../plugin.js';
import {Availability} from '../availability.js';
import {defaultHelperTools} from '../tool-config.js';

const config = {provider: {openabstractions: {npm: '@openabstractions/opencode', models: {}}}};
const helperToolNames=[...defaultHelperTools];
async function fixture({bun = false, tools, probe = async () => false} = {}) {
  const prior = Object.getOwnPropertyDescriptor(globalThis, 'Bun');
  if (bun) Object.defineProperty(globalThis, 'Bun', {value: {}, configurable: true});
  else delete globalThis.Bun;
  let ready = false, effects = 0, probes = 0;
  const plugin = await Plugin({directory: 'owned', client: {session: {get: async ({path}) => ({data: {id: path.id, directory: 'owned', title: ''}})}}}, {tools: {generate_image: false, speak: false, oa_video_generate: false, ...tools}}, {
    tool, availability: new Availability(),
    probeRuntime: async () => { if (bun) throw Object.assign(new Error('startup unavailable'), {status: 'runtime_unavailable'}); },
    readInventory: async () => ({offers: []}), readCapability: async () => false, readAnswerable: async () => false,
    readMessaging: async (...args) => { probes++; return probe(...args); },
    requestApplicationRegistration: async () => false,
    resolveApplications: async () => ({announce: async () => {
      if (!ready) throw Object.assign(new Error('private transport detail'), {status: 3, transferred: 0});
      return {outcome: 'applied', instance: 'native'};
    }, withdraw: async () => ({outcome: 'applied'})}),
    resolveMessages: async () => ({inboxesList: async () => { effects++; return {...newInboxPage(), outcome: 'page'}; }}),
  });
  const close = async () => { await plugin.dispose(); if (prior) Object.defineProperty(globalThis, 'Bun', prior); else delete globalThis.Bun; };
  try { await plugin.config(structuredClone(config)); }
  catch (error) { await close(); throw error; }
  return {plugin, close, ready: () => { ready = true; }, effects: () => effects, probes: () => probes};
}

for (const bun of [false, true]) test(`canonical helper wrappers remain visible in ${bun ? 'Bun' : 'Node'} during startup outage`, async () => {
  const f = await fixture({bun});
  try {
    assert.deepEqual(Object.keys(f.plugin.tool).sort(), [...helperToolNames].sort());
    assert.equal(f.probes(), 0);
    await f.plugin.event({event: {type: 'session.created', properties: {info: {id: 'ses_own', directory: 'owned'}}}});
    assert.equal(typeof f.plugin.tool.oa_messages_inboxes_list.execute,'function');
    assert.equal(JSON.parse(await f.plugin.tool.oa_messages_inboxes_list.execute({}, {sessionID:'ses_own'})).outcome,'unavailable');
    assert.equal(f.effects(), 0);
    f.ready();
    assert.equal(f.effects(), 0);
    assert.deepEqual(Object.keys(f.plugin.tool).sort(), [...helperToolNames].sort());
  } finally { await f.close(); }
});

test('startup never waits for an obsolete discovery probe and explicit off stays off', async () => {
  const disabled = Object.fromEntries(helperToolNames.map(name => [name, false]));
  const f = await fixture({tools: disabled, probe: async () => { throw new Error('obsolete discovery probe'); }});
  try {
    assert.deepEqual(Object.keys(f.plugin.tool), []);
    assert.equal(f.probes(), 0);
  } finally { await f.close(); }
});
