// Run with node monitor/credentials_access_page_test.js; no browser or
// service is started. Covers the Credentials page (credentials_page.go with
// connect_page.go) as the panel audit of 2026-11-01 found it: a key saved
// for DeepSeek left its row "Not connected", App access opened on "Other…"
// with an empty field and listed apps by id, every saved key read "App
// access could not be read." until the permission list had been read, and
// the Panel was named by its file name ("Abstraction Panel").
const fs=require('fs'),vm=require('vm'),assert=require('assert');
const go=name=>fs.readFileSync(__dirname+'/'+name,'utf8');
const script=name=>go(name).split('<script>')[1].split('</script>')[0];
const shell=go('panel_shell.go'),marker='const plainScript = `',start=shell.indexOf(marker)+marker.length;
const plain=shell.slice(start,shell.indexOf('`',start)).split('<script>')[1].split('</script>')[0];
assert(go('credentials_page.go').includes('<div id="credentials-panel-title" data-title="` + oaPanelTitle + `" hidden></div>'),'the markup carries the Panel title program_titles.go defines');
function node(tag){return {tagName:String(tag||'div').toUpperCase(),value:'',textContent:'',title:'',hidden:false,disabled:false,children:[],listeners:{},attributes:{},dataset:{},
 get options(){return this.children},append(...v){this.children.push(...v)},replaceChildren(...v){this.children=[...v]},setAttribute(k,v){this.attributes[k]=v},getAttribute(k){return this.attributes[k]},removeAttribute(k){delete this.attributes[k]},
 addEventListener(k,v){this.listeners[k]=v},focus(){},reset(){},scrollIntoView(){},close(){}}}
