// Run with node monitor/config_page_test.js; no browser or service is started.
// Settings (settings_page.go, research/panel-redesign/SPEC.md §4, S9):
// About on arrival, Locations and Downloads behind expanders, choosers
// instead of typed paths, and one sentence in a row's status per save.
const fs=require('fs'),vm=require('./panel_record_test_helper.js').vm,assert=require('assert');
const go=fs.readFileSync(__dirname+'/settings_page.go','utf8');
{
 const markup=go.slice(go.indexOf('var settingsPage = '),go.indexOf('<script>'));
 assert(markup.includes('oaPageHead("settings", "Settings", "Locations and information.")'),'the page caption');
 assert(markup.indexOf('identitySection')<markup.indexOf('oaExpander("Locations"'),'About comes first, shown on arrival');
 for(const name of ['Locations','Downloads','Job folders','Developer tools'])assert(markup.includes('oaExpander("'+name+'"'),name+' sits behind an expander');
 assert(markup.indexOf('<h2>Advanced</h2>')<markup.indexOf('oaExpander("Job folders"'),'the job stores sit under Advanced');
 assert(!markup.includes('loggingSection'),'the detailed log lives on Activity');
 assert(markup.includes('exploreBody'),'Explore sits in Developer');
}
function commonScript(){const src=fs.readFileSync(__dirname+'/service_page.go','utf8');const m='const serviceCommonScript = `';const s=src.indexOf(m)+m.length;return src.slice(s,src.indexOf('`',s))}
function plainScript(){const src=fs.readFileSync(__dirname+'/panel_shell.go','utf8');const m='const plainScript = `';const s=src.indexOf(m)+m.length;return src.slice(s,src.indexOf('`',s)).split('<script>')[1].split('</script>')[0]}
// The settings script alone: from serviceCommonScript's glue to the
// identity script it is followed by.
const script=go.split('<script>` + serviceCommonScript + `')[1].split('` + identityScript')[0];
function node(){return {value:'',textContent:'',className:'',title:'',hidden:false,checked:false,children:[],replaceChildren(...v){this.children=[...v]},append(...v){this.children.push(...v)},setAttribute(k,v){(this.attrs=this.attrs||{})[k]=v},removeAttribute(){}}}
const fields=new Map();function el(id){if(!fields.has(id))fields.set(id,node());return fields.get(id)}
const created=[];
let writes=[],settingsReply={Outcome:'conflict'};
let getReply={Revision:'r1',Values:{NasStore:'',Store:'/data/store',LogSink:'',LogService:'\\\\.\\pipe\\oa-log',Off:{nas:'no internet here'}}};
let snapshot={NasStore:'',Store:'/data/store',LogSink:'',LogService:'\\\\.\\pipe\\oa-log',Off:{nas:'no internet here'},Origins:{LogService:{Rung:'machine'}}};
const context={URLSearchParams,Date,location:{search:'?k=test'},document:{getElementById:id=>{const f=created.findLast(n=>n.id===id);return f||el(id)},createElement:t=>{const n=node();n.tagName=String(t).toUpperCase();created.push(n);return n}},fetch:async(path,options)=>{
 if(path.startsWith('/settings/observe'))return {ok:true,json:async()=>({Outcome:'snapshot',Cursor:'c',Snapshot:snapshot})};
 if(path.startsWith('/settings/archive'))return {ok:true,json:async()=>({folder:'',network:false})};
 assert.equal(path,'/settings');
 if(!options||!options.body)return {ok:true,json:async()=>getReply};
 writes.push(JSON.parse(options.body));return {ok:true,json:async()=>settingsReply};
}};
vm.runInNewContext(plainScript(),context);
vm.runInNewContext(commonScript()+script,context);
const tick=()=>new Promise(r=>setImmediate(r));
const rows=id=>el(id).children;
const label=row=>row.children[0].children[0].textContent,caption=row=>row.children[0].children[1];
const control=row=>row.children[1].children,status=row=>row.children[2];
(async()=>{
 await context.settingsReady;
 assert.deepEqual(rows('settings-rows').map(label),['Log file','Logging service']);
 assert.deepEqual(rows('settings-job-rows').map(label),['Job folder','Network job folder'],'the job stores sit under Advanced');
 const store=rows('settings-job-rows')[0];
 assert.equal(control(store).length,4,'the job store uses the existing location chooser');
 const nas=rows('settings-job-rows')[1];
 // Save and Cancel appear only on a row the person changed.
 for(const row of [...rows('settings-rows'),...rows('settings-job-rows')])assert.deepEqual([control(row)[2].hidden,control(row)[3].hidden],[true,true],label(row)+' shows Save or Cancel before any change');
 control(nas)[0].value='custom';control(nas)[0].onchange();
 assert.deepEqual([control(nas)[2].hidden,control(nas)[3].hidden],[false,false],'a changed row shows Save and Cancel');
 assert.equal(control(store)[2].hidden,true,'only on the changed row');
 const beforeInvalid=writes.length;
 await control(nas)[2].onclick();
 assert.equal(writes.length,beforeInvalid,'an empty custom address never reaches the editor');
 assert.match(status(nas).textContent,/Enter a location for Network job folder/);
 assert.equal(control(nas)[2].hidden,false,'a refused save keeps the edit open');
 control(nas)[1].value='share\nfolder';
 await control(nas)[2].onclick();
 assert.equal(writes.length,beforeInvalid,'control characters never reach the editor');
 assert.match(status(nas).textContent,/without control characters/);
 control(nas)[3].onclick();
 assert.deepEqual([control(nas)[2].hidden,control(nas)[3].hidden],[true,true],'Cancel hides Save and Cancel');
 assert.equal(caption(store).textContent,'');assert.equal(caption(store).title,'/data/store','the job store path sits on the title');
 const logFile=rows('settings-rows')[0],logService=rows('settings-rows')[1];
 assert.equal(caption(logFile).textContent,'');
 assert.equal(caption(logService).textContent,'For this device','a set value reads in words');
 assert.equal(caption(logService).title,'\\\\.\\pipe\\oa-log','the path sits on the title, never as text');
 assert.equal(control(logFile)[0].value,'default');assert.equal(control(logFile)[1].hidden,true,'no path field until Custom location is chosen');
 assert.equal(control(logService)[1].value,'\\\\.\\pipe\\oa-log','a custom field opens prefilled');
 // Choosing a custom location for the log file and saving writes one sentence.
 control(logFile)[0].value='custom';control(logFile)[0].onchange();
 assert.equal(control(logFile)[1].hidden,false);
 control(logFile)[1].value='/logs/oa.log';
 settingsReply={Outcome:'applied',Snapshot:{Revision:'r2',Values:{...getReply.Values,LogSink:'/logs/oa.log'}}};
 snapshot={...snapshot,LogSink:'/logs/oa.log',Origins:{LogSink:{Rung:'user'},LogService:{Rung:'machine'}}};
 await control(logFile)[2].onclick();
 assert.deepEqual(writes.at(-1),{Values:{...getReply.Values,LogSink:'/logs/oa.log'},Revision:'r1'},'Save resends every field, deprecated ones as read');
 const savedRow=rows('settings-rows')[0];
 assert.equal(status(savedRow).textContent,'Saved: Log file.','the row status names the save');
 assert.equal(control(savedRow)[2].hidden,true,'a save ends the edit and hides Save');
 assert.equal(caption(savedRow).textContent,'For you');
 // Saving the unchanged service says so.
 settingsReply={Outcome:'applied',Snapshot:{Revision:'r3',Values:{...getReply.Values,LogSink:'/logs/oa.log'}}};
 await control(rows('settings-rows')[1])[2].onclick();
 assert.equal(status(rows('settings-rows')[1]).textContent,'Saved; nothing changed.');
 // Downloads: one switch per delegate, on unless switched off.
 assert.deepEqual(rows('settings-downloads').map(label),['Windows Background Transfer (BITS)'],'the NAS has no row while no NAS address can be set');
 const bits=rows('settings-downloads')[0];
 assert.equal(control(bits)[0].checked,true);
 assert.equal(caption(bits).hidden,true,'the switch shows On; no caption repeats it');
 settingsReply={Outcome:'applied',Snapshot:{Revision:'r4',Values:{...getReply.Values,LogSink:'/logs/oa.log',Off:{nas:'no internet here',bits:''}}}};
 snapshot={...snapshot,Off:{nas:'no internet here',bits:''}};
 control(bits)[0].checked=false;await control(bits)[0].onchange();await tick();
 assert.deepEqual(writes.at(-1).Values.Off,{nas:'no internet here',bits:''},'switching one off keeps the other\'s reason');
 assert.equal(status(rows('settings-downloads')[0]).textContent,'Windows Background Transfer (BITS) is off.');
 // A conflict reloads and says so in the row.
 settingsReply={Outcome:'conflict'};
 await control(rows('settings-rows')[0])[2].onclick();
 assert.match(status(rows('settings-rows')[0]).textContent,/changed elsewhere/);
 // The rendered rows keep the Panel rules (static/panel_rules.js).
 const { assertPanelRules } = require('./static/panel_rules.js');
 assertPanelRules([el('settings-rows'), el('settings-job-rows'), el('settings-downloads')], 'Settings', { rules: ['raw-value', 'empty-input', 'button-status', 'class', 'raw-name', 'label-period', 'caption-repeat'], allowClasses: require('./panel_fake_dom_test_helper.js').allowlistedClasses('settings') });
 // A save's sentence survives the page's next reading of the same state.
 const before=rows('settings-rows')[0];
 status(before).textContent='Saved: Log file.';
 await context.settingsLoad();await tick();
 assert.strictEqual(rows('settings-rows')[0],before,'an unchanged reading keeps the same row');
 assert.strictEqual(status(before).textContent,'Saved: Log file.','the row\'s sentence survives a reread');

 // A custom path field is an Other… field, prefilled with the value in
 // effect; Cancel ends the edit so live updates resume.
 const svc=rows('settings-rows')[1];
 assert('data-oa-other' in control(svc)[1].attrs,'the path field is an Other… field');
 control(svc)[0].value='default';control(svc)[0].onchange();
 assert.equal(svc.editing,true);
 control(svc)[3].onclick();
 assert.equal(svc.editing,false,'Cancel ends the edit');
 assert.equal(status(svc).textContent,'Nothing changed.');
 assert.equal(control(svc)[0].value,'custom','Cancel restores the value in effect');
 // A reread with changed server state keeps the row and its sentence.
 await require('./panel_fake_dom_test_helper.js').assertLiveUpdateKeepsRow({
 change:()=>{getReply={Revision:'r9',Values:{...getReply.Values,LogService:'/var/run/oa-other'}};snapshot={...snapshot,LogService:'/var/run/oa-other',Origins:{LogService:{Rung:'user'}}}},
 data:()=>getReply,
 tick:async()=>{await context.settingsLoad();await tick()},
 row:()=>rows('settings-rows')[1],status:row=>status(row),label:'Settings, locations'});
 assert.equal(caption(svc).textContent,'For you','the row updated in place');
 // The network address uses the same revisioned full-document save.
 control(nas)[0].value='custom';control(nas)[0].onchange();
 control(nas)[1].value='shared/jobs';
 settingsReply={Outcome:'applied',Snapshot:{Revision:'r10',Values:{...getReply.Values,NasStore:'shared/jobs'}}};
 snapshot={...snapshot,NasStore:'shared/jobs'};
 await control(nas)[2].onclick();
 assert.equal(writes.at(-1).Values.NasStore,'shared/jobs');
 assert.equal(writes.at(-1).Values.Store,'/data/store','the other store is preserved');
 assert.equal(writes.at(-1).Revision,'r9');
 assert.equal(status(nas).textContent,'Saved: Network job folder.');
 // The real Settings follower owns one cancellable long wait, retaining only
 // service-issued cursors. No browser, owner service, or timer sleep is used.
 const events={},pending=[],requests=[],timers=new Map();let timerID=0;
 const followContext={...context,AbortController,window:{addEventListener:(name,fn)=>{events[name]=fn}},
  document:{...context.document,visibilityState:'visible',addEventListener:(name,fn)=>{events[name]=fn}},
  setTimeout:fn=>{timers.set(++timerID,fn);return timerID},clearTimeout:id=>timers.delete(id),
  fetch:async(path,options)=>{
   if(!options.signal)return context.fetch(path,options);
   assert(path.startsWith('/settings/observe')||path==='/settings');
   if(path==='/settings')return {ok:true,json:async()=>getReply};
   requests.push(new URL('http://panel'+path).searchParams.get('cursor'));
   return new Promise((resolve,reject)=>{
    const request={signal:options.signal,reply:page=>resolve({ok:true,json:async()=>page}),reject};pending.push(request);
    options.signal.addEventListener('abort',()=>reject(Object.assign(Error('aborted'),{name:'AbortError'})),{once:true});
   });
  }};
 vm.runInNewContext(plainScript(),followContext);
 vm.runInNewContext(commonScript()+script,followContext);
 await followContext.settingsReady;await tick();
 assert.deepEqual(requests,[''],'a follower starts with the service snapshot cursor');
 pending.at(-1).reply({Outcome:'snapshot',Cursor:'epoch/1',Snapshot:snapshot});await tick();
 assert.deepEqual(requests,['','epoch/1']);
 pending.at(-1).reply({Outcome:'unchanged',Cursor:'epoch/1'});await tick();
 assert.equal(requests.at(-1),'epoch/1','unchanged retains the last service cursor');
 pending.at(-1).reply({Outcome:'gap',Cursor:'epoch/1'});await tick();
 assert.equal(requests.at(-1),'','a gap requests a fresh authoritative snapshot');
 pending.at(-1).reply({Outcome:'snapshot',Cursor:'next-epoch/1',Snapshot:snapshot});await tick();
 vm.runInNewContext('settingsEditing=true',followContext);
 pending.at(-1).reply({Outcome:'snapshot',Cursor:'next-epoch/2',Snapshot:snapshot});await tick();
 assert.equal(timers.size,1,'an active edit pauses changed-state receiving');
 vm.runInNewContext('settingsEditing=false',followContext);
 [...timers.values()][0]();await tick();
 assert.equal(requests.at(-1),'next-epoch/1','an edit does not consume an unapplied snapshot cursor');
 pending.at(-1).reply({Outcome:'unchanged',Cursor:'next-epoch/1'});await tick();
 const waiting=pending.at(-1);events.pagehide();await tick();
 assert(waiting.signal.aborted,'page close cancels the active transport');
 const closedCount=requests.length;await tick();assert.equal(requests.length,closedCount,'close starts no more requests');
 events.pageshow();await tick();assert.equal(requests.at(-1),'next-epoch/1','reopening resumes the service cursor');
 followContext.document.visibilityState='hidden';events.visibilitychange();await tick();
 assert(pending.at(-1).signal.aborted,'hidden presenter cancels its wait');
 followContext.document.visibilityState='visible';events.visibilitychange();await tick();
 pending.at(-1).reject(Error('disconnected'));await tick();
 assert.equal(timers.size,1,'a failed connection waits before retrying');
 const failedCount=requests.length;await tick();assert.equal(requests.length,failedCount);
 const retry=[...timers.values()][0];retry();await tick();
 assert.equal(requests.at(-1),'next-epoch/1','reconnect preserves the service cursor');
 events.pagehide();await tick();assert.equal(timers.size,0,'close leaves no retry timer');
 console.log('PASS: Settings shows locations in words with the path on the title, saves through choosers with one sentence per row, switches download delegates, and reloads on a conflict');
})().catch(e=>{console.error(e);process.exitCode=1});
