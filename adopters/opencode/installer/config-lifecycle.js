// Fixed app-installer helper. The native installer host runs it with the
// verified official OpenCode Desktop program as Node (ELECTRON_RUN_AS_NODE).
// It performs no OA requests and installs no permissions.
import {parse, parseTree, findNodeAtLocation, createScanner, SyntaxKind, modify, applyEdits} from 'jsonc-parser';
import {readFileSync, existsSync, mkdirSync, writeFileSync, unlinkSync} from 'node:fs';
import {join, dirname, resolve, basename, isAbsolute} from 'node:path';
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
// Applying the owned registration uses the same editor without reformatting
// unrelated source bytes.
const format = {};

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

function bootstrapUnchanged(item) {
  const current=leafSnapshot(item.path,item.leaf);
  if(current.identity!==item.identity||current.sha256!==item.sha256)throw new Error('Bootstrap leaf changed while installer ran');
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

// OpenCode reads a plugin registration as the plugin folder's absolute path
// in the "plugin" list of the server and TUI documents. The installer appends
// that one entry to each selected document and records it as owned.
export function stageInstall({directory, plugin, mcpLauncher, mcpSeed = null, owned = {server: [], tui: []}, ownedFiles = [], seed = null}) {
  if (typeof plugin !== 'string' || !isAbsolute(plugin)) throw new Error('The plugin registration must be an absolute folder');
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
  for (const item of selected) Object.assign(item, appendOwned(item.text ?? '{}', [plugin]));
  return {plans, guards: state.all, seed:nextSeed, mcpSeed:nextMCP};
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
    return {exact,plans,guards:documents,seed:previous.seed,mcpSeed:null};
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
    // Only a transaction an earlier package version left can hold bootstrap
    // leaves; this version writes none.
    const restore=rollbackBootstrapPlans(transaction.bootstrap,[],request.directory,request.cache,old?.bootstrap);
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
  const pluginPin=process.env.OA_PLUGIN_INVENTORY_SHA256;
  const verifyPlugin=dependencies.verifyPlugin??(pluginPin===undefined?null:plugin=>verifyInstalledPlugin(plugin,pluginPin));
  if(request.operation!=='remove'&&verifyPlugin)verifyPlugin(request.plugin);
  const committed=request.operation==='remove'||!verifyPlugin?null:committedConfigPlan(request,previous,documents);
  // Earlier package versions pre-seeded OpenCode's own first-use install
  // (previous.bootstrap). Those files stay where they are as OpenCode's, and
  // this receipt no longer lists them.
  if(committed?.exact&&!previous.bootstrap) {
    return {operation:request.operation,directory:request.directory,unchanged:true};
  }
  const plan = request.operation === 'remove' ? removalPlans(request.directory, ownedFiles, previous?.seed, previous?.mcpSeed)
    : committed??stageInstall({...request,ownedFiles,seed:previous?.seed,mcpSeed:previous?.mcpSeed});
  const configWrites=committed?[]:plan.plans.map(item=>({kind:item.kind,path:item.path,identity:item.identity,
    before:item.bytes===null?null:item.bytes.toString('base64'),after:item.text}));
  const transaction={directory:request.directory,previousReceipt,plans:configWrites,bootstrap:null};
  if(committed)transaction.configUnchanged=true;
  writeFileSync(transactionPath,JSON.stringify(transaction),{flag:'wx',mode:0o600});
  if(committed)checkConfigGuards(plan.guards);
  else applyPair(plan.plans, plan.guards);
  const result={operation:request.operation,directory:request.directory,seed:plan.seed,
    files:plan.plans.map(item => ({kind:item.kind,path:item.path,before:item.sha256,after:digest(item.text === null ? null : Buffer.from(item.text)),owned:item.owned ?? []}))};
  if(plan.mcpSeed)result.mcpSeed=plan.mcpSeed;
  writeFileSync(receiptPath,JSON.stringify(result,null,2),{mode:0o600});
  if (plan.plans.some(item => item.repaired)) {
    process.stderr.write('OpenAbstractions restored edited installer-owned settings and retained the prior settings in its backup.\n');
  }
  return result;
}

export function runConfigureRequest(request, work, dependencies) {
  if (resolve(request.work)!==work) throw new Error('Invalid fixed helper workspace');
  try {
    const result = main(request, dependencies);
    writeFileSync(join(work, 'result.json'), JSON.stringify(result,null,2), {flag:'wx'});
    return 0;
  } catch (error) {
    writeFileSync(join(work,'result.json'),JSON.stringify({error:error.message}),{flag:'wx'});
    return error instanceof PluginRegistrationConflict ? pluginRegistrationConflictExit : 1;
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
