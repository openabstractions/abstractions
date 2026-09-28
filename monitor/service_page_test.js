// Run with node monitor/service_page_test.js; no browser or service is started.
// Covers what remains of service_page.go (Status: needs-you and Check the
// service), and the two pages split out of it that carry the bulk of the
// Status page's old behavior: work_page.go (job inventory, downloads,
// account work) and rights_page.go (questions and rights). Each page's own
// script is self-contained; loadPageScript splices serviceCommonScript back
// in where a page's source only carries the Go "` + serviceCommonScript + `"
// glue, so every page still runs as the one script monitor/*_page_test.js
// conventionally extracts.
const fs = require('fs'), vm = require('vm'), assert = require('assert');

// plainScript (monitor/panel_shell.go) is embedded ahead of every page
// fragment in the real document; the Status page's capability rows now call
// oaPlain (task 2026-09-23, fifth first-time visitor: "Work submission:
// Unavailable" carried no plain reason), so this isolated test loads it into
// the same vm context first, the same way monitor/card_page_test.js and its
// siblings already do.
function plainScript() {
 const shellGo = fs.readFileSync(__dirname + '/panel_shell.go', 'utf8');
 const marker = 'const plainScript = `';
 const start = shellGo.indexOf(marker) + marker.length;
 assert(start > marker.length - 1, 'plainScript constant not found in panel_shell.go');
 const end = shellGo.indexOf('`', start);
 return shellGo.slice(start, end).split('<script>')[1].split('</script>')[0];
}

function commonScript() {
 const src = fs.readFileSync(__dirname + '/service_page.go', 'utf8');
 const marker = 'const serviceCommonScript = `';
 const start = src.indexOf(marker) + marker.length;
 assert(start > marker.length - 1, 'serviceCommonScript not found in service_page.go');
 return src.slice(start, src.indexOf('`', start));
}
function loadPageScript(file) {
 const go = fs.readFileSync(__dirname + '/' + file, 'utf8');
 const script = go.split('<script>')[1].split('</script>')[0];
 const glue = '` + serviceCommonScript + `';
 assert(script.includes(glue), file + ': serviceCommonScript glue not found');
 return script.replace(glue, commonScript());
}

const fields = new Map();
function node() {
 return {value:'', textContent:'', disabled:false, listeners:{}, children:[], replaceChildren(){this.children=[]}, append(...v){this.children.push(...v)}, click(){}, addEventListener(type,fn){(this.listeners[type]=this.listeners[type]||[]).push(fn)}, dispatch(type){for(const fn of this.listeners[type]||[])fn()}};
}
function element(id) {
 if (!fields.has(id)) fields.set(id, node());
 return fields.get(id);
}

