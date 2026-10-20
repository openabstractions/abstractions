package main

// settingsPage is the Panel's Settings page (research/panel-redesign/SPEC.md
// §4, slice S9): "Locations and information." About is shown on arrival
// (identity_page.go: version, runtime and this Panel's identity); Locations,
// Downloads and Developer each sit behind an expander. Developer holds the
// runtime check and Explore (explore_page.go), whose old page path sends a
// navigation here. The detailed log moved to Activity (S5).
//
// Settings read themselves on arrival and again every 15 seconds while the
// page is visible. Each Location row names where the value lives in words
// ("Default location" or "Custom location", and the D20 location phrase:
// default, for you, for this device, for this run); the path itself sits on
// the row's title, never as page text. A row's chooser picks the default or
// "Custom location…", which shows the path field prefilled with the value in
// effect. Downloads is one switch per download delegate this build registers
// (DECISION.md D92). Every save sends the whole UserSettings document with
// one field changed (abstraction-config's UserReplaceResult merges nothing)
// and writes one sentence into its row's status line. The two deprecated
// job-store folders are no longer shown; their values are sent back as read.
var settingsPage = oaPageHead("settings", "Settings", "Locations and information.") + `<div data-ready-owner="page" hidden></div>` +
	identitySection +
	`<section id="settings" class="oa-group"><h2>Locations and downloads</h2>` + oaStatusLine("settings-status", false) +
	oaExpander("Locations", `<div id="settings-rows"></div>`) +
	oaExpander("Downloads", `<div id="settings-downloads"></div>`) + `</section>
<section id="runtime-support" class="oa-group"><h2>Developer</h2>` +
	oaExpander("Developer tools",
		oaRow("Show developer tools", "", `<input type="checkbox" class="switch" id="developer-switch" aria-label="Show developer tools">`, "developer-status")+
			`<div id="developer-tools"></div><template id="developer-template">`+
			oaRow("Runtime connection", "", `<button id="status" type="button">Check the service</button>`, "check-status")+
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
// settingsDelegates names the download delegates this build registers
// (abstraction-download/go/nas and .../bits, each an init() RegisterTier
// call). A key Off already carries that names neither keeps its row, labelled
// by the raw name, so a value nothing here recognizes is never dropped.
const settingsDelegates=[['nas',"NAS (another machine's runtime)"],['bits','Windows Background Transfer (BITS)']];
function settingsDelegateLabel(delegate){const known=settingsDelegates.find(d=>d[0]===delegate);return known?known[1]:delegate}
// settingsOriginWords reads one field's Origin (Snapshot.Origins.<field>; its
// wire field is still Rung until reader@2) as the location phrases of
// DECISION.md D20.
function settingsOriginWords(o){if(!o||!o.Rung||o.Rung==='default')return 'default';if(o.Rung==='user')return 'for you';if(o.Rung==='machine')return 'for this device';if(o.Rung==='environment')return 'for this run';return o.Rung}
let settingsValues=null,settingsRevision='',settingsSnap=null,settingsEditing=false;
function settingsOrigin(field){return settingsOriginWords(((settingsSnap&&settingsSnap.Origins)||{})[field])}
function settingsLocationCaption(field){const v=settingsSnap&&settingsSnap[field];return (v?'Custom location':'Default location')+' · '+settingsOrigin(field)}
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
// effect; Save and Cancel both end the edit, so live updates resume.
function settingsLocationRow([field,label]){
 const select=document.createElement('select');select.setAttribute&&select.setAttribute('aria-label',label);
 for(const[value,text]of[['default','Default location'],['custom','Custom location…']]){const o=document.createElement('option');o.value=value;o.textContent=text;select.append(o)}
 const input=document.createElement('input');input.type='text';if(input.setAttribute){input.setAttribute('aria-label',label);input.setAttribute('data-oa-other','')}
 const save=document.createElement('button');save.type='button';save.textContent='Save';
 const cancel=document.createElement('button');cancel.type='button';cancel.textContent='Cancel';
 const row=settingsRowShell(label,'','',[select,input,save,cancel],settingsStatusId(field));
 const endEdit=()=>{row.editing=false;settingsEditing=false};
 select.onchange=()=>{row.editing=true;settingsEditing=true;if(select.value==='custom'&&!input.value)input.value=(settingsSnap&&settingsSnap[field])||(settingsValues&&settingsValues[field])||'';input.hidden=select.value!=='custom'};
 input.oninput=()=>{row.editing=true;settingsEditing=true};
 save.onclick=async()=>{endEdit();await settingsSaveField(field,select.value==='custom'?input.value.trim():'',label,settingsStatusId(field))};
 cancel.onclick=()=>{endEdit();settingsUpdateLocationRow(row,[field,label]);oaSetStatus(settingsStatusId(field),'ok','Nothing changed.')};
 Object.assign(row,{select,input,save,cancel});return row}
// settingsUpdateLocationRow refreshes a row in place; a row being edited
// keeps what the person chose.
function settingsUpdateLocationRow(row,[field]){
 const current=(settingsSnap&&settingsSnap[field])||'';
 row.children[0].children[1].textContent=settingsLocationCaption(field);row.children[0].children[1].title=current;
 if(row.editing)return;
 row.select.value=current?'custom':'default';row.input.value=current;row.input.hidden=!current}
function settingsDelegateRow([delegate,label]){
 const box=document.createElement('input');box.type='checkbox';box.className='switch';box.setAttribute&&box.setAttribute('aria-label',label);
 box.onchange=()=>{const off=settingsCurrentOff(),next={...off};if(box.checked)delete next[delegate];else next[delegate]=off[delegate]||'';settingsSaveField('Off',next,label,settingsStatusId('Off-'+delegate),box.checked?label+' is on.':label+' is off.')};
 const row=settingsRowShell(label,'',null,[box],settingsStatusId('Off-'+delegate));
 row.box=box;return row}
function settingsUpdateDelegateRow(row,[delegate]){const on=!(delegate in settingsCurrentOff());row.box.checked=on;row.children[0].children[1].textContent=on?'On':'Off'}
// renderSettingsRows updates the rows in place (Panel rule: a reread keeps
// each row's sentence and focus).
function renderSettingsRows(){
 oaKeyedRows(el('settings-rows'),settingsFields,f=>f[0],settingsLocationRow,settingsUpdateLocationRow);
 const delegates=settingsDelegates.slice();for(const k of Object.keys(settingsCurrentOff()))if(!delegates.find(d=>d[0]===k))delegates.push([k,k]);
 oaKeyedRows(el('settings-downloads'),delegates,d=>d[0],settingsDelegateRow,settingsUpdateDelegateRow);
}
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
async function settingsLoad(force){try{const v=await call('/settings');settingsValues=v.Values||{};settingsRevision=v.Revision||'';await settingsRefreshSnapshot();const sig=JSON.stringify([settingsValues,settingsSnap]);if(!force&&sig===settingsLast)return;settingsLast=sig;renderSettingsRows()}catch(e){fail('settings-status',e)}}
var settingsReady=settingsLoad();
// The page updates itself while it is visible, unless a row is being edited.
// oaQuiet runs one reading at a time: a timer tick while one is still
// running is skipped.
function oaQuietOnce(fn){let busy=false;return async()=>{if(busy)return;busy=true;try{await fn()}finally{busy=false}}}
const settingsQuiet=oaQuietOnce(()=>settingsLoad());
if(typeof setInterval==='function')setInterval(()=>{if(document.visibilityState!=='hidden'&&!settingsEditing)settingsQuiet()},15000);
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
async function aboutRuntime(){try{const v=await call('/runtime');show('about-runtime',v.error?'Not connected. Try again or restart OpenAbstractions.':runtimeSentence(v));el('about-runtime').title=v.explicit?v.explicit.endpoint:''}catch(e){fail('about-runtime',e)}}
var aboutRuntimeReady=aboutRuntime();
// The page tells the screenshot tool it is ready once its first readings
// have finished (S1's oaReady; data-ready until it exists).
function oaPageReady(){if(typeof oaReady==='function')oaReady();else if(typeof document!=='undefined'&&document.documentElement)document.documentElement.dataset.ready='1'}
Promise.all([settingsReady,identityCheckReady,aboutRuntimeReady]).then(oaPageReady,oaPageReady);
</script>`
