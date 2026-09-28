// Run with node monitor/panel_views_page_test.js; no browser or service is
// started. Covers the three pages split out of service_page.go that used to
// be its Logging, Identity and Explore sections: logging_page.go,
// identity_page.go and explore_page.go. loadPageScript splices
// serviceCommonScript back into a page's own script tag, where its source
// only carries the Go "` + serviceCommonScript + `" glue, the same way
// service_page_test.js does.
const fs = require('fs'), vm = require('vm'), assert = require('assert');

function commonScript() {
 const src = fs.readFileSync(__dirname + '/service_page.go', 'utf8');
 const marker = 'const serviceCommonScript = `';
 const start = src.indexOf(marker) + marker.length;
 assert(start > marker.length - 1, 'serviceCommonScript not found in service_page.go');
 return src.slice(start, src.indexOf('`', start));
}
function loadPageScript(file) {
 const go = fs.readFileSync(__dirname + '/' + file, 'utf8');
 const script = go.split('<script>')[1].split('</script>')[0];
 const glue = '` + serviceCommonScript + `';
 assert(script.includes(glue), file + ': serviceCommonScript glue not found');
 return script.replace(glue, commonScript());
}
// plainScript (monitor/panel_shell.go) is embedded ahead of every page
// fragment in the real document; explore_page.go's own script now calls
// oaActionFallback (task 2026-09-23, seventh first-time visitor, finding 2),
// so the Explore sub-test below loads it into the same vm context first, the
// same way monitor/card_page_test.js and its siblings already do.
function plainScript() {
 const shellGo = fs.readFileSync(__dirname + '/panel_shell.go', 'utf8');
 const marker = 'const plainScript = `';
 const start = shellGo.indexOf(marker) + marker.length;
 assert(start > marker.length - 1, 'plainScript constant not found in panel_shell.go');
 const end = shellGo.indexOf('`', start);
 return shellGo.slice(start, end).split('<script>')[1].split('</script>')[0];
}

function node() {
 return {value:'', textContent:'', className:'', title:'', disabled:false, hidden:false, checked:false, dataset:{}, listeners:{}, children:[], replaceChildren(...v){this.children=[...v]}, append(...v){this.children.push(...v)}, click(){}, addEventListener(type,fn){(this.listeners[type]=this.listeners[type]||[]).push(fn)}, dispatch(type){for(const fn of this.listeners[type]||[])fn()}};
}
function makeElement() {
 const fields = new Map();
 return id => { if (!fields.has(id)) fields.set(id, node()); return fields.get(id); };
}
const text = n => [n.textContent, ...n.children.map(text)].join('\n');
const all = (n, out = []) => { out.push(n); n.children.forEach(c => all(c, out)); return out; };

const exe = 'C:\\panel\\monitor.exe';

