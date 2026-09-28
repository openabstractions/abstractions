// Run with node monitor/config_page_test.js; browser behavior, no runtime needed.
// Covers settings_page.go, split out of service_page.go's own Settings
// section (task 2026-09-23, requirement 1), redesigned for arrival state
// (task 2026-09-23, eleventh first-time visitor, requirement 1): the page
// reads its own state on load instead of opening with five empty fields
// and a Read button.
const fs=require('fs'),vm=require('vm'),assert=require('assert');
function commonScript(){const src=fs.readFileSync(__dirname+'/service_page.go','utf8');const marker='const serviceCommonScript = `';const start=src.indexOf(marker)+marker.length;assert(start>marker.length-1,'serviceCommonScript not found in service_page.go');return src.slice(start,src.indexOf('`',start))}
function loadPageScript(file){const go=fs.readFileSync(__dirname+'/'+file,'utf8');const script=go.split('<script>')[1].split('</script>')[0];const glue='` + serviceCommonScript + `';assert(script.includes(glue),file+': serviceCommonScript glue not found');return script.replace(glue,commonScript())}
// plainScript (panel_shell.go) is embedded ahead of every page fragment in
// the real document; settings_page.go's own script now calls oaLocalTime
// (task 2026-09-23, tenth first-time visitor, finding 2), so this test
// loads it into the same vm context first, the same way its siblings do.
function plainScript(){const shellGo=fs.readFileSync(__dirname+'/panel_shell.go','utf8');const marker='const plainScript = `';const start=shellGo.indexOf(marker)+marker.length;assert(start>marker.length-1,'plainScript constant not found in panel_shell.go');const end=shellGo.indexOf('`',start);return shellGo.slice(start,end).split('<script>')[1].split('</script>')[0]}
// node/el mirror the other *_page_test.js files' fake DOM: textContent and
// children are independent properties, append/replaceChildren track real
// children so a row's own children (and renderSettingsSnapshot's table) can
// be inspected the same way a real DOM tree would be.
function node(){return {value:'',textContent:'',className:'',title:'',checked:false,dataset:{},children:[],replaceChildren(...v){this.children=[...v]},append(...v){this.children.push(...v)},click(){this.onclick&&this.onclick()},setAttribute(){}}}
const fields=new Map();function el(id){if(!fields.has(id))fields.set(id,node());return fields.get(id)}
let pending=[],writes=[],settingsReply={Outcome:'conflict'};
// getReply is /settings's own GET reply (the raw saved Values a Save must
// resend unchanged for every field but the one edited); observeQueue holds
// the /settings/observe replies settingsLoad's own one-shot read and each
// "Watch settings" click consume in order, first in first out, before a
// call falls back to the pending[] queue a test can resolve by hand.
let getReply={Revision:'r1',Values:{NasStore:'',Store:'/data/store',LogSink:'',LogService:'\\\\.\\pipe\\oa-log',Off:{nas:'no internet here'}}};
let observeQueue=[{Outcome:'snapshot',Cursor:'c0',Snapshot:{NasStore:'',Store:'/data/store',LogSink:'',LogService:'\\\\.\\pipe\\oa-log',Off:{nas:'no internet here'},
 Origins:{Store:{Rung:'user',Path:'C:\\cfg\\user.json'},LogService:{Rung:'machine'}}}}];
