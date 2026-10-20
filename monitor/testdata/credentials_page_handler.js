// Called by TestCredentialsPageEntryAndRowAccess. Only the owned Panel's
// loopback URL is used; its real native credential/rights services are in memory.
const fs=require('fs'),path=require('path'),vm=require('vm'),assert=require('assert');
const root=path.join(__dirname,'..'),base=process.argv[2];
if(!/^http:\/\/127\.0\.0\.1:\d+$/.test(base))throw Error('owned loopback Panel required');
const fields=new Map();
function node(tag){return {tag,value:'',textContent:'',title:'',hidden:false,children:[],listeners:{},attributes:{},
 append(...v){this.children.push(...v)},replaceChildren(...v){this.children=[...v]},setAttribute(k,v){this.attributes[k]=v},getAttribute(k){return this.attributes[k]},
 addEventListener(k,v){this.listeners[k]=v},focus(){},reset(){},scrollIntoView(){}}}
function el(id){if(!fields.has(id))fields.set(id,node(id));return fields.get(id)}
function walk(n){return [n,...(n.children||[]).flatMap(walk)]}
function find(n,p){return walk(n).find(p)}
const secret='owned-dummy-credential-not-for-readback-1392';
let rightsWrites=0,credentialWrites=0;
async function request(p,opts){
 const r=await fetch(base+p,opts),text=await r.clone().text();
 assert(!text.includes(secret),'secret escaped in an HTTP reply');
 if(opts?.body){if(p==='/rights')rightsWrites++;if(p==='/connect'||p==='/credentials')credentialWrites++}
 return r;
}
(async()=>{
 const page=await request('/credentials-page?k=test-key');assert(page.ok);
 const html=await page.text();assert(html.includes('Connect a service')&&html.includes('id="add-cancel"'));
 const shell=fs.readFileSync(path.join(root,'panel_shell.go'),'utf8');
 const marker='const plainScript = `',start=shell.indexOf(marker)+marker.length;
 const plain=shell.slice(start,shell.indexOf('`',start)).split('<script>')[1].split('</script>')[0];
 const script=file=>fs.readFileSync(path.join(root,file),'utf8').split('<script>')[1].split('</script>')[0];
 assert(html.includes(script('credentials_page.go'))&&html.includes(script('connect_page.go')),'received page and executed scripts agree');
 const c={URLSearchParams,console,location:{search:'?k=test-key'},document:{getElementById:el,createElement:node},fetch:request};c.window=c;
 vm.runInNewContext(plain,c);vm.runInNewContext(script('connect_page.go'),c);vm.runInNewContext(script('credentials_page.go'),c);
 await Promise.all([c.connectReady,c.listReady,c.allowPolicyReady,c.allowChoosersReady,c.addForReady]);
 assert.equal(el('records').children.length,0,'first entry starts empty');
 const row=el('connect-rows').children.find(r=>r.item.preset.id==='github-download');assert(row,el('connect-status').textContent);
 row.parts.control.children[0].onclick();let input=find(row.form,n=>n.type==='password');input.value=secret;
 find(row.form,n=>n.textContent==='Cancel').onclick();assert.equal(input.value,'');assert(row.form.hidden);assert.equal(credentialWrites,0);assert.equal(rightsWrites,0);
 row.parts.control.children[0].onclick();input.value=secret;
 await find(row.form,n=>n.textContent==='Save credential').onclick();
 assert.equal(credentialWrites,1);assert.equal(input.value,'');assert.equal(rightsWrites,0,'saving grants no app access');
 const saved=el('records').children[0];assert(saved);const savedName=saved.record.Name;assert(savedName.startsWith('github-download-'));
 assert.deepEqual(saved.record.Scope.Consumers,['abstraction.download/http-execution@1'],'the dedicated download flow retains its exact native consumer restriction');
 assert(saved.pre.textContent.includes('api.github.com'));assert(!saved.caption.title);
 saved.access.onclick();assert.equal(el('allow-name').value,savedName);assert(!el('allow-form').hidden);assert.equal(rightsWrites,0);
 const self=await (await request('/explore',{headers:{'X-Panel-Key':'test-key'}})).json();
 const app=path.join(path.dirname(self.self.program),'other-app'+path.extname(self.self.program));
 el('allow-account').value=self.self.account;el('allow-program').value=app;el('allow-permit').value='permit';
 await el('allow-form').onsubmit({preventDefault(){}});assert.equal(rightsWrites,1);
 assert(saved.caption.textContent.includes(' · Used by other-app'),'the actual rule names its app on the credential row: '+saved.caption.textContent);
 assert(el('allow-rules').children.some(n=>/^other-app.* can use your /.test(n.sentence?.textContent)),'and is its own App access row');
 el('allow-permit').value='deny';await el('allow-form').onsubmit({preventDefault(){}});assert.equal(rightsWrites,2);
 assert(el('allow-rules').children.some(n=>/^other-app.* can't use your /.test(n.sentence?.textContent)));
 assert(saved.caption.textContent.endsWith(' · Not used by any app yet'));
 // A stale conditional change is refused by the actual owner, with no new row.
 const denied=await request('/credentials',{method:'POST',headers:{'X-Panel-Key':'test-key','Content-Type':'application/json'},body:JSON.stringify({action:'rotate',name:savedName,expected_revision:'stale',secret})});
 assert.equal((await denied.json()).Outcome,'conflict');
 saved.replace.onclick();saved.secret.value=secret;saved.cancel.onclick();assert.equal(saved.secret.value,'');assert(saved.editor.hidden);
 el('add-open').onclick();el('add-secret').value=secret;el('add-cancel').onclick();assert.equal(el('add-secret').value,'');
 // A service without a preset uses the existing explicit name/address route.
 // Selecting downloads changes only the secret's consuming-service scope.
 el('add-open').onclick();el('add-for').value='__other__';el('add-for').onchange();
 el('add-name').value='huggingface';el('add-targets').value='huggingface.co';el('add-kind').value='bearer';el('add-expires').value='';el('add-consumers').value='';
 for(const b of el('add-consumers-select').children)if(!!b.pressed!==(b.value==='abstraction.download/http-execution@1'))b.onclick();
 el('add-secret').value=secret;await el('add-form').onsubmit({preventDefault(){}});
 assert.equal(el('add-secret').value,'');assert.equal(rightsWrites,2,'manual key entry grants no app access');
 assert(el('records').children.some(r=>r.record.Name==='huggingface'&&r.record.Scope.Targets[0]==='huggingface.co'));
 for(const n of [...fields.values()].flatMap(walk)){assert(!String(n.textContent).includes(secret));assert(!String(n.title).includes(secret));assert(!String(n.value||'').includes(secret))}
 console.log('PASS: credential entry, row access, conditional refusal and no secret readback');
})().catch(e=>{console.error(e);process.exitCode=1});
