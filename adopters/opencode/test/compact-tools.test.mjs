import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {compactDefinition, compactSchema, compactShell, condensedFrom, condensedText, pinnedTools} from '../compact-tools.js';
import OpenAbstractionsPlugin from '../plugin.js';
import {shortToolText, toolNames} from '../tool-config.js';

const optional = () => ({optional() { return this; }});
const fakeTool = Object.assign((definition) => definition, {schema: {
  object: (value) => value, string: optional, number: optional, boolean: optional, enum: optional, array: optional,
}});

const powershell = 'Executes a given Windows PowerShell (5.1) command...\n\nBe aware: OS: win32, Shell: powershell\n\n'
  + 'Use `C:\\Users\\p\\AppData\\Local\\Temp\\opencode-oa-preview` for temporary work outside the workspace. This directory...';

test('the shell text keeps the essentials of the shell OpenCode chose', () => {
  const ps = compactShell(powershell);
  for (const rule of ['powershell', 'no && or ||', 'if ($?)', '$env:NAME', '& "path\\to\\exe"', 'workdir',
    'read, edit, write, glob and grep', '120000 ms', 'C:\\Users\\p\\AppData\\Local\\Temp\\opencode-oa-preview',
    'only when asked', 'never commit secrets']) {
    assert.ok(ps.includes(rule), `PowerShell text lacks ${rule}: ${ps}`);
  }
  assert.ok(ps.length < 1300, `PowerShell text is ${ps.length} characters`);
  const bash = compactShell('Be aware: OS: win32, Shell: bash');
  assert.match(bash, /Run a bash command/);
  assert.match(bash, /Chain dependent commands with &&/);
  assert.doesNotMatch(bash, /PowerShell/);
});

test('a compact schema drops $schema and the ±2^53 bounds and keeps everything else', () => {
  const schema = {$schema: 'https://json-schema.org/draft/2020-12/schema', type: 'object', required: ['n'],
    properties: {n: {type: 'integer', minimum: -9007199254740991, maximum: 9007199254740991, exclusiveMinimum: 0},
      m: {type: 'integer', minimum: 1, maximum: 10}, e: {type: 'string', enum: ['a', 'b']}}};
  assert.deepEqual(compactSchema(schema), {type: 'object', required: ['n'],
    properties: {n: {type: 'integer', exclusiveMinimum: 0}, m: {type: 'integer', minimum: 1, maximum: 10}, e: {type: 'string', enum: ['a', 'b']}}});
});

test('compact definitions replace core text and schemas, shorten OA tools and leave others alone', () => {
  const read = {description: 'long', parameters: {}, jsonSchema: undefined};
  compactDefinition('read', read, shortToolText);
  assert.match(read.description, /filePath must be absolute/);
  assert.deepEqual(read.jsonSchema.required, ['filePath']);
  assert.equal(read.jsonSchema.$schema, undefined);
  const image = {description: 'long image text', jsonSchema: {$schema: 'x', type: 'object', properties: {prompt: {type: 'string'}}, required: ['prompt']}};
  compactDefinition('openabstractions_generate_image', image, shortToolText);
  assert.equal(image.description, shortToolText.openabstractions_generate_image);
  assert.deepEqual(image.jsonSchema, {type: 'object', properties: {prompt: {type: 'string'}}, required: ['prompt']});
  const task = {description: 'task text', parameters: {}};
  compactDefinition('task', task, shortToolText);
  assert.equal(task.description, 'task text');
  assert.equal(task.jsonSchema, undefined);
  for (const name of toolNames) assert.ok(shortToolText[`openabstractions_${name}`], `no short text for ${name}`);
  assert.match(shortToolText.openabstractions_open_credentials_panel, /never ask for or pass a secret/);
});

const pinnedText = (tool) => readFile(new URL(`./fixtures/opencode-${condensedFrom}-tool/${tool}.txt`, import.meta.url), 'utf8');

test('compact mode turns itself off once OpenCode\'s core tool text differs from the condensed version', async () => {
  for (const tool of pinnedTools) {
    const text = await pinnedText(tool);
    assert.equal(condensedText(tool, text), true, tool);
    assert.equal(condensedText(tool, `${text}\n`), true, `${tool}: trailing whitespace is not a change`);
    assert.equal(condensedText(tool, `${text} NEVER do the new thing.`), false, tool);
  }
  assert.equal(condensedText('bash', 'any shell text'), true, 'the shell has no pinned text');
  const logs = [];
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async ({body}) => logs.push(body)}}},
    {compactTools: true, tools: {speak: true}}, {tool: fakeTool});
  const shell = {description: 'Shell: powershell\nlong text', parameters: {}};
  await plugin['tool.definition']({toolID: 'bash'}, shell);
  assert.match(shell.description, /Windows PowerShell 5.1/, 'compacted while nothing differs');
  const changed = {description: `${await pinnedText('read')}\nA rule a newer OpenCode added.`, parameters: {}};
  await plugin['tool.definition']({toolID: 'read'}, changed);
  assert.match(changed.description, /A rule a newer OpenCode added/, 'the changed tool keeps its own text');
  const later = {description: await pinnedText('edit'), parameters: {}};
  await plugin['tool.definition']({toolID: 'edit'}, later);
  assert.equal(later.description, await pinnedText('edit'), 'every later tool keeps its full text');
  const speak = {description: 'full speak text', parameters: {}};
  await plugin['tool.definition']({toolID: 'openabstractions_speak'}, speak);
  assert.equal(speak.description, 'full speak text');
  assert.equal(logs.filter((entry) => /compact tool text off/.test(entry.message)).length, 1);
  assert.match(logs[0].message, new RegExp(`OpenCode ${condensedFrom.replaceAll('.', '\\.')}`));
});

