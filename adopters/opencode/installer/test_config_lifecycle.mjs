import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtempSync, mkdirSync, readFileSync, writeFileSync, existsSync, rmSync, symlinkSync, readdirSync, unlinkSync, rmdirSync} from 'node:fs';
import {join, resolve, dirname} from 'node:path';
import {createHash} from 'node:crypto';
import {pathToFileURL} from 'node:url';
import fs from 'node:fs';
import childProcess from 'node:child_process';
import {syncBuiltinESMExports} from 'node:module';
import {tmpdir} from 'node:os';
import {document, removeOwned, installedDelta, snapshot, applyPair, selectedDocuments, stageInstall, removalPlans, rollbackTransaction, observeInstalled, readBootstrapPayload, verifyInstalledPlugin, main, runConfigureRequest} from './config-lifecycle.js';
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
    const stage=join(directory,'stage');
    assert.throws(()=>stageInstall({directory,stage,plugin:owned,cli:'fixture'},forbidden,()=>true,()=>[]),/Duplicate (plugin|provider)/);
    assert.equal(existsSync(stage),false);
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
      const stage=join(directory,'stage');
      assert.throws(()=>stageInstall({directory,stage,plugin:owned,cli:'fixture',mcpLauncher:launcher,mcpSeed},forbidden,()=>true,()=>[]),/Duplicate MCP/);
      assert.equal(existsSync(stage),false);
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
    result=main({operation:'observe',directory:input.directory,plugin:input.plugin,state,stage:join(root,'absent-stage')},
      {documents:()=>({all:input.documents}),read:()=>JSON.stringify(input.ownership),exists:()=>false,run:fail,verifyCLI:fail});
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
function officialFixture(_exe,args,options) {
  assert.equal(options.env.BUN_BE_BUN,undefined);
  const config=join(options.env.XDG_CONFIG_HOME,'opencode');
  for (const name of readdirSync(config)) {
    if(!/^(opencode|tui)\.jsonc?$/.test(name))continue;
    const path=join(config,name);const value=document(readFileSync(path,'utf8'));
    value.plugin=[...(value.plugin??[]),args[1]];
    writeFileSync(path,JSON.stringify(value));
  }
  return {status:0,stdout:'server + tui',stderr:''};
}

