// Inert Models/Providers composition; no browser, native runtime or effects.
const fs=require('fs'),vm=require('vm'),assert=require('assert');
const {fakeDocument}=require('./panel_fake_dom_test_helper.js');
const source=fs.readFileSync(__dirname+'/program_review_page.go','utf8');
const script=source.split('<script>')[1].split('</script>')[0];
// plainScript (panel_shell.go) is ahead of every page: oaShowError and oaTechnical.
const shell=fs.readFileSync(__dirname+'/panel_shell.go','utf8'),plainMark='const plainScript = `',plainAt=shell.indexOf(plainMark)+plainMark.length;
const plain=shell.slice(plainAt,shell.indexOf('`',plainAt)).split('<script>')[1].split('</script>')[0];
const document=fakeDocument();
for(const id of ['program-review-section','program-review-status','program-review-rows'])document.element(id,'div');
const record={Key:'canonical-program',Revision:'observed-revision',IdentityEvidence:'program',Verification:'changed',State:'enabled',Spec:{Program:'C:\\fixture\\provider.exe',Providers:[{Name:'inventory'}],Servers:[{Name:'preserved-server'}]}};
let records=[record],posted=[],failRead=false;
const context={document,URLSearchParams,location:{search:'?k=fixture'},fetch:async(path,options)=>{
 assert.equal(path,'/registry/programs');assert.equal(options.headers['X-Panel-Key'],'fixture');
 if(options.method==='POST'){posted.push(JSON.parse(options.body));return{ok:true,json:async()=>({outcome:'pending',reason:'Review the native question.'})}}
 if(failRead)return{ok:false,text:async()=>'service resolution: runtime_unavailable: abstraction.facade/registry@3 for S-1-5-21-1-2-3-1001'};
 return{ok:true,json:async()=>({registrations:records})};
}};context.window=context;
vm.createContext(context);vm.runInContext(plain,context);vm.runInContext(script,context);
(async()=>{
 await context.programReviewRefresh();
 const rows=document.getElementById('program-review-rows');let row=rows.children[0];const button=row.querySelector('button');
 assert.equal(button.textContent,'Review current program');await button.onclick();
 assert.deepEqual(posted,[{action:'review',key:'canonical-program',revision:'observed-revision'}]);
 await context.programReviewRefresh();assert.strictEqual(rows.children[0],row,'unchanged polling preserves focus and disclosure');
 records=[{...record,State:'pending_approval',Verification:'unverified',Reason:'Review the open OpenAbstractions question q-1.'}];await context.programReviewRefresh();assert(rows.children[0].querySelector('button').disabled,'an open native review stays pending');
 records=[{...record,State:'pending_approval',Verification:'unverified',Reason:'approval:pending'}];await context.programReviewRefresh();assert(!rows.children[0].querySelector('button').disabled,'a registration nobody reviewed waits for the Review click');
 records=[{...record,State:'pending_approval',Verification:'unverified',Reason:'approval:unavailable'}];await context.programReviewRefresh();assert(!rows.children[0].querySelector('button').disabled,'absent native group offers explicit retry');assert(rows.children[0].textContent.includes('question is unavailable'),'missing question differs from live pending review');
 records=[{...record,Spec:{...record.Spec,Applications:[{Name:'existing-native-app'}]}}];await context.programReviewRefresh();assert.equal(rows.children[0].querySelector('button'),null,'mixed Spec stays with its existing owner');
 records=[{...record,State:'disabled'}];await context.programReviewRefresh();assert.equal(rows.children[0].querySelector('button'),null,'disabled intent is preserved');
 records=[];await context.programReviewRefresh();assert(document.getElementById('program-review-section').hidden);
 // A failed read says so in one plain line; the service's words, its ids
 // and the account's SID stay behind Technical details (architect ruling
 // 2026-11-09, second block).
 failRead=true;await context.programReviewRefresh();
 const status=document.getElementById('program-review-status');
 assert(!/S-1-5-21|abstraction\.facade|runtime_unavailable/.test(status.childNodes?Array.from(status.childNodes).filter(n=>n.nodeType===3).map(n=>n.data).join(''):String(status.textContent).split('Technical details')[0]),'no raw value in the status line: '+status.textContent);
 failRead=false;
 for(const file of ['registry_page.go','inference_page.go'])assert(fs.readFileSync(__dirname+'/'+file,'utf8').includes('+ programReviewPanel'),'Models and Providers share the actual review owner');
 console.log('program review page PASS');
})().catch(error=>{console.error(error);process.exitCode=1});
