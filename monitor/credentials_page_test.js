// Run with node monitor/credentials_page_test.js; no browser or service is started.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/credentials_page.go', 'utf8');
const script = go.split('<script>')[1].split('</script>')[0];
// plainScript (monitor/panel_shell.go) is embedded ahead of every page
// fragment in the real document; this isolated test loads it into the same
// vm context first, since credentials_page.go's own script calls
// oaShowError.
const shellGo = fs.readFileSync(__dirname + '/panel_shell.go', 'utf8');
const plainMarker = 'const plainScript = `';
const plainStart = shellGo.indexOf(plainMarker) + plainMarker.length;
assert(plainStart > plainMarker.length - 1, 'plainScript constant not found in panel_shell.go');
const plainEnd = shellGo.indexOf('`', plainStart);
const plainScript = shellGo.slice(plainStart, plainEnd).split('<script>')[1].split('</script>')[0];
const fields = new Map();
function node() {
 return {value:'', textContent:'', href:'', disabled:false, hidden:false, title:'', selectedOptions:[], listeners:{}, children:[], replaceChildren(){this.children=[]}, append(...v){this.children.push(...v)}, click(){}, reset(){}, addEventListener(type,fn){(this.listeners[type]=this.listeners[type]||[]).push(fn)}, dispatch(type){for(const fn of this.listeners[type]||[])fn()}};
}
function element(id) {
 if (!fields.has(id)) fields.set(id, node());
 return fields.get(id);
}

const secretOne = 'sekret-one-should-never-be-shown-8b1a';
const secretTwo = 'sekret-two-should-never-be-shown-4d7e';

const activeRecord = {Name:'hf', Kind:'bearer', Header:'', State:'active', Revision:'rev-1',
 Scope:{Targets:['huggingface.co'], Consumers:['abstraction.download/http-execution@1']},
 Expires:'', LastApplied:'', RegisteredBy:{Account:'acct', Program:'/usr/bin/app'}, Registered:'2024-01-01T00:00:00.000000Z'};
const revokedRecord = {Name:'old', Kind:'header', Header:'X-Api-Key', State:'revoked', Revision:'rev-0',
 Scope:{Targets:['example.com'], Consumers:['abstraction.model/resolver@1']},
 Expires:'', LastApplied:'', RegisteredBy:{Account:'acct', Program:'/usr/bin/app'}, Registered:'2024-01-01T00:00:00.000000Z'};
// task 2026-09-23, eleventh first-time visitor, finding 7: a gateway key's
// own credential name ("local-key.<hash>.<random>") is meaningless as
// visible text; it names the target program only in Inference's own key
// list (fetched once, best-effort, alongside the credentials list), never
// in the credential record itself.
const gatewayKeyRecord = {Name:'local-key.7d0c0be6.a1b2', Kind:'openabstractions/local-key@1', Header:'', State:'active', Revision:'rev-4',
 Scope:{Targets:['localhost'], Consumers:['abstraction.inference/complete@1']},
 Expires:'', LastApplied:'', RegisteredBy:{Account:'acct', Program:'/usr/bin/app'}, Registered:'2024-01-01T00:00:00.000000Z'};
const inferenceKeysPage = {Outcome:'page', Keys:[{Program:'/usr/bin/python3', Name:'local-key.7d0c0be6.a1b2', Credential:'', IssuedUnixMs:0, IssuedBy:''}]};
const listPage = {Outcome:'page', Limits:{SecureStore:'file-0600', SupportedKinds:['bearer','header'], MaxSecretBytes:4096, MaxCredentials:64}, Records:[activeRecord, revokedRecord, gatewayKeyRecord], Next:'', Complete:true};

const credentialCalls = [];
const addReply = {Outcome:'stored', Revision:'rev-2', Current:activeRecord};
const rotateReply = {Outcome:'rotated', Revision:'rev-3', Current:activeRecord};
const revokeReply = {Outcome:'revoked', Revision:'rev-1'};

const rightsCalls = [];
const rightsPage = {Outcome:'page', Revision:'policy-rev-1', Catalog:['abstraction.credentials/apply'], Rules:[], Next:'', Complete:true};
const rightsEditReply = {Outcome:'applied', Revision:'policy-rev-2', Current:{Subject:{Account:'alice',Program:'/usr/bin/app'},Action:'abstraction.credentials/apply',Resource:'credential:hf',Permit:true}};

