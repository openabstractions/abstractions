package main

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	mwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/machines"
	"github.com/openabstractions/abstraction-facade/go/probe"
)

type machineEdit struct {
	Action      string                       `json:"action"`
	Request     mwire.MachineRequestIdentity `json:"request"`
	Preparation mwire.MachinePreparation     `json:"preparation"`
	Address     string                       `json:"address"`
	Code        string                       `json:"code"`
	Operation   string                       `json:"operation"`
	Cursor      string                       `json:"cursor"`
	Revision    string                       `json:"revision"`
	Peer        string                       `json:"peer"`
}

func (p *servicePanel) machinesRoutes(mux *http.ServeMux, key string) {
	mux.HandleFunc("/machines", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("k") != key {
			http.Error(w, "panel key required", 403)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, panelPage(r, key, "machines", "Paired PCs", machinesPage))
	})
	mux.HandleFunc("/machines/control", guard(key, p.machinesControl))
}
func (p *servicePanel) machinesControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	var edit machineEdit
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&edit) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid pairing request", 400)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	if edit.Action == "choices" {
		panelJSON(w, pairingChoices(ctx))
		return
	}
	control, err := panelMachine().ResolveMachineControl(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	var result any
	switch edit.Action {
	case "list":
		result, err = control.List(ctx, edit.Cursor, 64)
	case "offer":
		result, err = control.StartOffer(ctx, edit.Request, edit.Address, edit.Preparation)
	case "enter":
		result, err = control.Enter(ctx, edit.Request, edit.Code, edit.Preparation)
	case "observe":
		result, err = control.Observe(ctx, edit.Operation, edit.Cursor, 0*time.Second)
	case "cancel":
		result, err = control.Cancel(ctx, edit.Operation)
	case "remove":
		result, err = control.Remove(ctx, edit.Revision, edit.Peer)
	case "details":
		if !machinePeerKey(edit.Peer) {
			http.Error(w, "invalid computer", 400)
			return
		}
		var current *mwire.MachineRecord
		cursor, revision := "", ""
		for range 4 {
			page, e := control.List(ctx, cursor, 64)
			if e != nil || page.Outcome != mwire.MachineListOutcomePage || revision != "" && revision != page.Revision {
				break
			}
			revision = page.Revision
			for _, row := range page.Machines {
				if row.PeerKey == edit.Peer {
					copy := row
					current = &copy
					break
				}
			}
			if current != nil || page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		permissions := machinePermissions{Outcome: "unavailable", Rules: []policyRuleView{}, ActionPlain: rightsActionPlain}
		if current != nil {
			if operator, e := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal}); e == nil {
				account := probe.Self().Account
				permissions = readMachinePermissions(ctx, operator, account, edit.Peer)
				permissions = joinMachinePermissionSelectors(ctx, permissions, account, edit.Peer)
			}
		}
		result = struct {
			Machine     *mwire.MachineRecord
			Permissions machinePermissions
		}{current, permissions}
	default:
		http.Error(w, "unknown pairing action", 400)
		return
	}
	// Invitation and private preparation arguments never enter action logs.
	if err != nil {
		http.Error(w, "pairing reply unavailable; inspect the saved operation before starting another attempt", 503)
		return
	}
	panelJSON(w, result)
}

