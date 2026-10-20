// Drives the page's actual click owner with inert DOM/transport. No native UI.
const fs=require('fs'),vm=require('vm'),assert=require('node:assert/strict'),{test}=require('node:test');
const source=fs.readFileSync(__dirname+'/permission_review.go','utf8');
const marker='const permissionReviewScript = `',start=source.indexOf(marker)+marker.length;
assert(start>=marker.length,'permissionReviewScript exists');
const script=source.slice(start,source.indexOf('`',start));
function page(reply){
 const nodes=new Map(),calls=[];
 const node=()=>({textContent:'',disabled:false,children:[],replaceChildren(){this.children=[]},append(v){this.children.push(v)}});
 const el=id=>{if(!nodes.has(id))nodes.set(id,node());return nodes.get(id)};
 const context={el,document:{createElement:node},Date,reviewChoice:{context:{feature:'notifications',program:'actual-program'},shown:'shown-digest'},reviewFeatures:[{action:'notify',resource:'account',sentence:'Show notifications in your account.'}],call:async(path,body)=>{calls.push({path,body});return reply()}};
 vm.createContext(context);vm.runInContext(script,context);
 return {el,calls};
}
test('Allow posts the exact shown proposal once and lists saved plain features',async()=>{
 const p=page(async()=>({outcome:'applied',message:'Permissions saved for one hour.',landed:[{Rule:{Action:'notify',Resource:'account'},Expires:'2026-10-17T12:00:00Z'}]}));
 await p.el('review-allow').onclick();await p.el('review-allow').onclick();
 assert.equal(p.calls.length,1);assert.equal(p.calls[0].path,'/permission-review/choice');assert.equal(p.calls[0].body.allow,true);assert.equal(p.calls[0].body.shown,'shown-digest');
 assert.equal(p.el('review-status').textContent,'Permissions saved for one hour.');assert.match(p.el('review-saved').children[0].textContent,/Show notifications in your account\. Ends/);
 assert(p.el('review-allow').disabled&&p.el('review-refuse').disabled);
});
test('Refuse sends a false choice without a grant request',async()=>{
 const p=page(async()=>({outcome:'refused',message:'No permissions were changed.',landed:[]}));await p.el('review-refuse').onclick();
 assert.equal(p.calls.length,1);assert.equal(p.calls[0].body.allow,false);assert.equal(p.el('review-saved').children.length,0);
});
test('partial outcome retains its message and saved list without retry',async()=>{
 const p=page(async()=>({outcome:'partial',message:'Some permissions could not be verified.',landed:[{Rule:{Action:'notify',Resource:'account'},Expires:'2026-10-17T12:00:00Z'}]}));await p.el('review-allow').onclick();await p.el('review-refuse').onclick();
 assert.equal(p.calls.length,1);assert.equal(p.el('review-status').textContent,'Some permissions could not be verified.');assert.equal(p.el('review-saved').children.length,1);
});
test('lost response stays blocked and requires another review',async()=>{
 const p=page(async()=>{throw Error('Decision response unavailable.');});await p.el('review-allow').onclick();await p.el('review-allow').onclick();
 assert.equal(p.calls.length,1);assert.match(p.el('review-status').textContent,/Review again before continuing/);assert(p.el('review-allow').disabled);
});
