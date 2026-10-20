// Run with node monitor/config_page_test.js; no browser or service is started.
// Settings (settings_page.go, research/panel-redesign/SPEC.md §4, S9):
// About on arrival, Locations and Downloads behind expanders, choosers
// instead of typed paths, and one sentence in a row's status per save.
const fs=require('fs'),vm=require('vm'),assert=require('assert');
const go=fs.readFileSync(__dirname+'/settings_page.go','utf8');
{
 const markup=go.slice(go.indexOf('var settingsPage = '),go.indexOf('<script>'));
 assert(markup.includes('oaPageHead("settings", "Settings", "Locations and information.")'),'the page caption');
 assert(markup.indexOf('identitySection')<markup.indexOf('oaExpander("Locations"'),'About comes first, shown on arrival');
 for(const name of ['Locations','Downloads','Developer tools'])assert(markup.includes('oaExpander("'+name+'"'),name+' sits behind an expander');
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
 assert.deepEqual(rows('settings-rows').map(label),['Log file','Logging service'],'the deprecated job folders are not shown');
 const logFile=rows('settings-rows')[0],logService=rows('settings-rows')[1];
 assert.equal(caption(logFile).textContent,'Default location · default');
 assert.equal(caption(logService).textContent,'Custom location · for this device','a set value reads in words');
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
 assert.equal(caption(savedRow).textContent,'Custom location · for you');
 // Saving the unchanged service says so.
 settingsReply={Outcome:'applied',Snapshot:{Revision:'r3',Values:{...getReply.Values,LogSink:'/logs/oa.log'}}};
 await control(rows('settings-rows')[1])[2].onclick();
 assert.equal(status(rows('settings-rows')[1]).textContent,'Saved; nothing changed.');
 // Downloads: one switch per delegate, on unless switched off.
 assert.deepEqual(rows('settings-downloads').map(label),["NAS (another machine's runtime)",'Windows Background Transfer (BITS)']);
 const bits=rows('settings-downloads')[1];
 assert.equal(control(rows('settings-downloads')[0])[0].checked,false,'the delegate already off reads Off');
 assert.equal(control(bits)[0].checked,true);
 settingsReply={Outcome:'applied',Snapshot:{Revision:'r4',Values:{...getReply.Values,LogSink:'/logs/oa.log',Off:{nas:'no internet here',bits:''}}}};
 snapshot={...snapshot,Off:{nas:'no internet here',bits:''}};
 control(bits)[0].checked=false;await control(bits)[0].onchange();await tick();
 assert.deepEqual(writes.at(-1).Values.Off,{nas:'no internet here',bits:''},'switching one off keeps the other\'s reason');
 assert.equal(status(rows('settings-downloads')[1]).textContent,'Windows Background Transfer (BITS) is off.');
 // A conflict reloads and says so in the row.
 settingsReply={Outcome:'conflict'};
 await control(rows('settings-rows')[0])[2].onclick();
 assert.match(status(rows('settings-rows')[0]).textContent,/changed elsewhere/);
 // The rendered rows keep the Panel rules (static/panel_rules.js).
 const { assertPanelRules } = require('./static/panel_rules.js');
 assertPanelRules([el('settings-rows'), el('settings-downloads')], 'Settings', { rules: ['raw-value', 'empty-input', 'button-status', 'class'], allowClasses: require('./panel_fake_dom_test_helper.js').allowlistedClasses('settings') });
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
 assert.equal(caption(svc).textContent,'Custom location · for you','the row updated in place');
 console.log('PASS: Settings shows locations in words with the path on the title, saves through choosers with one sentence per row, switches download delegates, and reloads on a conflict');
})().catch(e=>{console.error(e);process.exitCode=1});
