import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtempSync, mkdirSync, readFileSync, writeFileSync, existsSync, rmSync, symlinkSync, unlinkSync, rmdirSync} from 'node:fs';
import {join, resolve, dirname} from 'node:path';
import {createHash} from 'node:crypto';
import {pathToFileURL} from 'node:url';
import fs from 'node:fs';
import childProcess from 'node:child_process';
import {syncBuiltinESMExports} from 'node:module';
import {tmpdir} from 'node:os';
import {document, removeOwned, snapshot, applyPair, selectedDocuments, stageInstall, removalPlans, rollbackTransaction, observeInstalled, verifyInstalledPlugin, main, runConfigureRequest} from './config-lifecycle.js';
import {replaceConfig, guaranteeFor} from '../configure.js';

const owned = 'C:\\Programs\\OA\\plugin\\0.8.0';
const old = 'C:\\Programs\\OA\\plugin\\0.4.0';
test('ambiguous plugin and provider keys refuse before offset edits or lifecycle writes', t => {
  const root=fixture(t),foreign='["foreign"]',mine=JSON.stringify([owned]);
  const foreignProvider='{"openabstractions":{"npm":"foreign"}}',mineProvider='{"openabstractions":{"npm":"original"}}';
  const texts=[...[ [foreign,mine],[mine,foreign] ].map(([a,b])=>`{"plugin":${a},"plugin":${b}}`),
    ...[ [foreignProvider,mineProvider],[mineProvider,foreignProvider] ].map(([a,b])=>`{"provider":${a},"provider":${b}}`),
    '{"provider":{"openabstractions":{"npm":"foreign"},"openabstractions":{"npm":"original"}}}',
    '{"provider":{"openabstractions":{"options":{"scope":"foreign","scope":"local"}}}}',
    `{"plugin":[[${JSON.stringify(owned)},{"scope":"foreign","scope":"local"}]]}`,
    `{"plugin":${foreign},"pl\\u0075gin":${mine}}`];
  let writes=0;const forbidden=()=>{writes++;throw Error('must not execute');};
  texts.forEach((raw,index)=>{
    const directory=join(root,String(index));mkdirSync(directory);const path=join(directory,'opencode.jsonc');
    const text='// retain these bytes\n'+raw;writeFileSync(path,text);
    assert.throws(()=>removeOwned(text,[owned,[owned,{scope:'local'}]]),/Duplicate (plugin|provider)/);
    assert.throws(()=>stageInstall({directory,plugin:owned}),/Duplicate (plugin|provider)/);
    assert.throws(()=>removalPlans(directory,[]),/Duplicate (plugin|provider)/);
    const current=snapshot(path),tui={...snapshot(join(directory,'tui.json')),kind:'tui',text:'{}'};
    assert.throws(()=>applyPair([{...current,kind:'server',text:'{}'},tui],[],forbidden),/Duplicate (plugin|provider)/);
    writeFileSync(path,'{}');const clean=snapshot(path);
    assert.throws(()=>applyPair([{...clean,kind:'server',text},tui],[],forbidden),/Duplicate (plugin|provider)/);
    assert.throws(()=>rollbackTransaction({plans:[{...clean,kind:'server',before:Buffer.from(text).toString('base64'),after:'{}'}]},forbidden),/Duplicate (plugin|provider)/);
    assert.equal(readFileSync(path,'utf8'),'{}');
  });
  assert.equal(writes,0);
  const opaque='// preserved opaque data\n{"plugin":["foreign"],"personal":{"value":1,"value":2},"mcp":{"foreign":{"timeout":1,"timeout":2}}}';
  assert.equal(removeOwned(opaque,[]),opaque);
});
test('duplicate MCP properties refuse every lifecycle writer before commands or replacement', t => {
  const root=fixture(t),launcher=join(root,'openabstractions-mcp.exe');writeFileSync(launcher,'inert');
  const value={type:'local',command:[launcher],timeout:60000},exact=JSON.stringify(value);
  const foreign='{"type":"remote","url":"https://example.invalid"}';
  const texts=[
    ...[ [foreign,exact],[exact,foreign] ].flatMap(([a,b])=>[
      `{"mcp":{"OpenAbstractions":${a}},"mcp":{"OpenAbstractions":${b}}}`,
      `{"mcp":{"OpenAbstractions":${a},"OpenAbstractions":${b}}}`]),
    ...['type','command','timeout'].map(key=>`{"mcp":{"OpenAbstractions":{${JSON.stringify(key)}:${JSON.stringify(value[key])},${exact.slice(1)}}}`),
    `{"mcp":{},"m\\u0063p":{}}`,
  ];
  let calls=0;const forbidden=()=>{calls++;throw Error('must not execute');};
  texts.forEach((raw,index)=>{
    const directory=join(root,String(index));mkdirSync(directory);const path=join(directory,'opencode.jsonc');
    const text='// retained comment\n'+raw;writeFileSync(path,text);const seed={path,value};
    for(const mcpSeed of [null,seed]) {
      assert.throws(()=>stageInstall({directory,plugin:owned,mcpLauncher:launcher,mcpSeed}),/Duplicate MCP/);
    }
    assert.throws(()=>removalPlans(directory,[],null,seed),/Duplicate MCP/);
    const current=snapshot(path),tui={...snapshot(join(directory,'tui.json')),kind:'tui',text:'{}'};
    assert.throws(()=>applyPair([{...current,kind:'server',text:'{}'},tui],[],forbidden),/Duplicate MCP/);
    assert.throws(()=>rollbackTransaction({plans:[{...current,kind:'server',before:Buffer.from(text).toString('base64'),after:'{}'}]},forbidden),/Duplicate MCP/);
    writeFileSync(path,'{}');const clean=snapshot(path);
    assert.throws(()=>applyPair([{...clean,kind:'server',text},tui],[],forbidden),/Duplicate MCP/);
    assert.throws(()=>rollbackTransaction({plans:[{...clean,kind:'server',before:Buffer.from(text).toString('base64'),after:'{}'}]},forbidden),/Duplicate MCP/);
    assert.equal(readFileSync(path,'utf8'),'{}');writeFileSync(path,text);
    assert.equal(readFileSync(path,'utf8'),text);
  });
  assert.equal(calls,0);
});
function observationFixture() {
  const directory=resolve('observe-account'),plugin=resolve('observe-plugin');
  const hash=bytes=>bytes===null?null:createHash('sha256').update(bytes).digest('hex');
  const seed={npm:pathToFileURL(join(plugin,'index.js')).href,models:{},options:{scope:'local',guarantees:[guaranteeFor('local')]}};
  const documents=[['server','opencode.jsonc'],['server','opencode.json'],['server','config.json'],['tui','tui.jsonc'],['tui','tui.json']].map(([kind,name])=>{
    const bytes=['opencode.jsonc','tui.jsonc'].includes(name)?Buffer.from('// person comment\n'+JSON.stringify({plugin:['foreign',plugin],...(kind==='server'?{provider:{openabstractions:seed},foreignSecret:'never output'}:{theme:'person'})})):null;
    return {kind,path:join(directory,name),bytes,identity:'private-identity',sha256:hash(bytes)};
  });
  const ownership={operation:'install',directory,seed:{path:documents[0].path,value:seed},files:documents.map(item=>({kind:item.kind,path:item.path,before:null,after:item.sha256,owned:item.bytes?[plugin]:[]}))};
  return {directory,plugin,documents,ownership,pending:false};
}

