// Run with node monitor/connect_page_test.js; no browser or service is started.
// Connect a service (P-M9): preset rows read today's spend and the budget in
// the unit the preset's replies carry (tokens for DeepSeek, money for
// OpenRouter), a budget is chosen and never typed, Connect sends the key once
// and clears it, and the gateway's address is shown as text.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/connect_page.go', 'utf8');
const script = go.split('<script>')[1].split('</script>')[0];
const shellGo = fs.readFileSync(__dirname + '/panel_shell.go', 'utf8');
const plainMarker = 'const plainScript = `';
const plainStart = shellGo.indexOf(plainMarker) + plainMarker.length;
const plainScript = shellGo.slice(plainStart, shellGo.indexOf('`', plainStart)).split('<script>')[1].split('</script>')[0];
const presets = JSON.parse(fs.readFileSync(__dirname + '/../openabstractions-flat/abstraction-router/go/hostedcatalogue/presets.json', 'utf8'));

const fields = new Map();
function node() {
 return {value:'', textContent:'', hidden:false, title:'', children:[], attributes:{}, setAttribute(k,v){this.attributes[k]=v}, replaceChildren(...v){this.children=[...v]}, append(...v){this.children.push(...v)}};
}
function element(id) {
 if (!fields.has(id)) fields.set(id, node());
 return fields.get(id);
}
function walk(n, out) { out.push(n); for (const c of n.children || []) if (c && typeof c === 'object') walk(c, out); return out; }
function find(root, pred) { return walk(root, []).find(pred); }

const secret = 'sk-never-shown-in-the-page-5c2e';
let hosts = [{Entry:{Name:'deepseek',Hosted:true,Kind:'openai-compatible',Base:'https://api.deepseek.com/v1',Credential:'deepseek',Ceiling:{TokensPerDay:1000000},DeclaredBy:'operator'},Up:true,Spend:{Day:'2026-10-19',Tokens:12000,Micros:0}}];
// ceiling is what the operator keeps for a budget edit, in its preset's unit.
function ceiling(name, budget) {
 if (!budget) return null;
 const p = presets.presets.find(p => p.credential === name);
 return p && p.budget === 'tokens' ? {TokensPerDay:budget} : {MicrosPerDay:budget};
}
const posts = [];
let credentialReads=0;
let hostFailure='',gatewayFailure='',presetFailure=false,gatewayListening=true,connectFailure='';
const context = {URLSearchParams, location:{search:'?k=test'}, console, Promise,
 document:{getElementById:element, createElement:t=>Object.assign(node(),{tagName:String(t).toUpperCase()})},
 fetch: async (path, opts) => {
  if (path === '/connect' && !(opts && opts.body)) {if(presetFailure)throw Error('preset source unavailable');return {ok:true, json:async()=>presets};}
  if (path === '/connect') {
   if(connectFailure==='transport')throw Error('connection service unavailable');
   if(connectFailure)return {ok:true,json:async()=>({outcome:connectFailure,part:'credential'})};
   const body = JSON.parse(opts.body);
   posts.push(body);
   if (body.edit === 'connect') {
    if(body.preset!=='github-download') hosts = hosts.concat([{Entry:{Name:body.preset,Hosted:true,Kind:'openai-compatible',Base:'https://openrouter.ai/api/v1',Credential:body.preset,Ceiling:ceiling(body.preset, body.budget),DeclaredBy:'operator'},Up:true}]);
    return {ok:true, json:async()=>({outcome:'applied', name:body.preset})};
   }
   hosts = hosts.map(h => h.Entry.Name === body.name ? {...h, Entry:{...h.Entry, Ceiling:ceiling(body.name, body.budget)}} : h);
   return {ok:true, json:async()=>({outcome:'applied', name:body.name})};
  }
  if (path === '/inference/hosts') {if(hostFailure==='transport')throw Error('host source unavailable');return {ok:true, json:async()=>({Outcome:hostFailure||'page', Revision:'r1', Hosts:hosts})};}
  if (path === '/inference/gateway') {if(gatewayFailure==='transport')throw Error('gateway source unavailable');return {ok:true, json:async()=>({Outcome:gatewayFailure||'page', Open:true, Listening:gatewayListening, ListeningAddress:'127.0.0.1:8793'})};}
  throw Error('unexpected fetch ' + path);
 }};
context.window = context;
context.credentialsChanged=async()=>{credentialReads++};
vm.runInNewContext(plainScript, context);
vm.runInNewContext(script, context);

