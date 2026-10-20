// Run by monitor/rights_page_handler_test.go as
//   node testdata/rights_page_handler.js <panel URL> <panel key>
// It runs the Questions and permissions page's own script (rights_page.go,
// with plainScript and serviceCommonScript spliced in the way the Panel
// serves them) in a fake DOM whose fetch reaches the real Panel handler at
// <panel URL>, then prints what the page shows as one JSON object. The Go
// test makes the assertions, so the page is checked against the handler's
// real replies instead of a hand-written fixture.
const fs = require('fs'), vm = require('vm'), path = require('path');

const monitor = path.join(__dirname, '..');
function constant(file, name) {
 const src = fs.readFileSync(path.join(monitor, file), 'utf8');
 const marker = 'const ' + name + ' = `';
 const start = src.indexOf(marker);
 if (start < 0) throw Error(name + ' not found in ' + file);
 return src.slice(start + marker.length, src.indexOf('`', start + marker.length));
}
function plainScript() {
 return constant('panel_shell.go', 'plainScript').split('<script>')[1].split('</script>')[0];
}
function rightsScript() {
 const go = fs.readFileSync(path.join(monitor, 'rights_page.go'), 'utf8');
 const script = go.split('<script>')[1].split('</script>')[0];
 const glue = '` + serviceCommonScript + `';
 if (!script.includes(glue)) throw Error('rights_page.go: serviceCommonScript glue not found');
 return script.replace(glue, constant('service_page.go', 'serviceCommonScript'));
}

function node(tag) {
 return {tag: tag || '', value: '', textContent: '', className: '', title: '', disabled: false, hidden: false, checked: false, dataset: {}, style: {}, listeners: {}, children: [],
  replaceChildren(...v) { this.children = [...v] }, append(...v) { this.children.push(...v) }, appendChild(v) { this.children.push(v); return v },
  setAttribute() {}, removeAttribute() {}, click() {}, focus() {},
  addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn) }};
}
const fields = new Map();
const element = id => { if (!fields.has(id)) fields.set(id, node('#' + id)); return fields.get(id); };

const [base, key, mode] = process.argv.slice(2);
if (!base || !key) throw Error('usage: node testdata/rights_page_handler.js <panel URL> <panel key>');

