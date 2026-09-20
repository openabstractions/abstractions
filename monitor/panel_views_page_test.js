// Run with node monitor/panel_views_page_test.js; no browser or service is started.
// Covers the Logging, Identity and Explore sections of service_page.go.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/service_page.go', 'utf8');
const script = go.split('<script>')[1].split('</script>')[0];
const fields = new Map();
function node() {
 return {value:'', textContent:'', className:'', disabled:false, children:[], replaceChildren(...v){this.children=[...v]}, append(...v){this.children.push(...v)}, click(){}};
}
function element(id) {
 if (!fields.has(id)) fields.set(id, node());
 return fields.get(id);
}
const text = n => [n.textContent, ...n.children.map(text)].join('\n');
const all = (n, out = []) => { out.push(n); n.children.forEach(c => all(c, out)); return out; };

const exe = 'C:\\panel\\monitor.exe';
const claimHop = {hop:0, by:'self', role:'writer claim', standing:'claimed', verified:false, program:'writer', uid:-1, gid:-1, pid:7};
const stampHop = {hop:1, by:'identity/windows', role:'writer', standing:'established', verified:true, exe:'C:\\apps\\writer.exe', user:'S-1-5-21-1', uid:-1, gid:-1, pid:7};
const sink = {adopted:true, state:'delivering', accepted:3, written:3, queued:0, dropped:0, failed:0, abandoned:0};
const pages = {
 read: [{mode:'read', outcome:'page', cursor:'', next:'c1', atEnd:false, hidden:1, sink, records:[
   {kind:'record', time:'2026-09-17T10:00:00.000000Z', level:0, levelName:'INFO', message:'verified record', program:'writer', hops:[claimHop, stampHop], author:true, disputed:false},
   {kind:'record', time:'2026-09-17T10:00:01.000000Z', level:0, levelName:'INFO', message:'bare record', hops:[{hop:0, by:'unclaimed', role:'no writer claim', standing:'claimed', verified:false, uid:-1, gid:-1, pid:-1}, stampHop], author:true, disputed:false},
   {kind:'record', time:'2026-09-17T10:00:02.000000Z', level:0, levelName:'INFO', message:'relayed record', program:'writer', hops:[claimHop, {hop:1, by:'so_peercred', role:'writer', standing:'asserted', verified:true, exe:'/usr/bin/writer', uid:1000, gid:1000, pid:42}, {hop:2, by:'identity/windows', role:'relay', standing:'established', verified:true, exe:'C:\\relay.exe', user:'S-1-5-21-1', uid:-1, gid:-1, pid:9}], author:false, disputed:false},
   {kind:'sink_gap', time:'2026-09-17T10:00:03.000000Z', level:4, levelName:'WARN', message:'records dropped while the sink was unreachable', program:'writer', dropped:'12', since:'2026-09-17T09:59:00.000000Z', until:'2026-09-17T10:00:03.000000Z', hops:[claimHop, stampHop], author:true, disputed:false}]}],
 follow: [
  {mode:'follow', outcome:'page', cursor:'c1', next:'c2', atEnd:true, hidden:0, sink, records:[{kind:'record', time:'2026-09-17T10:01:00.000000Z', level:8, levelName:'ERROR', message:'live record', program:'writer', hops:[claimHop, stampHop], author:true, disputed:false}]},
  {mode:'follow', outcome:'gap', cursor:'c2', next:'c2', atEnd:false, hidden:0, sink, records:[]},
  {mode:'follow', outcome:'page', cursor:'end', next:'c3', atEnd:true, hidden:0, sink, records:[]},
  {mode:'follow', outcome:'gap', cursor:'c3', next:'c3', atEnd:false, hidden:0, sink, records:[]}],
};
const identityGood = {
 selection:{status:'TRUSTED', endpoint:'\\\\.\\pipe\\rt', account:'S-1-5-21-1', program:'C:\\oa\\tools\\openabstractions.exe'},
 explicit:{endpoint:'\\\\.\\pipe\\iso', program:'C:\\oa\\openabstractions.exe', account:'S-1-5-21-1'},
 platform:'windows', declarations:[{id:'windows-user', name:'Windows x64, per-user installation', status:'supported', limits:[]}],
 ceiling:{platform:'windows', transport:'npipe', best:{user:'kernel', process:'kernel', path:'bound', package:'signed', code:'bound'}, why:{}, bindable:true, binding:'a process handle'},
 runtime:{absent:false, outcome:'observed', mechanism:'identity/windows', account:'S-1-5-21-1', program:exe, pid:4242, transport:'npipe', platform:'windows', bindable:true,
  attributes:[{attribute:'user', proof:'kernel', ceiling:'kernel'}, {attribute:'process', proof:'kernel', ceiling:'kernel'}, {attribute:'path', proof:'bound', ceiling:'bound'}, {attribute:'package', proof:'none', ceiling:'signed'}, {attribute:'code', proof:'unsigned', ceiling:'bound'}]},
 capabilities:[{name:'Logging history', contract:'abstraction.logging/reader@1', status:'resolved', label:'Ready'}, {name:'Rights administration', contract:'abstraction.rights/operator@1', status:'forbidden', label:'Access denied'}],
 logging:{absent:false, outcome:'found', claim:{...claimHop, program:'Abstraction Panel'}, stamp:{...stampHop, exe}, agreesWithRuntime:true},
 limits:['A program rule is only as strong as the path proof.'], checkedAt:'2026-09-17T10:02:00Z'};
