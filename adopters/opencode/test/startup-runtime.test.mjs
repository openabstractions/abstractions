import test from 'node:test';
import assert from 'node:assert/strict';
import {tool} from '@opencode-ai/plugin/tool';
import Plugin from '../plugin.js';
import {Availability} from '../availability.js';
import {capabilityTools, optionalTools} from '../tool-config.js';
import {messageToolNames} from '../messages.js';

for (const bun of [false, true]) {
  for (const available of [false, true]) {
    test(`${bun ? 'Bun' : 'Node'} startup uses the router probe before surveying (${available ? 'ready' : 'unavailable'})`, async () => {
      const prior = Object.getOwnPropertyDescriptor(globalThis, 'Bun');
      if (bun) Object.defineProperty(globalThis, 'Bun', {value: {}, configurable: true});
      else delete globalThis.Bun;
      const calls = [];
      let plugin;
      try {
        plugin = await Plugin({client: {}}, {}, {
          tool, env: {}, availability: new Availability(),
          resolveRouter: async () => {
            calls.push('router');
            if (!available) throw Object.assign(new Error('runtime unavailable'), {status: 'runtime_unavailable'});
            return {};
          },
          readInventory: async () => { calls.push('inventory'); return {offers: []}; },
          readSelectionPolicy: async () => ({outcome: 'unknown'}),
          readCapability: async () => false,
          readAnswerable: async () => false,
          requestApplicationRegistration: async () => false,
        });
        await plugin.config({provider: {openabstractions: {npm: '@openabstractions/opencode', models: {}}}});
        assert.equal(calls[0], 'router');
        assert.equal(calls.filter(call => call === 'router').length, 1);
        assert.equal(calls.includes('inventory'), available);
        // A helper capability tool registers guarded while OA is away and,
        // once OA answers, only when its profile is served (none is here).
        for (const name of messageToolNames) assert.equal(Boolean(plugin.tool[name]), !optionalTools.includes(name) && (!Object.hasOwn(capabilityTools, name) || !available), `canonical Messages tool ${name}`);
      } finally {
        await plugin?.dispose();
        if (prior) Object.defineProperty(globalThis, 'Bun', prior);
        else delete globalThis.Bun;
      }
    });
  }
}
