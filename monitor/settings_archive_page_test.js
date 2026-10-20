// Run with node monitor/settings_archive_page_test.js; no browser or service is started.
// Settings' Download archive row (settings_page.go, setup/download_archive.go
// and the abstraction-download archive package): the stored setting and listing in words on
// arrival with the path on the title, the reachability probe after it, the
// folder chooser, a typed folder where no chooser exists, Look again, Keep a
// copy of new downloads here, Stop using, and one status per action.
const fs=require('fs'),vm=require('./panel_record_test_helper.js').vm,assert=require('assert');
const go=fs.readFileSync(__dirname+'/settings_page.go','utf8');
{
 const markup=go.slice(go.indexOf('var settingsPage = '),go.indexOf('<script>'));
 assert(markup.indexOf('settings-archive')<markup.indexOf('oaExpander("Locations"'),'the archive row sits outside the expanders, shown on arrival');
}
function commonScript(){const src=fs.readFileSync(__dirname+'/service_page.go','utf8');const m='const serviceCommonScript = `';const s=src.indexOf(m)+m.length;return src.slice(s,src.indexOf('`',s))}
function plainScript(){const src=fs.readFileSync(__dirname+'/panel_shell.go','utf8');const m='const plainScript = `';const s=src.indexOf(m)+m.length;return src.slice(s,src.indexOf('`',s)).split('<script>')[1].split('</script>')[0]}
const script=go.split('<script>` + serviceCommonScript + `')[1].split('` + identityScript')[0];
function node(){return {value:'',textContent:'',className:'',title:'',hidden:false,disabled:false,checked:false,children:[],replaceChildren(...v){this.children=[...v]},append(...v){this.children.push(...v)},setAttribute(k,v){(this.attrs=this.attrs||{})[k]=v},removeAttribute(){}}}
const isNetwork=f=>f.startsWith('\\\\');
// contextFor serves the stored state at once and holds each probe until the
// test releases it with the folder's reachability. A listing runs until the
// row's first timed reread after a new folder or Look again, then reports
// listing().
function contextFor(stored,reachable,picker,listing){
 const fields=new Map(),created=[],posts=[],probes=[],timers=[];let looking=!!stored.looking,due=false;
 function el(id){if(!fields.has(id))fields.set(id,node());return fields.get(id)}
 const settings={Revision:'r1',Values:{Off:{}}};
 const state=()=>({keep:true,found:0,other:0,incomplete:false,...stored,network:!!stored.folder&&isNetwork(stored.folder),looking});
 const reread=()=>{if(looking){looking=false;Object.assign(stored,listing())}else if(stored.looking){stored.looking=false}return state()};
 const context={URLSearchParams,Date,setTimeout:f=>timers.push(()=>{due=true;f()}),location:{search:'?k=test'},document:{getElementById:id=>{const f=created.findLast(n=>n.id===id);return f||el(id)},createElement:t=>{const n=node();n.tagName=String(t).toUpperCase();created.push(n);return n}},fetch:async(path,options)=>{
  if(path.startsWith('/settings/observe'))return {ok:true,json:async()=>({Outcome:'unchanged'})};
  if(path==='/settings/archive?probe=1')return new Promise(resolve=>probes.push(()=>resolve({ok:true,json:async()=>({...state(),reachable})})));
  if(path==='/settings/archive'){
   if(!(options&&options.body)){const s=due&&looking?reread():state();due=false;return {ok:true,json:async()=>s}}
   const body=JSON.parse(options.body);posts.push(body);
   if('folder' in body){const changed=body.folder!==stored.folder;stored.folder=body.folder;if(!body.folder){delete stored.walked_at;stored.found=stored.other=0}else if(changed)looking=true}
   if(body.look)looking=true;
   if('keep' in body)stored.keep=body.keep;
   return {ok:true,json:async()=>state()}}
  if(path==='/settings/archive/folder')return {ok:true,json:async()=>picker};
  assert.equal(path,'/settings');return {ok:true,json:async()=>settings};
 }};
 vm.runInNewContext(plainScript(),context);
 vm.runInNewContext(commonScript()+script,context);
 const settle=async()=>{for(let i=0;i<20;i++)await new Promise(r=>setImmediate(r))};
 const release=async()=>{while(probes.length)probes.shift()();await settle()};
 // tick lets each waiting reread of a running listing happen.
 const tick=async()=>{await settle();while(timers.length){timers.shift()();await settle()}};
 // act runs a click through the listing it starts.
 const act=async click=>{const done=click();await tick();await done;await settle()};
 return {context,el,posts,probes,release,settle,tick,act};
}
const row=c=>c.el('settings-archive').children[0];
const caption=r=>r.children[0].children[1],status=r=>r.children[2];
const label=r=>r.children[0].children[0].textContent;
const noPathText=r=>assert(!/[\\/]/.test(caption(r).textContent),'the caption shows words, never the path: '+caption(r).textContent);
(async()=>{
 // Not set: the row says so on arrival and offers only the chooser.
 let c=contextFor({folder:''},true,{available:true,path:'\\\\nas.test\\archive'},()=>({walked_at:'2026-11-09T10:00:00Z',found:4,other:9}));
 await c.context.archiveReady;
 let r=row(c);
 assert.equal(label(r),'Download archive');
 assert.equal(caption(r).textContent,'Not set');
 assert.equal(c.probes.length,0,'nothing to probe without a folder');
 assert.equal(r.choose.textContent,'Choose folder…');
 assert.equal(r.look.textContent,'Look again');
 assert.equal(r.stop.textContent,'Stop using');
 assert.equal(label(r.keep),'Keep a copy of new downloads here');
 assert.equal(c.el('settings-archive').children[1],r.keep,'the switch row sits under the archive row');
 assert(r.look.hidden&&r.stop.hidden&&r.keep.hidden,'Look again, Stop using and the checkbox appear only with a folder');
 assert.equal(r.input.hidden,true,'no path field on arrival');
 // Choosing a folder saves it, lists it, and says what the listing found.
 const chosen=r.choose.onclick();
 await c.settle();
 assert.equal(caption(r).textContent,'Network folder · looking…','the caption follows the listing');
 assert.equal(status(r).textContent,'Downloads check this folder first and keep a copy there.');
 await c.tick();await chosen;await c.settle();
 assert.deepEqual(c.posts,[{folder:'\\\\nas.test\\archive'}],'the chosen folder is saved');
 assert.equal(caption(r).textContent,'Network folder · 4 models found');noPathText(r);
 assert.equal(caption(r).title,'\\\\nas.test\\archive','the path sits on the title');
 assert.equal(status(r).textContent,"Found 4 models Setup can use. 9 other model files here are not in the Catalog.");
 await c.release();
 assert.equal(caption(r).textContent,'Network folder · 4 models found','a reachable folder adds nothing');
 assert(!r.look.hidden&&!r.stop.hidden&&!r.keep.hidden&&r.keepBox.checked,'the folder brings its controls, the switch on');
 assert(caption(r.keep).hidden,'the switch says On or Off itself');
 // The switch turns the write-back off and on.
 r.keepBox.checked=false;await r.keepBox.onchange();
 assert.deepEqual(c.posts.at(-1),{keep:false});
 assert.equal(r.keepBox.checked,false);
 assert.equal(status(r.keep).textContent,'Downloads check this folder first.');
 r.keepBox.checked=true;await r.keepBox.onchange();
 assert.deepEqual(c.posts.at(-1),{keep:true});
 assert.equal(status(r.keep).textContent,'Downloads check this folder first and keep a copy there.');
 // Choosing the same folder changes nothing.
 await r.choose.onclick();await c.settle();
 assert.equal(status(r).textContent,'Nothing changed.');
 // Stop using.
 await r.stop.onclick();
 assert.deepEqual(c.posts.at(-1),{folder:''});
 assert.equal(caption(r).textContent,'Not set');
 assert.equal(status(r).textContent,'Downloads no longer use an archive.');

 // A stored listing shows on arrival before the probe; Look again finds
 // nothing, a single model, or a folder too large or silent.
 const listings=[{found:0,other:0},{found:1,other:1},{found:2,other:0,incomplete:true,stopped:'files'},{unanswered:true}];
 c=contextFor({folder:'\\\\nas.test\\archive',walked_at:'2026-11-09T09:00:00Z',found:3,other:0},false,{available:false},()=>({...listings.shift(),walked_at:'2026-11-09T10:00:00Z'}));
 await c.context.archiveReady;
 r=row(c);
 assert.equal(c.probes.length,1,'the probe follows arrival');
 assert.equal(caption(r).textContent,'Network folder · 3 models found','arrival waits for no probe');
 await c.release();
 assert.equal(caption(r).textContent,'Network folder · 3 models found · not reachable now');noPathText(r);
 assert.equal(caption(r).title,'\\\\nas.test\\archive');
 await c.act(r.look.onclick);
 assert.deepEqual(c.posts.at(-1),{look:true});
 assert.equal(caption(r).textContent,'Network folder · no models found');
 assert.equal(status(r).textContent,'Found no models Setup can use.');
 await c.act(r.look.onclick);
 assert.equal(caption(r).textContent,'Network folder · 1 model found');
 assert.equal(status(r).textContent,"Found 1 model Setup can use. 1 other model file here is not in the Catalog.");
 await c.act(r.look.onclick);
 assert.equal(status(r).textContent,'Stopped after 50,000 files. Choose the folder that holds the models.');
 await c.act(r.look.onclick);
 assert.equal(status(r).textContent,'The folder did not answer. Open it once in File Explorer, then Look again.');
 // Without a native chooser the row opens a prefilled field.
 await r.choose.onclick();
 assert.equal(r.input.hidden,false);assert.equal(r.input.value,'\\\\nas.test\\archive');
 r.input.value='  ';await r.save.onclick();
 assert(!c.posts.some(p=>p.folder==='  '||p.folder===''),'an empty folder is never sent');
 assert.match(status(r).textContent,/Enter a folder/);
 r.input.value='D:\\Archive';await c.act(r.save.onclick);
 assert.deepEqual(c.posts.filter(p=>'folder' in p),[{folder:'D:\\Archive'}]);
 assert.equal(r.input.hidden,true,'the field closes after saving');
 assert.match(caption(r).textContent,/^Folder on this PC/);noPathText(r);
 assert.equal(caption(r).title,'D:\\Archive');

 // A listing the Setup page started is followed from arrival without a sentence.
 c=contextFor({folder:'D:\\Models',looking:true},true,{available:true},()=>({}));
 await c.context.archiveReady;await c.settle();
 r=row(c);
 assert.equal(caption(r).textContent,'Folder on this PC · looking…','arrival shows a running listing');
 await c.tick();
 assert.equal(caption(r).textContent,'Folder on this PC','the listing ended with nothing stored');
 assert.equal(status(r).textContent,'','arrival writes no sentence');
 console.log('PASS: Download archive shows its setting and listing in words on arrival with the path on the title, probes reachability after, lists a chosen folder and on Look again, says what it found, keeps or stops keeping copies, and stops on request');
})().catch(e=>{console.error(e);process.exit(1)});
