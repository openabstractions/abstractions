// Run with node monitor/inference_page_test.js; no browser or service is started.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/inference_page.go', 'utf8');
const script = go.split('<script>')[1].split('</script>')[0];
const fields = new Map();
function node() {
 return {value:'', textContent:'', className:'', disabled:false, children:[], replaceChildren(...v){this.children=[...v]}, append(...v){this.children.push(...v)}, click(){this.onclick&&this.onclick()}};
}
function element(id) {
 if (!fields.has(id)) fields.set(id, node());
 return fields.get(id);
}
const calls = [];
const hostList = {Outcome:'page', Revision:'hosts-v1:a', Hosts:[{Entry:{Name:'openrouter',Hosted:true,Kind:'openai-compatible',Base:'https://openrouter.ai/api/v1',Credential:'openrouter',Ceiling:{TokensPerDay:1000,MicrosPerDay:0},Profiles:['chat','embed'],DeclaredBy:'operator'},Up:false,Why:'credential:unknown:openrouter',Spend:{Day:'2026-09-17',Tokens:42,Micros:1500}}]};
const replies = {
 '/inference/hosts': [hostList, {Outcome:'applied', Revision:'hosts-v1:b', Reason:''}, hostList, {Outcome:'conflict', Revision:'hosts-v1:c', Reason:'revision'}],
 '/inference/keys': [{Outcome:'applied', Key:'oalk_shown_once', Record:{Program:'/usr/bin/python3'}}, {Outcome:'page', Keys:[{Program:'/usr/bin/python3',State:'active',Credential:'',IssuedUnixMs:0,IssuedBy:'/opt/oa/openabstractions'}]}, {Outcome:'forbidden'}],
 '/inference/gateway': [{Outcome:'page', Revision:'gateway-v1:a', Open:false, Address:'', Listening:false, ListeningAddress:'', Why:''}, {Outcome:'applied', Revision:'gateway-v1:b', Reason:''}, {Outcome:'page', Revision:'gateway-v1:b', Open:true, Address:'127.0.0.1:8793', Listening:true, ListeningAddress:'127.0.0.1:8793', Why:''}, {Outcome:'forbidden', Revision:'', Reason:''}],
 '/inference/audit': [{Outcome:'gap', Entries:[], Next:5, AtEnd:false}, {Outcome:'page', Entries:[{Sequence:5,UnixMs:0,Route:'window',Rung:'tcp-loopback/linux user=kernel process=bound path=bound',Program:'/usr/bin/python3',Host:'ollama',Model:'m',Credential:'',Outcome:'completed',Reason:'',TokensIn:6,TokensOut:4}], Next:6, AtEnd:true}],
};
const context = {URLSearchParams, document:{getElementById:element,createElement:node}, location:{search:'?k=test'}, console, Date,
 fetch:async(path,opts)=>{
  assert.strictEqual(opts.headers['X-Panel-Key'], 'test');
  const base = path.split('?')[0];
  calls.push({path, body: opts.body ? JSON.parse(opts.body) : null});
  return {ok:true, json:async()=>replies[base].shift()};
 }};
vm.runInNewContext(script, context);
const text = id => element(id).textContent;
const cells = row => row.children.map(td => td.textContent);
(async () => {
 const settle = () => new Promise(r => setTimeout(r, 0));
 element('host-add').onsubmit({preventDefault(){}});
 await settle();
 assert.match(text('hosts-status'), /List hosts before/);
 element('hosts-list').onclick(); await settle();
 assert.deepStrictEqual(cells(element('hosts').children[0]).slice(0, 10), ['openrouter','hosted','openai-compatible','https://openrouter.ai/api/v1','openrouter','chat, embed','operator','down: credential:unknown:openrouter','42 tokens, 1500 micros (2026-09-17)','1000 tokens, 0 micros']);
 Object.assign(element('host-name'), {value:'ollama'}); Object.assign(element('host-base'), {value:'http://127.0.0.1:11434'});
 element('host-add').onsubmit({preventDefault(){}}); await settle();
 assert.deepStrictEqual(calls.at(-1).body, {edit:'add', host:{Name:'ollama',Hosted:false,Kind:'ollama',Base:'http://127.0.0.1:11434',Credential:''}, revision:'hosts-v1:a'});
 assert.match(text('hosts-status'), /Applied; configuration revision hosts-v1:b/);
 element('hosts-list').onclick(); await settle();
 element('hosts').children[0].children[10].children[0].onclick(); await settle();
 assert.deepStrictEqual(calls.at(-1).body, {edit:'remove', name:'openrouter', revision:'hosts-v1:a'});
 assert.match(text('hosts-status'), /configuration changed/);
 element('gateway-off').onclick(); await settle();
 assert.match(text('gateway-status'), /Read the setting before/);
 element('gateway-read').onclick(); await settle();
 assert.strictEqual(text('gateway-status'), 'Setting: off\nWindow: closed\nSetting revision gateway-v1:a.');
 element('gateway-set').onsubmit({preventDefault(){}}); await settle(); await settle();
 assert.deepStrictEqual(calls.filter(c => c.path === '/inference/gateway').map(c => c.body), [null, {revision:'gateway-v1:a', open:true, address:''}, null]);
 assert.match(text('gateway-status'), /^Setting: on at 127\.0\.0\.1:8793\nWindow: listening on http:\/\/127\.0\.0\.1:8793\/v1/);
 element('gateway-off').onclick(); await settle();
 assert.deepStrictEqual(calls.at(-1).body, {revision:'gateway-v1:b', open:false, address:''});
 assert.match(text('gateway-status'), /not permit/);
 Object.assign(element('key-program'), {value:'/usr/bin/python3'});
 element('key-issue').onsubmit({preventDefault(){}}); await settle();
 assert.deepStrictEqual(calls.at(-1).body, {edit:'issue', program:'/usr/bin/python3', credential:''});
 assert.strictEqual(element('key-shown').children[1].textContent, 'oalk_shown_once');
 element('keys-list').onclick(); await settle();
 assert.strictEqual(cells(element('keys').children[0])[1], 'active');
 element('keys').children[0].children[5].children[0].onclick(); await settle();
 assert.deepStrictEqual(calls.at(-1).body, {edit:'revoke', program:'/usr/bin/python3'});
 assert.strictEqual(element('key-shown').children.length, 0, 'a later action no longer shows the issued key');
 assert.match(text('keys-status'), /not permit/);
 element('audit-start').onclick(); await settle(); await settle();
 assert.deepStrictEqual(calls.filter(c => c.path.startsWith('/inference/audit')).map(c => c.path), ['/inference/audit?cursor=0', '/inference/audit?cursor=5']);
 assert.deepStrictEqual(cells(element('audit').children[0]).slice(2, 5), ['window','tcp-loopback/linux user=kernel process=bound path=bound','/usr/bin/python3']);
 assert.strictEqual(element('audit-next').disabled, true);
 console.log('PASS inference page');
})().catch(e => { console.error(e); process.exit(1); });
