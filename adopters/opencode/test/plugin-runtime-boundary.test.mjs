import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import vm from 'node:vm';
import test from 'node:test';

// Run actual packaged factory bodies with inert module boundaries. Every native
// constructor is a marker; no connector can load a DLL or open a transport.
async function fixture(runtime) {
  const calls = [];
  class NodeConnector { constructor() { this.runtime = 'node'; } }
  class BunConnector { constructor() { this.runtime = 'bun'; } }
  class Machine {
    constructor(endpoint, options) { calls.push({endpoint, options}); }
    async resolveService(contract, scope) { return {contract, scope}; }
  }
  class Client {
    constructor(binding) { this.binding = binding; }
    async readEffective(name) { calls.push({effective: name}); return {outcome: 'unknown', revision: ''}; }
  }
  const context = vm.createContext({Bun: runtime === 'bun' ? {} : undefined,
    AbortController, AbortSignal, TextEncoder, TextDecoder, Uint8Array,
    process: {platform: 'win32', env: {}}, console: {log() {}, warn() {}, error() {}}});
  const sources = new Map();
  const helperSource = await readFile(new URL('../runtime-connector.js', import.meta.url), 'utf8');
  const helper = new vm.SourceTextModule(helperSource, {context, importModuleDynamically: dynamic});
  function inert(exports) {
    return new vm.SyntheticModule(Object.keys(exports), function() {
      for (const [name, value] of Object.entries(exports)) this.setExport(name, value);
    }, {context});
  }
  async function ready(module) { await module.link(() => { throw Error('Unexpected nested import'); }); await module.evaluate(); return module; }
  async function dynamic(specifier) {
    if (specifier === './runtime-connector.js') return helper;
    const modules = {
      '@openabstractions/ipc': {NativeConnector: NodeConnector},
      '@openabstractions/ipc/bun': {BunNativeConnector: BunConnector},
      '@openabstractions/facade': {Machine},
      '@openabstractions/router': {RouterClient: Client},
      '@openabstractions/inference': {SelectionOperatorV2Client: Client, resolvePreload: async () => ({})},
      '@openabstractions/inference/protocol': {ImageClient: Client, ImageV2Client: Client, SpeechClient: Client},
      '@openabstractions/logging': {SourceReaderClient: Client},
      '@openabstractions/rights': {AuthorizationClient: Client},
      '@openabstractions/asks': {PermissionApplicationClient: Client},
      '@openabstractions/storage-content': {ContentReaderClient: Client},
    };
    assert.ok(Object.hasOwn(modules, specifier), `unexpected dynamic import ${specifier}`);
    return ready(inert(modules[specifier]));
  }
  await helper.link(() => { throw Error('Unexpected helper import'); }); await helper.evaluate();
  const permission = new vm.SourceTextModule(await readFile(new URL('../permission-wait.js', import.meta.url), 'utf8'),
    {context, importModuleDynamically: dynamic});
  await ready(permission);
  async function factories(file, names) {
    const source = await readFile(new URL(`../${file}`, import.meta.url), 'utf8');
    const imports = new Map([...source.matchAll(/^import\s*\{([\s\S]*?)\}\s*from\s*'([^']+)';/gm)]
      .map((m) => [m[2], m[1].split(',').map((part) => part.trim().split(/\s+as\s+/)[0]).filter(Boolean)]));
    const privateNames = names.filter(name => !new RegExp('export\\s+(?:async\\s+)?function\\s+' + name + '\\b').test(source));
    const module = new vm.SourceTextModule(`${source}\nexport {${privateNames.join(',')}};`, {context, importModuleDynamically: dynamic});
    await module.link((specifier) => {
      if (specifier === './runtime-connector.js') return helper;
      if (specifier === './permission-wait.js') return permission;
      assert.ok(imports.has(specifier), `unexpected static import ${specifier}`);
      return inert(Object.fromEntries(imports.get(specifier).map((name) => [name, () => { throw Error(`Unused dependency called: ${specifier}/${name}`); }])));
    });
    await module.evaluate(); return module.namespace;
  }
  return {calls, helper: helper.namespace, factories, Client};
}