test('read-only observation keeps JSONC and foreign options private with exact owned registration',()=>{
  const input=observationFixture();const result=observeInstalled(input);
  assert.equal(result.schema,1);assert.equal(result.owned_plugins.length,2);
  assert.equal(result.configured_provider.npm,pathToFileURL(join(input.plugin,'index.js')).href);
  const output=JSON.stringify(result);assert.ok(!output.includes('never output'));assert.ok(!output.includes('person'));assert.ok(!output.includes('before'));
});

test('read-only observation refuses pending edited incomplete and conflicting ownership',()=>{
  const cases=[x=>x.pending=true,x=>x.ownership.seed=null,x=>x.documents[0].sha256='f'.repeat(64),
    x=>x.ownership.files.pop(),x=>x.ownership.files[0].owned=[['foreign',{secret:true}]],
    x=>x.plugin='relative',x=>x.documents[0].bytes=Buffer.from('{invalid}'),
    x=>x.ownership.seed.value.npm='foreign-package',
    x=>x.documents[1]={...x.documents[0],path:x.documents[1].path}];
  for(const change of cases){const input=observationFixture();change(input);assert.throws(()=>observeInstalled(input));}
  const repin=(input,index,value)=>{
    const item=input.documents[index];item.bytes=Buffer.from(JSON.stringify(value));
    item.sha256=createHash('sha256').update(item.bytes).digest('hex');input.ownership.files[index].after=item.sha256;
  };
  const conflict=observationFixture();
  repin(conflict,1,{provider:{openabstractions:conflict.ownership.seed.value}});
  assert.throws(()=>observeInstalled(conflict),/route changed/);
  const edited=observationFixture();
  repin(edited,0,{plugin:[[edited.plugin,{changed:true}]],provider:{openabstractions:edited.ownership.seed.value}});
  assert.throws(()=>observeInstalled(edited),/registration changed/);
});

test('read-only main dispatch avoids all mutating installer dependencies and absent state creation',t=>{
  const input=observationFixture();const root=fixture(t);const state=join(root,'absent-state');
  const fail=()=>{throw new Error('Mutating dependency invoked');};
  const saved=[];
  for(const [owner,names] of [[fs,['mkdirSync','writeFileSync','unlinkSync','rmSync','renameSync','openSync']],
    [childProcess,['spawnSync','spawn','exec','execSync','execFile','execFileSync']]]) {
    for(const name of names){saved.push([owner,name,owner[name]]);owner[name]=fail;}
  }
  syncBuiltinESMExports();let result;
  try {
    result=main({operation:'observe',directory:input.directory,plugin:input.plugin,state},
      {documents:()=>({all:input.documents}),read:()=>JSON.stringify(input.ownership),exists:()=>false});
  } finally {for(const [owner,name,value] of saved)owner[name]=value;syncBuiltinESMExports();}
  assert.equal(result.schema,1);assert.equal(existsSync(state),false);assert.equal(existsSync(join(root,'absent-stage')),false);
  assert.throws(()=>main({operation:'observe',directory:input.directory,plugin:input.plugin,state},
    {exists:()=>true,read:fail,documents:fail}),/Committed/);
});
function fixture(t) {
  const root = mkdtempSync(join(tmpdir(),'oa956-'));
  t.after(() => rmSync(root,{recursive:true,force:true}));
  return root;
}

