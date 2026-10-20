package main

// settingsPage is the Panel's Settings page (research/panel-redesign/SPEC.md
// §4, slice S9): "Locations and information." About is shown on arrival
// (identity_page.go: version, runtime and this Panel's identity); Locations,
// Downloads and Developer each sit behind an expander. Developer holds the
// runtime check and Explore (explore_page.go), whose old page path sends a
// navigation here. The detailed log moved to Activity (S5).
//
// Settings read themselves on arrival and follow changes while the page is
// visible. Each Location row names where the value lives in words
// ("Default location" or "Custom location", and the D20 location phrase:
// default, for you, for this device, for this run); the path itself sits on
// the row's title, never as page text. A row's chooser picks the default or
// "Custom location…", which shows the path field prefilled with the value in
// effect. Downloads is one switch per download delegate this build registers
// (DECISION.md D92). Every save sends the whole UserSettings document with
// one field changed (abstraction-config's UserReplaceResult merges nothing)
// and writes one sentence into its row's status line. Both job store locations
// use the existing config editor; saving an address makes no network request.
var settingsPage = oaPageHead("settings", "Settings", "Locations and information.") + `<div data-ready-owner="page" hidden></div>` +
	identitySection + setupStepSection + typedSettingsSection +
	`<section id="settings" class="oa-group"><h2>Locations and downloads</h2>` + oaStatusLine("settings-status", false) +
	`<div id="settings-archive"></div>` +
	oaExpander("Locations", `<div id="settings-rows"></div>`) +
	oaExpander("Downloads", `<div id="settings-downloads"></div>`) + `</section>` +
	`<section id="typed-advanced" class="oa-group"><h2>Advanced</h2>` + typedAdvancedStarts +
	oaExpander("Job folders", `<div id="settings-job-rows"></div>`) + `</section>` + `
<section id="runtime-support" class="oa-group"><h2>Developer</h2>` +
	oaExpander("Developer tools",
		oaRow("Show developer tools", "", `<input type="checkbox" class="switch" id="developer-switch" aria-label="Show developer tools">`, "developer-status")+
			`<div id="developer-tools"></div><template id="developer-template">`+
			oaRow("Connection", "", `<button id="status" type="button">Check the service</button>`, "check-status")+
			`<div id="runtime"></div>`+exploreBody+`</template>`) + `</section>
<script>` + serviceCommonScript + `
// settingsFields names the location settings shown (abstraction-config/go/
// config.go's doc comments give each caption): LogSink is a file every tool
// appends structured records to; LogService is a local socket that attests
// identity.
const settingsFields=[
 ['LogSink','Log file'],
 ['LogService','Logging service'],
];
// settingsJobFields are the job stores, under Advanced › Job folders: Store
// is the local job store, NasStore a job store on a share that a supervisor
// elsewhere watches. Both are deprecated mechanisms most people never set.
const settingsJobFields=[
 ['Store','Job folder'],
 ['NasStore','Network job folder'],
];
// settingsDelegates names the download delegates this build registers
// (abstraction-download/go/nas and .../bits, each an init() RegisterTier
// call). A key Off already carries that names neither keeps its row, labelled
// by the raw name, so a value nothing here recognizes is never dropped.
// The NAS delegate stays hidden (owner, 2026-11-01: the NAS is not ready for
// models or setup); its Off value is still sent back as read.
const settingsDelegates=[['bits','Windows Background Transfer (BITS)']];
const settingsHiddenDelegates=['nas'];
// Reserved for locations that a future reader exposes without editing.
const settingsReadOnlyFields=[];
function settingsDelegateLabel(delegate){const known=settingsDelegates.find(d=>d[0]===delegate);return known?known[1]:delegate}
// settingsOriginWords reads one field's Origin (Snapshot.Origins.<field>; its
// wire field is still Rung until reader@2) as the location phrases of
// DECISION.md D20.
function settingsOriginWords(o){if(!o||!o.Rung||o.Rung==='default')return 'default';if(o.Rung==='user')return 'for you';if(o.Rung==='machine')return 'for this device';if(o.Rung==='environment')return 'for this run';return o.Rung}
let settingsValues=null,settingsRevision='',settingsSnap=null,settingsEditing=false;
function settingsOrigin(field){return settingsOriginWords(((settingsSnap&&settingsSnap.Origins)||{})[field])}
// The chooser already reads Default or Custom location; the caption names
// only where a set value lives ("For you"), and is hidden otherwise.
function settingsLocationCaption(field){const where=settingsOrigin(field);return where==='default'?'':capitalize(where)}
function settingsStatusId(field){return 'settings-status-'+field}
// settingsRowShell builds one oa-row: label, caption, the control and its
// own status line.
function settingsRowShell(label,caption,title,controls,statusId){
 const row=document.createElement('div');row.className='oa-row';
 const text=document.createElement('div');text.className='oa-row-text';
 const l=document.createElement('span');l.className='oa-row-label';l.textContent=label;
 const c=document.createElement('span');c.className='oa-row-caption';c.textContent=caption;if(title)c.title=title;
 text.append(l,c);
 const control=document.createElement('div');control.className='oa-row-control';control.append(...controls);
 const status=document.createElement('p');status.className='oa-status';status.id=statusId;if(status.setAttribute){status.setAttribute('role','status');status.setAttribute('aria-live','polite')}
 row.append(text,control,status);row.status=status;return row}
function settingsCurrentOff(){return (settingsSnap&&settingsSnap.Off)||(settingsValues&&settingsValues.Off)||{}}
// A Location row: Default location or Custom location…, which shows the
// path field (an "Other…" field, data-oa-other) prefilled with the value in
// effect. Save and Cancel appear on a row once the person changes it, and
// both end the edit, so live updates resume.
function settingsLocationRow([field,label]){
 const select=document.createElement('select');select.setAttribute&&select.setAttribute('aria-label',label);
 for(const[value,text]of[['default','Default location'],['custom','Custom location…']]){const o=document.createElement('option');o.value=value;o.textContent=text;select.append(o)}
 const input=document.createElement('input');input.type='text';if(input.setAttribute){input.setAttribute('aria-label',label);input.setAttribute('data-oa-other','')}
 const save=document.createElement('button');save.type='button';save.textContent='Save';save.hidden=true;
 const cancel=document.createElement('button');cancel.type='button';cancel.textContent='Cancel';cancel.hidden=true;
 const row=settingsRowShell(label,'','',[select,input,save,cancel],settingsStatusId(field));
 const beginEdit=()=>{row.editing=true;settingsEditing=true;save.hidden=false;cancel.hidden=false};
 const endEdit=()=>{row.editing=false;settingsEditing=false;save.hidden=true;cancel.hidden=true};
 select.onchange=()=>{beginEdit();if(select.value==='custom'&&!input.value)input.value=(settingsSnap&&settingsSnap[field])||(settingsValues&&settingsValues[field])||'';input.hidden=select.value!=='custom'};
 input.oninput=beginEdit;
 save.onclick=async()=>{const value=select.value==='custom'?input.value.trim():'';if(select.value==='custom'&&!value){oaSetStatus(settingsStatusId(field),'error','Enter a location for '+label+'.');return}if(/[\x00-\x1f\x7f]/.test(value)){oaSetStatus(settingsStatusId(field),'error','Enter a location without control characters.');return}endEdit();await settingsSaveField(field,value,label,settingsStatusId(field))};
 cancel.onclick=()=>{endEdit();settingsUpdateLocationRow(row,[field,label]);oaSetStatus(settingsStatusId(field),'ok','Nothing changed.')};
 Object.assign(row,{select,input,save,cancel});return row}
// settingsUpdateLocationRow refreshes a row in place; a row being edited
// keeps what the person chose.
function settingsUpdateLocationRow(row,[field]){
 const current=(settingsSnap&&settingsSnap[field])||'';
 row.children[0].children[1].textContent=settingsLocationCaption(field);row.children[0].children[1].hidden=!row.children[0].children[1].textContent;row.children[0].children[1].title=current;
 if(row.editing)return;
 row.select.value=current?'custom':'default';row.input.value=current;row.input.hidden=!current}
// A read-only Location row: its caption names where the value lives and the
// path sits on the caption's title.
function settingsReadOnlyRow([field,label]){return settingsRowShell(label,'','',[],settingsStatusId(field))}
function settingsUpdateReadOnlyRow(row,[field]){const current=(settingsSnap&&settingsSnap[field])||'';row.children[0].children[1].textContent=settingsLocationCaption(field);row.children[0].children[1].hidden=!row.children[0].children[1].textContent;row.children[0].children[1].title=current}
function settingsDelegateRow([delegate,label]){
 const box=document.createElement('input');box.type='checkbox';box.className='switch';box.setAttribute&&box.setAttribute('aria-label',label);
 box.onchange=()=>{const off=settingsCurrentOff(),next={...off};if(box.checked)delete next[delegate];else next[delegate]=off[delegate]||'';settingsSaveField('Off',next,label,settingsStatusId('Off-'+delegate),box.checked?label+' is on.':label+' is off.')};
 const row=settingsRowShell(label,'',null,[box],settingsStatusId('Off-'+delegate));
 row.box=box;return row}
// The switch shows On or Off; the row has no caption repeating it.
function settingsUpdateDelegateRow(row,[delegate]){row.box.checked=!(delegate in settingsCurrentOff());row.children[0].children[1].hidden=true}
// renderSettingsRows updates the rows in place (Panel rule: a reread keeps
// each row's sentence and focus).
function renderSettingsRows(){
 oaKeyedRows(el('settings-rows'),settingsFields.concat(settingsReadOnlyFields),f=>f[0],f=>settingsReadOnlyFields.includes(f)?settingsReadOnlyRow(f):settingsLocationRow(f),(row,f)=>settingsReadOnlyFields.includes(f)?settingsUpdateReadOnlyRow(row,f):settingsUpdateLocationRow(row,f));
 oaKeyedRows(el('settings-job-rows'),settingsJobFields,f=>f[0],settingsLocationRow,settingsUpdateLocationRow);
 const delegates=settingsDelegates.slice();for(const k of Object.keys(settingsCurrentOff()))if(!delegates.find(d=>d[0]===k)&&!settingsHiddenDelegates.includes(k))delegates.push([k,k]);
 oaKeyedRows(el('settings-downloads'),delegates,d=>d[0],settingsDelegateRow,settingsUpdateDelegateRow);
}
// The Download archive row (setup/download_archive.go over the
// abstraction-download archive package): a folder, often on a NAS, that the
// download service checks before every download that names its SHA-256,
// under any file name, and fills after it. It
// shows the stored setting and listing on arrival in words, with the path on
// the caption's title, and adds "not reachable now" when a probe that
// follows finds the folder silent. The folder comes from the native chooser,
// or a typed path where this Panel has none. Choosing a folder and Look
// again list it in the background; the row follows the listing, then says
// what it found. Keep a copy of new downloads here is a switch row under it,
// shown with a folder.
let archiveState=null,archiveBusy=false,archiveProbeRun=0,archiveFollowRun=0;
function archiveNumber(n){return Number(n||0).toLocaleString('en-US')}
function archiveCaption(s){if(!s||!s.folder)return 'Not set';let c=s.network?'Network folder':'Folder on this PC';if(s.looking)c+=' · looking…';else if(s.walked_at)c+=s.found?' · '+archiveNumber(s.found)+(s.found===1?' model found':' models found'):' · no models found';if(s.reachable===false)c+=' · not reachable now';return c}
function archiveKeepSentence(s){return s&&s.keep===false?'Downloads check this folder first.':'Downloads check this folder first and keep a copy there.'}
function archiveFoundSentence(s){
 if(s.unanswered)return 'The folder did not answer. Open it once in File Explorer, then Look again.';
 if(s.incomplete&&s.stopped==='files')return 'Stopped after 50,000 files. Choose the folder that holds the models.';
 let t=s.found?'Found '+archiveNumber(s.found)+(s.found===1?' model':' models')+' Setup can use.':'Found no models Setup can use.';
 if(s.other)t+=' '+archiveNumber(s.other)+(s.other===1?' other model file here is':' other model files here are')+" not in the Catalog.";
 return t}
function archiveRow(){
 let row=el('settings-archive').children[0];if(row)return row;
 const button=(text,onclick)=>{const b=document.createElement('button');b.type='button';b.textContent=text;b.onclick=onclick;return b};
 const choose=button('Choose folder…',archiveChoose),look=button('Look again',archiveLook),stop=button('Stop using',()=>archiveSave(''));
 const keepBox=document.createElement('input');keepBox.type='checkbox';keepBox.className='switch';if(keepBox.setAttribute)keepBox.setAttribute('aria-label','Keep a copy of new downloads here');keepBox.onchange=()=>archiveKeep(keepBox.checked);
 const keep=settingsRowShell('Keep a copy of new downloads here','',null,[keepBox],'settings-archive-keep-status');keep.children[0].children[1].hidden=true;
 const input=document.createElement('input');input.type='text';input.hidden=true;if(input.setAttribute){input.setAttribute('aria-label','Download archive folder');input.setAttribute('data-oa-other','')}
 const save=button('Save',()=>{const v=input.value.trim();if(!v){oaSetStatus('settings-archive-status','error','Enter a folder for the download archive.');return}return archiveSave(v)});save.hidden=true;
 row=settingsRowShell('Download archive','',null,[choose,look,stop,input,save],'settings-archive-status');Object.assign(row,{choose,look,stop,keep,keepBox,input,save});
 el('settings-archive').append(row,keep);return row}
function renderArchive(){const row=archiveRow(),c=row.children[0].children[1],s=archiveState||{},set=!!s.folder;c.textContent=archiveCaption(s);c.title=s.folder||'';row.look.hidden=row.stop.hidden=row.keep.hidden=!set;row.look.disabled=!!s.looking;row.keepBox.checked=s.keep!==false}
async function archiveProbe(){const run=++archiveProbeRun;if(!archiveState||!archiveState.folder)return;try{const probed=await call('/settings/archive?probe=1');if(run===archiveProbeRun&&archiveState&&probed.folder===archiveState.folder){archiveState=probed;renderArchive()}}catch(e){}}
// archiveFollow rereads the row each second while a listing runs; say
// writes what the listing found once it ends.
async function archiveFollow(say){
 const run=++archiveFollowRun;let followed=false;
 while(archiveState&&archiveState.looking){await new Promise(r=>setTimeout(r,1000));if(run!==archiveFollowRun)return;try{archiveState=await call('/settings/archive')}catch(e){return}followed=true;renderArchive()}
 if(run!==archiveFollowRun||!archiveState||!archiveState.folder)return;
 if(followed)archiveProbe();
 if(say)oaSetStatus('settings-archive-status',archiveState.unanswered?'error':'ok',archiveFoundSentence(archiveState))}
async function archiveLoad(){try{archiveState=await call('/settings/archive');renderArchive();archiveProbe();archiveFollow(false)}catch(e){archiveRow();fail('settings-archive-status',e)}}
async function archiveSave(folder){
 try{const before=(archiveState&&archiveState.folder)||'';archiveState=await call('/settings/archive',{folder});const row=archiveRow();row.input.hidden=true;row.save.hidden=true;renderArchive();archiveProbe();
  oaSetStatus('settings-archive-status','ok',archiveState.folder===before?'Nothing changed.':archiveState.folder?archiveKeepSentence(archiveState):'Downloads no longer use an archive.');
  if(archiveState.folder&&archiveState.folder!==before)await archiveFollow(true)}
 catch(e){fail('settings-archive-status',e)}}
async function archiveLook(){
 try{archiveState=await call('/settings/archive',{look:true});renderArchive();oaSetStatus('settings-archive-status','loading','');await archiveFollow(true)}
 catch(e){fail('settings-archive-status',e)}}
async function archiveKeep(on){
 try{archiveState=await call('/settings/archive',{keep:on});renderArchive();oaSetStatus('settings-archive-keep-status','ok',archiveKeepSentence(archiveState))}
 catch(e){renderArchive();fail('settings-archive-keep-status',e)}}
async function archiveChoose(){
 if(archiveBusy)return;archiveBusy=true;const row=archiveRow();
 try{oaSetStatus('settings-archive-status','loading','Choosing a folder…');const picked=await call('/settings/archive/folder',{});
  if(!picked.available){row.input.hidden=false;row.save.hidden=false;row.input.value=(archiveState&&archiveState.folder)||'';oaSetStatus('settings-archive-status','idle','Enter the full path of the folder.');return}
  if(!picked.path){oaSetStatus('settings-archive-status','idle','');return}
  await archiveSave(picked.path)}
 catch(e){fail('settings-archive-status',e)}finally{archiveBusy=false}}
var archiveReady=archiveLoad();
async function settingsRefreshSnapshot(){const p=await call('/settings/observe?'+new URLSearchParams({cursor:''}));if(p.Outcome==='snapshot')settingsSnap=p.Snapshot}
// settingsSaveField sends the one changed field inside the full UserSettings
// document and writes one sentence into the changed row's status line: what
// was saved, that nothing changed, or that the settings changed elsewhere.
async function settingsSaveField(field,newValue,label,statusId,sentence){
 const values={...(settingsValues||{})};const before=field==='Off'?JSON.stringify(values.Off||{}):(values[field]||'');
 values[field]=newValue;
 const after=field==='Off'?JSON.stringify(newValue||{}):(newValue||'');
 const say=text=>{if(statusId&&el(statusId))oaSetStatus(statusId,'ok',text);else show('settings-status',text)};
 try{
  const r=await call('/settings',{Values:values,Revision:settingsRevision});
  if(r.Outcome==='applied'){
   settingsValues=(r.Snapshot&&r.Snapshot.Values)||values;settingsRevision=(r.Snapshot&&r.Snapshot.Revision)||'';
   await settingsRefreshSnapshot();settingsLast=JSON.stringify([settingsValues,settingsSnap]);renderSettingsRows();
   say(after===before?'Saved; nothing changed.':(sentence||'Saved: '+label+'.'));
   return
  }
  if(r.Outcome==='conflict'){await settingsLoad(true);say('Settings changed elsewhere; the current values are shown.');return}
  say('The settings service did not save this: '+r.Outcome+'.')
 }catch(e){fail(statusId||'settings-status',e)}
}
// An unchanged reading leaves the rows, their status sentences and focus as
// they are.
let settingsLast='';
async function settingsLoad(force,page,signal){try{const v=await call('/settings',null,signal);if(signal&&signal.aborted)return false;settingsValues=v.Values||{};settingsRevision=v.Revision||'';if(page)settingsSnap=page.Snapshot;else await settingsRefreshSnapshot();const sig=JSON.stringify([settingsValues,settingsSnap]);if(force||sig!==settingsLast){settingsLast=sig;renderSettingsRows()}return true}catch(e){if(!signal||!signal.aborted)fail('settings-status',e);return false}}
var settingsReady=settingsLoad();
` + setupStepLinkScript + `
// Retain the owner's cursor across bounded waits and reconnects. A gap
// requires a fresh snapshot; it never licenses continuing from stale state.
// One presenter owns one request, cancelled when hidden or navigated away.
let settingsFollowRun=null,settingsFollowCursor='',settingsFollowClosed=false;
async function settingsFollow(run){
 while(!run.signal.aborted){
  let retry=false;
  try{
   const p=await call('/settings/observe?'+new URLSearchParams({cursor:settingsFollowCursor}),null,run.signal);
   if(run.signal.aborted)return;
   if(p.Outcome==='gap'){settingsFollowCursor='';continue}
   if(p.Outcome==='snapshot'){
    // Do not consume a changed snapshot while an edit is in progress. The
    // same cursor will receive it again after the edit ends.
    if(!settingsEditing){const loaded=await settingsLoad(false,p,run.signal);if(run.signal.aborted)return;if(loaded)settingsFollowCursor=p.Cursor;else retry=true}
   }else if(p.Outcome!=='unchanged')throw Error('Settings could not be refreshed.');
  }catch(e){
   if(run.signal.aborted)return;
   oaRuntimeEcho('settings-status',()=>fail('settings-status',e),e&&e.message);retry=true;
  }
  // Editing and failed connections need a bounded pause, not a busy retry.
  if(settingsEditing||retry)await new Promise(resolve=>{const timer=setTimeout(done,1000);function done(){clearTimeout(timer);run.signal.removeEventListener('abort',done);resolve()}run.signal.addEventListener('abort',done,{once:true})});
 }
}
function settingsFollowStop(){if(settingsFollowRun){settingsFollowRun.abort();settingsFollowRun=null}}
function settingsFollowStart(){if(settingsFollowClosed||settingsFollowRun||document.visibilityState==='hidden'||typeof AbortController==='undefined')return;const run=new AbortController();settingsFollowRun=run;settingsFollow(run).finally(()=>{if(settingsFollowRun===run)settingsFollowRun=null})}
settingsReady.then(settingsFollowStart);
if(typeof window!=='undefined'&&typeof window.addEventListener==='function'){
 window.addEventListener('pagehide',()=>{settingsFollowClosed=true;settingsFollowStop()});
 window.addEventListener('pageshow',()=>{settingsFollowClosed=false;settingsFollowStart()});
}
if(typeof document.addEventListener==='function')document.addEventListener('visibilitychange',()=>{if(document.visibilityState==='hidden')settingsFollowStop();else settingsFollowStart()});
` + identityScript + runtimeSupportScript + `
// Developer tools are expert-only: their forms are not in the page until
// Show developer tools is on, remembered per viewer (localStorage, which
// may be unavailable).
let developerStarted=false;
function developerStart(){if(developerStarted)return;developerStarted=true;el('developer-tools').replaceChildren(el('developer-template').content.cloneNode(true));runtimeSupportStart();` + exploreScript + `}
function developerStop(){developerStarted=false;el('developer-tools').replaceChildren()}
function developerRemembered(){try{return localStorage.getItem('oa-developer-tools')==='on'}catch(e){return false}}
el('developer-switch').checked=developerRemembered();if(el('developer-switch').checked)developerStart();
el('developer-switch').onchange=()=>{const on=el('developer-switch').checked;try{localStorage.setItem('oa-developer-tools',on?'on':'off')}catch(e){}if(on)developerStart();else developerStop();oaSetStatus('developer-status','ok',on?'Developer tools are shown.':'Developer tools are hidden.')};
// About's Runtime row reads the runtime connection on arrival.
async function aboutRuntime(){try{const v=await call('/runtime');show('about-runtime',v.error?'Not running':runtimeSentence(v));el('about-runtime').title=v.explicit?v.explicit.endpoint:''}catch(e){fail('about-runtime',e)}}
var aboutRuntimeReady=aboutRuntime();
if(typeof oaOnRuntimeBack==='function')oaOnRuntimeBack(()=>Promise.all([aboutRuntime(),identityCheck(),settingsFollowStart()]));
// The page tells the screenshot tool it is ready once its first readings
// have finished (S1's oaReady; data-ready until it exists).
function oaPageReady(){if(typeof oaReady==='function')oaReady();else if(typeof document!=='undefined'&&document.documentElement)document.documentElement.dataset.ready='1'}
` + typedSettingsScript + `
Promise.all([settingsReady,archiveReady,identityCheckReady,aboutRuntimeReady,typedSettingsReady]).then(oaPageReady,oaPageReady);
</script>`