for (const runtime of ['node', 'bun']) {
  test(`actual ${runtime} inventory read uses only the self effective-policy method`, {skip: !vm.SourceTextModule && 'run with --experimental-vm-modules'}, async () => {
    const f = await fixture(runtime);
    const plugin = await f.factories('plugin.js', ['readSelectionPolicy']);
    const result = await plugin.readSelectionPolicy({runtimeEndpoint:'fixture'}, 'oa-chat-default');
    assert.equal(result.outcome, 'unknown');
    assert.equal(f.calls[0].options.connector.runtime, runtime);
    assert.equal(f.calls[1].effective, 'oa-chat-default');
    assert.equal(f.calls.length, 2);
  });
  test(`actual plugin and delivery factories select ${runtime} and retain native proof`, {skip: !vm.SourceTextModule && 'run with --experimental-vm-modules'}, async () => {
    const f = await fixture(runtime);
    const server = {principalKind: 1, principal: 'fixture', program: 'fixture-runtime'};
    const options = {runtimeEndpoint: 'private-endpoint', server};
    const signal = new AbortController().signal;
    const names = ['resolveRouter', 'resolveSelectionOperator', 'resolveSourceReader', 'resolveAuthorization', 'resolveImageClient', 'resolvePermission'];
    const plugin = await f.factories('plugin.js', names);
    await plugin.resolveRouter(options); await plugin.resolveSelectionOperator(options);
    for (const name of names.slice(2)) await plugin[name](options, signal);
    await plugin.resolveImageClient(options, signal, 'abstraction.inference/image@2');
    for (const [file, name] of [['content-delivery.js','resolveContentReader'], ['speak.js','resolveSpeechClient'], ['preload.js','resolvePreloader']]) {
      const module = await f.factories(file, [name]); await module[name](options, signal);
    }
    const listen = await f.factories('listen.js', ['resolveService']);
    await listen.resolveService(options, 'abstraction.inference/transcription@1', signal, async () => f.Client);
    assert.equal(f.calls.length, 11);
    for (const call of f.calls) {
      assert.equal(call.options.connector.runtime, runtime);
      assert.equal(call.endpoint, options.runtimeEndpoint);
      assert.equal(call.options.server, server);
    }
    const injected = {runtime: 'injected'};
    await plugin.resolveSelectionOperator(options, injected);
    assert.equal(f.calls.at(-1).options.connector, injected);
    assert.equal(await f.helper.runtimeConnector(injected), injected);
    assert.equal(await f.helper.runtimeConnector(false), false, 'explicit invalid injection is preserved for its existing receiver to refuse');
  });
}

test('the shared runtime selector is included in the npm package', async () => {
  const manifest = JSON.parse(await readFile(new URL('../package.json', import.meta.url), 'utf8'));
  assert.ok(manifest.files.includes('runtime-connector.js'));
});

test('the actual plugin issues a loading toast before waiting for diagnostics', {skip: !vm.SourceTextModule && 'run with --experimental-vm-modules'}, async () => {
  const f = await fixture('node');
  const plugin = await f.factories('plugin.js', ['showRuntimeNotice']);
  const calls = [];
  let finishLog;
  const client = {tui: {showToast: ({body}) => { calls.push(body.message); }},
    app: {log: () => new Promise(resolve => { finishLog = resolve; })}};
  const pending = plugin.showRuntimeNotice(client, {variant: 'info', message: 'Loading model on this computer'});
  assert.deepEqual(calls, ['Loading model on this computer']);
  finishLog();
  await pending;
  let logged = false;
  await plugin.showRuntimeNotice({tui: {showToast() { throw Error('display unavailable'); }},
    app: {log() { logged = true; }}}, {variant: 'info', message: 'Getting model ready'});
  assert.equal(logged, true);
});
