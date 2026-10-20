import {nativeChatFixture} from './native-chat-fixture.mjs';
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {mkdtempSync, rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join as joinPath} from 'node:path';
import test, {after} from 'node:test';
import OpenAbstractionsPlugin from '../plugin.js';
import {createImageTool, noPictureEngine, pictureConnectionPending, savePath} from '../image-tool.js';
import {createMessages} from '../messages.js';
import {createOpenAbstractions} from '../index.js';
import {ImageClient, ImageV2Client} from '@openabstractions/inference/protocol';
import {createFailureRecord} from '../failure-logs.js';

const bytes = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScL/nwAAAABJRU5ErkJggg==', 'base64');
const digest = `sha256:${createHash('sha256').update(bytes).digest('hex')}`;
// Every fixture saves its project pictures here, never in the person's Pictures.
const testPictures = mkdtempSync(joinPath(tmpdir(), 'oa-image-pictures-'));
after(() => rmSync(testPictures, {recursive: true, force: true}));
const delivery = {location: 'local', digest, mediaType: 'image/png', size: BigInt(bytes.length)};
function fakeTool(definition) { return definition; }
const schema = {optional() { return this; }};
fakeTool.schema = {string: () => schema, number: () => schema, boolean: () => schema,
  enum: () => schema, array: () => schema, object: () => schema};

function fixture(overrides = {}) {
  const calls = [];
  const image = {
    async start(request) { calls.push(['start', request]); return {outcome: 'accepted', operation: 'op'}; },
    async observe() { calls.push('observe'); return {outcome: 'page', next: 2n, atEnd: true, deltas: [
      {kind: 'image_result', imageResult: {index: 0n, delivery}},
      {kind: 'end', imageEnd: {outcome: 'completed', host: 'fixture', model: 'paint', deliveries: [delivery]}},
    ]}; },
    async cancel() { calls.push('cancel'); },
    ...overrides.image,
  };
  const reader = {
    async open(value) { calls.push(['open', value]); return {outcome: 'opened', resource: {handle: 'h', digest, size: BigInt(bytes.length)}}; },
    async read() { calls.push('read'); return {outcome: 'data', chunk: {offset: 0n, total: BigInt(bytes.length), data: bytes, eof: true}}; },
    async close() { calls.push('close'); },
    ...overrides.reader,
  };
  const created = createImageTool({tool: fakeTool, resolveImage: async () => image, resolveReader: async () => reader,
    failures: overrides.failures, resolvePermission: overrides.resolvePermission, delegate: overrides.delegate});
  // A picture for Pictures goes to the gateway's tool; these fixtures drive
  // the plugin's own image@1/image@2 path, which saves into the project, so
  // a call naming no save_to gets one.
  const tool = overrides.delegate ? created : {...created, execute: (args, context) => args && typeof args === 'object' && !('save_to' in args) && args.handle === undefined
    ? created.execute({...args, save_to: `own-${++ownSaves}.png`}, {directory: testPictures, ...context})
    : created.execute(args, context)};
  return {tool, calls};
}
let ownSaves = 0;

test('image first use waits for committed permission and keeps the original request', async () => {
  let first, starts=0, commit;
  const permitted=new Promise(resolve=>{commit=resolve;});
  const {tool}=fixture({image:{async start(request){starts++;if(!first)first=request;assert.equal(request,first);return starts===1?{outcome:'not_permitted',reason:'rights:not_granted rights:question_pending:q1'}:{outcome:'accepted',operation:'op'};}},
    resolvePermission:async()=>({async observeQuestion(){return permitted;}})});
  const work=tool.execute({model:'paint',prompt:'cat',size:'512x512'},{ask:async()=>{}});
  while(starts===0)await new Promise(resolve=>setImmediate(resolve));assert.equal(starts,1);
  commit({observation:{outcome:'permitted',policyRevision:'committed'}});
  const result=await work;assert.equal(result.metadata.operation,'op');assert.equal(starts,2);
});

test('OpenCode receives verified image bytes as an attachment through OA clients', async () => {
  const {tool, calls} = fixture();
  const result = await tool.execute({model: 'paint', prompt: 'cat', size: '512x512'}, {ask: async () => { calls.push('ask'); }});
  assert.equal(result.attachments.length, 1);
  assert.equal(result.attachments[0].mime, 'image/png');
  assert.deepEqual({host: result.metadata.host, model: result.metadata.model}, {host: 'fixture', model: 'paint'});
  assert.equal(result.metadata.operation, 'op');
  assert.deepEqual(Buffer.from(result.attachments[0].url.split(',')[1], 'base64'), bytes);
  assert.deepEqual(calls.slice(0, 4).map(x => Array.isArray(x) ? x[0] : x), ['ask', 'start', 'observe', 'open']);
  assert.equal(calls.at(-1), 'close');
});