test('exact committed Repair keeps config and ownership bytes and a changed one is rewritten', t => {
  const root=fixture(t),directory=join(root,'account'),cache=join(root,'cache'),state=join(root,'state');
  const plugin=join(root,'installed-plugin');
  mkdirSync(directory);mkdirSync(plugin);
  const hash=bytes=>createHash('sha256').update(bytes).digest('hex');
  const pluginMember=join(plugin,'package.json'), pluginBytes='{"name":"fixture"}';
  const pluginIndex=join(root,'plugin-files.json');
  const pluginInventory=JSON.stringify({'package.json':{bytes:pluginBytes.length,sha256:hash(pluginBytes)}});
  writeFileSync(pluginMember,pluginBytes);writeFileSync(pluginIndex,pluginInventory);
  const verifyPlugin=dir=>verifyInstalledPlugin(dir,hash(pluginInventory),pluginIndex);
  const request=operation=>({operation,directory,cache,state,plugin});
  main(request('install'),{verifyPlugin});
  const pending=join(state,'pending.json'),receipt=join(state,'owned.json');
  unlinkSync(pending);
  const before=readFileSync(receipt), config=join(directory,'opencode.json'), original=readFileSync(config);
  const result=main(request('repair'),{verifyPlugin});
  assert.equal(result.unchanged,true);
  assert.deepEqual(readFileSync(receipt),before);
  assert.deepEqual(readFileSync(config),original);
  assert.equal(existsSync(pending),false);

  writeFileSync(pluginMember,pluginBytes.replace('fixture','changed'));
  assert.throws(()=>main(request('repair'),{verifyPlugin}),/plugin member changed/);
  assert.deepEqual(readFileSync(receipt),before);
  assert.deepEqual(readFileSync(config),original);
  writeFileSync(pluginMember,pluginBytes);
  writeFileSync(pluginIndex,pluginInventory+'\n');
  assert.throws(()=>main(request('repair'),{verifyPlugin}),/plugin inventory changed/);
  writeFileSync(pluginIndex,pluginInventory);

  // A receipt that records different options takes the full repair, which
  // rollback undoes.
  const stale=JSON.parse(before);
  stale.files.find(item=>item.path===config).owned=[[plugin,{options:'stale receipt'}]];
  writeFileSync(receipt,JSON.stringify(stale));
  assert.notEqual(main(request('repair'),{verifyPlugin}).unchanged,true);
  assert.ok(existsSync(pending));
  main(request('rollback'),{});
  assert.deepEqual(JSON.parse(readFileSync(receipt)),stale);
  assert.deepEqual(readFileSync(config),original);
  writeFileSync(receipt,before);

  const foreign=Buffer.from(original.toString('utf8').replace('{','{ "autoupdate": false,')+'\n// changed by person\n');
  writeFileSync(config,foreign);
  const observe=()=>observeInstalled({directory,plugin,documents:selectedDocuments(directory).all,
    ownership:JSON.parse(readFileSync(receipt))});
  assert.throws(observe,/Installed config changed/);
  const refreshed=main(request('repair'),{verifyPlugin});
  assert.notEqual(refreshed.unchanged,true);
  assert.deepEqual(readFileSync(config),foreign);
  const journal=JSON.parse(readFileSync(pending));
  assert.equal(journal.configUnchanged,true);
  assert.deepEqual(journal.plans,[]);
  assert.equal(observe().owned_plugins.length,2);
  // The person's editor can be partway through its next save at rollback.
  const inProgress=Buffer.from('{"autoupdate":');
  writeFileSync(config,inProgress);
  main(request('rollback'),{});
  assert.deepEqual(JSON.parse(readFileSync(receipt)),JSON.parse(before));
  assert.deepEqual(readFileSync(config),inProgress);
  assert.equal(existsSync(pending),false);

  // Changing an owned entry takes the ordinary full repair.
  const edited=document(foreign.toString('utf8'));
  edited.plugin[edited.plugin.length-1]=[plugin,{edited:true}];
  writeFileSync(config,JSON.stringify(edited));
  const repaired=main(request('repair'),{verifyPlugin});
  assert.notEqual(repaired.unchanged,true);
  assert.equal(document(readFileSync(config,'utf8')).autoupdate,false);
  assert.deepEqual(document(readFileSync(config,'utf8')).plugin,document(original.toString('utf8')).plugin);
});

test("an earlier version's bootstrap files stay byte for byte as OpenCode's through upgrade, rollback and removal", t => {
  // Earlier packages pre-seeded OpenCode's own first-use install and recorded
  // those files in the receipt. This package writes and removes none of them.
  const root=fixture(t),directory=join(root,'account'),cache=join(root,'cache'),state=join(root,'state');
  mkdirSync(directory);
  const request=operation=>({operation,directory,cache,state,plugin:owned});
  const commit=()=>unlinkSync(join(state,'pending.json'));
  main(request('install'),{});commit();
  const leaves={'config/package.json':join(directory,'package.json'),'cache/bin/rg.exe':join(cache,'bin','rg.exe')};
  const files=Object.entries(leaves).map(([name,path])=>{
    mkdirSync(dirname(path),{recursive:true});writeFileSync(path,'seeded '+name);
    const current=snapshot(path);
    return {name,identity:current.identity,leaf:{file:true,links:1},sha256:current.sha256,created:true};
  });
  const receipt=join(state,'owned.json');
  const earlier=JSON.parse(readFileSync(receipt,'utf8'));earlier.bootstrap={cache,files,theirs:[]};
  writeFileSync(receipt,JSON.stringify(earlier));
  const upgraded=main(request('upgrade'),{});
  assert.notEqual(upgraded.unchanged,true,'a receipt that lists bootstrap files is rewritten');
  assert.equal(JSON.parse(readFileSync(receipt,'utf8')).bootstrap,undefined);
  for(const [name,path] of Object.entries(leaves))assert.equal(readFileSync(path,'utf8'),'seeded '+name);
  main(request('rollback'),{});
  assert.deepEqual(JSON.parse(readFileSync(receipt,'utf8')).bootstrap.files.map(item=>item.name),Object.keys(leaves));
  main(request('upgrade'),{});commit();
  main(request('remove'),{});commit();
  for(const [name,path] of Object.entries(leaves))assert.equal(readFileSync(path,'utf8'),'seeded '+name);
  assert.ok(!(document(readFileSync(join(directory,'opencode.json'),'utf8')).plugin??[]).includes(owned));
});

