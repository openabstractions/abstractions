import assert from 'node:assert/strict';
import {EventEmitter} from 'node:events';
import test from 'node:test';

import {createOpenAbstractionsPanelTool, launchCredentialsPanel, resolveInstalledPanel} from './panel-launch.js';

const runtimeProgram = 'C:\\Program Files\\OpenAbstractions\\tools\\openabstractions.exe';
const panelProgram = 'C:\\Program Files\\OpenAbstractions\\tools\\Abstraction Panel.exe';

function fakeChild() {
  const child = new EventEmitter();
  child.unrefCalled = false;
  child.unref = () => { child.unrefCalled = true; };
  return child;
}

function fakeTool() {
  const definition = (options) => options;
  definition.schema = {
    enum(values) {
      return {values, optional() { return {values, isOptional: true}; }};
    },
  };
  return definition;
}

const approved = {ask: async () => {}};

test('the shared native runtime selection locates only its expected sibling Panel', async () => {
  let selected = 0;
  let checkedPath = '';
  const panel = await resolveInstalledPanel({
    selectRuntime: async () => { selected += 1; return {program: runtimeProgram}; },
    stat: async (path) => {
      checkedPath = path;
      return {isFile: () => true};
    },
  });
  assert.equal(selected, 1);
  assert.equal(panel, panelProgram);
  assert.equal(checkedPath, panelProgram);
});

test('untrusted runtime paths and a missing Panel fail closed', async () => {
  await assert.rejects(resolveInstalledPanel({
    selectRuntime: async () => ({program: 'C:\\Users\\alice\\openabstractions.exe'}),
    stat: async () => ({isFile: () => true}),
  }));
  await assert.rejects(resolveInstalledPanel({
    selectRuntime: async () => ({program: runtimeProgram}),
    stat: async () => ({isFile: () => false}),
  }));
});

test('the Credentials page is selected without secret or path arguments', async () => {
  const child = fakeChild();
  let spawnCall;
  const pending = launchCredentialsPanel({
    provider: 'credentials',
    platform: 'win32',
    env: {PATH: 'fixture-path', APPDATA: 'fixture-appdata', DEEPSEEK_API_KEY: 'must-not-cross-process'},
    selectRuntime: async () => ({program: runtimeProgram}),
    stat: async () => ({isFile: () => true}),
    spawn: (...args) => { spawnCall = args; return child; },
  });

  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(spawnCall[0], panelProgram);
  assert.deepEqual(spawnCall[1], ['-page', 'credentials', '-quiet']);
  assert.deepEqual(spawnCall[2], {
    detached: true,
    stdio: 'ignore',
    windowsHide: true,
    env: {PATH: 'fixture-path', APPDATA: 'fixture-appdata'},
  });
  assert.equal(Object.hasOwn(spawnCall[2].env, 'DEEPSEEK_API_KEY'), false);
  child.emit('spawn');
  const response = await pending;
  assert.equal(child.unrefCalled, true);
  assert.equal(response.title, 'OpenAbstractions Panel started');
  assert.match(response.output, /Credentials page selected/);
  assert.match(response.output, /did not store a credential or grant app access/);
  assert.doesNotMatch(response.output, /secret value|API key/i);
});

test('omitting provider preserves the generic Credentials page', async () => {
  const child = fakeChild();
  const pending = launchCredentialsPanel({
    platform: 'win32',
    selectRuntime: async () => ({program: runtimeProgram}),
    stat: async () => ({isFile: () => true}),
    spawn: (_file, args) => {
      assert.deepEqual(args, ['-page', 'credentials', '-quiet']);
      queueMicrotask(() => child.emit('spawn'));
      return child;
    },
  });
  assert.match((await pending).output, /Credentials page selected/);
});

test('unsupported platforms and unavailable selected installations give manual guidance', async () => {
  let selected = false;
  const unsupported = await launchCredentialsPanel({
    platform: 'linux',
    selectRuntime: async () => { selected = true; return {}; },
  });
  assert.equal(selected, false);
  assert.match(unsupported.output, /Windows Panel only/);
  assert.match(unsupported.output, /choose Credentials/);
  assert.match(unsupported.output, /--from-stdin/);

  const missing = await launchCredentialsPanel({
    platform: 'win32',
    selectRuntime: async () => ({program: runtimeProgram}),
    stat: async () => ({isFile: () => false}),
    spawn: () => { throw new Error('must not spawn'); },
  });
  assert.match(missing.output, /could not be resolved from the selected runtime installation/);
  assert.doesNotMatch(missing.title, /started/i);
});

test('a Panel process start failure returns manual guidance without claiming success', async () => {
  const child = fakeChild();
  const pending = launchCredentialsPanel({
    platform: 'win32',
    selectRuntime: async () => ({program: runtimeProgram}),
    stat: async () => ({isFile: () => true}),
    spawn: () => {
      queueMicrotask(() => child.emit('error', Object.assign(new Error('fixture'), {code: 'EACCES'})));
      return child;
    },
  });
  const response = await pending;
  assert.match(response.output, /could not be started from its selected runtime installation/);
  assert.match(response.output, /choose Credentials/);
  assert.doesNotMatch(response.title, /started/i);
});

test('the OpenCode tool exposes only a typed provider selector and requires per-call approval', async () => {
  const child = fakeChild();
  let spawnCalls = 0;
  const tool = createOpenAbstractionsPanelTool({
    tool: fakeTool(),
    platform: 'win32',
    selectRuntime: async () => ({program: runtimeProgram}),
    stat: async () => ({isFile: () => true}),
    spawn: (...args) => {
      spawnCalls += 1;
      queueMicrotask(() => child.emit('spawn'));
      return fakeChildWithSpawn(child, args);
    },
  });
  assert.deepEqual(tool.args.provider.values, ['credentials']);
  assert.equal(tool.args.provider.isOptional, true);
  assert.match(tool.description, /Never ask for, accept or pass a credential value, URL or executable path/);

  const noApproval = await tool.execute({provider: 'credentials'}, {});
  assert.match(noApproval.output, /approval prompt/);
  const refused = await tool.execute({provider: 'credentials'}, {ask: async () => { throw new Error('refused'); }});
  assert.equal(refused.title, 'Panel not started');
  assert.equal(spawnCalls, 0);

  let permission;
  const response = await tool.execute({provider: 'credentials'}, {
    ask: async (input) => { permission = input; },
  });
  assert.equal(permission.permission, 'oa_panel_launch');
  assert.deepEqual(permission.patterns, ['credentials']);
  assert.deepEqual(permission.always, []);
  assert.equal(permission.metadata.title, 'OpenAbstractions Credentials');
  assert.match(permission.metadata.description, /credential value stays in the Panel/);
  assert.equal(response.title, 'OpenAbstractions Panel started');
  assert.equal(spawnCalls, 1);
});

test('the OpenCode tool refuses extra input such as a secret or executable path', async () => {
  let approvals = 0;
  const tool = createOpenAbstractionsPanelTool({tool: fakeTool(), platform: 'win32'});
  await assert.rejects(tool.execute({provider: 'credentials', apiKey: 'fixture-secret'}, {
    ask: async () => { approvals += 1; },
  }), /credential values are never accepted/);
  assert.equal(approvals, 0);
});

function fakeChildWithSpawn(child, args) {
  child.spawnArgs = args;
  return child;
}
