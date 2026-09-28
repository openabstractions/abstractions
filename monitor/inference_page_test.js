// Run with node monitor/inference_page_test.js; no browser or service is started.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/inference_page.go', 'utf8');
const script = go.split('<script>')[1].split('</script>')[0];
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
 return {value:'', textContent:'', className:'', disabled:false, hidden:false, listeners:{}, children:[], replaceChildren(...v){this.children=[...v]}, append(...v){this.children.push(...v)}, click(){this.onclick&&this.onclick()}, focus(){}, addEventListener(type,fn){(this.listeners[type]=this.listeners[type]||[]).push(fn)}, dispatch(type){for(const fn of this.listeners[type]||[])fn()}};
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
 '/inference/audit': [{Outcome:'gap', Entries:[], Next:5, AtEnd:false}, {Outcome:'page', Entries:[{Sequence:5,UnixMs:0,Route:'window',Rung:'tcp-loopback/linux user=kernel process=bound path=bound',Program:'/usr/bin/python3',Host:'ollama',Model:'m',Credential:'',Outcome:'completed',Reason:'',TokensIn:6,TokensOut:4}], Next:6, AtEnd:true}],
};
// task 2026-09-23, fourteenth first-time visitor, finding 8: Issue key's
// own Program path now offers a chooser too, seeded from the Device
// page's own resource holders, the same source Rights' own Program
// chooser already reads.
const cardTableReply = {resources:[{resource:'card:0', holders:[{program:'C:\\lms\\llama-server.exe'}]}]};
const context = {URLSearchParams, document:{getElementById:element,createElement:node}, location:{search:'?k=test'}, console, Date,
 fetch:async(path,opts)=>{
  assert.strictEqual(opts.headers['X-Panel-Key'], 'test');
  if (path === '/card/table') return {ok:true, json:async()=>cardTableReply};
  const base = path.split('?')[0];
  calls.push({path, body: opts.body ? JSON.parse(opts.body) : null});
  const reply = replies[base].shift();
  if (reply && reply.__fail) return {ok:false, text: async()=>reply.__fail};
  return {ok:true, json:async()=>reply};
 }};