test('a successful image attachment remains visible beside a text-only OA follow-up', async () => {
  const {tool, calls} = fixture();
  const result = await tool.execute({model: 'paint', prompt: 'cat', size: '512x512'}, {ask: async () => { calls.push('ask'); }});
  const model = createOpenAbstractions({scope: 'local'}).languageModel('text-only');
  const requests = [];
  model.finalChat = async () => null; // This fixture owns frozen chat@1 receiving.
  model.chat = async () => nativeChatFixture({async complete(request) {
    requests.push(request);
    return {outcome: 'completed', host: 'fixture', model: 'text-only',
      message: {role: 'assistant', parts: [{kind: 'text', text: 'The image is attached.'}]}};
  }});
  model.placementOf = async () => 'local';
  // OpenCode's text-only media transform keeps the session attachment and
  // sends an explicit text notice instead of image pixels to the provider.
  const answer = await model.doGenerate({prompt: [
    {role: 'tool', content: [{type: 'tool-result', toolCallId: 'image-call',
      output: {type: 'text', value: result.output}}]},
    {role: 'user', content: [{type: 'text', text: 'ERROR: Cannot read image (this model does not support image input). Inform the user.'}]},
  ]});
  assert.equal(answer.content[0].text, 'The image is attached.');
  assert.equal(requests.length, 1);
  assert.equal(requests[0].messages[0].parts[0].kind, 'tool_result');
  assert.match(requests[0].messages[0].parts[0].text, /^Saved to .*\.png\. /);
  assert.ok(requests[0].messages[0].parts[0].text.includes(`To edit it, pass inputImage ${digest} with save_to.`), 'the follow-up model reads the digest');
  assert.equal(JSON.stringify(requests[0], (_, value) => typeof value === 'bigint' ? String(value) : value).includes(bytes.toString('base64')), false);
  assert.deepEqual(Buffer.from(result.attachments[0].url.split(',')[1], 'base64'), bytes);
  assert.equal(calls[0], 'ask');
  await assert.rejects(model.doGenerate({prompt: [{role: 'user', content: [{type: 'image', image: bytes}]}]}), /does not support prompt part image/);
});

test('OpenCode refuses remote and mismatched content, and closes the reader', async () => {
  const remote = fixture({image: {async observe() { return {outcome: 'page', next: 2n, atEnd: true, deltas: [
    {kind: 'image_result', imageResult: {index: 0n, delivery: {...delivery, location: 'elsewhere'}}},
    {kind: 'end', imageEnd: {outcome: 'completed', deliveries: [{...delivery, location: 'elsewhere'}]}},
  ]}; }}});
  await assert.rejects(remote.tool.execute({model: 'paint', prompt: 'cat', size: 'auto'}, {ask: async () => {}}), /unsupported/);
  assert.equal(remote.calls.includes('read'), false);
  const altered = fixture({reader: {async read() { return {outcome: 'data', chunk: {
    offset: 0n, total: BigInt(bytes.length), data: Buffer.concat([bytes.subarray(0, -1), Buffer.from([0])]), eof: true,
  }}; }}});
  await assert.rejects(altered.tool.execute({model: 'paint', prompt: 'cat', size: 'auto'}, {ask: async () => {}}), /digest mismatch/);
  assert.equal(altered.calls.at(-1), 'close');
});

test('OpenCode refuses unsupported options before service resolution', async () => {
  const {tool, calls} = fixture();
  await assert.rejects(tool.execute({model: 'paint', prompt: 'cat', size: '512x512', workflow: 'arbitrary'}, {ask: async () => {}}),
    /^TypeError: Unknown argument workflow; allowed: prompt, size, model, selectionPolicy, save_to, overwrite, inputImage, mask, handle, wait_ms\.$/);
  assert.deepEqual(calls, []);
});

test('saved image policy uses image@2 with no inline credential or provider target', async () => {
  const {tool, calls} = fixture();
  await tool.execute({selectionPolicy: 'paint_local', prompt: 'cat', size: 'auto'}, {ask: async () => {}});
  const request = calls.find((call) => Array.isArray(call) && call[0] === 'start')[1];
  assert.equal(request.target.selectionPolicy, 'paint_local');
  assert.equal(request.target.model, '');
  assert.deepEqual({mode: request.content.mode, prompt: request.content.prompt, size: request.content.size,
    count: request.content.count, guarantees: request.content.guarantees, credential: request.content.credential},
  {mode: 'generate', prompt: 'cat', size: 'auto', count: 1n, guarantees: [], credential: ''});
  await assert.rejects(tool.execute({model: 'paint', selectionPolicy: 'paint_local', prompt: 'cat', size: 'auto'},
    {ask: async () => {}}), /model or selectionPolicy, not both/);
});

test('both image request shapes pass the generated wire serializer before dispatch', async () => {
  for (const [args, Client] of [
    [{model: 'paint', prompt: 'cat', size: '512x512'}, ImageClient],
    [{selectionPolicy: 'paint_local', prompt: 'cat', size: '512x512'}, ImageV2Client],
  ]) {
    let encoded = false;
    const client = new Client({async exchangeFrame() { encoded = true; throw new Error('transport reached'); }});
    const tool = createImageTool({tool: fakeTool, resolveImage: async () => client,
      resolveReader: async () => { throw new Error('unexpected read'); }});
    await assert.rejects(tool.execute({...args, save_to: 'shapes.png'}, {directory: testPictures, ask: async () => {}}), /transport reached/);
    assert.equal(encoded, true);
  }
});