test('compactTools turns on the hook and the discovery tool; without it nothing changes', async () => {
  const plain = await OpenAbstractionsPlugin({client: {}}, {tools: {speak: true}}, {tool: fakeTool});
  const untouched = {description: 'long', parameters: {}};
  await plain['tool.definition']({toolID: 'read'}, untouched);
  assert.equal(untouched.description, 'long');
  assert.equal(plain.tool.openabstractions_tools, undefined);

  const compact = await OpenAbstractionsPlugin({client: {}}, {compactTools: true, tools: {speak: true, watch_logs: true}}, {tool: fakeTool});
  const output = {description: await pinnedText('read'), parameters: {}};
  await compact['tool.definition']({toolID: 'read'}, output);
  assert.match(output.description, /filePath must be absolute/);
  const discovery = compact.tool.openabstractions_tools;
  assert.match(discovery.description, /List and describe the OpenAbstractions capabilities/);
  const list = JSON.parse(await discovery.execute({}));
  assert.deepEqual(list.tools.map((entry) => entry.name), ['openabstractions_speak', 'openabstractions_watch_logs']);
  assert.ok(list.notRegistered.includes('openabstractions_generate_image'));
  const speak = JSON.parse(await discovery.execute({name: 'openabstractions_speak'}));
  assert.equal(speak.description, compact.tool.openabstractions_speak.description, 'the full description comes back');
  assert.match(speak.description, /Read a chosen text aloud/);
  assert.equal(JSON.parse(await discovery.execute({name: 'openabstractions_generate_image'})).outcome, 'not_registered');
});

// Every safety clause of the pinned OpenCode 1.18.31 tool text survives in
// the compact text: the fixture names each clause and the phrase that keeps
// it. The fixture must list every clause of the pinned tool text, committed
// beside it, so a later edit cannot drop one silently.
test('compact text keeps every safety clause of the pinned OpenCode tool text', async () => {
  const {readFileSync} = await import('node:fs');
  const fixture = JSON.parse(readFileSync(new URL('./fixtures/opencode-1.18.31-tool-safety.json', import.meta.url), 'utf8'));
  const text = (tool, shell) => {
    if (tool === 'bash') return compactShell(`Be aware: OS: win32, Shell: ${shell}`);
    const output = {description: 'original', parameters: {}};
    compactDefinition(tool, output);
    return output.description;
  };
  for (const clause of fixture.clauses) {
    for (const shell of clause.tool === 'bash' ? ['bash', 'powershell', 'pwsh', 'cmd'] : ['']) {
      for (const phrase of clause.keeps) {
        assert.ok(text(clause.tool, shell).includes(phrase), `${clause.tool} ${shell} drops "${clause.original}"; expected "${phrase}"`);
      }
    }
  }
  // OpenCode 1.18.31's own tool text, committed beside the fixture (MIT).
  const pinned = new URL('./fixtures/opencode-1.18.31-tool/', import.meta.url);
  const pattern = /NEVER|Never |never commit|explicitly request|DO NOT|Do NOT|do not amend|\b[Mm]ust\b|MUST/;
  const listed = new Set(fixture.clauses.map((clause) => clause.original));
  for (const file of ['read.txt', 'edit.txt', 'write.txt', 'glob.txt', 'grep.txt', 'webfetch.txt', 'todowrite.txt', 'question.txt', 'shell/shell.txt']) {
    for (const line of readFileSync(new URL(file, pinned), 'utf8').split('\n')) {
      if (pattern.test(line)) assert.ok(listed.has(line.trim().replace(/^- /, '')), `fixture misses ${file}: ${line.trim()}`);
    }
  }
  for (const line of readFileSync(new URL('shell/prompt.ts.txt', pinned), 'utf8').split('\n')) {
    if (/DO NOT|Do NOT/.test(line)) assert.ok(listed.has(line.trim().replace(/^- /, '')), `fixture misses shell/prompt.ts: ${line.trim()}`);
  }
});
