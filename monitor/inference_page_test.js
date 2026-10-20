// Run with node monitor/inference_page_test.js; no browser or service is started.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/inference_page.go', 'utf8');
// The model server, gateway and key sections moved to the Providers page
// (registry_page.go) with their own script, serversScript; this test drives
// it in its own context first, then the Models page in another (modelsPage
// at the end of this file).
const registryGo = fs.readFileSync(__dirname + '/registry_page.go', 'utf8');
const serversMarker = 'const serversScript = `';
const serversStart = registryGo.indexOf(serversMarker) + serversMarker.length;
assert(serversStart > serversMarker.length - 1, 'serversScript constant not found in registry_page.go');
const serversScript = registryGo.slice(serversStart, registryGo.indexOf('`', serversStart));
assert(!go.includes('id="hosts-section"') && !go.includes('id="gateway"') && !go.includes('id="keys-section"'), 'the Models page no longer carries the server, gateway and key sections');
// plainScript (monitor/panel_shell.go) is embedded ahead of every page
// fragment in the real document; this isolated test loads it into the same
// vm context first, since inference_page.go's own script calls oaShowError
// and oaPlain.
const shellGo = fs.readFileSync(__dirname + '/panel_shell.go', 'utf8');
const plainMarker = 'const plainScript = `';
const plainStart = shellGo.indexOf(plainMarker) + plainMarker.length;
assert(plainStart > plainMarker.length - 1, 'plainScript constant not found in panel_shell.go');
const plainEnd = shellGo.indexOf('`', plainStart);
const plainScript = shellGo.slice(plainStart, plainEnd).split('<script>')[1].split('</script>')[0];
const fields = new Map();
function node() {
 return {value:'', textContent:'', className:'', disabled:false, hidden:false, listeners:{}, children:[], replaceChildren(...v){this.children=[...v]}, append(...v){this.children.push(...v)}, click(){this.onclick&&this.onclick()}, focus(){this.focused=true}, addEventListener(type,fn){(this.listeners[type]=this.listeners[type]||[]).push(fn)}, dispatch(type){for(const fn of this.listeners[type]||[])fn()}};
}
function element(id) {
 if (!fields.has(id)) fields.set(id, node());
 return fields.get(id);
}
const calls = [];
const hostList = {Outcome:'page', Revision:'hosts-v1:a', Hosts:[
 // Micros (millionths of the account's currency) is the wire unit; the
 // page shows it as a two-decimal currency amount (task 2026-09-23, fourth
 // first-time visitor finding 2).
 {Entry:{Name:'openrouter',Hosted:true,Kind:'openai-compatible',Base:'https://openrouter.ai/api/v1',Credential:'openrouter',Ceiling:{TokensPerDay:1000,MicrosPerDay:750000},Profiles:['chat','embed'],DeclaredBy:'operator'},Up:false,Why:'credential:unknown:openrouter',Spend:{Day:'2026-09-17',Tokens:42,Micros:2500000}},
 // A local host with no credential, no spend recorded yet and no ceiling
 // set: every one of those three cells must read as a stated absence, never
 // an empty cell that looks broken (task 2026-09-23 finding 3).
 {Entry:{Name:'ollama',Hosted:false,Kind:'ollama',Base:'http://127.0.0.1:11434',Credential:'',Profiles:['chat'],DeclaredBy:'installation'},Up:true,Why:'',Spend:null},
]};
const replies = {
 '/inference/models': [
  {Offers:[
   {Family:'qwen3.6-demo',Names:['qwen3.6-demo'],Servers:['modelhost'],Context:32768,ContextSource:'default'},
   {Family:'reported',Names:['reported-alias','second-alias'],Servers:['local','cloud'],Context:8192,ContextSource:'effective'},
   {Family:'maximum',Names:['maximum'],Servers:['local'],Context:16384,ContextSource:'maximum'},
   {Family:'unknown',Names:['unknown'],Servers:['lemonade'],Context:0,ContextSource:'unknown',Unknown:['lemonade/unknown']},
  ]},
  {Offers:[{Family:'flux2-klein-4b',Names:['flux2-klein-4b'],Servers:['thenoise'],Context:0,ContextSource:'none'}]},
  {Offers:[]},
  {__fail:'router forbidden: abstraction.router/inventory.read on abstraction.router/inventory'},
  {__fail:'service resolution: unavailable: abstraction.router/router@1'},
 ],
 '/inference/hosts': [hostList, {Outcome:'applied', Revision:'hosts-v1:b', Reason:''}, hostList, {Outcome:'conflict', Revision:'hosts-v1:c', Reason:'revision'}, {Outcome:'page', Revision:'hosts-v1:d', Hosts:[]}],
 // task 2026-09-23, fourteenth first-time visitor, finding 2: keys() now
 // reads on arrival too, so the queue's own first reply is the arrival
 // read's own (empty) page, shifting every reply after it by one.
 '/inference/keys': [{Outcome:'page', Keys:[]}, {Outcome:'applied', Key:'oalk_shown_once', Record:{Program:'/usr/bin/python3'}}, {Outcome:'page', Keys:[{Program:'/usr/bin/python3',State:'active',Credential:'',IssuedUnixMs:Date.parse('2020-01-01T00:00:00Z'),IssuedBy:'C:\\oa\\openabstractions.exe'}]}, {Outcome:'forbidden'},
  // A transport failure, not a typed Outcome: the raw text a service
  // resolution failure would carry (task 2026-09-23 finding 1). oaShowError
  // must keep it off the visible line.
  { __fail: 'service resolution: unavailable: abstraction.inference/keys@1 could not be resolved at the installed runtime at \\\\.\\pipe\\openabstractions-user-S-1-5-21-1001' }],
 '/inference/gateway': [{Outcome:'page', Revision:'gateway-v1:a', Open:false, Address:'', Listening:false, ListeningAddress:'', Why:''}, {Outcome:'applied', Revision:'gateway-v1:b', Reason:''}, {Outcome:'page', Revision:'gateway-v1:b', Open:true, Address:'127.0.0.1:8793', Listening:true, ListeningAddress:'127.0.0.1:8793', Why:''}, {Outcome:'forbidden', Revision:'', Reason:''}],
 '/inference/audit': [{Outcome:'gap', Entries:[], Next:5, AtEnd:false}, {Outcome:'page', Entries:[
  {Sequence:5,UnixMs:0,Route:'window',Rung:'tcp-loopback/linux user=kernel process=bound path=bound',Program:'/usr/bin/python3',Host:'ollama',Model:'m',Credential:'key-name-only',Outcome:'completed',Reason:'',Operation:'op-completed',Profile:'chat',TokensIn:6,TokensOut:4,WallMs:18},
  {Sequence:6,UnixMs:0,Route:'window',Rung:'tcp-loopback/linux user=kernel process=bound path=bound',Program:'/usr/bin/opencode',Host:'ollama',Model:'m',Credential:'',Outcome:'cancelled',Reason:'cancel',Operation:'op-cancelled',Profile:'transcription',AudioSeconds:7,Characters:14,Images:2,Ceiling:'tokens 6/100'},
  {Sequence:7,UnixMs:0,Route:'window',Rung:'tcp-loopback/linux user=kernel process=bound path=bound',Program:'/usr/bin/opencode',Host:'',Model:'m',Credential:'',Outcome:'not_permitted',Reason:'<script>alert(1)</script>',Operation:''},
  {Sequence:8,UnixMs:0,Route:'window',Rung:'tcp-loopback/linux user=kernel process=bound path=bound',Program:'/usr/bin/opencode',Host:'ollama',Model:'m',Credential:'',Outcome:'refused',Reason:'upstream:503',Operation:'op-refused'}
 ], Next:9, AtEnd:true}],
};
// task 2026-09-23, fourteenth first-time visitor, finding 8: Issue key's
// own Program path now offers a chooser too, of the registered
// applications, the same source Rights' own Program chooser reads; the
// graphics card's running holders are never offered.
const cardTableReply = {resources:[{resource:'card:0', holders:[{program:'C:\\Windows\\System32\\csrss.exe'},{program:'C:\\Windows\\System32\\dwm.exe'}]}]};
const registeredApps = {outcome:'page', entries:[{program:'C:\\lms\\llama-server.exe', name:'', role:'application'}]};
const context = {URLSearchParams, document:{getElementById:element,createElement:tag=>Object.assign(node(),{tagName:tag})}, location:{search:'?k=test'}, console, Date, navigator:{clipboard:{writeText:async()=>{}}},
 fetch:async(path,opts)=>{
  assert.strictEqual(opts.headers['X-Panel-Key'], 'test');
  if (path === '/card/table') return {ok:true, json:async()=>cardTableReply};
  if (path === '/apps/registrations?source=applications&wait_ms=0') return {ok:true, json:async()=>registeredApps};
  const base = path.split('?')[0];
  calls.push({path, body: opts.body ? JSON.parse(opts.body) : null});
  const reply = replies[base].shift();
  if (reply && reply.__defer) return {ok:true, json:async()=>reply.__defer};
  if (reply && reply.__fail) return {ok:false, text: async()=>reply.__fail};
  return {ok:true, json:async()=>reply};
 }};