(async () => {
 // --- Logging page (logging_page.go). ---
 {
  const element = makeElement();
  const claimHop = {hop:0, by:'self', role:'writer claim', standing:'claimed', verified:false, program:'writer', uid:-1, gid:-1, pid:7};
  const stampHop = {hop:1, by:'identity/windows', role:'writer', standing:'established', verified:true, exe:'C:\\apps\\writer.exe', user:'S-1-5-21-1', uid:-1, gid:-1, pid:7};
  const sink = {adopted:true, state:'delivering', accepted:3, written:3, queued:0, dropped:0, failed:0, abandoned:0};
  const pages = {
   read: [{mode:'read', outcome:'page', cursor:'', next:'c1', atEnd:false, hidden:1, sink, records:[
     {kind:'record', time:'2026-09-17T10:00:00.000000Z', level:0, levelName:'INFO', message:'verified record', program:'C:\\apps\\writer.exe', hops:[claimHop, stampHop], author:true, disputed:false},
     {kind:'record', time:'2026-09-17T10:00:01.000000Z', level:0, levelName:'INFO', message:'bare record', hops:[{hop:0, by:'unclaimed', role:'no writer claim', standing:'claimed', verified:false, uid:-1, gid:-1, pid:-1}, stampHop], author:true, disputed:false},
     {kind:'record', time:'2026-09-17T10:00:02.000000Z', level:0, levelName:'INFO', message:'relayed record', program:'writer', hops:[claimHop, {hop:1, by:'so_peercred', role:'writer', standing:'asserted', verified:true, exe:'/usr/bin/writer', uid:1000, gid:1000, pid:42}, {hop:2, by:'identity/windows', role:'relay', standing:'established', verified:true, exe:'C:\\relay.exe', user:'S-1-5-21-1', uid:-1, gid:-1, pid:9}], author:false, disputed:false},
     {kind:'sink_gap', time:'2026-09-17T10:00:03.000000Z', level:4, levelName:'WARN', message:'records dropped while the sink was unreachable', program:'writer', dropped:'12', since:'2026-09-17T09:59:00.000000Z', until:'2026-09-17T10:00:03.000000Z', hops:[claimHop, stampHop], author:true, disputed:false}]}],
   follow: [
    {mode:'follow', outcome:'page', cursor:'c1', next:'c2', atEnd:true, hidden:0, sink, records:[{kind:'record', time:'2026-09-17T10:01:00.000000Z', level:8, levelName:'ERROR', message:'live record', program:'writer', hops:[claimHop, stampHop], author:true, disputed:false}]},
    {mode:'follow', outcome:'gap', cursor:'c2', next:'c2', atEnd:false, hidden:0, sink, records:[]},
    {mode:'follow', outcome:'page', cursor:'end', next:'c3', atEnd:true, hidden:0, sink, records:[]},
    {mode:'follow', outcome:'gap', cursor:'c3', next:'c3', atEnd:false, hidden:0, sink, records:[]}],
  };
  let loggingAbsent = false;
  const requests = [];
  const context = {URLSearchParams, localStorage:{length:0, key(){}, getItem(){return null}, setItem(){}}, document:{getElementById:element, createElement:node, createTextNode:text=>({textContent:text})}, location:{search:'?k=test'}, console,
   fetch:async(path, opts)=>{
    requests.push({path, key:opts.headers['X-Panel-Key']});
    if(loggingAbsent) return {ok:false, text:async()=>'The runtime is absent: service resolution: runtime_unavailable'};
    const q = new URLSearchParams(path.split('?')[1]);
    const reply = (q.get('follow')==='1' ? pages.follow : pages.read).shift();
    assert(reply, 'unexpected logging request '+path);
    return {ok:true, json:async()=>reply};
   }};
  const tick = () => new Promise(r => setImmediate(r));
  // localTime used to build its own 24-hour clock by hand, the same
  // formatting logging_page.go's logTimeParts used to; both now read
  // through the one shared oaClockText (panel_shell.go), so this test
  // helper reads through it too, for the same reason (task 2026-09-23,
  // twelfth first-time visitor, minor: one time format across pages).
  const localTime = iso => context.oaClockText(new Date(iso), true);
  const localDate = iso => new Date(iso).toLocaleDateString();
  const base = requests.length;
  // task 2026-09-23, eleventh first-time visitor, requirement 2: the page
  // used to open empty, an App name field, a level dropdown and a Read
  // button, nothing shown until clicked. It now reads the last 50 records
  // of the last hour itself, on arrival, with no click at all.
  vm.runInNewContext(plainScript(), context);
  vm.runInNewContext(loadPageScript('logging_page.go'), context);
  await tick(); await tick(); await tick();
  const first = new URLSearchParams(requests[base].path.split('?')[1]);
  assert.equal(requests[base].key, 'test', 'panel key sent');
  assert.equal(first.get('cursor'), '', 'the arrival read sends an empty cursor');
  assert.equal(first.get('program'), '', 'the arrival read names no program: All programs');
  assert.equal(first.get('level'), '', 'the arrival read applies no level filter: Everything');
  assert.equal(first.get('count'), '50', 'the arrival read asks for the default count, 50');
  assert(first.get('since'), 'the arrival read carries a since bound: the last hour');
  assert(new Date(first.get('since')).getTime() > Date.now()-3601000, 'the since bound is about an hour back, not unbounded');
  // The quick choices row leads with Last hour, active by default; the
  // level segmented control leads with Everything, active by default.
  assert.equal(element('log-quick').children.map(b=>b.textContent).join(', '), 'Last hour, Today, Last 7 days, All, Custom range…');
  assert.equal(element('log-quick').children[0].className, 'active', 'Last hour starts active');
  assert.equal(element('log-level-seg').children.map(b=>b.textContent).join(', '), 'Everything, Warnings and errors, Errors');
  assert.equal(element('log-level-seg').children[0].className, 'active', 'Everything starts active');
  const boxes = element('log-records').children;
  assert.equal(boxes.length, 5, 'four records plus one date heading, since they all fall on the same local day');
  assert.equal(boxes[0].className, 'log-date', 'a date heading leads the first group');
  assert.equal(boxes[0].textContent, localDate('2026-09-17T10:00:00.000000Z'));
  const [, verified, bare, relayed, sinkGap] = boxes;
  const details = box => box.children[1];
  const hops = box => details(box).children[1].children;
  assert.equal(verified.className, 'record');
  // task 2026-09-23, eighth first-time visitor, finding 1: hops used to sit
  // under a disclosure that opened by default, showing every hop's raw
  // executable path, account SID and PID as visible text on every record.
  // It is now a collapsed Technical details disclosure, like every other
  // raw-data block in this Panel, and the visible line names the program by
  // its file name alone, never the full path a hop's own claim carries.
  assert.equal(details(verified).children[0].textContent, 'Technical details', 'hops sit behind the same disclosure every other raw-data block uses');
  assert.equal(details(verified).open, undefined, 'the disclosure is collapsed by default');
  assert.equal(verified.children[0].children[0].className, 'verified', 'a stamped record is labelled verified on its one line');
  assert(verified.children[0].textContent.includes(localTime('2026-09-17T10:00:00.000000Z')+' writer.exe: verified record'), 'the visible line names local time, the program\'s file name (not its full path) and the message, with no level word for an ordinary INFO record');
  assert(!verified.children[0].textContent.includes('C:\\apps\\writer.exe'), 'the full program path is not in the visible line');
  assert(hops(verified)[0].textContent.includes('C:\\apps\\writer.exe')||hops(verified)[1].textContent.includes('C:\\apps\\writer.exe'), 'the full program path stays reachable in the record\'s own Technical details');
  assert.equal(bare.children[0].children[0].className, 'verified', 'the stamp alone still verifies the line even with no writer claim');
  assert.equal(relayed.children[0].children[0].className, 'claim', 'an unauthored record is unverified');
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
  // task 2026-09-23, eleventh first-time visitor, requirement 2: the
  // program filter is a dropdown built from the programs seen in the
  // records that have already come back, "All programs" first and
  // "Other…" last for a name not seen yet.
  assert.equal(element('log-program-select').children.map(o=>o.textContent).join(', '), 'All programs, writer.exe, writer, Other\u2026', 'the dropdown lists every program seen, by its file name');
  assert.equal(element('log-program-select').children[1].title, 'C:\\apps\\writer.exe', 'the full path sits on the option\'s own title');
  assert.equal(element('log-program').hidden, true, 'the typed field stays hidden until Other\u2026 is chosen');

  await element('log-follow').onclick();
  const followed = requests.slice(base+1).map(r => new URLSearchParams(r.path.split('?')[1]));
  assert.equal(followed.length, 2, 'following continues until the gap');
  assert.equal(followed[0].get('follow'), '1'); assert.equal(followed[0].get('cursor'), 'c1', 'follow continues from the read cursor');
  assert.equal(followed[1].get('cursor'), 'c2');
  const list = element('log-records').children;
  assert.equal(list[5].children[0].textContent.includes(localTime('2026-09-17T10:01:00.000000Z')+' error writer: live record'), true, 'live record appended, with its level word since ERROR is warning or higher');
  const observerGap = list[6];
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
  await element('log-quick').children[0].onclick();
  // fail (serviceCommonScript) routes an error through oaShowError/oaPlain
  // (plainScript) whenever both are loaded, the same way this Panel's real
  // page always has them; this subtest now loads plainScript too (it
  // already needs oaClockText for the row times), so the absent-runtime
  // reply reads the one plain sentence oaPlain gives any "service
  // resolution" failure, not the raw "The runtime is absent: ..." text.
  assert.equal(element('log-status').textContent, "That service isn't running on this account right now. Start the runtime, or check the service on the Status page.", 'absent runtime reported in the logging page, in the one plain sentence oaPlain gives it');
  loggingAbsent = false;

  // task 2026-09-23, eleventh first-time visitor, requirement 2: a quick
  // choice, a level segment and a count choice each restart the read with
  // that filter; an empty page names the range read and offers the next
  // wider quick choice.
  pages.read.push({mode:'read', outcome:'page', cursor:'', next:'e1', atEnd:true, hidden:0, sink, records:[]});
  await element('log-quick').children[1].onclick();
  assert.equal(element('log-quick').children[1].className, 'active', 'Today becomes the active quick choice');
  const todayQuery = new URLSearchParams(requests[requests.length-1].path.split('?')[1]);
  assert.equal(todayQuery.get('cursor'), '', 'a quick choice restarts the read from the start');
  assert.equal(element('log-status').textContent, 'No records in today.', 'an empty page names the range it read, in words');
  const offer = element('log-status').children.find(c=>c.textContent==='Last 7 days');
  assert(offer, 'the empty state offers the next wider quick choice');

  pages.read.push({mode:'read', outcome:'page', cursor:'', next:'e2', atEnd:true, hidden:0, sink, records:[]});
  await offer.onclick();
  assert.equal(element('log-quick').children[2].className, 'active', 'the offer switches to the quick choice it named');

  pages.read.push({mode:'read', outcome:'page', cursor:'', next:'e3', atEnd:true, hidden:0, sink, records:[]});
  await element('log-level-seg').children[2].onclick();
  assert.equal(element('log-level-seg').children[2].className, 'active', 'Errors becomes the active level segment');
  const errorsQuery = new URLSearchParams(requests[requests.length-1].path.split('?')[1]);
  assert.equal(errorsQuery.get('level'), '8', 'Errors sends the ERROR severity');

  pages.read.push({mode:'read', outcome:'page', cursor:'', next:'e4', atEnd:true, hidden:0, sink, records:[]});
  element('log-count').value='200';
  await element('log-count').onchange();
  const countQuery = new URLSearchParams(requests[requests.length-1].path.split('?')[1]);
  assert.equal(countQuery.get('count'), '200', 'the count choice asks for that many records');

  console.log('PASS: Logging page hops styled verified, claimed and untrusted, behind a collapsed Technical details disclosure, the visible line naming the program\'s file name only; unclaimed and relay hops named; sink-loss and observer gaps shown as gaps; follow stops at a gap; panel sink state; absent runtime; reads the last hour on arrival with no click; the program dropdown lists what it has seen; quick choices, level segments and the count choice each restart the read; an empty page offers the next wider range');
 }

 // --- Identity page (identity_page.go). ---
 {
  const element = makeElement();
  const claimHop = {hop:0, by:'self', role:'writer claim', standing:'claimed', verified:false, program:'writer', uid:-1, gid:-1, pid:7};
  const stampHop = {hop:1, by:'identity/windows', role:'writer', standing:'established', verified:true, exe:'C:\\apps\\writer.exe', user:'S-1-5-21-1', uid:-1, gid:-1, pid:7};
  const identityGood = {
   selection:{status:'TRUSTED', endpoint:'\\\\.\\pipe\\rt', account:'S-1-5-21-1', program:'C:\\oa\\tools\\openabstractions.exe'},
   explicit:{endpoint:'\\\\.\\pipe\\iso', program:'C:\\oa\\openabstractions.exe', account:'S-1-5-21-1'},
   platform:'windows', declarations:[{id:'windows-user', name:'Windows x64, per-user installation', status:'supported', limits:[]}],
   ceiling:{platform:'windows', transport:'npipe', best:{user:'kernel', process:'kernel', path:'bound', package:'signed', code:'bound'}, why:{}, bindable:true, binding:'a process handle'},
   runtime:{absent:false, outcome:'observed', mechanism:'identity/windows', account:'S-1-5-21-1', accountName:'reinis', program:exe, pid:4242, transport:'npipe', platform:'windows', bindable:true,
    attributes:[{attribute:'user', proof:'kernel', ceiling:'kernel'}, {attribute:'process', proof:'kernel', ceiling:'kernel'}, {attribute:'path', proof:'bound', ceiling:'bound'}, {attribute:'package', proof:'none', ceiling:'signed'}, {attribute:'code', proof:'unsigned', ceiling:'bound'}]},
   capabilities:[{name:'Logging history', contract:'abstraction.logging/reader@1', status:'resolved', label:'Ready'}, {name:'Rights administration', contract:'abstraction.rights/operator@1', status:'forbidden', label:'Access denied'}],
   logging:{absent:false, outcome:'found', claim:{...claimHop, program:'Abstraction Panel'}, stamp:{...stampHop, exe, accountName:'reinis'}, agreesWithRuntime:true},
   limits:['A program rule is only as strong as the path proof.'], checkedAt:'2026-09-17T10:02:00Z'};
  const identityAbsent = {
   selection:{status:'UNTRUSTED', detail:'select installed runtime: bootstrap: no trusted runtime installation'},
   platform:'darwin', declarations:[{id:'macos', name:'macOS (universal package)', status:'unsupported', limits:['Selection has no darwin path.']}],
   ceiling:{platform:'darwin', transport:'unix', best:{user:'kernel', process:'pid', path:'pid', package:'pid', code:'pid'}, why:{}, bindable:false, binding:'no binding', stronger:'XPC'},
   runtime:{absent:true, error:'service resolution: runtime_unavailable: abstraction.facade/caller@1', pid:-1, attributes:[]},
   capabilities:[], capabilityError:'runtime_unavailable',
   logging:{absent:true, outcome:'error', error:'service resolution: runtime_unavailable: abstraction.logging/reader@1'},
   limits:['On macOS, protected calls are refused: a socket peer cannot reach the Program proof.'], checkedAt:'2026-09-17T10:03:00Z'};
  let identityReply = identityGood;
  const context = {URLSearchParams, localStorage:{length:0, key(){}, getItem(){return null}, setItem(){}}, document:{getElementById:element, createElement:node}, location:{search:'?k=test'}, console,
   fetch:async(path)=>{
    if(path==='/identity') return {ok:true, json:async()=>identityReply};
    throw Error('unexpected request '+path);
   }};
  // identity_page.go's own script now calls oaLocalTime (task 2026-09-23,
  // tenth first-time visitor, finding 2), defined in plainScript
  // (panel_shell.go), so this sub-test loads it into the same vm context
  // first too.
  vm.runInNewContext(plainScript(), context);
  vm.runInNewContext(loadPageScript('identity_page.go'), context);

  // task 2026-09-23, twelfth first-time visitor, finding 3: the page's own
  // caption states what this section is, once, in the page markup itself
  // (not repeated on every check); the check then used to lay out every
  // paragraph it produces, one after another. It now shows one verdict
  // sentence; everything else this check can also show sits behind a
  // single Technical details disclosure.
  const identityHTML = fs.readFileSync(__dirname + '/identity_page.go', 'utf8');
  assert(identityHTML.includes('<h1 id="identity">Identity</h1><p>Who this Panel is running as, and how the runtime checked it.</p>'), 'the page states its own one caption once, ahead of any check');
  // task 2026-09-23, twelfth first-time visitor, finding 1: the page used
  // to show one caption and one button until Check identity was clicked.
  // It now runs the check once, unclicked, as soon as the page's own
  // script loads.
  await context.identityCheckReady;
  assert.equal(element('identity-view').children[0].className,'verified','the check already ran on arrival, with no click at all');
  await element('identity-check').onclick();
  const view = element('identity-view');
  const shown = text(view);
  const verdict = view.children[0];
  assert.equal(verdict.className, 'verified');
  assert.equal(verdict.textContent, "This Panel is talking to the runtime it was started for. The connection is proven by the operating system; the runtime's program is not code-signed on this machine.", 'the verdict sentence leads, naming both the expectation and the proof strength');
  assert(!verdict.textContent.toLowerCase().includes('pipe'), 'the verdict names no pipe endpoint');
  assert.equal(view.children.length, 2, 'nothing else sits directly on the page beside the verdict and the one Technical details');
  const technical = view.children[1];
  assert.equal(technical.children[0].textContent, 'Technical details');
  const technicalBody = technical.children[1];
  assert(text(technicalBody).toLowerCase().includes('pipe'), 'the raw endpoint is available under the one Technical details');
  assert(shown.includes('Platform declaration for Windows x64, per-user installation: supported.'));
  // task 2026-09-23, eighth first-time visitor, finding 7: the sentence
  // used to name the raw SID, the program's full path, the pid, and the raw
  // mechanism/transport words. It now names the account's display name, the
  // program's file name and the mechanism/transport in plain words; the raw
  // values move to the sentence's own title (the full JSON dump under
  // Technical details legitimately still carries them, which is why this
  // checks the one sentence's own textContent, not the whole page's).
  const establishedLine = all(view).find(p => p.textContent && p.textContent.startsWith('Your account,'));
  assert.equal(establishedLine.textContent, 'Your account, reinis, running monitor.exe, established by the Windows identity check over a named pipe.');
  assert.equal(establishedLine.title, 'account S-1-5-21-1; program '+exe+'; pid 4242', 'the raw SID, full path and pid sit on the sentence\'s own title');
  assert(shown.includes('Which program: proven by its path.'), 'the path attribute reads as a plain sentence, not a raw proof/ceiling dump');
  assert(shown.includes('Who is connected: proven by the operating system, the strongest proof this connection allows.'), 'a kernel-proven attribute names the strongest proof this connection allows');
  assert(shown.includes('Which process: proven by the operating system, the strongest proof this connection allows.'));
  assert(shown.includes('Signed package: none; this connection could prove one. Expected for a development build; an installed runtime carries one.'));
  assert(shown.includes('Signed code: not signed; expected for a development build.'));
  assert(shown.includes('Nothing here needs your attention.'), 'every row here reaches what the transport allows or is expected for a development build');
  // task 2026-09-23, seventh first-time visitor, finding 2: an id is never
  // visible page text; the contract id moves to the line's own title (the
  // full contract still appears in the page's own Technical details JSON
  // dump, which shown's recursive text collects too, so the id-absence
  // check below is against this one line's own textContent, not the whole
  // page).
  assert(shown.includes('Rights administration: access denied for this Panel.'), 'the capability line reads in plain words, the label lowercased into the sentence');
  const paragraphs = all(view);
  const rightsLine = paragraphs.find(p => p.textContent === 'Rights administration: access denied for this Panel.');
  assert(rightsLine && !rightsLine.textContent.includes('abstraction.rights/operator@1'), 'the contract id is not in this line\'s own visible text');
  assert(rightsLine && rightsLine.title === 'abstraction.rights/operator@1', 'the contract id sits in the line\'s own title attribute instead');
  assert(paragraphs.some(p => p.className==='claim' && p.textContent.includes('claiming to be Abstraction Panel')), 'the panel claim is a claim');
  // task 2026-09-23, ninth first-time visitor, finding 1: this paragraph
  // used to read "The logging service stamped it: executable C:\...\
  // Abstraction Panel.exe, account S-1-5-21-…, pid 7 (identity/windows,
  // verified by the logging service)." unchanged, the very raw SID, full
  // path and pid the sentence above it named until the previous fix. It now
  // matches that sentence's own shape; the checked-out node test had until
  // now asserted the old raw text as correct, which is why this paragraph's
  // leak survived that fix.
  const stampLine = paragraphs.find(p => p.textContent && p.textContent.startsWith('The logging service saw the same:'));
  assert.equal(stampLine.textContent, 'The logging service saw the same: your account, reinis, running monitor.exe, verified by the Windows identity check.');
  assert.equal(stampLine.className, 'verified');
  assert(!stampLine.textContent.includes(exe) && !stampLine.textContent.includes('S-1-5-'), 'the full path and raw SID are not in the visible line');
  assert.equal(stampLine.title, 'executable '+exe+'; account S-1-5-21-1; pid 7', 'the raw executable, account and pid sit on the sentence\'s own title');
  assert(shown.includes('bound the same program, account and process'));
  assert(shown.includes('A program rule is only as strong as the path proof.'));
  // task 2026-09-23, tenth first-time visitor, finding 2: "Checked at
  // 2026-09-23T18:14:50+03:00" read a raw ISO timestamp aloud; it now reads
  // a local readable time, the raw value kept on the line's own title.
  assert.equal(element('identity-status').textContent, 'Checked at '+context.oaLocalTime('2026-09-17T10:02:00Z')+'.');
  assert(!element('identity-status').textContent.includes('2026-09-17T10:02:00Z'), 'the raw timestamp is not in the visible line');
  assert.equal(element('identity-status').title, '2026-09-17T10:02:00Z');

  identityReply = identityAbsent;
  await element('identity-check').onclick();
  const absentView = element('identity-view');
  const absent = text(absentView);
  assert.equal(absentView.children[0].textContent, 'This Panel cannot reach a runtime right now: select installed runtime: bootstrap: no trusted runtime installation.', 'the verdict names the reason a selection failure gives');
  assert(absent.includes('Installed runtime selection: UNTRUSTED. select installed runtime'));
  assert(absent.includes('The runtime is absent: service resolution: runtime_unavailable: abstraction.facade/caller@1'), 'identity page reports the absent runtime');
  assert(absent.includes('The runtime is absent: service resolution: runtime_unavailable: abstraction.logging/reader@1'), 'logging view reports the absent runtime');
  assert(absent.includes('Platform declaration for macOS (universal package): unsupported.'));
  assert(absent.includes('On macOS, protected calls are refused'), 'macOS limit stated plainly');
  assert(absent.includes('No binding pins the caller'));
  assert(!absent.includes('saw the same'), 'no stamp without a runtime');
  console.log('PASS: Identity page selection, runtime caller rungs, logging stamp, refusals and absent runtime');
 }

 // --- Explore page (explore_page.go). ---
 {
  const element = makeElement();
  const editAction = 'abstraction.config/user.replace', editResource = 'abstraction.config/editor@1';
  const exploreCatalogue = {self:{program:exe, account:'S-1-5-21-1'}, selfAccountName:'reinis', actionPlain:{[editAction]:'config rewrite'}, probes:[
   {capability:'config', operation:'rewrite', contract:'abstraction.config/editor@1', writes:true, note:'WRITES: replaces your user settings with their current values at the read revision', rule:{action:editAction, resource:editResource}},
   {capability:'model', operation:'resolve', contract:'abstraction.model/resolver@1', argument:'REGISTRY:REPO[@REVISION]', rule:{action:'abstraction.model/lookup', resource:'<registry>'}},
   {capability:'router', operation:'pick', contract:'abstraction.router/router@1', argument:'MODEL', rule:{action:'abstraction.router/route', resource:'router'}},
   // task 2026-09-23, thirteenth first-time visitor, finding 8: "DIGEST"
   // named nothing, an example shape or where to find one.
   {capability:'storage', operation:'read', contract:'abstraction.storage/content-reader@1', argument:'DIGEST'},
   // task 2026-09-23, tenth first-time visitor, finding 6: a capability
   // with no rule at all left Grant and Revoke silently disabled forever,
   // explaining nothing.
   {capability:'asks', operation:'question.ask', contract:'abstraction.asks/question.ask@1'}]};
  const probeReply = (outcome, rule) => ({result:{capability:'config', operation:'rewrite', contract:'abstraction.config/editor@1', subject:{program:exe, account:'S-1-5-21-1'}, outcome, rule:{action:editAction, resource:editResource}}, rule});
  const unknownRule = {subject:{program:exe, account:'S-1-5-21-1'}, action:editAction, resource:editResource, outcome:'unknown', revision:'rev-1'};
  const foundRule = {...unknownRule, outcome:'found', revision:'rev-2', permit:true, setBy:{program:'C:\\oa\\openabstractions.exe', account:'S-1-5-21-1'}, setAt:'2026-09-17T10:05:00.000Z'};
  const exploreReplies = [probeReply('forbidden', unknownRule), probeReply('applied', foundRule), probeReply('forbidden', unknownRule), probeReply('forbidden', {...unknownRule, outcome:'forbidden', revision:''})];
  const exploreCalls = [], rightsPosts = [], confirmations = [];
  let confirmAnswer = true;
  const context = {URLSearchParams, localStorage:{length:0, key(){}, getItem(){return null}, setItem(){}}, document:{getElementById:element, createElement:node, createTextNode:text=>({textContent:text})}, location:{search:'?k=test'}, console, confirm:(t)=>{confirmations.push(t);return confirmAnswer}, Blob, URL:{createObjectURL(){}, revokeObjectURL(){}},
   fetch:async(path, opts)=>{
    if(path==='/explore') return {ok:true, json:async()=>exploreCatalogue};
    if(path.startsWith('/explore?')){exploreCalls.push(new URLSearchParams(path.split('?')[1]));const reply=exploreReplies.shift();assert(reply,'unexpected explore call '+path);return {ok:true, json:async()=>reply}}
    if(path==='/rights?cursor=') return {ok:true, json:async()=>({Outcome:'page', Revision:'rev-'+(rightsPosts.length+1), Catalog:[], Rules:[], Next:'', Complete:true})};
    if(path==='/rights'){const e=JSON.parse(opts.body);rightsPosts.push(e);return {ok:true, json:async()=>({Outcome:'applied', Revision:'rev-next'})}}
    throw Error('unexpected request '+path);
   }};
  vm.runInNewContext(plainScript(), context);
  vm.runInNewContext(loadPageScript('explore_page.go'), context);

  // task 2026-09-23, twelfth first-time visitor, finding 1: the page used
  // to show one sentence and one Load button until clicked. It now loads
  // its own capabilities once, unclicked, as soon as the page's own script
  // loads; Load still re-runs it for a different program or account.
  await context.exploreLoadReady;
  assert(element('explore-list').children.length>0, 'the capabilities list already renders on arrival, with no click at all');
  assert.equal(element('explore-status').children[0].textContent, "Showing this Panel's own capabilities. Probes act as monitor.exe. Rules name the program above.", 'the arrival read, with both fields genuinely blank, still says so');
  await element('explore-load').onclick();
  assert.equal(element('explore-program').value, exe, 'subject picker defaults to this Panel');
  assert.equal(element('explore-account').value, 'S-1-5-21-1');
  // task 2026-09-23, tenth first-time visitor, finding 4: the Account field
  // filled itself with the raw SID as visible input text and nothing else;
  // probes still need that raw value, so it stays the field's own value,
  // but a plain label beside it now names the account.
  assert.equal(element('explore-account').title, 'S-1-5-21-1', 'the raw SID also sits on the field\'s own title');
  assert.equal(element('explore-account-label').textContent, 'your account, reinis');
  // task 2026-09-23, ninth first-time visitor, finding 5: the status line
  // used to name the caller by its own full path; it now names it by its
  // file name, with the full path on the line's own title and in Technical
  // details.
  const exploreStatus = element('explore-status');
  // Load ran once already, unclicked, on arrival (above); by this second,
  // explicit click both fields are already filled from that first read, so
  // this read is no longer "showing this Panel's own capabilities" for the
  // first time.
  assert.equal(exploreStatus.children[0].textContent, 'Probes act as monitor.exe. Rules name the program above.');
  assert(!exploreStatus.children[0].textContent.includes(exe), 'the full path is not in the visible line');
  assert.equal(exploreStatus.title, exe, 'the full path stays reachable as the line\'s own title');
  assert.equal(exploreStatus.children[1].children[0].textContent, 'Technical details');
  assert(exploreStatus.children[1].children[1].textContent.includes(exe), 'the full path also sits behind Technical details');
  const [rewriteBox, modelBox, routerBox, storageReadBox, asksBox] = element('explore-list').children;
  // task 2026-09-23, thirteenth first-time visitor, finding 8: storage
  // read's own argument field named itself "DIGEST" with no explanation
  // and no example shape.
  assert.equal(storageReadBox.argument.placeholder, "the object's digest (sha256:…)", 'the field\'s own placeholder gives an example shape, not the raw word DIGEST');
  const storageCaption = storageReadBox.children.find(c=>c.textContent==='Copy a digest from the Device or Work page.');
  assert(storageCaption, 'a caption says where to find one');
  await storageReadBox.children.find(c=>c.textContent==='Call').onclick();
  assert.equal(storageReadBox.argumentNote.textContent, 'Fill in the object\'s digest (sha256:…) above to call storage read.', 'the missing-argument message sits beside the row\'s own field, in the same friendly words as its placeholder');
  // task 2026-09-23, tenth first-time visitor, finding 6: a capability
  // needing no rule left Grant and Revoke disabled with no explanation;
  // it now shows the same "Nothing to grant" wording the shared grant
  // control (panel_shell.go's gApplyHeld) already uses elsewhere, in place
  // of two buttons that could never do anything.
  assert.equal(asksBox.grant, undefined, 'no Grant button for a capability with no rule');
  assert.equal(asksBox.revoke, undefined, 'no Revoke button for a capability with no rule');
  const asksNote = asksBox.children.find(c => c.textContent === 'Nothing to grant; this capability needs no rule.');
  assert(asksNote, 'the note replacing Grant/Revoke names why they are absent');
  // task 2026-09-23, seventh first-time visitor, finding 2: a card used to
  // describe itself by its raw contract id alone; it now reads one plain
  // sentence, the deciding rule named in the same plain words Questions and
  // rights uses, with the contract and rule ids kept as this line's title.
  // editResource is itself a raw contract id ("abstraction.config/editor@1"),
  // the rule's own resource scope; argumentPhrase redacts that the same way
  // as any other raw id in visible text (task 2026-09-23, seventh
  // first-time visitor, finding 2), so the sentence names it generically and
  // keeps the real value only on the line's own title (checked below).
  assert.equal(rewriteBox.children[1].textContent, "Tries config rewrite. Decided by Config rewrite on this service. WRITES: replaces your user settings with their current values at the read revision", 'the card reads one plain sentence naming its deciding rule in plain words, with a raw contract id resource redacted the same way a raw action id is, "service" the vocabulary decision\'s own word for it (D4)');
  assert(!rewriteBox.children[1].textContent.includes(editAction), 'the raw action id is not in the visible sentence');
  assert.equal(rewriteBox.children[1].title, editResource+' — '+editAction+' on '+editResource, 'the contract and the rule\'s own action and resource sit on this line\'s title instead');
  assert.equal(rewriteBox.children[0].title, editResource, 'the contract sits on the plain name as a tooltip, not in the visible line');
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
  assert(text(rewriteBox.result).includes('Refused: this Panel is not authorized.'), 'refusal shown as a plain sentence, not a bare outcome word');
  assert(rewriteBox.result.children[1] && rewriteBox.result.children[1].children[0].textContent==='Technical details', 'the raw result sits under a disclosure');
  // task 2026-09-23, twelfth first-time visitor, finding 2: the deciding
  // rule's own record (its SID, two full paths, an ISO time and the rules
  // revision) used to sit in a `<pre>` sibling of this same disclosure,
  // always visible with no toggle at all; it now sits inside the one
  // Technical details disclosure this result already has, alongside the
  // raw JSON that was already there.
  const ruleDetailsText = box => text(box.result.children[1]);
  assert(ruleDetailsText(rewriteBox).includes('No exact rule for S-1-5-21-1 running '+exe), 'missing rule shown under Technical details');
  assert.equal(rewriteBox.grant.disabled, false);
  await rewriteBox.grant.onclick();
  assert.deepEqual(rightsPosts[0], {edit:'set', revision:'rev-1', account:'S-1-5-21-1', program:exe, action:editAction, resource:editResource, permit:true}, 'grant is the exact rule at the listed revision');
  assert(element('explore-status').textContent.startsWith('Granted Config rewrite'), 'grant reported in plain words, not the raw action id');
  // A call a rule decided now names that rule in the one visible line
  // itself ("Allowed: Config rewrite on this service, set by
  // openabstractions.exe on <date>."), the same plain words a rule's own
  // row reads with, replacing the old generic "Allowed; the call
  // answered." for this case.
  const foundDate = new Date(foundRule.setAt).toLocaleDateString();
  assert.equal(rewriteBox.result.children[0].textContent, 'Allowed: Config rewrite on this service, set by openabstractions.exe on '+foundDate+'.', 'the deciding rule is named in the one visible line, in plain words, not the raw action id');
  assert(!rewriteBox.result.children[0].textContent.includes(editAction), 'the raw action id is not in the visible line');
  assert(ruleDetailsText(rewriteBox).includes('permit Config rewrite') && ruleDetailsText(rewriteBox).includes('set by C:\\oa\\openabstractions.exe'), 'the deciding rule\'s full record, with the raw path, sits under Technical details');
  assert(ruleDetailsText(rewriteBox).includes('Rules revision: rev-2'), 'the full revision also sits there');
  // task 2026-09-23, fourteenth first-time visitor, finding 6: Grant on a
  // capability an installation rule (or an earlier Grant, as here) already
  // permits used to still round-trip a "set" edit for the identical rule
  // and land wherever that outcome fell in rightsStatus's own table,
  // reading as though the click did nothing; it now says so directly, with
  // no request sent at all.
  const rightsPostsBeforeRegrant = rightsPosts.length;
  await rewriteBox.grant.onclick();
  assert.equal(element('explore-status').textContent, 'Already allowed.', 'a second Grant on an already-permitted rule says so in one sentence, instead of repeating the round trip');
  assert.equal(rightsPosts.length, rightsPostsBeforeRegrant, 'no edit was sent for a rule already exactly as granted');
  await rewriteBox.revoke.onclick();
  assert.deepEqual(rightsPosts[1], {edit:'revoke', revision:'rev-2', account:'S-1-5-21-1', program:exe, action:editAction, resource:editResource}, 'revoke names the exact rule');
  assert(text(rewriteBox.result).includes('Refused: this Panel is not authorized.'), 'the call ran again and was refused');
  await rewriteBox.children.find(c=>c.textContent==='Call').onclick();
  assert(ruleDetailsText(rewriteBox).includes('not a rules operator'), 'operator refusal explained under Technical details');

  assert(text(modelBox.children[1]).includes('Decided by Model: lookup on the registry you enter'), 'a placeholder-shaped resource ("<registry>") reads as a plain clause, not printed as row text (finding 5), and the action reads in plain words, not the raw id');
  assert(!text(modelBox.children[1]).includes('abstraction.model/lookup'), 'the raw action id is not in the visible sentence');
  const callsBefore = exploreCalls.length;
  await modelBox.children.find(c=>c.textContent==='Call').onclick();
  assert.equal(exploreCalls.length, callsBefore, 'a probe needing an argument is not sent without one');
  // task 2026-09-23, thirteenth first-time visitor, finding 8: this
  // message used to write to explore-status, shared by every probe on the
  // page; it now sits beside the row's own field instead.
  assert(!element('explore-status').textContent.includes('Fill in'), 'the shared status line is untouched by a per-row validation message');
  assert(modelBox.argumentNote.textContent.includes('Fill in REGISTRY:REPO[@REVISION] above'), 'the message sits beside the row\'s own field and names it');
  const argumentLabel = modelBox.children.find(c => c.children.includes(modelBox.argument));
  assert(argumentLabel, 'the argument field sits under its own labelled row');
  assert(!argumentLabel.textContent.includes('REGISTRY:REPO[@REVISION]'), 'the placeholder text is not printed as row text (finding 5)');
  assert.equal(modelBox.argument.placeholder, 'REGISTRY:REPO[@REVISION]', 'the placeholder lives inside the input itself');

  // A probe this runtime refuses as incompatible marks itself "Not offered"
  // after that first refusal (finding 5) instead of inviting another Call.
  exploreReplies.push({result:{capability:'router', operation:'pick', contract:'abstraction.router/router@1', subject:{program:exe, account:'S-1-5-21-1'}, outcome:'error', resolution:'incompatible'}, rule:null});
  routerBox.argument.value = 'llama';
  await routerBox.children.find(c=>c.textContent==='Call').onclick();
  assert(text(routerBox.result).includes('Refused: the runtime does not support this call.'));
  assert(text(routerBox).includes('Not offered by this runtime.'), 'the row states it plainly once refused as incompatible');
  assert.equal(routerBox.run.disabled, true, 'Call stops inviting another try');
  assert.equal(routerBox.grant.disabled, true);
  assert.equal(routerBox.revoke.disabled, true);
  // task 2026-09-23, eleventh first-time visitor, finding 1: Call on a
  // capability with no rule at all threw "Cannot set properties of
  // undefined (setting 'disabled')", since exploreRun set box.grant.disabled
  // unconditionally, and such a probe has no box.grant to set. Calling it
  // now renders the result instead of throwing.
  exploreReplies.push({result:{capability:'asks', operation:'question.ask', contract:'abstraction.asks/question.ask@1', subject:{program:exe, account:'S-1-5-21-1'}, outcome:'applied'}, rule:null});
  await asksBox.children.find(c=>c.textContent==='Call').onclick();
  assert(text(asksBox.result).includes('Allowed'), 'the call ran and rendered its result instead of throwing');
  console.log('PASS: Explore page write probe labelled and confirmed, grant, call, revoke with exact rules at the listed revision, deciding rule beside each result, operator refusal; a placeholder resource reads as a clause; argument is a labelled input with the placeholder inside it; a probe refused as incompatible marks itself not offered; the Account field names the display name beside the raw value it still needs; a rule-less capability names why Grant is absent instead of staying inertly disabled; Call on a rule-less capability renders its result instead of throwing');
 }
})().catch(e=>{console.error(e);process.exitCode=1});