test('wrapper installation atomically retires only its owned MCP entry and rollback restores the old roster', t => {
  const root=fixture(t),directory=join(root,'account'),tools=join(root,'selected OA tools');mkdirSync(directory);mkdirSync(tools);
  const launcher=join(tools,'openabstractions-mcp.exe');writeFileSync(launcher,'inert generic role');
  const path=join(directory,'opencode.jsonc');
  const value={type:'local',command:[launcher],timeout:60000};
  const initial='// person comment\n'+JSON.stringify({plugin:['foreign'],mcp:{OpenAbstractions:value,foreign:{type:'remote',url:'https://example.invalid'}},personal:true});
  writeFileSync(path,initial);
  const request={directory,plugin:owned,mcpLauncher:launcher,mcpSeed:{path,value}};
  assert.throws(()=>stageInstall({...request,mcpSeed:{path,value:{...value,timeout:1800000}}}),/registration was edited/);
  assert.equal(readFileSync(path,'utf8'),initial,'failed wrapper preparation keeps the old Messages and Asks roster');
  const install=stageInstall(request);
  assert.equal(readFileSync(path,'utf8'),initial);
  assert.equal(install.mcpSeed,null);
  applyPair(install.plans,install.guards);
  const configured=readFileSync(path,'utf8');assert.ok(configured.includes('// person comment'));
  const current=document(configured);assert.equal(current.mcp.OpenAbstractions,undefined);assert.ok(current.plugin.some(entry=>(Array.isArray(entry)?entry[0]:entry)===owned));
  assert.deepEqual(current.mcp.foreign,{type:'remote',url:'https://example.invalid'});
  const records=install.plans.map(item=>({path:item.path,owned:item.owned}));
  const repair=stageInstall({...request,ownedFiles:records,seed:install.seed,mcpSeed:install.mcpSeed,mcpLauncher:undefined});
  assert.equal(repair.mcpSeed,null,'repair never adds a second roster');
  const transaction={plans:install.plans.map(item=>({kind:item.kind,path:item.path,identity:item.identity,before:item.bytes===null?null:item.bytes.toString('base64'),after:item.text}))};
  rollbackTransaction(transaction);assert.equal(readFileSync(path,'utf8'),initial);
  const restored=stageInstall({...request});applyPair(restored.plans,restored.guards);
  const removal=removalPlans(directory,restored.plans.map(item=>({path:item.path,owned:item.owned})),restored.seed,restored.mcpSeed);
  applyPair(removal.plans,removal.guards);
  assert.deepEqual(document(readFileSync(path,'utf8')).mcp,{foreign:{type:'remote',url:'https://example.invalid'}});
  assert.ok(readFileSync(path,'utf8').includes('// person comment'));
});

test('person-owned exact MCP entries and foreign conflicts refuse wrapper installation without changing settings', t => {
  const root=fixture(t),launcher=join(root,'openabstractions-mcp.exe');writeFileSync(launcher,'inert role');
  const expected={type:'local',command:[launcher],timeout:60000};
  let serial=0;
  for(const entry of [expected,{...expected,timeout:5000},{...expected,enabled:false},null]) {
    const directory=join(root,'case-'+serial++);mkdirSync(directory);
    const path=join(directory,'config.json');const text='// retain\n'+JSON.stringify({mcp:{OpenAbstractions:entry}});writeFileSync(path,text);
    assert.throws(()=>stageInstall({directory,plugin:owned,mcpLauncher:launcher}),/already owned/);
    assert.equal(readFileSync(path,'utf8'),text);
  }
  const directory=join(root,'exact');mkdirSync(directory);const path=join(directory,'opencode.json');
  writeFileSync(path,JSON.stringify({mcp:{OpenAbstractions:expected}}));
  assert.throws(()=>stageInstall({directory,plugin:owned,mcpLauncher:launcher}),/already owned/);
  assert.deepEqual(document(readFileSync(path,'utf8')).mcp.OpenAbstractions,expected);
  assert.throws(()=>removalPlans(directory,[],null,{path,value:{...expected,timeout:5000}}),/Invalid installer-owned MCP/);
  writeFileSync(join(directory,'config.json'),JSON.stringify({mcp:{OpenAbstractions:{...expected,command:['foreign']}}}));
  assert.throws(()=>stageInstall({directory,plugin:owned,mcpLauncher:launcher}),/already owned/);
  for(const value of [null,{...expected,timeout:5000},{...expected,command:['foreign']}]) {
    const text=JSON.stringify({mcp:{OpenAbstractions:value}});writeFileSync(path,text);
    assert.throws(()=>removalPlans(directory,[],null,{path,value}),/Invalid installer-owned MCP/);
    assert.equal(readFileSync(path,'utf8'),text);
  }
});

