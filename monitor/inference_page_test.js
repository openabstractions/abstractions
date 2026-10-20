const nativeCandidates=rows=>rows.map(candidate=>JSON.parse(JSON.stringify(require('./panel_record_test_helper.js').codecs.inference.decodeSelectionCandidateV2(new TextEncoder().encode(JSON.stringify(candidate))))));
// Run with node monitor/inference_page_test.js; no browser or service is started.
// A GB is 1024³ bytes, as oaBytes and Windows Explorer count it.
const GB = 1024 ** 3;
const fs = require('fs'), vm = require('./panel_record_test_helper.js').vm, assert = require('assert');
const { FakeHTMLCollection } = require('./panel_fake_dom_test_helper.js');
const go =fs.readFileSync(__dirname + '/inference_page.go', 'utf8');
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
  {Sequence:5,UnixMs:0,Route:'window',Rung:'tcp-loopback/linux user=kernel process=bound path=bound',Program:'/usr/bin/python3',Host:'ollama',Model:'m',Credential:'key-name-only',Outcome:'completed',Reason:'',Operation:'op-completed',Modality:'chat',TokensIn:6,TokensOut:4,WallMs:18},
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
 // A model server is one Settings-style row that fits a narrow window: its
 // name, a caption with kind, address and modalities, and a caption with
 // state, key, spend and budget. Who registered it sits on the label's
 // title; the raw state reason on the second caption's title.
 const hostText = r => r.children[0].children.map(n => n.textContent);
 assert.deepStrictEqual(hostText(element('hosts').children[0]), ['openrouter','Hosted model server · chat, embeddings','Not reachable · Key selected · Today: 42 tokens, 2.50 · Daily limit: 1000 tokens, 0.75']);
 assert.strictEqual(element('hosts').children[0].children[0].children[0].title, 'https://openrouter.ai/api/v1 · chat, embed · registered by operator · API: openai-compatible · Key: openrouter');
 assert.equal(element('hosts').children[0].children[0].children[2].title, 'credential:unknown:openrouter', 'the raw reason sits on the caption\'s own title');
 // A host with no credential, no spend yet and no ceiling reads as a stated
 // absence, never a blank.
 assert.deepStrictEqual(hostText(element('hosts').children[1])[2], 'Available · No key selected · Today: nothing spent · No daily limit');
 assert(!hostText(element('hosts').children[0]).join(' ').includes('https://'),'a long server address cannot widen the visible row');
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
 assert.strictEqual(text('host-add-status'), 'Added ollama.', 'the Add sentence sits next to the Add button');
 assert.strictEqual(element('host-add-status').title, 'Configuration revision hosts-v1:b');
 assert.strictEqual(element('host-add-status').children[0].children[0].textContent, 'Technical details');
 assert(element('host-add-status').children[0].children[1].textContent.includes('hosts-v1:b'));
 assert.strictEqual(calls.filter(c => c.path === '/inference/hosts' && !c.body).length, hostsGetCallsBeforeAdd + 1, 'a successful add read the hosts list again itself, with no click');
 element('hosts').children[0].children[1].children[0].onclick(); await settle();
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
 assert.strictEqual(text('gateway-status'), '', 'an off gateway adds no sentence to the switch\'s own Off');
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
 assert.strictEqual(text('keys-status'), "OpenAbstractions is running but doesn't provide inference on this account.");
 const keysDetails = element('keys-status').children[0];
 assert.strictEqual(keysDetails.className, 'oa-error-details');
 assert.strictEqual(keysDetails.children[1].textContent, 'service resolution: unavailable: abstraction.inference/keys@1 could not be resolved at the installed runtime at \\\\.\\pipe\\openabstractions-user-S-1-5-21-1001');

 // task 2026-09-23, seventh first-time visitor, finding 4: Add host with a
 // blank required field used to show no result at all.
 element('host-name').dispatch('invalid');
 assert.strictEqual(text('host-add-status'), 'Name is required to add a model server.', 'a blank required field is named, the Issue key pattern');


 // Live reread (Panel rule, S8 review): click Remove, the server state
 // changes elsewhere, the next tick rereads; the click's sentence and the
 // surviving row's element stay.
 const hostA={Entry:{Name:'alpha',Hosted:false,Kind:'k',Base:'http://127.0.0.1:1',Profiles:['chat'],DeclaredBy:'x'},Up:true,Why:''};
 const hostB={Entry:{Name:'beta',Hosted:false,Kind:'k',Base:'http://127.0.0.1:2',Profiles:['chat'],DeclaredBy:'x'},Up:true,Why:''};
 replies['/inference/hosts'].push({Outcome:'page',Revision:'live-1',Hosts:[hostA,hostB]});
 await context.serversRefresh.hosts(); await settle();
 const betaRow=element('hosts').children[1];
 await require('./panel_fake_dom_test_helper.js').assertLiveUpdateKeepsRow({
  click: async () => { replies['/inference/hosts'].push({Outcome:'conflict',Revision:'',Reason:''}); element('hosts').children[0].children[1].children[0].onclick(); await settle(); },
  change: () => { replies['/inference/hosts'].push({Outcome:'page',Revision:'live-2',Hosts:[hostA,{...hostB,Up:false}]}); },
  data: () => replies['/inference/hosts'],
  tick: async () => { await context.serversRefresh.hosts(); await settle(); },
  row: () => element('hosts').children[1], status: () => element('hosts-status'), label: 'Providers, model servers',
 });
 assert.match(hostText(betaRow)[2],/^Not reachable/,'the row updated in place');
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
// The established setup's row (setup_step.go): setupStepScript is
// setupStepRead followed by its own click handler.
function goText(file, start) {
 const source = fs.readFileSync(__dirname + '/' + file, 'utf8'), at = source.indexOf(start);
 assert(at >= 0, start + ' not found in ' + file);
 return source.slice(at + start.length, source.indexOf('`', at + start.length));
}
const stepScript = goText('setup_step.go', 'const setupStepRead = `') + goText('setup_step.go', 'const setupStepScript = setupStepRead + `');
assert(fs.readFileSync(__dirname + '/inference_page.go', 'utf8').includes('inferenceScript + `<script>` + setupStepScript + `</script>` + libraryScript + selectionPolicyScript'), 'the scripts run in this order');
const pageScripts = [goScript('inference_page.go', 'inferenceScript'), stepScript, goScript('models_library_page.go', 'libraryScript'), goScript('selection_page.go', 'selectionPolicyScript')];
function fakeDOM() {
 const fields = new Map(), created = [];
 function mk(tag) {
  // children is an HTMLCollection stand-in, as in a browser: a page script
  // that calls an Array method on it fails here too.
  const n = {tagName: String(tag || '').toUpperCase(), value: '', textContent: '', className: '', title: '', disabled: false, hidden: false, dataset: {}, attrs: {}, kids: [],
   get children() { return new FakeHTMLCollection(this.kids) },
   replaceChildren(...v) { this.kids = [...v] }, append(...v) { this.kids.push(...v) }, setAttribute(k, v) { this.attrs[k] = v }, removeAttribute(k) { delete this.attrs[k] },
   focus() { this.focused = true }};
  return n;
 }
 function walk(n, visit) { visit(n); for (const c of n.children || []) if (c && typeof c === 'object') walk(c, visit) }
 function find(id) { let hit = null; for (const root of fields.values()) walk(root, n => { if (!hit && n.id === id) hit = n }); if (!hit) for (const n of created) if (n.id === id) hit = n; return hit }
 function element(id) { if (fields.has(id)) return fields.get(id); const hit = find(id); if (hit) return hit; const n = mk('div'); n.id = id; fields.set(id, n); return n }
 const createElement = tag => { const n = mk(tag); created.push(n); return n };
 return {fields, element, createElement, walk};
}
// kids is a node's children as an Array, for the test's own reads.
const kids = n => Array.from(n.children || []);
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
  {Declaration: {Name: 'native-runtime'}, Role: 'provider'}, {Declaration: {Name: 'modelhost', Contracts: ['abstraction.inference/chat@1']}, Role: 'provider'}, {Declaration: {Name: 'comfy-image-1828', Contracts: ['abstraction.inference/image@1']}, Role: 'provider'}, {Declaration: {Name: 'oa-setup-chat-1', Contracts: ['abstraction.storage/inventory-source@1']}, Role: 'provider'}, {Declaration: {Name: 'some-app'}, Role: 'application'}]}};
 const policies = new Map();
 let revision = 0;
 policies.set('C:\\Apps\\chat.exe|oa-chat-default', {rev: 'r0', candidates: [
  {Model: 'qwen/tiny', Placement: 'local', Credential: '', Provider: 'native-runtime'},
  {Model: 'maximum', Placement: 'local', Credential: '', Provider: ''},
  {Model: 'old-model', Placement: 'local', Credential: '', Provider: ''}]});
 let localReply = {available: true, plan_id: 'plan-1', feature: {installed: true}, engine: {staged: true},
  root: {path: 'C:\\Models\\gguf', store: 'store-1', models: [
   {id: 'm1', name: 'Qwen3-8B-Q4.gguf', family: 'qwen3', quant: 'Q4_K_M', size: 5 * GB, hosted: false},
   {id: 'm2', name: 'phi4.gguf', family: 'phi4', size: 2 * GB, hosted: true}]}};
 // /inference/library (models_library.go): a chat model OA's model host
 // runs (the hosted m2 copy), a chat model another engine serves, a picture
 // model, an embedding model with no known file, a picture file no engine
 // runs, and a GGUF in OA's model folder not hosted yet (m1).
 const oaFolder = {name: 'OpenAbstractions › models', path: 'C:\\Users\\p\\AppData\\Local\\Programs\\OpenAbstractions\\models'}, comfyFolder = {name: 'ComfyUI › models', path: 'C:\\ComfyUI\\models'};
 const libraryReply = {files_known: true, catalog_known: true, reading: false, models: [
  {key: 'qwen3.6-demo', name: 'qwen3.6-demo', names: ['qwen3.6-demo', 'qwen/tiny'], uses: ['chat'], ready: true, running: true, engines: ['modelhost'], size: 21 * GB, places: [oaFolder], objects: [{store: 'store-1', id: 'm2', size: 21 * GB, hosted: true}],
   context: {automatic: {context: 32768, slots: 2}, sizes: [{context: 32768, slots: 2}, {context: 65536, slots: 1}]}},
  {key: 'maximum.gguf', name: 'maximum.gguf', names: ['maximum'], uses: ['chat'], ready: true, running: false, engines: ['native-runtime'], size: 4 * GB, places: [{name: '.lmstudio › models', path: 'C:\\Users\\p\\.lmstudio\\models'}], objects: [{store: 'lms', id: 'o-max', size: 4 * GB}]},
  {key: 'flux2-klein-4b', name: 'diffusion_models/flux2-klein-4b.safetensors', names: ['flux2-klein-4b'], uses: ['image'], ready: true, running: false, engines: ['thenoise'], size: 8 * GB, places: [comfyFolder], objects: [{store: 'comfy', id: 'o-flux2', size: 8 * GB}]},
  {key: 'nomic-embed', name: 'nomic-embed', names: ['nomic-embed'], uses: ['embed'], ready: true, running: false, engines: ['local-http'], size: 0, places: [], objects: []},
  {key: 'flux1-dev', name: 'flux1-dev.safetensors', names: ['flux1-dev.safetensors'], uses: [], ready: false, running: false, engines: [], size: 23.8 * GB, places: [comfyFolder, {name: 'another computer', path: 'mac'}], objects: [{store: 'comfy', id: 'o-flux1', size: 23.8 * GB}, {store: 'peer', id: 'o-flux1b', size: 23.8 * GB}]},
  {key: 'qwen3', name: 'Qwen3-8B-Q4.gguf', names: ['Qwen3-8B-Q4.gguf'], uses: [], ready: false, running: false, engines: [], size: 5 * GB, places: [oaFolder], objects: [{store: 'store-1', id: 'm1', size: 5 * GB}]}],
  running: [{key: 'qwen3.6-demo', name: 'qwen3.6-demo', engine: 'modelhost', uses: ['chat'], memory: 21.4 * GB}]};
 const calls = [];
 let holdRead = null, unhostReply = null;
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
    if(path==='/inference/modalities'){if(options.modalitiesFail)return{ok:false,text:async()=>options.modalitiesFail};return ok({modalities:options.modalities||['chat','image','embed','transcription','speech','live','video','example/custom@1']})}
    if (path.startsWith('/inference/models?profile=')) {
     const use = decodeURIComponent(path.split('=')[1]);
     if (options.offersFail) return {ok: false, text: async () => options.offersFail};
     return ok({Offers: offers[use] || [], Picks: {'flux2-klein-4b':'would-load'}});
    }
    if (path === '/inference/hosts') { if(options.hostsGate)await options.hostsGate;return ok(options.hostsOutcome?{Outcome:options.hostsOutcome}:hosts) }
    if (path === '/registry/view') { if (options.viewFail) return {ok: false, text: async () => options.viewFail}; return ok(options.view || view) }
    if (path === '/inference/folder') return ok(options.picker || {available:false});
    if (path.startsWith('/inference/audit')) throw Error('the Models page reads no model calls');
    if (path === '/setup/modelhost/chat') return ok(JSON.parse(JSON.stringify(options.local || localReply)));
    if (path === '/setup/modelhost/root/preview') return ok({root: {path: body.path}, approval: 'Read ' + body.path + ' in place', approval_digest: 'pd'});
    if (path === '/setup/modelhost/root') return ok({registered: true});
    if (path === '/settings/typed?action=user') return ok({snapshot: {revision: 's1', entries: [{key: 'model_context', value: {text_map: {'qwen3.6-demo': '65536'}}}]}});
    if (path === '/settings/typed') return ok({outcome: 'applied'});
    if (path === '/inference/library') { if (options.libraryFail) return {ok: false, text: async () => options.libraryFail}; return ok(JSON.parse(JSON.stringify(options.library || libraryReply))) }
    if (path === '/setup/step') return ok(body ? {message: 'Setup opened its question.'} : {label: 'Coding model and pictures for apps', model: 'Not set up yet', sign_in: 'Starts when an app is set up', chat_default: 'Apps choose their own model', action: 'Set up'});
    if (path === '/setup/modelhost/unhost') { if (unhostReply) return unhostReply(); return ok({unhosted: true}) }
    assert.strictEqual(path, '/registry/selection');
    if(body.account_default&&body.edit==='read'&&options.defaultReadOutcome)return ok({read:{Outcome:options.defaultReadOutcome}});
    // The owner's effective read (serve, INF-R1): the same-named account
    // default, then a generic name's profile default (the one Setup writes).
    if (body.edit === 'effective') {
     const profile = /^oa-([a-z]+)-default$/.exec(body.name), found = policies.get('account|' + body.name) || (profile && policies.get('account|' + profile[1]));
     return ok({read: found ? {Outcome: 'found', Revision: found.rev, Policy: {Name: body.name, Candidates: JSON.parse(JSON.stringify(found.candidates))}} : {Outcome: 'unknown'}});
    }
    const k = (body.account_default?'account':body.target_program) + '|' + body.name, stored = policies.get(k);
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
   section('models-local', status('models-local-status'), dom.element('models-local-rows')),
   section('models-running', status('models-running-status'), dom.element('models-running-rows')),
   section('selection-section', status('selection-status'), dom.element('selection-defaults'), dom.element('selection-apps'), dom.element('selection-fallback-rows'),
    {className: 'oa-row', children: [dom.element('setup-step-label'), dom.element('setup-step-caption'), {tagName: 'BUTTON', textContent: dom.element('setup-step-action').textContent}, status('setup-step-status')]},
    {className: 'oa-row', children: [dom.element('selection-fallback-model'), {tagName: 'BUTTON', textContent: 'Add'}, status('selection-fallback-status')]}),
   section('add-model', status('models-engine-status'), dom.element('models-engine-rows'), status('add-model-status'), dom.element('add-model-rows'), dom.element('add-model-tech')),
  ], 'Models', { naming: true });
 }
 const dom = load();
 assert.strictEqual(dom.context.libraryRunsOn({engines: ['hf', 'pytorchd', 'unknown-provider']}), 'Runs on Diffusers');
 assert.strictEqual(dom.context.libraryRunsOn({runs_on: ['unknown-provider']}), 'Runs on Unknown provider');
 const {element} = dom;
 const text = id => element(id).textContent;
 const appStatus = i => element('selection-apps').children[i].children[2].textContent;
 const tick = async () => { dom.context.modelsLive(); await settle() };
 const rowText = row => kids(row).map(c => c.children ? kids(c).filter(x => !x.hidden).map(x => x.textContent).join(' | ') : c.textContent);
 const lastPut = () => {const body=calls.filter(c => c.body && c.body.edit === 'put').at(-1).body;return {...body,candidates:nativeCandidates(body.candidates)}};
 await settle();

 // Arrival: ready, the chat use, and the models on this computer by use,
 // each by its friendly name with its size, folder and state.
 const pageGo = fs.readFileSync(__dirname + '/inference_page.go', 'utf8');
 assert(pageGo.includes('var inferencePage = `<div data-ready-owner="page" hidden></div>`'), 'the served markup declares that the page owns its ready signal');
 assert(pageGo.includes('libraryReadingStatus("models-local-status", "Reading model folders…")'), 'On this computer arrives saying it reads the model folders, never as an empty list');
 assert.strictEqual(dom.root.dataset.ready, '1', 'the page says it is ready once its first reads finish');
 assert.strictEqual(element('models-profile').value, 'chat');
 const groups = d => kids(d.element('models-local-rows')).map(g => [g.children[0].textContent, kids(g.children[1]).map(r => rowText(r)[0])]);
 assert.deepStrictEqual(groups(dom), [
  // A model reads in words and names its engine by upstream name
  // (architect ruling 2026-11-09, second block).
  // Layout ruling 2026-11-10: size, engine, state; the folders stay on the
  // caption's title and under Technical details.
  ['Chat', ['Qwen3.6 demo | 21 GB · Runs on llama.cpp · Loaded now', 'Maximum | 4 GB · Ready']],
  ['Pictures', ['FLUX.2 klein 4B | 8 GB · Runs on TheNoise · Ready']],
  ['Other uses', ['Nomic embed | Ready']],
  ['Not set up yet', ['FLUX.1 dev | 23.8 GB · No engine runs it yet', 'Qwen3-8B-Q4 | 5 GB · No engine runs it yet']]]);
 // A row is found by its label or by the model's own name.
 const modelRow = (d, name) => kids(d.element('models-local-rows')).flatMap(g => kids(g.children[1])).find(r => r.parts.label.textContent === name || (r.model && (r.model.name === name || r.model.key === name || (r.model.names || []).includes(name))));
 const actions = (d, name) => kids(modelRow(d, name).parts.control).filter(b => b.tagName === 'BUTTON').map(b => b.textContent);
 assert.deepStrictEqual(['qwen3.6-demo', 'maximum', 'flux2-klein-4b', 'nomic-embed', 'flux1-dev', 'Qwen3-8B-Q4'].map(n => actions(dom, n)),
  [['Use for chat', 'Remove'], ['Use for chat'], ['Use for pictures'], [], [], []], 'each row offers what the person can do');
 assert.strictEqual(modelRow(dom, 'qwen3.6-demo').parts.caption.title, oaFolderTitle(), 'the folder path sits on the title, never the text');
 assert.match(text('models-local-guide'), /^Qwen3\.6 demo\n  /, 'the engine guide sits behind the group Technical details');
 assert(text('models-local-guide').includes('  in ' + oaFolderTitle()), 'the folders sit behind Technical details too');
 // Context is a chooser in the row's controls, before its buttons.
 const qwenControls = kids(modelRow(dom, 'qwen3.6-demo').parts.control);
 assert.deepStrictEqual(qwenControls.map(c => c.tagName), ['SELECT', 'BUTTON', 'BUTTON']);
 assert.deepStrictEqual(kids(qwenControls[0]).map(o => o.textContent), ['Automatic context', '32K context', '64K context, one chat at a time']);
 assert.strictEqual(qwenControls[0].attrs['aria-label'], 'Context');
 assert.strictEqual(qwenControls[0].value, '65536', 'the saved size shows on arrival');
 assert.strictEqual(qwenControls[0].disabled, false);
 qwenControls[0].value = '32768'; await qwenControls[0].onchange(); await settle();
 assert.deepStrictEqual(calls.filter(c => c.path === '/settings/typed').at(-1).body, {revision: 's1', changes: [{key: 'model_context', set: {kind: 'text_map', text_map: {'qwen3.6-demo': '32768'}}}]});
 assert.strictEqual(modelRow(dom, 'qwen3.6-demo').parts.status.textContent, 'The new size applies to the next conversation.');
 assert.strictEqual(element('models-local-tech').hidden, false);
 function oaFolderTitle() { return 'C:\\Users\\p\\AppData\\Local\\Programs\\OpenAbstractions\\models' }
 assert.strictEqual(text('models-local-status'), '', 'the rows say it; the status line waits for an error');
 assert.deepStrictEqual(kids(element('models-running-rows')).map(r => rowText(r)[0]), ['Qwen3.6 demo | llama.cpp · 21.4 GB of graphics memory'], 'Loaded now names the loaded model, its engine and its graphics memory');
 assert.deepStrictEqual(kids(element('models-engine-rows')).map(r => rowText(r)[0]), ['Cloud | Online service · Up', 'Local http | On this computer · Up', 'llama.cpp | On this computer · Loaded now', 'ComfyUI | On this computer · Starts when an app asks'], 'native inference providers are engines too');
 assert.strictEqual(element('models-providers').href, '/registry?k=test');
 assert.strictEqual(text('setup-step-caption'), 'Not set up yet');
 assert.strictEqual(element('setup-step-action').textContent, 'Set up');
 assert(!fs.readFileSync(__dirname + '/inference_page.go', 'utf8').includes('audit'), 'the model calls are Activity\'s');
 assert(!calls.some(c => c.path.startsWith('/inference/audit')));

 // Each app's chat chooser shows its stored choice on arrival.
 const appRows = kids(element('selection-apps'));
 assert.deepStrictEqual(appRows.map(r => r.children[0].children[0].textContent), ['Chat model for Chat App', 'Chat model for draw'], 'one row per registered app, by title');
 const chatSelect = appRows[0].children[1].children[0], drawSelect = appRows[1].children[1].children[0];
 assert.strictEqual(chatSelect.value, 'qwen3.6-demo', 'a stored alias shows as the offered model it names');
 assert.deepStrictEqual(kids(chatSelect).map(o => o.textContent), ["App's own choice", 'Qwen3.6 demo', 'Reported', 'Maximum', 'Unnamed model']);
 assert.strictEqual(drawSelect.value, '', "an app with no policy shows its own choice");
 assert.strictEqual(element('selection-defaults').children[0].parts.label.textContent,'All apps');
 assert.deepStrictEqual(calls.filter(c => c.path === '/registry/selection'&&!c.body.account_default&&c.body.edit!=='effective').map(c => c.body), [
  {edit: 'read', target_program: 'C:\\Apps\\chat.exe', name: 'oa-chat-default'}, {edit: 'read', target_program: 'C:\\Apps\\draw.exe', name: 'oa-chat-default'}]);
 rules(dom);

 // Fallback order: the first app's later candidates, in order.
 assert.strictEqual(element('selection-fallback-app').value, JSON.stringify(['C:\\Apps\\chat.exe','oa-chat-default']));
 const fallbackLabels = () => kids(element('selection-fallback-rows')).map(r => r.children[0].children[0].textContent);
 assert.deepStrictEqual(fallbackLabels(), ['1. Maximum', '2. Old model']);
 assert.deepStrictEqual(kids(element('selection-fallback-model')).map(o => o.value), ['reported', 'sha256-0123456789abcdef0123456789abcdef'], 'Add offers the models not yet in the order');
 // Move up writes the reordered policy into its own row.
 const movedRow = element('selection-fallback-rows').children[1];
 movedRow.children[1].children[0].onclick(); await settle();
 assert.deepStrictEqual(lastPut().candidates, [
  {model: 'qwen/tiny', placement: 'local', credential: '', provider: 'native-runtime'},
  {model: 'old-model', placement: 'local', credential: '', provider: ''},
  {model: 'maximum', placement: 'local', credential: '', provider: ''}]);
 assert.deepStrictEqual(fallbackLabels(), ['1. Old model', '2. Maximum']);
 assert.strictEqual(element('selection-fallback-rows').children[0], movedRow, 'the moved row is the same row, updated in place');
 assert.strictEqual(movedRow.children[2].textContent, 'Old model moved up.', 'Move up writes into its own row');
 assert.strictEqual(movedRow.children[1].children[0].hidden, true, 'the first fallback has no Move up');
 // The server's state changes elsewhere (another fallback lands at the
 // end); the live reread shows it and keeps the row and its sentence.
 policies.get('C:\\Apps\\chat.exe|oa-chat-default').candidates.push({Model: 'reported', Placement: 'hosted', Credential: 'cloudcred', Provider: ''});
 policies.get('C:\\Apps\\chat.exe|oa-chat-default').rev = 'elsewhere-1';
 await tick();
 assert.deepStrictEqual(fallbackLabels(), ['1. Old model', '2. Maximum', '3. Reported'], 'the reread shows the changed order');
 assert.strictEqual(element('selection-fallback-rows').children[0], movedRow);
 assert.strictEqual(movedRow.children[2].textContent, 'Old model moved up.', 'the sentence survives a reread of changed state');
 // Remove drops one fallback.
 element('selection-fallback-rows').children[2].children[1].children[1].onclick(); await settle();
 assert.deepStrictEqual(fallbackLabels(), ['1. Old model', '2. Maximum']);
 assert.strictEqual(text('selection-fallback-status'), 'Reported is no longer a fallback.');
 // Add takes a hosted model's placement and key from its server.
 element('selection-fallback-model').value = 'reported'; await element('selection-fallback-add').onclick(); await settle();
 assert.deepStrictEqual(lastPut().candidates.at(-1), {model: 'reported', placement: 'hosted', credential: 'cloudcred', provider: ''});
 assert.strictEqual(text('selection-fallback-status'), 'Reported is now a fallback for Chat App.');

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
 assert.strictEqual(appStatus(0), 'Chat App now uses Maximum as its chat model.');
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
 assert.strictEqual(appStatus(0), 'Chat App now uses Maximum as its chat model.', 'the sentence survives a reread of changed state');

 // A new local model pins this machine's provider that offers it.
 drawSelect.value = 'maximum.gguf'; await drawSelect.onchange(); await settle();
 assert.deepStrictEqual(lastPut(), {edit: 'put', target_program: 'C:\\Apps\\draw.exe', name: 'oa-chat-default', revision: '',
  candidates: [{model: 'maximum.gguf', placement: 'local', credential: '', provider: 'native-runtime'}]});
 // A hosted one carries its server's key and no pin.
 drawSelect.value = 'reported'; await drawSelect.onchange(); await settle();
 assert.deepStrictEqual(lastPut().candidates, [{model: 'reported', placement: 'hosted', credential: 'cloudcred', provider: ''}]);
 assert.strictEqual(appStatus(1), 'draw now uses Reported as its chat model.');
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
 assert.strictEqual(appStatus(1), 'draw now uses Qwen3.6 demo as its chat model.');
 release(); await settle();
 assert.strictEqual(drawSelect.value, 'qwen3.6-demo', 'the older read does not replace the saved choice');
 assert.strictEqual(appStatus(1), 'draw now uses Qwen3.6 demo as its chat model.');

 // Another use: pictures read their own policy; a use with no policy name
 // says apps choose their own.
 element('models-profile').value = 'image'; element('models-profile').onchange(); await settle();
 assert.strictEqual(calls.filter(c => c.path === '/registry/selection').at(-1).body.name, 'oa-image-default');
 assert.strictEqual(element('selection-apps').children[0].children[0].children[0].textContent, 'Picture model for Chat App');
 element('models-profile').value = 'speech'; element('models-profile').onchange(); await settle();
 assert.strictEqual(element('selection-apps').children.length, 0, 'a use without per-app rows renders nothing');
 assert.strictEqual(element('selection-fallback').hidden, true, 'no fallback order without a policy');

 // Model rows expose choices and removal; Setup stays in its established row.
 const localRows = () => kids(element('add-model-rows'));
 assert.deepStrictEqual(localRows().map(r => rowText(r)[0]), ['Model folder | gguf folder · 2 models, read in place']);
 const hostRow = modelRow(dom, 'Qwen3-8B-Q4');
 assert.deepStrictEqual(actions(dom, 'Qwen3-8B-Q4'), []);
 const hostedLibrary = JSON.parse(JSON.stringify(libraryReply));
 Object.assign(hostedLibrary.models[5], {ready: true, uses: ['chat'], engines: ['modelhost']});
 hostedLibrary.models[5].objects[0].hosted = true;
 libraryReply.models = hostedLibrary.models;
 await tick();
 assert.strictEqual(modelRow(dom, 'Qwen3-8B-Q4'), hostRow, 'the row moved to Chat is the same row');
 assert.deepStrictEqual(actions(dom, 'Qwen3-8B-Q4'), ['Use for chat', 'Remove']);
 assert(!calls.some(c => c.path === '/setup/modelhost/host'), 'model rows never start Setup');
 // Choose folder… opens the folder row; Check folder previews, Confirm adds.
 await localRows()[0].children[1].children[0].onclick();
 assert.strictEqual(element('add-model-folder').hidden, false);
 element('add-model-path').value = 'D:\\More models';
 await element('add-model-check').onclick();
 assert.strictEqual(text('add-model-folder-status'), 'Confirm to read More models in place.');
 await element('add-model-check').onclick(); await settle();
 assert.deepStrictEqual(calls.filter(c => c.path === '/setup/modelhost/root').at(-1).body, {plan_id: 'plan-1', path: 'D:\\More models', approval_digest: 'pd', confirm: true});
 assert.strictEqual(text('add-model-folder-status'), 'More models is added; its models stay where they are.');
 assert.strictEqual(element('add-model-folder').hidden, true);
 assert.strictEqual(element('add-model-check').textContent, 'Check folder');
 const picked=load({picker:{available:true,path:'D:\\Chosen models'}});await settle();
 await picked.element('add-model-rows').children[0].children[1].children[0].onclick();
 assert.strictEqual(picked.element('add-model-path').value,'D:\\Chosen models');
 assert.strictEqual(picked.element('add-model-folder').hidden,false);
 assert(!calls.slice(calls.findLastIndex(c=>c.path==='/inference/folder')).some(c=>c.path==='/setup/modelhost/root'),'selection does not register the folder');
 const cancelled=load({picker:{available:true,path:''}});await settle();
 await cancelled.element('add-model-rows').children[0].children[1].children[0].onclick();
 assert.strictEqual(cancelled.element('add-model-folder').hidden,true,'cancel leaves confirmation closed');
 rules(dom);

 // Unavailable local models, unreadable offers and refused model folders
 // each read as one sentence.
 const other = load({local: {available: false, unavailable: 'isolated', reason: 'ModelHost Setup requires the installed runtime\'s user state'}, offersFail: 'router forbidden: abstraction.router/inventory.read',
  library: {files_known: false, files_refused: true, files_error: 'storage inventory forbidden', catalog_known: false, catalog_refused: true, catalog_error: 'router forbidden: abstraction.router/inventory.read', models: [], running: []}});
 await settle();
 assert.strictEqual(other.element('add-model-status').textContent, '');
 assert.strictEqual(rowText(other.element('add-model-rows').children[0])[0], "Local models | Not available in this test home.", 'one row says it');
 assert.strictEqual(other.element('add-model-tech').hidden, false, 'its reason sits under the group\'s Technical details');
 assert.strictEqual(other.element('add-model-reason').textContent, 'ModelHost Setup requires the installed runtime\'s user state');
 rules(other);
 // A platform refusal, an isolated home and a read error each have their
 // own caption; the reason stays under Technical details.
 for (const [unavailable, caption] of [['platform', 'Not supported on this computer.'], ['isolated', 'Not available in this test home.'], ['', 'Cannot be read now.']]) {
  const page = load({local: {available: false, unavailable, reason: 'reason ' + unavailable}}); await settle();
  assert.strictEqual(rowText(page.element('add-model-rows').children[0])[0], 'Local models | ' + caption, unavailable || 'read error');
  assert.strictEqual(page.element('add-model-reason').textContent, 'reason ' + unavailable);
  assert.strictEqual(page.element('add-model-tech').hidden, false);
 }
 // A native engine's caption follows its provider readiness: holding a
 // model, ready and idle, or unable to start with the reason behind
 // Technical details.
 const readinessView = {providers: {Outcome: 'page', Declarations: [
  {Declaration: {Name: 'modelhost', Contracts: ['abstraction.inference/chat@1']}, Role: 'provider', Readiness: 'ready'},
  {Declaration: {Name: 'comfy-image-1828', Contracts: ['abstraction.inference/image@1']}, Role: 'provider', Readiness: 'idle'},
  {Declaration: {Name: 'broken-engine', Contracts: ['abstraction.inference/chat@1']}, Role: 'provider', Readiness: 'refused', Why: 'program C:\missing.exe not found'}]}};
 const ready = load({view: readinessView}); await settle();
 const engineRows = kids(ready.element('models-engine-rows')), byName = name => engineRows.find(r => r.parts.label.title === name);
 assert.strictEqual(byName('modelhost').parts.caption.textContent, 'On this computer · Loaded now');
 assert.strictEqual(byName('comfy-image-1828').parts.caption.textContent, 'On this computer · Starts when an app asks');
 assert.strictEqual(byName('broken-engine').parts.caption.textContent, 'On this computer · Cannot start');
 assert.strictEqual(byName('broken-engine').parts.details.hidden, false);
 assert.strictEqual(byName('broken-engine').parts.details.children[1].textContent, 'program C:\missing.exe not found');
 assert(!byName('comfy-image-1828').parts.details, 'a startable engine has no Technical details');
 // A failed /registry/view read is one row with its reason, never "No
 // engines yet." and never a silent drop.
 for (const options of [{viewFail: 'registry forbidden'}, {viewFail: 'registry forbidden', hostsOutcome: 'unavailable'}, {view: {providers: {}, providers_error: 'registry forbidden'}}]) {
  const failedView = load(options); await settle();
  const rows = kids(failedView.element('models-engine-rows')), last = rows.at(-1);
  assert(!rows.some(r => r.textContent === 'No engines yet.'), 'no empty sentence');
  assert.strictEqual(last.parts.label.textContent, 'Engines cannot be read now.');
  assert.strictEqual(last.parts.details.children[1].textContent, 'registry forbidden');
 }
 // Refused: the reason in one sentence, the owner's own words behind
 // Technical details, and no row claims anything.
 assert.strictEqual(other.element('models-local-status').textContent, 'This Panel may not read the model folders.');
 assert.strictEqual(other.element('models-local-status').attrs['data-state'], 'error');
 assert.strictEqual(other.element('models-local-status').children[0].children[1].textContent, 'storage inventory forbidden');
 assert.strictEqual(other.element('models-running-status').textContent, 'This Panel may not see which models are loaded.');
 assert.strictEqual(other.element('models-local-rows').children.length, 0, 'a refusal is no empty list');
 assert.strictEqual(other.element('models-running-rows').children.length, 0);
 assert.strictEqual(other.element('selection-status').attrs['data-state'], 'error');
 assert.strictEqual(other.root.dataset.ready, '1', 'a page whose reads failed is still ready');
 const engine = load({local: {available: true, plan_id: 'p', feature: {installed: true}, engine: {staged: false, bundled: {available: true, approval: 'Stage from C:\\x (SHA-256 0123456789abcdef0123)', approval_digest: 'ad'}}}});
 await settle();
 const engineRow = engine.element('add-model-rows').children[0];
 assert.strictEqual(rowText(engineRow)[0], 'llama.cpp | Ready to prepare.', 'the engine reads by its upstream name');
 assert.strictEqual(engineRow.children[1].children[1].href, '/setup?k=test#setup-modelhost', 'the pinned ZIP recovery stays in Setup');
 assert.strictEqual(engineRow.children[1].children[1].textContent, 'Open Catalog');
 rules(engine);
 // Two declared policies for one native program keep independent CAS state
 // and fallback selection; both use the existing authority endpoint.
 apps.entries=[{program:'C:\\Apps\\chat.exe',name:'editor',choosers_status:'declared',choosers:{Entries:[{Name:'first',Modality:'chat'},{Name:'second',Modality:'chat'}]},application:{Descriptor:{Title:'Editor'}}}];
 policies.set('C:\\Apps\\chat.exe|first',{rev:'first-r1',candidates:[{Model:'qwen/tiny',Placement:'local',Provider:'native-runtime'},{Model:'maximum',Placement:'local'}]});
 policies.set('C:\\Apps\\chat.exe|second',{rev:'second-r1',candidates:[{Model:'reported',Placement:'hosted',Credential:'cloudcred'},{Model:'old-model',Placement:'local'}]});
 const named=load();await settle();const namedRows=named.element('selection-apps').children;
 assert.strictEqual(namedRows.length,2);assert.strictEqual(namedRows[0].parts.select.value,'qwen3.6-demo');assert.strictEqual(namedRows[1].parts.select.value,'reported');
 namedRows[0].parts.select.value='maximum.gguf';await namedRows[0].parts.select.onchange();await settle();
 assert.strictEqual(lastPut().name,'first');assert.strictEqual(lastPut().revision,'first-r1');assert.strictEqual(policies.get('C:\\Apps\\chat.exe|second').rev,'second-r1');
 named.element('selection-fallback-app').value=JSON.stringify(['C:\\Apps\\chat.exe','second']);await named.element('selection-fallback-app').onchange();await settle();
 assert.strictEqual(named.element('selection-fallback-rows').children[0].parts.label.textContent,'1. Old model');
 // Add begins for second, then the person switches to first while its
 // candidate preparation yields. No list from second may be written to first.
 named.element('selection-fallback-model').value='maximum.gguf';const beforeSwitch=calls.filter(c=>c.body&&c.body.edit==='put').length;
 const pendingAdd=named.element('selection-fallback-add').onclick();named.element('selection-fallback-app').value=JSON.stringify(['C:\\Apps\\chat.exe','first']);named.element('selection-fallback-app').onchange();await pendingAdd;await settle();assert.strictEqual(calls.filter(c=>c.body&&c.body.edit==='put').length,beforeSwitch,'changed fallback selection cannot receive another policy list');
 named.element('selection-fallback-app').value=JSON.stringify(['C:\\Apps\\chat.exe','second']);await named.element('selection-fallback-app').onchange();await settle();
 named.element('selection-fallback-rows').children[0].parts.remove.onclick();await settle();assert.strictEqual(lastPut().name,'second');assert.strictEqual(lastPut().revision,'second-r1');
 // Candidate preparation and policy refresh cannot rebase an old list onto
 // a newer CAS revision for the same named policy.
 let releaseHosts;const hostsGate=new Promise(r=>releaseHosts=r),revisionDOM=load({hostsGate});await settle();
 revisionDOM.element('selection-fallback-app').value=JSON.stringify(['C:\\Apps\\chat.exe','second']);revisionDOM.element('selection-fallback-app').onchange();await settle();
 const originalRevision=policies.get('C:\\Apps\\chat.exe|second').rev;
 revisionDOM.element('selection-fallback-model').value='maximum.gguf';const pendingRevisionAdd=revisionDOM.element('selection-fallback-add').onclick();await settle();
 policies.set('C:\\Apps\\chat.exe|second',{rev:'newer-concurrent',candidates:[{Model:'reported',Placement:'hosted',Credential:'cloudcred'},{Model:'newer-model',Placement:'local'}]});
 await revisionDOM.context.oaModelsHooks[0](false);await settle();releaseHosts();await pendingRevisionAdd;await settle();
 assert.strictEqual(lastPut().name,'second');assert.strictEqual(lastPut().revision,originalRevision,'prepared old list keeps its original CAS revision');assert.strictEqual(policies.get('C:\\Apps\\chat.exe|second').rev,'newer-concurrent','conflict preserves concurrent list');
 // Account defaults stay distinct from raw app overrides and their revisions.
 apps.entries=[{program:'C:\\Apps\\chat.exe',name:'chat-app',application:{Descriptor:{Title:'Chat App'}}}];
 policies.delete('C:\\Apps\\chat.exe|oa-chat-default');
 policies.set('account|oa-chat-default',{rev:'account-r1',candidates:[{Model:'qwen/tiny',Placement:'local',Provider:'native-runtime'},{Model:'old-model',Placement:'local'}]});
 const inherited=load();await settle();
 const inheritedRow=inherited.element('selection-apps').children[0],defaultRow=inherited.element('selection-defaults').children[0];
 assert.strictEqual(inheritedRow.parts.select.value,'','inheritance keeps the raw program policy absent');
 assert(inheritedRow.parts.select.children[0].textContent.startsWith('Use account default'));
 assert(inheritedRow.parts.caption.textContent.includes('Follows the account default'));
 assert.strictEqual(defaultRow.parts.select.value,'qwen3.6-demo');
 inheritedRow.parts.select.value='reported';await inheritedRow.parts.select.onchange();await settle();
 assert.strictEqual(policies.get('account|oa-chat-default').rev,'account-r1','app override never edits account default');
 assert.strictEqual(inheritedRow.parts.caption.textContent,'App override');
 const overrideRevision=policies.get('C:\\Apps\\chat.exe|oa-chat-default').rev;
 inheritedRow.parts.select.value='';await inheritedRow.parts.select.onchange();await settle();
 assert(!policies.has('C:\\Apps\\chat.exe|oa-chat-default'));
 assert.strictEqual(calls.filter(c=>c.body&&c.body.edit==='delete'&&!c.body.account_default).at(-1).body.revision,overrideRevision);
 assert(inheritedRow.parts.select.children[0].textContent.startsWith('Use account default'),'deletion reveals the default');
 assert(inherited.element(inheritedRow.parts.status.id).textContent.includes('now follows the account default'));
 defaultRow.parts.select.value='maximum.gguf';await defaultRow.parts.select.onchange();await settle();
 const defaultPut=calls.filter(c=>c.body&&c.body.account_default&&c.body.edit==='put').at(-1).body;
 assert.strictEqual(defaultPut.revision,'account-r1');assert.strictEqual(defaultPut.target_program,undefined,'account scope has no program sentinel');
 assert.strictEqual(policies.get('account|oa-chat-default').candidates[1].Model,'old-model','default edit preserves its existing fallback order');
 defaultRow.parts.select.value='';await defaultRow.parts.select.onchange();await settle();
 assert(!policies.has('account|oa-chat-default'));assert.strictEqual(inheritedRow.parts.select.children[0].textContent,"App's own choice");
 const uncertain=load({defaultReadOutcome:'unavailable'});await settle();
 assert(uncertain.element('selection-apps').children[0].parts.select.disabled,'default uncertainty cannot imply inheritance or app choice');
 assert.strictEqual(uncertain.element('selection-apps').children[0].parts.select.children[0].textContent,'Account default unavailable');
 policies.set('C:\\Apps\\chat.exe|oa-chat-default',{rev:'explicit',candidates:[{Model:'qwen/tiny',Placement:'local'}]});
 const override=load({defaultReadOutcome:'forbidden'});await settle();
 assert(!override.element('selection-apps').children[0].parts.select.disabled,'proven app override remains readable despite refused account default');
 assert.strictEqual(override.element('selection-apps').children[0].parts.caption.textContent,'App override');
 rules(inherited);rules(uncertain);rules(override);
 // The catalogue supplies custom uses, preserves selection across refreshes,
 // and discards stale choices on failure instead of substituting seed options.
 const catalogueOptions={modalities:['chat','example/custom@1']},catalogue=load(catalogueOptions);await settle();
 assert.deepStrictEqual(kids(catalogue.element('models-profile')).map(o=>o.textContent),['Chat','example/custom@1']);
 catalogue.element('models-profile').value='example/custom@1';catalogue.element('models-profile').onchange();await settle();
 assert.strictEqual(catalogue.element('selection-defaults').children.length,0,'no invented conventional policy for custom uses');
 catalogueOptions.modalities=['chat','image','example/custom@1'];await catalogue.context.modelsLive();await settle();
 assert.strictEqual(catalogue.element('models-profile').value,'example/custom@1');
 catalogueOptions.modalitiesFail='router uses: unavailable';await catalogue.context.modelsLive();await settle();
 assert(catalogue.element('models-profile').disabled);assert.strictEqual(catalogue.element('models-profile').children.length,0);assert.strictEqual(catalogue.element('selection-defaults').children.length,0);
 // Use shows no empty chooser: its row is hidden while the uses cannot be
 // read and returns with them (R19b render check, 2026-11-09).
 assert.strictEqual(catalogue.element('models-profile-row').hidden,true,'an unread Use row is hidden');
 delete catalogueOptions.modalitiesFail;await catalogue.context.modelsLive();await settle();
 assert.strictEqual(catalogue.element('models-profile-row').hidden,false,'the Use row returns with its uses');assert(kids(catalogue.element('models-profile')).length>0);
 await libraryStates();
 console.log('PASS models page');

 // On this computer's states and actions on fresh pages: reading, empty
 // with an explanation, a failed read, Use for chat, Use for pictures, Set
 // up through Setup, Remove from OA, and the Settings row's link.
 async function libraryStates() {
  policies.clear();
  const fresh = () => JSON.parse(JSON.stringify({...libraryReply, models: libraryReply.models.slice(0, 6)}));
  // Reading: the first read of the model folders is still running.
  const reading = load({library: {reading: true, files_known: false, catalog_known: true, models: [], running: []}}); await settle();
  assert.strictEqual(reading.element('models-local-status').textContent, 'Reading model folders…');
  assert.strictEqual(reading.element('models-local-status').attrs['data-state'], 'loading');
  assert.strictEqual(reading.element('models-local-rows').children.length, 0, 'no empty-list sentence while the folders are read');
  assert.strictEqual(reading.root.dataset.ready, '1', 'a page still reading folders is ready');
  // Empty: both owners answered; the sentence says what to do.
  const empty = load({library: {reading: false, files_known: true, catalog_known: true, models: [], running: []}}); await settle();
  assert.deepStrictEqual(kids(empty.element('models-local-rows')).map(c => [c.className, c.textContent]),
   [['oa-empty', 'No model files are in the folders OpenAbstractions reads. Add a folder under Engines, or choose Set up under Default models.']]);
  assert.deepStrictEqual(kids(empty.element('models-running-rows')).map(r => rowText(r)[0]), ['No model loaded | Apps load one when they ask.'], 'Loaded now shows one row when nothing is loaded');
  assert.strictEqual(empty.element('models-local-status').textContent, '');
  rules(empty);
  // A failed read says so in one sentence.
  const failed = load({libraryFail: 'service resolution: unavailable: abstraction.router/router@1'}); await settle();
  assert.match(failed.element('models-local-status').textContent, /doesn't provide|isn't running/);
  assert.strictEqual(failed.element('models-local-status').attrs['data-state'], 'error');
  // Setup's chat model (the account default named chat) is what apps with
  // no choice of their own follow; its row says so.
  policies.set('account|chat', {rev: 'setup-1', candidates: [{Model: 'qwen3.6-demo', Placement: 'local', Credential: '', Provider: ''}]});
  const afterSetup = load({library: fresh()}); await settle();
  assert.strictEqual(modelRow(afterSetup, 'qwen3.6-demo').parts.caption.textContent, '21 GB · Runs on llama.cpp · Chat model for your apps');
  assert.deepStrictEqual(actions(afterSetup, 'qwen3.6-demo'), ['Remove'], "Setup's model offers no Use for chat");
  // Default models shows the same model: the account default chooser
  // and each app that follows it name Setup's model, never "none".
  const setupDefault = afterSetup.element('selection-defaults').children[0];
  assert.strictEqual(setupDefault.parts.select.value, '', 'no account default of its own is stored');
  assert.strictEqual(setupDefault.parts.select.children[0].textContent, 'Setup’s model — Qwen3.6 demo');
  assert.strictEqual(setupDefault.parts.caption.textContent, 'Setup chose this model. Choose another to change it.');
  const followingApp = afterSetup.element('selection-apps').children[0];
  assert.strictEqual(followingApp.parts.select.children[0].textContent, 'Use Setup’s model — Qwen3.6 demo');
  assert.strictEqual(followingApp.parts.caption.textContent, 'Follows the chat model Setup chose.');
  rules(afterSetup);
  assert.deepStrictEqual(calls.filter(c => c.body && c.body.edit === 'effective').map(c => c.body).slice(-2).sort((a, b) => a.name < b.name ? -1 : 1), [{edit: 'effective', name: 'oa-chat-default'}, {edit: 'effective', name: 'oa-image-default'}]);
  policies.delete('account|chat');
  // Found: Use for chat makes the model the account default apps follow,
  // with the same write as the chooser; the row then says so.
  const page = load({library: fresh()}); await settle();
  const maximum = modelRow(page, 'maximum');
  await maximum.parts.control.children[0].onclick(); await settle();
  assert.deepStrictEqual(lastPut(), {account_default: true, edit: 'put', name: 'oa-chat-default', revision: '',
   candidates: [{model: 'maximum.gguf', placement: 'local', credential: '', provider: 'native-runtime'}]});
  assert.strictEqual(maximum.parts.status.textContent, 'Maximum is now the chat model for your apps.');
  assert.strictEqual(maximum.parts.caption.textContent, '4 GB · Chat model for your apps');
  assert.deepStrictEqual(actions(page, 'maximum'), [], 'the chat default offers no Use for chat');
  assert.deepStrictEqual(actions(page, 'qwen3.6-demo'), ['Use for chat', 'Remove']);
  // Use for pictures writes the picture default.
  const flux2 = modelRow(page, 'flux2-klein-4b');
  await flux2.parts.control.children[0].onclick(); await settle();
  assert.strictEqual(lastPut().name, 'oa-image-default');
  assert.strictEqual(lastPut().candidates[0].model, 'flux2-klein-4b');
  assert.strictEqual(flux2.parts.status.textContent, 'FLUX.2 klein 4B is now the picture model for your apps.');
  assert.strictEqual(flux2.parts.caption.textContent, '8 GB · Runs on TheNoise · Picture model for your apps');
  // A stored file has no row Set up action; the established setup row remains.
  assert.deepStrictEqual(actions(page, 'flux1-dev'), []);
  // Remove from OA stops hosting the copy and keeps the file; a refusal
  // says so on the row.
  const hosted = modelRow(page, 'qwen3.6-demo'), remove = hosted.parts.control.children[2];
  unhostReply = () => ({ok: false, text: async () => 'the model host kept the model: busy'});
  await remove.onclick();
  assert.strictEqual(hosted.parts.status.textContent, 'Confirm to remove Qwen3.6 demo from OpenAbstractions; its file stays where it is.');
  await remove.onclick(); await settle();
  assert.strictEqual(hosted.parts.status.textContent, "This didn't work.");
  assert.strictEqual(remove.textContent, 'Remove');
  // A busy 409 shows the page's another-step sentence and no JSON.
  unhostReply = () => ({ok: false, status: 409, text: async () => JSON.stringify({busy: {step: 'install'}})});
  await remove.onclick(); await remove.onclick(); await settle();
  assert.strictEqual(hosted.parts.status.textContent, 'Another step is running.');
  assert(!JSON.stringify(hosted.parts.status).includes('install'), 'no JSON in the details');
  unhostReply = null;
  await remove.onclick(); await remove.onclick(); await settle();
  assert.deepStrictEqual(calls.filter(c => c.path === '/setup/modelhost/unhost').at(-1).body, {store: 'store-1', id: 'm2', confirm: true});
  assert.strictEqual(hosted.parts.status.textContent, 'Qwen3.6 demo is removed from OpenAbstractions; its file stays where it is.');
  assert(!calls.some(c => /delete|remove/.test(c.path) && c.path !== '/setup/modelhost/unhost'), 'no file is deleted');
  // Removing the model apps use says they stop using it until Set up runs
  // again (Setup hosts it again on its next run).
  policies.set('account|oa-chat-default', {rev: 'r-chat', candidates: [{Model: 'qwen3.6-demo', Placement: 'local', Credential: '', Provider: ''}]});
  const inUse = load({library: fresh()}); await settle();
  const inUseRow = modelRow(inUse, 'qwen3.6-demo');
  await inUseRow.parts.control.children[1].onclick();
  assert.strictEqual(inUseRow.parts.status.textContent, 'Confirm to remove Qwen3.6 demo from OpenAbstractions; apps stop using it until Set up runs again, and its file stays where it is.');
  // The established setup's row starts the same step.
  await page.element('setup-step-action').onclick(); await settle();
  assert.strictEqual(page.element('setup-step-status').textContent, 'Setup opened its question.');
  rules(page);
  // Settings keeps the row's state and links to Models.
  const settingsGo = fs.readFileSync(__dirname + '/settings_page.go', 'utf8'), stepGo = fs.readFileSync(__dirname + '/setup_step.go', 'utf8');
  assert(settingsGo.includes('setupStepLinkScript') && !settingsGo.includes('+ setupStepScript +'), 'Settings links to Models instead of a second Set up');
  assert(stepGo.includes('<a id="setup-step-models" href="/inference">Open Models</a>'));
  assert(!fs.readFileSync(__dirname + '/registry_page.go', 'utf8').includes('Model inventory'), 'one place for models');

  // A reconciler-hosted external copy offers removal without an OA-root
  // match. Components stay inside the base row, with plain words and paths
  // in titles. The conventional policy name stays in requests alone.
  const externalLibrary = fresh();
  externalLibrary.models[1].objects[0].hosted = true;
  externalLibrary.models[1].objects.push({store:'second-store',id:'second-object',hosted:true});
  externalLibrary.models[1].components = [{name: 'vision.gguf', role: 'projector', place: {name: 'External models', path: 'D:\\External models'}}];
  const external = load({local: {available: false}, library: externalLibrary}); await settle();
  const externalRow = modelRow(external, 'maximum');
  assert.deepStrictEqual(actions(external, 'maximum'), ['Use for chat', 'Remove']);
  assert.strictEqual(externalRow.parts.components.children[0].textContent, 'Vision projector · Vision · in External models');
  assert.strictEqual(externalRow.parts.components.children[0].title, 'D:\\External models');
  assert(!modelRow(external, 'vision'), 'a component is not another model row');
  const externalRemove = externalRow.parts.control.children[1];
  let finishRemove;
  unhostReply=()=>new Promise(resolve=>{finishRemove=()=>{unhostReply=null;resolve({ok:true,json:async()=>({unhosted:true})})}});
  const removeStart=calls.length;
  await externalRemove.onclick();const removing=externalRemove.onclick();await settle();
  const busyRemove=kids(modelRow(external,'qwen3.6-demo').parts.control).at(-1);
  await busyRemove.onclick();
  assert.strictEqual(modelRow(external,'qwen3.6-demo').parts.status.textContent,'Another step is running.');
  assert.strictEqual(calls.slice(removeStart).filter(c=>c.path==='/setup/modelhost/unhost').length,1,'busy Remove starts no second operation');
  finishRemove();await removing;await settle();
  assert.deepStrictEqual(calls.slice(removeStart).filter(c=>c.path==='/setup/modelhost/unhost').map(c=>c.body),[{store:'lms',id:'o-max',confirm:true},{store:'second-store',id:'second-object',confirm:true}],'Remove unhosts every hosted copy before claiming completion');
  assert(!external.element('selection-defaults').children[0].parts.caption.textContent.includes('oa-chat-default'));
  rules(external);
  const titledLibrary=fresh();titledLibrary.models[1].title='Friendly / model';titledLibrary.models[1].name='Friendly / model';
  titledLibrary.running[0].title='Running / title';
  offers.chat[0].Title='Qwen / Chat';
  const titledPage=load({library:titledLibrary});await settle();
  assert(modelRow(titledPage,'Friendly / model'),'catalogue titles retain their slash');
  assert.strictEqual(titledPage.element('models-running-rows').children[0].parts.label.textContent,'Running / title');
  assert(kids(titledPage.element('selection-apps').children[0].parts.select).some(o=>o.textContent==='Qwen / Chat'),'chat chooser shows Offer.Title without filename shortening');
 }
}