const context = {URLSearchParams, document:{getElementById:element, createElement:node}, location:{search:'?k=test'}, console,
 fetch: async (path, opts) => {
  if (path.startsWith('/inference/keys')) return {ok:true, json:async()=>inferenceKeysPage};
  // task 2026-09-23, fourteenth first-time visitor, finding 8: App access'
  // own Program path and Account fields now build the same choosers
  // Rights' own Program and Account fields already use.
  if (path === '/card/table') return {ok:true, json:async()=>({resources:[{resource:'card:0', holders:[{program:'C:\\lms\\llama-server.exe'}]}]})};
  if (path === '/explore') return {ok:true, json:async()=>({self:{account:'root', program:'/usr/bin/openabstractions'}, selfAccountName:'reinis', probes:[]})};
  if (path.startsWith('/credentials')) {
   if (!opts || !opts.body) return {ok:true, json:async()=>listPage};
   const body = JSON.parse(opts.body);
   credentialCalls.push(body);
   if (body.action === 'add' && body.name === '__fail_name__') return {ok:false, text: async()=>'forbidden: abstraction.credentials/holder.manage not permitted for this Panel'};
   if (body.action === 'add') return {ok:true, json:async()=>addReply};
   if (body.action === 'rotate') return {ok:true, json:async()=>rotateReply};
   if (body.action === 'revoke') return {ok:true, json:async()=>revokeReply};
   throw Error('unexpected credential action ' + body.action);
  }
  if (path.startsWith('/rights')) {
   if (!opts || !opts.body) return {ok:true, json:async()=>rightsPage};
   const body = JSON.parse(opts.body);
   rightsCalls.push(body);
   return {ok:true, json:async()=>rightsEditReply};
  }
  throw Error('unexpected fetch ' + path);
 }};
context.window = context; // window is the global object here, same as a browser (task 2026-09-23, fifteenth first-time visitor, finding 1)
vm.runInNewContext(plainScript, context);
vm.runInNewContext(script, context);

// storeLabel's known-word mapping, exercised directly (the fixtures above
// only cover 'file-0600', which has no friendly name of its own).
assert.equal(context.storeLabel('windows-credential-manager'), 'Windows Credential Manager');
assert.equal(context.storeLabel('macos-keychain'), 'macOS Keychain');
assert.equal(context.storeLabel('some-future-store'), 'some-future-store', 'an unrecognized store word still shows, unchanged');