const context={URLSearchParams,AbortController,Date,location:{search:'?k=test'},document:{getElementById:el,createElement:node,createTextNode:text=>({textContent:text})},localStorage:{length:0},fetch:async(path,options)=>{
 if(path.startsWith('/settings/observe')){if(observeQueue.length)return {ok:true,json:async()=>observeQueue.shift()};return new Promise(resolve=>pending.push({resolve,options}))}
 assert.equal(path,'/settings');
 if(!options||!options.body)return {ok:true,json:async()=>getReply};
 writes.push(JSON.parse(options.body));return {ok:true,json:async()=>settingsReply};
}};
vm.runInNewContext(plainScript(),context);
vm.runInNewContext(loadPageScript('settings_page.go'),context);
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function respond(index,value){pending[index].resolve({ok:true,json:async()=>value})}
// row finds one of the five settings-page rows by its own plain label, the
// same way the other *_page_test.js files find a row by content rather than
// a fixed index. Each row is [line, Change button, caption, editor?].
function row(label){return el('settings-rows').children.find(r=>r.children[0].textContent.startsWith(label+' ·'))}
function rowLine(label){return row(label).children[0]}
function rowChange(label){return row(label).children[1]}
// rowEditor returns a row's own editor slot, always present as a fourth
// child (task 2026-09-23, eleventh first-time visitor, requirement 1);
// empty when no editor is open, filled with that editor's own controls
// once one is.
function rowEditor(label){return row(label).children[3]}
(async()=>{
 await tick();await tick();await tick();
 // task 2026-09-23, eleventh first-time visitor, requirement 1: the page
 // used to open with five empty text inputs and a Read button, nothing
 // shown until clicked. It now reads its own state on arrival and shows
 // each setting as a row: the plain label, the effective value in words,
 // and where that value came from.
 assert.equal(el('settings-rows').children.length,5,'one row per setting, Off included');
 assert.equal(rowLine('NAS job directory (deprecated)').textContent,'NAS job directory (deprecated) \u00b7 the default location \u00b7 default ','an unset field states the default plainly, never a blank value');
 assert.equal(rowLine('Job directory (deprecated)').textContent,'Job directory (deprecated) \u00b7 /data/store \u00b7 for you ','a user-location value reads the value and \u201cfor you\u201d, the Panel\u2019s phrase for the user location (D20)');
 assert.equal(rowLine('Log file').textContent,'Log file \u00b7 the default location \u00b7 default ');
 assert.equal(rowLine('Logging service').textContent,'Logging service \u00b7 \\\\.\\pipe\\oa-log \u00b7 for this device ','a machine-location value reads \u201cfor this device\u201d');
 assert.equal(rowLine('Disabled download delegates').textContent,'Disabled download delegates \u00b7 NAS (another machine\'s runtime) disabled \u00b7 default ','a disabled delegate reads its own plain label, not the raw delegate name (D92)');
 assert(row('NAS job directory (deprecated)').children[2].textContent.includes('network share'),'the renamed NAS store row carries its own caption');

 // Change opens a prefilled editor; the path field prefills from the
 // effective value (settingsSnap), not the raw saved one, so a value
 // inherited from a location other than the user's starts from what is
 // actually in effect.
 rowChange('Job directory (deprecated)').click();
 let editor=rowEditor('Job directory (deprecated)');
 assert.equal(editor.children[0].value,'/data/store','the editor prefills from the effective value');
 editor.children[1].click();
 assert.equal(editor.children[0].value,'','Use the default clears the field to hand the key back to the location that would otherwise answer it');
 editor.children[0].value='/new/store';
 settingsReply={Outcome:'applied',Snapshot:{Revision:'r2',Values:{NasStore:'',Store:'/new/store',LogSink:'',LogService:'\\\\.\\pipe\\oa-log',Off:{nas:'no internet here'}}}};
 observeQueue.push({Outcome:'snapshot',Cursor:'c1',Snapshot:{NasStore:'',Store:'/new/store',LogSink:'',LogService:'\\\\.\\pipe\\oa-log',Off:{nas:'no internet here'},Origins:{Store:{Rung:'user'},LogService:{Rung:'machine'}}}});
 editor.children[2].click();await tick();await tick();await tick();
 // Save writes only the row the person changed: every field still rides in
 // the one document the service requires, but every value besides Store is
 // exactly what was last read.
 assert.deepEqual(writes[writes.length-1],{Values:{NasStore:'',Store:'/new/store',LogSink:'',LogService:'\\\\.\\pipe\\oa-log',Off:{nas:'no internet here'}},Revision:'r1'},'Save resends every field, only Store actually different from what was read');
 assert.equal(el('settings-status').textContent,'Saved: Job directory (deprecated).','the confirmation names the one row that changed');
 assert.equal(rowLine('Job directory (deprecated)').textContent,'Job directory (deprecated) \u00b7 /new/store \u00b7 for you ','the row re-reads the just-applied effective value');
 assert.equal(rowEditor('Job directory (deprecated)').children.length,0,'the editor slot empties itself once its own save lands');

 // A save that lands the same value already in effect says so plainly.
 rowChange('Log file').click();
 settingsReply={Outcome:'applied',Snapshot:{Revision:'r3',Values:{NasStore:'',Store:'/new/store',LogSink:'',LogService:'\\\\.\\pipe\\oa-log',Off:{nas:'no internet here'}}}};
 observeQueue.push({Outcome:'snapshot',Cursor:'c2',Snapshot:{NasStore:'',Store:'/new/store',LogSink:'',LogService:'\\\\.\\pipe\\oa-log',Off:{nas:'no internet here'}}});
 rowEditor('Log file').children[2].click();await tick();await tick();await tick();
 assert.equal(el('settings-status').textContent,'Saved; nothing changed.');

 // Disabled download delegates is checkboxes, one per registered delegate,
 // each with its own plain label; the raw delegate name never appears as
 // visible text.
 rowChange('Disabled download delegates').click();
 const delegatesEditor=rowEditor('Disabled download delegates');
 const nasBox=delegatesEditor.children[0].children[0],bitsBox=delegatesEditor.children[1].children[0];
 assert.equal(nasBox.checked,true,'the delegate already disabled starts checked');
 assert.equal(bitsBox.checked,false);
 assert.equal(delegatesEditor.children[0].children[1].textContent,' NAS (another machine\'s runtime)','the checkbox carries its own plain label, not the raw delegate name');
 bitsBox.checked=true;
 settingsReply={Outcome:'applied',Snapshot:{Revision:'r4',Values:{NasStore:'',Store:'/new/store',LogSink:'',LogService:'\\\\.\\pipe\\oa-log',Off:{nas:'no internet here',bits:''}}}};
 observeQueue.push({Outcome:'snapshot',Cursor:'c3',Snapshot:{NasStore:'',Store:'/new/store',LogSink:'',LogService:'\\\\.\\pipe\\oa-log',Off:{nas:'no internet here',bits:''},Origins:{Off:{Rung:'user'}}}});
 delegatesEditor.children[2].click();await tick();await tick();await tick();
 assert.deepEqual(writes[writes.length-1].Values.Off,{nas:'no internet here',bits:''},'checking a second delegate adds it to the Off map, the first delegate\'s own reason kept');
 assert.equal(el('settings-status').textContent,'Saved: Disabled download delegates.');
 assert.equal(rowLine('Disabled download delegates').textContent,'Disabled download delegates \u00b7 NAS (another machine\'s runtime), Windows Background Transfer (BITS) disabled \u00b7 for you ');

 // A conflict reloads the current values instead of leaving a stale editor
 // open on a revision the service already rejected.
 rowChange('Job directory (deprecated)').click();
 rowEditor('Job directory (deprecated)').children[0].value='/conflicting/store';
 settingsReply={Outcome:'conflict'};
 observeQueue.push({Outcome:'snapshot',Cursor:'c4',Snapshot:{NasStore:'',Store:'/reloaded/store',LogSink:'',LogService:'\\\\.\\pipe\\oa-log',Off:{},Origins:{Store:{Rung:'user'}}}});
 getReply={Revision:'r5',Values:{NasStore:'',Store:'/reloaded/store',LogSink:'',LogService:'\\\\.\\pipe\\oa-log',Off:{}}};
 rowEditor('Job directory (deprecated)').children[2].click();await tick();await tick();await tick();
 assert.match(el('settings-status').textContent,/changed elsewhere/);
 assert.equal(rowLine('Job directory (deprecated)').textContent,'Job directory (deprecated) \u00b7 /reloaded/store \u00b7 for you ','the row reflects the reloaded value after a conflict');

 // Watch settings keeps behaving the way it always did: a snapshot renders
 // as plain rows with the raw JSON behind Technical details, a late reply
 // for an aborted watch is ignored, and an unoffered contract is named
 // plainly instead of the generic version-mismatch wording.
 const first=el('follow-settings').onclick();assert.equal(pending.length,1);
 assert.equal(el('follow-settings').textContent,'Stop watching','the button names the running state');
 assert.match(el('settings-observation-status').textContent,/^Watching since/,'a status line says watching started');
 respond(0,{Outcome:'snapshot',Cursor:'w1',Snapshot:{Store:'watched',Origins:{Store:{Rung:'user',Path:'C:\\settings\\user.json'}}}});await tick();await tick();assert.equal(pending.length,2);
 const table=el('effective-settings').children[0].children[0];
 const tbody=table.children[1];
 const watchedRow=tbody.children.find(tr=>tr.children[0].textContent==='Job directory (deprecated)');
 assert.equal(watchedRow.children[1].textContent,'watched');
 // task 2026-09-23, eleventh first-time visitor, finding 9: the Source
 // column read the full path of the settings file as visible text; it now
 // reads a plain phrase, the path kept on that cell's own title.
 assert.equal(watchedRow.children[2].children[0].textContent,'your settings file');
 assert.equal(watchedRow.children[2].children[0].title,'C:\\settings\\user.json');
 const nasWatched=tbody.children.find(tr=>tr.children[0].textContent==='NAS job directory (deprecated)');
 assert.equal(nasWatched.children[1].textContent,'the default location','a field the watched snapshot leaves empty still shows a stated absence');
 const details=el('effective-settings').children[1];
 assert.equal(details.children[0].textContent,'Technical details');
 assert(details.children[1].textContent.includes('"Store": "watched"'));
 await el('follow-settings').onclick();assert(pending[1].options.signal.aborted);
 assert.equal(el('follow-settings').textContent,'Watch settings');
 assert.equal(el('settings-observation-status').textContent,'Stopped.');
 const second=el('follow-settings').onclick();assert.equal(pending.length,3);
 respond(1,{Outcome:'snapshot',Cursor:'late',Snapshot:{Store:'late'}});await first;
 const tbody2=el('effective-settings').children[0].children[0].children[1];
 assert.notEqual(tbody2.children.find(tr=>tr.children[0].textContent==='Job directory (deprecated)').children[1].textContent,'late','a reply for an aborted watch is ignored');
 assert.equal(el('follow-settings').textContent,'Stop watching');
 respond(2,{Outcome:'gap'});await second;assert.equal(el('follow-settings').textContent,'Watch settings');assert.match(el('settings-observation-status').textContent,/continuity was lost/);assert.equal(pending.length,3);

 const third=el('follow-settings').onclick();
 pending[3].resolve({ok:false,text:async()=>'service resolution: incompatible: abstraction.config/observer@1'});
 await third;
 assert.equal(el('follow-settings').textContent,'Watch settings');
 assert.equal(el('settings-observation-status').textContent,"This runtime doesn't offer settings watching.",'the sentence names the actual cause, not a version mismatch');

 console.log('PASS: Settings page reads its own state on arrival, five rows with plain labels, effective values and their locations; Change opens a prefilled editor, Use the default clears it, Save writes only the changed row and names it, a conflict reloads the current values; Disabled download delegates is checkboxes with plain labels; Watch settings renders plain rows, ignores a late reply for an aborted watch, and names an unoffered contract plainly');
})().catch(e=>{console.error(e);process.exitCode=1});