const fields=new Map();
function el(id){if(!fields.has(id))fields.set(id,node(id.includes('select')||id==='add-for'?'select':'div'));return fields.get(id)}
el('credentials-panel-title').dataset.title='OpenAbstractions Panel';
el('add-kind').value='bearer';// the markup's first option
function walk(n){return [n,...(n.children||[]).flatMap(walk)]}
const text=n=>walk(n).map(x=>x.textContent||'').join('\n');
const panelExe='C:\\OA\\Abstraction Panel.exe',appExe='C:\\Apps\\pretend-lemonade.exe';
const deepseekKey={Name:'deepseek',Kind:'bearer',Header:'',State:'active',Revision:'1',Scope:{Targets:['api.deepseek.com'],Consumers:['abstraction.inference/chat@1']},Expires:'',LastApplied:'',RegisteredBy:{Account:'acct',Program:panelExe},Registered:'2026-11-01T00:00:00Z'};
let records=[deepseekKey],hosts=[],releaseRights;const rightsHeld=new Promise(r=>releaseRights=r);
const posts=[];
const ok=v=>({ok:true,json:async()=>v,text:async()=>JSON.stringify(v)});
const c={URLSearchParams,console,location:{search:'?k=test'},document:{getElementById:el,createElement:node},setInterval(){},fetch:async(path,opts={})=>{
 if(opts.body)posts.push({path,body:JSON.parse(opts.body)});
 if(path==='/connect'&&opts.body){hosts=[{Entry:{Name:'deepseek',Hosted:true,Kind:'openai-compatible',Base:'https://api.deepseek.com/v1',Credential:'deepseek'},Spend:{}}];return ok({outcome:'applied',name:'deepseek'})}
 if(path==='/connect')return ok(JSON.parse(go('../openabstractions-flat/abstraction-router/go/hostedcatalogue/presets.json')));
 if(path==='/inference/hosts')return ok({Outcome:'page',Hosts:hosts});
 if(path==='/inference/gateway')return ok({Outcome:'page',Listening:false});
 if(path==='/inference/keys')return ok({Outcome:'page',Keys:[]});
 if(path.startsWith('/credentials?'))return ok({Outcome:'page',Limits:{SecureStore:'windows-dpapi-file',SupportedKinds:['bearer','header'],MaxSecretBytes:4096,MaxCredentials:64},Records:records,Next:'',Complete:true});
 if(path.startsWith('/rights?')){await rightsHeld;return ok({Outcome:'page',Revision:'r1',Rules:[],Names:{},Next:'',Complete:true})}
 if(path==='/explore')return ok({self:{account:'acct',program:panelExe},selfAccountName:'Owner'});
 if(path.startsWith('/apps/registrations'))return ok({outcome:'page',entries:[{role:'application',program:appExe,name:'pretend-lemonade',title:'Pretend Lemonade'}]});
 throw Error('unexpected '+path);
}};c.window=c;
vm.runInNewContext(plain,c);vm.runInNewContext(script('connect_page.go'),c);vm.runInNewContext(script('credentials_page.go'),c);
(async()=>{
 await Promise.all([c.connectReady,c.listReady,c.addForReady]);
 const saved=el('records').children[0];
 assert(!text(saved).includes('App access could not be read.'),'no failure is claimed while the permissions are still read');
 assert.strictEqual(saved.label.textContent,'DeepSeek','the row is titled by its service');
 assert(saved.caption.textContent.startsWith('Connected '),'a service key reads Connected: '+saved.caption.textContent);
 assert(saved.pre.textContent.includes('Registered by: OpenAbstractions Panel'),'the Panel reads by its title in Technical details: '+saved.pre.textContent);
 const deepseekRow=()=>el('connect-rows').children.find(r=>r.item.preset&&r.item.preset.id==='deepseek');
 assert.strictEqual(deepseekRow().parts.caption.textContent,'A key is saved; connect to let apps use it.','the saved key shows on its service row');
 releaseRights();await Promise.all([c.allowPolicyReady,c.allowChoosersReady]);
 assert(saved.caption.textContent.endsWith(' · Not used by any app yet'),saved.caption.textContent);
 const pick=el('allow-program-select');
 assert.strictEqual(pick.value,appExe,'App access opens on the first registered app');
 assert.strictEqual(pick.children.find(o=>o.value===appExe).textContent,'Pretend Lemonade','by its title');
 assert.strictEqual(el('allow-program').value,appExe);
 assert.strictEqual(el('allow-name-select').value,'deepseek','App access opens on the saved key, not on Other…');
 assert.strictEqual(el('allow-name').hidden,true);
 const generated={...deepseekKey,Name:'deepseek-abcdefghijklm'};
 const generatedRow=c.credentialRow(generated);c.updateCredentialRow(generatedRow,generated);
 assert.strictEqual(generatedRow.label.textContent,'DeepSeek','generated key IDs use their service name');
 assert.strictEqual(c.metaBoxName({...generated,Scope:{...generated.Scope,Targets:['another.example']}}),generated.Name,'a matching name alone cannot name a credential for another target');

 // Saving a DeepSeek key with the preset unchanged connects DeepSeek.
 records=[];
 const add=el('add-for');add.value=add.children.find(o=>o.textContent==='DeepSeek (preset)').value;add.onchange();
 el('add-secret').value='sk-test-not-real';
 await el('add-form').onsubmit({preventDefault(){}});
 assert.deepStrictEqual(posts.filter(p=>p.path==='/credentials').length,0,'the key is not saved alone');
 const connect=posts.find(p=>p.path==='/connect');
 assert(connect&&connect.body.edit==='connect'&&connect.body.preset==='deepseek'&&!('credential_name' in connect.body),'saved through Connect under the preset name');
 assert.strictEqual(el('add-open-status').textContent,'Saved the key and configured DeepSeek. Each app still needs your permission to use it.');
 assert.strictEqual(el('add-status').textContent,'','the closed dialog keeps no Saving… line');
 assert(deepseekRow().parts.caption.textContent.startsWith('Configured; connection not confirmed'),'saving a key did not verify the service: '+deepseekRow().parts.caption.textContent);
 const before=posts.length;
 el('allow-program-select').value='__other__';el('allow-program-select').onchange();
 await el('allow-form').onsubmit({preventDefault(){}});
 assert.strictEqual(posts.length,before,'an empty custom app never reaches the rights service');
 assert.strictEqual(el('allow-save-status').textContent,'Program path is required.');
 console.log('credentials_access_page_test: ok');
})().catch(e=>{console.error(e);process.exit(1)});