const identityAbsent = {
 selection:{status:'UNTRUSTED', detail:'select installed runtime: bootstrap: no trusted runtime installation'},
 platform:'darwin', declarations:[{id:'macos', name:'macOS (universal package)', status:'unsupported', limits:['Selection has no darwin path.']}],
 ceiling:{platform:'darwin', transport:'unix', best:{user:'kernel', process:'pid', path:'pid', package:'pid', code:'pid'}, why:{}, bindable:false, binding:'no binding', stronger:'XPC'},
 runtime:{absent:true, error:'service resolution: runtime_unavailable: abstraction.facade/caller@1', pid:-1, attributes:[]},
 capabilities:[], capabilityError:'runtime_unavailable',
 logging:{absent:true, outcome:'error', error:'service resolution: runtime_unavailable: abstraction.logging/reader@1'},
 limits:['On macOS, protected calls are refused: a socket peer cannot reach the Program proof.'], checkedAt:'2026-09-17T10:03:00Z'};
let identityReply = identityGood, loggingAbsent = false;
const editAction = 'abstraction.config/user.replace', editResource = 'abstraction.config/editor@1';
const exploreCatalogue = {self:{program:exe, account:'S-1-5-21-1'}, probes:[
 {capability:'config', operation:'rewrite', contract:'abstraction.config/editor@1', writes:true, note:'WRITES: replaces your user settings with their current values at the read revision', rule:{action:editAction, resource:editResource}},
 {capability:'model', operation:'resolve', contract:'abstraction.model/resolver@1', argument:'REGISTRY:REPO[@REVISION]', rule:{action:'abstraction.model/lookup', resource:'<registry>'}}]};
const probeReply = (outcome, rule) => ({result:{capability:'config', operation:'rewrite', contract:'abstraction.config/editor@1', subject:{program:exe, account:'S-1-5-21-1'}, outcome, rule:{action:editAction, resource:editResource}}, rule});
const unknownRule = {subject:{program:exe, account:'S-1-5-21-1'}, action:editAction, resource:editResource, outcome:'unknown', revision:'rev-1'};
const foundRule = {...unknownRule, outcome:'found', revision:'rev-2', permit:true, setBy:{program:'C:\\oa\\openabstractions.exe', account:'S-1-5-21-1'}, setAt:'2026-09-17T10:05:00.000Z'};
const exploreReplies = [probeReply('forbidden', unknownRule), probeReply('applied', foundRule), probeReply('forbidden', unknownRule), probeReply('forbidden', {...unknownRule, outcome:'forbidden', revision:''})];
const exploreCalls = [], rightsPosts = [], confirmations = [];
let confirmAnswer = true;
const requests = [];
const context = {URLSearchParams, localStorage:{length:0, key(){}, getItem(){return null}, setItem(){}}, document:{getElementById:element, createElement:node}, location:{search:'?k=test'}, console, confirm:(text)=>{confirmations.push(text);return confirmAnswer}, Blob, URL:{createObjectURL(){}, revokeObjectURL(){}},
 fetch:async(path, opts)=>{
  requests.push({path, key:opts.headers['X-Panel-Key']});
  if(path.startsWith('/logging')){
   if(loggingAbsent) return {ok:false, text:async()=>'The runtime is absent: service resolution: runtime_unavailable'};
   const q = new URLSearchParams(path.split('?')[1]);
   const reply = (q.get('follow')==='1' ? pages.follow : pages.read).shift();
   assert(reply, 'unexpected logging request '+path);
   return {ok:true, json:async()=>reply};
  }
  if(path==='/identity') return {ok:true, json:async()=>identityReply};
  if(path==='/explore') return {ok:true, json:async()=>exploreCatalogue};
  if(path.startsWith('/explore?')){exploreCalls.push(new URLSearchParams(path.split('?')[1]));const reply=exploreReplies.shift();assert(reply,'unexpected explore call '+path);return {ok:true, json:async()=>reply}}
  if(path==='/rights?cursor=') return {ok:true, json:async()=>({Outcome:'page', Revision:'rev-'+(rightsPosts.length+1), Catalog:[], Rules:[], Next:'', Complete:true})};
  if(path==='/rights'){const e=JSON.parse(opts.body);rightsPosts.push(e);return {ok:true, json:async()=>({Outcome:'applied', Revision:'rev-next'})}}
  throw Error('unexpected request '+path);
 }};