context.window = context; // window is the global object here, same as a browser (task 2026-09-23, fifteenth first-time visitor, finding 1)
vm.runInNewContext(plainScript, context);
vm.runInNewContext(serversScript, context);
const text = id => element(id).textContent;
const cells = row => row.children.map(td => td.textContent);
(async () => {
 const settle = () => new Promise(r => setTimeout(r, 0));
 // task 2026-09-23, fourteenth first-time visitor, findings 2 and 3: Hosts,
 // Gateway and Keys all used to open empty until their own "Show" button
 // was clicked, and a write refused ("List ... before ...") until then;
 // all three now read on arrival ("Show hosts"/"Show setting"/"Show keys"
 // renamed Refresh), so this settles the three eager loads before any
 // assertion or interaction.
 await settle(); await settle(); await settle();
 // Class and Kind used to be two columns saying the same thing (a local
 // host's Kind is already one of the known local backends, a hosted host's
 // Kind is already one of the known wire formats): they are now the one
 // "Kind" column, "hosted: " or "local: " ahead of the value (task
 // 2026-09-23 finding 3).
 // task 2026-09-23, eleventh first-time visitor, finding 5: the State
 // column used to read a raw connection error ("down: Get \"http://...\":
 // dial tcp ...: connectex: ..."); it now reads oaHostState's own plain
 // word, the raw reason kept on that cell's own title.
 assert.deepStrictEqual(cells(element('hosts').children[0]).slice(0, 6), ['openrouter','hosted: openai-compatible','openrouter','Not reachable','42 tokens, 2.50 (2026-09-17)','1000 tokens, 0.75']);
 // The address and modalities are the Name cell's second line, so the
 // table fits 1100 px; who registered it sits on the Name cell's title.
 assert.strictEqual(element('hosts').children[0].children[0].children.at(-1).textContent, 'https://openrouter.ai/api/v1 · chat, embed');
 assert.strictEqual(element('hosts').children[0].children[0].title, 'https://openrouter.ai/api/v1 · chat, embed · registered by operator');
 assert.equal(element('hosts').children[0].children[3].title, 'credential:unknown:openrouter', 'the raw reason sits on the State cell\'s own title');
 // A host with no credential, no spend yet and no ceiling reads as a stated
 // absence in each of those three cells, never a blank cell (task
 // 2026-09-23 finding 3).
 assert.deepStrictEqual(cells(element('hosts').children[1]).slice(0, 6), ['ollama','local: ollama','none','Up','0','no budget']);
 // The configuration revision is kept for the conditional edit, but is no
 // longer printed as a sentence; it moves to a title attribute instead.
 assert(!text('hosts-status').includes('Configuration revision'), 'hosts-status text: ' + text('hosts-status'));
 assert.strictEqual(element('hosts-status').title, 'Configuration revision hosts-v1:a');
 // task 2026-09-23, eleventh first-time visitor, requirement 3: Base URL
 // opened as one empty field with no hint what to type; it now offers
 // ollama's own documented default and every base already seen among the
 // current hosts, with "Other…" revealing the typed field.
 assert(element('host-base-select').children.map(o=>o.value).includes('http://127.0.0.1:11434'), 'ollama\'s documented default is offered');
 assert(element('host-base-select').children.map(o=>o.value).includes('https://openrouter.ai/api/v1'), 'openrouter\'s base (seen in the just-read hosts) is offered');
 assert.equal(element('host-base-select').children.at(-1).textContent, 'Other…');
 assert.equal(element('host-base').hidden, true, 'the typed field stays hidden until Other… is chosen');
 element('host-base-select').value='__other__'; element('host-base-select').onchange();
 assert.equal(element('host-base').hidden, false, 'choosing Other… reveals the typed field');
 element('host-base-select').value='http://127.0.0.1:11434'; element('host-base-select').onchange();
 assert.equal(element('host-base').value, 'http://127.0.0.1:11434', 'choosing a known base fills the field from it');
 assert.equal(element('host-base').hidden, true);
 Object.assign(element('host-name'), {value:'ollama'}); Object.assign(element('host-base'), {value:'http://127.0.0.1:11434'});
 // The form takes the spend ceiling in currency (task 2026-09-23 finding
 // 2); the request still carries it in micros.
 Object.assign(element('host-micros'), {value:'1.25'});
 // task 2026-09-23, fourteenth first-time visitor, findings 2 and 3: Add
 // host used to refuse ("List hosts before changing them.") unless Show
 // hosts had been clicked; the arrival read above already did that, so
 // this goes straight through.
 const hostsGetCallsBeforeAdd = calls.filter(c => c.path === '/inference/hosts' && !c.body).length;
 element('host-add').onsubmit({preventDefault(){}}); await settle();
 // A successful add now reads the hosts list again itself, so the add's
 // own POST is the second-to-last call, not the last.
 assert.deepStrictEqual(calls.at(-2).body, {edit:'add', host:{Name:'ollama',Hosted:false,Kind:'ollama',Base:'http://127.0.0.1:11434',Credential:'',Ceiling:{TokensPerDay:0,MicrosPerDay:1250000}}, revision:'hosts-v1:a'});
 // task 2026-09-23, tenth first-time visitor, finding 1: "Applied;
 // configuration revision hosts-v1:b" printed the new revision as visible
 // text; it now reads "Applied.", the revision on the line's own title and
 // behind Technical details. task 2026-09-23, fourteenth first-time
 // visitor, finding 3: it also used to add "List again before another
 // change." while the table under it kept its stale rows; the table
 // refreshed itself instead, so that sentence goes too.
 assert.strictEqual(text('hosts-status'), 'Applied.');
 assert.strictEqual(element('hosts-status').title, 'Configuration revision hosts-v1:b');
 assert.strictEqual(element('hosts-status').children[0].children[0].textContent, 'Technical details');
 assert(element('hosts-status').children[0].children[1].textContent.includes('hosts-v1:b'));
 assert.strictEqual(calls.filter(c => c.path === '/inference/hosts' && !c.body).length, hostsGetCallsBeforeAdd + 1, 'a successful add read the hosts list again itself, with no click');
 element('hosts').children[0].children[6].children[0].onclick(); await settle();
 assert.deepStrictEqual(calls.at(-1).body, {edit:'remove', name:'openrouter', revision:'hosts-v1:a'});
 assert.match(text('hosts-status'), /configuration changed/);
 // task 2026-09-23, ninth first-time visitor, finding 3: Show hosts with no
 // hosts left only the table's own headers and no line at all; it now
 // names what to do next, the same as Gateway keys' and Audit's own
 // empty/end-of-list lines already did. A conflicted remove (above) did
 // not refresh the list itself (only a successful write does), so this is
 // still a manual Refresh click.
 context.serversRefresh.hosts(); await settle();
 assert.strictEqual(element('hosts-empty').hidden, false, 'the empty list shows its one sentence');
 assert.match(text('hosts-status'), /configuration changed/, 'the Remove sentence survives the reread');
 assert.strictEqual(element('hosts').children.length, 0);
 // Gateway already shows the arrival read too, with no click; "Show
 // setting" is Refresh now.
 assert.strictEqual(text('gateway-status'), 'Off.', 'the gateway state reads as one sentence');
 assert.strictEqual(element('gateway-status').title, 'Setting revision gateway-v1:a');
 element('gateway-switch').checked = true; element('gateway-switch').onchange(); await settle(); await settle();
 assert.deepStrictEqual(calls.filter(c => c.path === '/inference/gateway').map(c => c.body), [null, {revision:'gateway-v1:a', open:true, address:'127.0.0.1:8793'}, null]);
 assert.strictEqual(text('gateway-status'), 'On. OpenAI apps use http://127.0.0.1:8793/v1 and Anthropic apps use http://127.0.0.1:8793.');
 assert.strictEqual(element('gateway-address').value, '127.0.0.1:8793', 'the address field shows the setting in effect');
 assert.strictEqual(element('gateway-status').title, 'Setting revision gateway-v1:b');
 assert.strictEqual(element('gateway-switch').checked, true, 'the switch follows the gateway');
 element('gateway-switch').checked = false; element('gateway-switch').onchange(); await settle();
 assert.deepStrictEqual(calls.at(-1).body, {revision:'gateway-v1:b', open:false, address:''});
 assert.match(text('gateway-status'), /not permit/);
 // Issue key with the required program path left blank used to depend on
 // the browser's own validation UI alone: with no keys listed yet, a click
 // showed no row, no error and no field (task 2026-09-23, fifth first-time
 // visitor, finding 6). A real browser blocks the submit event itself in
 // this case (this test's onsubmit stand-in cannot model that refusal) but
 // always fires "invalid" on the field that failed, submit or not; that is
 // simulated here, and keys-status must name the problem from it, with no
 // request sent.
 // task 2026-09-23, fourteenth first-time visitor, finding 8: Program path
 // was a bare text box, while Rights already offered a chooser for the
 // same kind of field; it now builds its own equivalent chooser from
 // oaProgramChooser (panel_shell.go), the one shared implementation,
 // seeded from the registered applications.
 assert.deepEqual(element('key-program-select').children.map(o=>o.value), ['C:\\lms\\llama-server.exe','__other__'], 'the chooser opens with the registered applications, Other\u2026 last');
 assert.equal(element('key-program-select').children[0].textContent, 'llama-server', 'a program reads by name, without .exe');
 assert.equal(element('key-program-select').children[0].title, 'C:\\lms\\llama-server.exe');
 element('key-program-select').value='C:\\lms\\llama-server.exe';
 element('key-program-select').onchange();
 assert.equal(element('key-program').hidden, true, 'choosing a known program hides the typed field');
 assert.equal(element('key-program').value, 'C:\\lms\\llama-server.exe', 'choosing a known program syncs the typed field\'s own value, so the existing submit handler keeps reading it unchanged');
 element('key-program-select').value='__other__';
 element('key-program-select').onchange();
 assert.equal(element('key-program').hidden, false, 'choosing Other\u2026 reveals the typed field again');
 assert.equal(element('key-program').value, '', 'choosing Other\u2026 clears the typed field for a fresh entry');

 const callsBeforeBlank = calls.length;
 element('key-program').dispatch('invalid');
 assert.strictEqual(calls.length, callsBeforeBlank, 'a blank program path sends no request');
 assert.strictEqual(text('keys-status'), 'Program path is required to issue a key.');

 Object.assign(element('key-program'), {value:'/usr/bin/python3'});
 element('key-issue').onsubmit({preventDefault(){}}); await settle();
 // task 2026-09-23, fourteenth first-time visitor, finding 3: Issue key's
 // own reply used to say "Issued. List again to refresh." while the table
 // under it kept its stale rows; it now reads the list again itself
 // before showing the confirmation, so the issue's own POST is the
 // second-to-last call, not the last, and the table already shows the new
 // key with no click.
 assert.deepStrictEqual(calls.at(-2).body, {edit:'issue', program:'/usr/bin/python3', credential:''});
 assert.strictEqual(text('keys-status'), 'Issued.');
 assert.strictEqual(element('key-shown').textContent, 'oalk_shown_once');
 assert.strictEqual(element('key-shown-row').hidden, false);
 await element('key-copy').onclick();
 assert.strictEqual(text('key-copy-status'), 'Copied the key.', 'Copy writes one sentence');
 assert.strictEqual(cells(element('keys').children[0])[1], 'active');
 assert.strictEqual(cells(element('keys').children[0])[0], 'python3', 'a key row names the program by its file name');
 assert.strictEqual(element('keys').children[0].children[0].title, '/usr/bin/python3');
 // task 2026-09-23, tenth first-time visitor, findings 2 and 3: Issued read
 // a raw ISO timestamp, and Issued by read a full path, both as visible
 // cell text; each now reads a local readable value (or a file name), the
 // raw value kept as that cell's own title.
 const keyRow = element('keys').children[0];
 assert.strictEqual(cells(keyRow)[3], new Date(Date.parse('2020-01-01T00:00:00Z')).toLocaleDateString(), 'a key issued well before today reads a local date, not a raw ISO timestamp');
 assert.strictEqual(keyRow.children[3].title, new Date(Date.parse('2020-01-01T00:00:00Z')).toISOString());
 assert.strictEqual(cells(keyRow)[4], 'openabstractions', 'Issued by reads the program name, without .exe');
 assert.strictEqual(keyRow.children[4].title, 'C:\\oa\\openabstractions.exe', 'the full path sits on the cell\'s own title');
 element('keys').children[0].children[5].children[0].onclick(); await settle();
 assert.deepStrictEqual(calls.at(-1).body, {edit:'revoke', program:'/usr/bin/python3'});
 assert.strictEqual(element('key-shown').textContent, '', 'a later action no longer shows the issued key');
 assert.match(text('keys-status'), /not permit/);
 // A transport failure (the fetch itself rejects, not a typed Outcome) is
 // shown as a plain sentence, never the raw text naming a pipe path and a
 // security identifier; that text stays available behind a disclosure. The
 // text names a contract ("abstraction.inference/keys@1"), so oaPlain reads
 // it as the runtime answering and lacking that service, not as "isn't
 // running" (task 2026-09-23 finding 1).
 context.serversRefresh.keys(); await settle();
 assert.strictEqual(text('keys-status'), "The runtime is running but doesn't provide inference on this account.");
 const keysDetails = element('keys-status').children[0];
 assert.strictEqual(keysDetails.className, 'oa-error-details');
 assert.strictEqual(keysDetails.children[1].textContent, 'service resolution: unavailable: abstraction.inference/keys@1 could not be resolved at the installed runtime at \\\\.\\pipe\\openabstractions-user-S-1-5-21-1001');

 // task 2026-09-23, seventh first-time visitor, finding 4: Add host with a
 // blank required field used to show no result at all.
 element('host-name').dispatch('invalid');
 assert.strictEqual(text('hosts-status'), 'Name is required to add a model server.', 'a blank required field is named, the Issue key pattern');


 // Live reread (Panel rule, S8 review): click Remove, the server state
 // changes elsewhere, the next tick rereads; the click's sentence and the
 // surviving row's element stay.
 const hostA={Entry:{Name:'alpha',Hosted:false,Kind:'k',Base:'http://127.0.0.1:1',Profiles:['chat'],DeclaredBy:'x'},Up:true,Why:''};
 const hostB={Entry:{Name:'beta',Hosted:false,Kind:'k',Base:'http://127.0.0.1:2',Profiles:['chat'],DeclaredBy:'x'},Up:true,Why:''};
 replies['/inference/hosts'].push({Outcome:'page',Revision:'live-1',Hosts:[hostA,hostB]});
 await context.serversRefresh.hosts(); await settle();
 const betaRow=element('hosts').children[1];
 await require('./panel_fake_dom_test_helper.js').assertLiveUpdateKeepsRow({
  click: async () => { replies['/inference/hosts'].push({Outcome:'conflict',Revision:'',Reason:''}); element('hosts').children[0].children[6].children[0].onclick(); await settle(); },
  change: () => { replies['/inference/hosts'].push({Outcome:'page',Revision:'live-2',Hosts:[hostA,{...hostB,Up:false}]}); },
  data: () => replies['/inference/hosts'],
  tick: async () => { await context.serversRefresh.hosts(); await settle(); },
  row: () => element('hosts').children[1], status: () => element('hosts-status'), label: 'Providers, model servers',
 });
 assert.strictEqual(cells(betaRow)[3],'Not reachable','the row updated in place');
 const realNow = Date.now; Date.now = () => realNow() + 61000; await context.serversTick(); await settle(); Date.now = realNow;
 assert.strictEqual(element('key-shown').textContent, '', 'the key is cleared on the first tick after a minute');
 await modelsPage();
 console.log('PASS inference page');
})().catch(e => { console.error(e); process.exit(1); });

