import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import test from 'node:test';
import OpenAbstractionsPlugin from '../plugin.js';
import {createImageTool, savePath} from '../image-tool.js';
import {ImageClient, ImageV2Client} from '@openabstractions/inference/protocol';

const bytes = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScL/nwAAAABJRU5ErkJggg==', 'base64');
const digest = `sha256:${createHash('sha256').update(bytes).digest('hex')}`;
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
  const tool = createImageTool({tool: fakeTool, resolveImage: async () => image, resolveReader: async () => reader});
  return {tool, calls};
}

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
  await assert.rejects(tool.execute({model: 'paint', prompt: 'cat', size: '512x512', workflow: 'arbitrary'}, {ask: async () => {}}), /Provide exactly one/);
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
    {ask: async () => {}}), /exactly one/);
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
    await assert.rejects(tool.execute(args, {ask: async () => {}}), /transport reached/);
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
    resolveImageClient: async (options, signal, contract) => { calls.push(['resolve', options.runtimeEndpoint, signal, contract]); return image; },
    resolveContentReader: async () => { throw new Error('unexpected content reader'); },
  });
  await plugin.config({provider: {'oa-fixture': {
    npm: '@openabstractions/opencode', options: {runtimeEndpoint: 'fixture-runtime', scope: 'local'}, models: {},
  }}});
  await assert.rejects(plugin.tool.openabstractions_generate_image.execute(
    {model: 'paint', prompt: 'cat', size: 'auto'},
    {ask: async () => {}, abort: controller.signal}), /interrupted/);
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
  const pending = tool.execute({model: 'paint', prompt: 'cat', size: 'auto'},
    {ask: async () => {}, abort: controller.signal});
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
  assert.match(asked[0].patterns[0], /^Edit one 1024x1024 image sha256:[0-9a-f]{12}/);
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
    await assert.rejects(tool.execute(args, {ask: async () => {}}), /Provide exactly one/);
    assert.deepEqual(calls, []);
  }
  const missing = fixture({reader: {async open() { return {outcome: 'unknown'}; }}});
  assert.match(await missing.tool.execute({model: 'paint', prompt: 'x', size: 'auto', inputImage: digest}, {ask: async () => {}}),
    /Input image unavailable: unknown/);
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

test('a not_granted refusal names the waiting question, a denial names the Permissions page, others keep their own line', async () => {
  const waiting = fixture({image: {async start() { return {outcome: 'not_permitted', reason: 'rights:not_granted output:forbidden'}; }}});
  assert.equal(await waiting.tool.execute({model: 'paint', prompt: 'cat', size: 'auto'}, {ask: async () => {}}),
    'Image generation refused: not_permitted (rights:not_granted output:forbidden). '
    + 'A question is waiting in the OpenAbstractions Panel; allow it there and retry.');
  const denied = fixture({image: {async start() { return {outcome: 'not_permitted', reason: 'rights:denied'}; }}});
  assert.equal(await denied.tool.execute({model: 'paint', prompt: 'cat', size: 'auto'}, {ask: async () => {}}),
    'Image generation refused: not_permitted (rights:denied). '
    + 'This program is blocked from doing that; to change that, allow it on the Permissions page of the OpenAbstractions Panel.');
  const unknown = fixture({image: {async start() { return {outcome: 'not_permitted', reason: 'rights:unknown_action'}; }}});
  assert.equal(await unknown.tool.execute({model: 'paint', prompt: 'cat', size: 'auto'}, {ask: async () => {}}),
    'Image generation refused: not_permitted (rights:unknown_action).');
});

// save_to writes the generated image inside the project, so a model does not
// loop on bash or write calls to save an attachment.
test('save_to writes the image inside the project and refuses anything else before generating', async () => {
  const {mkdtemp, mkdir, readFile, writeFile, rm, symlink} = await import('node:fs/promises');
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
    assert.match(saved.output, /Saved to assets\/cat\.png\.$/);
    assert.equal(saved.metadata.savedTo, 'assets/cat.png');
    assert.equal(saved.attachments.length, 1, 'the attachment stays');
    assert.deepEqual(await readFile(join(project, 'assets', 'cat.png')), bytes);
    assert.match(asked.at(-1), /and save it as assets\/cat\.png\.$/, 'the approval names the file');

    const kept = await run({save_to: 'assets/cat.png'});
    assert.equal(kept, 'Image not generated: assets/cat.png exists; set overwrite to true to replace it.');
    await writeFile(join(project, 'assets', 'cat.png'), 'old');
    const replaced = await run({save_to: 'assets/cat.png', overwrite: true});
    assert.match(replaced.output, /Saved to assets\/cat\.png\.$/);
    assert.deepEqual(await readFile(join(project, 'assets', 'cat.png')), bytes);

    for (const [saveTo, why] of [['../cat.png', /stay inside the project/], [join(root, 'cat.png'), /relative to the project/],
      ['C:/cat.png', /relative to the project/], ['assets/cat.txt', /\.png, \.jpg, \.jpeg or \.webp/], ['assets', /\.png/]]) {
      const {tool, calls} = fixture();
      assert.match(await tool.execute({model: 'paint', prompt: 'cat', size: 'auto', save_to: saveTo}, context), why, saveTo);
      assert.deepEqual(calls, [], `${saveTo} reaches no service`);
    }
    const typed = await run({save_to: 'cat.webp'});
    assert.match(typed.output, /Not saved: the image is image\/png, and cat\.webp names another type\.$/);
    await assert.rejects(run({overwrite: true}), /overwrite goes only with save_to/);
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
      assert.match(result.output, /Saved to .*cat\.png\.$/);
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
    assert.match(replaced.output, /Saved to hard\.png\.$/);
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
      assert.match(swapped.output, /Not saved: a folder on the way to sub\/deep\/cat\.png now leads outside the project\.$/);
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