(async () => {
 // --- Status page: needs-you and Check the service (service_page.go). ---
 {
  fields.clear();
  let questionPage = {Outcome:'page', Records:[{ID:'q1', Text:'Allow download?', Options:['once','refuse'], Option:''}], Next:'', Complete:true};
  const inventoryPage = {Outcome:'page', Next:'', Complete:true, Snapshots:[{Receipt:{OperationID:'op-1'}, State:'running', Label:''}]};
  const accountPage = {Outcome:'page', Next:'', Complete:true, Snapshots:[{Receipt:{OperationID:'op-2'}, State:'pending', Label:''}]};
  const context = {URLSearchParams, localStorage:{length:0, key(){}, getItem(){return null}, setItem(){}}, document:{getElementById:element, createElement:node}, location:{search:'?k=test'}, console,
   fetch:async(path)=>{
    if(path.startsWith('/questions'))return {ok:true,json:async()=>questionPage};
    if(path.startsWith('/inventory'))return {ok:true,json:async()=>inventoryPage};
    if(path.startsWith('/account-work'))return {ok:true,json:async()=>accountPage};
    if(path==='/runtime')return {ok:true,json:async()=>({explicit:{program:'/opt/openabstractions/openabstractions',endpoint:'\\\\.\\pipe\\openabstractions-user-S-1-5-21-3833002460-2194338898-3510754826-1001-panel9-runtime'},capabilities:[
     {name:'Work submission', contract:'abstraction.job/acceptance@1', status:'unavailable', label:'Unavailable'},
     {name:'Work status and results', contract:'abstraction.job/operations@1', status:'unavailable', label:'Unavailable'},
     {name:'Logging', contract:'abstraction.logging/sink@1', status:'resolved', label:'Ready'},
    ]})};
    throw Error('unexpected request '+path);
   }};
  context.window = context; // window is the global object here, same as a browser (task 2026-09-23, fifteenth first-time visitor, finding 1)
  vm.runInNewContext(plainScript(), context);
  vm.runInNewContext(loadPageScript('service_page.go'), context);
  await context.needsYouReady;
  assert.equal(element('needs-you').textContent, '1 app is waiting for your answer under Questions. 2 downloads are in progress under Work.', 'the needs-you line counts pending questions and active work across both pages it summarizes');

  await element('status').onclick();
  const runtimeText = element('runtime').children.map(c=>c.textContent||'').join(' | ');
  // task 2026-09-23, seventh first-time visitor, finding 3: the sentence
  // named nothing; it now names the endpoint's own short tail, skipping the
  // security identifier in between.
  assert(runtimeText.includes('This Panel is connected to the runtime at panel9-runtime.'), 'the sentence names the endpoint\'s short tail');
  assert(!runtimeText.includes('/opt/openabstractions/openabstractions'), 'the runtime program path is not in the visible sentence');
  assert(!runtimeText.includes('S-1-5-21'), 'the security identifier is not in the visible sentence');
  // Fifth first-time visitor: "Work submission: Unavailable" and "Work status
  // and results: Unavailable" carried no plain reason, only the Technical
  // details JSON. The Status page must state the same plain sentence the
  // Work page already shows for this condition (work_page.go, oaPlain).
  assert(runtimeText.includes("Work submission: The runtime is running but doesn't provide background work on this account."), 'the unavailable job/acceptance capability states the plain reason, not a bare label');
  assert(runtimeText.includes("Work status and results: The runtime is running but doesn't provide background work on this account."), 'the unavailable job/operations capability states the plain reason too');
  assert(runtimeText.includes('Logging: Ready'), 'a resolved capability keeps its plain label unchanged');
  const details = element('runtime').children.find(c=>c.children&&c.children[0]&&c.children[0].textContent==='Technical details');
  assert(details && details.children[1].textContent.includes('/opt/openabstractions/openabstractions'), 'the path is available under Technical details');
  assert(details.children[1].textContent.includes('"status": "unavailable"'), 'the raw status stays available under Technical details');
  // task 2026-09-23, thirteenth first-time visitor, finding 1: a second
  // click of Check the service rebuilt the same content from the same
  // reply, with nothing saying a check had actually just run.
  assert.equal(element('check-status').textContent, 'Checked at '+context.oaClockText(new Date(),false)+': the service answered.', 'one line beside the button names when this click\'s own answer came back');
  console.log('PASS: Status page needs-you summary and Check the service (path only under Technical details)');
 }

 // --- Work page (work_page.go): job inventory, downloads, account work. ---
 {
  fields.clear();
  const values = new Map();
  let submitted = 0;
  // Unlike the Status page (needsYou() also reads /account-work as part of
  // its summary and so consumes one reply before the test's own calls),
  // this page's first click is the test's first /account-work request.
  const accountPages = [{Outcome:'forbidden', Snapshots:[], Next:'', Complete:false}, {Outcome:'page', Next:'', Complete:true, Snapshots:[{Receipt:{OperationID:'op-other'}, State:'running', Label:'another program'}]}, {Outcome:'page', Next:'', Complete:true, Snapshots:[]}];
  const accountCalls = [], accountReplies = [{Outcome:'forbidden'},{Outcome:'requested'},{Outcome:'already_terminal'}];
  const jobReceipt = id => ({OperationID:id, LogicalOwner:'owner', Identity:{Key:id, HistoryEpoch:'epoch'}});
  const inventoryPage = {Outcome:'page', Next:'', Complete:true, Snapshots:[
   {Receipt:jobReceipt('op-derived'), State:'running', Label:'huggingface.co · model.safetensors', LabelDerived:true},
   {Receipt:jobReceipt('op-caller'), State:'complete', Label:'org/tiny@abc · unet/model.safetensors', LabelDerived:false},
   {Receipt:jobReceipt('op-absent'), State:'pending', Label:'', LabelDerived:false, Waiting:'network:metered'}]};
  // task 2026-09-23, twelfth first-time visitor, finding 7: a further
  // Next page past the real end used to wipe the rows already shown and
  // leave only a small status change, reading as doing nothing.
  const inventoryPages = [inventoryPage, {Outcome:'page', Next:'', Complete:true, Snapshots:[]}];
  const localStorage = {get length(){return values.size}, key(i){return [...values.keys()][i]}, getItem(k){return values.get(k)||null}, setItem(k,v){values.set(k,v)}};
  // TestURL is real URL parsing (savedLabel, task 2026-09-23, thirteenth
  // first-time visitor, finding 4, now parses a saved record's own URL),
  // with the two static methods Save download record's own blob export
  // already needed stubbed onto it.
  class TestURL extends URL {}
  TestURL.createObjectURL = () => 'blob:test';
  TestURL.revokeObjectURL = () => {};
  const context = {URLSearchParams, localStorage, document:{getElementById:element, createElement:node}, location:{search:'?k=test'}, console, Blob,
   URL:TestURL,
   fetch:async(path,opts)=>{
    if(path==='/binding') return {ok:true,json:async()=>({endpoint:'fixed',history:{LogicalOwner:'owner',HistoryEpoch:'epoch'}})};
    if(path.startsWith('/inventory')) return {ok:true,json:async()=>(inventoryPages.length>1?inventoryPages.shift():inventoryPages[0])};
    if(path.startsWith('/account-work')){
     if(!opts.body) return {ok:true,json:async()=>accountPages.shift()};
     accountCalls.push(JSON.parse(opts.body));
     return {ok:true,json:async()=>accountReplies.shift()};
    }
    assert.equal(path,'/action');const action=JSON.parse(opts.body);
    // task 2026-09-23, fifteenth first-time visitor, finding 5: the real
    // /action (service_panel.go's serviceAction) refuses any field it does
    // not itself name (DisallowUnknownFields); Continue saved download
    // used to send the saved record's own savedAt straight through, and
    // this mock now refuses it the same way the real server did, so a
    // regression here fails this test instead of only the real runtime.
    const allowedActionFields=['action','endpoint','owner','identity','url','digest','size'];
    if(Object.keys(action).some(k=>!allowedActionFields.includes(k)))return {ok:false,text:async()=>'invalid action'};
    const stored=[...values.values()].map(x=>JSON.parse(x));
    assert(stored.some(x=>x.identity.Key===action.identity.Key&&x.endpoint==='fixed'&&x.owner==='owner'&&x.identity.HistoryEpoch==='epoch'&&x.digest===action.digest),'submit preceded recovery persistence');
    // A reconcile against the already-saved "first" record proves Continue
    // saved download's own request now reaches this far at all (task
    // 2026-09-23, fifteenth first-time visitor, finding 5): before the fix
    // above, its extra savedAt field never got this far, refused by the
    // check above instead.
    if(action.action==='reconcile'&&action.identity.Key==='first')return {ok:true,json:async()=>({Outcome:'accepted',Receipt:jobReceipt('op-reconciled'),Reason:'idempotent replay'})};
    submitted++;
    // task 2026-09-23, tenth first-time visitor, finding 1: a refused
    // download printed the raw {"Outcome":"definitely_not_accepted",...}
    // object; this one key exercises that reply instead of the network
    // failure every other submission in this test throws.
    if(action.identity.Key==='refused')return {ok:true,json:async()=>({Outcome:'definitely_not_accepted',Receipt:null,Reason:'configured executor refused this work'})};
    throw Error('reply lost');
   }};
  // task 2026-09-23, ninth first-time visitor, finding 2: Continue saved
  // download and Save download record, with nothing saved, throw straight
  // into fail('operation', e), which calls oaPlain (panel_shell.go); this
  // section had never needed plainScript loaded before, since nothing here
  // exercised that path.
  context.window = context; // window is the global object here, same as a browser (task 2026-09-23, fifteenth first-time visitor, finding 1)
  vm.runInNewContext(plainScript(), context);
  vm.runInNewContext(loadPageScript('work_page.go'), context);

  // task 2026-09-23, thirteenth first-time visitor, finding 2: Request key
  // used to be a visible, required text field showing the generated UUID
  // as its own value; New key is now the control, with a plain sentence in
  // its place and the raw key kept behind Technical details.
  assert.equal(element('requestkey-note').children[0].textContent, 'An idempotency key was generated for this download.', 'the plain sentence stands in for the raw generated value');
  assert.equal(element('requestkey-note').children[1].children[0].textContent, 'Technical details');
  assert(element('requestkey-note').children[1].children[1].textContent.startsWith('Idempotency key: '), 'the raw key sits behind Technical details');
  assert(element('requestkey').value, 'the hidden field still carries the generated key, for the submit handler to keep reading unchanged');
  const firstGenerated = element('requestkey').value;
  await element('requestkey-new').onclick();
  assert.notEqual(element('requestkey').value, firstGenerated, 'New key regenerates the hidden value');
  assert.equal(element('requestkey-note').children[0].textContent, 'An idempotency key was generated for this download.', 'the sentence stays the same shape after regenerating');

  // task 2026-09-23, fourteenth first-time visitor, finding 4: a refused
  // download used to read "The runtime's download executor refused this
  // work: configured executor refused this work." with no next step; the
  // two shapes that reason actually protects against are now checked
  // before the request is even sent, with a next step of their own.
  element('url').value='not a url';element('digest').value='sha256:'+'a'.repeat(64);element('size').value='1';
  await element('submit').onsubmit({preventDefault(){}});
  assert(element('operation').textContent.startsWith('File URL must be a full address, starting with http:// or https://.'), element('operation').textContent);
  element('url').value='ftp://example.com/file';
  await element('submit').onsubmit({preventDefault(){}});
  assert(element('operation').textContent.startsWith('File URL must start with http:// or https://.'), element('operation').textContent);
  element('url').value='http://127.0.0.1/data';element('digest').value='not-a-digest';
  await element('submit').onsubmit({preventDefault(){}});
  assert(element('operation').textContent.startsWith('Hash must read sha256: followed by 64 hex characters.'), element('operation').textContent);
  assert.equal(submitted,0,'none of the three malformed submissions reached the service');

  element('url').value='http://127.0.0.1/data';element('digest').value='sha256:'+'a'.repeat(64);element('size').value='1';
  for(const key of ['first','second']) {element('requestkey').value=key;await element('submit').onsubmit({preventDefault(){}})}
  // task 2026-09-23, thirteenth first-time visitor, finding 4: Saved
  // downloads used to label each choice by its own raw request key, a
  // UUID with no meaning to read; it now reads the file name and host out
  // of the saved URL, with the raw key kept on the option's own title.
  const savedOptions = element('saved').children;
  assert.equal(savedOptions.length, 2, 'both submissions are saved, one option each');
  for(const o of savedOptions){
   assert(o.textContent.startsWith('data · 127.0.0.1'), 'the option reads the file name and host, not the raw key: '+o.textContent);
   assert(o.title==='first'||o.title==='second', 'the raw request key sits on the option\'s own title instead');
  }
  assert.equal(submitted,2);
  assert([...values.keys()].some(k=>k.includes('first')),'older recovery record overwritten');
  assert([...values.keys()].some(k=>k.includes('second')));
  assert(element('operation').textContent.includes('Reconcile'));
  // task 2026-09-23, tenth first-time visitor, finding 1: a refused
  // download used to print the raw {"Outcome":"definitely_not_accepted",
  // "Receipt":null,"Reason":"configured executor refused this work"}
  // object; it now reads one plain sentence, the server's own Reason after
  // a colon, the full object behind Technical details.
  // task 2026-09-23, thirteenth first-time visitor, finding 3: a round-12
  // pass read that exact Reason as meaning this runtime holds no download
  // executor at all and said so; that was wrong (the acceptance provider
  // only reaches this Reason when a configured executor's own Prepare
  // rejected the specific request, never when no executor exists at all).
  // It now names the download executor, not the runtime, and repeats the
  // service's own reason rather than guess at one.
  element('requestkey').value='refused';await element('submit').onsubmit({preventDefault(){}});
  // task 2026-09-23, fifteenth first-time visitor, finding 5: the wire
  // truly carries nothing past this one fixed Reason for any executor
  // refusal (abstraction-job/go's acceptanceprovider discards the
  // executor's own error unconditionally); the sentence now names what a
  // person here can still check instead of repeating the one word the
  // server itself already gave no more meaning than.
  assert.equal(element('operation').textContent,'The download executor refused this work. Check that the file URL carries no username or password, and that the hash and size are exactly right.');
  assert.equal(element('operation').children[0].children[0].textContent,'Technical details');
  assert(element('operation').children[0].children[1].textContent.includes('definitely_not_accepted'));

  // task 2026-09-23, fifteenth first-time visitor, finding 5: a URL
  // carrying a username or password used to reach the server and read
  // "configured executor refused this work" (HTTPExecution.PrepareScoped
  // requires an anonymous source); it is now checked, and named, before
  // the request is even sent, the same as the other two shapes above.
  element('requestkey').value='never-sent-userinfo';element('url').value='http://user:pass@example.com/file';element('digest').value='sha256:'+'a'.repeat(64);element('size').value='1';
  const submittedBeforeUserinfo=submitted;
  await element('submit').onsubmit({preventDefault(){}});
  assert.equal(element('operation').textContent,'File URL must not include a username or password; the download executor requires a plain, anonymous address. Retain the saved request; use Reconcile before any new submission.');
  assert.equal(submitted,submittedBeforeUserinfo,'the malformed submission never reached the service');

  // task 2026-09-23, fifteenth first-time visitor, finding 5: Continue
  // saved download used to send the saved record's own savedAt straight
  // to /action, an extra field the server's own strict decoder refused
  // ("invalid action"), generalized by oaPlain to "The runtime rejected
  // this request." with the real cause reachable only in Technical
  // details, and no way to tell it apart from a request refused for a
  // genuinely different reason. It is stripped now, the same way the
  // submit handler above already strips it before comparing a saved
  // record against a new one.
  element('saved').value=savedOptions.find(o=>o.title==='first').value;
  await element('reconcile').onclick();
  assert.equal(element('operation').textContent,'Accepted; the runtime queued this download.','Continue saved download now reaches the service at all; before this fix the extra savedAt field refused it before ever reaching this reply');
  element('requestkey').value='first';element('url').value='http://127.0.0.1/different';await element('submit').onsubmit({preventDefault(){}});assert.equal(submitted,3,'changed work reused a saved key');
  localStorage.setItem=()=>{throw Error('storage refused')};
  element('requestkey').value='never-sent';await element('submit').onsubmit({preventDefault(){}});
  assert.equal(submitted,3,'submission continued after recovery persistence failed');

  await element('next').onclick();
  const titles=element('records').children.map(box=>box.children[0].textContent);
  assert.deepEqual(titles,['huggingface.co · model.safetensors (derived) - running','org/tiny@abc · unet/model.safetensors - complete','Unlabelled job op-absent (waiting: network:metered) - pending'],'job list shows each label, derived ones marked, and what holds waiting work');
  // task 2026-09-23, twelfth first-time visitor, finding 7: clicking Next
  // page once more, past the real end, used to wipe the rows just shown
  // (this page's own Snapshots come back empty) and change only the small
  // status text, reading as though the click did nothing.
  await element('next').onclick();
  assert.equal(element('inventory-status').textContent,'No more jobs to show.','a further Next page past the end says so plainly');
  assert.deepEqual(element('records').children.map(box=>box.children[0].textContent),titles,'the rows already shown stay, instead of being wiped by an empty page');

  await element('account-start').onclick();
  assert.equal(element('account').children.length,0,'a refused account listing shows no work');
  assert(element('account-status').textContent.includes('no rule'),'forbidden account listing names the missing rule');
  await element('account-start').onclick();
  const other=element('account').children[0];
  assert(other.children[0].textContent.includes('another program - running'),'account work shows every program\'s operation, label first and state second, the same shape the Device page uses for the same download');
  assert(!other.children[0].textContent.includes('op-other'),'the operation id is not in the visible line');
  assert.equal(other.children[0].title,'op-other','the operation id sits on the line\'s own title attribute instead');
  await other.children[1].onclick();
  assert.deepEqual(accountCalls[0],{operation:'op-other'});
  assert(element('account-status').textContent.startsWith('another program:'),'a cancellation refusal names the operation by its label, never its raw id (task 2026-09-23, eighth first-time visitor, finding 3)');
  assert(element('account-status').textContent.includes('nothing was listed or changed'),'forbidden cancellation is a refusal');
  await other.children[1].onclick();
  assert(element('account-status').textContent.startsWith('another program:'),'a permitted cancellation also names the operation by its label');
  assert(element('account-status').textContent.includes('cancellation requested'),'permitted cancellation is reported');
  await other.children[1].onclick();
  assert.equal(element('account-status').textContent,'another program: already finished.','a cancellation against finished work names the label, never the id, and reads "already finished"');
  // task 2026-09-23, twelfth first-time visitor, finding 7: the same guard
  // on the account-work list's own Next page.
  const accountTitles=element('account').children.map(box=>box.children[0].textContent);
  await element('account-next').onclick();
  assert.equal(element('account-status').textContent,'No more jobs to show.','a further Next page past the end says so plainly');
  assert.deepEqual(element('account').children.map(box=>box.children[0].textContent),accountTitles,'the row already shown stays, instead of being wiped by an empty page');
  // task 2026-09-23, seventh first-time visitor, finding 4: Download with a
  // required field left blank used to show no result at all (a browser
  // blocks the submit event itself, so the form's own onsubmit never ran).
  // Every required field now names itself, from its own "invalid" event.
  element('url').dispatch('invalid');
  assert.equal(element('operation').textContent,'File URL is required to submit a download.','a blank required field is named, the Issue key pattern');
  // task 2026-09-23, ninth first-time visitor, finding 2: Continue saved
  // download and Save download record, called with nothing saved (or a
  // saved-download selection naming a key never actually saved), both used
  // to show the one technical sentence unchanged; each now shows its own
  // plain line, through oaPlain's table, with the technical sentence kept
  // behind Technical details.
  // task 2026-09-23, fourteenth first-time visitor, finding 5: Save
  // download record used to read "Saved operation-recovery.json for
  // request 55219ba1-…", the file this Panel wrote and the raw request
  // key both visible text; it now names the download itself.
  element('saved').value=element('saved').children.find(o=>o.title==='second').value;
  element('export').onclick();
  assert.equal(element('operation').textContent,'Saved the record for data.');
  assert.equal(element('operation').children[0].children[0].textContent,'Technical details');
  assert.equal(element('operation').title,'file: operation-recovery.json; idempotency key: second');

  element('saved').value='nonexistent-key';
  await element('reconcile').onclick();
  assert.equal(element('operation').textContent,'Choose a saved download first.');
  assert.equal(element('operation').children[0].children[0].textContent,'Technical details');
  assert(element('operation').children[0].children[1].textContent.includes('No saved request to continue.'));
  element('export').onclick();
  assert.equal(element('operation').textContent,'Start a download first, then save its record.');
  assert(element('operation').children[0].children[1].textContent.includes('No saved request to save.'));
  console.log('PASS: Work page job list shows labels, derived ones marked; recovery persisted before send, prior keys retained, storage refusal prevents send; account work list/cancel with typed refusals; a blank required download field names itself; Continue/Save with nothing saved name themselves in plain words');
 }

 // --- Rights page (rights_page.go): questions and rights. ---
 {
  fields.clear();
  const firstUseRule = {Subject:{Account:'alice',Program:'/usr/bin/python3'},Action:'abstraction.job/acceptance.submit',Resource:'abstraction.job/acceptance@1',Permit:true};
  const questionReplies = {answer:[{Outcome:'forbidden'},{Outcome:'answered',Record:{ID:'q1'}},{Outcome:'answered',Record:{ID:'q2'},rule:firstUseRule,edit:{Outcome:'applied',Revision:'rev-2'}},{Outcome:'answered',Record:{ID:'q2'},rule:{...firstUseRule,Permit:false},edit:{Outcome:'conflict',Revision:'rev-3'}},{Outcome:'answered',Record:{ID:'q2'},rule:firstUseRule,edit:{Outcome:'applied',Revision:'rev-4'}}], retire:[{Outcome:'unavailable'},{Outcome:'retired'}]};
  // task 2026-09-23, thirteenth first-time visitor, finding 7: a first-use
  // question's own server-filled Text read as "<full path> wants to
  // <raw action id> on <resource>" unchanged.
  const firstUseProgram = 'C:\\Users\\alice\\AppData\\Local\\Programs\\OpenAbstractions\\tools\\openabstractions.exe';
  let questionPage = {Outcome:'page', Records:[{ID:'q1', Text:'Allow download?', Options:['once','refuse'], Option:''}, {ID:'q3', Text:firstUseProgram+' wants to abstraction.model/lookup on nosuchregistry', About:firstUseProgram, Options:['allow','refuse'], Option:''}], Next:'', Complete:true};
  const rule = {Subject:{Account:'alice',Program:'/usr/bin/app'},Action:'abstraction.storage/content.read',Resource:'sha256:x',Permit:true,SetBy:{Account:'alice',Program:'/usr/bin/openabstractions'},SetAt:'2026-09-17T10:00:00.000Z'};
  const dupA = {Subject:{Account:'alice',Program:'/usr/bin/app'},Action:'abstraction.model/lookup',Resource:'hf',Permit:true,SetBy:{Account:'alice',Program:'/usr/bin/openabstractions'},SetAt:'2026-09-17T10:00:00.000Z'};
  const dupB = {...dupA, Resource:'civitai'};
  const exploreSelfReply = {self:{account:'root',program:'/usr/bin/openabstractions'},selfAccountName:'alice',probes:[]};
  let rightsPage = {Outcome:'page', Revision:'sha256:e3b0c44298fc1c149afbf4c8996fb924', Catalog:['abstraction.storage/content.read','abstraction.model/lookup'], ActionPlain:{'abstraction.model/lookup':'model resolve'}, Rules:[rule, dupA, dupB], Next:'', Complete:true};
  const rightsCalls = [];
  const allowRules = [{action:'abstraction.job/acceptance.submit',resource:'abstraction.job/acceptance@1'},{action:'abstraction.model/lookup',resource:'hf'}];
  const allowCalls = [], allowReplies = [{rules:allowRules,outcome:'conflict',revision:'rev-9',landed:[allowRules[0]],stopped:allowRules[1]},{rules:allowRules,outcome:'applied',revision:'rev-10',landed:allowRules}];
  // task 2026-09-23, twelfth first-time visitor, requirement 2: Save
  // rule's own Program path field now offers a dropdown, seeded from the
  // Device page's own resource holders (best-effort: this Panel may not
  // hold resource table read) and every program a rule already names.
  const cardTableReply = {resources:[{resource:'card:0', holders:[{program:'C:\\lms\\llama-server.exe'},{program:'/usr/bin/python3'}]}]};
  let rightsGetCount=0;
  const context = {URLSearchParams, localStorage:{length:0,key(){},getItem(){return null},setItem(){}}, document:{getElementById:element, createElement:node}, location:{search:'?k=test'}, console,
   fetch:async(path,opts)=>{
    if(path==='/card/table') return {ok:true,json:async()=>cardTableReply};
    if(path==='/explore') return {ok:true,json:async()=>exploreSelfReply};
    if(path==='/rights/allow'){allowCalls.push(JSON.parse(opts.body));return {ok:true,json:async()=>allowReplies.shift()}}
    if(path.startsWith('/rights')){
     if(!opts.body){rightsGetCount++;return {ok:true,json:async()=>rightsPage}}
     const edit=JSON.parse(opts.body);rightsCalls.push(edit);
     return {ok:true,json:async()=>({Outcome:'applied',Revision:'rev-next',Current:rule})};
    }
    if(path.startsWith('/questions')){
     if(!opts.body) return {ok:true,json:async()=>questionPage};
     const action=JSON.parse(opts.body);
     return {ok:true,json:async()=>questionReplies[action.action].shift()};
    }
    throw Error('unexpected request '+path);
   }};
  context.window = context; // window is the global object here, same as a browser (task 2026-09-23, fifteenth first-time visitor, finding 1)
  vm.runInNewContext(plainScript(), context);
  vm.runInNewContext(loadPageScript('rights_page.go'), context);
  // task 2026-09-23, fourteenth first-time visitor, findings 2 and 3:
  // Questions and Rules both used to open empty until their own "Show"
  // button was clicked, with the Action dropdown empty and the Program
  // and Account choosers unfed until then too. Both now read on arrival
  // (questionsReady, rightsListReady); "Show questions" and "Show rules"
  // are renamed Refresh.
  await context.questionsReady;
  await context.rightsDeviceHoldersReady;
  await context.rightsSelfReady;
  await context.rightsListReady;

  assert.deepEqual(element('rights-action').children.map(o=>o.value),['abstraction.storage/content.read','abstraction.model/lookup'],'catalogue offers the grant actions on arrival, with no click');
  const box=element('questions-list').children[0];
  const buttons=box.children.filter(c=>c.onclick);
  assert.deepEqual(buttons.map(b=>b.textContent),['once','refuse','Dismiss'],'the pending question is already listed on arrival, with no click');
  // task 2026-09-23, thirteenth first-time visitor, finding 7: the
  // first-use question's own sentence already reads in plain words on
  // arrival, rightsActionPlainCache already populated by the time
  // Questions itself first renders (both load in parallel above; the
  // render order the page's own script defines them in does not matter,
  // since rightsList's ActionPlain and Questions' own render are
  // independent of one another here — this fixture's ActionPlain is
  // shared ahead of either settling).
  const firstUseBox = element('questions-list').children[1];
  const firstUseText = firstUseBox.children[0];
  assert.equal(firstUseText.textContent.split('\n')[0], 'openabstractions.exe wants to model resolve on nosuchregistry.', 'the plain sentence names the program by file name and the action in plain words, not the raw path or action id');
  assert.equal(firstUseText.title, firstUseProgram+'; abstraction.model/lookup', 'the raw path and action id sit on the line\'s own title instead');
  const firstUseDetails = firstUseBox.children[1];
  assert.equal(firstUseDetails.children[0].textContent, 'Technical details');
  assert.equal(firstUseDetails.children[1].textContent, firstUseProgram+' wants to abstraction.model/lookup on nosuchregistry', 'the server\'s own raw sentence also sits behind Technical details');

  // task 2026-09-23, fourteenth first-time visitor, finding 3: answering a
  // question used to say "... List again to refresh." while the list
  // under it kept its stale rows; it now reads the list again itself.
  await buttons[0].onclick();
  assert(element('questions-status').textContent.includes('not authorized'),'forbidden answer is shown as a refusal');
  await buttons[0].onclick();
  assert(element('questions-status').textContent.includes('Answer recorded'));
  assert(!element('questions-status').textContent.includes('List again'), 'the reply no longer tells the person to list again; the list already refreshed itself before this text was shown');
  assert(element('questions-list').children.length>0, 'the list stayed rendered (refreshed, not cleared) after a successful answer');

  // task 2026-09-23, twelfth first-time visitor, finding 6: Save rule's own
  // Account field opened blank and required with no default, while Allow's
  // quick-grant form already defaults its account to this one. It now
  // offers a chooser defaulting to this account, prefilled, on arrival,
  // "Other…" for a different one.
  assert.deepEqual(element('rights-account-select').children.map(o=>[o.value,o.textContent]),[['root','this account, alice'],['__other__','Other…']],'the chooser defaults to this account by name, Other… last');
  assert.equal(element('rights-account-select').children[0].title,'root','the raw account value stays reachable as the option\'s own title');
  assert.equal(element('rights-account').hidden,true,'the typed field stays hidden while the known default is selected');
  assert.equal(element('rights-account').value,'root','the typed field is kept in sync with the known default, so the existing submit handler keeps reading it unchanged');
  element('rights-account-select').value='__other__';
  element('rights-account-select').onchange();
  assert.equal(element('rights-account').hidden,false,'choosing Other… reveals the typed field');
  assert.equal(element('rights-account').value,'','choosing Other… clears the typed field for a fresh entry');
  element('rights-account-select').value='root';
  element('rights-account-select').onchange();
  assert.equal(element('rights-account').hidden,true,'choosing the known default again hides the typed field');
  assert.equal(element('rights-account').value,'root');

  // task 2026-09-23, twelfth first-time visitor, requirement 2: on
  // arrival, Save rule's Program dropdown already offers every program
  // the Device page's own resource table names as a holder merged with
  // every program a loaded rule already names (allowSeen), each shown by
  // file name with the full path on its title, sorted, Other… last.
  assert.deepEqual(element('rights-program-select').children.map(o=>o.value),['/usr/bin/app','/usr/bin/python3','C:\\lms\\llama-server.exe','__other__'],'the dropdown already merges the Device holders and every program a loaded rule already names, on arrival, sorted with Other\u2026 last');
  assert.equal(element('rights-program-select').children[1].title,'/usr/bin/python3','the full path stays reachable as the option\'s own title');
  element('rights-program-select').value='/usr/bin/app';
  element('rights-program-select').onchange();
  assert.equal(element('rights-program').hidden,true,'choosing a known program keeps the typed field hidden');
  assert.equal(element('rights-program').value,'/usr/bin/app','choosing a known program syncs the typed field\'s own value, so the existing submit handler keeps reading it unchanged');
  element('rights-program-select').value='__other__';
  element('rights-program-select').onchange();
  assert.equal(element('rights-program').hidden,false,'choosing Other\u2026 reveals the typed field');
  assert.equal(element('rights-program').value,'','choosing Other\u2026 clears the typed field for a fresh entry');

  // task 2026-09-23, fifteenth first-time visitor, finding 6: Resource
  // opened as a bare free-text field with no hint what to type. It now
  // offers a dropdown built from the resources rules already loaded here
  // name for whichever action is chosen (rule and dupA/dupB above,
  // Storage content read on sha256:x, Model resolve on hf and civitai).
  element('rights-action').value='abstraction.storage/content.read';
  element('rights-action').onchange();
  assert.deepEqual(element('rights-resource-select').children.map(o=>o.value),['sha256:x','__other__'],'the resource chooser offers only the resources this one action\'s own loaded rules named');
  assert.equal(element('rights-resource').hidden,true,'a known resource keeps the typed field hidden');
  assert.equal(element('rights-resource').value,'sha256:x','the typed field stays in sync with the chosen resource, so the existing submit handler keeps reading it unchanged');
  element('rights-action').value='abstraction.model/lookup';
  element('rights-action').onchange();
  assert.deepEqual(element('rights-resource-select').children.map(o=>o.value),['civitai','hf','__other__'],'a different action offers that action\'s own resources instead, sorted, Other\u2026 last');
  element('rights-resource-select').value='__other__';
  element('rights-resource-select').onchange();
  assert.equal(element('rights-resource').hidden,false,'choosing Other\u2026 reveals the typed field');
  assert.equal(element('rights-resource').value,'','choosing Other\u2026 clears the typed field for a fresh entry');

  const group=element('rights').children[0];
  const rows=group.children.slice(1);
  assert.equal(rows.length,3,'every rule for this program is shown');
  assert(rows[0].children[0].textContent.startsWith('Storage: content read: allowed'),'a rule whose action is not repeated in this group carries no resource in its sentence; an action this runtime\'s catalogue names with no rightsActionPlain entry falls back to oaActionFallback\'s generic phrase, never a bare id in the visible text');
  assert(!rows[0].children[0].textContent.includes('abstraction.storage/content.read'),'the raw id is never visible text, even for the generic fallback phrase');
  assert(rows[1].children[0].textContent.startsWith('Model resolve on hf: allowed'),'a repeated action names its own resource, finding 2');
  assert(rows[2].children[0].textContent.startsWith('Model resolve on civitai: allowed'),'the sibling repeated rule names its own, different resource');
  // task 2026-09-23, eighth first-time visitor, finding 2: even truncated to
  // its first 8 hex characters, a revision is an identifier; it is no
  // longer visible text at all, only the rules list heading's own title and
  // this section's Technical details.
  const status = element('rights-status');
  assert(!status.children.some(c=>c.className==='oa-muted'&&/^Rules revision/.test(c.textContent)),'no visible line names the revision');
  assert.equal(element('rights-heading').title,'sha256:e3b0c44298fc1c149afbf4c8996fb924','the full revision stays reachable as the rules list heading\'s title');
  const revisionDetails = status.children.find(c=>c.children&&c.children[0]&&c.children[0].textContent==='Technical details');
  assert(revisionDetails && revisionDetails.children[1].textContent.includes('sha256:e3b0c44298fc1c149afbf4c8996fb924'),'the full revision also sits behind a Technical details disclosure');
  assert(!status.textContent.includes('More rules remain') && !status.textContent.includes('End of rules'), 'the inert remain/end sentence is dropped; the rights-next button already carries that state');
  assert.equal(element('rights-next').disabled,true,'this page is complete, so Next page stays disabled');

  // task 2026-09-23, seventh first-time visitor, finding 4: Allow with a
  // blank program path used to show no result at all.
  element('allow-program').dispatch('invalid');
  assert.equal(element('rights-status').textContent,'Program path is required to allow anything.','a blank required field is named, the Issue key pattern');

  // task 2026-09-23, eighth first-time visitor, finding 4: Save rule with a
  // blank required field also showed no result at all.
  element('rights-account').dispatch('invalid');
  assert.equal(element('rights-status').textContent,'Account is required.','a blank required Save rule field is named');
  element('rights-program').dispatch('invalid');
  assert.equal(element('rights-status').textContent,'Program path is required.');
  element('rights-resource').dispatch('invalid');
  assert.equal(element('rights-status').textContent,'Resource is required.');

  // task 2026-09-23, fourteenth first-time visitor, findings 2 and 3: Save
  // rule used to refuse ("List rules before editing.") unless Show rules
  // had been clicked first, even though rightsList already read it on
  // arrival above, and its own reply told the person to list again while
  // the table under it kept its stale rows. Neither happens now: the
  // fixture's account/program/action/resource fields are all already
  // filled from the arrival state and the earlier chooser checks.
  const rightsGetCountBeforeEdit=rightsGetCount;
  element('rights-program').value='/usr/bin/newapp';
  element('rights-resource').value='sha256:y';
  await element('rights-grant').onsubmit({preventDefault(){}});
  assert.equal(rightsCalls[0].account,'root');
  assert(!element('rights-status').textContent.includes('List rules before editing'),'no precondition blocked the edit; rightsList already ran on arrival');
  assert(!element('rights-status').textContent.includes('List again'),'the confirmation no longer tells the person to list again');
  assert(element('rights-status').textContent.startsWith('Rule set: '),'the edit still confirms what it set');
  assert.equal(rightsGetCount,rightsGetCountBeforeEdit+1,'a successful edit reads the rules list again itself');

  element('allow-for').value='downloads';element('allow-program').value='/usr/bin/app';element('allow-names').value='hf, ';element('allow-credentials').value='';
  await element('rights-allow').onsubmit({preventDefault(){}});
  assert.deepEqual(allowCalls[0],{for:'downloads',revision:'sha256:e3b0c44298fc1c149afbf4c8996fb924',account:'alice',program:'/usr/bin/app',registries:['hf'],hosts:[],credentials:[]});
  // task 2026-09-23, eleventh first-time visitor, finding 4: a
  // contract-shaped resource ("abstraction.job/acceptance@1") read as its
  // own raw id; it now reads through allowResourceLabel the same way a
  // rule row reads one through oaResourceLabel, except this confirmation
  // already knows the bundle it granted, so it names that instead of the
  // generic word, "the downloads service" (task 2026-09-23, twelfth
  // first-time visitor, finding 4). Each landed line no longer repeats
  // "permit": the sentence introducing them ("Allowed downloads for
  // app:") already says so once.
  assert(element('rights-status').textContent.includes('Job: acceptance submit on the downloads service'),'a landed allow rule reads in plain words (an action this fixture\'s ActionPlain does not map falls back to oaActionFallback\'s generic phrase), and a contract-shaped resource names the bundle this confirmation granted, not the raw action id or resource standing alone');
  assert(!element('rights-status').textContent.includes('Stopped at abstraction.model/lookup'),'the stopped rule also reads in plain words, not the raw action id');

  // task 2026-09-23, fourteenth first-time visitor, findings 2 and 3: a
  // second Allow, this one applied, reads the rules list again itself too.
  const rightsGetCountBeforeAllow=rightsGetCount;
  await element('rights-allow').onsubmit({preventDefault(){}});
  assert.deepEqual(allowCalls[1],allowCalls[0]);
  assert(element('rights-status').textContent.startsWith('Allowed downloads for app:'),'the applied allow still confirms what it granted');
  assert(!element('rights-status').textContent.includes('List again'),'the applied confirmation does not tell the person to list again');
  assert.equal(rightsGetCount,rightsGetCountBeforeAllow+1,'a successful allow reads the rules list again itself');
  console.log('PASS: Rights page questions answer with typed refusals; rules list disambiguates a repeated plain phrase by resource, revision is muted trailing text with no inert remain-sentence, allow bundle sends the listed revision, a blank required Allow field names itself');
 }

 // task 2026-09-23, fifteenth first-time visitor, finding 3: a rule Save
 // rule or Allow downloads just landed never appeared in the Rights list,
 // not on arrival and not on Refresh, though the confirmation said it was
 // set. This runtime already grants itself dozens of rules at
 // installation, so a freshly named program's own rule almost never sorts
 // onto the one page rightsCursor happened to be on; this fixture reaches
 // the same shape with two pages, the saved rule only on the second, to
 // prove Save rule now walks forward and shows the page it actually
 // landed on instead of leaving it unseen past the first page.
 {
  fields.clear();
  const exploreSelfReply = {self:{account:'root',program:'/usr/bin/openabstractions'},selfAccountName:'alice',probes:[]};
  const pageOneRule = {Subject:{Account:'root',Program:'/usr/bin/alreadyinstalled'},Action:'abstraction.storage/content.read',Resource:'sha256:a',Permit:true};
  const target = {Subject:{Account:'root',Program:'/usr/bin/verylateapp'},Action:'abstraction.model/lookup',Resource:'newregistry',Permit:true};
  const catalog = ['abstraction.storage/content.read','abstraction.model/lookup'];
  const actionPlain = {'abstraction.model/lookup':'model resolve'};
  const pageOne = {Outcome:'page', Revision:'rev-1', Catalog:catalog, ActionPlain:actionPlain, Rules:[pageOneRule], Next:'cursor-2', Complete:false};
  const pageTwo = {Outcome:'page', Revision:'rev-1', Catalog:catalog, ActionPlain:actionPlain, Rules:[target], Next:'', Complete:true};
  const questionsEmpty = {Outcome:'page', Records:[], Next:'', Complete:true};
  const cardTableEmpty = {resources:[]};
  let rightsGetCount=0, rightsGetCursors=[];
  const context = {URLSearchParams, localStorage:{length:0,key(){},getItem(){return null},setItem(){}}, document:{getElementById:element, createElement:node}, location:{search:'?k=test'}, console,
   fetch:async(path,opts)=>{
    if(path==='/card/table') return {ok:true,json:async()=>cardTableEmpty};
    if(path==='/explore') return {ok:true,json:async()=>exploreSelfReply};
    if(path.startsWith('/questions')) return {ok:true,json:async()=>questionsEmpty};
    if(path.startsWith('/rights')){
     if(!opts.body){
      rightsGetCount++;
      const cursor=path.split('cursor=')[1]||'';
      rightsGetCursors.push(cursor);
      return {ok:true,json:async()=>(cursor?pageTwo:pageOne)};
     }
     return {ok:true,json:async()=>({Outcome:'applied',Revision:'rev-2',Current:target})};
    }
    throw Error('unexpected request '+path);
   }};
  context.window = context; // window is the global object here, same as a browser (task 2026-09-23, fifteenth first-time visitor, finding 1)
  vm.runInNewContext(plainScript(), context);
  vm.runInNewContext(loadPageScript('rights_page.go'), context);
  await context.questionsReady;
  await context.rightsDeviceHoldersReady;
  await context.rightsSelfReady;
  await context.rightsListReady;

  const rightsGroups=()=>element('rights').children.filter(g=>g.className==='rights-program');
  assert.equal(rightsGetCount,1,'arrival reads only the first page, same as before this fix, since no rule was just saved');
  assert.equal(rightsGroups().length,1,'only the first page\'s one group is shown on arrival');
  assert.equal(rightsGroups()[0].children[0].children[0].title,'/usr/bin/alreadyinstalled','the first page\'s own program is the one shown');
  assert.equal(element('rights-next').disabled,false,'a second page remains, so Next page stays enabled');

  const rightsGetCountBeforeSave=rightsGetCount;
  element('rights-account').value='root';
  element('rights-program').value='/usr/bin/verylateapp';
  element('rights-action').value='abstraction.model/lookup';
  element('rights-resource').value='newregistry';
  await element('rights-grant').onsubmit({preventDefault(){}});
  assert.equal(rightsGetCount,rightsGetCountBeforeSave+2,'the save walked forward across both pages to find the rule it just set, not only the first');
  assert.deepEqual(rightsGetCursors.slice(-2),['','cursor-2'],'the walk restarted from the first page and followed Next into the second');
  assert.equal(element('rights-status').textContent.startsWith('Rule set: '),true,'the confirmation still says the rule was set');
  const groups=rightsGroups();
  assert.equal(groups.length,2,'both pages\' groups are shown together, the one the rule landed on included');
  const programs=groups.map(g=>g.children[0].children[0].title);
  assert(programs.includes('/usr/bin/verylateapp'),'the rule Save rule just set is now visible in the list itself, not only in the confirmation: '+programs.join(', '));
  assert.equal(element('rights-next').disabled,true,'the walk reached the second, complete page, so Next page is now disabled');
  console.log('PASS: Rights page a rule Save rule just set, landing past the first page, is shown in the list itself after the walk that finds it');
 }
})().catch(e=>{console.error(e);process.exitCode=1});
