package main

// Both Models and Providers expose the canonical PROGRAM review. The browser
// sends only a key and observed revision; the backend rereads the complete Spec.
// While the runtime-down banner shows, the section is hidden whole
// (data-runtime-echo): its list cannot be read then.
const programReviewPanel = `<section id="program-review-section" data-runtime-echo class="oa-group" hidden><h2>Programs needing review</h2><p class="oa-caption">Review these apps to keep using them.</p><p class="oa-status" id="program-review-status" role="status" aria-live="polite" data-state="idle"></p><div id="program-review-rows"></div></section>
<script>(function(){
 const section=document.getElementById('program-review-section'),rows=document.getElementById('program-review-rows'),status=document.getElementById('program-review-status'),key=new URLSearchParams(location.search).get('k');
 let last='';
 async function request(body){const response=await fetch('/registry/programs',{method:body?'POST':'GET',headers:{'X-Panel-Key':key,'Content-Type':'application/json'},...(body?{body:JSON.stringify(body)}:{})});if(!response.ok)throw Error(await response.text());return response.json()}
 async function review(record,button){button.disabled=true;status.textContent='Requesting a native review…';try{const answer=await request({action:'review',key:record.Key,revision:record.Revision});status.textContent=answer.reason||('Review status: '+answer.outcome);await refresh()}catch(error){oaShowError('program-review-status',error)}finally{button.disabled=false}}
 function element(tag,className,text){const node=document.createElement(tag);if(className)node.className=className;if(text)node.textContent=text;return node}
 function rowFor(record){
  const row=element('div','oa-row'),text=element('div','oa-row-text');
  const cause=record.State==='disabled'?'This program is disabled. Its current choice is preserved.':record.Reason==='approval:unavailable'?'The native review question is unavailable. Retry this review.':record.State==='pending_approval'&&String(record.Reason||'').startsWith('Review the open OpenAbstractions question')?'Answer the open OpenAbstractions question to use this program.':record.State==='pending_approval'?'Review it to use this program.':'The program file changed. Review the current file before using it.';
  text.append(element('span','oa-row-label',record.ProductName||'Registered program'),element('span','oa-row-caption',cause));
  // The program's path and the service's reason are raw values: they sit
  // behind Technical details (architect ruling 2026-11-09, second block).
  const details=oaTechnical(element('details','oa-expander')),summary=element('summary',null,'Technical details');
  details.append(summary,element('p',null,record.Spec.Program));
  if(record.Reason||record.VerificationReason)details.append(element('p',null,record.Reason||record.VerificationReason));
  text.append(details);row.append(text);
  const mixed=(record.Spec.Applications||[]).length||(record.Spec.Remotes||[]).length;
  if(!mixed&&record.State!=='disabled'){
   const pending=record.State==='pending_approval'&&record.Verification!=='changed'&&String(record.Reason||'').startsWith('Review the open OpenAbstractions question');
   const button=element('button',null,pending?'Waiting for your answer':'Review');
   button.disabled=pending;button.onclick=()=>review(record,button);row.append(button);
  }
  return row;
 }
 async function refresh(){
  try{
   const answer=await request(),records=(answer.registrations||[]).filter(record=>record.IdentityEvidence==='program'&&(record.Verification==='changed'||record.State==='pending_approval'));
   const signature=JSON.stringify(records);if(signature===last)return;last=signature;
   section.hidden=!records.length;rows.replaceChildren(...records.map(rowFor));
  }catch(error){last='';section.hidden=false;oaShowError('program-review-status',error)}
 }
 window.programReviewRefresh=refresh;refresh();if(typeof setInterval==='function')setInterval(()=>{if(document.visibilityState!=='hidden')refresh()},15000);
})();</script>`
