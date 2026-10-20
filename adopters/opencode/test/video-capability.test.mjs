import test from 'node:test';
import assert from 'node:assert/strict';
import OpenAbstractionsPlugin from '../plugin.js';
import {capabilityTools, defaultHelperTools, optionalTools} from '../tool-config.js';
import {tool as fakeTool} from '@opencode-ai/plugin/tool';

const config = () => ({provider: {openabstractions: {npm: '@openabstractions/opencode', options: {}, models: {}}}});

async function started(served) {
  const asked = [];
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async () => {}}}}, {}, {
    tool: fakeTool,
    probeRuntime: async () => true,
    readInventory: async () => ({offers: []}),
    readCapability: async (_options, profile) => { asked.push(profile); return served.includes(profile); },
  });
  await plugin.config(config());
  return {plugin, asked};
}

// The R35 acceptance: a local chat finds oa_video_generate on its first try
// while video is served, and a chat without video carries no video
// definition (architect, 2026-11-12).
test('oa_video_generate registers by default exactly while video is served', async () => {
  assert.equal(capabilityTools.oa_video_generate, 'video');
  assert.ok(!defaultHelperTools.includes('oa_video_generate'));
  assert.ok(!optionalTools.includes('oa_video_generate'));
  const served = await started(['video']);
  try {
    assert.ok(served.asked.includes('video'));
    assert.equal(typeof served.plugin.tool.oa_video_generate?.execute, 'function');
    assert.match(served.plugin.tool.oa_video_generate.description, /^Make a video \(with sound when the model supports it\) from a text prompt/);
    assert.ok(!served.plugin.tool.oa_inference_job_submit);
  } finally { await served.plugin.dispose(); }
  const absent = await started([]);
  try {
    assert.ok(!absent.plugin.tool.oa_video_generate);
    for (const name of defaultHelperTools) assert.ok(absent.plugin.tool[name], name);
  } finally { await absent.plugin.dispose(); }
});

test('the default helper set stays at 19 tools', () => {
  assert.equal(defaultHelperTools.length, 19, defaultHelperTools.join(', '));
});
