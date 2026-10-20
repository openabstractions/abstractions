// Run with node monitor/credentials_service_row_page_test.js; no browser or
// service is started. Covers the saved DeepSeek row as the owner found it on
// R10 (2026-11-08): titled "oa-panel-deepseek", a caption of internal words,
// an empty Technical details box, a second Technical details expander and a
// line "Allowed OpenAbstractions to use oa-panel-deepseek for this account."
// Architect ruling C sets its words: the service's name, "Connected … · Used
// by OpenCode", one collapsed Technical details, and app rows that read
// "OpenCode can use your DeepSeek account" with a Remove.
const fs=require('fs'),vm=require('vm'),assert=require('assert');
const go=name=>fs.readFileSync(__dirname+'/'+name,'utf8');
const script=name=>go(name).split('<script>')[1].split('</script>')[0];
const shell=go('panel_shell.go'),marker='const plainScript = `',start=shell.indexOf(marker)+marker.length;
const plain=shell.slice(start,shell.indexOf('`',start)).split('<script>')[1].split('</script>')[0];
function node(tag){return {tagName:String(tag||'div').toUpperCase(),value:'',textContent:'',title:'',hidden:false,disabled:false,children:[],listeners:{},attributes:{},dataset:{},
 get options(){return this.children},append(...v){this.children.push(...v)},replaceChildren(...v){this.children=[...v]},setAttribute(k,v){this.attributes[k]=v},getAttribute(k){return this.attributes[k]},removeAttribute(k){delete this.attributes[k]},
 addEventListener(k,v){this.listeners[k]=v},focus(){},reset(){},scrollIntoView(){},close(){}}}
const fields=new Map();
function el(id){if(!fields.has(id))fields.set(id,node(id.includes('select')||id==='add-for'?'select':'div'));return fields.get(id)}
el('credentials-panel-title').dataset.title='OpenAbstractions Panel';
el('add-kind').value='bearer';
function walk(n){return [n,...(n.children||[]).flatMap(walk)]}
const panelExe='C:\\OA\\Abstraction Panel.exe',runtimeExe='C:\\OA\\openabstractions.exe',opencodeExe='C:\\Apps\\OpenCode.exe';
const key={Name:'oa-panel-deepseek',Kind:'bearer',Header:'',State:'active',Revision:'1',Scope:{Targets:['api.deepseek.com'],Consumers:['abstraction.inference/chat@1','abstraction.router/router@1']},Expires:'',LastApplied:'',RegisteredBy:{Account:'acct',Program:panelExe},Registered:'2026-11-08T14:14:00Z'};
const apply=(program,permit=true)=>({Subject:{Account:'acct',Program:program},Action:'abstraction.credentials/apply',Resource:'credential:oa-panel-deepseek',Permit:permit});
let rules=[apply(runtimeExe),apply(opencodeExe)];
const posts=[];
const ok=v=>({ok:true,json:async()=>v,text:async()=>JSON.stringify(v)});
const c={URLSearchParams,console,location:{search:'?k=test'},document:{getElementById:el,createElement:node},setInterval(){},fetch:async(path,opts={})=>{
 if(opts.body){const body=JSON.parse(opts.body);posts.push({path,body});if(path==='/rights'&&body.edit==='revoke'){rules=rules.filter(r=>r.Subject.Program!==body.program);return ok({Outcome:'applied',Revision:'r2'})}throw Error('unexpected post '+path)}
 if(path==='/connect')return ok(JSON.parse(go('../openabstractions-flat/abstraction-router/go/hostedcatalogue/presets.json')));
 if(path==='/inference/hosts')return ok({Outcome:'page',Hosts:[{Entry:{Name:'deepseek',Hosted:true,Kind:'openai-compatible',Base:'https://api.deepseek.com/v1',Credential:'oa-panel-deepseek'},Spend:{}}]});
 if(path==='/inference/gateway')return ok({Outcome:'page',Listening:false});
 if(path==='/inference/keys')return ok({Outcome:'page',Keys:[]});
 if(path.startsWith('/credentials?'))return ok({Outcome:'page',Limits:{SecureStore:'windows-dpapi-file',SupportedKinds:['bearer','header'],MaxSecretBytes:4096,MaxCredentials:64},Records:[key],Next:'',Complete:true});
 if(path.startsWith('/rights?'))return ok({Outcome:'page',Revision:'r1',Rules:rules,Names:{[runtimeExe]:'OpenAbstractions',[panelExe]:'OpenAbstractions Panel',[opencodeExe]:'OpenCode'},Next:'',Complete:true});
 if(path==='/explore')return ok({self:{account:'acct',program:panelExe},selfAccountName:'Owner'});
 if(path.startsWith('/apps/registrations'))return ok({outcome:'page',entries:[{role:'application',program:opencodeExe,name:'opencode',title:'OpenCode'}]});
 throw Error('unexpected '+path);
}};c.window=c;
vm.runInNewContext(plain,c);vm.runInNewContext(script('connect_page.go'),c);vm.runInNewContext(script('credentials_page.go'),c);
(async()=>{
 await Promise.all([c.connectReady,c.listReady,c.addForReady,c.allowPolicyReady]);
 const row=el('records').children[0];
 assert.strictEqual(row.label.textContent,'DeepSeek','the row is titled by its service');
 assert.strictEqual(row.caption.textContent,'Connected '+c.oaWhen(key.Registered)+' · Used by OpenCode','one caption, the apps that may use the key');
 assert.deepStrictEqual(row.children[1].children.filter(b=>!b.hidden).map(b=>b.textContent),['App access…','Replace key…','Remove']);
 const technical=walk(row).filter(n=>n.tagName==='DETAILS');
 assert.strictEqual(technical.length,1,'exactly one Technical details');
 assert.strictEqual(technical[0].open,undefined,'it starts collapsed');
 for(const raw of ['Name: oa-panel-deepseek','Registered by: OpenAbstractions Panel','Services: chat'])assert(row.pre.textContent.includes(raw),raw+' in Technical details');
 const visible=walk(row).filter(n=>n.tagName!=='PRE'&&!walk(technical[0]).includes(n)).map(n=>n.textContent).join('\n');
 assert(!visible.includes('oa-panel'),'no internal name in visible text: '+visible);
 assert(!visible.includes('OpenAbstractions to use'),'no line about OpenAbstractions allowing itself');
 const appRows=el('allow-rules').children;
 assert.deepStrictEqual(appRows.map(r=>r.sentence.textContent),['OpenCode can use your DeepSeek account'],'apps only, by the service name');
 await appRows[0].remove.onclick();
 const revoke=posts.find(p=>p.body.edit==='revoke');
 assert(revoke&&revoke.body.program===opencodeExe&&revoke.body.resource==='credential:oa-panel-deepseek'&&revoke.body.revision==='r1');
 assert.strictEqual(el('allow-status').textContent,'OpenCode can no longer use your DeepSeek account.');
 assert.strictEqual(row.caption.textContent,'Connected '+c.oaWhen(key.Registered)+' · Not used by any app yet');
 console.log('credentials_service_row_page_test: ok');
})().catch(e=>{console.error(e);process.exit(1)});
