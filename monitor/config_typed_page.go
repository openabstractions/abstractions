package main

import (
	"encoding/json"
	wire "github.com/openabstractions/abstraction-config/go/abstraction/config"
)

// One renderer consumes the definition-owned catalogue. Every change is one
// compared user-key edit; effective values and locks remain service-owned.
// While the runtime-down banner shows, Preferences is hidden whole
// (data-runtime-echo): the runtime owns the settings (R19b render check).
var typedSettingsSection = `<section data-runtime-echo class="oa-group"><h2>Preferences</h2><div id="typed-settings-status" role="status">Reading settings…</div><div id="typed-settings-rows"></div></section>`

// typedAdvancedStarts shows the agent-start settings read-only in Settings'
// Advanced section. The runtime reads them when a message would start a
// closed agent (serve/runtime_config_typed.go, serve/messages_start.go);
// whether the Panel edits them is a later architect question.
var typedAdvancedStarts = `<div id="typed-advanced-starts" hidden>` + oaExpander("Agent starts", `<div id="typed-advanced-rows"></div>`) + `</div>`

// panelSettingLabels names each typed setting in the Panel (architect ruling
// 2026-11-09): the Settings rows and a saved permission's config-key caption
// read the same words. Any other key reads its catalogue description without
// a final period.
var panelSettingLabels = map[string]string{
	"cache_max_use":               "Cache size limit",
	"cache_max_retention":         "Keep cache for",
	"tmp_max_retention":           "Keep temporary data for",
	"logs_max_use":                "Log size limit",
	"logs_max_retention":          "Keep logs for",
	"messages_start_questions":    "Start closed agents for questions",
	"messages_start_notes":        "Start closed agents for notes",
	"messages_start_per_hour":     "Start limit for each agent",
	"messages_start_open_app":     "Open the agent app when it cannot start",
	"nas_store":                   "Network job folder",
	"disabled_download_delegates": "Switched-off download methods",
	"model_context":               "Context for each local model",
	"prompt_cache_persist":        "Keep saved prompt data",
	"prompt_cache_max_use":        "Saved prompt data size limit",
	"prompt_cache_max_retention":  "Keep saved prompt data for",
	"prompt_cache_off_for":        "Saved prompt data off for",
}

// Location words come from the published codec; the Panel owns their captions.
var typedSettingsScript = func() string {
	captions, _ := json.Marshal(map[string]string{wire.LocationMachine.String(): "For this device", wire.LocationUser.String(): "For you", wire.LocationProcess.String(): "For this run"})
	labels, _ := json.Marshal(panelSettingLabels)
	return "const typedLocationCaptions=" + string(captions) + ";\nconst typedLabels=" + string(labels) + ";\n" + typedSettingsBody
}()