test('registered OpenCode image tool resolves the configured runtime and cancels an interrupted operation', async () => {
  const calls = [];
  const controller = new AbortController();
  const image = {
    async start() { return {outcome: 'accepted', operation: 'op'}; },
    async observe() { controller.abort(); throw new Error('interrupted'); },
    async cancel(operation) { calls.push(['cancel', operation]); },
  };
  const plugin = await OpenAbstractionsPlugin({client: {}}, {providerId: 'oa-fixture', tools: {generate_image: true}}, {
    tool: fakeTool, readInventory: async () => ({offers: []}),
    readCapability: async () => false,
    probeRuntime: async () => ({}),
    requestOpenCodeConnection: async () => ({outcome:'connected',hostRegistered:true,helperEnrolled:true}),
    resolveImageClient: async (options, signal, contract) => { calls.push(['resolve', options.runtimeEndpoint, signal, contract]); return image; },
    resolveContentReader: async () => { throw new Error('unexpected content reader'); },
  });
  await plugin.config({provider: {'oa-fixture': {
    npm: '@openabstractions/opencode', options: {runtimeEndpoint: 'fixture-runtime', scope: 'local'}, models: {},
  }}});
  await assert.rejects(plugin.tool.oa_image_generate.execute(
    {model: 'paint', prompt: 'cat', size: 'auto', save_to: 'registered.png'},
    {directory: testPictures, ask: async () => {}, abort: controller.signal}), /interrupted/);
  assert.deepEqual(calls[0], ['resolve', 'fixture-runtime', controller.signal, 'abstraction.inference/image@1']);
  assert.deepEqual(calls[1], ['resolve', 'fixture-runtime', null, 'abstraction.inference/image@1']);
  assert.deepEqual(calls[2], ['cancel', 'op']);
});

test('abort while resolving an image client never starts paid work', async () => {
  const controller = new AbortController();
  let release;
  let enter;
  const gate = new Promise((resolve) => { release = resolve; });
  const entered = new Promise((resolve) => { enter = resolve; });
  let started = 0;
  const tool = createImageTool({tool: fakeTool, resolveImage: async () => {
    enter();
    await gate;
    return {async start() { started++; }};
  }, resolveReader: async () => { throw new Error('unexpected reader'); }});
  const pending = tool.execute({model: 'paint', prompt: 'cat', size: 'auto', save_to: 'abort.png'},
    {directory: testPictures, ask: async () => {}, abort: controller.signal});
  await entered;
  controller.abort();
  release();
  assert.equal(await pending, 'Image generation cancelled.');
  assert.equal(started, 0);
});

test('an input image digest sends image@1 edit with its sniffed media type and an optional PNG mask', async () => {
  const jpeg = Buffer.from([255, 216, 255, 224, 0, 16, 74, 70, 73, 70]);
  const jpegDigest = `sha256:${createHash('sha256').update(jpeg).digest('hex')}`;
  const contents = new Map([[jpegDigest, jpeg], [digest, bytes]]);
  const asked = [];
  const {tool, calls} = fixture({reader: {
    async open(value) { calls.push(['open', value]); const data = contents.get(value) ?? bytes;
      return {outcome: 'opened', resource: {handle: value, digest: value, size: BigInt(data.length)}}; },
    async read(handle) { const data = contents.get(handle) ?? bytes;
      return {outcome: 'data', chunk: {offset: 0n, total: BigInt(data.length), data, eof: true}}; },
  }});
  const result = await tool.execute({model: 'paint', prompt: 'snow on the rocks', size: '1024x1024', inputImage: jpegDigest, mask: digest},
    {ask: async (request) => { asked.push(request); }});
  const request = calls.find((call) => Array.isArray(call) && call[0] === 'start')[1];
  assert.deepEqual({mode: request.mode, imageDigest: request.imageDigest, imageMediaType: request.imageMediaType,
    maskDigest: request.maskDigest, maskMediaType: request.maskMediaType, model: request.model, prompt: request.prompt},
  {mode: 'edit', imageDigest: jpegDigest, imageMediaType: 'image/jpeg', maskDigest: digest, maskMediaType: 'image/png',
    model: 'paint', prompt: 'snow on the rocks'});
  assert.equal(Object.keys(request.extensions).length, 0, 'the tool names no vendor extension');
  assert.match(asked[0].patterns[0], /^Edit one 1024x1024 picture sha256:[0-9a-f]{12}/);
  assert.equal(asked[0].metadata.inputImage, jpegDigest);
  assert.equal(result.attachments.length, 1);
  const startAt = calls.findIndex((call) => Array.isArray(call) && call[0] === 'start');
  assert.deepEqual(calls.slice(0, startAt).filter((call) => Array.isArray(call)).map((call) => call[1]), [jpegDigest, digest],
    'input and mask are opened before the call starts');

  const policy = fixture();
  await policy.tool.execute({selectionPolicy: 'paint_local', prompt: 'snow', size: 'auto', inputImage: digest}, {ask: async () => {}});
  const content = policy.calls.find((call) => Array.isArray(call) && call[0] === 'start')[1].content;
  assert.deepEqual({mode: content.mode, imageDigest: content.imageDigest, imageMediaType: content.imageMediaType, maskDigest: content.maskDigest},
    {mode: 'edit', imageDigest: digest, imageMediaType: 'image/png', maskDigest: ''});
});

