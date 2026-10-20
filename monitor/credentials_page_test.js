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
 return {value:'', textContent:'', href:'', disabled:false, hidden:false, title:'', selectedOptions:[], listeners:{}, children:[], replaceChildren(...v){this.children=[...v]}, append(...v){this.children.push(...v)}, click(){}, reset(){}, addEventListener(type,fn){(this.listeners[type]=this.listeners[type]||[]).push(fn)}, dispatch(type){for(const fn of this.listeners[type]||[])fn()}};
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

const context = {URLSearchParams, document:{getElementById:element, createElement:t=>Object.assign(node(),{tagName:String(t).toUpperCase()})}, location:{search:'?k=test'}, console,
 fetch: async (path, opts) => {
  if (path.startsWith('/inference/keys')) return {ok:true, json:async()=>inferenceKeysPage};
  // task 2026-09-23, fourteenth first-time visitor, finding 8: App access'
  // own Program path and Account fields now build the same choosers
  // Rights' own Program and Account fields already use. The program
  // chooser lists registered applications, never the graphics card's
  // running holders.
  if (path === '/card/table') return {ok:true, json:async()=>({resources:[{resource:'card:0', holders:[{program:'C:\\Windows\\System32\\csrss.exe'},{program:'C:\\Windows\\System32\\dwm.exe'}]}]})};
  if (path === '/apps/registrations?source=applications&wait_ms=0') return {ok:true, json:async()=>({outcome:'page', entries:[{program:'C:\\lms\\llama-server.exe', name:'', role:'application'}]})};
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
assert.equal(context.storeLabel('windows-dpapi-file'), 'an encrypted file for this Windows account');
assert.equal(context.storeLabel('macos-login-keychain'), 'the macOS login Keychain');
assert.equal(context.storeLabel('some-future-store'), 'some-future-store', 'an unrecognized store word still shows, unchanged');

function pressOnly(value){for(const b of element('add-consumers-select').children)if(!!b.pressed!==(b.value===value))b.onclick()}
(async () => {
 // SPEC §4 (S8): one caption, no "Back to Status", no Refresh button, and
 // every text field but the secret waits behind a chooser.
 {
  const markup = go.slice(go.indexOf('var credentialsPage = '), go.indexOf('<script>'));
  assert(markup.includes('"Keys your apps use for hosted services."'), 'the page caption');
  assert(!markup.includes('id="back"') && !markup.includes('Refresh'), 'no back link and no Refresh button');
  for (const m of markup.matchAll(/<input[^>]*>/g)) assert(/\bhidden\b|data-oa-secret/.test(m[0]), 'a visible input that is not a secret: ' + m[0]);
 }
 await context.addForReady;
 // Nothing is typed until the person asks: the form opens from a row action.
 assert.equal(element('add-form').hidden, true, 'the add form is closed on arrival');
 element('add-open').onclick();
 assert.equal(element('add-form').hidden, false);
 assert.equal(element('add-open-status').textContent, 'Fill in the form below, then Add.');
 assert.equal(element('allow-form').hidden, true, 'the app access form is closed on arrival');
 element('allow-open').onclick();
 assert.equal(element('allow-open-status').textContent, 'Choose a credential and an app, then Save.');
 assert.deepEqual([...element('add-consumers-select').children].filter(b=>b.pressed).map(b=>b.value), ['abstraction.inference/chat@1'], 'chat is on by default');
 assert.deepEqual(element('add-for').children.map(o=>o.value), ['__other__'], 'with no hosted server, For offers only Other…');
 assert.equal(element('add-name').hidden, false, 'Other… shows the typed name');

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
 assert.deepEqual(element('allow-program-select').children.map(o=>o.value), ['C:\\lms\\llama-server.exe','__other__'], 'the program chooser opens with the registered applications, Other\u2026 last');
 assert.deepEqual(element('allow-account-select').children.map(o=>[o.value,o.textContent]), [['root','this account, reinis'],['__other__','Other…']], 'the account chooser defaults to this account by name, Other\u2026 last');
 assert.equal(element('allow-account').hidden, true, 'the typed account field stays hidden while the known default is selected');
 assert.equal(element('allow-account').value, 'root');
 // Each saved credential is one oa-row: name, a caption (state, who
 // registered it, what uses it) with the raw values on its title and
 // behind Technical details, and the row actions Replace key… and Remove.
 const activeBox = element('records').children[0];
 assert.equal(activeBox.className, 'oa-row');
 assert.equal(activeBox.label.textContent, 'hf');
 assert.equal(activeBox.caption.textContent, 'active · registered by app '+context.oaWhen('2024-01-01T00:00:00.000000Z')+' · used by downloads');
 assert.equal(activeBox.caption.title, 'id: hf; /usr/bin/app; registered 2024-01-01T00:00:00.000000Z');
 for (const raw of ['bearer', 'rev-1', 'huggingface.co', 'abstraction.download/http-execution@1', '/usr/bin/app'])
  assert(activeBox.pre.textContent.includes(raw), raw + ' missing from Technical details');
 assert.equal(activeBox.replace.textContent, 'Replace key…');
 assert.equal(activeBox.remove.textContent, 'Remove');
 const revokedBox = element('records').children[1];
 assert.equal(revokedBox.caption.textContent, 'revoked · registered by app '+context.oaWhen('2024-01-01T00:00:00.000000Z')+' · used by model resolve');
 assert.equal(revokedBox.replace.hidden && revokedBox.remove.hidden, true, 'a revoked record offers no row actions');
 const gatewayBox = element('records').children[2];
 assert.equal(gatewayBox.label.textContent, 'Gateway key for python3');
 assert.equal(gatewayBox.caption.title, 'id: local-key.7d0c0be6.a1b2; /usr/bin/app; registered 2024-01-01T00:00:00.000000Z');
 // The limits line reads as a sentence, not a "store: ...; kinds: ...; max
 // secret bytes: ...; max credentials: ..." field dump (task 2026-09-23
 // finding 5).
 assert.equal(element('limits').textContent, 'Secrets are kept in a local file, restricted to this account. Up to 64 credentials of kind bearer or header, each up to 4,096 bytes.');

 element('add-name').value = 'newcred'; element('add-kind').value = 'bearer';
 // task 2026-09-23, twelfth first-time visitor, requirement 1: Consumers
 // is now a multi-select of known consumers, plain phrases with the
 // contract id as each option's own value.
 element('add-targets').value = 'a.com'; pressOnly('abstraction.download/http-execution@1');
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

 // Replace key… opens the row's editor; Save sends the secret at the
 // revision the row read and writes one sentence into the row.
 activeBox.replace.onclick();
 assert.equal(activeBox.editor.hidden, false);
 assert.equal(activeBox.status.textContent, 'Type the new key, then Save.');
 activeBox.secret.value = secretTwo;
 await activeBox.save.onclick();
 assert.equal(activeBox.secret.value, '', 'the secret field is cleared once sent');
 assert.deepEqual(credentialCalls[1], {action:'rotate', name:'hf', expected_revision:'rev-1', expires:'', secret:secretTwo});
 assert.equal(activeBox.status.textContent, 'Replaced the key for hf.');
 assert.equal(activeBox.status.title, 'id: hf; revision rev-3');
 assert(!activeBox.status.textContent.includes(secretTwo));
 // Remove asks for a second click, then revokes at the listed revision.
 await activeBox.remove.onclick();
 assert.equal(activeBox.status.textContent, 'Click Remove again to delete hf.');
 assert.equal(credentialCalls.length, 2, 'the first click sends nothing');
 await activeBox.remove.onclick();
 assert.deepEqual(credentialCalls[2], {action:'revoke', name:'hf', expected_revision:'rev-1'});
 assert.equal(activeBox.status.textContent, 'Removed hf.');
 await gatewayBox.remove.onclick(); await gatewayBox.remove.onclick();
 assert.deepEqual(credentialCalls[3], {action:'revoke', name:'local-key.7d0c0be6.a1b2', expected_revision:'rev-4'});
 assert.equal(gatewayBox.status.textContent, 'Removed the gateway key for python3.', 'a gateway key reads its friendly sentence');

 // task 2026-09-23, fourteenth first-time visitor, findings 2 and 3: App
 // access Save used to refuse ("List rights policy before allowing.")
 // unless Show policy had been clicked first, and Show policy itself
 // wrote nothing about the policy at all until clicked; allowPolicyList
 // now reads it on arrival, so an empty policy already reads "No app has
 // access yet." with no click.
 await context.allowPolicyReady;
 assert.equal(element('allow-status').textContent, 'No app has access yet.');
 assert.equal(element('allow-status').title, 'Policy revision policy-rev-1');

 element('allow-name-select').value = 'hf'; element('allow-name-select').onchange();
 assert.equal(element('allow-name').value, 'hf');
 assert.equal(element('allow-name').value, 'hf');
 // A rule that names this credential's own apply action reads as a
 // sentence, not a bare revision line. Refresh (Show policy) still re-reads
 // it manually.
 rightsPage.Rules = [{Subject:{Account:'alice',Program:'/usr/bin/app'},Action:'abstraction.credentials/apply',Resource:'credential:hf',Permit:true}];
 await context.allowPolicyList();
 // task 2026-09-23, fifteenth first-time visitor, finding 4: this line used
 // to read the raw full program path and the raw account SID, a different
 // formatter than Save's own confirmation below used; both now read
 // through the one accessSentence, file name and "this account"/the plain
 // account, and each granted rule is its own row in allow-rules instead of
 // being joined into the status line, which now stays blank once rows
 // exist.
 assert.equal(element('allow-status').textContent, '');
 assert.equal(element('allow-rules').children.length, 1, 'the granted rule is its own row');
 assert.equal(element('allow-rules').children[0].textContent, 'Allowed app to apply hf for another account on this device.');
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
 assert.equal(element('allow-save-status').textContent, 'Allowed app to apply hf for another account on this device.', 'Save\'s own confirmation now reads through the same accessSentence the list rows do, account included');
 assert.equal(element('allow-save-status').title, 'Program: /usr/bin/app; Account: alice; id: hf; revision policy-rev-1', 'the raw program path and account sit in Technical details, not the visible sentence');
 assert.equal(element('allow-save-status').children[0].children[0].textContent, 'Technical details');

 // A raw refusal from the runtime (not the credentials service's own curated
 // "forbidden" outcome) is shown as a plain sentence naming the rule this
 // Panel needs, never the raw backend text; that text stays available
 // behind a "Technical details" disclosure (task 2026-09-23 finding 1).
 element('add-name').value = '__fail_name__'; element('add-kind').value = 'bearer';
 element('add-targets').value = 'a.com'; pressOnly('abstraction.download/http-execution@1');
 element('add-secret').value = secretOne;
 await element('add-form').onsubmit({preventDefault(){}});
 assert.equal(element('add-status').textContent, 'The runtime did not permit this Panel to do this; nothing was changed. It needs the “credentials holder manage” permission.');
 const addDetails = element('add-status').children[0];
 assert.equal(addDetails.className, 'oa-error-details');
 assert.match(addDetails.children[1].textContent, /not permitted for this Panel/);

 for (const id of ['list-status','limits','add-status','allow-status','allow-save-status']) {
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
 // Used for: toggle buttons, chat on by default, Other… last.
 const consumerOptions = element('add-consumers-select').children;
 assert.equal(consumerOptions.length, 9, 'eight known consumers plus Other…');
 assert.deepEqual(consumerOptions.slice(0, 3).map(o => [o.value, o.textContent]),
  [['abstraction.download/http-execution@1', 'downloads'], ['abstraction.model/resolver@1', 'model resolve'], ['abstraction.inference/chat@1', 'chat']]);
 assert.equal(consumerOptions[0].title, 'abstraction.download/http-execution@1', 'the raw id sits on the button\'s own title');
 assert.equal(consumerOptions.at(-1).value, '__other__');
 assert.equal(element('add-consumers').hidden, true, 'the typed field stays hidden until Other… is pressed');
 consumerOptions.at(-1).onclick();
 assert.equal(element('add-consumers').hidden, false, 'pressing Other… reveals the typed field');
 assert.equal(element('add-consumers-status').textContent, 'Other… on.', 'a press writes one sentence');
 consumerOptions.at(-1).onclick();
 assert.equal(element('add-consumers').hidden, true, 'pressing it again hides it');
 // task 2026-09-23, eighth first-time visitor, finding 4: App access ->
 // Save with a blank required field also showed no result at all.
 element('allow-name').dispatch('invalid');
 assert.equal(element('allow-save-status').textContent, 'Credential name is required.', 'a blank required Save field is named, the Issue key pattern');
 element('allow-account').dispatch('invalid');
 assert.equal(element('allow-save-status').textContent, 'Account is required.');
 element('allow-program').dispatch('invalid');
 assert.equal(element('allow-save-status').textContent, 'Program path is required.');

 // The saved list shows no path, SID or hash as text (static/panel_rules.js).
 const { assertPanelRules } = require('./static/panel_rules.js');
 assertPanelRules([element('records'), element('allow-rules')], 'Credentials', { rules: ['raw-value', 'class'], allowClasses: require('./panel_fake_dom_test_helper.js').allowlistedClasses('credentials') });
 // A click's sentence survives a reread in which the server state changed:
 // the same row element, its sentence and the updated caption.
 await require('./panel_fake_dom_test_helper.js').assertLiveUpdateKeepsRow({
 click: () => element('records').children[1].remove.onclick(),
 change: () => { activeRecord.State = 'revoked'; listPage.Records = [activeRecord, {...revokedRecord, Registered:'2025-01-01T00:00:00.000000Z'}, gatewayKeyRecord]; },
 data: () => listPage,
 tick: async () => { await context.credentialsTick(); await new Promise(r => setTimeout(r, 0)); },
 row: () => element('records').children[1], status: row => row.status, label: 'Credentials, saved credentials',
 });
 const box = element('records').children[1];
 assert(box.caption.textContent.includes(context.oaLocalTime('2025-01-01T00:00:00.000000Z')), 'the row updated in place');
 console.log('PASS: credentials page lists metadata and limits, adds/rotates/revokes at the listed revision, allows through /rights at the listed policy revision, never echoes a secret, and names a blank required field on each form, App access Save included');
})().catch(e => { console.error(e); process.exitCode = 1; });