(async () => {
 await context.connectReady;
 // The presets are data: every row comes from hosted_presets.json.
 const rows = element('connect-rows').children;
 assert.deepEqual(rows.map(r => r.parts.label.textContent), ['DeepSeek', 'OpenRouter', 'GitHub private downloads', 'Another OpenAI-compatible service']);
 // DeepSeek's replies carry tokens and no cost, so its row reads tokens.
 assert.equal(rows[0].parts.caption.textContent, 'Connected · 12,000 tokens today, budget 1,000,000 tokens a day', 'a confirmed connection reads today\'s spend and the budget');
 assert.equal(rows[1].parts.caption.textContent, 'Not connected');
 assert.equal(presets.presets.find(p=>p.id==='deepseek').reports_cost,false);
 assert.equal(context.connectSpendLine('tokens',{Entry:{},Spend:{Tokens:12,Micros:999999}}),'12 tokens today, no daily token limit','tokens are never converted to money');
 assert.equal(context.connectSpendLine('money',{Entry:{},Spend:{Micros:123456}}),'0.12 today, no daily spending limit');
 const budget = rows[0].parts.control.children[0];
 assert.equal(budget.tagName, 'SELECT', 'the budget is a chooser, never a typed field');
 assert.deepEqual(budget.children.map(o => o.textContent), ['No daily token limit', '100,000 tokens a day', '1,000,000 tokens a day', '5,000,000 tokens a day', '20,000,000 tokens a day']);
 assert.equal(element('connect-gateway').textContent, 'http://127.0.0.1:8793/v1', 'the gateway address is shown as text');
 assert.equal(element('connect-gateway-status').textContent, 'Give an app this address as its base URL and any text as its key.');

 // Choosing a budget replaces it and the row reads it back.
 budget.value = '5000000';
 await budget.onchange();
 assert.deepEqual(posts.at(-1), {edit:'budget', name:'deepseek', budget:5000000});
 assert.equal(rows[0].parts.caption.textContent, 'Connected · 12,000 tokens today, budget 5,000,000 tokens a day');
 assert.equal(element(rows[0].parts.status.id).textContent, 'DeepSeek may use up to 5,000,000 tokens a day.', 'the change writes one sentence');

 // Connect… opens the key and the budget (OpenRouter reports its cost, so
 // 2.00 by default); Connect sends the key once and clears the field.
 const open = rows[1].parts.control.children[0];
 assert.equal(open.textContent, 'Connect…');
 open.onclick();
 const form = rows[1].form;
 const key = find(form, n => n.type === 'password');
 const chosen = find(form, n => n.tagName === 'SELECT');
 assert.equal(chosen.children[0].textContent,'No daily spending limit');assert(find(form,n=>n.textContent==='Daily spending limit (optional)'));
 assert.equal(chosen.value, '0', 'an optional limit starts with no limit');assert(find(form,n=>n.textContent==='API key (required)'));assert(find(form,n=>n.textContent==='Key name (optional)'));open.onclick();assert.equal(form.hidden,false,'repeated Connect keeps the dialog open');chosen.value='2000000';
 assert.equal(find(form, n => n.type === 'url'), undefined, 'a preset with an address asks for none');
 key.value=secret;find(form,n=>n.textContent==='Show key').onclick();assert.equal(key.type,'text');
 const beforeCancel=posts.length;
 find(form,n=>n.textContent==='Cancel').onclick();
 assert.equal(key.value,'');assert.equal(key.type,'password');assert(form.hidden);assert.equal(posts.length,beforeCancel,'cancel has no effects');
 open.onclick();
 key.value = secret;
 await find(form, n => n.textContent === 'Connect').onclick();
 assert.deepEqual(posts.at(-1), {edit:'connect', preset:'openrouter', base:'', secret, credential_name:'', budget:2000000});
 assert.equal(key.value, '', 'the key field is cleared once sent');
 assert.equal(credentialReads,1,'preset entry immediately refreshes Saved credentials');
 const after = element('connect-rows').children;
 assert.equal(after[1].parts.caption.textContent, 'Connected · 0.00 today, budget 2.00 a day');
 assert.equal(element(after[1].parts.status.id).textContent,'Saved the key and configured OpenRouter. Each app still needs your permission to use it.','saving reports only the changes it made');
 hosts=hosts.map(h=>h.Entry.Name==='openrouter'?{...h,Up:false,Why:'credential:not_found'}:h);
 await context.connectChanged();
 assert.equal(element('connect-rows').children[1].parts.caption.textContent,'Configured; connection not confirmed · 0.00 today, budget 2.00 a day','registration alone never claims a successful connection');
 assert.equal(element('connect-rows').children[1].parts.control.children[0].tagName,'SELECT','configured budgets remain editable without a confirmed connection');
 for (const n of walk(element('connect-rows'), [])) assert(!String(n.textContent).includes(secret), 'the key appears in the page');

 // Source tokens never offer a model budget or gateway readiness claim.
 const source = after[2];
 assert.equal(source.parts.control.children[0].textContent, 'Save credential…');
 source.parts.control.children[0].onclick();
 assert.equal(find(source.form,n=>n.tagName==='SELECT'), undefined);
 const sourceKey=find(source.form,n=>n.type==='password'); sourceKey.value=secret;
 await find(source.form,n=>n.textContent==='Save credential').onclick();
 assert.deepEqual(posts.at(-1),{edit:'connect',preset:'github-download',base:'',secret,credential_name:'',budget:0});
 assert.equal(sourceKey.value,'');
 assert(element(source.parts.status.id).textContent.includes('permission is checked separately'));
 assert(!element(source.parts.status.id).textContent.includes('any key'));

 // The generic row asks for the address as well.
 after.at(-1).parts.control.children[0].onclick();
 assert(find(after.at(-1).form, n => n.type === 'url'), 'the generic row asks for an address');
 const generic=after.at(-1),genericSecret=find(generic.form,n=>n.type==='password');genericSecret.value=secret;
 const sourceSentence=element(source.parts.status.id).textContent;
 await context.connectChanged();
 assert.strictEqual(element('connect-rows').children.at(-1),generic,'healthy refresh retains the open form and row');
 assert.equal(genericSecret.value,secret,'healthy refresh preserves the person\'s unfinished input');
 const beforeInvalidName=posts.length;generic.form.parts.base.value='https://api.example.com/v1';generic.form.parts.name.value='invalid name';
 await find(generic.form,n=>n.textContent==='Connect').onclick();assert.equal(posts.length,beforeInvalidName,'invalid optional name dispatches nothing');assert.equal(genericSecret.value,'');assert(element(generic.form.parts.status.id).textContent.includes('Key name must'),'validation is visible in the modal');
 generic.form.parts.name.value='';genericSecret.value=secret;
 for(const failure of ['conflict','invalid','transport']){connectFailure=failure;genericSecret.value=secret;await find(generic.form,n=>n.textContent==='Connect').onclick();assert.equal(generic.form.hidden,false);assert.equal(genericSecret.value,'');assert(element(generic.form.parts.status.id).textContent,'service failure is visible in the modal')};connectFailure='';
 assert.equal(element(source.parts.status.id).textContent,sourceSentence,'refresh preserves the last action sentence');
 for(const failure of ['transport','forbidden','unavailable']){
  hostFailure=failure;await context.connectChanged();
  assert.equal(element('connect-rows').children.length,0,'an unreadable host source is not a disconnected snapshot');
  assert(element('connect-status').textContent,'unreadable services have a visible status');
  assert.equal(genericSecret.value,'','failed source clears a detached secret field');
  hostFailure='';await context.connectChanged();
  assert.equal(element('connect-rows').children[0].parts.caption.textContent,'Connected · 12,000 tokens today, budget 5,000,000 tokens a day');
  assert.equal(element('connect-status').textContent,'','recovery clears the read error');
 }
 presetFailure=true;await context.connectChanged();assert.equal(element('connect-rows').children.length,0);
 presetFailure=false;await context.connectChanged();assert(element('connect-rows').children.length);
 for(const failure of ['transport','forbidden','unavailable']){
  gatewayFailure=failure;await context.connectChanged();
  assert.equal(element('connect-gateway').textContent,'','failed status withdraws the old address');
  assert.equal(element('connect-gateway').hidden,true);assert(!element('connect-gateway-status').textContent.includes('gateway is off'),'unreadable does not mean off');
  gatewayFailure='';await context.connectChanged();assert.equal(element('connect-gateway').textContent,'http://127.0.0.1:8793/v1');
 }
 gatewayListening=false;await context.connectChanged();assert(element('connect-gateway-status').textContent.includes('gateway is off'),'a successful off observation remains distinct');
 for(const hostName of ['example.com','deepseek']){
  hosts=[{Entry:{Name:hostName,Hosted:true,DeclaredBy:'operator',Kind:'openai-compatible',Base:hostName==='deepseek'?'https://api.deepseek.com/v1':'https://example.com/v1',Credential:'openrouter',Ceiling:{TokensPerDay:123}}}];
  await context.connectChanged();const classified=element('connect-rows').children;
  assert(!classified.find(r=>r.item.preset.id==='openrouter').item.host,'arbitrary key names cannot select a provider');
  assert.equal(!!classified.find(r=>r.item.preset.id==='deepseek').item.host,hostName==='deepseek');
  if(hostName==='example.com')assert(classified.some(r=>r.item.label==='example.com'),'a generic host remains visible with a cross-preset key name');
 }
 hosts=[];await context.connectChanged();
 const tokenRow=element('connect-rows').children[0];tokenRow.parts.control.children[0].onclick();
 assert(find(tokenRow.form,n=>n.textContent==='Daily token limit (optional)'));
 assert(find(tokenRow.form,n=>n.textContent==='No daily token limit is applied unless you choose one. Token usage is shown; monetary cost is not reported.'));
 assert.equal(tokenRow.form.parts.budget.value,'0','token limits remain optional');
 assert.equal(tokenRow.form.parts.budget.children[0].textContent,'No daily token limit');
 console.log('connect page: ok');
})().catch(e => { console.error(e); process.exit(1); });