var machinesPage = oaPageHead("machines", "Paired PCs", "Manage your paired computers.") + `
<section id="pair-connections" data-runtime-echo>
<p>Paired computers can use the access you approve on each computer.</p>
<button id="pair-add" type="button">Add computer</button><button id="pair-refresh" type="button">Refresh</button>
<p id="pair-list-status" role="status"></p>
<p id="pair-empty" hidden>No paired computers yet. Add a computer to get started.</p>
<div id="pair-machines"></div>
<details id="pair-recovery" hidden><summary>Connections needing review</summary><div id="pair-review-rows"></div></details>
<details id="pair-list-technical" data-oa-technical><summary>Technical details</summary><p id="pair-receiving-state">Receiving state unavailable.</p></details>
</section>
<section id="pair-setup" hidden>
<button id="pair-back" type="button">Back to computers</button><h2>Add computer</h2>
<div id="pair-method"><p>Choose how to connect the other computer.</p><button id="pair-create" type="button">Create invitation</button><button id="pair-enter" type="button">Enter invitation</button></div>
<form id="pair-form" class="oa-group" hidden>
<button id="pair-change-method" type="button">Change method</button>
<p>You’ll review permissions on each computer before pairing.</p>
<label class="oa-row">Access <select id="pair-purpose" required><option value="">Choose explicitly</option><option value="none">Connect without model access</option><option value="use">Use models on the other computer</option><option value="share">Let the other computer use my models</option><option value="both">Use models on both computers</option></select></label>
<label class="oa-row">Your name for the other computer <input id="pair-binding" required maxlength="64"></label>
<label id="pair-host-row" class="oa-row" hidden>Model server to share <select id="pair-host"><option value="">Choose a registered model server</option></select></label>
<label id="pair-code-row" class="oa-row" hidden>Invitation <input id="pair-code" type="password" maxlength="74" autocomplete="off"></label>
<label id="pair-network-row" class="oa-row" hidden>Network <select id="pair-network"><option value="">Choose explicitly</option></select></label>
<details id="pair-advanced" data-oa-technical><summary>Technical details</summary>
<label id="pair-service-row" class="oa-row" hidden>Normal receiving address <select id="pair-service"><option value="">Choose explicitly</option></select><input id="pair-service-other" hidden data-oa-other maxlength="64" aria-label="Other normal receiving numeric IP and port"></label>
<label id="pair-listening" class="oa-row" hidden>Invitation listening address <select id="pair-address"><option value="">Choose explicitly</option></select><input id="pair-address-other" hidden data-oa-other maxlength="64" aria-label="Other invitation listening numeric IP and port"></label>
<p>Proposed ports are not reserved or checked. Starting creates one listener or attempts one connection. Normal and invitation addresses remain separate.</p>
</details>
<button id="pair-start" type="submit">Create invitation</button>
</form>
<div id="pair-progress" hidden><h3>Setup progress</h3><p id="pair-progress-status" role="status"></p>
<p id="pair-invitation-row" hidden>Enter this invitation on the other computer: <output id="pair-invitation" data-oa-secret></output></p>
<p>Compare the six-digit code and review both permission directions in the native windows.</p>
<button id="pair-check" type="button">Check saved attempt</button><button id="pair-cancel" type="button">Cancel attempt</button><button id="pair-new" type="button" hidden>Start another setup</button>
<details data-oa-technical><summary>Technical details</summary><pre id="pair-operation-details"></pre></details></div>
</section>
<section id="pair-detail-view" hidden><button id="pair-details-back" type="button">Back to computers</button><h2 id="pair-details-title">Computer details</h2>
<h3>Access to this computer</h3><p id="pair-permission-status" role="status"></p><ul id="pair-permissions"></ul>
<p id="pair-remote-access"></p>
<details data-oa-technical><summary>Technical details</summary><p>Paired records a trust registration, not a live connection. Last contact is observed only since this runtime started.</p><pre id="pair-details"></pre></details></section>
<script>
(function(){
const el=id=>document.getElementById(id),key=new URL(location.href).searchParams.get('k');
let page=null,choices={Hosts:[],Addresses:[]},method='',operation='',invitation='',phase='',terminal=false,checking=false,starting=false,timer=null,detailRun=null;
const random=new Uint32Array(3);crypto.getRandomValues(random);el('pair-binding').value='other-pc';
// bindingDefault keeps the suggested name readable and free: "other-pc",
// then "other-pc-2" and so on past names already paired. A name the person
// typed stays.
let bindingSuggested='other-pc';
function bindingDefault(taken){if(el('pair-binding').value!==bindingSuggested)return;let name='other-pc';for(let n=2;taken.has(name);n++)name='other-pc-'+n;bindingSuggested=name;el('pair-binding').value=name}
const normalPort=49152+(random[1]&16383),invitationPort=49152+((random[2]&16383)===(random[1]&16383)?((random[2]+1)&16383):(random[2]&16383));
async function call(body,signal){const reply=await fetch('/machines/control',{signal,method:'POST',headers:{'X-Panel-Key':key,'Content-Type':'application/json'},body:JSON.stringify(body)});if(!reply.ok)throw Error('The runtime did not return a usable reply.');return reply.json()}
function ready(){if(typeof oaReady==='function')oaReady()}
function node(tag,text){const item=document.createElement(tag);if(text!==undefined)item.textContent=text;return item}
function stopDetails(){if(detailRun)detailRun.abort();detailRun=null;clearPermissions()}
function visible(view){if(view!=='pair-detail-view')stopDetails();for(const id of ['pair-connections','pair-setup','pair-detail-view'])el(id).hidden=id!==view;if(view!=='pair-setup'){clearTimeout(timer);timer=null}}
// BindingName is the name this runtime reports. The pairing API does not
// publish an unpaired computer's hostname; a key is never a display name.
function label(pc){return pc.BindingName||'Unnamed computer'}
function contact(pc){const at=Number.isSafeInteger(pc.LastCallUnixMs)&&pc.LastCallUnixMs>0?new Date(pc.LastCallUnixMs):null;return at&&Number.isFinite(at.getTime())?'Last contact: '+at.toLocaleString():'No contact observed since OA started'}
function row(pc,revision){const box=node('div');box.className='oa-row';const text=node('div');text.className='oa-row-text';text.append(node('strong',label(pc)),node('span',pc.State==='active'?'Paired · '+contact(pc):pc.State==='removing'?'Removal pending':'Needs review'));
const controls=node('div');controls.className='oa-row-control';const details=node('button','Details');details.type='button';details.onclick=()=>openDetails(pc).catch(()=>{el('pair-permission-status').textContent='Saved permissions are unavailable. Try again later.'});const remove=node('button','Remove');remove.type='button';remove.onclick=()=>removeComputer(pc.PeerKey,revision);controls.append(details,remove);box.append(text,controls);return box}
async function refresh(){el('pair-receiving-state').textContent='Receiving state unavailable.';const rows=el('pair-machines'),recovery=el('pair-review-rows');rows.replaceChildren();recovery.replaceChildren();el('pair-empty').hidden=true;el('pair-recovery').hidden=true;let cursor='',revision='',count=0;const taken=new Set();
for(let n=0;n<4;n++){const next=await call({action:'list',cursor});if(next.Outcome!=='page'||revision&&revision!==next.Revision)throw Error('Connections unavailable');if(!page||!cursor)page=next;revision=next.Revision;for(const pc of next.Machines||[]){(pc.State==='active'?rows:recovery).append(row(pc,revision));if(pc.BindingName)taken.add(pc.BindingName);count++}cursor=next.NextCursor||'';if(!cursor)break}bindingDefault(taken);
el('pair-empty').hidden=count!==0||!!cursor;el('pair-recovery').hidden=recovery.children.length===0;el('pair-list-status').textContent=cursor?'Only part of the connection list is available. Refresh before making changes.':'';el('pair-receiving-state').textContent=typeof page.LocalServiceAddress!=='string'?'No receiving address reported.':page.LocalServiceAddress?'Receiving address: '+page.LocalServiceAddress:'Receiving connections: Off';addressOptions();ready()}
async function refreshSafe(){try{await refresh()}catch(e){el('pair-machines').replaceChildren();el('pair-review-rows').replaceChildren();el('pair-empty').hidden=true;el('pair-recovery').hidden=true;el('pair-receiving-state').textContent='Receiving state unavailable.';el('pair-list-status').textContent='Paired computers are unavailable. Try Refresh.'}}
async function removeComputer(peer,revision){try{const reply=await call({action:'remove',peer,revision});await refreshSafe();el('pair-list-status').textContent=reply.Outcome==='removed'?'Computer removed.':reply.Outcome==='cleanup_pending'?'Trust disabled. Permission cleanup is still pending.':'Removal was not completed. Refresh and review the connection.'}catch(e){el('pair-list-status').textContent='Removal reply unavailable. Refresh to check the saved connection.'}}
function permissionTarget(rule){if(rule.Selector){const scope=rule.Selector.Scope;return scope==='all'?'All resources of this permission':scope==='one'?'One resource: '+(rule.Selector.Name||rule.Selector.Target):'Target details unavailable'}const resource=rule.Resource||'',separator=resource.indexOf(':'),kind=separator<0?'':resource.slice(0,separator),name=separator<0?resource:resource.slice(separator+1);return kind==='host'?'Model server '+name:resource==='*'?'Exact resource “*”':oaResourceLabel(resource)}
function clearPermissions(){if(detailRun)detailRun.current=false;el('pair-details').textContent='';el('pair-permissions').replaceChildren();el('pair-permission-status').textContent='Saved permissions are unavailable. Try again later.'}
async function loadDetails(pc,run){const details=await call({action:'details',peer:pc.PeerKey},run.signal);if(detailRun!==run||run.signal.aborted)return;el('pair-details').textContent=JSON.stringify(details,null,2);const current=details.Permissions||{};run.current=current.Outcome==='page'&&current.Complete;el('pair-permissions').replaceChildren();for(const rule of current.Rules||[]){const phrase=(current.ActionPlain||{})[rule.Action],name=phrase?phrase.charAt(0).toUpperCase()+phrase.slice(1):'Saved permission',state=rule.Expired?'Expired':rule.Permit?'Allowed':'Blocked';const at=rule.Expires?new Date(rule.Expires):null;const lifetime=at?(Number.isFinite(at.getTime())?(rule.Expired?'Expired ':'Until ')+at.toLocaleString():'Expiry unavailable'):'Until removed';const item=node('li');item.append(node('div',name+': '+state),node('div',permissionTarget(rule)+' · '+lifetime));el('pair-permissions').append(item)}el('pair-permission-status').textContent=current.Outcome==='page'&&current.Complete?(current.Rules.length?'Saved access is shown below.':'No permissions saved specifically for this computer.'):current.Outcome==='partial'||current.Outcome==='page'?'Some saved permissions could not be read. The list may be incomplete.':'Saved permissions are unavailable. Try again later.';ready()}
function rightsRetryPause(signal){return new Promise(resolve=>{if(signal.aborted){resolve();return}const done=()=>{clearTimeout(timer);signal.removeEventListener('abort',done);resolve()},timer=setTimeout(done,2000);signal.addEventListener('abort',done,{once:true})})}
async function followDetails(pc,run){let after='';while(detailRun===run&&!run.signal.aborted){try{const query=new URLSearchParams({after});const response=await fetch('/rights/observe?'+query,{signal:run.signal,headers:{'X-Panel-Key':key}});if(!response.ok)throw Error('Observation unavailable');const notice=await response.json();if(detailRun!==run||run.signal.aborted)return;if(notice.Outcome==='changed'||notice.Outcome==='unchanged'){after=notice.Cursor;if(notice.Outcome==='changed'||!run.current){clearPermissions();await loadDetails(pc,run)}}else{clearPermissions();await rightsRetryPause(run.signal)}}catch(e){if(detailRun!==run||run.signal.aborted)return;clearPermissions();await rightsRetryPause(run.signal)}}}
async function openDetails(pc){stopDetails();visible('pair-detail-view');el('pair-details-title').textContent=label(pc);el('pair-remote-access').textContent='To manage this computer’s access to '+label(pc)+', open permissions on '+label(pc)+'.';el('pair-permissions').replaceChildren();el('pair-permission-status').textContent='Reading saved permissions…';const run=new AbortController();detailRun=run;try{await loadDetails(pc,run)}catch(e){if(detailRun===run&&!run.signal.aborted)clearPermissions()}if(detailRun===run&&!run.signal.aborted)followDetails(pc,run)}
addEventListener('pagehide',stopDetails);
function shares(){return el('pair-purpose').value==='share'||el('pair-purpose').value==='both'}
function addressValue(id){return el(id).value==='other'?el(id+'-other').value:el(id).value}
function fill(select,values,prompt){const chosen=select.value;select.replaceChildren();for(const [value,text] of [['',prompt],...values]){const option=node('option',text);option.value=value;select.append(option)}select.value=chosen}
function join(ip,port){return(ip.includes(':')?'['+ip+']':ip)+':'+port}
function addressOptions(){const active=page&&page.LocalServiceAddress;const ip=active?active.slice(0,active.lastIndexOf(':')).replace(/^\[|\]$/g,''):'';const networks=ip?[ip]:choices.Addresses;
fill(el('pair-service'),[...(active?[[active,'Current receiver: '+active]]:networks.map(value=>[join(value,normalPort),'Proposed: '+join(value,normalPort)])),['other','Other…']],'Choose explicitly');fill(el('pair-address'),[...networks.map(value=>[join(value,invitationPort),'Proposed: '+join(value,invitationPort)]),['other','Other…']],'Choose explicitly');fill(el('pair-network'),[...networks.map(value=>[value,value]),['other','Other network…']],'Choose explicitly');if(!el('pair-network').value&&networks.length===1){el('pair-network').value=networks[0];network()}else mode()}
function network(){const ip=el('pair-network').value;if(ip==='other'){el('pair-advanced').open=true;el('pair-service').value='other';el('pair-address').value='other'}else{el('pair-service').value=ip?(page&&page.LocalServiceAddress||join(ip,normalPort)):'';el('pair-address').value=ip?join(ip,invitationPort):''}mode()}
function mode(){el('pair-host-row').hidden=!shares();el('pair-host').required=shares();el('pair-service-row').hidden=!shares();el('pair-listening').hidden=method!=='offer';el('pair-code-row').hidden=method!=='enter';el('pair-code').required=method==='enter';const needs=shares()||method==='offer';const count=page&&page.LocalServiceAddress?1:choices.Addresses.length;el('pair-network-row').hidden=!needs||count===1;el('pair-network').required=needs;for(const id of ['pair-service','pair-address'])el(id+'-other').hidden=el(id).value!=='other'}
function setup(open){if(open===true)visible('pair-setup');el('pair-add').textContent=operation&&!terminal?'Resume setup':'Add computer';el('pair-method').hidden=!!method||!!operation;el('pair-form').hidden=!method||!!operation;el('pair-progress').hidden=!operation;if(operation)schedule()}
function choose(value){method=value;if(value!=='enter')el('pair-code').value='';el('pair-start').textContent=value==='offer'?'Create invitation':'Connect';setup();mode()}
function schedule(){clearTimeout(timer);if(operation&&!terminal&&['offering','connecting','checking','awaiting_person','consent_sent','reconciling'].includes(phase)&&!el('pair-setup').hidden)timer=setTimeout(()=>check().catch(uncertain),1200)}
function progress(value){if(value.Operation){operation=value.Operation.ID;phase=value.Operation.Phase||''}if(!value.Operation&&value.Outcome!=='unchanged')phase='unknown';if(value.Code)invitation=value.Code;terminal=['paired','refused','cancelled','expired'].includes(phase);if(phase!=='offering')invitation='';const debug={...value};delete debug.Code;delete debug.code;el('pair-operation-details').textContent=JSON.stringify(debug,null,2);const words={offering:'Enter the invitation on the other computer.',connecting:'Connecting to the other computer…',checking:'Checking the connection…',awaiting_person:'Review the permissions in the native window on each computer.',consent_sent:'Waiting for the other computer.',reconciling:'Saving the approved connection…',paired:'Computer paired.',refused:'The pairing attempt was refused.',cancelled:'Attempt cancelled.',expired:'The invitation expired.',uncertain:'The outcome needs review. Check the saved attempt and the connection list.'};el('pair-progress-status').textContent=words[phase]||'Saved attempt unavailable. Review the connections before starting again.';el('pair-invitation').textContent=invitation;el('pair-invitation-row').hidden=!invitation;el('pair-new').hidden=!terminal;setup();if(terminal)refreshSafe()}
function uncertain(){terminal=false;invitation='';el('pair-invitation').textContent='';el('pair-invitation-row').hidden=true;el('pair-progress-status').textContent='The reply was lost or unavailable. Check this saved attempt; closing setup does not cancel or retry it.';el('pair-new').hidden=true;clearTimeout(timer)}
async function check(){if(!operation||checking)return;checking=true;try{progress(await call({action:'observe',operation}));if(phase==='uncertain')clearTimeout(timer)}finally{checking=false}}
el('pair-form').onsubmit=async event=>{event.preventDefault();if(operation||starting)return;starting=true;el('pair-start').disabled=true;const purpose=el('pair-purpose').value;const offer=purpose==='share'||purpose==='both';const requestModels=purpose==='use'||purpose==='both';const frozen={action:method,preparation:{Request:purpose?(requestModels?'one_host_inference':'none'):'',Offer:purpose?(offer?'one_host_inference':'none'):'',OfferHost:offer?el('pair-host').value:'',NormalServiceAddress:offer?addressValue('pair-service'):'',BindingName:el('pair-binding').value},address:method==='offer'?addressValue('pair-address'):'',code:method==='enter'?el('pair-code').value:''};el('pair-code').value='';try{const latest=await call({action:'list'});if(latest.Outcome!=='page')throw Error('Preparation unavailable');const nonce=Array.from(crypto.getRandomValues(new Uint8Array(16)),value=>value.toString(16).padStart(2,'0')).join('');frozen.request={Epoch:latest.Epoch,ExpiresMs:latest.RequestExpiresMs,Nonce:nonce};operation=latest.Epoch+'.'+nonce;terminal=false;phase=frozen.action==='offer'?'offering':'connecting';setup();el('pair-progress-status').textContent='Starting setup…';progress(await call(frozen))}catch(e){if(operation)uncertain();else el('pair-list-status').textContent='Setup is unavailable. Refresh the connection list before trying again.'}finally{starting=false;el('pair-start').disabled=false}};
el('pair-add').onclick=()=>{if(terminal){operation='';invitation='';phase='';method='';terminal=false;el('pair-purpose').value=''}setup(true)};el('pair-change-method').onclick=()=>{if(!operation&&!starting){method='';setup()}};el('pair-create').onclick=()=>choose('offer');el('pair-enter').onclick=()=>choose('enter');el('pair-back').onclick=()=>{visible('pair-connections');refreshSafe()};el('pair-details-back').onclick=()=>visible('pair-connections');el('pair-refresh').onclick=refreshSafe;el('pair-purpose').onchange=mode;el('pair-network').onchange=network;for(const id of ['pair-service','pair-address'])el(id).onchange=mode;
el('pair-check').onclick=()=>check().catch(uncertain);el('pair-cancel').onclick=async()=>{if(!operation)return;try{progress(await call({action:'cancel',operation}))}catch(e){uncertain()}};el('pair-new').onclick=()=>{if(!terminal)return;operation='';invitation='';phase='';method='';terminal=false;el('pair-purpose').value='';setup()};
(async()=>{choices=await call({action:'choices'});fill(el('pair-host'),choices.Hosts.map(name=>[name,name]),'Choose a registered model server');await refreshSafe()})().catch(()=>{el('pair-list-status').textContent='Setup choices are unavailable. Existing connections can still be refreshed.';refreshSafe()});
if(typeof oaOnRuntimeBack==='function')oaOnRuntimeBack(refreshSafe);
})();</script>`