context.window = context; // window is the global object here, same as a browser (task 2026-09-23, fifteenth first-time visitor, finding 1)
vm.runInNewContext(plainScript, context);
vm.runInNewContext(script, context);
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
 assert.deepStrictEqual(cells(element('hosts').children[0]).slice(0, 9), ['openrouter','hosted: openai-compatible','https://openrouter.ai/api/v1','openrouter','chat, embed','operator','Not reachable','42 tokens, 2.50 (2026-09-17)','1000 tokens, 0.75']);
 assert.equal(element('hosts').children[0].children[6].title, 'credential:unknown:openrouter', 'the raw reason sits on the State cell\'s own title');
 // A host with no credential, no spend yet and no ceiling reads as a stated
 // absence in each of those three cells, never a blank cell (task
 // 2026-09-23 finding 3).
 assert.deepStrictEqual(cells(element('hosts').children[1]).slice(0, 9), ['ollama','local: ollama','http://127.0.0.1:11434','none','chat','installation','Up','0','no budget']);
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
 element('hosts').children[0].children[9].children[0].onclick(); await settle();
 assert.deepStrictEqual(calls.at(-1).body, {edit:'remove', name:'openrouter', revision:'hosts-v1:a'});
 assert.match(text('hosts-status'), /configuration changed/);
 // task 2026-09-23, ninth first-time visitor, finding 3: Show hosts with no
 // hosts left only the table's own headers and no line at all; it now
 // names what to do next, the same as Gateway keys' and Audit's own
 // empty/end-of-list lines already did. A conflicted remove (above) did
 // not refresh the list itself (only a successful write does), so this is
 // still a manual Refresh click.
 element('hosts-list').onclick(); await settle();
 assert.strictEqual(text('hosts-status'), 'No model servers registered. Add one below.');
 assert.strictEqual(element('hosts').children.length, 0);
 // Gateway already shows the arrival read too, with no click; "Show
 // setting" is Refresh now.
 assert.strictEqual(text('gateway-status'), 'Setting: off\nGateway: closed');
 assert.strictEqual(element('gateway-status').title, 'Setting revision gateway-v1:a');
 element('gateway-set').onsubmit({preventDefault(){}}); await settle(); await settle();
 assert.deepStrictEqual(calls.filter(c => c.path === '/inference/gateway').map(c => c.body), [null, {revision:'gateway-v1:a', open:true, address:''}, null]);
 assert.match(text('gateway-status'), /^Setting: on at 127\.0\.0\.1:8793\nGateway: listening on http:\/\/127\.0\.0\.1:8793\/v1/);
 assert.strictEqual(element('gateway-status').title, 'Setting revision gateway-v1:b');
 element('gateway-off').onclick(); await settle();
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
 // seeded from the Device page's own resource holders.
 assert.deepEqual(element('key-program-select').children.map(o=>o.value), ['C:\\lms\\llama-server.exe','__other__'], 'the chooser opens with the Device holders this runtime could read, Other\u2026 last');
 assert.equal(element('key-program-select').children[0].textContent, 'llama-server.exe');
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
 assert.strictEqual(element('key-shown').children[1].textContent, 'oalk_shown_once');
 assert.strictEqual(cells(element('keys').children[0])[1], 'active');
 // task 2026-09-23, tenth first-time visitor, findings 2 and 3: Issued read
 // a raw ISO timestamp, and Issued by read a full path, both as visible
 // cell text; each now reads a local readable value (or a file name), the
 // raw value kept as that cell's own title.
 const keyRow = element('keys').children[0];
 assert.strictEqual(cells(keyRow)[3], new Date(Date.parse('2020-01-01T00:00:00Z')).toLocaleDateString(), 'a key issued well before today reads a local date, not a raw ISO timestamp');
 assert.strictEqual(keyRow.children[3].title, new Date(Date.parse('2020-01-01T00:00:00Z')).toISOString());
 assert.strictEqual(cells(keyRow)[4], 'openabstractions.exe', 'Issued by reads the file name');
 assert.strictEqual(keyRow.children[4].title, 'C:\\oa\\openabstractions.exe', 'the full path sits on the cell\'s own title');
 element('keys').children[0].children[5].children[0].onclick(); await settle();
 assert.deepStrictEqual(calls.at(-1).body, {edit:'revoke', program:'/usr/bin/python3'});
 assert.strictEqual(element('key-shown').children.length, 0, 'a later action no longer shows the issued key');
 assert.match(text('keys-status'), /not permit/);
 element('audit-start').onclick(); await settle(); await settle();
 assert.deepStrictEqual(calls.filter(c => c.path.startsWith('/inference/audit')).map(c => c.path), ['/inference/audit?cursor=0', '/inference/audit?cursor=5']);
 assert.deepStrictEqual(cells(element('audit').children[0]).slice(2, 5), ['window','tcp-loopback/linux user=kernel process=bound path=bound','/usr/bin/python3']);
 // A transport failure (the fetch itself rejects, not a typed Outcome) is
 // shown as a plain sentence, never the raw text naming a pipe path and a
 // security identifier; that text stays available behind a disclosure. The
 // text names a contract ("abstraction.inference/keys@1"), so oaPlain reads
 // it as the runtime answering and lacking that service, not as "isn't
 // running" (task 2026-09-23 finding 1).
 element('keys-list').onclick(); await settle();
 assert.strictEqual(text('keys-status'), "The runtime is running but doesn't provide inference on this account.");
 const keysDetails = element('keys-status').children[0];
 assert.strictEqual(keysDetails.className, 'oa-error-details');
 assert.strictEqual(keysDetails.children[1].textContent, 'service resolution: unavailable: abstraction.inference/keys@1 could not be resolved at the installed runtime at \\\\.\\pipe\\openabstractions-user-S-1-5-21-1001');

 // task 2026-09-23, seventh first-time visitor, finding 4: Add host with a
 // blank required field used to show no result at all.
 element('host-name').dispatch('invalid');
 assert.strictEqual(text('hosts-status'), 'Name is required to add a model server.', 'a blank required field is named, the Issue key pattern');

 console.log('PASS inference page');
})().catch(e => { console.error(e); process.exit(1); });