const typedSettingsBody = `
let typedDescriptors=[],typedEntries=[],typedUserEntries=[],typedRevision='',typedWritable=false,typedCursor='',typedRun=null,typedClosed=false;
// typedLabels (panelSettingLabels) names each setting; any other key reads
// its catalogue description without a final period.
function typedLabel(d){return typedLabels[d.key]||String(d.description||d.key).replace(/[.]$/,'')}
// Sizes read in binary units with the Windows names ("4 GB"); durations in
// the largest whole unit ("10 days").
const typedBytesText=oaBytes;
function typedDurationText(ms){if(ms===0)return '0 seconds';for(const[unit,size]of[['day',86400000],['hour',3600000],['minute',60000],['second',1000]])if(ms>=size&&ms%size===0){const v=ms/size;return v+' '+unit+(v===1?'':'s')}if(ms<1000)return ms+' ms';const sec=Math.round(ms/1000);return sec+' seconds'}
function typedValueText(v){if(!v)return 'Default';switch(v.kind){case 'boolean':return v.boolean?'On':'Off';case 'bytes':return Number.isSafeInteger(v.bytes||0)?typedBytesText(v.bytes||0):'Custom amount';case 'duration':return Number.isSafeInteger(v.duration_ms||0)?typedDurationText(v.duration_ms||0):'Custom duration';case 'choice':return v.choice;case 'unlimited':return 'No limit';case 'text':return v.text||'Not set';default:return 'Managed automatically'}}
// typedValueWords reads a setting whose bare value needs its unit: an agent
// start limit of "1" reads "1 start an hour".
const typedValueWords={messages_start_per_hour:v=>v&&v.kind==='choice'&&/^[0-9]+$/.test(v.choice)?v.choice+(v.choice==='1'?' start':' starts')+' an hour':''};
function typedKeyValueText(key,v){return (typedValueWords[key]&&typedValueWords[key](v))||typedValueText(v)}
// The default choice names the catalogue default when it has one.
function typedDefaultText(d){return d.default?'Default ('+typedValueText(d.default)+')':'Default'}
// A caption appears only when it adds a fact: where a set value lives, a
// lock, a pending restart, or what the default means.
function typedCaption(d,e){const o=(e&&e.origin)||{};if(o.state==='locked')return 'Locked by your administrator';if(!e)return 'Unavailable';if(o.state==='pending')return 'Takes effect after restart';if(o.state==='ignored')return 'Ignored: '+(o.reason||'invalid value');return Object.prototype.hasOwnProperty.call(typedLocationCaptions,o.location)?typedLocationCaptions[o.location]:d.default_description||''}
// The chooser shows the set value when any location set one (the person,
// the device, this run or an administrator's lock), and Default otherwise.
function typedChosen(e){const o=(e&&e.origin)||{};return e&&e.value&&(o.state==='locked'||(!!o.location&&o.location!=='default'))?e.value:null}
function typedRender(){
 const rows=el('typed-settings-rows');rows.replaceChildren();const advanced=el('typed-advanced-rows');advanced.replaceChildren();
 for(const d of typedDescriptors){
  // OA658: persistent conversation memory has no accepted production owner.
  // Its dormant catalogue entries are not offered as working settings.
  if(d.key.startsWith('prompt_cache_'))continue;
  // The Models page owns each local model's Context chooser.
  if(d.key==='model_context')continue;
  // Locations and downloads owns the download keys.
  if(d.group==='downloads')continue;
  const e=typedEntries.find(e=>e.key===d.key),v=e&&e.value||d.default,label=typedLabel(d),caption=typedCaption(d,e);
  // Agent starts show their value read-only under Advanced.
  if(d.group==='messages'){const value=document.createElement('span');value.textContent=typedKeyValueText(d.key,v);const row=settingsRowShell(label,caption,'',[value],'typed-status-'+d.key);row.children[0].children[1].hidden=!caption;advanced.append(row);continue}
  const row=settingsRowShell(label,caption,'',[],'typed-status-'+d.key),status=row.status,controls=row.children[1];row.children[0].children[1].hidden=!caption;
  const locked=e&&e.origin&&e.origin.state==='locked',available=typedWritable&&!!e;
  if(d.kind==='boolean'){
   const control=document.createElement('input');control.type='checkbox';control.className='switch';control.checked=!!(v&&v.boolean);control.disabled=locked||!available;control.setAttribute('aria-label',label);
   control.onchange=()=>typedSave(d.key,{kind:'boolean',boolean:control.checked},status,control);controls.append(control);
  }else if(['choice','bytes','duration'].includes(d.kind)){
   const control=document.createElement('select');control.setAttribute('aria-label',label);control.disabled=locked||!available;
   const values=[null,...(d.presets||[])];if(d.kind==='choice')for(const choice of d.choices||[])if(!values.some(v=>v&&v.choice===choice))values.push({kind:'choice',choice:choice});
   if(d.allows_unlimited&&!values.some(v=>v&&v.kind==='unlimited'))values.push({kind:'unlimited'});
   if(v&&!values.some(x=>JSON.stringify(x)===JSON.stringify(v)))values.push(v);
   values.forEach((value,index)=>{const option=document.createElement('option');option.value=String(index);option.textContent=value?typedValueText(value):typedDefaultText(d);option.disabled=!!value&&(!Number.isSafeInteger(value.bytes||0)||!Number.isSafeInteger(value.duration_ms||0));control.append(option)});
   const chosen=typedChosen(e);control.value=String(Math.max(0,values.findIndex(x=>JSON.stringify(x)===JSON.stringify(chosen))));
   control.onchange=()=>typedSave(d.key,values[Number(control.value)],status,control);controls.append(control);
  }else{const value=document.createElement('span');value.textContent=typedValueText(v);controls.append(value)}
  if(available&&!locked&&['boolean','choice','bytes','duration'].includes(d.kind)&&typedUserEntries.some(entry=>entry.key===d.key&&entry.value)){
   const reset=document.createElement('button');reset.textContent='Use default';reset.onclick=()=>typedSave(d.key,null,status,reset);controls.append(reset);
  }
  rows.append(row);
 }
 el('typed-advanced-starts').hidden=!advanced.children.length;
}
async function typedLoad(signal){
 try{
  const [catalogue,read,user]=await Promise.all(['describe','read','user'].map(action=>call('/settings/typed?action='+action,null,signal)));
  if(signal&&signal.aborted)return false;
  if(catalogue.outcome!=='read'||read.outcome!=='read'||user.outcome!=='read'||!user.snapshot)throw Error('Settings are unavailable.');
  typedDescriptors=catalogue.descriptors||[];typedEntries=read.entries||[];typedUserEntries=user.snapshot.entries||[];typedRevision=user.snapshot.revision;typedWritable=true;typedRender();el('typed-settings-status').textContent='';return true;
 }catch(error){if(!signal||!signal.aborted){typedWritable=false;typedRender();el('typed-settings-status').textContent='Settings could not be read. Check again.'}return false}
}
async function typedSave(key,value,status,control){
 control.disabled=true;status.textContent='Saving…';
 try{const change={key:key};if(value!==null)change.set=value;const saved=await call('/settings/typed',{revision:typedRevision,changes:[change]});
  if(saved.outcome==='applied'){await typedLoad();el('typed-settings-status').textContent='Saved.'}
  else if(saved.outcome==='conflict'){await typedLoad();el('typed-settings-status').textContent='Settings changed elsewhere. Review the current value.'}
  else{await typedLoad();el('typed-settings-status').textContent=saved.outcome==='not_permitted'?'This setting cannot be changed.':'The setting was not saved: '+saved.outcome+'.'}
 }catch(error){typedWritable=false;typedRender();el('typed-settings-status').textContent='The change could not be confirmed. Check again.'}
}
async function typedFollow(run){while(!run.signal.aborted){let retry=false;try{const page=await call('/settings/typed?action=observe&cursor='+encodeURIComponent(typedCursor),null,run.signal);if(run.signal.aborted)return;if(page.outcome==='gap'){typedCursor='';continue}if(page.outcome==='changed'){if(await typedLoad(run.signal))typedCursor=page.cursor;else retry=true}else if(page.outcome!=='unchanged')throw Error('Settings unavailable')}catch(error){if(run.signal.aborted)return;typedWritable=false;typedRender();oaRuntimeEcho('typed-settings-status',()=>{el('typed-settings-status').textContent='Settings could not be refreshed.'},error&&error.message);retry=true}if(retry)typedCursor='';if(retry)typedCursor='';if(retry)await new Promise(resolve=>{const timer=setTimeout(done,1000);function done(){clearTimeout(timer);run.signal.removeEventListener('abort',done);resolve()}run.signal.addEventListener('abort',done,{once:true})})}}
function typedStop(){if(typedRun){typedRun.abort();typedRun=null}}
function typedStart(){if(typedClosed||typedRun||document.visibilityState==='hidden'||typeof AbortController==='undefined')return;const run=new AbortController();typedRun=run;typedFollow(run).finally(()=>{if(typedRun===run)typedRun=null})}
var typedSettingsReady=typedLoad();typedSettingsReady.then(typedStart);
if(typeof window!=='undefined'&&typeof window.addEventListener==='function'){window.addEventListener('pagehide',()=>{typedClosed=true;typedStop()});window.addEventListener('pageshow',()=>{typedClosed=false;typedStart()})}
if(typeof document.addEventListener==='function')document.addEventListener('visibilitychange',()=>{if(document.visibilityState==='hidden')typedStop();else typedStart()});
`