test('the tool description states the INF-I1 mask meaning', () => {
  const {tool} = fixture();
  assert.match(tool.description, /mask[^.]*PNG[^.]*input's pixel size[^.]*fully transparent pixels mark the area to change/);
});

test('edit inputs are refused before paid work when malformed, missing or of the wrong type', async () => {
  for (const args of [
    {model: 'paint', prompt: 'x', size: 'auto', inputImage: 'C:/pictures/cat.png'},
    {model: 'paint', prompt: 'x', size: 'auto', inputImage: digest.toUpperCase()},
    {model: 'paint', prompt: 'x', size: 'auto', mask: digest},
  ]) {
    const {tool, calls} = fixture();
    await assert.rejects(tool.execute(args, {ask: async () => {}}), /inputImage must be|mask must be/);
    assert.deepEqual(calls, []);
  }
  const missing = fixture({reader: {async open() { return {outcome: 'unknown'}; }}});
  assert.match(await missing.tool.execute({model: 'paint', prompt: 'x', size: 'auto', inputImage: digest}, {ask: async () => {}}),
    /^That picture was saved in Pictures; leave out save_to to edit it\.$/);
  assert.equal(missing.calls.some((call) => Array.isArray(call) && call[0] === 'start'), false);
  const jpeg = Buffer.from([255, 216, 255, 224, 0, 16]);
  const jpegMask = fixture({reader: {async read() { return {outcome: 'data', chunk: {offset: 0n, total: BigInt(bytes.length), data: jpeg, eof: false}}; }}});
  assert.match(await jpegMask.tool.execute({model: 'paint', prompt: 'x', size: 'auto', inputImage: digest, mask: digest}, {ask: async () => {}}),
    /mask must be a PNG/);
  assert.equal(jpegMask.calls.some((call) => Array.isArray(call) && call[0] === 'start'), false);
});

test('terminal refusal does not cancel an already ended operation', async () => {
  const {tool, calls} = fixture({image: {async observe() { return {outcome: 'page', next: 1n, atEnd: true,
    deltas: [{kind: 'end', imageEnd: {outcome: 'unsupported_feature', reason: 'feature:image'}}]}; }}});
  const result = await tool.execute({model: 'paint', prompt: 'cat', size: 'auto'}, {ask: async () => {}});
  assert.match(result, /unsupported_feature/);
  assert.equal(calls.includes('cancel'), false);
});

test('a refusal naming its question says it waits, a bare not_granted names the Permissions page, a denial too, others keep their own line', async () => {
  const waiting = fixture({image: {async start() { return {outcome: 'not_permitted', reason: 'rights:not_granted output:forbidden rights:question_pending:q1'}; }}});
  const pending = JSON.parse(await waiting.tool.execute({model: 'paint', prompt: 'cat', size: 'auto'}, {ask: async () => {}}));
  assert.equal(pending.outcome, 'awaiting_permission');
  assert.deepEqual(pending.question_ids, ['q1']);
  assert.match(pending.message, /waiting for your permission/);
  const none = fixture({image: {async start() { return {outcome: 'not_permitted', reason: 'rights:not_granted output:forbidden'}; }}});
  assert.equal(await none.tool.execute({model: 'paint', prompt: 'cat', size: 'auto'}, {ask: async () => {}}),
    'Image generation refused: not_permitted (rights:not_granted output:forbidden). '
    + 'No question is waiting; allow it on the Permissions page of the OpenAbstractions Panel.');
  const denied = fixture({image: {async start() { return {outcome: 'not_permitted', reason: 'rights:denied'}; }}});
  assert.equal(await denied.tool.execute({model: 'paint', prompt: 'cat', size: 'auto'}, {ask: async () => {}}),
    'Image generation refused: not_permitted (rights:denied). '
    + 'This program is blocked from doing that; to change that, allow it on the Permissions page of the OpenAbstractions Panel.');
  const unknown = fixture({image: {async start() { return {outcome: 'not_permitted', reason: 'rights:unknown_action'}; }}});
  assert.equal(await unknown.tool.execute({model: 'paint', prompt: 'cat', size: 'auto'}, {ask: async () => {}}),
    'Image generation refused: not_permitted (rights:unknown_action).');
});

test('with no provider serving image@1 the tool says to add Lemonade in Setup, for a model or a saved policy', async () => {
  const failures = createFailureRecord({clock: () => new Date('2026-11-09T12:00:00.000Z')});
  for (const args of [{model: 'flux2-klein-4b', prompt: 'cat', size: 'auto'}, {selectionPolicy: 'pictures', prompt: 'cat', size: 'auto'}]) {
    const {tool, calls} = fixture({failures, image: {async start() { return {outcome: 'no_host', reason: 'router:no-host'}; }}});
    assert.equal(await tool.execute(args, {ask: async () => {}}), 'Pictures are not set up yet. Open Catalog to add them.');
    assert.equal(calls.includes('observe'), false);
  }
  // A router that has picture providers and no host for this model keeps its own words.
  const {tool} = fixture({image: {async start() { return {outcome: 'no_host', reason: 'router:not-here'}; }}});
  assert.equal(await tool.execute({model: 'paint', prompt: 'cat', size: 'auto'}, {ask: async () => {}}),
    'Image generation refused: no_host (router:not-here).');
});

test('an OA refusal or failed end is recorded for the failure-log tool; a cancel is not', async () => {
  const failures = createFailureRecord({clock: () => new Date('2026-10-31T12:00:00.000Z')});
  const refused = fixture({failures, image: {async start() { return {outcome: 'unavailable', reason: 'engine:none'}; }}});
  assert.equal(await refused.tool.execute({model: 'paint', prompt: 'cat', size: 'auto'}, {ask: async () => {}}),
    'Image generation refused: unavailable (engine:none). To see what OpenAbstractions recorded about it, call oa_logs_read_failure with failureId f1.');
  const ended = fixture({failures, image: {async observe() { return {outcome: 'page', next: 1n, atEnd: true,
    deltas: [{kind: 'end', imageEnd: {outcome: 'failed', reason: 'engine:crashed', host: 'lemonade'}}]}; }}});
  assert.match(await ended.tool.execute({model: 'paint', prompt: 'cat', size: 'auto'}, {ask: async () => {}}), /failureId f2\.$/);
  const cancelled = fixture({failures, image: {async observe() { return {outcome: 'page', next: 1n, atEnd: true,
    deltas: [{kind: 'end', imageEnd: {outcome: 'cancelled', reason: 'caller'}}]}; }}});
  assert.equal(await cancelled.tool.execute({model: 'paint', prompt: 'cat', size: 'auto'}, {ask: async () => {}}), 'Image generation cancelled: caller.');
  assert.deepEqual(failures.list().map(({id, operation, host, outcome}) => [id, operation, host, outcome]),
    [['f1', 'image', '', 'unavailable'], ['f2', 'image', 'lemonade', 'failed']]);
});

// save_to writes the generated image inside the project, so a model does not
// loop on bash or write calls to save an attachment.
test('save_to writes the image inside the project and refuses anything else before generating', async () => {
  const {mkdtemp, mkdir, readFile, writeFile, rm, symlink, realpath} = await import('node:fs/promises');
  const {tmpdir} = await import('node:os');
  const {join} = await import('node:path');
  const root = await mkdtemp(join(tmpdir(), 'oa-image-save-'));
  const project = join(root, 'project');
  await mkdir(project);
  try {
    const asked = [];
    const context = {directory: project, ask: async (request) => { asked.push(request.patterns[0]); }};
    const run = (extra) => fixture().tool.execute({model: 'paint', prompt: 'cat', size: '512x512', ...extra}, context);

    const saved = await run({save_to: 'assets/cat.png'});
    assert.equal(saved.output, `Saved to ${join(await realpath(project), 'assets', 'cat.png')}. To edit it, pass inputImage ${digest} with save_to.`);
    assert.equal(saved.metadata.savedTo, 'assets/cat.png');
    assert.equal(saved.attachments.length, 1, 'the attachment stays');
    assert.deepEqual(await readFile(join(project, 'assets', 'cat.png')), bytes);
    assert.match(asked.at(-1), /and save it as assets\/cat\.png\.$/, 'the approval names the file');

    const kept = await run({save_to: 'assets/cat.png'});
    assert.equal(kept, 'Image not generated: assets/cat.png exists; set overwrite to true to replace it.');
    await writeFile(join(project, 'assets', 'cat.png'), 'old');
    const replaced = await run({save_to: 'assets/cat.png', overwrite: true});
    assert.equal(replaced.output, `Saved to ${join(await realpath(project), 'assets', 'cat.png')}. To edit it, pass inputImage ${digest} with save_to.`);
    assert.deepEqual(await readFile(join(project, 'assets', 'cat.png')), bytes);

    for (const [saveTo, why] of [['../cat.png', /stay inside the project/], [join(root, 'cat.png'), /relative to the project/],
      ['C:/cat.png', /relative to the project/], ['assets/cat.txt', /\.png, \.jpg, \.jpeg or \.webp/], ['assets', /\.png/]]) {
      const {tool, calls} = fixture();
      assert.match(await tool.execute({model: 'paint', prompt: 'cat', size: 'auto', save_to: saveTo}, context), why, saveTo);
      assert.deepEqual(calls, [], `${saveTo} reaches no service`);
    }
    const typed = await run({save_to: 'cat.webp'});
    assert.equal(typed.output, `Not saved: the image is image/png, and cat.webp names another type. To edit it, pass inputImage ${digest} with save_to.`);
    await assert.rejects(run({overwrite: true, save_to: undefined}), /overwrite is true or false and goes only with save_to/);
    assert.match(await fixture().tool.execute({model: 'paint', prompt: 'cat', size: 'auto', save_to: 'x.png'}, {ask: async () => {}}),
      /the project directory is unknown/);

    // A link inside the project that leads out of it is refused.
    const outside = join(root, 'outside');
    await mkdir(outside);
    let linked = true;
    try { await symlink(outside, join(project, 'out'), 'junction'); } catch { linked = false; }
    if (linked) assert.match(await run({save_to: 'out/cat.png'}), /stay inside the project/);
  } finally {
    // The link goes first, on its own, so removing the folders never follows it.
    const {unlink, rmdir} = await import('node:fs/promises');
    await unlink(join(project, 'out')).catch(() => rmdir(join(project, 'out'))).catch(() => {});
    await rm(root, {recursive: true, force: true});
  }
});

test('save_to refuses a distinct NTFS Unicode-twin sibling before generating', async () => {
  const {mkdtemp, mkdir, rm, stat} = await import('node:fs/promises');
  const {tmpdir} = await import('node:os');
  const {join} = await import('node:path');
  const parent = await mkdtemp(join(tmpdir(), 'oa-image-unicode-twin-'));
  const project = join(parent, 'link');
  const twin = join(parent, 'lin\u212a');
  await mkdir(project);
  try {
    try { await mkdir(twin); } catch (error) {
      if (error?.code !== 'EEXIST') throw error;
    }
    const [projectIdentity, twinIdentity] = await Promise.all([stat(project, {bigint: true}), stat(twin, {bigint: true})]);
    const sameDirectory = projectIdentity.dev === twinIdentity.dev && projectIdentity.ino !== 0n
      && projectIdentity.ino === twinIdentity.ino;
    const preflight = await savePath(project, '../lin\u212a/cat.png');
    if (sameDirectory) assert.equal(preflight.refused, undefined);
    else assert.equal(preflight.refused, 'save_to must stay inside the project');

    const {tool, calls} = fixture();
    const result = await tool.execute({model: 'paint', prompt: 'cat', size: 'auto', save_to: '../lin\u212a/cat.png'},
      {directory: project, ask: async () => {}});
    if (sameDirectory) {
      assert.match(result.output, /Saved to .*cat\.png\. To edit it, pass inputImage sha256:[0-9a-f]{64} with save_to.$/);
    } else {
      assert.equal(result, 'Image not generated: save_to must stay inside the project.');
      assert.deepEqual(calls, [], 'a distinct sibling reaches no service');
    }
  } finally {
    await rm(parent, {recursive: true, force: true});
  }
});

// Review of save_to: a hard link, a junction swapped in during the approval,
// and an NTFS alternate data stream never write outside the project.
test('save_to never writes outside the project through a hard link, a late junction or a stream', async () => {
  const {mkdtemp, mkdir, readFile, writeFile, rm, link, symlink, readdir, unlink, rmdir} = await import('node:fs/promises');
  const {tmpdir} = await import('node:os');
  const {join} = await import('node:path');
  const root = await mkdtemp(join(tmpdir(), 'oa-image-escape-'));
  const project = join(root, 'project');
  const away = join(root, 'away');
  await mkdir(project);
  await mkdir(away);
  let junction = '';
  try {
    // A hard link inside the project to a file outside it: overwrite renames a
    // new file onto the name and leaves the outside file as it was.
    await writeFile(join(away, 'shared.png'), 'outside');
    await link(join(away, 'shared.png'), join(project, 'hard.png'));
    const context = {directory: project, ask: async () => {}};
    const run = (extra, use = context) => fixture().tool.execute({model: 'paint', prompt: 'cat', size: 'auto', ...extra}, use);
    const replaced = await run({save_to: 'hard.png', overwrite: true});
    assert.match(replaced.output, /^Saved to .*hard\.png\. To edit it, pass inputImage sha256:[0-9a-f]{64} with save_to.$/);
    assert.equal(await readFile(join(away, 'shared.png'), 'utf8'), 'outside', 'the outside file is unchanged');
    assert.deepEqual(await readFile(join(project, 'hard.png')), bytes);
    assert.deepEqual((await readdir(project)).filter((name) => name.endsWith('.oa-tmp')), [], 'no temporary file stays');

    // A junction swapped in while the person reads the approval.
    let linked = true;
    const late = {directory: project, ask: async () => {
      try { await symlink(away, join(project, 'sub'), 'junction'); junction = join(project, 'sub'); } catch { linked = false; }
    }};
    const swapped = await run({save_to: 'sub/deep/cat.png'}, late);
    if (linked) {
      assert.equal(swapped.output, `Not saved: a folder on the way to sub/deep/cat.png now leads outside the project. To edit it, pass inputImage ${digest} with save_to.`);
      assert.deepEqual((await readdir(away)).sort(), ['shared.png'], 'nothing is written outside');
    }

    // A colon names a drive or an alternate data stream.
    for (const saveTo of ['README.md:hidden.png', 'notes:x.png', 'C:cat.png']) {
      const {tool, calls} = fixture();
      assert.match(await tool.execute({model: 'paint', prompt: 'cat', size: 'auto', save_to: saveTo}, context),
        /save_to (must not contain a colon|must be a path relative to the project)/, saveTo);
      assert.deepEqual(calls, []);
    }
  } finally {
    // The junction goes first, on its own, so removing the folders never follows it.
    if (junction) await unlink(junction).catch(() => rmdir(junction)).catch(() => {});
    await rm(root, {recursive: true, force: true});
  }
});

// PICTURES-STORAGE amendments (coordinator, 2026-11-10): a picture for the person's Pictures folder is
// made by the gateway's oa_image_generate, through this chat's helper. The
// plugin answers with the gateway's sentence and attaches the saved file.
function gatewayAnswer(path) {
  return {outcome: 'saved', path, digest, message: `Saved to ${path}. To edit it, pass inputImage ${digest}.`};
}

test('a prompt-only call goes to the gateway tool and answers its sentence with the picture attached', async () => {
  const {mkdtemp, writeFile, rm} = await import('node:fs/promises');
  const folder = await mkdtemp(joinPath(tmpdir(), 'oa-image-delegated-'));
  try {
    const path = joinPath(folder, 'image-20261110-140509.png');
    await writeFile(path, bytes);
    const asked = [], forwarded = [];
    const {tool, calls} = fixture({delegate: async (args, context) => { forwarded.push([args, context.sessionID]); return gatewayAnswer(path); }});
    const result = await tool.execute({prompt: 'a fox'}, {sessionID: 'ses_A', ask: async (request) => { asked.push(request.patterns[0]); }});
    assert.equal(result.output, `Saved to ${path}. To edit it, pass inputImage ${digest}.`);
    assert.deepEqual(result.metadata, {digest, path, savedTo: '', mediaType: 'image/png', bytes: bytes.length});
    assert.deepEqual(Buffer.from(result.attachments[0].url.split(',')[1], 'base64'), bytes);
    assert.deepEqual(forwarded, [[{prompt: 'a fox', size: 'auto'}, 'ses_A']]);
    assert.deepEqual(asked, ['Make one picture with your picture model and save it in Pictures/OpenAbstractions.']);
    assert.deepEqual(calls, [], 'the plugin starts no image@1 or image@2 work of its own');
    // A named model and size travel as they are.
    await tool.execute({prompt: 'a fox', model: 'paint', size: '512x512'}, {sessionID: 'ses_A', ask: async () => {}});
    assert.deepEqual(forwarded[1][0], {prompt: 'a fox', size: '512x512', model: 'paint'});
  } finally {
    await rm(folder, {recursive: true, force: true});
  }
});

// A picture belongs to the program identity that saved it; an edit across
// identities names the other way to call.
test('an edit across program identities names the call that can read the picture', async () => {
  for (const outcome of ['forbidden', 'unknown']) {
    const {tool} = fixture({delegate: async () => ({outcome, message: `Input image unavailable: ${outcome}.`})});
    assert.equal(await tool.execute({prompt: 'x', inputImage: digest}, {ask: async () => {}}),
      'That picture was saved in the project; pass save_to to edit it.');
    const project = fixture({reader: {async open() { return {outcome}; }}});
    assert.equal(await project.tool.execute({prompt: 'x', inputImage: digest, save_to: 'cat.png'}, {directory: tmpdir(), ask: async () => {}}),
      'That picture was saved in Pictures; leave out save_to to edit it.');
    assert.equal(project.calls.some((call) => Array.isArray(call) && call[0] === 'start'), false);
  }
});

test('a handle continues without asking, and the gateway answers pass through', async () => {
  const forwarded = [];
  const answers = [
    {outcome: 'working', handle: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', message: 'Still making the picture. Call oa_image_generate with handle "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" to wait for it.'},
    {outcome: 'awaiting_permission', question_ids: ['q1'], message: 'OpenAbstractions is waiting for your permission.'},
    {outcome: 'not_set_up', message: 'Pictures are not set up yet.'},
    {outcome: 'unavailable', reason: 'native_helper_unavailable'},
  ];
  const {tool} = fixture({delegate: async (args) => { forwarded.push(args); return answers.shift(); }});
  const ask = async () => { throw new Error('a continuation asks nothing'); };
  assert.equal(await tool.execute({handle: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}, {ask}),
    'Still making the picture. Call oa_image_generate with handle "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" to wait for it.');
  assert.deepEqual(forwarded, [{handle: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}]);
  assert.equal(JSON.parse(await tool.execute({handle: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}, {ask})).outcome, 'awaiting_permission');
  assert.equal(await tool.execute({handle: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}, {ask}), 'Pictures are not set up yet.');
  assert.equal(await tool.execute({handle: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}, {ask}), pictureConnectionPending);
  const none = fixture();
  assert.equal(await none.tool.execute({handle: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}, {ask}), pictureConnectionPending);
});

// R35: a client that fills defaults sends size auto, or empty and null
// fields, beside the handle; the continuation goes on with handle and wait_ms.
test('a handle with filled-in defaults continues', async () => {
  const forwarded = [];
  const {tool} = fixture({delegate: async (args) => { forwarded.push(args); return {outcome: 'working', handle: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', message: 'Still making the picture.'}; }});
  const ask = async () => { throw new Error('a continuation asks nothing'); };
  assert.equal(await tool.execute({handle: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', size: 'auto', model: '', mask: null, wait_ms: 5000}, {ask}), 'Still making the picture.');
  assert.deepEqual(forwarded, [{handle: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', wait_ms: 5000}]);
});

test('a handle goes alone, and an edit or a saved policy without save_to goes to the gateway', async () => {
  const {tool, calls} = fixture({delegate: async () => { throw new Error('a refused call reaches the gateway'); }});
  await assert.rejects(tool.execute({handle: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', prompt: 'x'}, {ask: async () => {}}),
    /^TypeError: prompt describes a new picture, and handle continues one already started: to wait for it, call oa_image_generate with the handle and optionally wait_ms; to start another, leave out handle\.$/);
  await assert.rejects(tool.execute({handle: 'short'}, {ask: async () => {}}), /handle must be the handle an earlier/);
  assert.deepEqual(calls, []);
  const forwarded = [], asked = [];
  const created = createImageTool({tool: fakeTool, resolveImage: async () => { throw new Error('unexpected image@1'); }, resolveReader: async () => null,
    delegate: async (args) => { forwarded.push(args); return {outcome: 'saved', path: '', digest, message: 'Saved.'}; }});
  const ask = async (request) => { asked.push(request.patterns[0]); };
  await created.execute({prompt: 'snow', inputImage: digest, mask: digest}, {ask});
  await created.execute({prompt: 'snow', selectionPolicy: 'paint_local', size: '512x512'}, {ask});
  assert.deepEqual(forwarded, [{prompt: 'snow', size: 'auto', inputImage: digest, mask: digest},
    {prompt: 'snow', size: '512x512', selectionPolicy: 'paint_local'}]);
  assert.match(asked[0], /^Edit one picture sha256:[0-9a-f]{12}\.\.\. with your picture model and save it in Pictures\/OpenAbstractions\.$/);
  assert.equal(asked[1], 'Make one 512x512 picture with saved policy "paint_local" and save it in Pictures/OpenAbstractions.');
});

test('the delegated call reaches the chat helper\'s oa_image_generate and gives the same answer', async () => {
  const {mkdtemp, writeFile, rm} = await import('node:fs/promises');
  const folder = await mkdtemp(joinPath(tmpdir(), 'oa-image-helper-'));
  const path = joinPath(folder, 'image-20261110-140509.png');
  await writeFile(path, bytes);
  const helperCalls = [];
  const messages = createMessages({tool: fakeTool, directory: 'owned', environment: {}, helperProgram: () => 'C:/kept/oa-opencode-integration.exe',
    client: {session: {get: async (request) => ({data: {id: request.path.id, directory: 'owned', title: ''}}),
      promptAsync: async () => { throw new Error('idle waking is off'); }}},
    open: ({id}) => ({initialize: async () => ({}), isClosed: () => false, close: async () => {},
      async call(name, args) {
        helperCalls.push([name, args]);
        if (name === 'oa_opencode_session_start') return {structuredContent: {outcome: 'observed', session_id: id, context: 'canonical-' + id}};
        if (name === 'oa_image_generate') return {structuredContent: gatewayAnswer(path), content: [{type: 'text', text: gatewayAnswer(path).message}]};
        return {structuredContent: {outcome: 'page'}};
      }})});
  try {
    const tool = createImageTool({tool: fakeTool, resolveImage: async () => { throw new Error('unexpected image@1'); },
      resolveReader: async () => { throw new Error('unexpected reader'); },
      delegate: (args, context) => messages.call('oa_image_generate', args, context)});
    const result = await tool.execute({prompt: 'a fox'}, {sessionID: 'ses_A', ask: async () => {}});
    assert.equal(result.output, gatewayAnswer(path).message);
    assert.deepEqual(helperCalls.filter(([name]) => name === 'oa_image_generate'), [['oa_image_generate', {prompt: 'a fox', size: 'auto'}]]);
  } finally {
    await messages.close();
    await rm(folder, {recursive: true, force: true});
  }
});

// Review N4: the picture default is image@2 and waits for Allow like image@1.
test('an image@2 default call waits on its question and is accepted after Allow', async () => {
  let starts = 0, commit;
  const permitted = new Promise((resolve) => { commit = resolve; });
  const {tool, calls} = fixture({image: {async start(request) { starts++; calls.push(['start', request]);
    return starts === 1 ? {outcome: 'not_permitted', reason: 'rights:not_granted rights:question_pending:q1'} : {outcome: 'accepted', operation: 'op'}; }},
  resolvePermission: async () => ({async observeQuestion(id) { assert.equal(id, 'q1'); return permitted; }})});
  const work = tool.execute({prompt: 'a fox'}, {ask: async () => {}});
  while (starts === 0) await new Promise((resolve) => setImmediate(resolve));
  assert.equal(starts, 1);
  commit({observation: {outcome: 'permitted', policyRevision: 'committed'}});
  const result = await work;
  assert.equal(starts, 2);
  assert.equal(result.metadata.operation, 'op');
  assert.equal(calls.find((call) => Array.isArray(call) && call[0] === 'start')[1].target.selectionPolicy, 'oa-image-default');
});

test('a bad size is refused before anything is asked or started, naming the allowed values', async () => {
  for (const size of ['1024', 'big', '1024 x 1024', '0x512', '']) {
    const {tool, calls} = fixture();
    await assert.rejects(tool.execute({prompt: 'a fox', size}, {ask: async () => { calls.push('ask'); }}),
      /^TypeError: size must be auto or WIDTHxHEIGHT such as 512x512; leave it out for auto\.$/, size);
    assert.deepEqual(calls, [], `${size} reaches no service`);
  }
  const {tool, calls} = fixture();
  await assert.rejects(tool.execute({size: 'auto'}, {ask: async () => {}}), /^TypeError: prompt is required: describe the picture\.$/);
  assert.deepEqual(calls, []);
});

test('a size the engine does not make is answered with its sizes and the next call', async () => {
  const atStart = fixture({image: {async start() { return {outcome: 'unsupported_feature', reason: 'native:size:512x512'}; }}});
  assert.equal(await atStart.tool.execute({prompt: 'a fox', size: '1024x1024'}, {ask: async () => {}}),
    'This picture model does not make 1024x1024. Allowed sizes: auto, 512x512. Leave size out for auto.');
  const atEnd = fixture({image: {async observe() { return {outcome: 'page', next: 1n, atEnd: true, deltas: [
    {kind: 'end', imageEnd: {outcome: 'unsupported_feature', reason: 'native:size'}}]}; }}});
  assert.equal(await atEnd.tool.execute({prompt: 'a fox', size: '1024x1024'}, {ask: async () => {}}),
    'This picture model does not make 1024x1024. Allowed sizes: auto. Leave size out for auto.');
});

test('with no picture default stored the prompt-only call says to open Setup', async () => {
  const {tool} = fixture({image: {async start() { return {outcome: 'no_host', reason: 'selection:unknown'}; }}});
  assert.equal(await tool.execute({prompt: 'a fox'}, {ask: async () => {}}), noPictureEngine);
});

test('the tool schema needs only prompt and states size auto as the default', () => {
  const descriptions = {};
  const field = (kind) => {
    const value = {kind, required: true, optional() { this.required = false; return this; },
      describe(text) { this.description = text; return this; }};
    return value;
  };
  const describingTool = (definition) => definition;
  describingTool.schema = {string: () => field('string'), boolean: () => field('boolean'), number: () => field('number')};
  const tool = createImageTool({tool: describingTool, resolveImage: async () => null, resolveReader: async () => null});
  for (const [name, value] of Object.entries(tool.args)) descriptions[name] = value;
  assert.deepEqual(Object.keys(tool.args).filter((name) => tool.args[name].required), ['prompt']);
  assert.match(descriptions.size.description, /^auto \(default\): the model's own size; or WIDTHxHEIGHT such as 512x512\.$/);
  assert.match(tool.description, /Only prompt is needed/);
  assert.ok(tool.description.length < 600, `description is ${tool.description.length} characters`);
});

test('wait_ms goes to the gateway with a new picture and with a handle', async () => {
  const forwarded = [];
  const {tool} = fixture({delegate: async (args) => { forwarded.push(args); return {outcome: 'working', handle: 'h', message: 'Making the picture: 3 s so far. Call oa_image_generate with handle "h" to wait for it.'}; }});
  await tool.execute({prompt: 'a fox', wait_ms: 90000}, {ask: async () => {}});
  await tool.execute({handle: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA', wait_ms: 90000}, {ask: async () => {}});
  assert.equal(forwarded[0].wait_ms, 90000);
  assert.deepEqual(forwarded[1], {handle: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA', wait_ms: 90000});
  await assert.rejects(tool.execute({prompt: 'a fox', wait_ms: -1}, {ask: async () => {}}), /wait_ms must be a whole number/);
});