test('actual wrapper installer request commits one roster through repair and remove rollback', t => {
  const root=fixture(t),directory=join(root,'account'),state=join(root,'state');mkdirSync(directory);
  const tools=join(root,'selected OA tools');mkdirSync(tools);
  const launcher=join(tools,'openabstractions-mcp.exe');writeFileSync(launcher,'inert role');
  const path=join(directory,'opencode.jsonc'),original='// kept\n{"plugin":["foreign"]}';writeFileSync(path,original);
  const dependencies={};let serial=0;
  const request=operation=>({operation,directory,state,plugin:owned});
  main({...request('install'),mcpLauncher:launcher},dependencies);
  const receipt=JSON.parse(readFileSync(join(state,'owned.json'),'utf8'));
  assert.equal(receipt.mcpSeed,undefined);
  unlinkSync(join(state,'pending.json'));
  main(request('repair'),dependencies);unlinkSync(join(state,'pending.json'));
  const before=readFileSync(path,'utf8');assert.equal(document(before).mcp?.OpenAbstractions,undefined);
  unlinkSync(launcher);
  main({...request('remove'),mcpLauncher:''},dependencies);assert.equal(document(readFileSync(path,'utf8')).mcp?.OpenAbstractions,undefined);
  main({...request('rollback'),mcpLauncher:''},dependencies);assert.equal(readFileSync(path,'utf8'),before);
  assert.deepEqual(JSON.parse(readFileSync(join(state,'owned.json'),'utf8')).mcpSeed,receipt.mcpSeed);
});

test('element removal preserves foreign entries, comments and trailing commas', () => {
  const text = '{\n // top\n "plugin": ["foreign", /* foreign comment */ '+JSON.stringify(owned)+',],\n "other": 1 // tail\n}';
  const next = removeOwned(text,[owned]);
  assert.deepEqual(document(next).plugin,['foreign']);
  assert.ok(next.includes('// top')); assert.ok(next.includes('// tail'));
  assert.ok(next.includes('/* foreign comment */'));
  assert.ok(next.includes('"other": 1'));
});

test('upstream parser offset edits remove first, last and only entries with either comma shape', () => {
  for (const list of [[owned,'foreign'],['foreign',owned],[owned],[owned,owned,'foreign']]) {
    for (const trailing of [false,true]) {
      const text='{"plugin":['+list.map(JSON.stringify).join(', /* keep comment */ ')+(trailing?',':'')+']}';
      const next=removeOwned(text,[owned]);
      assert.deepEqual(document(next).plugin,list.filter(item=>item!==owned));
      if (list.length>1) assert.ok(next.includes('/* keep comment */'));
    }
  }
});

test('edited owned options and malformed JSONC refuse', () => {
  assert.throws(() => removeOwned(JSON.stringify({plugin:[[owned,{changed:true}]]}),[owned]),/edited/);
  assert.throws(() => removeOwned('{invalid}',[owned]),/Invalid/);
});

test('foreign tuple bytes and comments survive removal and insertion', t => {
  const directory=fixture(t),path=join(directory,'opencode.json');
  const foreign='["foreign", { "note" : "x,y", "unicode" : "\\u0041" }]';
  const before='{ "plugin" : ['+foreign+', /* person comment */ '+JSON.stringify(old)+'], "other" : 1 }';
  writeFileSync(path,before);
  const output=stageInstall({directory,plugin:owned,ownedFiles:[{path,owned:[old]}]});
  const next=output.plans.find(item=>item.path===path).text;
  assert.ok(next.includes(foreign));assert.ok(next.includes('/* person comment */'));assert.ok(next.includes('"other" : 1'));
  assert.deepEqual(document(next).plugin,[['foreign',{note:'x,y',unicode:'A'}],owned]);
  assert.equal(readFileSync(path,'utf8'),before,'planning changes no account document');
});

test('pair writes refuse changed input and compensate first file on second failure', t => {
  const root=fixture(t); const server=join(root,'opencode.jsonc'); const tui=join(root,'tui.jsonc');
  writeFileSync(server,'{"plugin":[]}'); writeFileSync(tui,'{"plugin":[]}');
  const plans=[{kind:'server',...snapshot(server),text:'{"plugin":["owned"]}'},{kind:'tui',...snapshot(tui),text:'{"plugin":["owned"]}'}];
  let count=0;
  assert.throws(() => applyPair(plans,[],(path,prior,text)=>{if (++count===2) throw Error('second failure');replaceConfig(path,prior,text);}),/second failure/);
  assert.equal(readFileSync(server,'utf8'),'{'+'"plugin":[]}');
  writeFileSync(tui,'{"user":true}');
  assert.throws(() => applyPair(plans),/changed/);
  assert.equal(readFileSync(tui,'utf8'),'{"user":true}');
});

