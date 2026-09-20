// Run with node monitor/config_page_test.js; browser behavior, no runtime needed.
const fs=require('fs'),vm=require('vm'),assert=require('assert');
const source=fs.readFileSync(__dirname+'/service_page.go','utf8').split('<script>')[1].split('</script>')[0];
const fields=new Map();function el(id){if(!fields.has(id))fields.set(id,{value:'',textContent:'',replaceChildren(){},append(){}});return fields.get(id)}
let pending=[],writes=[],reply={Outcome:'conflict'},draft={Revision:'r1',Values:{Store:'draft'}};
const context={URLSearchParams,AbortController,location:{search:'?k=test'},document:{getElementById:el},localStorage:{length:0},fetch:async(path,options)=>{
 if(path.startsWith('/settings/observe'))return new Promise(resolve=>pending.push({resolve,options}));
 assert.equal(path,'/settings');writes.push(JSON.parse(options.body));return {ok:true,json:async()=>reply};
}};
vm.runInNewContext(source,context);
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function respond(index,value){pending[index].resolve({ok:true,json:async()=>value})}
(async()=>{
 el('settings').value=JSON.stringify(draft);
 await el('write-settings').onclick();assert.equal(el('settings').value,JSON.stringify(draft));assert.match(el('settings-status').textContent,/draft is preserved/);
 const first=el('follow-settings').onclick();assert.equal(pending.length,1);
 respond(0,{Outcome:'snapshot',Cursor:'c1',Snapshot:{Store:'effective'}});await tick();assert.equal(pending.length,2);assert.match(el('effective-settings').textContent,/effective/);assert.equal(el('settings').value,JSON.stringify(draft));
 await el('follow-settings').onclick();assert(pending[1].options.signal.aborted);
 const second=el('follow-settings').onclick();assert.equal(pending.length,3);
 respond(1,{Outcome:'snapshot',Cursor:'late',Snapshot:{Store:'late'}});await first;assert(!el('effective-settings').textContent.includes('late'));assert.equal(el('follow-settings').textContent,'Stop following');
 respond(2,{Outcome:'gap'});await second;assert.equal(el('follow-settings').textContent,'Follow effective settings');assert.match(el('settings-observation-status').textContent,/continuity was lost/);assert.equal(pending.length,3);assert.equal(el('settings').value,JSON.stringify(draft));
 reply={Outcome:'forbidden'};await el('write-settings').onclick();assert.equal(el('settings').value,JSON.stringify(draft));assert.equal(el('settings-status').textContent,'forbidden');
 console.log('PASS: config conflict/refusal preserves draft; observation keeps draft, aborts, ignores late responses and stops on gap');
})().catch(e=>{console.error(e);process.exitCode=1});