test('exact committed Repair keeps config and ownership bytes without launching or reading unused bootstrap members', t => {
  const root=fixture(t),directory=join(root,'account'),cache=join(root,'cache'),state=join(root,'state');
  const plugin=join(root,'installed-plugin'),payload=join(root,'bootstrap');
  mkdirSync(directory);mkdirSync(plugin);
  const files=['config/package.json','config/package-lock.json','config/node_modules/@opencode-ai/plugin/package.json','cache/bin/rg.exe'];
  const hash=bytes=>createHash('sha256').update(bytes).digest('hex');
  const pluginMember=join(plugin,'package.json'), pluginBytes='{"name":"fixture"}';
  const pluginIndex=join(root,'plugin-files.json');
  const pluginInventory=JSON.stringify({'package.json':{bytes:pluginBytes.length,sha256:hash(pluginBytes)}});
  writeFileSync(pluginMember,pluginBytes);writeFileSync(pluginIndex,pluginInventory);
  const verifyPlugin=dir=>verifyInstalledPlugin(dir,hash(pluginInventory),pluginIndex);
  const inventory={};
  for(const name of files) {
    const path=join(payload,...name.split('/')),bytes=Buffer.from(name);
    mkdirSync(dirname(path),{recursive:true});writeFileSync(path,bytes);
    inventory[name]={bytes:bytes.length,sha256:hash(bytes)};
  }
  const index=join(payload,'files.json'),raw=JSON.stringify(inventory);
  writeFileSync(index,raw);
  const pin=hash(raw), bootstrap=()=>readBootstrapPayload(payload,pin);
  const request=operation=>({operation,directory,cache,state,plugin,cli:'fixture',stage:join(root,operation)});
  main(request('install'),{bootstrap,verifyPlugin,run:officialFixture,verifyCLI:()=>true});
  const pending=join(state,'pending.json'),receipt=join(state,'owned.json');
  unlinkSync(pending);
  const before=readFileSync(receipt), config=join(directory,'opencode.json'), original=readFileSync(config);
  unlinkSync(join(payload,'cache','bin','rg.exe'));
  const fail=()=>assert.fail('exact Repair must not invoke the official CLI');
  const result=main(request('repair'),{bootstrap,verifyPlugin,run:fail,verifyCLI:fail});
  assert.equal(result.unchanged,true);
  assert.deepEqual(readFileSync(receipt),before);
  assert.deepEqual(readFileSync(config),original);
  assert.equal(existsSync(pending),false);
  assert.equal(existsSync(request('repair').stage),false);

  writeFileSync(index, raw+'\n');
  assert.throws(()=>main(request('repair'),{bootstrap,verifyPlugin,run:fail,verifyCLI:fail}),/bootstrap inventory changed/);
  writeFileSync(index,raw);
  writeFileSync(pluginMember,pluginBytes.replace('fixture','changed'));
  assert.throws(()=>main(request('repair'),{bootstrap,verifyPlugin,run:fail,verifyCLI:fail}),/plugin member changed/);
  assert.deepEqual(readFileSync(receipt),before);
  assert.deepEqual(readFileSync(config),original);
  writeFileSync(pluginMember,pluginBytes);
  writeFileSync(pluginIndex,pluginInventory+'\n');
  assert.throws(()=>main(request('repair'),{bootstrap,verifyPlugin,run:fail,verifyCLI:fail}),/plugin inventory changed/);
  writeFileSync(pluginIndex,pluginInventory);

  const stale=JSON.parse(before);
  stale.files.find(item=>item.path===config).owned=[[plugin,{options:'stale receipt'}]];
  writeFileSync(receipt,JSON.stringify(stale));
  assert.throws(()=>main(request('repair'),{bootstrap,verifyPlugin,verifyCLI:()=>true,run:fail}),/bootstrap member changed/);
  assert.equal(existsSync(pending),false);
  writeFileSync(receipt,before);
  // The failed staging directory belongs to this fixture.
  rmSync(request('repair').stage,{recursive:true,force:true});
  const foreign=Buffer.from(original.toString('utf8').replace('{','{ "autoupdate": false,')+'\n// changed by person\n');
  writeFileSync(config,foreign);
  const observe=()=>observeInstalled({directory,plugin,documents:selectedDocuments(directory).all,
    ownership:JSON.parse(readFileSync(receipt))});
  assert.throws(observe,/Installed config changed/);
  const refreshed=main(request('repair'),{bootstrap,verifyPlugin,verifyCLI:fail,run:fail});
  assert.equal(refreshed.command.skipped,'configuration_current');
  assert.deepEqual(readFileSync(config),foreign);
  assert.equal(existsSync(request('repair').stage),false);
  const journal=JSON.parse(readFileSync(pending));
  assert.equal(journal.configUnchanged,true);
  assert.deepEqual(journal.plans,[]);
  assert.equal(observe().owned_plugins.length,2);
  // The person's editor can be partway through its next save at rollback.
  const inProgress=Buffer.from('{"autoupdate":');
  writeFileSync(config,inProgress);
  main(request('rollback'),{bootstrap});
  assert.deepEqual(JSON.parse(readFileSync(receipt)),JSON.parse(before));
  assert.deepEqual(readFileSync(config),inProgress);
  assert.equal(existsSync(pending),false);
  writeFileSync(config,foreign);
  assert.throws(observe,/Installed config changed/);

  const reordered=document(foreign.toString('utf8'));
  reordered.plugin.push('person-added-after-OA');
  writeFileSync(config,JSON.stringify(reordered));
  assert.throws(()=>main(request('repair'),{bootstrap,verifyPlugin,verifyCLI:()=>true,run:fail}),/bootstrap member changed/);
  assert.equal(existsSync(pending),false);
  rmSync(request('repair').stage,{recursive:true,force:true});

  // Changing an owned entry still uses the ordinary CLI-backed repair.
  const edited=document(foreign.toString('utf8'));
  edited.plugin[edited.plugin.length-1]=[plugin,{edited:true}];
  writeFileSync(config,JSON.stringify(edited));
  writeFileSync(join(payload,'cache','bin','rg.exe'),files.at(-1));
  let repairs=0;
  const repaired=main(request('repair'),{bootstrap,verifyPlugin,verifyCLI:()=>true,run:(...args)=>{
    repairs++;
    return officialFixture(...args);
  }});
  assert.equal(repairs,1);
  assert.equal(repaired.command.status,0);
  assert.equal(document(readFileSync(config,'utf8')).autoupdate,false);
  assert.deepEqual(document(readFileSync(config,'utf8')).plugin,document(original.toString('utf8')).plugin);
});

