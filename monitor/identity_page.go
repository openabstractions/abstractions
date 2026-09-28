package main

// identityPage is the Panel's Identity page: which runtime this device is
// using, how it recognized this Panel, and what this platform can prove
// about it (task 2026-09-23, requirement 1: split out of the Status page,
// its own page at /identity). Its one caption states that plainly (task
// 2026-09-23, twelfth first-time visitor, finding 3: the page used to lay
// out every paragraph this check produces, one after another); the check
// itself then shows only the verdict sentence, with everything else it can
// also show behind a single Technical details disclosure.
const identityPage = `<h1 id="identity">Identity</h1><p>Who this Panel is running as, and how the runtime checked it.</p><button id="identity-check">Check identity</button><pre id="identity-status"></pre><div id="identity-view"></div>
<script>` + serviceCommonScript + `
const standingText={established:'verified by the logging service',vouched:'vouched for through a trusted path',asserted:'asserts verification nothing the Panel trusts stands behind'};
// identityMechanismText and identityTransportText read the raw mechanism
// ("identity/windows") and transport ("npipe") words as a plain phrase
// (task 2026-09-23, eighth first-time visitor, finding 7): "established by
// identity/windows over npipe" is now "established by the Windows identity
// check over a named pipe". An unlisted future word still shows, unchanged,
// the same way storeLabel (credentials_page.go) treats an unrecognized
// platform store word.
const identityMechanismText={'identity/windows':'the Windows identity check','identity/linux':'the Linux identity check','identity/darwin':'the macOS identity check'};
function identityMechanismPhrase(m){return identityMechanismText[m]||m}
const identityTransportText={npipe:'a named pipe',unix:'a Unix domain socket'};
function identityTransportPhrase(t){return identityTransportText[t]||t}
const identityCodeText={signed:'the runtime\'s program is code-signed on this machine',bound:'the runtime\'s program is verified by content match but not code-signed on this machine',unsigned:'the runtime\'s program is not code-signed on this machine',none:'the runtime\'s program\'s code identity is not verified on this machine'};
const identityProofPhrase={kernel:'proven by the operating system',signed:'proven by a validated code signature',bound:'proven by a bound handle to the process',pid:'proven by process id, a weaker check',claimed:'only the peer\'s own claim',none:'not proven',invalid:'a signature the operating system rejected',unmet:'signed, but not by what this call required'};
// identityAttributeOK marks a row that needs no attention: it reached what
// this transport can prove, or its shortfall is the one every development
// build has (an unpackaged, unsigned program) rather than a surprise.
function identityAttributeOK(a){if(a.attribute==='package'&&a.proof==='none')return true;if(a.attribute==='code'&&a.proof==='unsigned')return true;return a.proof===a.ceiling}
function identityAttributeLine(a){const reachedMax=a.proof===a.ceiling;switch(a.attribute){
 case 'user':return 'Who is connected: '+(identityProofPhrase[a.proof]||a.proof)+(reachedMax?', the strongest proof this connection allows.':'; this connection could prove more.');
 case 'process':return 'Which process: '+(identityProofPhrase[a.proof]||a.proof)+(reachedMax?', the strongest proof this connection allows.':'; this connection could prove more.');
 case 'path':return a.proof==='none'||a.proof==='claimed'?'Which program: not proven.':'Which program: proven by its path.';
 case 'package':if(a.proof==='none')return 'Signed package: none; this connection could prove one. Expected for a development build; an installed runtime carries one.';return 'Signed package: '+(reachedMax?'signed.':(identityProofPhrase[a.proof]||a.proof)+'.');
 case 'code':if(a.proof==='unsigned')return 'Signed code: not signed; expected for a development build.';return 'Signed code: '+(reachedMax?'signed.':(identityProofPhrase[a.proof]||a.proof)+'.');
 default:return a.attribute+': '+(identityProofPhrase[a.proof]||a.proof)+'.';
}}
function identityProofClause(v){const bind=v.ceiling.bindable?'The connection is proven by the operating system':'The connection is not proven by the operating system';const code=(v.runtime.attributes||[]).find(a=>a.attribute==='code');const codeClause=code&&identityCodeText[code.proof];return codeClause?bind+'; '+codeClause+'.':bind+'.'}
function identityVerdict(v){const r=v.runtime;if(r.outcome==='observed')return(v.explicit?'This Panel is talking to the runtime it was started for.':'This Panel is talking to the runtime installed on this machine.')+' '+identityProofClause(v);if(r.absent)return 'This Panel cannot reach a runtime right now: '+(v.selection.detail||r.error||'no runtime is reachable')+'.';return 'This Panel could not confirm which runtime it is talking to.'}
// renderIdentity used to lay out every paragraph this check produces
// directly on the page: the selection warning, each platform declaration,
// every proof attribute, every capability line, the logging service's own
// stamp, and the limits list, one after another (task 2026-09-23, twelfth
// first-time visitor, finding 3: the page's own caption already says what
// this section is; a first-time visitor then sees one verdict sentence,
// with everything this check can also show, unchanged, behind a single
// Technical details disclosure, next to the raw JSON that was already
// there).
function renderIdentity(v){const out=el('identity-view');out.replaceChildren();const s=v.selection;
para(out,identityVerdict(v),v.runtime.outcome==='observed'?'verified':'error');
const details=document.createElement('details');const summary=document.createElement('summary');summary.textContent='Technical details';details.append(summary);
const body=document.createElement('div');details.append(body);out.append(details);
if(s.status!=='TRUSTED')para(body,'Installed runtime selection: '+s.status+(s.detail?'. '+s.detail:'')+'.','error');
for(const d of v.declarations)para(body,'Platform declaration for '+d.name+': '+d.status+'.');
heading(body,'What was proven, and what this transport can prove at most');const r=v.runtime;
if(r.outcome==='observed'){
// The sentence used to name the account by its raw SID, the program by its
// full path and the mechanism/transport by their raw words ("Account
// S-1-5-21-…, program C:\...\Abstraction Panel.exe, pid 1234, established
// by identity/windows over npipe"); it now names the account by its
// display name, the program by its file name, and the mechanism/transport
// in plain words, with the raw SID, full path and pid kept on the
// sentence's own title (task 2026-09-23, eighth first-time visitor,
// finding 7).
para(body,'Your account, '+(r.accountName||r.account)+', running '+shortName(r.program)+', established by '+identityMechanismPhrase(r.mechanism)+' over '+identityTransportPhrase(r.transport)+'.','verified','account '+r.account+'; program '+r.program+'; pid '+r.pid);let allOK=true;for(const a of r.attributes){const ok=identityAttributeOK(a);if(!ok)allOK=false;para(body,identityAttributeLine(a),ok?'verified':'error')}para(body,allOK?'Nothing here needs your attention.':'Some of what is proven here falls short of what this connection allows; check the rows above.')}else{if(r.outcome)para(body,'The runtime answered '+r.outcome+' and showed no identity.','error');else para(body,(r.absent?'The runtime is absent: ':'The runtime refused this Panel: ')+r.error,'error');const c=v.ceiling;para(body,'This transport can prove at most: user '+c.best.user+', process '+c.best.process+', path '+c.best.path+', package '+c.best.package+', code '+c.best.code+'. '+(c.bindable?'A binding pins the caller\'s process.':'No binding pins the caller\'s process.'))}
// Each capability line used to name its own contract id inline, "Logging
// (abstraction.logging/sink@1): Ready for this Panel." (task 2026-09-23,
// seventh first-time visitor, finding 2: an id is never visible page text).
// It now reads "Logging: ready for this Panel.", the label lowercased into
// the sentence; the contract id moves to the line's own title attribute.
for(const c of v.capabilities)para(body,c.name+': '+c.label.toLowerCase()+' for this Panel.',c.status==='resolved'?'':c.status==='forbidden'?'error':'',c.contract);
if(v.capabilityError)para(body,'Resolution check stopped: '+v.capabilityError,'error');
heading(body,'How the logging service bound this Panel');const L=v.logging;
// The logging service's own stamp used to read "executable C:\...\Abstraction
// Panel.exe, account S-1-5-21-…, pid 1234 (identity/windows, verified by the
// logging service)", the same raw SID, full path and pid the sentence above
// it named directly until the previous fix. It now names the account by its
// display name and the program by its file name, in the same shape as that
// sentence; the raw path, account and pid sit on this sentence's own title
// (task 2026-09-23, ninth first-time visitor, finding 1).
function loggingStandingPhrase(stamp){return stamp.standing==='established'?'verified by '+identityMechanismPhrase(stamp.by):(standingText[stamp.standing]||stamp.standing)}
if(L.outcome==='found'){if(L.claim)para(body,'The Panel wrote a record claiming to be '+L.claim.program+'.','claim');if(L.stamp)para(body,'The logging service saw the same: your account, '+(L.stamp.accountName||L.stamp.user||L.stamp.uid)+', running '+shortName(L.stamp.exe)+', '+loggingStandingPhrase(L.stamp)+'.',L.stamp.standing==='established'?'verified':'asserted','executable '+L.stamp.exe+'; account '+(L.stamp.user||L.stamp.uid)+'; pid '+L.stamp.pid);if(L.agreesWithRuntime===true)para(body,'The logging service and the runtime bound the same program, account and process.');if(L.agreesWithRuntime===false)para(body,'The logging service and the runtime disagree about this Panel.','error')}
else para(body,L.absent?'The runtime is absent: '+L.error:'The logging service did not show its view: '+(L.error||L.outcome),'error');
heading(body,'Limits on this platform');
const limits=document.createElement('ul');for(const l of v.limits){const li=document.createElement('li');li.textContent=l;limits.append(li)}body.append(limits);
const pre=document.createElement('pre');pre.textContent=JSON.stringify(v,null,2);body.append(pre)}
// identityCheck used to run only on a Check identity click, so the page
// opened as one caption and one button with no verdict until clicked (task
// 2026-09-23, twelfth first-time visitor, finding 1: the other pages
// already read themselves on arrival). It now also runs once, unclicked, as
// soon as the page's own script loads; Check identity still re-runs it.
async function identityCheck(){show('identity-status','Checking identity...');try{const v=await call('/identity');renderIdentity(v);el('identity-status').textContent='Checked at '+oaLocalTime(v.checkedAt)+'.';el('identity-status').title=v.checkedAt}catch(e){el('identity-view').replaceChildren();fail('identity-status',e)}}
el('identity-check').onclick=identityCheck;
var identityCheckReady=identityCheck();
</script>`
