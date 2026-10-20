import assert from 'node:assert/strict';
import {EventEmitter} from 'node:events';
import test from 'node:test';

import {createOpenAbstractionsPanelTool, installedRegistrationManifest, launchCredentialsPanel, requestApplicationRegistration, requestOpenCodeConnection,
  resolveInstalledPanel} from './panel-launch.js';

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

const integrationRoot = 'C:\\Users\\person\\AppData\\Local\\Programs\\OpenAbstractions\\apps\\opencode';
const integrationManifest = integrationRoot + '\\openabstractions-application.json';

// The Panel request is Windows-only (requestInstalledPanel); file: URLs with a
// drive letter convert to Windows paths only there.
test('the registration manifest is the integration root beside plugin/<version>, and none elsewhere', {skip: process.platform !== 'win32' && 'Windows install layout'}, () => {
  assert.equal(installedRegistrationManifest('file:///C:/Users/person/AppData/Local/Programs/OpenAbstractions/apps/opencode/plugin/0.8.0/panel-launch.js'),
    integrationManifest);
  assert.equal(installedRegistrationManifest('file:///C:/work/abstractions/adopters/opencode/panel-launch.js'), '');
  assert.equal(installedRegistrationManifest('not a url'), '');
});

test('a registration request starts the selected Panel with the integration manifest only', async () => {
  const spawned = [];
  const started = await requestApplicationRegistration({
    platform: 'win32', manifest: integrationManifest, env: {PATH: 'p', OPENAI_API_KEY: 'secret'},
    selectRuntime: async () => ({program: runtimeProgram}),
    stat: async () => ({isFile: () => true}),
    spawn: (program, args, options) => {
      spawned.push({program, args, options});
      const child = fakeChild();
      setImmediate(() => child.emit('spawn'));
      return child;
    },
  });
  assert.equal(started, true);
  assert.equal(spawned.length, 1);
  assert.equal(spawned[0].program, panelProgram);
  assert.deepEqual(spawned[0].args, ['--register-application', integrationManifest]);
  assert.equal(spawned[0].options.detached, true);
  assert.deepEqual(spawned[0].options.env, {PATH: 'p'}, 'no secret reaches the Panel environment');
});

test('without an installed manifest, a Panel or Windows, nothing starts', async () => {
  let spawns = 0;
  const spawn = () => { spawns += 1; return fakeChild(); };
  const runtime = async () => ({program: runtimeProgram});
  assert.equal(await requestApplicationRegistration({platform: 'win32', manifest: '', spawn, selectRuntime: runtime}), false);
  assert.equal(await requestApplicationRegistration({platform: 'win32', manifest: integrationManifest, spawn, selectRuntime: runtime,
    stat: async () => { throw new Error('absent'); }}), false);
  assert.equal(await requestApplicationRegistration({platform: 'win32', manifest: integrationManifest, spawn,
    selectRuntime: async () => { throw new Error('no runtime'); }, stat: async () => ({isFile: () => true})}), false);
  assert.equal(await requestApplicationRegistration({platform: 'linux', manifest: integrationManifest, spawn, selectRuntime: runtime}), false);
  assert.equal(spawns, 0);
});

test('OpenCode connection first use starts the selected common Panel owner without authority metadata', async () => {
  const spawned = [];
  let child;
  let settled = false;
  const work = requestOpenCodeConnection({platform: 'win32', manifest: integrationManifest,
    env: {PATH: 'p', OPENAI_API_KEY: 'secret'}, selectRuntime: async () => ({program: runtimeProgram}),
    stat: async () => ({isFile: () => true}), spawn: (program, args, options) => {
      spawned.push({program, args, options}); child = fakeChild(); child.stdout = new EventEmitter();
      return child;
    }}).then(value => { settled = true; return value; });
  await new Promise(setImmediate);
  child.emit('spawn'); child.emit('exit', 0);
  await new Promise(setImmediate);
  assert.equal(settled, false, 'spawn and exit do not provide native proof or join stdout');
  child.stdout.emit('data', '{"outcome":"connected","hostRegistered":true,"helperEnrolled":true}\n');
  child.emit('close', 0);
  assert.deepEqual(await work, {outcome: 'connected', hostRegistered: true, helperEnrolled: true});
  assert.equal(spawned[0].program, panelProgram);
  assert.deepEqual(spawned[0].args, ['--connect-opencode']);
  assert.deepEqual(spawned[0].options.env, {PATH: 'p'});
  assert.notEqual(spawned[0].options.detached, true);
  assert.equal(child.unrefCalled, false);
  assert.equal(await requestOpenCodeConnection({platform: 'win32', manifest: '', spawn: () => { throw new Error('development must not launch'); }}), false);
});

test('typed native completion requires both proofs, a joined successful child and one closed result', async () => {
  const good = '{"outcome":"connected","hostRegistered":true,"helperEnrolled":true}';
  for (const [body, code] of [['', 0], [good, 1], [good.replace('true', 'false'), 0],
    [good.replace(',"helperEnrolled":true', ''), 0], [good.replace('}', ',"extra":true}'), 0],
    [good.replace('}', ',"hostRegistered":true}'), 0], ['x'.repeat(1025), 0]]) {
    const result = await requestOpenCodeConnection({platform: 'win32', manifest: integrationManifest,
      selectRuntime: async () => ({program: runtimeProgram}), stat: async () => ({isFile: () => true}),
      spawn: () => { const child = fakeChild(); child.stdout = new EventEmitter();
        setImmediate(() => { child.stdout.emit('data', body); child.emit('close', code); }); return child; }});
    assert.deepEqual(result, {outcome: 'not_connected'});
  }
});

test('only the typed pending answer reports a waiting native question', async () => {
  const pending = '{"outcome":"connect_pending","hostRegistered":false,"helperEnrolled":false}';
  for (const [body, code, outcome] of [[pending, 0, 'connect_pending'], [pending, 1, 'not_connected'], ['', 1, 'not_connected'],
    ['{"outcome":"not_connected","hostRegistered":false,"helperEnrolled":false}', 0, 'not_connected']]) {
    const result = await requestOpenCodeConnection({platform: 'win32', manifest: integrationManifest,
      selectRuntime: async () => ({program: runtimeProgram}), stat: async () => ({isFile: () => true}),
      spawn: () => { const child = fakeChild(); child.stdout = new EventEmitter();
        setImmediate(() => { child.stdout.emit('data', body); child.emit('close', code); }); return child; }});
    assert.deepEqual(result, {outcome}, body + ' ' + code);
  }
});

test('an explicit pending-state recheck uses the read-only native owner', async () => {
  let argumentsSeen;
  const result = await requestOpenCodeConnection({platform: 'win32', manifest: integrationManifest, readOnly: true,
    selectRuntime: async () => ({program: runtimeProgram}), stat: async () => ({isFile: () => true}),
    spawn: (_program, args) => { argumentsSeen = args; const child = fakeChild(); child.stdout = new EventEmitter();
      setImmediate(() => { child.stdout.emit('data', '{"outcome":"connected","hostRegistered":true,"helperEnrolled":true}\n'); child.emit('close', 0); }); return child; }});
  assert.deepEqual(argumentsSeen, ['--read-opencode-connection']);
  assert.equal(result.outcome, 'connected');
});