test('wrapper installation atomically retires only its owned MCP entry and rollback restores the old roster', t => {
  const root=fixture(t),directory=join(root,'account'),tools=join(root,'selected OA tools');mkdirSync(directory);mkdirSync(tools);
  const launcher=join(tools,'openabstractions-mcp.exe');writeFileSync(launcher,'inert generic role');
  const path=join(directory,'opencode.jsonc');
  const value={type:'local',command:[launcher],timeout:60000};
  const initial='// person comment\n'+JSON.stringify({plugin:['foreign'],mcp:{OpenAbstractions:value,foreign:{type:'remote',url:'https://example.invalid'}},personal:true});
  writeFileSync(path,initial);
  const request={directory,stage:join(root,'stage'),plugin:owned,cli:'fixture',mcpLauncher:launcher,mcpSeed:{path,value}};
  assert.throws(()=>stageInstall({...request,stage:join(root,'failed-stage')},()=>({status:1,stdout:'',stderr:'inert wrapper refusal'}),()=>true,()=>[]),/inert wrapper refusal/);
  assert.throws(()=>stageInstall({...request,mcpSeed:{path,value:{...value,timeout:1800000}}},officialFixture,()=>true,()=>[]),/registration was edited/);
  assert.equal(readFileSync(path,'utf8'),initial,'failed wrapper preparation keeps the old Messages and Asks roster');
  const install=stageInstall(request,officialFixture,()=>true,()=>[]);
  assert.equal(readFileSync(path,'utf8'),initial);
  assert.equal(install.mcpSeed,null);
  applyPair(install.plans,install.guards);
  const configured=readFileSync(path,'utf8');assert.ok(configured.includes('// person comment'));
  const current=document(configured);assert.equal(current.mcp.OpenAbstractions,undefined);assert.ok(current.plugin.some(entry=>(Array.isArray(entry)?entry[0]:entry)===owned));
  assert.deepEqual(current.mcp.foreign,{type:'remote',url:'https://example.invalid'});
  const records=install.plans.map(item=>({path:item.path,owned:item.owned}));
  const repair=stageInstall({...request,stage:join(root,'repair'),ownedFiles:records,seed:install.seed,mcpSeed:install.mcpSeed,mcpLauncher:undefined},officialFixture,()=>true,()=>[]);
  assert.equal(repair.mcpSeed,null,'repair never adds a second roster');
  const transaction={plans:install.plans.map(item=>({kind:item.kind,path:item.path,identity:item.identity,before:item.bytes===null?null:item.bytes.toString('base64'),after:item.text}))};
  rollbackTransaction(transaction);assert.equal(readFileSync(path,'utf8'),initial);
  const restored=stageInstall({...request,stage:join(root,'restore')},officialFixture,()=>true,()=>[]);applyPair(restored.plans,restored.guards);
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
    assert.throws(()=>stageInstall({directory,stage:join(directory,'stage'),plugin:owned,cli:'fixture',mcpLauncher:launcher},()=>{throw Error('must not launch');},()=>true,()=>[]),/already owned/);
    assert.equal(readFileSync(path,'utf8'),text);assert.equal(existsSync(join(directory,'stage')),false);
  }
  const directory=join(root,'exact');mkdirSync(directory);const path=join(directory,'opencode.json');
  writeFileSync(path,JSON.stringify({mcp:{OpenAbstractions:expected}}));
  assert.throws(()=>stageInstall({directory,stage:join(root,'exact-stage'),plugin:owned,cli:'fixture',mcpLauncher:launcher},officialFixture,()=>true,()=>[]),/already owned/);
  assert.deepEqual(document(readFileSync(path,'utf8')).mcp.OpenAbstractions,expected);
  assert.throws(()=>removalPlans(directory,[],null,{path,value:{...expected,timeout:5000}}),/Invalid installer-owned MCP/);
  writeFileSync(join(directory,'config.json'),JSON.stringify({mcp:{OpenAbstractions:{...expected,command:['foreign']}}}));
  assert.throws(()=>stageInstall({directory,stage:join(root,'shadow'),plugin:owned,cli:'fixture',mcpLauncher:launcher},officialFixture,()=>true,()=>[]),/already owned/);
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
  const dependencies={run:officialFixture,verifyCLI:()=>true,bootstrap:()=>[]};let serial=0;
  const request=operation=>({operation,directory,state,stage:join(root,'stage-'+serial++),plugin:owned,cli:'fixture'});
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

test('foreign tuple bytes and comments survive removal and insertion', () => {
  const foreign='["foreign", { "note" : "x,y", "unicode" : "\\u0041" }]';
  const before='{ "plugin" : ['+foreign+', /* person comment */ '+JSON.stringify(old)+'], "other" : 1 }';
  const staged=JSON.stringify({plugin:[['foreign',{note:'x,y',unicode:'A'}],owned],other:1});
  const next=installedDelta(before,staged,[old],owned).text;
  assert.ok(next.includes(foreign));assert.ok(next.includes('/* person comment */'));assert.ok(next.includes('"other" : 1'));
});

test('staged official delta cannot rewrite foreign plugin registrations', () => {
  const before = JSON.stringify({plugin:['foreign',old]});
  const next = installedDelta(before,JSON.stringify({plugin:['foreign',owned]}),[old],owned);
  assert.deepEqual(document(next.text).plugin,['foreign',owned]);
  assert.throws(() => installedDelta(before,JSON.stringify({plugin:['changed',owned]}),[old],owned),/foreign/);
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

test('official receiving keeps foreign JSONC out of its private stage and preserves its bytes', t => {
  const root=fixture(t),config=join(root,'account');mkdirSync(config);
  const path=join(config,'opencode.jsonc');
  const foreign='["./plugins/foreign", { "alias" : "local", "paths" : ["./relative"] }]';
  const initial='// keep\n{"plugin":['+foreign+' /* foreign comment */], "personal": { "alias" : "./relative" }}';
  writeFileSync(path,initial);
  const stage=join(root,'stage');let called=0;
  const request={directory:config,stage,plugin:owned,cli:'fixed-cli'};
  const output=stageInstall(request,(exe,args,options)=>{
    called++;assert.equal(exe,'fixed-cli');assert.deepEqual(args,['plugin',owned,'--global']);
    assert.equal(options.env.BUN_BE_BUN,undefined);assert.equal(options.env.NODE_OPTIONS,undefined);assert.equal(options.env.BUN_OPTIONS,undefined);
    assert.equal(options.env.XDG_CONFIG_HOME,join(stage,'config'));
    for (const name of ['opencode.jsonc','tui.json']) {
      const staged=join(stage,'config','opencode',name);const data=document(readFileSync(staged,'utf8'));
      const normalized=data.plugin.map(entry=>Array.isArray(entry)?[new URL('file:///stage/foreign').href,entry[1]]:entry);
      assert.deepEqual(data,{plugin:[]});
      writeFileSync(staged,JSON.stringify({plugin:[...normalized,owned],other:'ignored'}));
    }
    return {status:0,stdout:'detected server + tui',stderr:''};
  },()=>true,()=>[]);
  assert.equal(called,1);
  assert.equal(readFileSync(path,'utf8'),initial,'staging changes no account document');
  assert.throws(()=>stageInstall({...request,stage:join(root,'extra')},(exe,args,options)=>{
    const result=officialFixture(exe,args,options);
    writeFileSync(join(options.env.OPENCODE_CONFIG_DIR,'tui.json'),JSON.stringify({plugin:[owned,'unexpected']}));
    return result;
  },()=>true,()=>[]),/changed foreign registrations/);
  assert.equal(readFileSync(path,'utf8'),initial,'unexpected upstream output changes no account document');
  applyPair(output.plans,output.guards);
  const configured=readFileSync(path,'utf8');
  assert.ok(configured.includes('// keep'));
  assert.ok(configured.includes(foreign+' /* foreign comment */'));
  assert.ok(configured.includes('"personal": { "alias" : "./relative" }'));
  assert.deepEqual(document(configured).plugin,[JSON.parse(foreign),owned]);
  assert.equal(document(configured).other,undefined);
  assert.throws(()=>stageInstall({...request,stage:join(root,'unowned')},()=>{
    assert.fail('unowned OA entry must refuse before launch');
  },()=>true,()=>[]),/already owned/);
});

test('the configure worker reports the unowned restored plugin conflict distinctly', t => {
  const root=fixture(t),directory=join(root,'account'),state=join(root,'state');
  const work=join(root,'work'),other=join(root,'other');
  for (const dir of [directory,state,work,other]) mkdirSync(dir);
  const config=join(directory,'opencode.jsonc');
  const original='// Restored preferences\n'+JSON.stringify({plugin:[owned,'./other.js'],personal:{keep:true}});
  writeFileSync(config,original);
  const request=work=>({operation:'install',directory,state,work,stage:join(work,'stage'),plugin:owned,cli:'fixture'});
  const conflict='The OpenAbstractions plugin registration is already owned by the person or different settings';
  const deps={bootstrap:()=>[],verifyCLI:()=>true,run:()=>assert.fail('must refuse before launching the official command')};
  assert.equal(runConfigureRequest(request(work),work,deps),80);
  assert.deepEqual(JSON.parse(readFileSync(join(work,'result.json'),'utf8')),{error:conflict});
  assert.equal(readFileSync(config,'utf8'),original);
  assert.equal(existsSync(join(state,'owned.json')),false);
  assert.equal(existsSync(join(state,'pending.json')),false);
  assert.equal(existsSync(config+'.openabstractions.bak'),false);

  // The error's type, rather than arbitrary child text, controls the exit.
  const ordinary={...deps,verifyCLI:()=>{throw Error(conflict);}};
  assert.equal(runConfigureRequest(request(other),other,ordinary),1);
  assert.deepEqual(JSON.parse(readFileSync(join(other,'result.json'),'utf8')),{error:conflict});
});

test('repair restores exact recorded OA sources and keeps the before image and foreign bytes', t => {
  const root=fixture(t), directory=join(root,'account'), state=join(root,'state');
  mkdirSync(directory);mkdirSync(state);
  const path=join(directory,'opencode.jsonc');
  const request={operation:'repair',directory,state,stage:join(root,'stage'),plugin:owned,cli:'fixture'};
  const bootstrap=()=>[], dependencies={bootstrap,run:officialFixture,verifyCLI:()=>true};
  writeFileSync(path,'{}');
  const initial=stageInstall({...request,stage:join(root,'initial')},officialFixture,()=>true,bootstrap);
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
  assert.throws(()=>main({...request,stage:join(root,'foreign')},dependencies),/provider was edited/);
  assert.equal(readFileSync(path,'utf8'),changed);
  const duplicate=edited.replace(foreign,JSON.stringify(owned));
  writeFileSync(path,duplicate);
  assert.throws(()=>main({...request,stage:join(root,'duplicate')},dependencies),/Ambiguous plugin/);
  assert.equal(readFileSync(path,'utf8'),duplicate);
});

test('official command stdout refusal and launch failure remain actionable', t => {
  const root=fixture(t);let index=0;
  for(const [result,message] of [[{status:1,stderr:'\u001b[0m\r\n',stdout:'\u001b[31mNo plugin targets found\u001b[0m'},/No plugin targets found/],
      [{status:null,error:new Error('private CLI launch refused')},/private CLI launch refused/],
      [{status:7,stderr:'',stdout:''},/exit 7/]]) {
    assert.throws(()=>stageInstall({directory:root,stage:join(root,'stage-'+(++index)),plugin:owned,cli:'fixture'},()=>result,()=>true,()=>[]),message);
  }
});

test('lower relative plugin origins and compatible inherited provider remain in their owning document', t => {
  const root=fixture(t);
  const lower='// lower origin\n'+JSON.stringify({plugin:['./relative.js'],provider:{openabstractions:{npm:'@openabstractions/opencode',options:{person:true}}}});
  const upper='// upper\n'+JSON.stringify({provider:{openabstractions:{options:{override:true}},foreign:{npm:'foreign'}}});
  writeFileSync(join(root,'config.json'),lower);writeFileSync(join(root,'opencode.jsonc'),upper);
  const output=stageInstall({directory:root,stage:join(root,'stage'),plugin:owned,cli:'fixture'},officialFixture,()=>true,()=>[]);
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
  const output=stageInstall({directory:root,stage:join(root,'stage'),plugin:owned,cli:'fixture'},officialFixture,()=>true,()=>[]);
  assert.ok(output.seed.path.endsWith('opencode.json'));
  assert.equal(output.plans.find(item=>item.path.endsWith('opencode.jsonc')).text,upper);
  const server=output.plans.find(item=>item.path.endsWith('opencode.json'));
  assert.deepEqual(document(server.text).plugin,['./foreign.js',owned]);
  assert.equal(document(server.text).provider.openabstractions.options.guarantees[0],'abstraction.inference/local-only@1');
});

test('foreign provider ID and edited seed refuse before account writes', t => {
  const root=fixture(t);const path=join(root,'opencode.jsonc');
  const foreign='{"provider":{"openabstractions":{"npm":"foreign"}}}';writeFileSync(path,foreign);
  assert.throws(()=>stageInstall({directory:root,stage:join(root,'stage'),plugin:owned,cli:'fixture'},officialFixture,()=>true,()=>[]),/belongs/);
  assert.equal(readFileSync(path,'utf8'),foreign);
  writeFileSync(path,'{"provider":{"openabstractions":{"npm":"edited"}}}');
  assert.throws(()=>removalPlans(root,[],{path,value:{npm:'original'}}),/seeded provider was edited/);
  assert.equal(document(readFileSync(path,'utf8')).provider.openabstractions.npm,'edited');
});

test('guarding an unselected inherited file prevents any candidate writes', t => {
  const root=fixture(t);writeFileSync(join(root,'opencode.jsonc'),'{"plugin":[]}');
  const output=stageInstall({directory:root,stage:join(root,'stage'),plugin:owned,cli:'fixture'},officialFixture,()=>true,()=>[]);
  writeFileSync(join(root,'config.json'),'{"person":true}');
  assert.throws(()=>applyPair(output.plans,output.guards),/changed/);
  assert.equal(readFileSync(join(root,'opencode.jsonc'),'utf8'),'{"plugin":[]}');
});

test('later inherited personal provider options refuse seed retirement and upgrade', t => {
  const root=fixture(t);const lower=join(root,'config.json');
  writeFileSync(lower,'{"plugin":["./relative.js"]}');
  const installed=stageInstall({directory:root,stage:join(root,'stage'),plugin:owned,cli:'fixture'},officialFixture,()=>true,()=>[]);
  applyPair(installed.plans,installed.guards);
  const upper='// preserve personal override\n{"provider":{"openabstractions":{"options":{"person":true}}}}';
  writeFileSync(join(root,'opencode.jsonc'),upper);
  const records=installed.plans.map(item=>({path:item.path,owned:item.owned}));
  assert.throws(()=>removalPlans(root,records,installed.seed),/depends on/);
  assert.throws(()=>stageInstall({directory:root,stage:join(root,'upgrade'),plugin:owned+'-next',cli:'fixture',ownedFiles:records,seed:installed.seed},officialFixture,()=>true,()=>[]),/depends on/);
  assert.equal(readFileSync(join(root,'opencode.jsonc'),'utf8'),upper);
  assert.equal(document(readFileSync(lower,'utf8')).provider.openabstractions.npm,installed.seed.value.npm);
});

test('owned registrations retire across all candidates while foreign origins stay byte-identical', t => {
  const root=fixture(t);const records=[];
  for (const name of ['config.json','opencode.json','opencode.jsonc','tui.json','tui.jsonc']) {
    const path=join(root,name);const text='// '+name+'\n{"plugin":["./'+name+'.js",'+JSON.stringify(old)+']}';
    writeFileSync(path,text);records.push({path,owned:[old]});
  }
  const result=stageInstall({directory:root,stage:join(root,'stage'),plugin:owned,cli:'fixture',ownedFiles:records},officialFixture,()=>true,()=>[]);
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

test('installed bootstrap source validates complete pins and refuses linked parents', t => {
  const root=fixture(t),inventory={};
  for(const name of ['config/package.json','config/package-lock.json','config/node_modules/@opencode-ai/plugin/package.json','cache/bin/rg.exe']) {
    const path=join(root,...name.split('/')),data=Buffer.from('inert '+name);mkdirSync(join(path,'..'),{recursive:true});writeFileSync(path,data);
    inventory[name]={bytes:data.length,sha256:createHash('sha256').update(data).digest('hex')};
  }
  const manifest=join(root,'files.json');writeFileSync(manifest,JSON.stringify(inventory));
  assert.equal(readBootstrapPayload(root).length,4);
  writeFileSync(join(root,'cache','bin','rg.exe'),'changed');
  assert.throws(()=>readBootstrapPayload(root),/changed/);
  const original=Buffer.from('inert cache/bin/rg.exe');writeFileSync(join(root,'cache','bin','rg.exe'),original);
  const external=join(root,'other');mkdirSync(external);writeFileSync(join(external,'rg.exe'),original);
  rmSync(join(root,'cache','bin'),{recursive:true});symlinkSync(external,join(root,'cache','bin'),'junction');
  assert.throws(()=>readBootstrapPayload(root),/link/);
  rmdirSync(join(root,'cache','bin'));
  mkdirSync(join(root,'cache','bin'));writeFileSync(join(root,'cache','bin','rg.exe'),original);
  inventory['config/../escape']={bytes:0,sha256:'a'.repeat(64)};writeFileSync(manifest,JSON.stringify(inventory));
  assert.throws(()=>readBootstrapPayload(root),/Invalid/);
});

test('bootstrap inventory rejects serialized Unicode case collisions before reading the colliding member', t => {
  const root=fixture(t),inventory={};
  const members=['config/package.json','config/package-lock.json','config/node_modules/@opencode-ai/plugin/package.json','cache/bin/rg.exe','config/node_modules/helper/K.js'];
  for(const name of members) {
    const path=join(root,...name.split('/')),data=Buffer.from('inert '+name);mkdirSync(dirname(path),{recursive:true});writeFileSync(path,data);
    inventory[name]={bytes:data.length,sha256:createHash('sha256').update(data).digest('hex')};
  }
  // This second serialized key is refused by the portable inventory policy.
  // No file with that spelling is needed to establish a filesystem identity.
  inventory['config/node_modules/helper/\u212a.js']=inventory['config/node_modules/helper/K.js'];
  writeFileSync(join(root,'files.json'),JSON.stringify(inventory));
  assert.throws(()=>readBootstrapPayload(root),/Invalid installed bootstrap member/);
});

test('bootstrap exact leaves preserve unmanaged files and own their install, upgrade, rollback and removal', t => {
  const root=fixture(t),directory=join(root,'account'),cache=join(root,'cache'),state=join(root,'state');mkdirSync(directory);
  writeFileSync(join(directory,'opencode.jsonc'),'// person\n{"plugin":["foreign"]}');
  const bytes=text=>Buffer.from(text),hash=value=>createHash('sha256').update(value).digest('hex');
  const source=[['config/package.json','{"dependencies":{"@opencode-ai/plugin":"fixture"}}'],
    ['config/package-lock.json','{"lockfileVersion":3}'],['config/node_modules/@opencode-ai/plugin/package.json','{}'],
    ['config/node_modules/@opencode-ai/plugin/index.js','original helper'],['cache/bin/rg.exe','inert rg']];
  let records=source.map(([name,text])=>({name,bytes:bytes(text),sha256:hash(bytes(text))})),iteration=0;
  const leaf=name=>join(name.startsWith('config/')?directory:cache,...name.split('/').slice(1));
  const unmanaged=leaf(source[4][0]);mkdirSync(dirname(unmanaged),{recursive:true});writeFileSync(unmanaged,records[4].bytes);
  const deps={bootstrap:()=>records,verifyCLI:()=>true,run:(exe,args,options)=>{
    assert.equal(options.env.npm_config_offline,'true');
    assert.equal(readFileSync(join(options.env.XDG_CACHE_HOME,'opencode','bin','rg.exe'),'utf8'),'inert rg');
    assert.equal(readFileSync(join(options.env.XDG_CONFIG_HOME,'opencode','node_modules','@opencode-ai','plugin','index.js'),'utf8'),records[3].bytes.toString());
    return officialFixture(exe,args,options);
  }};
  const request=operation=>({operation,directory,cache,state,stage:join(root,'stage-'+(++iteration)),plugin:owned,cli:'fixture'});
  const commit=()=>unlinkSync(join(state,'pending.json'));
  const installed=main(request('install'),deps);
  assert.equal(installed.bootstrap.files.length,records.length-1);
  assert.ok(!installed.bootstrap.files.some(row=>row.name===source[4][0]));
  assert.deepEqual(installed.bootstrap.theirs,[source[4][0]]);
  assert.deepEqual(document(readFileSync(join(directory,'opencode.jsonc'),'utf8')).plugin,['foreign',owned]);
  main(request('rollback'),deps);
  assert.ok(existsSync(unmanaged));assert.ok(!existsSync(leaf(source[3][0])));
  main(request('install'),deps);commit();
  records=records.map(row=>row.name===source[3][0]?{...row,bytes:bytes('new helper'),sha256:hash(bytes('new helper'))}:row);
  main(request('upgrade'),deps);assert.equal(readFileSync(leaf(source[3][0]),'utf8'),'new helper');
  main(request('rollback'),deps);assert.equal(readFileSync(leaf(source[3][0]),'utf8'),'original helper');
  records=source.map(([name,text])=>({name,bytes:bytes(text),sha256:hash(bytes(text))}));
  main(request('repair'),deps);commit();
  writeFileSync(leaf(source[3][0]),'person edit');
  assert.throws(()=>main(request('remove'),deps),/edited/);assert.equal(readFileSync(leaf(source[3][0]),'utf8'),'person edit');
  writeFileSync(leaf(source[3][0]),'original helper');
  main(request('remove'),deps);assert.ok(!existsSync(leaf(source[3][0])));assert.ok(existsSync(unmanaged));
  main(request('rollback'),deps);assert.equal(readFileSync(leaf(source[3][0]),'utf8'),'original helper');
  main(request('remove'),deps);commit();
  assert.ok(existsSync(unmanaged));assert.ok(!existsSync(leaf(source[0][0])));
  assert.ok(!existsSync(leaf(source[3][0])+'.openabstractions.bak'));
  // A differing existing leaf is the person's: install and remove leave it.
  writeFileSync(unmanaged,'different user rg');
  main(request('install'),deps);commit();assert.equal(readFileSync(unmanaged,'utf8'),'different user rg');
  main(request('remove'),deps);commit();assert.equal(readFileSync(unmanaged,'utf8'),'different user rg');
});

test("an existing user's own OpenCode config folder stays theirs byte for byte while OA installs and leaves", t => {
  // Every existing OpenCode user has OpenCode's own bootstrap (here plugin
  // 1.18.18, an extra dependency and their own plugin). OA's plugin loads its
  // @opencode-ai/plugin from its own folder, so the folder stays theirs.
  const root=fixture(t),directory=join(root,'account'),cache=join(root,'cache'),state=join(root,'state');mkdirSync(directory);
  const row=(name,text)=>({name,bytes:Buffer.from(text),sha256:createHash('sha256').update(text).digest('hex')});
  const records=[row('config/package.json','{"dependencies":{"@opencode-ai/plugin":"1.18.34"}}'),row('config/package-lock.json','{"lockfileVersion":3,"pinned":true}'),
    row('config/node_modules/@opencode-ai/plugin/package.json','{"version":"1.18.34"}'),row('cache/bin/rg.exe','pinned rg')];
  const leaf=name=>join(name.startsWith('config/')?directory:cache,...name.split('/').slice(1));
  const theirs={'config/package.json':'{\n  "dependencies": {\n    "@opencode-ai/plugin": "1.18.18",\n    "left-pad": "1.3.0"\n  }\n}\n',
    'config/package-lock.json':'{"lockfileVersion":3,"packages":{"node_modules/left-pad":{"version":"1.3.0"}}}',
    'config/node_modules/@opencode-ai/plugin/package.json':'{"version":"1.18.18"}','plugins/mine.js':'export default async () => ({})'};
  const write=()=>{for(const [name,text] of Object.entries(theirs)){const path=name.startsWith('plugins/')?join(directory,...name.split('/')):leaf(name);mkdirSync(dirname(path),{recursive:true});writeFileSync(path,text);}};
  const unchanged=()=>{for(const [name,text] of Object.entries(theirs)){const path=name.startsWith('plugins/')?join(directory,...name.split('/')):leaf(name);assert.equal(readFileSync(path,'utf8'),text,name);}};
  write();
  let iteration=0;const request=operation=>({operation,directory,cache,state,stage:join(root,'stage-'+(++iteration)),plugin:owned,cli:'fixture'});
  const deps={bootstrap:()=>records,verifyCLI:()=>true,run:officialFixture};
  const commit=()=>unlinkSync(join(state,'pending.json'));
  const installed=main(request('install'),deps);commit();
  unchanged();
  assert.deepEqual(installed.bootstrap.theirs,['config/package.json','config/package-lock.json','config/node_modules/@opencode-ai/plugin/package.json']);
  // Only the missing ripgrep is OA's.
  assert.deepEqual(installed.bootstrap.files.map(file=>file.name),['cache/bin/rg.exe']);
  assert.equal(readFileSync(leaf('cache/bin/rg.exe'),'utf8'),'pinned rg');
  // OpenCode with OA still loads: the official command registered OA's plugin.
  assert.ok(document(readFileSync(join(directory,'opencode.json'),'utf8')).plugin.includes(owned));
  main(request('repair'),deps);commit();unchanged();
  main(request('remove'),deps);commit();
  unchanged();assert.equal(existsSync(leaf('cache/bin/rg.exe')),false);
  assert.ok(!(document(readFileSync(join(directory,'opencode.json'),'utf8')).plugin??[]).includes(owned));
  // A person with only their package.json gets no OA dependency files beside it.
  rmSync(leaf('config/package-lock.json'));rmSync(join(directory,'node_modules'),{recursive:true});
  main(request('install'),deps);commit();
  assert.equal(readFileSync(leaf('config/package.json'),'utf8'),theirs['config/package.json']);
  assert.equal(existsSync(leaf('config/package-lock.json')),false);assert.equal(existsSync(join(directory,'node_modules')),false);
});

test('bootstrap upgrade retires dropped owned leaves and rollback restores the prior roster', t => {
  const root=fixture(t),directory=join(root,'account'),cache=join(root,'cache'),state=join(root,'state');mkdirSync(directory);
  const row=(name,text)=>({name,bytes:Buffer.from(text),sha256:createHash('sha256').update(text).digest('hex')});
  const retired='config/node_modules/helper/old.js',added='config/node_modules/helper/new.js';
  let records=[row('config/package.json','{}'),row('config/package-lock.json','{}'),row('config/node_modules/@opencode-ai/plugin/package.json','{}'),row('cache/bin/rg.exe','inert'),row(retired,'old helper')];
  const leaf=name=>join(name.startsWith('config/')?directory:cache,...name.split('/').slice(1));
  let iteration=0;const request=operation=>({operation,directory,cache,state,stage:join(root,'stage-'+(++iteration)),plugin:owned,cli:'fixture'});
  const deps={bootstrap:()=>records,verifyCLI:()=>true,run:officialFixture};
  const commit=()=>unlinkSync(join(state,'pending.json'));
  main(request('install'),deps);commit();
  records=[...records.filter(item=>item.name!==retired),row(added,'new helper')];
  const before=readFileSync(join(directory,'opencode.json'),'utf8');
  writeFileSync(leaf(retired),'person edit');
  assert.throws(()=>main(request('upgrade'),deps),/edited/);
  assert.equal(existsSync(join(state,'pending.json')),false);assert.equal(existsSync(leaf(added)),false);
  assert.equal(readFileSync(join(directory,'opencode.json'),'utf8'),before);
  writeFileSync(leaf(retired),'old helper');
  main(request('upgrade'),deps);
  assert.equal(existsSync(leaf(retired)),false);assert.equal(readFileSync(leaf(added),'utf8'),'new helper');
  writeFileSync(leaf(added),'person edit');
  assert.throws(()=>main(request('rollback'),deps),/edited/);
  assert.equal(existsSync(leaf(retired)),false);assert.equal(readFileSync(leaf(added),'utf8'),'person edit');
  writeFileSync(leaf(added),'new helper');
  main(request('rollback'),deps);
  assert.equal(readFileSync(leaf(retired),'utf8'),'old helper');assert.equal(existsSync(leaf(added)),false);
  main(request('upgrade'),deps);commit();
  main(request('remove'),deps);commit();
  assert.equal(existsSync(leaf(retired)),false);assert.equal(existsSync(leaf(added)),false);
});

test('retired bootstrap ownership refuses path escapes, duplicate names and changed spelling before writes', t => {
  const root=fixture(t),directory=join(root,'account'),cache=join(root,'cache'),state=join(root,'state');mkdirSync(directory);
  const row=(name,text)=>({name,bytes:Buffer.from(text),sha256:createHash('sha256').update(text).digest('hex')});
  const retired='config/node_modules/helper/old.js';
  let records=[row('config/package.json','{}'),row('config/package-lock.json','{}'),row('config/node_modules/@opencode-ai/plugin/package.json','{}'),row('cache/bin/rg.exe','inert'),row(retired,'old helper')];
  let iteration=0;const request=()=>({operation:'upgrade',directory,cache,state,stage:join(root,'stage-'+(++iteration)),plugin:owned,cli:'fixture'});
  const deps={bootstrap:()=>records,verifyCLI:()=>true,run:officialFixture};
  main({...request(),operation:'install'},deps);unlinkSync(join(state,'pending.json'));
  const receipt=join(state,'owned.json'),original=readFileSync(receipt,'utf8'),server=readFileSync(join(directory,'opencode.json'),'utf8');
  records=records.filter(item=>item.name!==retired);
  for(const change of [value=>value.bootstrap.files.at(-1).name='config/node_modules/../escape',
    value=>value.bootstrap.files.push({...value.bootstrap.files.at(-1),name:'config/node_modules/helper/OLD.js'})]) {
    const value=JSON.parse(original);change(value);writeFileSync(receipt,JSON.stringify(value));
    assert.throws(()=>main(request(),deps),/Invalid bootstrap ownership/);
    assert.equal(existsSync(join(state,'pending.json')),false);
    assert.equal(readFileSync(join(directory,'opencode.json'),'utf8'),server);
  }
  writeFileSync(receipt,original);
  records.push(row('config/node_modules/helper/OLD.js','new helper'));
  assert.throws(()=>main(request(),deps),/spelling changed/);
  assert.equal(existsSync(join(state,'pending.json')),false);
  assert.equal(readFileSync(join(directory,'node_modules','helper','old.js'),'utf8'),'old helper');
});

test('ordinary install, repair, upgrade, uninstall and MSI rollback retain both config owners', t => {
  const root=fixture(t);const directory=join(root,'account');mkdirSync(directory);
  const state=join(root,'state');const server=join(directory,'opencode.jsonc');const tui=join(directory,'tui.jsonc');
  const originalServer='// server person comment\n{"plugin":["foreign"],"other":1}';
  const originalTui='// tui person comment\n{"plugin":["foreign-tui"],"theme":"person"}';
  writeFileSync(server,originalServer);writeFileSync(tui,originalTui);
  const dependencies={run:officialFixture,verifyCLI:()=>true,bootstrap:()=>[]};let iteration=0;
  const request=operation=>({operation,directory,state,stage:join(root,'stage-'+(++iteration)),plugin:owned,cli:'fixture'});
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