(async () => {

 const writes=[];
 const context = {URLSearchParams, console, Blob, setTimeout, clearTimeout,
  localStorage: {length: 0, key() {}, getItem() { return null }, setItem() {}, removeItem() {}},
  document: {getElementById: element, createElement: node, createTextNode: text => ({textContent: text}), querySelectorAll: () => [], body: node('body')},
  location: {search: '?k=' + encodeURIComponent(key)},
  fetch: async (p, opts) => {
   const reply=await fetch(base+p,opts);
   if(p.startsWith('/rights?')===false&&p==='/rights'&&opts?.method==='POST')writes.push({request:JSON.parse(opts.body),reply:await reply.clone().json()});
   return reply;
  }};
 context.window = context;
 vm.runInNewContext(plainScript(), context);
 vm.runInNewContext(rightsScript(), context);
 for (const ready of ['choicesReady', 'questionsReady', 'rightsDeviceHoldersReady', 'rightsSelfReady', 'rightsListReady', 'delegationReady', 'assistantReadyPromise', 'answeredReady']) {
  await context[ready];
 }
 // A listed item is a wrapper whose first child is its oa-row: the text
 // (label, then caption) and the controls (rights_page.go, rightsItem).
 const itemLine = box => { const text = box.children[0].children[0]; return text.children.map(c => c.textContent).join('\n'); };
 const itemControls = box => box.children[0].children[1].children;
 const item = box => ({lines: box.children[0].children[0].children.map(c => c.textContent), buttons: itemControls(box).filter(c => c.tag === 'button').map(c => c.textContent)});
 const report = {
  choicesStatus: element('choices-status').textContent,
  choices: element('choices-list').children.filter(box => box.children.length).map(item),
  choiceHistory: element('choices-history-list').children.filter(box => box.children.length).map(item),
  delegationStatus: element('delegation-status').textContent,
  delegationRequests: element('delegation-question').children.map(o => ({value: o.value, text: o.textContent})),
  questionsStatus: element('questions-status').textContent,
  questions: element('questions-list').children.filter(box => box.children.length).map(item),
  assistantStatus: element('assistant-status').textContent,
  assistants: element('assistant-list').children.filter(box => box.children.length).map(box => [itemLine(box), ...itemControls(box).map(c => c.textContent)]),
  assistantWarning: element('assistant-warning').children.map(c => c.tag === 'ul' ? c.children.map(li => li.textContent).join('; ') : c.textContent),
  assistantButton: element('assistant-save').textContent,
  answeredStatus: element('answered-status').textContent,
  answered: element('answered-list').children.filter(box => box.children.length).map(item),
 };
 if(mode==='defaults') {
  // Defaults for all apps: the switches on arrival, then one turned off.
  const rows=()=>element('defaults-list').children.filter(box=>box.children.length).map(box=>{const toggle=itemControls(box)[0];return {lines:box.children[0].children[0].children.map(c=>c.textContent),on:!!toggle.checked,disabled:!!toggle.disabled}});
  await context.defaultsList();
  report.defaults=rows();report.defaultsStatus=element('defaults-status').textContent;
  const download=element('defaults-list').children.find(box=>box.children.length&&box.children[0].children[0].children[0].textContent==='Download in the background');
  if(!download)throw Error('no download switch: '+JSON.stringify(report.defaults));
  const toggle=itemControls(download)[0];toggle.checked=false;await toggle.onchange();
  report.defaultsAfter=rows();report.defaultsAfterStatus=element('defaults-status').textContent;
 }
 if(mode==='typed') {
  const self=(await (await context.fetch('/explore',{headers:{'X-Panel-Key':key}})).json()).self;
  element('rights-account').value=self.account;element('rights-program').value=self.program;
  const text=n=>[n.textContent||'',...(n.children||[]).map(text)].join('\n');
  const use={Action:'abstraction.credentials/apply',ResourceProfile:'abstraction.credentials/named-use@2'};
  const read={Action:'abstraction.credentials/secret.read',ResourceProfile:'abstraction.credentials/named-secret@1'};
  if(element('rights-action').children.some(o=>o.value===read.Action))throw Error('typed-only Read offered as a legacy exact permission');
  if(!element('rights-action').children.some(o=>o.value===context.selectorActionValue(read)))throw Error('typed Read control missing');
  const choose=(capability,scope,name,choice)=>{
   context.resetRightsDraft('grant');
   element('rights-program-select').value=element('rights-program-select').children.some(o=>o.value===self.program)?self.program:'__other__';
   element('rights-program-select').onchange();element('rights-program').value=self.program;
   element('rights-account-select').value=self.account;element('rights-account-select').onchange();element('rights-account').value=self.account;
   element('rights-action').value=typeof capability==='string'?capability:context.selectorActionValue(capability);
   element('rights-action').onchange();
   element('rights-scope').value=scope;element('rights-name').value=name||'';
   element('rights-permit').value=choice;element('rights-ttl').value='300000';
   element('rights-why').value='native public page fixture';
   context.markRightsDraft('grant');context.rightsGrantPreview();
  };
  report.typedJourney=[];
  const submit=async stage=>{
   const preview=text(element('rights-preview'));
   await element('rights-grant').onsubmit({preventDefault(){}});
   report.typedJourney.push({stage,preview,outcome:writes.at(-1)?.reply.Outcome||'',writes:writes.length});
  };
  choose(use,'all','','permit');await submit('all-unconfirmed');
  element('rights-broad-confirm').checked=true;await submit('all');
  choose(use,'one','alpha','deny');await submit('one-block');
  choose(use,'one','alpha','remove');await submit('one-remove');
  choose('bundle:abstraction.credentials/full-control@1','one','alpha','permit');
  element('rights-broad-confirm').checked=true;await submit('bundle');
  choose(read,'one','alpha','deny');await submit('read-block');
  const page=await (await context.fetch('/rights?cursor=',{headers:{'X-Panel-Key':key}})).json();
  const originRow=page.Rules.find(r=>r.Selector?.Origin);
  if(!originRow)throw Error('native handler returned no retained bundle origin: '+JSON.stringify(writes.map(w=>({request:w.request,outcome:w.reply.Outcome}))));
  context.editBundleOrigin(originRow);
  element('rights-broad-confirm').checked=true;await submit('origin-remove');
 }
 process.stdout.write(JSON.stringify(report));
})().catch(e => { console.error(e && e.stack || String(e)); process.exit(1) });
