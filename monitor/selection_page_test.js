// Run with node monitor/selection_page_test.js; no browser or service is started.
// Covers the Models page's choice group (selection_page.go): its markup holds
// choosers only (no textarea, no typed program or policy name), the fallback
// order sits behind an expander, and the group reads as one sentence when
// the registered apps cannot be read or none are registered. The choosers'
// reads and writes are driven in inference_page_test.js.
const fs = require('fs'), vm = require('./panel_record_test_helper.js').vm, assert = require('assert');
const go = fs.readFileSync(__dirname + '/selection_page.go', 'utf8');
const inference = fs.readFileSync(__dirname + '/inference_page.go', 'utf8');
const markup = go.slice(go.indexOf('var selectionPolicySection'), go.indexOf('const selectionPolicyScript'));
assert(inference.includes('selectionPolicySection +') && /inferenceScript \+ .*libraryScript \+ selectionPolicyScript/.test(inference), 'the Models page carries the choice group and runs its script after its own and the model rows');
assert(!fs.readFileSync(__dirname + '/registry_page.go', 'utf8').includes('selection-section'), 'the Providers page does not carry it');
assert(!/<textarea|<input/.test(markup), 'the choice group has no text field');
assert(!/selection-program|selection-name|selection-candidates|This Panel/.test(go), 'no typed program path, policy name or candidate list');
assert(/<div id="selection-fallback" hidden>` \+ oaExpander\("Fallback order"/.test(markup), 'the fallback order sits behind an expander, shown once an app is listed');
assert(markup.includes('id="models-profile" aria-label="Defaults for" disabled></select>'), 'uses arrive from the router catalogue');
assert(go.includes("policyNames={chat:'oa-chat-default',image:'oa-image-default'}"), 'the policy names apps read stay the same');

const shell = fs.readFileSync(__dirname + '/panel_shell.go', 'utf8');
const plain = shell.slice(shell.indexOf('const plainScript = `') + 21).split('<script>')[1].split('</script>')[0];
function goScript(source, name) { const marker = 'const ' + name + ' = `<script>', start = source.indexOf(marker); return source.slice(start + marker.length, source.indexOf('</script>`', start)) }
function node(tag) {
 return {tagName: String(tag || '').toUpperCase(), value: '', textContent: '', className: '', hidden: false, disabled: false, dataset: {}, attrs: {}, children: [],
  append(...v) { this.children.push(...v) }, replaceChildren(...v) { this.children = v }, setAttribute(k, v) { this.attrs[k] = v }, removeAttribute(k) { delete this.attrs[k] }};
}
function run(registrations, selection, defaultSelection, settings={}) {
 const fields = new Map();
 const element = id => { if (!fields.has(id)) fields.set(id, node('div')); return fields.get(id) };
 element('models-profile').value = settings.search?'':'chat';
 const reads = [], defaultReads=[];
 const context = {URLSearchParams, console, document: {getElementById: element, createElement: node}, location: {search: settings.search||'?k=test'},
  fetch: async (path, opts) => {
   assert.strictEqual(opts.headers['X-Panel-Key'], 'test');
   if (path.startsWith('/apps/registrations?')) return registrations();
   if (path === '/registry/selection') { const body=JSON.parse(opts.body);if(body.edit==='effective')return {ok:true,json:async()=>({read:{Outcome:'unknown'}})};if(body.account_default){defaultReads.push(body);if(defaultSelection)return defaultSelection(body)}else{reads.push(body);if(selection)return selection(body)} return {ok: true, json: async () => ({read: {Outcome: 'unknown'}})} }
   if(path==='/inference/modalities')return {ok:true,json:async()=>({modalities:['chat','image','embed','transcription','speech','live','video','example/custom@1']})};
   if (path.startsWith('/inference/models')) {if(settings.offersFail)return{ok:false,text:async()=>settings.offersFail};return {ok: true, json: async () => ({Offers: settings.offers||[],Picks:settings.picks||{}})}};
   if (path === '/inference/hosts' && settings.hosts) return {ok: true, json: async () => ({Outcome: 'page', Hosts: settings.hosts})};
   return {ok: true, json: async () => ({available: false, Outcome: 'page', Entries: [], AtEnd: true, Hosts: []})};
  }};
 vm.runInNewContext(plain, context);
 vm.runInNewContext(goScript(inference, 'inferenceScript'), context);
 vm.runInNewContext(goScript(go, 'selectionPolicyScript'), context);
 return {element, reads, defaultReads, context, refresh:()=>context.oaModelsHooks[0](false)};
}
const settle = async () => { for (let i = 0; i < 10; i++) await new Promise(r => setTimeout(r, 0)) };
(async () => {
 let page = run(() => ({ok: false, text: async () => 'forbidden'}));
 await settle();
 assert.strictEqual(page.element('selection-status').textContent, 'OpenAbstractions did not permit this Panel to do this; nothing was changed.');
 assert.strictEqual(page.reads.length, 0, 'no policy is read without an app');
 page = run(() => ({ok: true, json: async () => ({outcome: 'page', entries: []})}));
 await settle();
 assert.strictEqual(page.element('selection-apps').children.length, 0, 'no per-app rows render nothing');
 assert.strictEqual(page.element('selection-fallback').hidden, true, 'no fallback order without an app');
 const declared=(name,profiles,program='/apps/editor')=>({program,name,choosers_status:'declared',choosers:{Entries:profiles.map(([Name,Modality])=>({Name,Modality}))},application:{Descriptor:{Title:'Editor'}}});
 const prepared={search:'?k=test&use=image&model=prepared',offers:[{Family:'prepared',Names:['prepared'],Servers:[]}],picks:{prepared:'would-load'}};
 let stored={Outcome:'unknown'};
 page=run(()=>({ok:true,json:async()=>({outcome:'page',entries:[declared('editor',[['pictures','image']])]})}),body=>{if(body.edit==='put'){stored={Outcome:'found',Revision:'saved',Policy:{Name:body.name,Candidates:body.candidates.map(c=>({Model:c.model,Placement:c.placement,Provider:c.provider,Credential:c.credential}))}};return {ok:true,json:async()=>({change:{Outcome:'applied'}})}}return {ok:true,json:async()=>({read:stored})}},null,prepared);await settle();
 let suggested=page.element('selection-apps').children[0];
 assert.strictEqual(page.element('models-profile').value,'image','preparation opens the matching generic use');
 assert.strictEqual(suggested.parts.select.value,'prepared','reviewed model is preselected');
 assert.strictEqual(suggested.parts.confirm.hidden,false);
 assert(!page.reads.some(r=>r.edit==='put'),'suggestion never writes before confirmation');
 await suggested.parts.confirm.onclick();await settle();
 assert.strictEqual(page.reads.find(r=>r.edit==='put').target_program,'/apps/editor');
 assert.strictEqual(page.reads.find(r=>r.edit==='put').name,'pictures','declared app policy owns the write');
 assert.strictEqual(suggested.parts.confirm.hidden,true,'saved policy supersedes suggestion');
 stored={Outcome:'forbidden'};await page.refresh();await settle();
 assert.strictEqual(suggested.parts.confirm.hidden,true,'refused reads cannot confirm a suggestion');
 stored={Outcome:'found',Revision:'other',Policy:{Candidates:[{Model:'existing',Placement:'local'},{Model:'fallback',Placement:'local'}]}};await page.refresh();await settle();
 assert.strictEqual(suggested.parts.select.value,'existing','existing policy and fallback remain authoritative');
 assert.strictEqual(suggested.parts.confirm.hidden,true);
 let entries=[declared('editor',[['first','chat'],['second','chat'],['images','image']])];
 page=run(()=>({ok:true,json:async()=>({outcome:'page',entries})}));await settle();
 assert.deepStrictEqual(page.reads.map(r=>r.name),['first','second'],'two names use separate policy reads');
 assert.deepStrictEqual(page.defaultReads.map(r=>r.name),['first','oa-chat-default','second'],'account defaults keep the exact named-policy scopes');
 assert(page.element('selection-apps').children[0].parts.label.textContent.includes('first'));
 for(const use of ['chat','image','embed','transcription','speech','live','video']){
  entries=[declared('editor',[[use+'-choice',use]])];page.element('models-profile').value=use;await page.refresh();await settle();
  assert.strictEqual(page.reads.at(-1).name,use+'-choice','all existing use filters use declared names');
 }
 entries=[declared('one',[['same','chat']]),declared('two',[['same','chat']])];page.element('models-profile').value='chat';let before=page.reads.length;await page.refresh();await settle();assert.strictEqual(page.reads.length,before+1,'identical program/name/profile deduplicates');
 entries=[declared('one',[['same','chat']]),declared('two',[['same','image']])];before=page.reads.length;await page.refresh();await settle();assert.strictEqual(page.reads.length,before,'conflict cannot read/edit policy');assert(page.element('selection-apps').children[0].parts.select.disabled);assert(page.element(page.element('selection-apps').children[0].parts.status.id).textContent.includes('conflicting'));
 entries=[declared('one',[['same','chat']])];await page.refresh();await settle();assert(!page.element('selection-apps').children[0].parts.select.disabled);assert.strictEqual(page.element(page.element('selection-apps').children[0].parts.status.id).textContent,'','resolved same-key conflict clears error');
 entries=[declared('empty',[])];before=page.reads.length;await page.refresh();await settle();assert.strictEqual(page.reads.length,before,'explicit empty never fabricates default');assert(page.element('selection-fallback').hidden);
 entries=[{program:'/apps/editor',name:'legacy'}];await page.refresh();await settle();assert.strictEqual(page.reads.at(-1).name,'oa-chat-default','absent legacy retains convention');
 entries=[declared('one',[['custom','chat']]),{program:'/apps/editor',name:'legacy'}];before=page.reads.length;await page.refresh();await settle();assert.deepStrictEqual(page.reads.slice(before).map(r=>r.name),['custom'],'declared replaces same-program legacy conventions');
 entries=[{program:'/apps/editor',name:'one',choosers_status:'unavailable'}];before=page.reads.length;await page.refresh();await settle();assert.strictEqual(page.reads.length,before);assert(page.element('selection-apps').children[0].parts.select.disabled,'failed metadata cannot downgrade to legacy');
 page=run(()=>({ok:true,json:async()=>({outcome:'stale',entries:[declared('one',[['old','chat']])]})}));await settle();assert.strictEqual(page.reads.length,0,'stale snapshot never reads old declaration');
 let release,hold=false;const gate=new Promise(r=>release=r);
 page=run(()=>({ok:true,json:async()=>({outcome:'page',entries:[declared('one',[['chat-choice','chat'],['image-choice','image']])]})}),async body=>{if(hold&&body.edit==='read')await gate;return {ok:true,json:async()=>({read:{Outcome:'unknown'}})}});await settle();
 const oldRow=page.element('selection-apps').children[0];hold=true;const pending=page.refresh();await settle();page.element('models-profile').value='image';page.element('models-profile').onchange();oldRow.parts.select.value='wrong-model';await oldRow.parts.select.onchange();assert(!page.reads.some(r=>r.edit!=='read'),'old rendered use cannot write while use change is queued');release();await pending;await settle();

 // Choosers show only saved state (panel audit 2026-11-01): an account
 // default that needs a key says so in its visible sentence and turns back
 // to the saved choice, though the person's focus is still on it; Add a
 // fallback stays usable and writes why nothing can be added yet.
 {
  const needsKey='This model needs a saved key before an app can use it. Add its key under Credentials.';
  const hosted={settings:{offers:[{Family:'deepseek-chat',Names:['deepseek-chat'],Servers:['deepseek']}],hosts:[{Entry:{Name:'deepseek',Hosted:true,Credential:''}}]}};
  page=run(()=>({ok:true,json:async()=>({outcome:'page',entries:[declared('editor',[['oa-chat-default','chat']])]})}),null,null,hosted.settings);await settle();
  const row=page.element('selection-defaults').children[0];
  assert.strictEqual(page.element('selection-apps').children[0].parts.label.textContent,'Chat model for Editor — chat');
  assert(!row.parts.caption.textContent.includes('oa-chat-default'),'policy names stay out of visible words');
  assert.strictEqual(row.parts.select.value,'','no account default is saved');
  row.parts.select.value='deepseek-chat';page.context.document.activeElement=row.parts.select;
  await row.parts.select.onchange();await settle();
  assert.strictEqual(page.element(row.parts.status.id).textContent,needsKey,'the reason is the visible sentence');
  assert.strictEqual(page.element(row.parts.status.id).attrs['data-state'],'error');
  assert.strictEqual(row.parts.select.value,'','the chooser turns back to the saved choice');
  assert(!page.defaultReads.some(r=>r.edit==='put'),'nothing was written');
  page.context.document.activeElement=null;
  const add=page.element('selection-fallback-add');
  assert.strictEqual(add.disabled,false,'Add a fallback is usable');
  await add.onclick();
  assert.strictEqual(page.element('selection-fallback-status').textContent,'Editor has no chat model of its own yet. Choose one above first.','Add writes why nothing can be added');
 }
 // Picture choices project the router's decision; a declaration alone
 // never fills the chooser. Chat choices retain their existing behavior.
 {
  const settings={search:'?k=test&use=image',offers:[{Family:'unstaged'},{Family:'ready'},{Family:'running'},{Family:'cloud'}],
   picks:{unstaged:'not-here',ready:'would-load',running:'resident',cloud:'hosted'}};
  page=run(()=>({ok:true,json:async()=>({outcome:'page',entries:[declared('draw',[['pictures','image']])]})}),null,null,settings);
  await settle();
  assert.deepStrictEqual(Array.from(await page.context.modelsOffers('image'),o=>o.Family),['ready','running']);
  const select=page.element('selection-defaults').children[0].parts.select;
  assert.deepStrictEqual(Array.from(select.children,o=>o.value).filter(Boolean),['ready','running']);
  settings.picks.unstaged='would-load';
  await page.context.modelsLive();await settle();
  assert(Array.from(await page.context.modelsOffers('image'),o=>o.Family).includes('unstaged'),'a storage transition reaches the chooser on refresh');
  settings.offersFail='router unavailable';
  await page.context.modelsLive();await settle();
  assert.strictEqual(page.element('selection-defaults').children.length,0,'unavailable picture listing clears old choices');
  assert(page.element('selection-status').textContent,'unavailable stays an explained failure');
 }
 console.log('selection_page_test: ok');
})().catch((error) => { console.error(error); process.exit(1); });
