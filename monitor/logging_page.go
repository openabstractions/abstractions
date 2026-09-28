package main

// loggingPage is the Panel's Logging page: records kept by the runtime (task
// 2026-09-23, requirement 1: split out of the Status page, its own page at
// /logging).
//
// The page used to open empty: an empty App name field, a level dropdown
// defaulting to INFO, two raw date-time pickers, and three buttons, nothing
// shown until one was clicked. The owner's rule (task 2026-09-23, eleventh
// first-time visitor) is that no field opens empty when a quick choice or
// the current state can fill it. It now reads the last 50 records of the
// last hour on arrival; a row of quick choices (Last hour, Today, Last 7
// days, All, or a typed custom range) replaces the two date pickers for the
// common case, a count choice replaces a number nobody could see coming,
// the program filter is a dropdown of the programs this page has actually
// seen (an "Other…" option still takes a typed name), and the level filter
// is three buttons instead of five raw severities nobody asked for. Follow
// live is unchanged. log-sink's own plain sentence ("This Panel's own
// logging: delivering…") used to render monospace in a <pre>, like a JSON
// dump instead of a status sentence (task 2026-09-23, fourteenth
// first-time visitor, finding 1); it is a <p> now, since it only ever
// carries the one sentence sinkText builds, never a details child.
const loggingPage = `<h1 id="logging">Logging</h1><p class="oa-note">Records kept by the runtime.</p>
<input type="hidden" id="log-custom-active">
<div id="log-quick"></div>
<div id="log-custom" hidden><label>From <input id="log-since" type="datetime-local"></label> <label>Until <input id="log-until" type="datetime-local"></label> <button id="log-custom-apply" type="button">Apply</button></div>
<div><label>Program <select id="log-program-select" aria-label="Program"></select></label> <input id="log-program" placeholder="Program name" hidden></div>
<div id="log-level-seg"></div>
<div><label>Show <select id="log-count" aria-label="Record count"><option value="50">50</option><option value="200">200</option><option value="1000">1000</option></select></label></div>
<button id="log-next" disabled>Next page</button><button id="log-follow">Follow live</button><pre id="log-status"></pre><p id="log-sink"></p><div id="log-records"></div>
<script>` + serviceCommonScript + `
let logCursor=null,logFollowing=false,logLastDate='';const logKept=1000;
// logTimeParts used to build its own 24-hour clock by hand ("20:32:56")
// while every other page's time reads through oaLocalTime's 12-hour one
// ("8:42 PM") — two formats for the same thing (task 2026-09-23, twelfth
// first-time visitor, minor). It now reads oaClockText, the exact function
// oaLocalTime itself calls (panel_shell.go), with seconds on: several log
// records land in the same minute, so this row keeps that precision.
function logTimeParts(t){const d=new Date(t);return{time:oaClockText(d,true),date:d.toLocaleDateString()}}
const logLevelWord={WARN:'warning',ERROR:'error'};
function logLevelText(r){return r.level>=4?(logLevelWord[r.levelName]||String(r.levelName||'').toLowerCase())+' ':''}
function logDateHeading(list,t){const parts=logTimeParts(t);if(parts.date!==logLastDate){logLastDate=parts.date;const h=document.createElement('h4');h.className='log-date';h.textContent=parts.date;list.append(h)}}
const logOutcomes={gap:'History gap: the cursor no longer names retained history, because the logging service restarted or its history was replaced. Records in between may be missing. Read from the start to continue.',unavailable:'The logging service has no readable history right now.',unsupported:'The logging service does not offer live observation.',invalid_request:'The logging service refused the request as invalid.',record_too_large:'A retained record is larger than one page and cannot be shown.',corrupt:'The retained history is corrupt at this position.'};
function isoOrEmpty(v){if(!v)return '';const d=new Date(v);return isNaN(d.getTime())?'':d.toISOString()}
const standingText={established:'verified by the logging service',vouched:'vouched for through a trusted path',asserted:'asserts verification nothing the Panel trusts stands behind'};
function hopText(h){const who=[h.program&&'program '+h.program,h.exe&&'executable '+h.exe,h.user&&'account '+h.user,h.uid>=0&&'uid '+h.uid,h.pid>=0&&'pid '+h.pid,h.host&&'host '+h.host].filter(Boolean).join(', ');const standing=standingText[h.standing]||(h.by==='unclaimed'?'the writer sent no claim':'the writer\'s own claim, unverified');return 'hop '+h.hop+', '+h.role+' ('+h.by+'): '+standing+(who?'. '+who:'')}
function hopClass(h){return 'hop '+(h.standing==='established'||h.standing==='vouched'?'verified':h.standing==='asserted'?'asserted':'claim')}
function logRecordVerified(r){return !!r.author}
function logEntry(r){const box=document.createElement('div');if(r.kind==='sink_gap'){box.className='gap';const t=document.createElement('p');t.textContent='Sink loss: '+r.dropped+' records from '+(r.program||'a writer with no program claim')+' were dropped between '+r.since+' and '+r.until+' while its logging service was unreachable.';box.append(t);return box}
box.className='record';const line=document.createElement('p');const verified=logRecordVerified(r);const label=document.createElement('span');label.className=verified?'verified':'claim';label.textContent=verified?'verified':'unverified';
line.textContent=logTimeParts(r.time).time+' '+logLevelText(r)+(r.program?shortName(r.program):'(no program claim)')+': '+r.message+(r.job?' [job '+r.job+']':'')+' ';line.append(label);box.append(line);
const details=document.createElement('details');const summary=document.createElement('summary');summary.textContent='Technical details';const hops=document.createElement('ul');for(const h of r.hops){const li=document.createElement('li');li.className=hopClass(h);li.textContent=hopText(h);hops.append(li)}details.append(summary,hops);if(r.attrs){const a=document.createElement('p');a.textContent=JSON.stringify(r.attrs);details.append(a)}box.append(details);
if(r.disputed){const d=document.createElement('p');d.className='error';d.textContent='Disputed: the writer\'s claim contradicts the stamp about the writer.';box.append(d)}return box}
function sinkText(s){if(!s)return '';if(!s.adopted)return 'This Panel\'s own logging: '+(s.bindError?'not bound. '+s.bindError:'not bound yet; it binds when the Panel first logs an action or checks identity.');return 'This Panel\'s own logging: '+s.state+'. '+s.accepted+' accepted, '+s.written+' written, '+s.queued+' queued, '+s.dropped+' dropped, '+s.failed+' failed, '+s.abandoned+' abandoned'+(s.lastError?'. Last error: '+s.lastError:'')+(s.transition?'. Last transition: '+s.transition:'')}
function logStop(){logFollowing=false;el('log-follow').textContent='Follow live'}
// logQuickChoices names each quick range, its own button label, and how to
// compute "since" from it: a millisecond span back from now, the string
// "today" (midnight local), or null for no lower bound at all (task
// 2026-09-23, eleventh first-time visitor, requirement 2).
const logQuickChoices=[['hour','Last hour',3600e3],['today','Today','today'],['week','Last 7 days',7*86400e3],['all','All',null]];
let logRangeKey='hour',logLevel='',logProgramFilter='';
function logRangeSince(key){const found=logQuickChoices.find(c=>c[0]===key);if(!found)return '';const span=found[2];if(span===null)return '';if(span==='today'){const d=new Date();d.setHours(0,0,0,0);return d.toISOString()}return new Date(Date.now()-span).toISOString()}
function logRenderQuick(){const out=el('log-quick');out.replaceChildren();for(const[key,label]of logQuickChoices){const b=document.createElement('button');b.type='button';b.textContent=label;b.className=logRangeKey===key&&!el('log-custom-active').value?'active':'';b.onclick=()=>{logRangeKey=key;el('log-custom-active').value='';el('log-custom').hidden=true;logRenderQuick();return logRestart()};out.append(b)}
 const custom=document.createElement('button');custom.type='button';custom.textContent='Custom range…';custom.className=el('log-custom-active').value?'active':'';custom.onclick=()=>{el('log-custom').hidden=false};out.append(custom)}
// logLevelChoices collapses the five raw severities (DEBUG/INFO/WARN/ERROR
// plus "any") this runtime's own logging.Level carries into the three a
// person actually chooses between: everything, warnings and errors up, or
// errors alone (task 2026-09-23, eleventh first-time visitor, requirement
// 2).
const logLevelChoices=[['','Everything'],['4','Warnings and errors'],['8','Errors']];
function logRenderLevel(){const out=el('log-level-seg');out.replaceChildren();for(const[value,label]of logLevelChoices){const b=document.createElement('button');b.type='button';b.textContent=label;b.className=logLevel===value?'active':'';b.onclick=()=>{logLevel=value;logRenderLevel();return logRestart()};out.append(b)}}
// logRebuildProgramSelect adds every program this page has actually seen
// in a record, "All programs" first and "Other…" last for a name not seen
// yet (task 2026-09-23, eleventh first-time visitor, requirement 2); the
// typed field beside it only shows once "Other…" is chosen.
let logPrograms=new Set();
function logRebuildProgramSelect(){const select=el('log-program-select');const current=select.value;select.replaceChildren();
 const all=document.createElement('option');all.value='';all.textContent='All programs';select.append(all);
 for(const p of[...logPrograms].sort()){const o=document.createElement('option');o.value=p;o.textContent=shortName(p);o.title=p;select.append(o)}
 const other=document.createElement('option');other.value='__other__';other.textContent='Other…';select.append(other);
 select.value=current&&(logPrograms.has(current)||current==='__other__')?current:''}
function logQuery(extra){const custom=!!el('log-custom-active').value;const since=custom?isoOrEmpty(el('log-since').value):logRangeSince(logRangeKey);const until=custom?isoOrEmpty(el('log-until').value):'';return '/logging?'+new URLSearchParams({cursor:logCursor||'',program:logProgramFilter,level:logLevel,since,until,count:el('log-count').value,...extra})}
// logEmptyOffer names the quick choice one step wider than the one just
// read, so an empty page offers exactly where to look next instead of
// leaving the person to guess (task 2026-09-23, eleventh first-time
// visitor, requirement 2: "No records in the last hour." with the next
// quick choice offered).
function logEmptyOffer(){const i=logQuickChoices.findIndex(c=>c[0]===logRangeKey);const next=logQuickChoices[i+1];if(!next)return null;const b=document.createElement('button');b.type='button';b.textContent=next[1];b.onclick=()=>{logRangeKey=next[0];el('log-custom-active').value='';el('log-custom').hidden=true;logRenderQuick();return logRestart()};return b}
function logPage(v,replace){show('log-sink',sinkText(v.sink));const list=el('log-records');if(replace){list.replaceChildren();logLastDate=''}if(v.outcome!=='page'){logStop();el('log-next').disabled=true;if(v.outcome==='gap'){logCursor=null;const g=document.createElement('div');g.className='gap';g.textContent=logOutcomes.gap;list.append(g)}show('log-status',logOutcomes[v.outcome]||'Logging service outcome: '+v.outcome);return false}logCursor=v.next;
for(const r of v.records){logDateHeading(list,r.time);list.append(logEntry(r));if(r.program)logPrograms.add(r.program)}
if(replace&&!v.records.length){const status=el('log-status');status.replaceChildren();const rangeLabel=(logQuickChoices.find(c=>c[0]===logRangeKey)||['','a custom range'])[1].toLowerCase();status.textContent='No records in '+(logRangeKey==='all'?'this runtime\'s retained history':rangeLabel)+'.';const offer=logEmptyOffer();if(offer)status.append(offer)}
else show('log-status',(v.mode==='follow'?'Following live. ':'')+(v.atEnd?'At the current end of history.':'More history remains.')+(v.hidden?' '+v.hidden+' records on this page hidden by the filter.':''));
logRebuildProgramSelect();
if(list.children.length>logKept)list.replaceChildren(...[...list.children].slice(-logKept));el('log-next').disabled=v.atEnd||v.mode==='follow';return true}
async function logRead(){try{logPage(await call(logQuery({})),true)}catch(e){logStop();fail('log-status',e)}}
function logRestart(){logStop();logCursor=null;return logRead()}
el('log-next').onclick=()=>{logStop();return logRead()};
el('log-program-select').onchange=()=>{const v=el('log-program-select').value;if(v==='__other__'){el('log-program').hidden=false;logProgramFilter=el('log-program').value;return}el('log-program').hidden=true;logProgramFilter=v;return logRestart()};
el('log-program').addEventListener('input',()=>{logProgramFilter=el('log-program').value});
el('log-program').addEventListener('change',logRestart);
el('log-custom-apply').onclick=()=>{el('log-custom-active').value='1';logRenderQuick();return logRestart()};
el('log-follow').onclick=async()=>{if(logFollowing){logStop();return}logFollowing=true;el('log-follow').textContent='Stop following';while(logFollowing){try{const v=await call(logQuery({cursor:logCursor||'end',follow:'1',wait:'10000'}));if(!logFollowing||!logPage(v,false))break}catch(e){logStop();fail('log-status',e);break}}};
el('log-count').onchange=logRestart;
el('log-custom-active').value='';
el('log-program').hidden=true;
logRenderQuick();logRenderLevel();logRebuildProgramSelect();el('log-count').value='50';
logRead();
</script>`