// The Models page (inference_page.go with selection_page.go's script): the
// Use selector, per-app choosers filled on arrival, the fallback order, the
// available models by friendly name and Add a model. No visible text carries
// a path or a hash. Every live check changes the server's state between
// ticks, then asserts the row element and its sentence stay.
function goScript(file, name) {
 const source = fs.readFileSync(__dirname + '/' + file, 'utf8'), marker = 'const ' + name + ' = `<script>';
 const start = source.indexOf(marker);
 assert(start >= 0, name + ' not found in ' + file);
 return source.slice(start + marker.length, source.indexOf('</script>`', start));
}
const pageScripts = [goScript('inference_page.go', 'inferenceScript'), goScript('selection_page.go', 'selectionPolicyScript')];
function fakeDOM() {
 const fields = new Map(), created = [];
 function mk(tag) {
  const n = {tagName: String(tag || '').toUpperCase(), value: '', textContent: '', className: '', title: '', disabled: false, hidden: false, dataset: {}, attrs: {}, children: [],
   replaceChildren(...v) { this.children = [...v] }, append(...v) { this.children.push(...v) }, setAttribute(k, v) { this.attrs[k] = v }, removeAttribute(k) { delete this.attrs[k] },
   focus() { this.focused = true }};
  return n;
 }
 function walk(n, visit) { visit(n); for (const c of n.children || []) if (c && typeof c === 'object') walk(c, visit) }
 function find(id) { let hit = null; for (const root of fields.values()) walk(root, n => { if (!hit && n.id === id) hit = n }); if (!hit) for (const n of created) if (n.id === id) hit = n; return hit }
 function element(id) { if (fields.has(id)) return fields.get(id); const hit = find(id); if (hit) return hit; const n = mk('div'); n.id = id; fields.set(id, n); return n }
 const createElement = tag => { const n = mk(tag); created.push(n); return n };
 return {fields, element, createElement, walk};
}
async function modelsPage() {
 const settle = async () => { for (let i = 0; i < 20; i++) await new Promise(r => setTimeout(r, 0)) };
 const apps = {outcome: 'page', entries: [
  {program: 'C:\\Apps\\chat.exe', name: 'chat-app', role: 'application', application: {Descriptor: {Title: 'Chat App'}}},
  {program: 'C:\\Apps\\chat.exe', name: 'chat-app', role: 'application', application: {Descriptor: {Title: 'Chat App'}}},
  {program: 'C:\\Apps\\draw.exe', name: 'draw', role: 'application', application: {Descriptor: {Title: ''}}}]};
 const offers = {
  chat: [
   {Family: 'qwen3.6-demo', Names: ['qwen3.6-demo', 'qwen/tiny'], Servers: ['modelhost'], Context: 32768, ContextSource: 'default'},
   {Family: 'reported', Names: ['reported'], Servers: ['cloud'], Context: 8192, ContextSource: 'effective'},
   {Family: 'maximum.gguf', Names: ['maximum'], Servers: ['native-runtime', 'local-http'], Context: 0, ContextSource: 'unknown'},
   {Family: 'sha256-0123456789abcdef0123456789abcdef', Names: ['x'], Servers: ['local-http'], Context: 0, ContextSource: 'none'}],
  image: [{Family: 'flux2-klein-4b', Names: ['flux2-klein-4b'], Servers: ['thenoise'], Context: 0, ContextSource: 'none'}],
  speech: []};
 const hosts = {Outcome: 'page', Revision: 'h1', Hosts: [
  {Entry: {Name: 'cloud', Hosted: true, Credential: 'cloudcred'}, Up: true},
  {Entry: {Name: 'local-http', Hosted: false, Credential: ''}, Up: true}]};
 const view = {providers: {Outcome: 'page', Declarations: [
  {Declaration: {Name: 'native-runtime'}, Role: 'provider'}, {Declaration: {Name: 'modelhost'}, Role: 'provider'}, {Declaration: {Name: 'some-app'}, Role: 'application'}]}};
 const policies = new Map();
 let revision = 0;
 policies.set('C:\\Apps\\chat.exe|oa-chat-default', {rev: 'r0', candidates: [
  {Model: 'qwen/tiny', Placement: 'local', Credential: '', Provider: 'native-runtime'},
  {Model: 'maximum', Placement: 'local', Credential: '', Provider: ''},
  {Model: 'old-model', Placement: 'local', Credential: '', Provider: ''}]});
 let localReply = {available: true, plan_id: 'plan-1', feature: {installed: true}, engine: {staged: true},
  root: {path: 'C:\\Models\\gguf', store: 'store-1', models: [
   {id: 'm1', name: 'Qwen3-8B-Q4.gguf', family: 'qwen3', quant: 'Q4_K_M', size: 5000000000, hosted: false, approval: 'Allow ModelHost to load C:\\Models\\gguf\\Qwen3-8B-Q4.gguf (inventory ID 0123456789abcdef0123)', approval_digest: 'd1'},
   {id: 'm2', name: 'phi4.gguf', family: 'phi4', size: 2000000000, hosted: true}]}};
 const calls = [];
 let hostReply = null, holdRead = null;
 function load(options = {}) {
  const dom = fakeDOM(); dom.root = {dataset: {}};
  dom.element('models-profile').value = 'chat';
  dom.element('add-model-folder').hidden = true;
  const context = {URLSearchParams, document: {getElementById: dom.element, createElement: dom.createElement, documentElement: dom.root}, location: {search: '?k=test'}, console, Date,
   fetch: async (path, opts) => {
    assert.strictEqual(opts.headers['X-Panel-Key'], 'test');
    const body = opts.body ? JSON.parse(opts.body) : null;
    calls.push({path, body});
    const ok = value => ({ok: true, json: async () => value});
    if (path.startsWith('/apps/registrations?')) return ok(apps);
    if (path.startsWith('/inference/models?profile=')) {
     const use = decodeURIComponent(path.split('=')[1]);
     if (options.offersFail) return {ok: false, text: async () => options.offersFail};
     return ok({Offers: offers[use] || []});
    }
    if (path === '/inference/hosts') return ok(hosts);
    if (path === '/registry/view') return ok(view);
    if (path.startsWith('/inference/audit')) throw Error('the Models page reads no model calls');
    if (path === '/setup/modelhost/chat') return ok(JSON.parse(JSON.stringify(options.local || localReply)));
    if (path === '/setup/modelhost/host') { if (hostReply) return hostReply(); return ok({hosted: true}) }
    if (path === '/setup/modelhost/root/preview') return ok({root: {path: body.path}, approval: 'Read ' + body.path + ' in place', approval_digest: 'pd'});
    if (path === '/setup/modelhost/root') return ok({registered: true});
    assert.strictEqual(path, '/registry/selection');
    const k = body.target_program + '|' + body.name, stored = policies.get(k);
    if (body.edit === 'read') {
     const reply = {read: stored ? {Outcome: 'found', Revision: stored.rev, Policy: {Name: body.name, Candidates: JSON.parse(JSON.stringify(stored.candidates))}} : {Outcome: 'unknown'}};
     if (holdRead && body.target_program === holdRead.program) { const hold = holdRead; holdRead = null; await hold.gate; }
     return ok(reply);
    }
    if ((stored ? stored.rev : '') !== (body.revision || '')) return ok({change: {Outcome: 'conflict'}});
    if (body.edit === 'delete') policies.delete(k);
    else policies.set(k, {rev: 'r' + (++revision), candidates: body.candidates.map(c => ({Model: c.model, Placement: c.placement, Credential: c.credential, Provider: c.provider}))});
    return ok({change: {Outcome: 'applied'}});
   }};
  vm.runInNewContext(plainScript, context);
  for (const s of pageScripts) vm.runInNewContext(s, context);
  return Object.assign(dom, {context});
 }
 // The Panel rules on what the scripts rendered (static/panel_rules.js):
 // no path, SID or hash as visible text, no empty field, every button with
 // a status line in its row or section. Each section is the markup's section
 // around the nodes this fake DOM keeps by id; the markup's status lines
 // carry role="status".
 const { assertPanelRules } = require('./static/panel_rules.js');
 function rules(dom) {
  const section = (id, ...children) => ({tagName: 'SECTION', id, children});
  const status = id => Object.assign(dom.element(id), {role: 'status'});
  assertPanelRules([
   section('selection-section', status('selection-status'), dom.element('selection-apps'), dom.element('selection-fallback-rows'),
    {className: 'oa-row', children: [dom.element('selection-fallback-model'), {tagName: 'BUTTON', textContent: 'Add'}, status('selection-fallback-status')]}),
   section('models-section', status('models-status'), dom.element('models')),
   section('add-model', status('add-model-status'), dom.element('add-model-rows'), dom.element('add-model-tech')),
  ], 'Models');
 }
 const dom = load();
 const {element} = dom;
 const text = id => element(id).textContent;
 const appStatus = i => element('selection-apps').children[i].children[2].textContent;
 const tick = async () => { dom.context.modelsLive(); await settle() };
 const rowText = row => row.children.map(c => c.children ? c.children.filter(x => !x.hidden).map(x => x.textContent).join(' | ') : c.textContent);
 const lastPut = () => calls.filter(c => c.body && c.body.edit === 'put').at(-1).body;
 await settle();

 // Arrival: ready, the chat use, its available models by friendly name.
 assert(fs.readFileSync(__dirname + '/inference_page.go', 'utf8').includes('var inferencePage = `<div data-ready-owner="page" hidden></div>`'), 'the served markup declares that the page owns its ready signal');
 assert.strictEqual(dom.root.dataset.ready, '1', 'the page says it is ready once its first reads finish');
 assert.strictEqual(element('models-profile').value, 'chat');
 assert.deepStrictEqual(element('models').children.map(r => rowText(r)[0]), ['qwen3.6-demo | Up to 32,768 tokens', 'reported | Up to 8,192 tokens', 'maximum', 'Unnamed model']);
 assert.strictEqual(text('models-status'), '', 'the rows say it; the status line waits for an error');
 assert.strictEqual(element('models').children.length, 4);
 assert(!fs.readFileSync(__dirname + '/inference_page.go', 'utf8').includes('audit'), 'the model calls are Activity\'s');
 assert(!calls.some(c => c.path.startsWith('/inference/audit')));

 // Each app's chat chooser shows its stored choice on arrival.
 const appRows = element('selection-apps').children;
 assert.deepStrictEqual(appRows.map(r => r.children[0].children[0].textContent), ['Chat model for Chat App', 'Chat model for draw'], 'one row per registered app, by title');
 const chatSelect = appRows[0].children[1].children[0], drawSelect = appRows[1].children[1].children[0];
 assert.strictEqual(chatSelect.value, 'qwen3.6-demo', 'a stored alias shows as the offered model it names');
 assert.deepStrictEqual(chatSelect.children.map(o => o.textContent), ["App's own choice", 'qwen3.6-demo', 'reported', 'maximum', 'Unnamed model']);
 assert.strictEqual(drawSelect.value, '', "an app with no policy shows its own choice");
 assert(!fs.readFileSync(__dirname + '/selection_page.go', 'utf8').includes('Default model'), 'no default row until the service holds an account-wide default');
 assert.deepStrictEqual(calls.filter(c => c.path === '/registry/selection').map(c => c.body), [
  {edit: 'read', target_program: 'C:\\Apps\\chat.exe', name: 'oa-chat-default'}, {edit: 'read', target_program: 'C:\\Apps\\draw.exe', name: 'oa-chat-default'}]);
 rules(dom);

 // Fallback order: the first app's later candidates, in order.
 assert.strictEqual(element('selection-fallback-app').value, 'C:\\Apps\\chat.exe');
 const fallbackLabels = () => element('selection-fallback-rows').children.map(r => r.children[0].children[0].textContent);
 assert.deepStrictEqual(fallbackLabels(), ['1. maximum', '2. old-model']);
 assert.deepStrictEqual(element('selection-fallback-model').children.map(o => o.value), ['reported', 'sha256-0123456789abcdef0123456789abcdef'], 'Add offers the models not yet in the order');
 // Move up writes the reordered policy into its own row.
 const movedRow = element('selection-fallback-rows').children[1];
 movedRow.children[1].children[0].onclick(); await settle();
 assert.deepStrictEqual(lastPut().candidates, [
  {model: 'qwen/tiny', placement: 'local', credential: '', provider: 'native-runtime'},
  {model: 'old-model', placement: 'local', credential: '', provider: ''},
  {model: 'maximum', placement: 'local', credential: '', provider: ''}]);
 assert.deepStrictEqual(fallbackLabels(), ['1. old-model', '2. maximum']);
 assert.strictEqual(element('selection-fallback-rows').children[0], movedRow, 'the moved row is the same row, updated in place');
 assert.strictEqual(movedRow.children[2].textContent, 'old-model moved up.', 'Move up writes into its own row');
 assert.strictEqual(movedRow.children[1].children[0].hidden, true, 'the first fallback has no Move up');
 // The server's state changes elsewhere (another fallback lands at the
 // end); the live reread shows it and keeps the row and its sentence.
 policies.get('C:\\Apps\\chat.exe|oa-chat-default').candidates.push({Model: 'reported', Placement: 'hosted', Credential: 'cloudcred', Provider: ''});
 policies.get('C:\\Apps\\chat.exe|oa-chat-default').rev = 'elsewhere-1';
 await tick();
 assert.deepStrictEqual(fallbackLabels(), ['1. old-model', '2. maximum', '3. reported'], 'the reread shows the changed order');
 assert.strictEqual(element('selection-fallback-rows').children[0], movedRow);
 assert.strictEqual(movedRow.children[2].textContent, 'old-model moved up.', 'the sentence survives a reread of changed state');
 // Remove drops one fallback.
 element('selection-fallback-rows').children[2].children[1].children[1].onclick(); await settle();
 assert.deepStrictEqual(fallbackLabels(), ['1. old-model', '2. maximum']);
 assert.strictEqual(text('selection-fallback-status'), 'reported is no longer a fallback.');
 // Add takes a hosted model's placement and key from its server.
 element('selection-fallback-model').value = 'reported'; await element('selection-fallback-add').onclick(); await settle();
 assert.deepStrictEqual(lastPut().candidates.at(-1), {model: 'reported', placement: 'hosted', credential: 'cloudcred', provider: ''});
 assert.strictEqual(text('selection-fallback-status'), 'reported is now a fallback for Chat App.');

 // Choosing a model moves only that one candidate to first place: the old
 // first stays, and a second candidate for the same model with another
 // placement stays a fallback.
 policies.get('C:\\Apps\\chat.exe|oa-chat-default').candidates.push({Model: 'maximum', Placement: 'hosted', Credential: 'cloudcred', Provider: ''});
 policies.get('C:\\Apps\\chat.exe|oa-chat-default').rev = 'elsewhere-2';
 await tick();
 chatSelect.value = 'maximum.gguf'; await chatSelect.onchange(); await settle();
 assert.strictEqual(lastPut().target_program, 'C:\\Apps\\chat.exe');
 assert.deepStrictEqual(lastPut().candidates, [
  {model: 'maximum', placement: 'local', credential: '', provider: ''},
  {model: 'old-model', placement: 'local', credential: '', provider: ''},
  {model: 'reported', placement: 'hosted', credential: 'cloudcred', provider: ''},
  {model: 'maximum', placement: 'hosted', credential: 'cloudcred', provider: ''}], 'the earlier first candidate is replaced and every other candidate keeps its place');
 assert.strictEqual(appStatus(0), 'Chat App now uses maximum as its chat model.');
 assert.strictEqual(chatSelect.value, 'maximum.gguf');
 // The server's state changes elsewhere; the live reread updates the
 // chooser's value in the same element and keeps the sentence.
 policies.get('C:\\Apps\\chat.exe|oa-chat-default').candidates[0] = {Model: 'reported', Placement: 'hosted', Credential: 'cloudcred', Provider: ''};
 policies.get('C:\\Apps\\chat.exe|oa-chat-default').rev = 'elsewhere-3';
 const chatRow = element('selection-apps').children[0];
 await tick();
 assert.strictEqual(element('selection-apps').children[0], chatRow, 'the app row is the same element after a reread');
 assert.strictEqual(chatRow.children[1].children[0], chatSelect, 'the chooser is the same element after a reread');
 assert.strictEqual(chatSelect.value, 'reported', 'the reread shows the changed choice');
 assert.strictEqual(appStatus(0), 'Chat App now uses maximum as its chat model.', 'the sentence survives a reread of changed state');

 // A new local model pins this machine's provider that offers it.
 drawSelect.value = 'maximum.gguf'; await drawSelect.onchange(); await settle();
 assert.deepStrictEqual(lastPut(), {edit: 'put', target_program: 'C:\\Apps\\draw.exe', name: 'oa-chat-default', revision: '',
  candidates: [{model: 'maximum.gguf', placement: 'local', credential: '', provider: 'native-runtime'}]});
 // A hosted one carries its server's key and no pin.
 drawSelect.value = 'reported'; await drawSelect.onchange(); await settle();
 assert.deepStrictEqual(lastPut().candidates, [{model: 'reported', placement: 'hosted', credential: 'cloudcred', provider: ''}]);
 assert.strictEqual(appStatus(1), 'draw now uses reported as its chat model.');
 // The app's own choice deletes its policy with its fallback order.
 drawSelect.value = ''; await drawSelect.onchange(); await settle();
 assert.strictEqual(calls.filter(c => c.body && c.body.edit === 'delete').at(-1).body.target_program, 'C:\\Apps\\draw.exe');
 assert.strictEqual(appStatus(1), 'draw now chooses its own chat model; its fallback order is removed.');
 assert.strictEqual(policies.has('C:\\Apps\\draw.exe|oa-chat-default'), false);
 // A conflicting write says so and shows the current choice.
 policies.get('C:\\Apps\\chat.exe|oa-chat-default').rev = 'elsewhere-4';
 chatSelect.value = 'qwen3.6-demo'; await chatSelect.onchange(); await settle();
 assert.strictEqual(appStatus(0), 'The choice changed elsewhere; this shows the current one.');
 assert.strictEqual(chatSelect.value, 'reported');

 // A live pass whose read started before a save finished drops that read:
 // the read is held while the save lands, then answers with the old state.
 let release;
 holdRead = {program: 'C:\\Apps\\draw.exe', gate: new Promise(r => { release = r })};
 dom.context.modelsLive(); await settle();
 drawSelect.value = 'qwen3.6-demo'; const saving = drawSelect.onchange(); await settle(); await saving;
 assert.strictEqual(appStatus(1), 'draw now uses qwen3.6-demo as its chat model.');
 release(); await settle();
 assert.strictEqual(drawSelect.value, 'qwen3.6-demo', 'the older read does not replace the saved choice');
 assert.strictEqual(appStatus(1), 'draw now uses qwen3.6-demo as its chat model.');

 // Another use: pictures read their own policy; a use with no policy name
 // says apps choose their own.
 element('models-profile').value = 'image'; element('models-profile').onchange(); await settle();
 assert.strictEqual(calls.filter(c => c.path === '/registry/selection').at(-1).body.name, 'oa-image-default');
 assert.strictEqual(element('selection-apps').children[0].children[0].children[0].textContent, 'Picture model for Chat App');
 assert.deepStrictEqual(element('models').children.map(r => rowText(r)[0]), ['flux2-klein-4b']);
 element('models-profile').value = 'speech'; element('models-profile').onchange(); await settle();
 assert.strictEqual(element('selection-apps').children[0].textContent, 'Apps choose their own speech model.');
 assert.strictEqual(element('selection-fallback').hidden, true, 'no fallback order without a policy');
 assert.strictEqual(element('models').children.length, 1);
 assert.strictEqual(element('models').children[0].className, 'oa-empty', 'an empty list is one sentence in a card');
 assert.strictEqual(element('models').children[0].textContent, 'No models are available for Speech yet.');

 // Add a model: the folder by its name, each model by its friendly name.
 const localRows = () => element('add-model-rows').children;
 assert.deepStrictEqual(localRows().map(r => rowText(r)[0]), [
  'gguf | 2 models, read in place.', 'Qwen3-8B-Q4 | qwen3 · Q4_K_M · 5 GB', 'phi4 | phi4 · 2 GB']);
 assert.strictEqual(localRows()[2].children[1].children[0].textContent, 'Hosted on demand');
 const hostRow = localRows()[1], host = hostRow.children[1].children[0];
 // A failed step returns the button to its own label.
 hostReply = () => ({ok: false, text: async () => 'host refused'});
 await host.onclick();
 assert.strictEqual(host.textContent, 'Confirm', 'hosting asks once more on the same button');
 assert.strictEqual(hostRow.children[2].textContent, 'Confirm to host Qwen3-8B-Q4; it starts only when an app asks.');
 rules(dom);
 await host.onclick(); await settle();
 assert.strictEqual(host.textContent, 'Host on demand', 'after a failed step the button reads its own label');
 assert.strictEqual(hostRow.children[2].textContent, "This didn't work.");
 // A click while another step runs writes one sentence and starts nothing.
 let finishHost;
 hostReply = () => new Promise(r => { finishHost = () => r({ok: true, json: async () => ({hosted: true})}) });
 await host.onclick(); const hosting = host.onclick(); await settle();
 const folderRow = localRows()[0];
 const before = calls.length;
 await folderRow.children[1].children[0].onclick();
 await element('add-model-check').onclick();
 assert.strictEqual(element('add-model-folder-status').textContent, 'Another step is running.');
 await localRows()[1].children[1].children[0].onclick();
 assert.strictEqual(hostRow.children[2].textContent, 'Another step is running.');
 assert.strictEqual(calls.length, before, 'nothing else is sent while a step runs');
 localReply.root.models[0].hosted = true;
 finishHost(); await hosting; await settle();
 assert.deepStrictEqual(calls.filter(c => c.path === '/setup/modelhost/host').at(-1).body, {plan_id: 'plan-1', store: 'store-1', id: 'm1', approval_digest: 'd1', confirm: true});
 assert.strictEqual(localRows()[1], hostRow, 'the model row is updated in place');
 assert.strictEqual(hostRow.children[1].children[0].textContent, 'Hosted on demand');
 assert.strictEqual(hostRow.children[2].textContent, 'Qwen3-8B-Q4 is hosted on demand.');
 // Add a model is live: a model appears in the folder, the reread adds its
 // row and keeps the others with their sentences.
 localReply.root.models.push({id: 'm3', name: 'mistral.gguf', family: 'mistral', size: 4000000000, hosted: false, approval: 'x', approval_digest: 'd3'});
 await tick();
 assert.deepStrictEqual(localRows().map(r => rowText(r)[0]).slice(0, 1).concat(localRows().slice(3).map(r => rowText(r)[0])), ['gguf | 3 models, read in place.', 'mistral | mistral · 4 GB']);
 assert.strictEqual(localRows()[1], hostRow);
 assert.strictEqual(hostRow.children[2].textContent, 'Qwen3-8B-Q4 is hosted on demand.', 'the sentence survives a live reread of changed state');
 // Choose folder… opens the folder row; Check folder previews, Confirm adds.
 localRows()[0].children[1].children[0].onclick();
 assert.strictEqual(element('add-model-folder').hidden, false);
 element('add-model-path').value = 'D:\\More models';
 await element('add-model-check').onclick();
 assert.strictEqual(text('add-model-folder-status'), 'Confirm to read More models in place.');
 await element('add-model-check').onclick(); await settle();
 assert.deepStrictEqual(calls.filter(c => c.path === '/setup/modelhost/root').at(-1).body, {plan_id: 'plan-1', path: 'D:\\More models', approval_digest: 'pd', confirm: true});
 assert.strictEqual(text('add-model-folder-status'), 'More models is added; its models stay where they are.');
 assert.strictEqual(element('add-model-folder').hidden, true);
 assert.strictEqual(element('add-model-check').textContent, 'Check folder');
 rules(dom);

 // Unavailable local models and unreadable offers each read as one sentence.
 const other = load({local: {available: false, reason: 'ModelHost Setup requires the installed runtime\'s user state'}, offersFail: 'router forbidden: abstraction.router/inventory.read'});
 await settle();
 assert.strictEqual(other.element('add-model-status').textContent, '');
 assert.strictEqual(rowText(other.element('add-model-rows').children[0])[0], "Local models | Can't be added on this computer.", 'one row says it');
 assert.strictEqual(other.element('add-model-tech').hidden, false, 'its reason sits under the group\'s Technical details');
 assert.strictEqual(other.element('add-model-reason').textContent, 'ModelHost Setup requires the installed runtime\'s user state');
 rules(other);
 assert.match(other.element('models-status').textContent, /did not permit this Panel/);
 assert.strictEqual(other.element('selection-status').attrs['data-state'], 'error');
 assert.strictEqual(other.root.dataset.ready, '1', 'a page whose reads failed is still ready');
 const engine = load({local: {available: true, plan_id: 'p', feature: {installed: true}, engine: {staged: false, bundled: {available: true, approval: 'Stage from C:\\x (SHA-256 0123456789abcdef0123)', approval_digest: 'ad'}}}});
 await settle();
 const engineRow = engine.element('add-model-rows').children[0];
 assert.strictEqual(rowText(engineRow)[0], 'Local model engine | Ready to prepare.');
 assert.strictEqual(engineRow.children[1].children[1].href, '/setup?k=test#setup-modelhost', 'the pinned ZIP recovery stays on Get started');
 rules(engine);
 console.log('PASS models page');
}
