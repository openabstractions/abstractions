// configure.js writes OpenCode's own configuration from the router's offers
// (moved from the runtime's `openabstractions opencode configure`, VISION
// 2026-10-06 move step 6). These tests hold its projection to the shared
// inventory table, its merge to the person's own settings, its headings,
// profile and Zen choice, its TUI registration and its whole-file writes.
import test from 'node:test';
import assert from 'node:assert/strict';
import {existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, symlinkSync, writeFileSync, lstatSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {
  adapter, adapterOffersTUI, apply, choices, ConfigureError, fileURL, headings, localBuildPrompt, localSwitch, main,
  mergeConfig, outputBudget, packageSpecifier, placementFor, profileFor, project, providerPackage, replaceConfig,
  routerRefusal, selectionAlias,
} from '../configure.js';

const table = JSON.parse(readFileSync(new URL('./fixtures/inventory-context.json', import.meta.url), 'utf8'));

function temp(t) {
  const dir = mkdtempSync(join(tmpdir(), 'oa-configure-'));
  t.after(() => rmSync(dir, {recursive: true, force: true}));
  return dir;
}

const offers = (rows) => rows.map((row) => ({family: row.family, names: row.names ?? [], servers: row.servers ?? [],
  context: BigInt(row.context ?? 0), contextSource: row.contextSource, input: BigInt(row.input ?? 0), unknown: row.unknown ?? []}));

const testModels = () => ({'llama3.1:8b': {name: 'llama3.1', limit: {context: 32768, output: outputBudget}, tool_call: true}});

function io() {
  const sink = {out: '', err: ''};
  return {sink, io: {out: (text) => { sink.out += text; }, err: (text) => { sink.err += text; }}};
}

function app(path, fields = {}) {
  return {command: 'configure', path, providerID: 'openabstractions', scope: 'local', guarantee: 'abstraction.inference/local-only@1',
    models: testModels(), ...fields};
}

function run(application, deps) {
  const {sink, io: streams} = io();
  apply(application, streams, deps);
  return sink;
}

const read = (path) => JSON.parse(readFileSync(path, 'utf8'));
const at = (doc, ...path) => path.reduce((value, name) => (value && typeof value === 'object' ? value[name] : undefined), doc);

function localAdapter(t, tui = true) {
  const dir = temp(t);
  const exports = {'./server': {default: './plugin.js'}, ...(tui ? {'./tui': {default: './tui.js'}} : {})};
  writeFileSync(join(dir, 'package.json'), JSON.stringify({name: providerPackage, exports}));
  return adapter(dir);
}

function pluginTuple(entry) {
  assert.ok(Array.isArray(entry) && entry.length === 2, `not a [name, options] tuple: ${JSON.stringify(entry)}`);
  return {id: entry[0], options: entry[1]};
}

const provider = (fields = {}) => ({name: 'OpenAbstractions', npm: providerPackage, models: testModels(),
  options: {scope: 'local', guarantees: ['abstraction.inference/local-only@1'], ...fields}});

// ---- Projection ----

test('the projection holds to the shared inventory table', () => {
  assert.equal(table.outputBudget, outputBudget);
  for (const c of table.cases) {
    const isLocal = c.localServers ? (server) => c.localServers.includes(server) : undefined;
    const {models, unusable, warnings} = project(offers(c.offers), isLocal);
    assert.deepEqual(models, c.models, c.name);
    const observed = {...unusable};
    for (const [family, model] of Object.entries(models)) observed[family] = model.limit.context;
    assert.deepEqual(observed, c.observed, c.name);
    assert.equal(warnings.length, c.withheld.length, c.name);
    for (const name of c.withheld) assert.ok(warnings.some((w) => w.includes(JSON.stringify(name))), `${c.name}: ${name}`);
  }
});

test('an unknown context withholds only its own family and names the alias', () => {
  const {models, unusable, warnings} = project(offers([
    {family: 'mixed', names: ['lemonade-auto', 'ollama-qwen'], servers: ['lemonade', 'ollama'], contextSource: 'unknown',
      unknown: ['lemonade/lemonade-auto']},
    {family: 'independent', names: ['ollama-independent'], servers: ['ollama'], context: 16384, contextSource: 'effective', input: 12288},
  ]));
  assert.ok(!models.mixed && models.independent.limit.context === 16384 && !Object.keys(unusable).length);
  assert.equal(warnings.length, 1);
  assert.match(warnings[0], /"lemonade\/lemonade-auto".*reported no request limit/);
});

test('the chooser lists exactly the families configure writes, with their servers', () => {
  const rows = offers([
    {family: 'qwen3:8b', names: ['qwen3'], servers: ['ollama', 'lemonade'], context: 32768, contextSource: 'effective', input: 28672},
    {family: 'llama3.1:8b', names: ['llama3.1'], servers: ['ollama'], context: 8192, contextSource: 'maximum', input: 4096},
    {family: 'small', names: ['small'], servers: ['ollama'], context: 4096, contextSource: 'effective', input: 0},
    {family: 'unserved', names: ['unserved'], context: 8192, contextSource: 'effective', input: 4096},
  ]);
  const listed = choices(rows);
  assert.deepEqual(listed, {warnings: [], models: [
    {family: 'llama3.1:8b', name: 'llama3.1:8b · 8k · this computer: ollama', context: 8192, servers: ['ollama']},
    {family: 'qwen3:8b', name: 'qwen3:8b · 32k · this computer: ollama, lemonade', context: 32768, servers: ['ollama', 'lemonade']},
  ]});
});

test('the placement follows the scope, and router refusals name the Panel or the update', () => {
  assert.equal(placementFor('local'), 'local');
  assert.equal(placementFor('remote'), 'any');
  const forbidden = routerRefusal({code: 'forbidden'});
  assert.equal(forbidden.exitCode, 3);
  assert.match(forbidden.message, /abstraction\.router\/inventory\.read.*Panel/);
  const older = routerRefusal({code: 'unknown_method'});
  assert.equal(older.exitCode, 3);
  assert.ok(older.message.includes(table.olderRuntime));
  assert.equal(routerRefusal(new Error('pipe closed')).exitCode, 1);
});

// ---- Writing the provider block ----

test('configure writes into an empty folder, explains omitted models, and repeats unchanged', (t) => {
  const path = join(temp(t), 'opencode.json');
  const application = app(path, {unusableModels: {'small-local': 4096}});
  const first = run(application);
  assert.match(first.err, /small-local=4096 tokens/);
  assert.match(first.out, /configured provider/);
  const doc = read(path);
  assert.equal(doc.model, 'openabstractions/llama3.1:8b');
  const mine = doc.provider.openabstractions;
  assert.equal(mine.npm, providerPackage);
  assert.equal(mine.name, headings.local);
  assert.equal(mine.options.scope, 'local');
  assert.deepEqual(mine.models['llama3.1:8b'], {name: 'llama3.1', limit: {context: 32768, output: outputBudget}, tool_call: true});
  const raw = readFileSync(path, 'utf8');
  const again = run(application);
  assert.match(again.err, /small-local=4096 tokens/);
  assert.match(again.out, /unchanged/);
  assert.equal(readFileSync(path, 'utf8'), raw);
});

test('configure writes the block the startup refresh warns about and prints no stale-limit warning', (t) => {
  const c = table.configure;
  const path = join(temp(t), 'opencode.json');
  writeFileSync(path, JSON.stringify({provider: {openabstractions: c.existing}}));
  const {models, unusable, warnings} = project(offers(c.offers));
  const sink = run(app(path, {scope: c.scope, models, unusableModels: unusable, inventoryWarnings: warnings}));
  assert.ok(!sink.err.includes('limit.context'));
  const written = read(path).provider.openabstractions;
  assert.equal(written.options.scope, c.configured.options.scope);
  assert.deepEqual(written.options.oaInventoryManagedModels, c.configured.options.oaInventoryManagedModels);
  assert.deepEqual(written.models, c.configured.models);
});

test('an unusable default the config already selects is refused and the file kept', (t) => {
  const path = join(temp(t), 'opencode.json');
  const existing = '{"model":"openabstractions/small-local","mcp":{"keep":true}}';
  writeFileSync(path, existing);
  assert.throws(() => run(app(path, {unusableModels: {'small-local': 4096}})), (error) => error.exitCode === 3);
  assert.equal(readFileSync(path, 'utf8'), existing);
});

test('configure merges beside existing providers and MCP keys and keeps the selected model', (t) => {
  const path = join(temp(t), 'opencode.json');
  writeFileSync(path, JSON.stringify({provider: {anthropic: {name: 'Anthropic', npm: '@ai-sdk/anthropic'}},
    mcp: {playwright: {type: 'local'}}, model: 'anthropic/claude'}));
  run(app(path));
  const doc = read(path);
  assert.equal(doc.model, 'anthropic/claude');
  assert.ok(doc.mcp.playwright && doc.provider.anthropic && doc.provider.openabstractions);
});

test('only untouched managed models are refreshed; customizations stay', (t) => {
  const local = temp(t);
  writeFileSync(join(local, 'package.json'), JSON.stringify({name: providerPackage}));
  const entryURL = fileURL(join(local, 'index.js'));
  const generated = {name: 'old generated', limit: {context: 32768, output: 4096}, tool_call: true};
  const existing = JSON.stringify({model: 'openabstractions/stale', plugin: ['custom-plugin'], provider: {openabstractions: {
    name: 'My OA', npm: entryURL, customProviderField: true,
    options: {scope: 'remote', credential: 'deepseek', runtimeEndpoint: 'chosen-runtime', customOption: 'preserve',
      oaInventoryManagedModels: {stale: generated, edited: generated}},
    models: {stale: generated, edited: {...generated, name: 'my edited name'}, hand: {name: 'hand-authored', limit: {context: 65536, output: 4096}}},
  }}});
  const {doc} = mergeConfig(existing, 'openabstractions', {...provider(), models: {new: {name: 'new model', limit: {context: 8192, output: 4096}, tool_call: true}}},
    'openabstractions/new');
  const block = doc.provider.openabstractions;
  const tuple = pluginTuple(doc.plugin[1]);
  assert.ok(doc.plugin.length === 2 && doc.plugin[0] === 'custom-plugin' && tuple.id === providerPackage && tuple.options.providerId === 'openabstractions');
  assert.ok(block.name === 'My OA' && block.npm === entryURL && block.customProviderField === true);
  for (const [key, want] of Object.entries({credential: 'deepseek', runtimeEndpoint: 'chosen-runtime', customOption: 'preserve'})) {
    assert.equal(block.options[key], want);
  }
  assert.ok(!Object.hasOwn(block.models, 'stale'));
  assert.equal(block.models.edited.name, 'my edited name');
  assert.equal(block.models.hand.name, 'hand-authored');
  assert.equal(block.models.new.name, 'new model');
  assert.equal(doc.model, 'openabstractions/stale');
  assert.ok(!Object.hasOwn(block.options.oaInventoryManagedModels, 'stale'));
});

test('a legacy block without the marker keeps its models and credential', () => {
  const {doc} = mergeConfig('{"provider":{"openabstractions":{"npm":"@openabstractions/opencode","options":{"credential":"keep"},"models":{"legacy":{"name":"keep"}}}}}',
    'openabstractions', provider(), 'openabstractions/new');
  assert.equal(doc.provider.openabstractions.models.legacy.name, 'keep');
  assert.equal(doc.provider.openabstractions.options.credential, 'keep');
});

test('a provider ID owned by another package, by no package, or by a forged path is refused', (t) => {
  const forged = join(temp(t), 'adopters', 'opencode', 'index.js');
  for (const existing of [
    '{"provider":{"openabstractions":{"npm":"@other/provider","models":{"keep":{"name":"keep"}}}}}',
    '{"provider":{"openabstractions":{"name":"OpenAbstractions","models":{"keep":{"name":"keep"}}}}}',
    JSON.stringify({provider: {openabstractions: {npm: forged, options: {oaInventoryManagedModels: {}}, models: {keep: {name: 'keep'}}}}}),
  ]) {
    assert.throws(() => mergeConfig(existing, 'openabstractions', provider(), 'openabstractions/new'), /choose a different --provider-id/);
  }
});

test('a local adapter must name the package and export ./server', (t) => {
  const dir = temp(t);
  writeFileSync(join(dir, 'package.json'), JSON.stringify({name: providerPackage, exports: {'./server': {default: './plugin.js'}}}));
  const {pluginEntry, providerPackage: npm} = adapter(dir);
  assert.equal(pluginEntry, dir);
  const url = new URL(npm);
  assert.ok(url.protocol === 'file:' && url.host === '' && url.pathname.endsWith('/index.js'));
  for (const body of [{name: '@someone/else', exports: {'./server': './plugin.js'}}, {name: providerPackage, exports: {'.': './index.js'}}]) {
    writeFileSync(join(dir, 'package.json'), JSON.stringify(body));
    assert.throws(() => adapter(dir), (error) => error.exitCode === 2);
  }
  assert.throws(() => adapter('file://remote.invalid/share/opencode'));
  if (process.platform === 'win32') assert.throws(() => adapter('\\\\server\\share\\opencode'));
});

test('a file URL escapes spaces and names no host', (t) => {
  const got = fileURL(join(temp(t), 'space dir', 'index.js'));
  assert.ok(got.includes('space%20dir'));
  assert.equal(new URL(got).host, '');
});

test('an existing exact pin is kept, the provider follows it, and a repeat does not duplicate', () => {
  for (const deceptive of ['@file:../foreign', '@https://example.invalid/foreign.tgz', '@github:someone/foreign']) {
    assert.ok(!packageSpecifier(providerPackage + deceptive));
  }
  const pinned = `${providerPackage}@0.4.0`;
  const {doc} = mergeConfig(JSON.stringify({plugin: ['my-plugin', pinned]}), 'openabstractions', provider(), 'openabstractions/new', providerPackage);
  const tuple = pluginTuple(doc.plugin[1]);
  assert.ok(doc.plugin.length === 2 && doc.plugin[0] === 'my-plugin' && tuple.id === pinned && tuple.options.providerId === 'openabstractions');
  assert.equal(doc.provider.openabstractions.npm, pinned);
  const again = mergeConfig(JSON.stringify(doc), 'openabstractions', provider(), 'openabstractions/new', providerPackage).doc;
  assert.equal(again.plugin.length, 2);
});

test('an existing plugin tuple is bound to the provider ID and keeps its options', () => {
  const {doc} = mergeConfig(JSON.stringify({plugin: [[`${providerPackage}@0.4.0`, {providerId: 'old-id', custom: 'keep'}]]}),
    'oa-custom', provider(), 'oa-custom/new', providerPackage);
  assert.equal(doc.plugin.length, 1);
  const tuple = pluginTuple(doc.plugin[0]);
  assert.ok(tuple.id === `${providerPackage}@0.4.0` && tuple.options.providerId === 'oa-custom' && tuple.options.custom === 'keep');
  assert.equal(doc.provider['oa-custom'].npm, tuple.id);
});

test('an explicit local adapter replaces only the OA registration and points the provider at it', (t) => {
  const {pluginEntry: local} = localAdapter(t, false);
  const pinned = `${providerPackage}@0.4.0`;
  const {doc} = mergeConfig(JSON.stringify({plugin: ['custom-plugin', pinned], model: 'elsewhere/model',
    provider: {openabstractions: {npm: pinned, options: {credential: 'keep'}, models: {legacy: {name: 'keep'}}}}}),
  'openabstractions', provider(), 'openabstractions/new', local);
  const tuple = pluginTuple(doc.plugin[1]);
  assert.ok(doc.plugin[0] === 'custom-plugin' && tuple.id === local && tuple.options.providerId === 'openabstractions');
  assert.equal(doc.provider.openabstractions.npm, fileURL(join(local, 'index.js')));
  assert.ok(doc.model === 'elsewhere/model' && doc.provider.openabstractions.options.credential === 'keep' && doc.provider.openabstractions.models.legacy);
  const again = mergeConfig(JSON.stringify(doc), 'openabstractions', provider(), 'openabstractions/new', local).doc;
  assert.equal(again.plugin.length, 2);
});

test('--model selects the chosen family and refuses one not servable now', (t) => {
  const path = join(temp(t), 'opencode.json');
  const existing = '{"model":"other/kept-until-chosen","mcp":{"keep":true}}';
  writeFileSync(path, existing);
  const models = {...testModels(), 'qwen3:8b': {name: 'qwen3', limit: {context: 32768, output: outputBudget}, tool_call: true}};
  assert.throws(() => run(app(path, {models, chosenModel: 'absent'})), (error) => error.exitCode === 4 && /configure\.js models/.test(error.message));
  assert.equal(readFileSync(path, 'utf8'), existing);
  run(app(path, {models, chosenModel: 'qwen3:8b'}));
  const doc = read(path);
  assert.ok(doc.model === 'openabstractions/qwen3:8b' && doc.mcp);
  assert.match(run(app(path, {models, chosenModel: 'qwen3:8b'})).out, /unchanged/);
});

test('--dry-run prints the block and writes nothing; no servable model writes nothing and says why', (t) => {
  const dir = temp(t);
  const path = join(dir, 'opencode.json');
  const {pluginEntry, providerPackage: npm} = localAdapter(t);
  const dry = run(app(path, {scope: 'remote', guarantee: 'abstraction.inference/hosted-allowed@1', dryRun: true, pluginEntry, providerPackage: npm,
    unusableModels: {'small-local': 4096}}));
  assert.match(dry.err, /small-local=4096 tokens/);
  assert.ok(dry.out.includes('"provider"') && dry.out.includes('"llama3.1:8b"') && dry.out.includes('hosted-allowed'));
  assert.ok(!existsSync(path) && !existsSync(join(dir, 'tui.json')));
  const none = run(app(path, {models: {}}));
  assert.match(none.out, /no servable chat model/);
  assert.ok(!existsSync(path));
});

// ---- Headings, Zen and saved preferences ----

test('Zen is hidden unless shown, and showing removes only the entry configure added', (t) => {
  const path = join(temp(t), 'opencode.json');
  writeFileSync(path, '{"disabled_providers":["anthropic"]}');
  let sink = run(app(path, {zen: 'hide'}));
  assert.deepEqual(read(path).disabled_providers, ['anthropic', 'opencode']);
  assert.match(sink.out, /free cloud models are hidden/);
  sink = run(app(path, {zen: 'show'}));
  assert.deepEqual(read(path).disabled_providers, ['anthropic']);
  assert.match(sink.out, /sends your code to their servers/);
  assert.equal(at(read(path), 'provider', 'openabstractions', 'options', 'oaZen'), undefined);
  const own = join(temp(t), 'opencode.json');
  writeFileSync(own, '{"disabled_providers":["opencode"]}');
  sink = run(app(own, {zen: 'show'}));
  assert.deepEqual(read(own).disabled_providers, ['opencode']);
  assert.match(sink.out, /keeps your own/);
});

test('hosted models get their own heading, named hosted', (t) => {
  const hosted = project(offers([{family: 'deepseek-chat', names: ['deepseek-chat'], servers: ['deepseek'], context: 65536,
    contextSource: 'maximum', input: 61440}]), () => false).models;
  const path = join(temp(t), 'opencode.json');
  run(app(path, {hostedModels: hosted}));
  const doc = read(path);
  const main = doc.provider.openabstractions;
  const block = doc.provider['openabstractions-hosted'];
  assert.equal(main.name, headings.local);
  assert.ok(block.name === headings.hosted && block.npm === main.npm);
  assert.ok(block.options.scope === 'remote' && block.options.oaPlacement === 'hosted');
  assert.deepEqual(block.options.guarantees, ['abstraction.inference/hosted-allowed@1']);
  assert.equal(block.models['deepseek-chat'].name, 'deepseek-chat · 64k · hosted: deepseek');
  const none = join(temp(t), 'opencode.json');
  run(app(none));
  assert.equal(read(none).provider['openabstractions-hosted'], undefined);
});

test('a saved preference sits under its own heading, and the old single heading takes the placement name', (t) => {
  const path = join(temp(t), 'opencode.json');
  writeFileSync(path, '{"provider":{"openabstractions":{"name":"OpenAbstractions","npm":"@openabstractions/opencode","options":{}}}}');
  run(app(path, {selectionPolicy: 'cloud-local'}));
  const doc = read(path);
  assert.equal(doc.provider.openabstractions.name, headings.local);
  assert.equal(doc.provider.openabstractions.options.oaSelectionPolicies, undefined);
  const prefs = doc.provider['openabstractions-preferences'];
  assert.ok(prefs.name === headings.preferences && prefs.options.oaInventory === false);
  assert.equal(prefs.options.oaSelectionPolicies[selectionAlias('cloud-local')], 'cloud-local');
  const mine = join(temp(t), 'opencode.json');
  writeFileSync(mine, '{"provider":{"openabstractions":{"name":"My OA","npm":"@openabstractions/opencode","options":{}}}}');
  run(app(mine));
  assert.equal(read(mine).provider.openabstractions.name, 'My OA');
});

test('a preference alias that collides or remaps is refused', () => {
  const policies = {selectionPolicies: {'oa-preference-cloud-local': 'cloud-local'}};
  assert.throws(() => mergeConfig('{"provider":{"openabstractions":{"npm":"@openabstractions/opencode","models":{"oa-preference-cloud-local":{"name":"Hand"}}}}}',
    'openabstractions', provider(policies), 'openabstractions/new'), /conflicts with an existing model/);
  assert.throws(() => mergeConfig('{"provider":{"openabstractions":{"npm":"@openabstractions/opencode","options":{"oaSelectionPolicies":{"oa-preference-cloud-local":"another"}}}}}',
    'openabstractions', provider(policies), 'openabstractions/new'), /already names a different policy/);
});

test('the headings sort this computer first', () => {
  assert.deepEqual([headings.preferences, headings.hosted, headings.local].sort(), [headings.local, headings.hosted, headings.preferences]);
});

// ---- The local profile ----

function profileApp(t, existing, profile) {
  const path = join(temp(t), 'opencode.json');
  if (existing) writeFileSync(path, existing);
  const models = {...testModels(), 'qwen3:8b': {name: 'qwen3', limit: {context: 32768, output: outputBudget}, tool_call: true}};
  return app(path, {models, chosenModel: 'llama3.1:8b', profile});
}

const pluginOptionsOf = (doc) => doc.plugin.find((entry) => Array.isArray(entry) && entry[1]?.providerId === 'openabstractions')?.[1];

test('the local profile adds the Local assistant as the default and keeps build, plan and other keys', (t) => {
  const application = profileApp(t, '{"mcp":{"keep":true},"agent":{"build":{"prompt":"mine","permission":{"bash":"ask"}},"plan":{"model":"x/y"}}}', 'local');
  const sink = run(application);
  assert.ok(sink.out.includes('Local assistant') && sink.out.includes(localSwitch));
  const doc = read(application.path);
  const local = doc.agent.local;
  assert.ok(local.mode === 'primary' && local.model === undefined && doc.model === 'openabstractions/llama3.1:8b' && local.prompt === localBuildPrompt);
  assert.match(local.description, /Local assistant/);
  assert.deepEqual(local.permission, {task: 'deny', question: 'allow'});
  assert.ok(doc.default_agent === 'local' && doc.agent.title.disable === true && pluginOptionsOf(doc).compactTools === true);
  assert.ok(doc.agent.build.prompt === 'mine' && doc.agent.build.permission.bash === 'ask' && doc.agent.plan.model === 'x/y' && doc.mcp.keep);
  assert.ok(readFileSync(application.path, 'utf8').includes('<system-reminder>'));
  assert.match(run(application).out, /unchanged/);
});

test('the local profile follows the default model and keeps settings the person changed', (t) => {
  const application = profileApp(t, '', 'local');
  run(application);
  const doc = read(application.path);
  doc.agent.local.prompt = 'my local prompt';
  doc.agent.local.model = 'openabstractions/llama3.1:8b';
  doc.provider.openabstractions.options.oaProfile.wrote['agent.local.model'] = 'openabstractions/llama3.1:8b';
  doc.default_agent = 'plan';
  writeFileSync(application.path, JSON.stringify(doc));
  const sink = run({...application, chosenModel: 'qwen3:8b'});
  const after = read(application.path);
  assert.ok(after.model === 'openabstractions/qwen3:8b' && after.agent.local.model === undefined);
  assert.ok(after.agent.local.prompt === 'my local prompt' && after.default_agent === 'plan');
  for (const key of ['agent.local.prompt', 'default_agent']) assert.ok(sink.err.includes(`keeps your own ${key}`), key);
});

test('the default profile removes only what local wrote', (t) => {
  const application = profileApp(t, '{"agent":{"build":{"permission":{"bash":"ask"}}}}', 'local');
  run(application);
  const doc = read(application.path);
  doc.agent.local.color = 'accent';
  writeFileSync(application.path, JSON.stringify(doc));
  const sink = run({...application, profile: 'default'});
  const after = read(application.path);
  assert.deepEqual(after.agent.local, {color: 'accent'});
  assert.ok(after.default_agent === undefined && after.agent.title === undefined && pluginOptionsOf(after).compactTools === undefined);
  assert.equal(after.agent.build.permission.bash, 'ask');
  assert.equal(after.provider.openabstractions.options.oaProfile, undefined);
  assert.ok(sink.out.includes('default profile') && sink.err.includes('keeps your own agent.local.color'));
});

test('no profile leaves agent settings alone; --model picks the profile from the scope', (t) => {
  const application = profileApp(t, '{"agent":{"build":{"prompt":"x"}}}', '');
  run(application);
  const doc = read(application.path);
  assert.ok(doc.agent.build.prompt === 'x' && doc.agent.local === undefined && doc.default_agent === undefined);
  for (const [profile, model, scope, want] of [['', 'qwen3', 'local', 'local'], ['', 'qwen3', 'remote', 'default'], ['', '', 'local', ''],
    ['default', 'qwen3', 'local', 'default'], ['local', '', 'local', 'local']]) {
    assert.equal(profileFor(profile, model, scope), want);
  }
});

test('the local prompt keeps OpenCode\'s essential rules', () => {
  for (const rule of ['concise', 'fewer than 4 lines', 'read a file before you edit it', 'conventions',
    'Never commit unless the user explicitly asks', 'secrets', 'tests, lint and typecheck', 'file_path:line_number',
    'independent tools together', 'openabstractions_tools', 'OpenAbstractions Panel']) {
    assert.ok(localBuildPrompt.includes(rule), rule);
  }
  const words = localBuildPrompt.split(/\s+/).filter(Boolean).length;
  assert.ok(words >= 350 && words <= 560, `${words} words`);
});

// ---- The TUI module ----

function tuiApp(dir, registration) {
  return app(join(dir, 'opencode.json'), registration);
}

test('the TUI module is registered in a fresh config, once', (t) => {
  const dir = temp(t);
  const registration = localAdapter(t);
  const first = run(tuiApp(dir, registration));
  const tuiPath = join(dir, 'tui.json');
  assert.deepEqual(read(tuiPath).plugin, [registration.pluginEntry]);
  assert.ok(first.out.includes(tuiPath));
  const raw = readFileSync(tuiPath, 'utf8');
  const again = run(tuiApp(dir, registration));
  assert.equal(readFileSync(tuiPath, 'utf8'), raw);
  assert.ok(again.out.includes(`${tuiPath} already registers the TUI module; unchanged`));
});

test('the person\'s other TUI plugins and keys stay', (t) => {
  const dir = temp(t);
  const registration = localAdapter(t);
  const tuiPath = join(dir, 'tui.json');
  writeFileSync(tuiPath, '{"theme":"tokyonight","plugin":["opencode-wakatime",["./local-tui.ts",{"x":1}]],"keybinds":{"leader":"ctrl+x"}}');
  run(tuiApp(dir, registration));
  const doc = read(tuiPath);
  assert.deepEqual(doc.plugin, ['opencode-wakatime', ['./local-tui.ts', {x: 1}], registration.pluginEntry]);
  assert.ok(doc.theme === 'tokyonight' && doc.keybinds.leader === 'ctrl+x');
});

test('an already registered TUI module keeps its pin and options; a local adapter replaces only its entry', (t) => {
  const dir = temp(t);
  const pinned = `${providerPackage}@0.4.0`;
  const tuiPath = join(dir, 'tui.json');
  writeFileSync(tuiPath, JSON.stringify({plugin: ['other', [pinned, {keybind: 'ctrl+alt+l'}]]}));
  writeFileSync(join(dir, 'opencode.json'), JSON.stringify({plugin: [pinned]}));
  run(tuiApp(dir, {pluginEntry: providerPackage, providerPackage}));
  assert.deepEqual(read(tuiPath).plugin, ['other', [pinned, {keybind: 'ctrl+alt+l'}]]);
  const registration = localAdapter(t);
  run(tuiApp(dir, registration));
  assert.deepEqual(read(tuiPath).plugin, ['other', [registration.pluginEntry, {keybind: 'ctrl+alt+l'}]]);
});

test('the TUI entry follows the source opencode.json names; a pin without ./tui writes no tui.json', (t) => {
  const dir = temp(t);
  writeFileSync(join(dir, 'tui.json'), JSON.stringify({plugin: [[`${providerPackage}@0.4.0`, {keybind: 'ctrl+alt+l'}]]}));
  writeFileSync(join(dir, 'opencode.json'), JSON.stringify({plugin: [`${providerPackage}@0.5.0`]}));
  run(tuiApp(dir, {pluginEntry: providerPackage, providerPackage}));
  assert.deepEqual(read(join(dir, 'tui.json')).plugin, [[`${providerPackage}@0.5.0`, {keybind: 'ctrl+alt+l'}]]);
  const old = temp(t);
  writeFileSync(join(old, 'opencode.json'), JSON.stringify({plugin: [`${providerPackage}@0.3.0`]}));
  run(tuiApp(old, {pluginEntry: providerPackage, providerPackage}));
  assert.ok(!existsSync(join(old, 'tui.json')));
  for (const [source, want] of [[providerPackage, true], [`${providerPackage}@0.4.0`, true], [`${providerPackage}@0.4.0-rc.1`, true],
    [`${providerPackage}@1.0.0`, true], [`${providerPackage}@0.3.9`, false], [`${providerPackage}@0.0.0`, false]]) {
    assert.equal(adapterOffersTUI(source), want, source);
  }
});

test('another package\'s local TUI plugin stays and OA\'s entry is appended', (t) => {
  const dir = temp(t);
  const other = temp(t);
  writeFileSync(join(other, 'package.json'), JSON.stringify({name: '@someone/tui-theme', exports: {'./tui': './tui.js'}}));
  writeFileSync(join(dir, 'tui.json'), JSON.stringify({plugin: [[other, {x: 1}]]}));
  const registration = localAdapter(t);
  run(tuiApp(dir, registration));
  assert.deepEqual(read(join(dir, 'tui.json')).plugin, [[other, {x: 1}], registration.pluginEntry]);
});

test('a tui.jsonc is left for the person, with the entry to add', (t) => {
  const dir = temp(t);
  const jsonc = '// my TUI settings\n{"plugin": []}\n';
  writeFileSync(join(dir, 'tui.jsonc'), jsonc);
  const registration = localAdapter(t);
  const sink = run(tuiApp(dir, registration));
  assert.ok(!existsSync(join(dir, 'tui.json')));
  assert.equal(readFileSync(join(dir, 'tui.jsonc'), 'utf8'), jsonc);
  assert.ok(sink.err.includes('tui.jsonc') && sink.err.includes(JSON.stringify(registration.pluginEntry)));
  assert.ok(existsSync(join(dir, 'opencode.json')));
});

test('a foreign package under the OA name in tui.json is refused before either file is written', (t) => {
  for (const foreign of [`${providerPackage}@file:../foreign`, `${providerPackage}@github:someone/foreign`]) {
    const dir = temp(t);
    const existing = JSON.stringify({plugin: [foreign]});
    writeFileSync(join(dir, 'tui.json'), existing);
    assert.throws(() => run(tuiApp(dir, {pluginEntry: providerPackage, providerPackage})), (error) => error.message.includes(foreign));
    assert.equal(readFileSync(join(dir, 'tui.json'), 'utf8'), existing);
    assert.ok(!existsSync(join(dir, 'opencode.json')));
  }
});

test('an adapter without a TUI module leaves tui.json alone', (t) => {
  const dir = temp(t);
  run(tuiApp(dir, localAdapter(t, false)));
  assert.ok(!existsSync(join(dir, 'tui.json')));
});

// ---- Whole-file writes ----

const oldConfig = '{"mcp":{"keep":true},"model":"elsewhere/model"}';
const oldTUI = '{"plugin":["other-tui"]}';

function writeFixture(t) {
  const dir = temp(t);
  writeFileSync(join(dir, 'opencode.json'), oldConfig);
  writeFileSync(join(dir, 'tui.json'), oldTUI);
  return {dir, application: tuiApp(dir, localAdapter(t))};
}

const interrupt = (name) => ({replace: (path, previous, data) => {
  if (path.endsWith(name)) throw new Error('simulated interruption');
  replaceConfig(path, previous, data);
}});

test('OpenCode\'s folder holds only its files and OA\'s backups', (t) => {
  const dir = temp(t);
  writeFileSync(join(dir, 'AGENTS.md'), 'the person\'s file');
  const application = tuiApp(dir, localAdapter(t));
  const names = () => readdirSync(dir).sort();
  run(application);
  assert.deepEqual(names(), ['AGENTS.md', 'opencode.json', 'tui.json']);
  const changed = {...application, models: {'qwen3:8b': {name: 'qwen3', limit: {context: 40960, output: outputBudget}, tool_call: true}}};
  run(changed);
  assert.deepEqual(names(), ['AGENTS.md', 'opencode.json', 'opencode.json.openabstractions.bak', 'tui.json']);
  run(changed);
  assert.deepEqual(names(), ['AGENTS.md', 'opencode.json', 'opencode.json.openabstractions.bak', 'tui.json']);
  writeFileSync(join(dir, 'tui.json'), oldTUI);
  assert.throws(() => run(application, interrupt('tui.json')));
  assert.deepEqual(names(), ['AGENTS.md', 'opencode.json', 'opencode.json.openabstractions.bak', 'tui.json']);
});

test('the person\'s own .bak files are never touched', (t) => {
  const {dir, application} = writeFixture(t);
  for (const name of ['opencode.json.bak', 'tui.json.bak']) writeFileSync(join(dir, name), 'mine');
  run(application);
  for (const name of ['opencode.json.bak', 'tui.json.bak']) assert.equal(readFileSync(join(dir, name), 'utf8'), 'mine');
  assert.equal(readFileSync(join(dir, 'opencode.json.openabstractions.bak'), 'utf8'), oldConfig);
  assert.equal(readFileSync(join(dir, 'tui.json.openabstractions.bak'), 'utf8'), oldTUI);
});

test('a linked config\'s target is replaced and the link stays', (t) => {
  const dir = temp(t);
  const target = join(temp(t), 'opencode.json');
  writeFileSync(target, oldConfig);
  const link = join(dir, 'opencode.json');
  try {
    symlinkSync(target, link);
  } catch (error) {
    t.skip(`symbolic links cannot be created here: ${error.code}`);
    return;
  }
  run(tuiApp(dir, localAdapter(t)));
  assert.ok(lstatSync(link).isSymbolicLink());
  assert.ok(read(target).provider);
  assert.equal(readFileSync(`${target}.openabstractions.bak`, 'utf8'), oldConfig);
});

test('a config folder reached through a junction is written in its target and the junction stays', (t) => {
  const target = temp(t);
  writeFileSync(join(target, 'opencode.json'), oldConfig);
  const link = join(temp(t), 'opencode');
  // A junction needs no special right on Windows; elsewhere it is a directory link.
  symlinkSync(target, link, 'junction');
  run(tuiApp(link, localAdapter(t)));
  assert.ok(lstatSync(link).isSymbolicLink());
  assert.ok(read(join(target, 'opencode.json')).provider);
  assert.equal(readFileSync(join(target, 'opencode.json.openabstractions.bak'), 'utf8'), oldConfig);
  assert.ok(read(join(target, 'tui.json')).plugin.length === 1);
  assert.ok(!readdirSync(target).some((name) => name.endsWith('.tmp')));
});

test('the sweep removes only configure\'s own staged files; a person\'s .tmp files stay', (t) => {
  const {dir, application} = writeFixture(t);
  const mine = ['opencode.json.old.tmp', 'opencode.json.abc123.tmp', 'tui.json.mine.tmp'];
  for (const name of mine) writeFileSync(join(dir, name), 'the person\'s file');
  const dead = 'opencode.json.openabstractions-0123456789abcdef.tmp';
  writeFileSync(join(dir, dead), 'a killed run\'s staged file');
  run(application);
  for (const name of mine) assert.equal(readFileSync(join(dir, name), 'utf8'), 'the person\'s file', name);
  assert.ok(!existsSync(join(dir, dead)));
});

test('interrupted between the two writes, both files are whole and the next run finishes', (t) => {
  const {dir, application} = writeFixture(t);
  assert.throws(() => run(application, interrupt('tui.json')), /simulated interruption/);
  const doc = read(join(dir, 'opencode.json'));
  assert.ok(doc.provider && doc.mcp && doc.model === 'elsewhere/model');
  assert.equal(readFileSync(join(dir, 'opencode.json.openabstractions.bak'), 'utf8'), oldConfig);
  assert.equal(readFileSync(join(dir, 'tui.json'), 'utf8'), oldTUI);
  assert.ok(!existsSync(join(dir, 'tui.json.openabstractions.bak')));
  assert.ok(!readdirSync(dir).some((name) => name.endsWith('.tmp')));
  run(application);
  assert.deepEqual(read(join(dir, 'tui.json')).plugin.length, 2);
  assert.equal(readFileSync(join(dir, 'tui.json.openabstractions.bak'), 'utf8'), oldTUI);
});

test('interrupted at the first write, both files stay old', (t) => {
  const {dir, application} = writeFixture(t);
  assert.throws(() => run(application, interrupt('opencode.json')));
  assert.equal(readFileSync(join(dir, 'opencode.json'), 'utf8'), oldConfig);
  assert.equal(readFileSync(join(dir, 'tui.json'), 'utf8'), oldTUI);
});

test('one backup per changed write; an unchanged run replaces nothing', (t) => {
  const dir = temp(t);
  const application = tuiApp(dir, localAdapter(t));
  run(application);
  assert.ok(!existsSync(join(dir, 'opencode.json.openabstractions.bak')) && !existsSync(join(dir, 'tui.json.openabstractions.bak')));
  const first = readFileSync(join(dir, 'opencode.json'), 'utf8');
  const changed = {...application, models: {'qwen3:8b': {name: 'qwen3', limit: {context: 40960, output: outputBudget}, tool_call: true}}};
  run(changed);
  assert.equal(readFileSync(join(dir, 'opencode.json.openabstractions.bak'), 'utf8'), first);
  const second = readFileSync(join(dir, 'opencode.json'), 'utf8');
  run(changed);
  assert.equal(readFileSync(join(dir, 'opencode.json.openabstractions.bak'), 'utf8'), first);
  assert.equal(readFileSync(join(dir, 'opencode.json'), 'utf8'), second);
  assert.ok(!existsSync(join(dir, 'tui.json.openabstractions.bak')));
});

test('a config edited while configure ran is refused and the edit kept', (t) => {
  const {dir, application} = writeFixture(t);
  const edited = '{"edited":"by the person"}';
  const deps = {replace: (path, previous, data) => {
    if (path.endsWith('opencode.json')) writeFileSync(path, edited);
    replaceConfig(path, previous, data);
  }};
  assert.throws(() => run(application, deps), /changed while configure ran/);
  assert.equal(readFileSync(join(dir, 'opencode.json'), 'utf8'), edited);
});

// ---- Command line ----

function fakeRouter(byPlacement) {
  return async () => ({offer: async ({placement, reserve}) => {
    assert.equal(reserve, BigInt(outputBudget));
    return {outcome: 'offered', offers: offers(byPlacement[placement] ?? [])};
  }});
}

async function cli(args, byPlacement = {}) {
  const {sink, io: streams} = io();
  const code = await main(args, streams, {resolveRouter: fakeRouter(byPlacement)});
  return {code, ...sink};
}

const qwen = {family: 'qwen3:8b', names: ['qwen3'], servers: ['lemonade'], context: 32768, contextSource: 'effective', input: 28672};

test('no arguments and --help print the usage; mistakes exit 2', async (t) => {
  for (const args of [[], ['--help'], ['configure', '--help'], ['models', '--help']]) {
    const got = await cli(args);
    assert.equal(got.code, 0);
    assert.ok(got.out.includes('--opencode-zen hide|show') && got.out.includes('models [--scope local|remote]') && got.out.includes('--model FAMILY'));
  }
  const config = join(temp(t), 'opencode.json');
  for (const args of [['unknown'], ['configure', 'extra'], ['configure', '--scope', 'bogus'], ['configure', '--provider-id', 'Not Valid!'],
    ['configure', '--selection-policy', 'Bad Name'], ['configure', '--timeout', '-1s'], ['configure', '--opencode-zen', 'maybe', '--config', config],
    ['configure', '--profile', 'fast', '--config', config], ['models', 'extra']]) {
    assert.equal((await cli(args, {local: [qwen]})).code, 2, args.join(' '));
  }
  assert.ok(!existsSync(config));
});

test('configure refuses an invalid adapter and a .jsonc config before writing', async (t) => {
  const dir = temp(t);
  writeFileSync(join(dir, 'package.json'), '{"name":"@other/provider"}');
  const config = join(temp(t), 'opencode.json');
  const bad = await cli(['configure', '--config', config, '--adapter', dir], {local: [qwen]});
  assert.ok(bad.code === 2 && bad.err.includes('--adapter must identify'));
  assert.ok(!existsSync(config));
  const jsonc = join(temp(t), 'opencode.jsonc');
  const refused = await cli(['configure', '--config', jsonc], {local: [qwen]});
  assert.ok(refused.code === 3 && /JSONC/i.test(refused.err) && !existsSync(jsonc));
});

test('configure from the router writes both headings, the local profile and hidden Zen', async (t) => {
  const dir = temp(t);
  const config = join(dir, 'opencode.json');
  const {pluginEntry} = localAdapter(t);
  const deepseek = {family: 'deepseek-chat', names: ['deepseek-chat'], servers: ['deepseek'], context: 65536, contextSource: 'maximum', input: 61440};
  const got = await cli(['configure', '--config', config, '--adapter', pluginEntry, '--model', 'qwen3:8b', '--profile', 'local', '--opencode-zen', 'hide'],
    {local: [qwen], hosted: [deepseek]});
  assert.equal(got.code, 0, got.err);
  const doc = read(config);
  assert.equal(doc.model, 'openabstractions/qwen3:8b');
  assert.equal(doc.provider.openabstractions.models['qwen3:8b'].name, 'qwen3:8b · 32k · this computer: lemonade');
  assert.equal(doc.provider['openabstractions-hosted'].models['deepseek-chat'].name, 'deepseek-chat · 64k · hosted: deepseek');
  assert.ok(doc.disabled_providers.includes('opencode') && doc.default_agent === 'local');
  assert.deepEqual(read(join(dir, 'tui.json')).plugin, [pluginEntry]);
});

test('models lists the servable families as JSON and as lines', async () => {
  const json = await cli(['models', '--json'], {local: [qwen]});
  assert.deepEqual(JSON.parse(json.out), {models: [{family: 'qwen3:8b', name: 'qwen3:8b · 32k · this computer: lemonade', context: 32768,
    servers: ['lemonade']}], warnings: []});
  const lines = await cli(['models'], {local: [qwen]});
  assert.equal(lines.out, 'qwen3:8b\tqwen3:8b · 32k · this computer: lemonade\tcontext 32768\ton lemonade\n');
  assert.match((await cli(['models'], {})).out, /no servable chat model/);
});

test('a refusing router exits 3 and writes nothing', async (t) => {
  const config = join(temp(t), 'opencode.json');
  const {sink, io: streams} = io();
  const code = await main(['configure', '--config', config], streams, {resolveRouter: async () => ({offer: async () => {
    throw Object.assign(new Error('denied'), {code: 'forbidden'});
  }})});
  assert.ok(code === 3 && sink.err.includes('abstraction.router/inventory.read') && sink.err.includes('Panel'));
  assert.ok(!existsSync(config));
});

test('typed router refusals keep their exit category and never write supplied offers', async (t) => {
  for (const outcome of ['not_permitted', 'forbidden', 'unavailable', 'invalid', undefined]) {
    const config = join(temp(t), 'opencode.json');
    const {sink, io: streams} = io();
    const code = await main(['configure', '--config', config], streams, {resolveRouter: async () => ({
      offer: async () => ({outcome, offers: offers([qwen])}),
    })});
    assert.equal(code, outcome === 'unavailable' ? 4 : 3, sink.err);
    assert.ok(sink.err.includes(outcome ?? 'invalid_result'));
    if (outcome === 'not_permitted') assert.ok(sink.err.includes('abstraction.router/inventory.read') && sink.err.includes('Panel'));
    if (outcome === 'forbidden') {
      assert.ok(!sink.err.includes('this program lacks') && !sink.err.includes('abstraction.router/inventory.read'));
      assert.ok(!sink.err.includes('Panel') && !sink.err.includes('grant it'));
    }
    assert.ok(!existsSync(config));
  }
});

test('ConfigureError carries its exit code', () => {
  assert.equal(new ConfigureError(4, 'x').exitCode, 4);
});

test('a write creates the config folder, and the lock and staging leave no file in it', (t) => {
  const dir = temp(t);
  const path = join(dir, 'sub', 'opencode.json');
  replaceConfig(path, null, '{}\n');
  assert.deepEqual(readdirSync(join(dir, 'sub')), ['opencode.json']);
});