test('rollback restores an absent document and preserves changed compensation input', t => {
  const root=fixture(t);const server=join(root,'opencode.json');const tui=join(root,'tui.json');
  const plans=[{kind:'server',...snapshot(server),text:'{"plugin":[]}'},{kind:'tui',...snapshot(tui),text:'{"plugin":[]}'}];
  let count=0;
  assert.throws(()=>applyPair(plans,[],(path,prior,text)=>{if(++count===2)throw Error('second');replaceConfig(path,prior,text);}),/second/);
  assert.equal(existsSync(server),false);
  count=0;
  assert.throws(()=>applyPair(plans,[],(path,prior,text)=>{if(++count===2){writeFileSync(server,'{"user":true}');throw Error('second');}replaceConfig(path,prior,text);}),/preserved changed input/);
  assert.equal(readFileSync(server,'utf8'),'{"user":true}');
});

test('selection snapshots all candidates and chooses runtime precedence', t => {
  const root=fixture(t);
  for (const name of ['opencode.json','opencode.jsonc','tui.json','tui.jsonc'])writeFileSync(join(root,name),'{}');
  const result=selectedDocuments(root);
  assert.ok(result.selected[0].path.endsWith('opencode.jsonc'));assert.ok(result.selected[1].path.endsWith('tui.jsonc'));
  assert.equal(result.all.length,5);
});

test('equal-byte junction retarget between batch writes cannot redirect write or compensation', t => {
  const root=fixture(t);const first=join(root,'first');const second=join(root,'second');const link=join(root,'config');
  mkdirSync(first);mkdirSync(second);symlinkSync(first,link,'junction');
  for (const name of ['opencode.json','tui.json']) {writeFileSync(join(first,name),'{}');writeFileSync(join(second,name),'{}');}
  const plans=['opencode.json','tui.json'].map((name,index)=>({kind:index?'tui':'server',...snapshot(join(link,name)),text:'{"plugin":["owned"]}'}));
  let writes=0;
  assert.throws(()=>applyPair(plans,[],(path,prior,text,identity)=>{
    replaceConfig(path,prior,text,identity);
    if(++writes===1){rmdirSync(link);symlinkSync(second,link,'junction');}
  }),/identity changed.*preserved changed input/s);
  assert.equal(readFileSync(join(second,'opencode.json'),'utf8'),'{}');
  assert.equal(readFileSync(join(second,'tui.json'),'utf8'),'{}');
  assert.equal(existsSync(join(second,'opencode.json.openabstractions.bak')),false);
  assert.equal(readFileSync(join(first,'tui.json'),'utf8'),'{}');
});

test('formerly absent target keeps its parent identity before restoring absence', t => {
  const root=fixture(t);const first=join(root,'first');const second=join(root,'second');const link=join(root,'config');
  mkdirSync(first);mkdirSync(second);symlinkSync(first,link,'junction');
  const plans=['opencode.json','tui.json'].map((name,index)=>({kind:index?'tui':'server',...snapshot(join(link,name)),text:'{"plugin":["owned"]}'}));
  const transaction={plans:plans.map(item=>({kind:item.kind,path:item.path,identity:item.identity,before:null,after:item.text}))};
  applyPair(plans);
  for(const item of plans)writeFileSync(join(second,item.kind==='server'?'opencode.json':'tui.json'),item.text);
  rmdirSync(link);symlinkSync(second,link,'junction');
  assert.throws(()=>rollbackTransaction(transaction),/identity changed/);
  for(const folder of [first,second])for(const name of ['opencode.json','tui.json'])assert.equal(readFileSync(join(folder,name),'utf8'),'{"plugin":["owned"]}');
});

test('installation appends one owned entry to the server and TUI documents and keeps foreign JSONC bytes', t => {
  const root=fixture(t),config=join(root,'account');mkdirSync(config);
  const path=join(config,'opencode.jsonc');
  const foreign='["./plugins/foreign", { "alias" : "local", "paths" : ["./relative"] }]';
  const initial='// keep\n{"plugin":['+foreign+' /* foreign comment */], "personal": { "alias" : "./relative" }}';
  writeFileSync(path,initial);
  const request={directory:config,plugin:owned};
  assert.throws(()=>stageInstall({...request,plugin:'relative\\plugin'}),/absolute/);
  const output=stageInstall(request);
  assert.equal(readFileSync(path,'utf8'),initial,'planning changes no account document');
  applyPair(output.plans,output.guards);
  const configured=readFileSync(path,'utf8');
  assert.ok(configured.includes('// keep'));
  assert.ok(configured.includes(foreign+' /* foreign comment */'));
  assert.ok(configured.includes('"personal": { "alias" : "./relative" }'));
  assert.deepEqual(document(configured).plugin,[JSON.parse(foreign),owned]);
  assert.deepEqual(document(readFileSync(join(config,'tui.json'),'utf8')).plugin,[owned]);
  assert.deepEqual(output.plans.filter(item=>item.owned.length).map(item=>item.owned),[[owned],[owned]]);
  assert.throws(()=>stageInstall(request),/already owned/);
});

