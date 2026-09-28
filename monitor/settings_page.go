package main

// settingsPage is the Panel's Settings page: the person's saved settings,
// and what's in effect right now (task 2026-09-23, requirement 1: split out
// of the Status page, its own page at /settings).
//
// The page used to open with five empty text inputs and a Read button,
// nothing shown until clicked; the owner's rule (task 2026-09-23, eleventh
// first-time visitor) is that no field opens empty when a default or the
// current state can fill it. It now reads its own state on arrival and
// shows each setting as a row: the plain label, the effective value in
// words, and where that value came from (default, for you, for this device,
// for this run: the location phrases of DECISION.md D20); a Change control opens a small editor for that one row only,
// prefilled with the current value, with "Use the default" beside it. Save
// sends only that row's field, changed, inside the one UserSettings
// document the service accepts (task 2026-09-23, requirement: "no outcome
// merges settings implicitly" — abstraction-config/go/rec.go's own comment
// on UserReplaceResult — so every save still carries every field, just with
// only one of them actually different from what was last read).
const settingsPage = `<h1 id="settings">Settings</h1><p class="oa-note">Your saved settings, and what's in effect right now.</p>
<div id="settings-rows"></div>
<pre id="settings-status"></pre>
<p class="oa-note">See the settings currently in effect.</p><button id="follow-settings">Watch settings</button><pre id="settings-observation-status" aria-live="polite"></pre><div id="effective-settings"></div>
<script>` + serviceCommonScript + `
// settingsFields names every path-shaped setting key (abstraction-config/go/
// config.go's own doc comments give each caption): NasStore is a job store on
// a share a supervisor elsewhere watches; Store is the local job store;
// LogSink is a file every tool appends structured records to; LogService is
// a local socket that attests identity. The two job-store rows carry
// "(deprecated)" with the legacy job store they point at (config README,
// RENAME-PLAN config row 7). Off, the download delegates this machine will
// not hand work to (DECISION.md D92), is its own row below, since it is a
// set of switches, not a path.
const settingsFields=[
 ['NasStore','NAS job directory (deprecated)','A folder on a network share that another machine\'s runtime also watches.'],
 ['Store','Job directory (deprecated)','The folder this runtime keeps its own downloads and job records in.'],
 ['LogSink','Log file','The file every tool on this account appends structured log records to.'],
 ['LogService','Logging service','The local socket that attests which program and account a log record came from.'],
];
// settingsDelegates names the download delegates this build registers
// (abstraction-download/go/nas and .../bits, each an init() RegisterTier
// call): the only two Off can name in a build that links this program's own
// delegates. A key Off already carries that names neither still shows, labelled
// by the raw name, so a value nothing here recognizes is never silently
// dropped from the editor.
const settingsDelegates=[['nas',"NAS (another machine's runtime)"],['bits','Windows Background Transfer (BITS)']];
function settingsDelegateLabel(delegate){const known=settingsDelegates.find(d=>d[0]===delegate);return known?known[1]:delegate}
// settingsOriginWords reads one field's Origin (Snapshot.Origins.<field>,
// abstraction-config/go/config.go's Origin; its wire field is still Rung
// until reader@2) as the location phrases of DECISION.md D20: "default",
// "for you" (the user location), "for this device" (the machine location)
// or "for this run" (an environment override for this process).
function settingsOriginWords(o){if(!o||!o.Rung||o.Rung==='default')return 'default';if(o.Rung==='user')return 'for you';if(o.Rung==='machine')return 'for this device';if(o.Rung==='environment')return 'for this run';return o.Rung}
// settingsOriginText reads one field's Origin (Snapshot.Origins.<field>,
// abstraction-config/go/config.go's Origin.String()) for the Watch
// settings table below: the location phrase alone, or a plain phrase naming
// whose settings file answered, never the file's own full path (task
// 2026-09-23, eleventh first-time visitor, finding 9: the Local store
// row's Source column read the full path of config.json as visible text).
// settingsOriginTitle keeps that path reachable as the cell's own title.
function settingsOriginText(o){if(!o||!o.Rung)return'default';if(!o.Path)return settingsOriginWords(o);if(o.Rung==='user')return'your settings file';if(o.Rung==='machine')return"this device's settings file";return o.Rung+' file'}
function settingsOriginTitle(o){return o&&o.Path?o.Path:''}
function settingsPathWords(v){return v?v:'the default location'}
function settingsOffWords(off){const keys=Object.keys(off||{});return keys.length?keys.map(settingsDelegateLabel).join(', ')+' disabled':'nothing disabled'}
let settingsValues=null,settingsRevision='',settingsSnap=null;
function settingsRowValue(field){return field==='Off'?settingsOffWords(settingsSnap&&settingsSnap.Off):settingsPathWords(settingsSnap&&settingsSnap[field])}
function settingsRowSource(field){const origins=(settingsSnap&&settingsSnap.Origins)||{};return settingsOriginWords(origins[field])}
// Every row carries its own fourth child, an editor slot, present and empty
// from the start; opening or closing an editor is filling or clearing that
// one slot (document.replaceChildren, the same call every other page in
// this Panel already uses to redraw a list), never removing an element by
// itself, which the fake DOM every *_page_test.js file drives does not
// support any more than it supports document.querySelector.
let settingsOpenSlot=null;
function settingsCloseEditor(){if(settingsOpenSlot){settingsOpenSlot.replaceChildren();settingsOpenSlot=null}}
// settingsPathEditor prefills its field from settingsSnap, the effective
// value this run actually uses, never from the raw settingsValues alone: a
// value inherited from the machine file or a built-in default is what a
// person editing it needs to see and start from, and "Use the default"
// clears the field to hand the key back to whatever would otherwise answer
// it (task 2026-09-23, eleventh first-time visitor, requirement 1).
function settingsPathEditor(field,slot){settingsCloseEditor();settingsOpenSlot=slot;
 const input=document.createElement('input');input.type='text';input.setAttribute('aria-label',(settingsFields.find(f=>f[0]===field)||['',field])[1]);input.value=(settingsSnap&&settingsSnap[field])||'';
 const useDefault=document.createElement('button');useDefault.type='button';useDefault.textContent='Use the default';useDefault.onclick=()=>{input.value=''};
 const save=document.createElement('button');save.type='button';save.textContent='Save';save.onclick=()=>settingsSaveField(field,input.value.trim());
 const cancel=document.createElement('button');cancel.type='button';cancel.textContent='Cancel';cancel.onclick=settingsCloseEditor;
 slot.replaceChildren(input,useDefault,save,cancel)}
function settingsDelegatesEditor(slot){settingsCloseEditor();settingsOpenSlot=slot;
 const off=(settingsSnap&&settingsSnap.Off)||(settingsValues&&settingsValues.Off)||{};const boxes=[],rows=[];
 for(const[delegate,label]of settingsDelegates){const wrap=document.createElement('label');const box=document.createElement('input');box.type='checkbox';box.className='switch';box.checked=delegate in off;box.dataset.delegate=delegate;wrap.append(box,document.createTextNode(' '+label));rows.push(wrap);boxes.push(box)}
 const save=document.createElement('button');save.type='button';save.textContent='Save';save.onclick=()=>{const next={};for(const box of boxes)if(box.checked)next[box.dataset.delegate]=off[box.dataset.delegate]||'';settingsSaveField('Off',next)};
 const cancel=document.createElement('button');cancel.type='button';cancel.textContent='Cancel';cancel.onclick=settingsCloseEditor;
 slot.replaceChildren(...rows,save,cancel)}
// settingsRow builds one row: a line naming the label, the effective value
// and where it came from; a Change button beside it, never nested inside
// the line itself, so opening an editor never disturbs that line's own
// text; the row's own caption; and an empty editor slot, always present
// (task 2026-09-23, eleventh first-time visitor, requirement 1). editFn
// opens whichever editor this row needs into that slot (a path field's
// text editor, or the delegate checkboxes).
function settingsRow(line,caption,editFn){
 const row=document.createElement('div');row.className='record';
 const p=document.createElement('p');p.textContent=line;row.append(p);
 const slot=document.createElement('div');
 const change=document.createElement('button');change.type='button';change.textContent='Change';change.onclick=()=>editFn(slot);row.append(change);
 const cap=document.createElement('p');cap.className='oa-note';cap.textContent=caption;row.append(cap);
 row.append(slot);
 return row
}
function renderSettingsRows(){
 settingsOpenSlot=null;
 const out=el('settings-rows');out.replaceChildren();
 for(const[field,label,caption]of settingsFields)out.append(settingsRow(label+' · '+settingsRowValue(field)+' · '+settingsRowSource(field)+' ',caption,slot=>settingsPathEditor(field,slot)));
 out.append(settingsRow('Disabled download delegates · '+settingsRowValue('Off')+' · '+settingsRowSource('Off')+' ','A download delegate switched off here is never handed new work on this device until it is switched on again.',settingsDelegatesEditor))
}
async function settingsRefreshSnapshot(){const p=await call('/settings/observe?'+new URLSearchParams({cursor:''}));if(p.Outcome==='snapshot')settingsSnap=p.Snapshot}
// settingsSaveField sends the one changed field inside the full UserSettings
// document the service requires (task 2026-09-23, eleventh first-time
// visitor, requirement: "Save writes only the rows the person changed"; the
// wire document itself still carries every field, since a partial write is
// not an outcome this service has). The confirmation names the one row that
// changed, or says plainly that nothing did.
async function settingsSaveField(field,newValue){
 const values={...(settingsValues||{})};const before=field==='Off'?JSON.stringify(values.Off||{}):(values[field]||'');
 values[field]=newValue;
 try{
  const r=await call('/settings',{Values:values,Revision:settingsRevision});
  if(r.Outcome==='applied'){
   settingsValues=(r.Snapshot&&r.Snapshot.Values)||values;settingsRevision=(r.Snapshot&&r.Snapshot.Revision)||'';
   await settingsRefreshSnapshot();settingsCloseEditor();renderSettingsRows();
   const after=field==='Off'?JSON.stringify(values.Off||{}):(values[field]||'');
   const label=field==='Off'?'Disabled download delegates':(settingsFields.find(f=>f[0]===field)||['',field])[1];
   show('settings-status',after===before?'Saved; nothing changed.':'Saved: '+label+'.');
   return
  }
  show('settings-status',r.Outcome==='conflict'?'Settings changed elsewhere. Reloading the current values.':'Settings service outcome: '+r.Outcome);
  if(r.Outcome==='conflict')await settingsLoad()
 }catch(e){fail('settings-status',e)}
}
async function settingsLoad(){try{const v=await call('/settings');settingsValues=v.Values||{};settingsRevision=v.Revision||'';await settingsRefreshSnapshot();renderSettingsRows()}catch(e){fail('settings-status',e)}}
settingsLoad();
let settingsFollow=null;
const settingsObserveText={gap:'Observation continuity was lost. Start following again to read current settings.',unavailable:"That service isn't running on this account right now. Start the runtime, or check the service on the Status page.",incompatible:'This runtime does not support watching settings for changes.',forbidden:'You are not authorized to watch settings; nothing was changed.'};
function settingsObserveStatus(o){return settingsObserveText[o]||'Configuration observation: '+o}
function stopSettingsFollow(message){if(settingsFollow)settingsFollow.abort();settingsFollow=null;el('follow-settings').textContent='Watch settings';show('settings-observation-status',message||'Stopped.')}
// renderSettingsSnapshot replaces the raw JSON a "Watch settings" snapshot
// used to write straight into the page with the same plain rows the editor
// above already reads: a table of setting, value and the location it came from,
// muted, updated on every snapshot; the JSON moves behind Technical details
// (task 2026-09-23, sixth first-time visitor, finding 4).
function renderSettingsSnapshot(snap){
 const out=el('effective-settings');out.replaceChildren();
 const table=document.createElement('table');
 const thead=document.createElement('thead');const head=document.createElement('tr');
 for(const h of['Setting','Value','Source']){const th=document.createElement('th');th.textContent=h;head.append(th)}
 thead.append(head);
 const tbody=document.createElement('tbody');
 const origins=(snap&&snap.Origins)||{};
 for(const[field,label]of[...settingsFields.map(f=>[f[0],f[1]]),['Off','Disabled download delegates']]){
  const tr=document.createElement('tr');
  const name=document.createElement('td');name.textContent=label;
  const value=document.createElement('td');value.textContent=field==='Off'?settingsOffWords(snap&&snap.Off):settingsPathWords(snap&&snap[field]);
  const source=document.createElement('td');const small=document.createElement('small');small.className='oa-muted';small.textContent=settingsOriginText(origins[field]);small.title=settingsOriginTitle(origins[field]);source.append(small);
  tr.append(name,value,source);tbody.append(tr)
 }
 table.append(thead,tbody);const wrap=document.createElement('div');wrap.className='oa-table';wrap.append(table);out.append(wrap);
 const details=document.createElement('details');const summary=document.createElement('summary');summary.textContent='Technical details';const pre=document.createElement('pre');pre.textContent=JSON.stringify(snap,null,2);details.append(summary,pre);out.append(details)
}
el('follow-settings').onclick=async()=>{if(settingsFollow){stopSettingsFollow('Stopped.');return}const run=new AbortController();settingsFollow=run;el('follow-settings').textContent='Stop watching';show('settings-observation-status','Watching since '+oaLocalTime(new Date().toISOString())+'.');let cursor='';try{while(settingsFollow===run){const r=await call('/settings/observe?'+new URLSearchParams({cursor}),undefined,run.signal);if(settingsFollow!==run)return;if(r.Outcome==='snapshot'){cursor=r.Cursor;renderSettingsSnapshot(r.Snapshot)}else if(r.Outcome==='unchanged'){cursor=r.Cursor}else{stopSettingsFollow(settingsObserveStatus(r.Outcome));return}}}catch(e){if(settingsFollow===run){settingsFollow=null;el('follow-settings').textContent='Watch settings';
  // A call that throws never reached a typed reply, so settingsObserveText
  // above never ran. A native install watches through the config module's
  // own directory notification; a state-dir-scoped runtime (every
  // --isolated runtime, or an explicit --state-dir) watches through the
  // file-polling source serve/runtime.go wires in. A runtime with neither
  // refuses this request as "incompatible", and this button states that
  // plainly instead of falling through to the generic "a version this
  // Panel doesn't understand" wording.
  const msg=(e&&e.message)||String(e);
  if(/incompatible/i.test(msg))show('settings-observation-status',"This runtime doesn't offer settings watching.");
  else fail('settings-observation-status',e)
 }}};
</script>`