vm.runInNewContext(script, context);
(async()=>{
 element('log-program').value='writer'; element('log-level').value='0';
 await element('log-start').onclick();
 const first = new URLSearchParams(requests[0].path.split('?')[1]);
 assert.equal(requests[0].key, 'test', 'panel key sent');
 assert.equal(first.get('cursor'), '', 'read from the start sends an empty cursor');
 assert.equal(first.get('program'), 'writer'); assert.equal(first.get('level'), '0');
 const boxes = element('log-records').children;
 assert.equal(boxes.length, 4);
 const [verified, bare, relayed, sinkGap] = boxes;
 const hops = box => box.children[1].children;
 assert.equal(verified.className, 'record');
 assert.equal(hops(verified)[0].className, 'hop claim', 'a self claim is styled as a claim');
 assert.equal(hops(verified)[1].className, 'hop verified', 'a service stamp is styled as verified');
 assert.notEqual(hops(verified)[0].className, hops(verified)[1].className, 'verified and claimed hops look different');
 assert(hops(verified)[0].textContent.includes("writer's own claim, unverified"));
 assert(hops(verified)[1].textContent.includes('verified by the logging service'));
 assert(hops(bare)[0].textContent.includes('the writer sent no claim'), 'unclaimed hop 0 says so');
 assert.equal(hops(relayed)[1].className, 'hop asserted', 'a relayed stamp nobody vouched for is untrusted');
 assert(hops(relayed)[2].textContent.startsWith('hop 2, relay'), 'relay hop is named');
 assert.equal(sinkGap.className, 'gap', 'a sink-loss gap record is shown as a gap');
 assert(sinkGap.children[0].textContent.includes('12 records') && sinkGap.children[0].textContent.includes('dropped'), 'gap names the dropped count');
 assert(element('log-status').textContent.includes('More history remains') && element('log-status').textContent.includes('1 records on this page hidden'));
 assert.equal(element('log-next').disabled, false);
 assert(element('log-sink').textContent.includes('delivering. 3 accepted, 3 written'), 'panel sink state shown');

 await element('log-follow').onclick();
 const followed = requests.slice(1).map(r => new URLSearchParams(r.path.split('?')[1]));
 assert.equal(followed.length, 2, 'following continues until the gap');
 assert.equal(followed[0].get('follow'), '1'); assert.equal(followed[0].get('cursor'), 'c1', 'follow continues from the read cursor');
 assert.equal(followed[1].get('cursor'), 'c2');
 const list = element('log-records').children;
 assert.equal(list[4].children[0].textContent.includes('ERROR writer: live record'), true, 'live record appended');
 const observerGap = list[5];
 assert.equal(observerGap.className, 'gap', 'an observer gap is shown as a gap');
 assert(observerGap.textContent.includes('History gap'));
 assert.equal(element('log-follow').textContent, 'Follow live', 'following stops at a gap');
 assert(element('log-status').textContent.includes('Read from the start'), 'the gap asks for an explicit restart');

 const before = requests.length;
 await element('log-follow').onclick();
 const fromEnd = requests.slice(before).map(r => new URLSearchParams(r.path.split('?')[1]));
 assert.equal(fromEnd.length, 2, 'following from the end continues until the next gap');
 assert.equal(fromEnd[0].get('cursor'), 'end', 'following with no read position starts at the history end');
 assert.equal(fromEnd[1].get('cursor'), 'c3', 'following continues from the end continuation');

 loggingAbsent = true;
 await element('log-start').onclick();
 assert(element('log-status').textContent.startsWith('The runtime is absent'), 'absent runtime reported in the logging section');

 await element('identity-check').onclick();
 const shown = text(element('identity'));
 assert(shown.includes('Installed runtime selection: TRUSTED. Program C:\\oa\\tools\\openabstractions.exe'));
 assert(shown.includes('started against another runtime at \\\\.\\pipe\\iso'));
 assert(shown.includes('Platform declaration for Windows x64, per-user installation: supported.'));
 assert(shown.includes('Account S-1-5-21-1, program '+exe+', pid 4242, established by identity/windows over npipe.'));
 assert(shown.includes('path: proven bound; the ceiling on this transport is bound.'));
 assert(shown.includes('Rights administration (abstraction.rights/operator@1): Access denied for this Panel.'));
 const paragraphs = all(element('identity'));
 assert(paragraphs.some(p => p.className==='claim' && p.textContent.includes('claiming to be Abstraction Panel')), 'the panel claim is a claim');
 assert(paragraphs.some(p => p.className==='verified' && p.textContent.includes('The logging service stamped it: executable '+exe)), 'the logging stamp is verified');
 assert(shown.includes('bound the same program, account and process'));
 assert(shown.includes('A program rule is only as strong as the path proof.'));
 assert(element('identity-status').textContent.includes('Checked at'));

 identityReply = identityAbsent;
 await element('identity-check').onclick();
 const absent = text(element('identity'));
 assert(absent.includes('Installed runtime selection: UNTRUSTED. select installed runtime'));
 assert(absent.includes('The runtime is absent: service resolution: runtime_unavailable: abstraction.facade/caller@1'), 'identity section reports the absent runtime');
 assert(absent.includes('The runtime is absent: service resolution: runtime_unavailable: abstraction.logging/reader@1'), 'logging view reports the absent runtime');
 assert(absent.includes('Platform declaration for macOS (universal package): unsupported.'));
 assert(absent.includes('On macOS, protected calls are refused'), 'macOS limit stated plainly');
 assert(absent.includes('No binding pins the caller'));
 assert(!absent.includes('stamped it'), 'no stamp without a runtime');
 await element('explore-load').onclick();
 assert.equal(element('explore-program').value, exe, 'subject picker defaults to this Panel');
 assert.equal(element('explore-account').value, 'S-1-5-21-1');
 const [rewriteBox, modelBox] = element('explore').children;
 assert(rewriteBox.children[1].textContent.includes('decided by '+editAction+' on '+editResource), 'probe names its rule');
 assert.equal(rewriteBox.grant.disabled, true, 'grant waits for a call');
 assert.equal(rewriteBox.children[0].textContent, 'config rewrite (writes)', 'a probe that writes is labelled');
 confirmAnswer = false;
 await rewriteBox.children.find(c=>c.textContent==='Call').onclick();
 assert.equal(exploreCalls.length, 0, 'a declined write is not sent');
 assert(confirmations[0].startsWith('config rewrite: WRITES: replaces your user settings'), 'the confirmation says what it writes');
 confirmAnswer = true;
 await rewriteBox.children.find(c=>c.textContent==='Call').onclick();
 assert.equal(exploreCalls[0].get('capability'), 'config'); assert.equal(exploreCalls[0].get('program'), exe);
 assert.equal(exploreCalls[0].get('confirm'), 'write', 'a confirmed write says so to the Panel');
 assert(rewriteBox.result.textContent.includes(': forbidden'), 'refusal shown');
 assert(rewriteBox.rule.textContent.includes('No exact rule for S-1-5-21-1 running '+exe), 'missing rule shown beside the refusal');
 assert.equal(rewriteBox.grant.disabled, false);
 await rewriteBox.grant.onclick();
 assert.deepEqual(rightsPosts[0], {edit:'set', revision:'rev-1', account:'S-1-5-21-1', program:exe, action:editAction, resource:editResource, permit:true}, 'grant is the exact rule at the listed revision');
 assert(element('explore-status').textContent.startsWith('Granted '+editAction), 'grant reported');
 assert(rewriteBox.result.textContent.includes(': applied'), 'the call ran again and was allowed');
 assert(rewriteBox.rule.textContent.includes('permit '+editAction) && rewriteBox.rule.textContent.includes('set by C:\\oa\\openabstractions.exe'), 'the deciding rule is shown');
 await rewriteBox.revoke.onclick();
 assert.deepEqual(rightsPosts[1], {edit:'revoke', revision:'rev-2', account:'S-1-5-21-1', program:exe, action:editAction, resource:editResource}, 'revoke names the exact rule');
 assert(rewriteBox.result.textContent.includes(': forbidden'), 'the call ran again and was refused');
 await rewriteBox.children.find(c=>c.textContent==='Call').onclick();
 assert(rewriteBox.rule.textContent.includes('not a rights operator'), 'operator refusal explained');
 const callsBefore = exploreCalls.length;
 await modelBox.children.find(c=>c.textContent==='Call').onclick();
 assert.equal(exploreCalls.length, callsBefore, 'a probe needing an argument is not sent without one');
 assert(element('explore-status').textContent.includes('Name REGISTRY:REPO[@REVISION]'));
 console.log('PASS: explore write probe labelled and confirmed, grant, call, revoke with exact rules at the listed revision, deciding rule beside each result, operator refusal, argument required');
 console.log('PASS: logging hops styled verified, claimed and untrusted; unclaimed and relay hops named; sink-loss and observer gaps shown as gaps; follow stops at a gap; panel sink state; identity selection, runtime caller rungs, logging stamp, refusals and absent runtime');
})().catch(e=>{console.error(e);process.exitCode=1});