test('the configure worker reports the unowned restored plugin conflict distinctly', t => {
  const root=fixture(t),directory=join(root,'account'),state=join(root,'state');
  const work=join(root,'work'),other=join(root,'other');
  for (const dir of [directory,state,work,other]) mkdirSync(dir);
  const config=join(directory,'opencode.jsonc');
  const original='// Restored preferences\n'+JSON.stringify({plugin:[owned,'./other.js'],personal:{keep:true}});
  writeFileSync(config,original);
  const request=work=>({operation:'install',directory,state,work,plugin:owned});
  const conflict='The OpenAbstractions plugin registration is already owned by the person or different settings';
  const deps={};
  assert.equal(runConfigureRequest(request(work),work,deps),80);
  assert.deepEqual(JSON.parse(readFileSync(join(work,'result.json'),'utf8')),{error:conflict});
  assert.equal(readFileSync(config,'utf8'),original);
  assert.equal(existsSync(join(state,'owned.json')),false);
  assert.equal(existsSync(join(state,'pending.json')),false);
  assert.equal(existsSync(config+'.openabstractions.bak'),false);

  // The error's type, rather than arbitrary child text, controls the exit.
  const ordinary={verifyPlugin:()=>{throw Error(conflict);}};
  assert.equal(runConfigureRequest(request(other),other,ordinary),1);
  assert.deepEqual(JSON.parse(readFileSync(join(other,'result.json'),'utf8')),{error:conflict});
});

test('repair restores exact recorded OA sources and keeps the before image and foreign bytes', t => {
  const root=fixture(t), directory=join(root,'account'), state=join(root,'state');
  mkdirSync(directory);mkdirSync(state);
  const path=join(directory,'opencode.jsonc');
  const request={operation:'repair',directory,state,plugin:owned};
  const dependencies={};
  writeFileSync(path,'{}');
  const initial=stageInstall({...request});
  const seed=initial.seed;
  const receipt=JSON.stringify({directory,seed,files:[{path,owned:[owned]}]});
  writeFileSync(join(state,'owned.json'),receipt);
  const foreign='["./foreign", { "keep" : true }]';
  const edited=JSON.stringify({plugin:[[owned,{edited:true}]],provider:{openabstractions:{...seed.value,name:'Edited'}}})
    .replace(']]', '],'+foreign+']');
  writeFileSync(path,edited);
  const result=main(request,dependencies);
  assert.deepEqual(document(readFileSync(path,'utf8')).plugin,[JSON.parse(foreign),owned]);
  assert.ok(readFileSync(path,'utf8').includes(foreign));
  assert.deepEqual(result.seed,seed);
  assert.equal(readFileSync(path+'.openabstractions.bak','utf8'),edited);
  main({...request,operation:'rollback'},dependencies);
  assert.equal(readFileSync(path,'utf8'),edited);
  assert.deepEqual(JSON.parse(readFileSync(join(state,'owned.json'),'utf8')),JSON.parse(receipt));
  const changed=edited.replace(seed.value.npm,'file:///foreign/index.js');
  writeFileSync(path,changed);
  assert.throws(()=>main({...request},dependencies),/provider was edited/);
  assert.equal(readFileSync(path,'utf8'),changed);
  const duplicate=edited.replace(foreign,JSON.stringify(owned));
  writeFileSync(path,duplicate);
  assert.throws(()=>main({...request},dependencies),/Ambiguous plugin/);
  assert.equal(readFileSync(path,'utf8'),duplicate);
});

test('lower relative plugin origins and compatible inherited provider remain in their owning document', t => {
  const root=fixture(t);
  const lower='// lower origin\n'+JSON.stringify({plugin:['./relative.js'],provider:{openabstractions:{npm:'@openabstractions/opencode',options:{person:true}}}});
  const upper='// upper\n'+JSON.stringify({provider:{openabstractions:{options:{override:true}},foreign:{npm:'foreign'}}});
  writeFileSync(join(root,'config.json'),lower);writeFileSync(join(root,'opencode.jsonc'),upper);
  const output=stageInstall({directory:root,plugin:owned});
  const legacy=output.plans.find(item=>item.path.endsWith('config.json'));
  assert.deepEqual(document(legacy.text).plugin,['./relative.js',owned]);
  assert.ok(legacy.text.includes('// lower origin'));
  assert.equal(output.plans.find(item=>item.path.endsWith('opencode.jsonc')).text,upper);
  assert.equal(output.seed,null);
  assert.deepEqual(document(legacy.text).provider,document(lower).provider);
});

test('cold provider seed and plugin stay beside the inherited active list, preserving higher config', t => {
  const root=fixture(t);writeFileSync(join(root,'opencode.json'),'{"plugin":["./foreign.js"]}');
  const upper='// person settings\n{"other":true}';writeFileSync(join(root,'opencode.jsonc'),upper);
  const output=stageInstall({directory:root,plugin:owned});
  assert.ok(output.seed.path.endsWith('opencode.json'));
  assert.equal(output.plans.find(item=>item.path.endsWith('opencode.jsonc')).text,upper);
  const server=output.plans.find(item=>item.path.endsWith('opencode.json'));
  assert.deepEqual(document(server.text).plugin,['./foreign.js',owned]);
  assert.equal(document(server.text).provider.openabstractions.options.guarantees[0],'abstraction.inference/local-only@1');
});

test('foreign provider ID and edited seed refuse before account writes', t => {
  const root=fixture(t);const path=join(root,'opencode.jsonc');
  const foreign='{"provider":{"openabstractions":{"npm":"foreign"}}}';writeFileSync(path,foreign);
  assert.throws(()=>stageInstall({directory:root,plugin:owned}),/belongs/);
  assert.equal(readFileSync(path,'utf8'),foreign);
  writeFileSync(path,'{"provider":{"openabstractions":{"npm":"edited"}}}');
  assert.throws(()=>removalPlans(root,[],{path,value:{npm:'original'}}),/seeded provider was edited/);
  assert.equal(document(readFileSync(path,'utf8')).provider.openabstractions.npm,'edited');
});

