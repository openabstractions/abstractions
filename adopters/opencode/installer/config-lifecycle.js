// Fixed app-installer helper. Runs under the pinned private CLI's documented
// Bun host mode; it performs no OA requests and installs no permissions.
import {parse, parseTree, findNodeAtLocation, createScanner, SyntaxKind, modify, applyEdits} from 'jsonc-parser';
import {readFileSync, existsSync, mkdirSync, writeFileSync, unlinkSync, rmSync} from 'node:fs';
import {join, dirname, resolve, basename, isAbsolute} from 'node:path';
import {spawnSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {fileURLToPath, pathToFileURL} from 'node:url';
import {readConfigSnapshot, replaceConfig, mergeProvider, compatibleProvider, headings, guaranteeFor,
  mcpConfiguration, sameMCPConfiguration, validateMCPProperties} from '../configure.js';

// The native installer maps this private configure-worker exit to the
// exact plugin-registration conflict. Other refusals keep their usual exit.
const pluginRegistrationConflictExit = 80;
class PluginRegistrationConflict extends Error {}

const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const spec = entry => typeof entry === 'string' ? entry : Array.isArray(entry) && typeof entry[0] === 'string' ? entry[0] : null;
const digest = bytes => bytes === null ? null : createHash('sha256').update(bytes).digest('hex');
// The official staged command owns its formatting. Applying its owned delta
// uses the same editor without reformatting unrelated source bytes.
const format = {};
export const officialCLIHash = '5283c2cd5b4285855daa97e6f7d17bf48146227cdf2a940e79e0f2b7869fd06e';

function payloadName(name) {
  return typeof name==='string'&&!name.includes('\\')&&!name.includes(':')&&!name.split('/').some(part=>['','.','..'].includes(part));
}

function bootstrapName(name) {
  return payloadName(name)&&(['config/package.json','config/package-lock.json','cache/bin/rg.exe'].includes(name) ||
    name.startsWith('config/node_modules/'));
}

function payloadPin(pin) {
  return pin&&Object.keys(pin).sort().join(',')==='bytes,sha256'&&
    Number.isSafeInteger(pin.bytes)&&pin.bytes>=0&&pin.bytes<=64*1024**2&&/^[a-f0-9]{64}$/.test(pin.sha256);
}

function installedPayloadIndex(path,pin,label) {
  const bytes=readConfigSnapshot(path,{ownedLeaf:true}).bytes;
  if(pin!==undefined&&(!/^[a-f0-9]{64}$/.test(pin)||digest(bytes)!==pin))
    throw new Error(`Installed ${label} inventory changed`);
  const inventory=JSON.parse(bytes.toString('utf8'));
  if(!inventory||typeof inventory!=='object'||Array.isArray(inventory)||Object.keys(inventory).length>10000)
    throw new Error(`Invalid installed ${label} inventory`);
  return inventory;
}

export function verifyInstalledPlugin(plugin,pin,index=resolve(dirname(fileURLToPath(import.meta.url)),'../../..','installer/plugin-files.json')) {
  if(pin===undefined||!isAbsolute(plugin))throw new Error('Installed plugin inventory proof required');
  const inventory=installedPayloadIndex(index,pin,'plugin');
  if(!Object.hasOwn(inventory,'package.json'))throw new Error('Incomplete installed plugin closure');
  for(const [name,member] of Object.entries(inventory)) {
    if(!payloadName(name)||!payloadPin(member))throw new Error('Invalid installed plugin member');
    const source=readConfigSnapshot(join(plugin,...name.split('/')),{ownedLeaf:true}).bytes;
    if(!source||source.length!==member.bytes||digest(source)!==member.sha256)
      throw new Error('Installed plugin member changed');
  }
}

// Conservative collision key for serialized members of the closed bootstrap
// inventory/ownership namespace. The original name reaches bootstrapPath;
// readConfigSnapshot and replaceConfig separately decide filesystem identity.
function bootstrapMemberKey(name) {
  if(!bootstrapName(name))throw new Error('Invalid installed bootstrap member');
  return name.toLowerCase();
}

export function readBootstrapPayload(root=resolve(dirname(fileURLToPath(import.meta.url)),'../../..','bootstrap'), pinnedInventory) {
  const inventory=installedPayloadIndex(join(root,'files.json'),pinnedInventory,'bootstrap');
  const folded=new Set(),rows=[];
  for(const [name,pin] of Object.entries(inventory).sort()) {
    const key=bootstrapMemberKey(name);
    if(folded.has(key)||!payloadPin(pin))throw new Error('Invalid installed bootstrap member');
    folded.add(key);
    const load=()=>{
      const source=readConfigSnapshot(join(root,...name.split('/')),{ownedLeaf:true});
      if(!source.bytes||source.bytes.length!==pin.bytes||digest(source.bytes)!==pin.sha256)throw new Error('Installed bootstrap member changed');
      return source.bytes;
    };
    if(pinnedInventory===undefined)rows.push({name,bytes:load(),sha256:pin.sha256});
    else {
      let bytes;
      rows.push({name,size:pin.bytes,sha256:pin.sha256,get bytes(){return bytes??=load();}});
    }
  }
  for(const name of ['config/package.json','config/package-lock.json','config/node_modules/@opencode-ai/plugin/package.json','cache/bin/rg.exe'])
    if(!rows.some(row=>row.name===name))throw new Error('Incomplete installed bootstrap closure');
  return rows;
}

function bootstrapPath(name,directory,cache) {
  if(!bootstrapName(name)||!isAbsolute(directory)||!isAbsolute(cache))throw new Error('Invalid bootstrap leaf root');
  return join(name.startsWith('config/')?directory:cache,...name.slice(name.indexOf('/')+1).split('/'));
}

function leafSnapshot(path,expected=null) {
  const value=readConfigSnapshot(path,{ownedLeaf:expected??true});
  return {path,...value,sha256:digest(value.bytes)};
}

function ownedBootstrap(previous,cache) {
  if(previous&&resolve(previous.cache)!==resolve(cache))throw new Error('The owned bootstrap cache folder changed');
  const owned=new Map(),folded=new Set();
  if(previous&&(!Array.isArray(previous.files)||previous.files.length>10000))throw new Error('Invalid bootstrap ownership receipt');
  for(const item of previous?.files??[]) {
    if(!bootstrapName(item.name))throw new Error('Invalid bootstrap ownership receipt');
    const key=bootstrapMemberKey(item.name);
    if(folded.has(key)||item.created!==true||!item.leaf?.file||item.leaf.links!==1||!/^[a-f0-9]{64}$/.test(item.sha256))throw new Error('Invalid bootstrap ownership receipt');
    folded.add(key);
    owned.set(item.name,item);
  }
  return owned;
}

function bootstrapPlans(records,directory,cache,previous,operation) {
  if(!records.length&&!previous)return [];
  const names=new Set(records.map(row=>row.name)),owned=ownedBootstrap(previous,cache);
  const priorSpellings=new Map([...owned.keys()].map(name=>[bootstrapMemberKey(name),name]));
  for(const record of records) {
    const prior=priorSpellings.get(bootstrapMemberKey(record.name));
    if(prior&&prior!==record.name)throw new Error('Bootstrap member spelling changed');
  }
  const plans=[];
  // The bootstrap only pre-seeds OpenCode's own first-use install on a fresh
  // account; OA's plugin loads its @opencode-ai/plugin from its own folder.
  // A person's existing config folder (their package.json, lock and
  // dependencies, kept by OpenCode for their own plugins) is theirs: when
  // any config leaf OA did not create exists, OA writes none of the config
  // closure, so their dependency tree never mixes with OA's. A missing
  // ripgrep is written on its own.
  const snapshotFor=record=>leafSnapshot(bootstrapPath(record.name,directory,cache),owned.get(record.name)?.leaf);
  const observed=new Map();
  const observe=record=>{
    if(!observed.has(record.name))observed.set(record.name,snapshotFor(record));
    return observed.get(record.name);
  };
  const theirs=root=>records.some(record=>record.name.startsWith(root)&&!owned.has(record.name)&&observe(record).bytes!==null);
  const kept={'config/':operation!=='remove'&&theirs('config/'),'cache/':operation!=='remove'&&theirs('cache/')};
  for(const record of records) {
    const prior=owned.get(record.name),current=observe(record);
    if(prior&&(current.identity!==prior.identity||current.sha256!==prior.sha256))throw new Error('An owned bootstrap leaf was edited');
    if(operation==='remove') {
      if(prior)plans.push({...current,name:record.name,after:null,owned:true});
      continue;
    }
    if(!prior&&(current.bytes!==null||kept[record.name.slice(0,record.name.indexOf('/')+1)])) {
      plans.push({...current,name:record.name,after:current.bytes,owned:false,theirs:true});
      continue;
    }
    const samePinnedLeaf=current.bytes!==null&&current.sha256===record.sha256&&current.bytes.length===record.size;
    plans.push({...current,name:record.name,after:samePinnedLeaf?current.bytes:record.bytes,owned:true});
  }
  // A dependency upgrade can retire filenames. The committed ownership receipt
  // retains their exact identity and bytes until this transaction removes them.
  for(const [name,prior] of owned) {
    if(names.has(name))continue;
    const current=leafSnapshot(bootstrapPath(name,directory,cache),prior.leaf);
    if(current.identity!==prior.identity||current.sha256!==prior.sha256)throw new Error('An owned bootstrap leaf was edited');
    plans.push({...current,name,after:null,owned:true});
  }
  return plans;
}

function bootstrapUnchanged(item) {
  const current=leafSnapshot(item.path,item.leaf);
  if(current.identity!==item.identity||current.sha256!==item.sha256)throw new Error('Bootstrap leaf changed while installer ran');
}

const bootstrapAfter = item => item.after===item.bytes ? item.sha256 : digest(item.after);

function bootstrapTransaction(plans,cache) {
  if(!plans.length)return null;
  return {cache,files:plans.filter(item=>item.sha256!==bootstrapAfter(item)).map(item=>({name:item.name,identity:item.identity,
    leaf:item.leaf,before:item.bytes===null?null:item.bytes.toString('base64'),after:bootstrapAfter(item),written:null}))};
}

function applyBootstrap(plans,transaction) {
  for(const item of plans)bootstrapUnchanged(item);
  for(const item of plans) {
    if(item.sha256===bootstrapAfter(item))continue;
    replaceConfig(item.path,item.bytes,item.after,item.identity,{backup:false,leaf:item.leaf});
    transaction.files.find(row=>row.name===item.name).written=leafSnapshot(item.path).leaf;
  }
}

function bootstrapOwnership(plans,cache) {
  return {cache,files:plans.filter(item=>item.owned&&item.after!==null).map(item=>{
    const current=leafSnapshot(item.path);
    if(current.sha256!==bootstrapAfter(item)||!current.leaf.file||current.leaf.links!==1)throw new Error('Created bootstrap leaf changed before commit');
    return {name:item.name,identity:current.identity,leaf:current.leaf,sha256:current.sha256,created:true};
  }),
  // Bootstrap leaves left as the person's: present and not created by OA, or
  // in a config folder that is theirs. OA never writes or removes them.
  theirs:plans.filter(item=>item.theirs).map(item=>item.name)};
}

function committedBootstrap(plans, cache, previous) {
  if(!previous)return plans.length===0;
  if(plans.some(item=>item.sha256!==bootstrapAfter(item)))return false;
  const files=plans.filter(item=>item.owned&&item.bytes!==null).map(item=>({
    name:item.name,identity:item.identity,leaf:item.leaf,sha256:item.sha256,created:true,
  }));
  return same({cache,files,theirs:plans.filter(item=>item.theirs).map(item=>item.name)},previous);
}

function rollbackBootstrapPlans(transaction,records,directory,cache,previous) {
  if(!transaction)return [];
  if(resolve(transaction.cache)!==resolve(cache))throw new Error('Rollback bootstrap cache folder changed');
  const names=new Set([...records.map(row=>row.name),...ownedBootstrap(previous,cache).keys()]),seen=new Set(),plans=[];
  for(const item of transaction.files) {
    if(!names.has(item.name)||seen.has(item.name))throw new Error('Unexpected rollback bootstrap leaf');
    seen.add(item.name);
    const path=bootstrapPath(item.name,directory,cache),current=leafSnapshot(path);
    const before=item.before===null?null:Buffer.from(item.before,'base64');
    if(current.sha256===digest(before)) {
      readConfigSnapshot(path,{ownedLeaf:item.leaf});
      continue;
    }
    if(!item.written||current.sha256!==item.after)throw new Error('Bootstrap rollback ownership is unknown or the leaf was edited');
    readConfigSnapshot(path,{ownedLeaf:item.written});
    plans.push({...current,after:before});
  }
  return plans;
}

function restoreBootstrap(plans) {
  for(const item of plans)bootstrapUnchanged(item);
  for(const item of [...plans].reverse())replaceConfig(item.path,item.bytes,item.after,item.identity,{backup:false,leaf:item.leaf});
}

function seedBootstrapStage(records,stage) {
  for(const item of records) {
    const path=bootstrapPath(item.name,join(stage,'config','opencode'),join(stage,'cache','opencode'));
    mkdirSync(dirname(path),{recursive:true});writeFileSync(path,item.bytes,{flag:'wx'});
  }
}

export function document(text) {
  validateMCPProperties(text?.trim() ? text : '{}');
  const errors = [];
  const value = parse(text?.trim() ? text : '{}', errors, {allowTrailingComma: true});
  if (errors.length || !value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Invalid config document');
  if (value.plugin !== undefined && !Array.isArray(value.plugin)) throw new Error('plugin must be an array');
  for (const entry of value.plugin ?? []) if (!spec(entry)) throw new Error('Invalid plugin entry');
  return value;
}

// Full recorded entries establish ownership. A person's changed options refuse
// instead of becoming an installer-owned replacement or deletion.
export function removeOwned(text, owned, recovery = null) {
  let result = text ?? '{}';
  const list = document(result).plugin ?? [];
  if (recovery && owned.some(entry => spec(entry) === recovery.plugin) &&
      (owned.filter(entry => spec(entry) === recovery.plugin).length !== 1 ||
        list.filter(entry => spec(entry) === recovery.plugin).length > 1)) {
    throw new Error('Ambiguous plugin ownership receipt or registration');
  }
  for (let index = list.length - 1; index >= 0; index--) {
    if (!owned.some(entry => spec(entry) === spec(list[index]))) continue;
    if (!owned.some(entry => same(entry, list[index]))) {
      const source = spec(list[index]);
      if (!recovery || source !== recovery.plugin || !isAbsolute(source)) {
        throw new Error('Owned plugin registration was edited');
      }
      recovery.repaired = true;
    }
    // 3.3.1 modify() leaves a closing quote when deleting a last array item
    // without a trailing comma. Use that same parser's AST/scanner offsets;
    // remove the owned value and one comma, retaining every comment/trivia.
    const array = findNodeAtLocation(parseTree(result), ['plugin']);
    const node = array.children[index];
    const scanner = createScanner(result, false);
    const comma = (start, end) => {
      scanner.setPosition(start);
      while (scanner.scan() !== SyntaxKind.EOF && scanner.getTokenOffset() < end) {
        if (scanner.getToken() === SyntaxKind.CommaToken) return scanner.getTokenOffset();
      }
      return null;
    };
    let separator = comma(node.offset + node.length, array.children[index + 1]?.offset ?? array.offset + array.length - 1);
    if (separator === null && index > 0) separator = comma(array.children[index - 1].offset + array.children[index - 1].length, node.offset);
    const edits = [{offset:node.offset,length:node.length,content:''}];
    if (separator !== null) edits.push({offset:separator,length:1,content:''});
    result = applyEdits(result, edits);
    document(result);
  }
  return result;
}

export function installedDelta(before, staged, prior, currentSpec) {
  const original = document(before ?? '{}').plugin ?? [];
  const next = document(staged).plugin ?? [];
  const additions = next.filter(entry => spec(entry) === currentSpec);
  if (additions.length !== 1) throw new Error('Official command did not produce one exact owned registration');
  const foreignBefore = original.filter(entry => !prior.some(value => spec(value) === spec(entry)));
  const foreignAfter = next.filter(entry => spec(entry) !== currentSpec);
  if (!same(foreignBefore, foreignAfter)) throw new Error('Official command changed foreign registrations');
  return appendOwned(removeOwned(before, prior), additions);
}

function appendOwned(result, additions) {
  const array = findNodeAtLocation(parseTree(result), ['plugin']);
  if (!array) {
    return {text: applyEdits(result, modify(result, ['plugin'], additions, format)), owned: additions};
  }
  const closing = array.offset + array.length - 1;
  const last = array.children.at(-1);
  const scanner = createScanner(result, true);
  scanner.setPosition(last ? last.offset + last.length : closing);
  const separator = last && scanner.scan() !== SyntaxKind.CommaToken ? ',' : '';
  const text = applyEdits(result, [{offset: closing, length: 0, content: separator + JSON.stringify(additions[0])}]);
  document(text);
  return {text, owned: additions};
}

export function snapshot(path) {
  const {bytes, identity} = readConfigSnapshot(path);
  return {path, bytes, identity, sha256: digest(bytes)};
}

function unchanged(item) {
  const current = snapshot(item.path);
  if (current.sha256 !== item.sha256 || current.identity !== item.identity) throw new Error(`${item.path} changed while installer ran`);
}

function checkConfigGuards(items) {
  for (const item of items) {
    if (item.bytes !== null) validateMCPProperties(item.bytes);
    if (item.text !== undefined && item.text !== null) validateMCPProperties(item.text);
    unchanged(item);
  }
}

// Only the five ordinary server/TUI candidate documents; compensation uses the existing file owner and
// refuses a concurrent user edit. No config document is parsed or rewritten by
// a native launcher, PowerShell, or a replacement JSONC implementation.
export function applyPair(plans, guards = [], replace = replaceConfig) {
  if (plans.length < 2 || plans.length > 5 || new Set(plans.map(p => p.kind)).size !== 2
    || new Set(plans.map(p=>resolve(p.path))).size !== plans.length
    || new Set(plans.map(p=>resolve(dirname(p.path)))).size !== 1
    || !plans.every(p => (p.kind === 'server' ? ['config.json','opencode.json','opencode.jsonc'] : ['tui.json','tui.jsonc']).includes(basename(p.path)))) throw new Error('Expected ordinary server and TUI config candidates');
  checkConfigGuards([...guards, ...plans]);
  const written = [];
  try {
    for (const item of plans) {
      if (item.bytes?.toString('utf8') === item.text || item.bytes === null && item.text === null) continue;
      replace(item.path, item.bytes, item.text, item.identity);
      written.push(item);
    }
  } catch (error) {
    const rollback = [];
    for (const item of written.reverse()) {
      try { replace(item.path, item.text === null ? null : Buffer.from(item.text), item.bytes === null ? null : item.bytes.toString('utf8'), item.identity); }
      catch (failure) { rollback.push(failure.message); }
    }
    if (rollback.length) throw new Error(`${error.message}; preserved changed input during rollback: ${rollback.join('; ')}`);
    throw error;
  }
}

export function selectedDocuments(directory) {
  const candidates = {server: ['opencode.jsonc','opencode.json','config.json'], tui: ['tui.jsonc','tui.json']};
  const all = Object.entries(candidates).flatMap(([kind, names]) => names.map(name => ({kind, ...snapshot(join(directory, name))})));
  const selected = Object.keys(candidates).map(kind => all.find(item => item.kind === kind && item.bytes !== null && Object.hasOwn(document(item.bytes.toString('utf8')),'plugin'))
    ?? all.find(item => item.kind === kind && item.bytes !== null)
    ?? {...snapshot(join(directory, kind === 'server' ? 'opencode.json' : 'tui.json')), kind});
  return {all, selected};
}

function installerProvider(plugin) {
  return mergeProvider(undefined,{name:headings.local,npm:pathToFileURL(join(plugin,'index.js')).href,models:{},
    options:{scope:'local',guarantees:[guaranteeFor('local')]}});
}

function ownProvider(plans, selected, plugin, seed) {
  const server = plans.filter(item=>item.kind==='server');
  let present=false;let npm;
  for (const item of [...server].reverse()) {
    const providers=document(item.text ?? '{}').provider;
    if (providers !== undefined && (!providers || typeof providers!=='object' || Array.isArray(providers))) throw new Error('Invalid providers');
    if (!Object.hasOwn(providers??{},'openabstractions')) continue;
    const value=providers.openabstractions;
    if (!value || typeof value!=='object' || Array.isArray(value)) throw new Error('Invalid OA provider block');
    present=true;if (Object.hasOwn(value,'npm')) npm=value.npm;
  }
  let target=selected;
  if (seed) {
    target=server.find(item=>resolve(item.path)===resolve(seed.path));
    const prior = target && document(target.text ?? '{}').provider?.openabstractions;
    if (!target || !same(prior, seed.value)) {
      const module = pathToFileURL(join(plugin, 'index.js')).href;
      if (!prior || prior.npm !== module || seed.value?.npm !== module) throw new Error('Installer-seeded provider was edited');
      target.repaired = true;
    }
    refuseSeedDependents(server, seed);
  } else if (present) {
    if (!compatibleProvider(npm)) throw new Error('OA provider ID belongs to another package');
    return null;
  }
  const value=installerProvider(plugin);
  target.text=applyEdits(target.text??'{}',modify(target.text??'{}',['provider','openabstractions'],value,format));
  return {path:target.path,value};
}

// A later document can inherit the seed's npm while adding personal options.
// Retiring that seed would strand the personal block; preserve both and refuse.
function refuseSeedDependents(plans, seed) {
  if (plans.some(item=>item.kind==='server' && resolve(item.path)!==resolve(seed.path)
    && Object.hasOwn(document(item.text??'{}').provider??{},'openabstractions'))) {
    throw new Error('Another config document now depends on the installer-seeded provider');
  }
}

function checkedMCPSeed(plans, seed) {
  if (seed === null || seed === undefined) return null;
  if (typeof seed !== 'object' || Object.keys(seed).sort().join(',') !== 'path,value' || typeof seed.path !== 'string'
    || !sameMCPConfiguration(seed.value, seed.value)) throw new Error('Invalid installer-owned MCP metadata');
  const target = plans.find(item => item.kind === 'server' && resolve(item.path) === resolve(seed.path));
  if (!target || !sameMCPConfiguration(document(target.text ?? '{}').mcp?.OpenAbstractions, seed.value)) {
    throw new Error('Installer-owned MCP registration was edited');
  }
  return target;
}

function ownMCP(plans, selected, launcher, seed) {
  const previous = checkedMCPSeed(plans, seed);
  if(launcher!==undefined)mcpConfiguration(launcher); // Existing selected-image validation only.
  for (const item of plans.filter(item => item.kind === 'server')) {
    const mcp = document(item.text ?? '{}').mcp;
    if (mcp !== undefined && (!mcp || typeof mcp !== 'object' || Array.isArray(mcp))) throw new Error('Invalid MCP configuration');
    if (!Object.hasOwn(mcp ?? {}, 'OpenAbstractions') || item === previous) continue;
    throw new Error('The OpenAbstractions MCP entry is already owned by the person or different settings; resolve that connection before installing canonical OA plugin tools');
  }
  // The same atomic plugin installation plan retires only its own exact old
  // default entry. Rollback restores the original file and its Asks roster.
  if(previous)previous.text=applyEdits(previous.text,modify(previous.text,['mcp','OpenAbstractions'],undefined,format));
  return null;
}

export function stageInstall({directory, stage, plugin, cli, mcpLauncher, mcpSeed = null, owned = {server: [], tui: []}, ownedFiles = [], seed = null}, run = spawnSync, verifyCLI = path => digest(readFileSync(path)) === officialCLIHash, bootstrap = readBootstrapPayload) {
  if (!verifyCLI(cli)) throw new Error('Private official CLI identity changed');
  const state = selectedDocuments(directory);
  const plans=state.all.map(item=>{
    const recorded = ownedFiles.filter(record => resolve(record.path) === resolve(item.path));
    if (recorded.length > 1) throw new Error('Ambiguous plugin ownership receipt');
    const recovery = recorded.length === 1 ? {plugin, repaired:false} : null;
    const text = item.bytes === null ? null : removeOwned(item.bytes.toString('utf8'), recorded[0]?.owned ?? owned[item.kind], recovery);
    return {...item, owned:[], text, repaired:recovery?.repaired ?? false};
  });
  const selected=state.selected.map(item=>plans.find(plan=>plan.path===item.path));
  if (plans.some(item => (document(item.text ?? '{}').plugin ?? []).some(entry => spec(entry) === plugin))) {
    throw new PluginRegistrationConflict('The OpenAbstractions plugin registration is already owned by the person or different settings');
  }
  const nextSeed=ownProvider(plans,selected.find(item=>item.kind==='server'),plugin,seed);
  const nextMCP=ownMCP(plans,selected.find(item=>item.kind==='server'),mcpLauncher,mcpSeed);
  mkdirSync(stage, {recursive: false});
  const config = join(stage, 'config', 'opencode'); mkdirSync(config, {recursive: true});
  for (const item of selected) {
    const name = item.kind === 'server' ? (item.path.endsWith('.jsonc') ? 'opencode.jsonc' : 'opencode.json')
      : (item.path.endsWith('.jsonc') ? 'tui.jsonc' : 'tui.json');
    item.stagedPath = join(config, name);
    // Upstream may normalize plugin specifiers in its output. Only the
    // private document receives upstream writes; the original is edited below.
    writeFileSync(item.stagedPath, '{"plugin":[]}');
  }
  const home = join(stage, 'home'); mkdirSync(home);
  const temp=join(stage,'tmp');mkdirSync(temp);
  for (const name of ['user.npmrc','global.npmrc']) writeFileSync(join(stage,name),'');
  writeFileSync(join(stage,'empty-models.json'),'{}');
  seedBootstrapStage(bootstrap(),stage);
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => /^(SystemRoot|WINDIR|COMSPEC|PATHEXT|PROCESSOR_ARCHITECTURE)$/i.test(key)));
  Object.assign(env, {HOME:home, USERPROFILE:home, APPDATA:join(home,'AppData','Roaming'), LOCALAPPDATA:join(home,'AppData','Local'),
    XDG_CONFIG_HOME:join(stage,'config'), XDG_CACHE_HOME:join(stage,'cache'), XDG_DATA_HOME:join(stage,'data'), XDG_STATE_HOME:join(stage,'state'),
    OPENCODE_CONFIG_DIR:config, OPENCODE_DISABLE_PROJECT_CONFIG:'true', OPENCODE_DISABLE_MODELS_FETCH:'true', OPENCODE_PURE:'true',
    OPENCODE_DISABLE_DEFAULT_PLUGINS:'true', OPENCODE_MODELS_PATH:join(stage,'empty-models.json'), TEMP:temp,TMP:temp,
    npm_config_userconfig:join(stage,'user.npmrc'),npm_config_globalconfig:join(stage,'global.npmrc'),npm_config_cache:join(stage,'npm-cache'),
    npm_config_offline:'true', npm_config_ignore_scripts:'true', npm_config_audit:'false', npm_config_fund:'false',npm_config_update_notifier:'false'});
  // BUN_BE_BUN and all inherited runtime/preload options are absent here.
  const result = run(cli, ['plugin', plugin, '--global'], {cwd:stage, env, encoding:'utf8', timeout:60000, windowsHide:true});
  if (result.error || result.status !== 0) {
    const clean=value=>String(value??'').replace(/\u001b\[[0-9;?]*[ -/]*[@-~]/g,'').trim();
    const detail=clean(result.error?.message)||clean(result.stderr)||clean(result.stdout)||`exit ${result.status}`;
    throw new Error(`Official plugin command failed: ${detail.slice(0,2000)}`);
  }
  for (const item of selected) {
    const {owned} = installedDelta('{}', readFileSync(item.stagedPath,'utf8'), [], plugin);
    Object.assign(item, appendOwned(item.text ?? '{}', owned));
  }
  return {plans, guards: state.all, seed:nextSeed, mcpSeed:nextMCP, command:{program:cli, arguments:['plugin',plugin,'--global'], status:result.status, stdout:result.stdout, stderr:result.stderr}};
}

export function removalPlans(directory, ownedFiles, seed = null, mcpSeed = null) {
  const state = selectedDocuments(directory);
  const plans=state.all.map(item=>({...item,owned:[],text:item.bytes===null?null:removeOwned(item.bytes.toString('utf8'),
    ownedFiles.find(record=>resolve(record.path)===resolve(item.path))?.owned??[])}));
  const mcpTarget=checkedMCPSeed(plans,mcpSeed);
  if (mcpTarget) mcpTarget.text=applyEdits(mcpTarget.text,modify(mcpTarget.text,['mcp','OpenAbstractions'],undefined,format));
  if (seed) {
    const item=plans.find(item=>resolve(item.path)===resolve(seed.path));
    if (!item || !same(document(item.text??'{}').provider?.openabstractions,seed.value)) throw new Error('Installer-seeded provider was edited');
    refuseSeedDependents(plans, seed);
    item.text=applyEdits(item.text,modify(item.text,['provider','openabstractions'],undefined,format));
  }
  return {guards: state.all, plans, seed:null};
}

// The native installer host supplies a fixed data request; no arbitrary script
// or executable path is exposed by the installed product interface.
export function rollbackTransaction(transaction, replace = replaceConfig) {
  const plans = transaction.plans.map(item => {
    const current = snapshot(item.path);
    const before = item.before === null ? null : Buffer.from(item.before,'base64');
    const after = item.after === null ? null : Buffer.from(item.after,'utf8');
    for (const bytes of [current.bytes,before,after]) if (bytes !== null) validateMCPProperties(bytes);
    if (typeof item.identity !== 'string' || current.identity !== item.identity) throw new Error('Config identity changed before rollback');
    if (current.sha256 !== digest(before) && current.sha256 !== digest(after)) throw new Error('Config changed before rollback');
    return {...current,kind:item.kind,text:before === null ? null : before.toString('utf8')};
  });
  applyPair(plans, [], replace);
}

// Read-only receiving boundary for an installed, committed registration.
// Callers admit the expected installed plugin path and source separately.
function installedRegistration({directory, plugin, documents, ownership, pending = false}, allowForeignEdits = false) {
  if (pending || !ownership || !['install','repair','upgrade'].includes(ownership.operation)
    || resolve(ownership.directory) !== resolve(directory) || !ownership.seed
    || !Array.isArray(ownership.files) || !Array.isArray(documents) || documents.length !== 5
    || !isAbsolute(plugin) || resolve(plugin) !== plugin || !isAbsolute(directory)) throw new Error('Committed installer registration required');
  const candidates = {server:['opencode.jsonc','opencode.json','config.json'],tui:['tui.jsonc','tui.json']};
  const allowed = new Map(Object.entries(candidates).flatMap(([kind,names])=>names.map(name=>[resolve(join(directory,name)),kind])));
  const records = new Map();
  for (const item of ownership.files) {
    const path=resolve(item.path);
    if (!allowed.has(path) || records.has(path) || allowed.get(path)!==item.kind || !Array.isArray(item.owned)) throw new Error('Invalid ownership receipt');
    records.set(path,item);
  }
  if (records.size!==5) throw new Error('Incomplete ownership receipt');
  const seen=new Set(), result=[], registrations=[];let provider=null, exact=true;
  const module=pathToFileURL(join(plugin,'index.js')).href;
  for (const item of documents) {
    const path=resolve(item.path),record=records.get(path);
    if (!record || seen.has(path) || item.kind!==record.kind) throw new Error('Installed config changed');
    if (digest(item.bytes)!==record.after || item.sha256!==record.after) {
      if (!allowForeignEdits || typeof record.after!=='string' || !/^[a-f0-9]{64}$/.test(record.after)
        || item.bytes===null || !Buffer.from(item.bytes.toString('utf8')).equals(item.bytes))
        throw new Error('Installed config changed');
      exact=false;
    }
    seen.add(path);
    const value=document(item.bytes===null?'{}':item.bytes.toString('utf8'));
    const entries=value.plugin??[];
    for (const entry of record.owned) {
      if (spec(entry)!==plugin || entries.filter(candidate=>same(candidate,entry)).length!==1
        || entries.filter(candidate=>spec(candidate)===plugin).length!==1) throw new Error('Installed plugin registration changed');
    }
    if (entries.some(entry=>spec(entry)===plugin) && record.owned.length!==1) throw new Error('Unowned installed plugin registration');
    if (record.owned.length) registrations.push({kind:item.kind,path,module:plugin,sha256:digest(Buffer.from(JSON.stringify(record.owned)))});
    const block=value.provider?.openabstractions;
    if (item.kind==='server' && block!==undefined) {
      if (provider || path!==resolve(ownership.seed.path) || !same(block,ownership.seed.value)
        || block.npm!==module || block.options?.scope!=='local'
        || !same(block.options?.guarantees,[guaranteeFor('local')])) throw new Error('Installed OA provider route changed');
      provider={path,npm:module,scope:'local',guarantees:[guaranteeFor('local')],sha256:digest(Buffer.from(JSON.stringify(block)))};
    }
    result.push({kind:item.kind,path,sha256:item.sha256,bytes:item.bytes===null?null:item.bytes.length});
  }
  if (!provider || !['server','tui'].every(kind=>registrations.some(row=>row.kind===kind))) throw new Error('Complete installed OA registration required');
  return {exact, observation:{schema:1,directory:resolve(directory),plugin,documents:result,
    owned_plugins:registrations,configured_provider:provider}};
}

export function observeInstalled(request) {
  return installedRegistration(request).observation;
}

function committedConfigPlan(request, previous, {all:documents, selected}) {
  if(previous?.mcpSeed!=null||!same(previous?.seed?.value,installerProvider(request.plugin)))return null;
  try {
    const {exact}=installedRegistration({...request,documents,ownership:previous},true);
    const plans=documents.map(item=>({...item,text:item.bytes===null?null:item.bytes.toString('utf8'),
      owned:previous.files.find(row=>resolve(row.path)===resolve(item.path)).owned}));
    if(!exact) {
      // The CLI would select these same documents and append its owned
      // entry last. Preserve the existing documents only if that order,
      // the prior candidate roster, and the complete owned projection agree.
      const registered=plans.filter(item=>item.owned.length);
      if(registered.length!==selected.length||!selected.every(item=>{
        const plan=plans[documents.indexOf(item)];
        return plan?.owned.length===1&&same(document(plan.text).plugin?.at(-1),plan.owned[0]);
      }))return null;
      // Validate the provider containers through the existing owner without
      // editing the actual plan or changing the person's bytes.
      ownProvider(plans.map(item=>({...item})),selected[0],request.plugin,previous.seed);
    }
    ownMCP(plans,null,request.mcpLauncher,null);
    return {exact,plans,guards:documents,seed:previous.seed,mcpSeed:null,command:{skipped:'configuration_current'}};
  } catch {
    return null;
  }
}

export function main(request, dependencies = {}) {
  if (request.operation === 'observe') {
    const read=dependencies.read??(path=>snapshot(path).bytes.toString('utf8')), exists=dependencies.exists??existsSync;
    if (exists(join(request.state,'pending.json'))) throw new Error('Committed installer registration required');
    const ownership=JSON.parse(read(join(request.state,'owned.json'),'utf8'));
    const documents=(dependencies.documents??selectedDocuments)(request.directory).all;
    return observeInstalled({...request,documents,ownership,pending:false});
  }
  if (!['install','repair','upgrade','remove','rollback'].includes(request.operation)) throw new Error('Unknown installer operation');
  const receiptPath = join(request.state,'owned.json');
  const transactionPath = join(request.state,'pending.json');
  const records=(dependencies.bootstrap??(()=>readBootstrapPayload(undefined,process.env.OA_BOOTSTRAP_INVENTORY_SHA256)))();
  mkdirSync(request.state,{recursive:true});
  if (request.operation === 'rollback') {
    if (!existsSync(transactionPath)) return {operation:'rollback',unchanged:true};
    const transaction=JSON.parse(readFileSync(transactionPath,'utf8'));
    if (resolve(transaction.directory)!==resolve(request.directory)) throw new Error('Rollback config directory changed');
    const old=transaction.previousReceipt===null?null:JSON.parse(transaction.previousReceipt);
    if(old&&resolve(old.directory)!==resolve(request.directory))throw new Error('Rollback config directory changed');
    if(transaction.configUnchanged===true) {
      if(!Array.isArray(transaction.plans)||transaction.plans.length!==0)throw new Error('Unexpected config rollback plans');
    } else {
      const allowed = new Set(selectedDocuments(request.directory).all.map(item=>resolve(item.path)));
      if (!transaction.plans.every(item=>allowed.has(resolve(item.path)))) throw new Error('Unexpected rollback target');
    }
    const restore=rollbackBootstrapPlans(transaction.bootstrap,records,request.directory,request.cache,old?.bootstrap);
    if(transaction.configUnchanged!==true)rollbackTransaction(transaction);
    restoreBootstrap(restore);
    if (transaction.previousReceipt === null) {if (existsSync(receiptPath)) unlinkSync(receiptPath);}
    else {
      if(old.bootstrap)old.bootstrap.files=old.bootstrap.files.map(item=>{
        const current=leafSnapshot(bootstrapPath(item.name,request.directory,request.cache));
        if(current.sha256!==item.sha256)throw new Error('Restored bootstrap leaf changed');
        return {...item,identity:current.identity,leaf:current.leaf};
      });
      writeFileSync(receiptPath,JSON.stringify(old),{mode:0o600});
    }
    unlinkSync(transactionPath);
    return {operation:'rollback',restored:true};
  }
  if (existsSync(transactionPath)) throw new Error('An installer transaction needs rollback before retry');
  const previousReceipt=existsSync(receiptPath)?readFileSync(receiptPath,'utf8'):null;
  const previous=previousReceipt===null?null:JSON.parse(previousReceipt);
  if (previous && resolve(previous.directory)!==resolve(request.directory)) throw new Error('Installed config directory changed');
  const ownedFiles=previous?.files??[];
  const documents=selectedDocuments(request.directory);
  const allowed=new Set(documents.all.map(item=>resolve(item.path)));
  if (!ownedFiles.every(item=>allowed.has(resolve(item.path)) && Array.isArray(item.owned) && item.owned.every(entry=>spec(entry)))) throw new Error('Invalid ownership receipt');
  const leaves=bootstrapPlans(records,request.directory,request.cache,previous?.bootstrap,request.operation);
  const pluginPin=process.env.OA_PLUGIN_INVENTORY_SHA256;
  const verifyPlugin=dependencies.verifyPlugin??(pluginPin===undefined?null:plugin=>verifyInstalledPlugin(plugin,pluginPin));
  if(request.operation!=='remove'&&verifyPlugin)verifyPlugin(request.plugin);
  const committed=request.operation==='remove'||!verifyPlugin?null:committedConfigPlan(request,previous,documents);
  if(committed?.exact&&committedBootstrap(leaves,request.cache,previous.bootstrap)) {
    return {operation:request.operation,directory:request.directory,unchanged:true};
  }
  const plan = request.operation === 'remove' ? removalPlans(request.directory, ownedFiles, previous?.seed, previous?.mcpSeed)
    : committed??stageInstall({...request,ownedFiles,seed:previous?.seed,mcpSeed:previous?.mcpSeed},dependencies.run,dependencies.verifyCLI,()=>records);
  const configWrites=committed?[]:plan.plans.map(item=>({kind:item.kind,path:item.path,identity:item.identity,
    before:item.bytes===null?null:item.bytes.toString('base64'),after:item.text}));
  const transaction={directory:request.directory,previousReceipt,plans:configWrites,bootstrap:bootstrapTransaction(leaves,request.cache)};
  if(committed)transaction.configUnchanged=true;
  writeFileSync(transactionPath,JSON.stringify(transaction),{flag:'wx',mode:0o600});
  try {applyBootstrap(leaves,transaction.bootstrap);}
  finally {writeFileSync(transactionPath,JSON.stringify(transaction),{mode:0o600});}
  if(committed)checkConfigGuards(plan.guards);
  else applyPair(plan.plans, plan.guards);
  const result={operation:request.operation,directory:request.directory,command:plan.command,seed:plan.seed,
    files:plan.plans.map(item => ({kind:item.kind,path:item.path,before:item.sha256,after:digest(item.text === null ? null : Buffer.from(item.text)),owned:item.owned ?? []}))};
  if(plan.mcpSeed)result.mcpSeed=plan.mcpSeed;
  if(records.length||previous?.bootstrap)result.bootstrap=bootstrapOwnership(leaves,request.cache);
  writeFileSync(receiptPath,JSON.stringify(result,null,2),{mode:0o600});
  if (plan.plans.some(item => item.repaired)) {
    process.stderr.write('OpenAbstractions restored edited installer-owned settings and retained the prior settings in its backup.\n');
  }
  return result;
}

export function runConfigureRequest(request, work, dependencies) {
  if (resolve(request.work)!==work || resolve(request.stage)!==join(work,'stage')) throw new Error('Invalid fixed helper workspace');
  try {
    const result = main(request, dependencies);
    writeFileSync(join(work, 'result.json'), JSON.stringify(result,null,2), {flag:'wx'});
    return 0;
  } catch (error) {
    writeFileSync(join(work,'result.json'),JSON.stringify({error:error.message}),{flag:'wx'});
    return error instanceof PluginRegistrationConflict ? pluginRegistrationConflictExit : 1;
  } finally {
    rmSync(request.stage,{recursive:true,force:true});
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  if (process.argv.length === 4 && process.argv[2] === '--observe-request') {
    try {
      const request=JSON.parse(readFileSync(process.argv[3],'utf8'));
      if (request.operation!=='observe') throw new Error('Read-only request required');
      process.stdout.write(JSON.stringify(main(request))+'\n');
    } catch { process.stderr.write('Installed config observation refused\n');process.exitCode=1; }
  } else {
    if (process.argv.length !== 4 || process.argv[2] !== '--request') throw new Error('The fixed installer helper requires one request file');
    const request = JSON.parse(readFileSync(process.argv[3],'utf8'));
    process.exitCode = runConfigureRequest(request, dirname(resolve(process.argv[3])));
  }
}