(async () => {
 assert.equal(element('back').href, '/?k=test');
 // The rule-editor mention is a link, not a name with nowhere to go (task
 // 2026-09-23 finding 1: "the same rule editor as Rights on the Status page"
 // sent the reader to another page with no link). It also names the page the
 // way the nav does: Rights moved off the Status page to its own page,
 // "Questions and rights" (task 2026-09-23, fifth first-time visitor, finding
 // 3), so both the link text and its target now point there.
 assert.equal(element('rights-editor-link').href, '/rights?k=test#rights-section');

 // task 2026-09-23, fourteenth first-time visitor, findings 2 and 3: Saved
 // credentials used to open empty until Show credentials was clicked; it
 // now reads on arrival, "Show credentials" renamed Refresh.
 await context.listReady;
 assert.equal(element('records').children.length, 3, 'every record rendered, tombstone and gateway key included, with no click');

 // task 2026-09-23, fourteenth first-time visitor, finding 8: Program path
 // and Account were bare text boxes on App access while Rights already
 // offered choosers for both; both now build their own equivalent chooser
 // from oaProgramChooser and oaAccountChooser (panel_shell.go), the one
 // shared implementation each, seeded the same way Rights' own choosers
 // are.
 await context.allowChoosersReady;
 assert.deepEqual(element('allow-program-select').children.map(o=>o.value), ['C:\\lms\\llama-server.exe','__other__'], 'the program chooser opens with the Device holders this runtime could read, Other\u2026 last');
 assert.deepEqual(element('allow-account-select').children.map(o=>[o.value,o.textContent]), [['root','this account, reinis'],['__other__','Other…']], 'the account chooser defaults to this account by name, Other\u2026 last');
 assert.equal(element('allow-account').hidden, true, 'the typed account field stays hidden while the known default is selected');
 assert.equal(element('allow-account').value, 'root');
 const activeBox = element('records').children[0];
 // task 2026-09-23, tenth first-time visitor, finding 3: a record's row
 // used to concatenate its id, kind, revision, targets, raw consumer
 // contract ids, the full registered-by path and a raw timestamp onto one
 // dense block. It now reads one summary line, a consumers line in plain
 // words, and every raw value behind Technical details.
 assert.equal(activeBox.children[0].textContent, 'hf · active · registered by app '+context.oaLocalTime('2024-01-01T00:00:00.000000Z'), 'the summary line names the record, its state, and who registered it, by file name and readable time');
 assert.equal(activeBox.children[0].title, 'id: hf; /usr/bin/app; registered 2024-01-01T00:00:00.000000Z', 'the full path and exact timestamp sit on the summary line\'s own title, the raw name (redundant for an ordinary credential) is also there for a gateway key row that needs to name what its own hashed id was');
 assert.equal(activeBox.children[1].textContent, 'Used by: downloads.', 'the raw consumer contract id reads as a plain phrase');
 const activeDetails = activeBox.children[2];
 assert.equal(activeDetails.children[0].textContent, 'Technical details');
 for (const raw of ['bearer', 'rev-1', 'huggingface.co', 'abstraction.download/http-execution@1', '/usr/bin/app'])
  assert(activeDetails.children[1].textContent.includes(raw), raw + ' missing from Technical details: ' + activeDetails.children[1].textContent);
 // task 2026-09-23, fourteenth first-time visitor, finding 7: a fourth
 // child, a plain note, now follows the three buttons, naming which one
 // was chosen right beside them.
 assert.equal(activeBox.children.length, 7, 'an active record offers rotate, revoke and allow buttons, and a chosen-for note, after the summary, consumers line and Technical details');
 const revokedBox = element('records').children[1];
 // consumerServiceName (credentials_page.go) names a consumer the same word
 // the Consumers chooser above uses for it (task 2026-09-23, twelfth
 // first-time visitor, finding 5): "model resolve", not oaServiceName's
 // own generic "model lookup" for the bare model layer.
 assert.equal(revokedBox.children[1].textContent, 'Used by: model resolve.');
 assert.equal(revokedBox.children.length, 3, 'a revoked record offers no conditional-edit buttons');
 // task 2026-09-23, eleventh first-time visitor, finding 7: a gateway key
 // registers a credential named "local-key.<hash>.<random>"; it now reads
 // "Gateway key for <file name>", the program read from Inference's own
 // key list, with the raw id kept on the summary line's own title.
 const gatewayBox = element('records').children[2];
 assert.equal(gatewayBox.children[0].textContent, 'Gateway key for python3 · active · registered by app '+context.oaLocalTime('2024-01-01T00:00:00.000000Z'));
 assert.equal(gatewayBox.children[0].title, 'id: local-key.7d0c0be6.a1b2; /usr/bin/app; registered 2024-01-01T00:00:00.000000Z');
 // The limits line reads as a sentence, not a "store: ...; kinds: ...; max
 // secret bytes: ...; max credentials: ..." field dump (task 2026-09-23
 // finding 5).
 assert.equal(element('limits').textContent, 'Secrets are kept in a local file, restricted to this account. Up to 64 credentials of kind bearer or header, each up to 4,096 bytes.');

 element('add-name').value = 'newcred'; element('add-kind').value = 'bearer';
 // task 2026-09-23, twelfth first-time visitor, requirement 1: Consumers
 // is now a multi-select of known consumers, plain phrases with the
 // contract id as each option's own value.
 element('add-targets').value = 'a.com'; element('add-consumers-select').selectedOptions = [{value:'abstraction.download/http-execution@1'}];
 element('add-secret').value = secretOne;
 await element('add-form').onsubmit({preventDefault(){}});
 assert.equal(element('add-secret').value, '', 'the secret field is cleared once sent');
 assert.deepEqual(credentialCalls[0], {action:'add', name:'newcred', kind:'bearer', header:'', targets:['a.com'], consumers:['abstraction.download/http-execution@1'], expires:'', secret:secretOne, expected_revision:''});
 // task 2026-09-23, tenth first-time visitor, finding 1: "Stored newcred at
 // revision rev-2" printed the new revision as visible text; it now reads
 // "Stored newcred.", the revision on the line's own title and behind
 // Technical details. task 2026-09-23, fourteenth first-time visitor,
 // finding 3: it also used to add "List again to see it." while the list
 // under it kept its stale rows; the list reads itself again instead, so
 // that instruction goes too.
 assert.equal(element('add-status').textContent, 'Stored newcred.');
 assert.equal(element('add-status').title, 'Revision rev-2');
 assert.equal(element('add-status').children[0].children[0].textContent, 'Technical details');
 assert(element('add-status').children[0].children[1].textContent.includes('rev-2'));
 assert(!element('add-status').textContent.includes(secretOne), 'the reply never echoes the secret');
 assert.equal(element('records').children.length, 3, 'the list read itself again after a successful add, with no click');

 // task 2026-09-23, thirteenth first-time visitor, finding 5: these three
 // buttons used to set the target field's own visible value directly, a
 // raw name a gateway key never made friendly to read (fine for an
 // ordinary name like "hf", the same value a person already chose).
 // They now go through a chooser, built from the credentials this page
 // has listed; the hidden field they still fill keeps reading unchanged.
 const rotateButton = activeBox.children[3];
 assert.equal(rotateButton.textContent, 'Use for rotate');
 rotateButton.onclick();
 assert.equal(element('rotate-name-select').value, 'hf', 'the chooser selects the known name');
 assert.equal(element('rotate-name-select').children.find(o=>o.value==='hf').textContent, 'hf', 'an ordinary name\'s own friendly label is the name itself');
 assert.equal(element('rotate-name').hidden, true, 'the hidden field stays hidden for a known name');
 assert.equal(element('rotate-name').value, 'hf');
 assert.equal(element('rotate-revision').value, 'rev-1');
 // task 2026-09-23, fourteenth first-time visitor, finding 7: the button
 // used to fill a field far below with no visible change here; it now
 // names what it chose right beside itself, the note appended as the
 // fourth child, after the three buttons.
 assert.equal(activeBox.children[6].textContent, 'Chosen for rotate: hf.', 'the note beside the buttons names what was chosen');
 element('rotate-secret').value = secretTwo;
 await element('rotate-form').onsubmit({preventDefault(){}});
 assert.equal(element('rotate-secret').value, '', 'the secret field is cleared once sent');
 assert.deepEqual(credentialCalls[1], {action:'rotate', name:'hf', expected_revision:'rev-1', expires:'', secret:secretTwo});
 // task 2026-09-23, thirteenth first-time visitor, finding 6: the
 // confirmation used to print "Rotated hf to revision rev-3." with the
 // revision as visible text; the revision now sits in Technical details,
 // through oaShowResult, the same shape every other confirmation uses.
 assert.equal(element('rotate-status').textContent, 'Rotated hf.');
 assert.equal(element('rotate-status').title, 'id: hf; revision rev-3');
 assert.equal(element('rotate-status').children[0].children[0].textContent, 'Technical details');
 assert(!element('rotate-status').textContent.includes(secretTwo));

 const revokeButton = activeBox.children[4];
 assert.equal(revokeButton.textContent, 'Use for revoke');
 revokeButton.onclick();
 assert.equal(element('revoke-name-select').value, 'hf');
 assert.equal(element('revoke-name').value, 'hf');
 assert.equal(element('revoke-revision').value, 'rev-1');
 await element('revoke-form').onsubmit({preventDefault(){}});
 assert.deepEqual(credentialCalls[2], {action:'revoke', name:'hf', expected_revision:'rev-1'});
 assert.equal(element('revoke-status').textContent, 'Revoked hf.');
 assert.equal(element('revoke-status').title, 'id: hf; revision rev-1');
 assert.equal(element('revoke-status').children[0].children[0].textContent, 'Technical details');

 // A gateway key's own name reads through credentialSentenceName's
 // sentence-shaped phrase, "the gateway key for <program>", the same
 // program metaBoxName above already named this same credential by.
 const gatewayRevokeButton = gatewayBox.children[4];
 assert.equal(gatewayRevokeButton.textContent, 'Use for revoke');
 gatewayRevokeButton.onclick();
 assert.equal(element('revoke-name-select').value, 'local-key.7d0c0be6.a1b2', 'the chooser selects the gateway key\'s own raw name');
 assert.equal(element('revoke-name-select').children.find(o=>o.value==='local-key.7d0c0be6.a1b2').textContent, 'Gateway key for python3', 'the option\'s own text is the same friendly name the saved list above already reads this credential as');
 assert.equal(element('revoke-name').value, 'local-key.7d0c0be6.a1b2', 'the hidden field still carries the raw id the service needs');
 await element('revoke-form').onsubmit({preventDefault(){}});
 assert.deepEqual(credentialCalls[3], {action:'revoke', name:'local-key.7d0c0be6.a1b2', expected_revision:'rev-4'});
 assert.equal(element('revoke-status').textContent, 'Revoked the gateway key for python3.', 'a gateway key\'s own confirmation reads the friendly sentence, not its raw name');
 assert.equal(element('revoke-status').title, 'id: local-key.7d0c0be6.a1b2; revision rev-1');

 // task 2026-09-23, fourteenth first-time visitor, findings 2 and 3: App
 // access Save used to refuse ("List rights policy before allowing.")
 // unless Show policy had been clicked first, and Show policy itself
 // wrote nothing about the policy at all until clicked; allowPolicyList
 // now reads it on arrival, so an empty policy already reads "No app has
 // access yet." with no click.
 await context.allowPolicyReady;
 assert.equal(element('allow-status').textContent, 'No app has access yet.');
 assert.equal(element('allow-status').title, 'Policy revision policy-rev-1');

 const allowButton = activeBox.children[5];
 assert.equal(allowButton.textContent, 'Allow/block this name');
 allowButton.onclick();
 assert.equal(element('allow-name').value, 'hf');
 // A rule that names this credential's own apply action reads as a
 // sentence, not a bare revision line. Refresh (Show policy) still re-reads
 // it manually.
 rightsPage.Rules = [{Subject:{Account:'alice',Program:'/usr/bin/app'},Action:'abstraction.credentials/apply',Resource:'credential:hf',Permit:true}];
 await element('allow-list').onclick();
 // task 2026-09-23, fifteenth first-time visitor, finding 4: this line used
 // to read the raw full program path and the raw account SID, a different
 // formatter than Save's own confirmation below used; both now read
 // through the one accessSentence, file name and "this account"/the plain
 // account, and each granted rule is its own row in allow-rules instead of
 // being joined into the status line, which now stays blank once rows
 // exist.
 assert.equal(element('allow-status').textContent, '');
 assert.equal(element('allow-rules').children.length, 1, 'the granted rule is its own row');
 assert.equal(element('allow-rules').children[0].textContent, 'Allowed app to apply hf for alice.');
 assert.equal(element('allow-rules').children[0].title, '/usr/bin/app; alice; credential:hf', 'the raw path, account and resource stay reachable as the row\'s own title');
 rightsPage.Rules = [];
 element('allow-account').value = 'alice'; element('allow-program').value = '/usr/bin/app'; element('allow-permit').value = 'permit';
 await element('allow-form').onsubmit({preventDefault(){}});
 assert.deepEqual(rightsCalls[0], {edit:'set', revision:'policy-rev-1', account:'alice', program:'/usr/bin/app', action:'abstraction.credentials/apply', resource:'credential:hf', permit:true});
 // task 2026-09-23, tenth first-time visitor, finding 1: "Allowed
 // /usr/bin/app to apply hf at revision policy-rev-1" printed the full
 // program path and the revision as visible text; it now names the program
 // by its file name, with the path in Technical details and the revision
 // on the line's own title (task 2026-09-23, thirteenth first-time
 // visitor, finding 6: the title now also carries the raw credential id,
 // "hf" here unchanged since it is already the name a person chose).
 // task 2026-09-23, fourteenth first-time visitor, finding 3: it also used
 // to add "List rights policy again before another edit." while the
 // policy list under it kept its stale state; the policy list reads
 // itself again instead.
 assert.equal(element('allow-status').textContent, 'Allowed app to apply hf for alice.', 'Save\'s own confirmation now reads through the same accessSentence the list rows do, account included');
 assert.equal(element('allow-status').title, 'Program: /usr/bin/app; Account: alice; id: hf; revision policy-rev-1', 'the raw program path and account sit in Technical details, not the visible sentence');
 assert.equal(element('allow-status').children[0].children[0].textContent, 'Technical details');

 // A raw refusal from the runtime (not the credentials service's own curated
 // "forbidden" outcome) is shown as a plain sentence naming the rule this
 // Panel needs, never the raw backend text; that text stays available
 // behind a "Technical details" disclosure (task 2026-09-23 finding 1).
 element('add-name').value = '__fail_name__'; element('add-kind').value = 'bearer';
 element('add-targets').value = 'a.com'; element('add-consumers-select').selectedOptions = [{value:'abstraction.download/http-execution@1'}];
 element('add-secret').value = secretOne;
 await element('add-form').onsubmit({preventDefault(){}});
 assert.equal(element('add-status').textContent, 'The runtime did not permit this Panel to do this; nothing was changed. It needs the rule abstraction.credentials/holder.manage.');
 const addDetails = element('add-status').children[0];
 assert.equal(addDetails.className, 'oa-error-details');
 assert.match(addDetails.children[1].textContent, /not permitted for this Panel/);

 for (const id of ['list-status','limits','add-status','rotate-status','revoke-status','allow-status']) {
  assert(!element(id).textContent.includes(secretOne), id + ' carried the secret');
  assert(!element(id).textContent.includes(secretTwo), id + ' carried the secret');
 }

 // task 2026-09-23, seventh first-time visitor, finding 4: Add, Rotate and
 // Revoke with a blank required field used to show no result at all.
 element('add-name').dispatch('invalid');
 assert.equal(element('add-status').textContent, 'Name is required.', 'a blank required Add field is named, the Issue key pattern');
 element('add-consumers-select').dispatch('invalid');
 assert.equal(element('add-status').textContent, 'Consumers is required.', 'the now-required select names itself the same way');
 // task 2026-09-23, twelfth first-time visitor, requirement 1: Consumers
 // lists the credentials module's own known consumers and every inference
 // capability a gateway key can be issued for, each a plain phrase with
 // the contract id as that option's own value and title; "Other…" reveals
 // the typed field.
 const consumerOptions = element('add-consumers-select').children;
 assert.equal(consumerOptions.length, 9, 'eight known consumers plus Other…');
 // Each option's own phrase reads through consumerServiceName, the same
 // function the saved list above reads a stored consumer id through, so
 // the two never carry a different word for the same consumer (task
 // 2026-09-23, twelfth first-time visitor, finding 5).
 assert.deepEqual(consumerOptions.slice(0, 3).map(o => [o.value, o.textContent]),
  [['abstraction.download/http-execution@1', 'downloads'], ['abstraction.model/resolver@1', 'model resolve'], ['abstraction.inference/chat@1', 'chat']]);
 assert.equal(consumerOptions[0].title, 'abstraction.download/http-execution@1', 'the raw id sits on the option\'s own title too');
 assert.equal(consumerOptions.at(-1).value, '__other__');
 assert.equal(element('add-consumers').hidden, true, 'the typed field stays hidden until Other… is chosen');
 element('add-consumers-select').selectedOptions = [{value: '__other__'}];
 element('add-consumers-select').onchange();
 assert.equal(element('add-consumers').hidden, false, 'choosing Other… reveals the typed field');
 element('add-consumers-select').selectedOptions = [{value: 'abstraction.download/http-execution@1'}];
 element('add-consumers-select').onchange();
 assert.equal(element('add-consumers').hidden, true, 'choosing a known consumer instead hides it again');
 element('rotate-name').dispatch('invalid');
 assert.equal(element('rotate-status').textContent, 'Name is required.', 'a blank required Rotate field is named');
 element('revoke-revision').dispatch('invalid');
 assert.equal(element('revoke-status').textContent, 'Revision is required.', 'a blank required Revoke field is named');
 // task 2026-09-23, eighth first-time visitor, finding 4: App access ->
 // Save with a blank required field also showed no result at all.
 element('allow-name').dispatch('invalid');
 assert.equal(element('allow-status').textContent, 'Credential name is required.', 'a blank required Save field is named, the Issue key pattern');
 element('allow-account').dispatch('invalid');
 assert.equal(element('allow-status').textContent, 'Account is required.');
 element('allow-program').dispatch('invalid');
 assert.equal(element('allow-status').textContent, 'Program path is required.');

 console.log('PASS: credentials page lists metadata and limits, adds/rotates/revokes at the listed revision, allows through /rights at the listed policy revision, never echoes a secret, and names a blank required field on each form, App access Save included');
})().catch(e => { console.error(e); process.exitCode = 1; });