test('guarding an unselected inherited file prevents any candidate writes', t => {
  const root=fixture(t);writeFileSync(join(root,'opencode.jsonc'),'{"plugin":[]}');
  const output=stageInstall({directory:root,plugin:owned});
  writeFileSync(join(root,'config.json'),'{"person":true}');
  assert.throws(()=>applyPair(output.plans,output.guards),/changed/);
  assert.equal(readFileSync(join(root,'opencode.jsonc'),'utf8'),'{"plugin":[]}');
});

test('later inherited personal provider options refuse seed retirement and upgrade', t => {
  const root=fixture(t);const lower=join(root,'config.json');
  writeFileSync(lower,'{"plugin":["./relative.js"]}');
  const installed=stageInstall({directory:root,plugin:owned});
  applyPair(installed.plans,installed.guards);
  const upper='// preserve personal override\n{"provider":{"openabstractions":{"options":{"person":true}}}}';
  writeFileSync(join(root,'opencode.jsonc'),upper);
  const records=installed.plans.map(item=>({path:item.path,owned:item.owned}));
  assert.throws(()=>removalPlans(root,records,installed.seed),/depends on/);
  assert.throws(()=>stageInstall({directory:root,plugin:owned+'-next',ownedFiles:records,seed:installed.seed}),/depends on/);
  assert.equal(readFileSync(join(root,'opencode.jsonc'),'utf8'),upper);
  assert.equal(document(readFileSync(lower,'utf8')).provider.openabstractions.npm,installed.seed.value.npm);
});

test('owned registrations retire across all candidates while foreign origins stay byte-identical', t => {
  const root=fixture(t);const records=[];
  for (const name of ['config.json','opencode.json','opencode.jsonc','tui.json','tui.jsonc']) {
    const path=join(root,name);const text='// '+name+'\n{"plugin":["./'+name+'.js",'+JSON.stringify(old)+']}';
    writeFileSync(path,text);records.push({path,owned:[old]});
  }
  const result=stageInstall({directory:root,plugin:owned,ownedFiles:records});
  for (const item of result.plans) {
    assert.ok(item.text.includes('// '));assert.ok(item.text.includes('"./'));
    assert.ok(!document(item.text).plugin.includes(old));
  }
  assert.equal(result.plans.filter(item=>document(item.text).plugin.includes(owned)).length,2);
});

test('symlink target identity changes refuse even when bytes are identical', t => {
  const root=fixture(t);const path=join(root,'opencode.json');const first=join(root,'first');const second=join(root,'second');
  writeFileSync(first,'{}');writeFileSync(second,'{}');
  try {symlinkSync(first,path,'file');} catch (error) {if(error.code==='EPERM'){t.skip('Windows symlink privilege unavailable');return;}throw error;}
  const plans=[{kind:'server',...snapshot(path),text:'{"plugin":[]}'},{kind:'tui',...snapshot(join(root,'tui.json')),text:'{}'}];
  unlinkSync(path);symlinkSync(second,path,'file');
  assert.throws(()=>applyPair(plans),/changed/);
  assert.equal(readFileSync(first,'utf8'),'{}');assert.equal(readFileSync(second,'utf8'),'{}');
});

test('ordinary install, repair, upgrade, uninstall and MSI rollback retain both config owners', t => {
  const root=fixture(t);const directory=join(root,'account');mkdirSync(directory);
  const state=join(root,'state');const server=join(directory,'opencode.jsonc');const tui=join(directory,'tui.jsonc');
  const originalServer='// server person comment\n{"plugin":["foreign"],"other":1}';
  const originalTui='// tui person comment\n{"plugin":["foreign-tui"],"theme":"person"}';
  writeFileSync(server,originalServer);writeFileSync(tui,originalTui);
  const dependencies={};let iteration=0;
  const request=operation=>({operation,directory,state,plugin:owned});
  const commit=()=>unlinkSync(join(state,'pending.json'));
  main(request('install'),dependencies);
  assert.deepEqual(document(readFileSync(server,'utf8')).plugin,['foreign',owned]);
  assert.deepEqual(document(readFileSync(tui,'utf8')).plugin,['foreign-tui',owned]);
  main(request('rollback'),dependencies);
  assert.equal(readFileSync(server,'utf8'),originalServer);assert.equal(readFileSync(tui,'utf8'),originalTui);
  assert.equal(existsSync(join(state,'owned.json')),false);
  main(request('install'),dependencies);commit();
  main(request('repair'),dependencies);commit();
  const next=owned+'-next';main({...request('upgrade'),plugin:next},dependencies);commit();
  assert.deepEqual(document(readFileSync(server,'utf8')).plugin,['foreign',next]);
  const beforeRemove=readFileSync(server,'utf8');
  main(request('remove'),dependencies);
  assert.deepEqual(document(readFileSync(server,'utf8')).plugin,['foreign']);
  assert.deepEqual(document(readFileSync(tui,'utf8')).plugin,['foreign-tui']);
  main(request('rollback'),dependencies);
  assert.equal(readFileSync(server,'utf8'),beforeRemove);
  main(request('remove'),dependencies);commit();
  assert.equal(document(readFileSync(server,'utf8')).provider.openabstractions,undefined);
  assert.ok(readFileSync(server,'utf8').includes('// server person comment'));
  assert.equal(document(readFileSync(tui,'utf8')).theme,'person');
});
