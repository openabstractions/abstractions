// Run with node monitor/credentials_page_test.js; no browser or service is started.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/credentials_page.go', 'utf8');
const script = go.split('<script>')[1].split('</script>')[0];
const fields = new Map();
function node() {
 return {value:'', textContent:'', href:'', disabled:false, children:[], replaceChildren(){this.children=[]}, append(...v){this.children.push(...v)}, click(){}, reset(){}};
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
const listPage = {Outcome:'page', Limits:{SecureStore:'file-0600', SupportedKinds:['bearer','header'], MaxSecretBytes:4096, MaxCredentials:64}, Records:[activeRecord, revokedRecord], Next:'', Complete:true};

const credentialCalls = [];
const addReply = {Outcome:'stored', Revision:'rev-2', Current:activeRecord};
const rotateReply = {Outcome:'rotated', Revision:'rev-3', Current:activeRecord};
const revokeReply = {Outcome:'revoked', Revision:'rev-1'};

const rightsCalls = [];
const rightsPage = {Outcome:'page', Revision:'policy-rev-1', Catalog:['abstraction.credentials/apply'], Rules:[], Next:'', Complete:true};
const rightsEditReply = {Outcome:'applied', Revision:'policy-rev-2', Current:{Subject:{Account:'alice',Program:'/usr/bin/app'},Action:'abstraction.credentials/apply',Resource:'credential:hf',Permit:true}};

const context = {URLSearchParams, document:{getElementById:element, createElement:node}, location:{search:'?k=test'}, console,
 fetch: async (path, opts) => {
  if (path.startsWith('/credentials')) {
   if (!opts || !opts.body) return {ok:true, json:async()=>listPage};
   const body = JSON.parse(opts.body);
   credentialCalls.push(body);
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
vm.runInNewContext(script, context);

(async () => {
 assert.equal(element('back').href, '/?k=test');

 await element('list-start').onclick();
 assert.equal(element('records').children.length, 2, 'both records rendered, tombstone included');
 const activeBox = element('records').children[0];
 assert(activeBox.children[0].textContent.includes('hf'));
 assert(activeBox.children[0].textContent.includes('bearer'));
 assert(activeBox.children[0].textContent.includes('active'));
 assert(activeBox.children[0].textContent.includes('rev-1'));
 assert(activeBox.children[0].textContent.includes('huggingface.co'));
 assert(activeBox.children[0].textContent.includes('abstraction.download/http-execution@1'));
 assert(activeBox.children[0].textContent.includes('/usr/bin/app'));
 assert.equal(activeBox.children.length, 4, 'an active record offers rotate, revoke and allow buttons');
 const revokedBox = element('records').children[1];
 assert.equal(revokedBox.children.length, 1, 'a revoked record offers no conditional-edit buttons');
 assert(element('limits').textContent.includes('file-0600'));
 assert(element('limits').textContent.includes('4096'));
 assert(element('limits').textContent.includes('64'));

 element('add-name').value = 'newcred'; element('add-kind').value = 'bearer';
 element('add-targets').value = 'a.com'; element('add-consumers').value = 'abstraction.download/http-execution@1';
 element('add-secret').value = secretOne;
 await element('add-form').onsubmit({preventDefault(){}});
 assert.equal(element('add-secret').value, '', 'the secret field is cleared once sent');
 assert.deepEqual(credentialCalls[0], {action:'add', name:'newcred', kind:'bearer', header:'', targets:['a.com'], consumers:['abstraction.download/http-execution@1'], expires:'', secret:secretOne, expected_revision:''});
 assert(element('add-status').textContent.includes('Stored newcred at revision rev-2'));
 assert(!element('add-status').textContent.includes(secretOne), 'the reply never echoes the secret');

 const rotateButton = activeBox.children[1];
 assert.equal(rotateButton.textContent, 'Use for rotate');
 rotateButton.onclick();
 assert.equal(element('rotate-name').value, 'hf');
 assert.equal(element('rotate-revision').value, 'rev-1');
 element('rotate-secret').value = secretTwo;
 await element('rotate-form').onsubmit({preventDefault(){}});
 assert.equal(element('rotate-secret').value, '', 'the secret field is cleared once sent');
 assert.deepEqual(credentialCalls[1], {action:'rotate', name:'hf', expected_revision:'rev-1', expires:'', secret:secretTwo});
 assert(element('rotate-status').textContent.includes('Rotated hf to revision rev-3'));
 assert(!element('rotate-status').textContent.includes(secretTwo));

 const revokeButton = activeBox.children[2];
 assert.equal(revokeButton.textContent, 'Use for revoke');
 revokeButton.onclick();
 assert.equal(element('revoke-name').value, 'hf');
 assert.equal(element('revoke-revision').value, 'rev-1');
 await element('revoke-form').onsubmit({preventDefault(){}});
 assert.deepEqual(credentialCalls[2], {action:'revoke', name:'hf', expected_revision:'rev-1'});
 assert(element('revoke-status').textContent.includes('Revoked hf at revision rev-1'));

 await element('allow-form').onsubmit({preventDefault(){}});
 assert.equal(rightsCalls.length, 0, 'allow without a listed rights revision reached the service');
 assert(element('allow-status').textContent.includes('List rights policy'));

 const allowButton = activeBox.children[3];
 assert.equal(allowButton.textContent, 'Allow/deny this name');
 allowButton.onclick();
 assert.equal(element('allow-name').value, 'hf');
 await element('allow-list').onclick();
 assert(element('allow-status').textContent.includes('policy-rev-1'));
 element('allow-account').value = 'alice'; element('allow-program').value = '/usr/bin/app'; element('allow-permit').value = 'permit';
 await element('allow-form').onsubmit({preventDefault(){}});
 assert.deepEqual(rightsCalls[0], {edit:'set', revision:'policy-rev-1', account:'alice', program:'/usr/bin/app', action:'abstraction.credentials/apply', resource:'credential:hf', permit:true});
 assert(element('allow-status').textContent.includes('Allowed /usr/bin/app to apply hf at revision policy-rev-1'));

 for (const id of ['list-status','limits','add-status','rotate-status','revoke-status','allow-status']) {
  assert(!element(id).textContent.includes(secretOne), id + ' carried the secret');
  assert(!element(id).textContent.includes(secretTwo), id + ' carried the secret');
 }
 console.log('PASS: credentials page lists metadata and limits, adds/rotates/revokes at the listed revision, allows through /rights at the listed policy revision, and never echoes a secret');
})().catch(e => { console.error(e); process.exitCode = 1; });
