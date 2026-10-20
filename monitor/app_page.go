package main

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// The app page (research/panel-redesign/SPEC.md §4, S2): one registered
// application at /app/<registered name>, opened from a row of the Apps
// list. It is generic: every word it shows about the app comes from the
// app's registry entry, escaped as untrusted text, and nothing here names an
// app. Sections: Models it uses, Permissions, Answering for you (only while
// the app is the assistant), Recent activity, Start and Remove.

// appPagePrefix is the path every app page and its two actions live under.
const appPagePrefix = "/app/"

// appPageEntry finds the registered entry named name. readable is false when
// the Panel cannot list the registry.
func appPageEntry(ctx context.Context, apps setupApplications, name string) (entry *fwire.ApplicationEntry, readable bool) {
	page, err := apps.Observe(ctx, "", 0)
	if err != nil || page.Outcome != fwire.ApplicationOutcomePage && page.Outcome != fwire.ApplicationOutcomeStale {
		return nil, false
	}
	for _, listed := range page.Applications {
		if listed.Descriptor.Name == name && listed.Scope == fwire.ScopeLocal {
			found := listed
			return &found, true
		}
	}
	return nil, true
}

// appPageTitle is the name the page shows for an entry: its registered
// title, else its registered name. A title that is one of the product names
// (reservedTitle) reads as the program's stem and "(registered app)", so a
// registered app never reads as OpenAbstractions.
func appPageTitle(d fwire.ApplicationDescriptor) string {
	if reservedTitle(d.Title) {
		return programStem(d.Program) + " (registered app)"
	}
	if strings.TrimSpace(d.Title) != "" {
		return d.Title
	}
	return d.Name
}

// appEntryTitle is the one name of a registered entry wherever a person reads
// it (Apps, the app page, prompts, card holders, choosers): appPageTitle, and
// "(added itself)" while its own program registered it and the person has not
// answered Keep to the runtime's question (facade AskConfirmation).
func appEntryTitle(entry fwire.ApplicationEntry) string {
	title := appPageTitle(entry.Descriptor)
	if entry.SelfRegistered {
		title += " (added itself)"
	}
	return title
}

// appPageRoutes serves /app/<name> (the page), /app/<name>/start,
// /app/<name>/remove and /app/<name>/facts (app_facts.go). The prefix differs from /apps/registrations, the Apps
// list's own data, so every registered name has a page.
func (p *servicePanel) appPageRoutes(mux *http.ServeMux, key string) {
	mux.HandleFunc(appPagePrefix, func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, appPagePrefix)
		name, action, _ := strings.Cut(rest, "/")
		if !validApplicationName(name) {
			http.NotFound(w, r)
			return
		}
		switch action {
		case "":
			p.appPage(key, name)(w, r)
		case "start":
			guard(key, func(w http.ResponseWriter, r *http.Request) { p.appStart(w, r, name) })(w, r)
		case "remove":
			guard(key, func(w http.ResponseWriter, r *http.Request) { p.appRemove(w, r, name) })(w, r)
		case "confirm":
			guard(key, func(w http.ResponseWriter, r *http.Request) { p.appConfirm(w, r, name) })(w, r)
		case "facts":
			guard(key, func(w http.ResponseWriter, r *http.Request) { p.appFacts(w, r, name) })(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

// appPage renders one app's page with its registry entry read on the
// server, so the page shows the app on arrival.
func (p *servicePanel) appPage(key, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("k") != key {
			http.Error(w, "panel key required", 403)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "GET required", http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := panelCall(r)
		defer cancel()
		var entry *fwire.ApplicationEntry
		readable := false
		if apps, err := p.setupApplicationsForPanel(ctx); err == nil {
			entry, readable = appPageEntry(ctx, apps, name)
		}
		account := ""
		if entry != nil {
			account = panelAccount(ctx)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		//unchecked: a failed write means the client is already gone; there is no response left to recover
		_, _ = io.WriteString(w, panelPage(r, key, "status", "", appPageFragment(key, panelSkin(r), name, entry, readable, account)))
	}
}

// appPageFragment is the page for one entry. skin decides the document
// title: the breadcrumb "Apps › <name>" on Windows and the web, the app's
// name alone on macOS, whose toolbar carries the Back button (SPEC §4).
func appPageFragment(key, skin, name string, entry *fwire.ApplicationEntry, readable bool, account string) string {
	e := html.EscapeString
	keyed := func(path string, query url.Values) string {
		if query == nil {
			query = url.Values{}
		}
		query.Set("k", key)
		return path + "?" + query.Encode()
	}
	title := "App"
	if entry != nil {
		title = appEntryTitle(*entry)
	}
	documentTitle := "Apps › " + title
	if skin == "macos" {
		documentTitle = title
	}
	appsItem, _ := navItemByID("status")
	// The page calls oaReady once its first reads finish (data-ready-owner).
	head := `<header class="oa-pagehead" data-ready-owner="page"><span class="oa-pagehead-tile oa-icon-status"><svg viewBox="0 0 24 24" aria-hidden="true">` + appsItem.Icon +
		`</svg></span><div class="oa-pagehead-text"><h1 class="oa-pagehead-title"><a href="` + e(keyed("/", nil)) + `">Apps</a> › ` + e(title) + `</h1></div></header>`
	setTitle := `<script>document.title=` + jsString(documentTitle) + `;</script>`
	back := oaRowNav("All apps", "", keyed("/", nil))
	if entry == nil {
		sentence := "This app is no longer registered."
		if !readable {
			sentence = "The list of apps is unavailable right now."
		}
		return setTitle + strings.Replace(head, ` data-ready-owner="page"`, "", 1) + oaGroup("app-missing", "This app", "", oaEmpty(sentence), back)
	}
	d := entry.Descriptor
	subject := appsRegistrationSubject(account, d.Program)
	subjectAccount, subjectProgram := "", d.Program
	if subject != nil {
		subjectAccount, subjectProgram = subject.Account, subject.Program
	}
	// data-defaults is the Permissions page's "Defaults for all apps" group,
	// where a default's own switch lives.
	data := `<div id="app-page" hidden data-name="` + e(d.Name) + `" data-title="` + e(title) + `" data-shown="` + e(applicationDigest(&d)) +
		`" data-defaults="` + e(keyed("/rights", nil)+"#defaults") + `"></div>`

	permissionsQuery := url.Values{"program": {subjectProgram}}
	if subjectAccount != "" {
		permissionsQuery.Set("account", subjectAccount)
	}
	models := oaGroup("app-models", "Models it uses", "Models this app called recently.",
		oaStatus("app-models-status"), `<div id="app-models-list"></div>`,
		oaRowNav("Choose models", "", keyed("/inference", nil)))
	permissions := oaGroup("app-permissions", "Permissions", "What this app may do.",
		oaStatus("app-permissions-status"), `<div id="app-permissions-list"></div>`,
		oaRowNav("Review picture workflow permissions", "Choose whether this app may make and save pictures for one hour.", keyed("/permission-review", url.Values{"app": {d.Name}, "feature": {"picture-workflow"}})),
		oaRowNav("All permissions for this app", "", keyed("/rights", permissionsQuery)+"#rights-section"))
	answering := strings.Replace(oaGroup("app-answering", "Answering for you", "This app answers routine questions from your apps.",
		oaRow("Answers for you", "", `<button id="app-answering-revoke">Revoke answering access</button>`, "app-answering-status"),
		`<div id="app-answered-list"></div>`), `class="oa-group"`, `class="oa-group" hidden`, 1)
	activity := oaGroup("app-activity", "Recent activity", "What this app did recently.",
		oaStatus("app-activity-status"), `<div id="app-activity-list"></div>`,
		`<button id="app-activity-more" hidden>Show more</button>`)
	var start string
	switch {
	case d.Activation != nil:
		caption := "Not running."
		if len(entry.Instances) > 0 {
			caption = "Running now."
		}
		start = oaRow("Start "+title, caption, `<button id="app-start" class="oa-primary">Start</button>`, "app-start-status")
	case strings.TrimSpace(d.StartGuidance) != "":
		start = oaRow("Start "+title, d.StartGuidance, "", "")
	default:
		start = oaRow("Start "+title, "Start it from its own shortcut or menu.", "", "")
	}
	remove := oaRow("Remove from OpenAbstractions", "Its files stay until its uninstaller removes them.",
		`<button id="app-remove">Remove…</button>`, "app-remove-status")
	// An entry its own program registered reads "(added itself)" until the
	// person keeps it in the runtime's own prompt; Confirm… shows that prompt
	// again.
	confirm := ""
	if entry.SelfRegistered {
		confirm = oaGroup("app-confirm-section", "Added itself", "This app added itself to OpenAbstractions.",
			oaRow("Keep "+appPageTitle(d)+"?", "", `<button id="app-confirm">Confirm…</button>`, "app-confirm-status"))
	}
	return setTitle + head + data + confirm + models + permissions + answering + activity +
		oaGroup("app-start-section", "Start", "", start) +
		oaGroup("app-remove-section", "Remove", "", remove) +
		`<script>` + serviceCommonScript + appPageScript + `</script>`
}

// jsString is s as a JavaScript string literal safe inside a script element:
// encoding/json escapes <, >, & and the two line separators.
func jsString(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// appPageScript fills the app page from /app/<name>/facts (app_facts.go),
// which the server filters by program identity: this app's model calls, its
// saved permissions and whether it answers for the person. Answers it gave
// come from /rights/answered?app=<name>. Edits go through the existing
// /rights and /rights/assistant handlers; Start and Remove through the
// page's own. Rows are kept by key and updated in place, so a reread keeps a
// row's status sentence and focus, and passes run one at a time. Every
// app-supplied or recorded text is set as text, never as markup.
const appPageScript = `
const appData=el('app-page').dataset;
const appSubject={name:appData.name,title:appData.title,shown:appData.shown};
const appPath='/app/'+encodeURIComponent(appSubject.name);
const appPageSize=5;
function appStatusLine(){const s=document.createElement('p');s.className='oa-status';s.setAttribute('role','status');s.setAttribute('aria-live','polite');s.setAttribute('data-state','idle');return s}
function appRowSay(row,state,sentence){if(!row||!row.status)return;row.status.textContent=sentence;row.status.setAttribute('data-state',state)}
function appWhen(ms){return ms?oaLocalTime(ms):''}
function appOutcomeWord(outcome){return outcome==='completed'?'Completed':capitalize(String(outcome||'').replace(/_/g,' '))}
// appRow is one oa-row: label, caption, controls and, when asked, its own
// status line. fill updates its texts in place.
function appRow(controls,withStatus){const row=document.createElement('div');row.className='oa-row';const text=document.createElement('div');text.className='oa-row-text';row.label=document.createElement('span');row.label.className='oa-row-label';row.caption=document.createElement('span');row.caption.className='oa-row-caption';text.append(row.label,row.caption);const control=document.createElement('div');control.className='oa-row-control';for(const c of controls||[])control.append(c);row.append(text,control);if(withStatus){row.status=appStatusLine();row.append(row.status)}return row}
function appFill(row,label,caption){if(row.label.textContent!==label)row.label.textContent=label;if(row.caption.textContent!==caption)row.caption.textContent=caption;row.caption.hidden=!caption}
// appList keeps one list's rows by key: new keys get a row, gone keys lose
// theirs, the rest are updated in place. An empty list shows one sentence.
function appList(listId,state,items,keyOf,make,fill,empty){
 const out=el(listId),next=new Map();
 const rows=items.map(item=>{const k=keyOf(item);let row=state.rows.get(k);if(!row)row=make(item);fill(row,item);next.set(k,row);return row});
 const same=state.drawn&&rows.length===state.order.length&&rows.every((row,i)=>state.order[i]===row);
 state.rows=next;state.order=rows;
 if(same)return;
 state.drawn=true;out.replaceChildren();
 if(!rows.length){const p=document.createElement('p');p.className='oa-empty';p.textContent=empty;out.append(p);return}
 for(const row of rows)out.append(row);
}
const appModelRows={rows:new Map(),order:[],drawn:false},appActivityRows={rows:new Map(),order:[],drawn:false},appRuleRows={rows:new Map(),order:[],drawn:false},appAnswerRows={rows:new Map(),order:[],drawn:false};
let appCalls=[],appAnswers=[],appRules=[],appDefaults=[],appRuleSubject=null,appScriptHost=false,appRevision='',appPlain={},appRulesAvailable=false,appAssistant=null,appAssistantRevision='',appAnsweringRevoked=false,appActivityAll=false;
const appFailed={models:true,permissions:true};
// Models it uses: one row per model, most recently used first.
function renderAppModels(){
 const models=[],seen=new Set();for(const c of appCalls){const name=oaModelName(c.model);if(!seen.has(name)){seen.add(name);models.push(c)}}
 appList('app-models-list',appModelRows,models,c=>oaModelName(c.model),()=>appRow([],false),(row,c)=>appFill(row,oaModelName(c.model),'Last used '+appWhen(c.unixMs)+' on '+(c.server||'Model server')),'This app has not used a model yet.');
}
// Recent activity: model calls and the answers it gave, newest first, a
// page at a time.
function appActivityItems(){const items=appCalls.map(c=>({key:'call:'+c.unixMs+':'+c.model,ms:c.unixMs,label:'Used '+oaModelName(c.model),caption:appWhen(c.unixMs)+' · '+appOutcomeWord(c.outcome)}));for(const a of appAnswers){const ms=a.answered?Date.parse(a.answered):0;items.push({key:'answer:'+a.id,ms,label:a.sentence,caption:(a.kind==='choice'?'Chose '+(a.optionLabel||a.option)+' for you':a.option==='allow'?'Allowed for you':'Not now for you')+(ms?' · '+appWhen(ms):'')})}return items.sort((a,b)=>b.ms-a.ms)}
function renderAppActivity(){const items=appActivityItems();appList('app-activity-list',appActivityRows,appActivityAll?items:items.slice(0,appPageSize),i=>i.key,()=>appRow([],false),(row,i)=>appFill(row,i.label,i.caption),'Nothing recent from this app.');el('app-activity-more').hidden=appActivityAll||items.length<=appPageSize}
el('app-activity-more').onclick=()=>{appActivityAll=true;renderAppActivity();oaSetStatus('app-activity-status','ok','Showing all '+appActivityItems().length+' recent events.')};
// Permissions: what this app may do today. Its own rules, each with a switch
// (off blocks it) and Remove; then each default for all apps that applies to
// it (rights RIGHTS-R22), with Block, which saves this app's own block, and a
// link to the default's switch. A script host receives no default; one row
// says so.
const appRightsText={forbidden:'The runtime did not permit this Panel to change permissions; nothing was changed.',conflict:'Permissions changed meanwhile; nothing was changed. Try again.',unavailable:'The permission service is unavailable; nothing was changed.',invalid:'This permission is changed on Permissions.'};
function appRulePhrase(r){const phrase=appPlain[r.Action];return phrase?capitalize(phrase):oaActionFallback(r.Action)}
function appRuleKey(r){return 'rule:'+JSON.stringify([r.Subject.Account,r.Action,r.Resource])}
function appRuleCaption(r){const target=/^sha256:/i.test(r.Resource)?'Stored content':oaResourceLabel(r.Resource);const expiry=r.Expired?'Expired':r.Expires?'Until '+oaLocalTime(r.Expires):'';return [target,expiry].filter(Boolean).join(' · ')}
function appPermissionRow(){
 const state=document.createElement('span'),toggle=document.createElement('input'),remove=document.createElement('button');toggle.type='checkbox';toggle.className='switch';remove.textContent='Remove';
 const row=appRow([state,toggle,remove],true);row.state=state;row.toggle=toggle;row.remove=remove;
 toggle.onchange=()=>{const r=row.rule,permit=!!toggle.checked,edit={edit:'set',permit};if(r.Expires&&!r.Expired){const left=Date.parse(r.Expires)-Date.now();if(left>0)edit.ttl_ms=Math.max(1000,Math.round(left))}return appRuleEdit(r,edit,row,appRulePhrase(r)+(permit?' is allowed.':' is blocked.'))};
 remove.onclick=()=>appRuleEdit(row.rule,{edit:'revoke'},row,appRulePhrase(row.rule)+' was removed.');
 return row;
}
function appFillPermission(row,r){row.rule=r;appFill(row,appRulePhrase(r),appRuleCaption(r));const word=r.Permit?'Allowed':'Blocked';if(row.state.textContent!==word)row.state.textContent=word;row.toggle.checked=!!r.Permit;row.toggle.setAttribute('aria-label','Allow '+appRulePhrase(r));row.remove.setAttribute('aria-label','Remove '+appRulePhrase(r))}
function appDefaultRow(){
 const state=document.createElement('span'),block=document.createElement('button'),all=document.createElement('a');block.textContent='Block';all.textContent='Change for all apps';all.href=appData.defaults||'';
 const row=appRow([state,block,all],true);row.state=state;row.block=block;
 block.onclick=()=>{const d=row.d;if(!appRuleSubject){appRowSay(row,'error','This app cannot be blocked here; nothing was changed.');return}
  return appRuleEdit({Subject:appRuleSubject,Action:d.action,Resource:d.resource},{edit:'set',permit:false},row,d.label+' is blocked for '+appSubject.title+'.')};
 return row;
}
function appFillDefault(row,d){row.d=d;appFill(row,d.label,'Allowed for all apps');if(row.state.textContent!=='Allowed')row.state.textContent='Allowed';row.block.setAttribute('aria-label','Block '+d.label+' for '+appSubject.title)}
function appPermissionItems(){const items=appRules.map(r=>({kind:'rule',key:appRuleKey(r),rule:r}));for(const d of appDefaults)items.push({kind:'default',key:'default:'+d.name,d});if(appScriptHost)items.push({kind:'note',key:'note:script-host'});return items}
function renderAppPermissions(){appList('app-permissions-list',appRuleRows,appPermissionItems(),i=>i.key,
 i=>i.kind==='rule'?appPermissionRow():i.kind==='default'?appDefaultRow():appRow([],false),
 (row,i)=>i.kind==='rule'?appFillPermission(row,i.rule):i.kind==='default'?appFillDefault(row,i.d):appFill(row,'Defaults for all apps','They never apply to this app: it runs scripts.'),
 'This app has no permissions.')}
async function appRuleEdit(r,edit,row,sentence){
 if(!appRulesAvailable){appRowSay(row,'error','The permission list is unavailable; nothing was changed.');return}
 appRowSay(row,'loading','Saving…');
 try{const x=await call('/rights',{...edit,revision:appRevision,account:r.Subject.Account,program:r.Subject.Program,action:r.Action,resource:r.Resource});
  await appFacts();
  const now=appRuleRows.rows.get(appRuleKey(r)),line=x.Outcome==='applied'?sentence:appRightsText[x.Outcome]||'The permission was not changed.',state=x.Outcome==='applied'?'ok':'error';
  if(now)appRowSay(now,state,line);else oaSetStatus('app-permissions-status',state,line);
 }catch(e){appRowSay(row,'error',oaPlain(e&&e.message||e).line)}}
// Answering for you: shown while this app answers for the person, and after
// a revoke here so its sentence stays readable.
function renderAppAnswering(){
 el('app-answering').hidden=!(appAssistant||appAnsweringRevoked);
 el('app-answering-revoke').disabled=!appAssistant;
 appList('app-answered-list',appAnswerRows,appAnswers.slice(0,appPageSize),a=>a.id,()=>appRow([],false),(row,a)=>appFill(row,a.sentence,(a.kind==='choice'?'Chose '+(a.optionLabel||a.option):a.option==='allow'?'Allowed':'Not now')+(a.answered?' · '+appWhen(Date.parse(a.answered)):'')),'It has not answered a question yet.');
}
el('app-answering-revoke').onclick=async()=>{if(!appAssistant){oaSetStatus('app-answering-status','idle','This app does not answer for you.');return}
 try{const r=await call('/rights/assistant',{action:'stop',program:appAssistant.program,revision:appAssistantRevision,alsoRevokeGrants:false});if(r.outcome==='applied')appAnsweringRevoked=true;oaSetStatus('app-answering-status',r.outcome==='applied'?'ok':'error',r.message);await appFacts()}catch(e){oaShowError('app-answering-status',e)}};
// One facts pass at a time. A part's status line is written when its read
// fails and cleared only when it recovers.
let appFactsChain=Promise.resolve();
function appFacts(){const run=appFactsChain.then(appFactsPass);appFactsChain=run.catch(()=>{});return run}
function appPartFailed(part,id,sentence){appFailed[part]=true;oaSetStatus(id,'error',sentence)}
function appPartRecovered(part,ids){if(!appFailed[part])return;appFailed[part]=false;for(const id of ids)oaSetStatus(id,'idle','')}
async function appFactsPass(){
 let f;
 try{f=await call(appPath+'/facts')}catch(e){appRulesAvailable=false;appPartFailed('models','app-models-status',oaPlain(e&&e.message||e).line);appPartFailed('permissions','app-permissions-status',oaPlain(e&&e.message||e).line);return}
 if(f.outcome==='missing'){appRulesAvailable=false;appPartFailed('permissions','app-permissions-status','This app is no longer registered.');return}
 if(f.rulesOutcome==='page'){appRevision=f.revision;appPlain=f.actionPlain||{};appRules=f.rules||[];appDefaults=f.defaults||[];appRuleSubject=f.subject||null;appScriptHost=!!f.scriptHost;appRulesAvailable=true;appPartRecovered('permissions',['app-permissions-status']);renderAppPermissions()}
 else{appRulesAvailable=false;appPartFailed('permissions','app-permissions-status',f.rulesOutcome==='forbidden'?'This Panel may not read permissions.':'Permissions are unavailable right now.')}
 if(f.callsOutcome==='page'){appCalls=f.calls||[];appPartRecovered('models',['app-models-status','app-activity-status'])}
 else{appPartFailed('models','app-models-status',f.callsOutcome==='forbidden'?'This Panel may not read model calls.':'Model calls are unavailable right now.')}
 appAssistant=f.assistant||null;appAssistantRevision=f.assistantRevision||'';
 if(appAssistant||appAnswers.length){try{const p=await call('/rights/answered?app='+encodeURIComponent(appSubject.name));if(p.outcome==='page')appAnswers=p.answers||[]}catch(e){}}
 renderAppModels();renderAppActivity();renderAppAnswering();
}
// Saved permissions follow the rights observer; the facts are read again on
// a change, and every ten seconds while the page is visible.
function appPause(signal,ms){return new Promise(resolve=>{if(signal.aborted){resolve();return}const done=()=>{clearTimeout(timer);signal.removeEventListener('abort',done);resolve()},timer=setTimeout(done,ms);signal.addEventListener('abort',done,{once:true})})}
async function appFollowRights(run){while(!run.signal.aborted){try{if(!appRulesAvailable){await appPause(run.signal,5000);if(run.signal.aborted)return;await appFacts();continue}const o=await call('/rights/observe?'+new URLSearchParams({revision:appRevision}),undefined,run.signal);if(run.signal.aborted)return;if(o.Outcome==='changed')await appFacts();else if(o.Outcome!=='unchanged')await appPause(run.signal,5000)}catch(e){if(run.signal.aborted)return;await appPause(run.signal,5000)}}}
// Start and Remove act on the registry entry this page showed.
async function appAction(action){return call(appPath+'/'+action,{confirm:true,shown:appSubject.shown})}
const appStart=el('app-start');
if(appStart)appStart.onclick=async()=>{appStart.disabled=true;oaSetStatus('app-start-status','loading','Starting '+appSubject.title+'…');try{const r=await appAction('start');oaSetStatus('app-start-status',r.outcome==='ready'||r.outcome==='not_ready'?'ok':'error',r.message)}catch(e){oaShowError('app-start-status',e)}appStart.disabled=false};
const appConfirm=el('app-confirm');
if(appConfirm)appConfirm.onclick=async()=>{appConfirm.disabled=true;try{const r=await appAction('confirm');oaSetStatus('app-confirm-status','ok',r.message)}catch(e){oaShowError('app-confirm-status',e)}appConfirm.disabled=false};
let appRemoveArmed=false;
el('app-remove').onclick=async()=>{const button=el('app-remove');
 if(!appRemoveArmed){appRemoveArmed=true;button.textContent='Remove '+appSubject.title;oaSetStatus('app-remove-status','idle','Select Remove '+appSubject.title+' again to remove it.');return}
 button.disabled=true;oaSetStatus('app-remove-status','loading','Removing '+appSubject.title+'…');
 try{const r=await appAction('remove');oaSetStatus('app-remove-status','ok',r.message);if(appStart)appStart.disabled=true}catch(e){appRemoveArmed=false;button.disabled=false;button.textContent='Remove…';oaShowError('app-remove-status',e)}};
var appFactsReady=appFacts();
// The screenshot tool waits for data-ready once the first reads finished.
var appArrived=appFactsReady.then(()=>{if(typeof oaReady==='function')oaReady()});
const appRun=typeof AbortController==='undefined'?null:new AbortController();
var appWatchReady=appRun?appFactsReady.then(()=>appFollowRights(appRun)):Promise.resolve();
if(typeof window!=='undefined'&&typeof window.addEventListener==='function')window.addEventListener('pagehide',()=>{if(appRun)appRun.abort()});
if(typeof document.addEventListener==='function'){const tick=()=>setTimeout(async()=>{if(document.visibilityState!=='hidden')await appFacts();tick()},10000);tick();document.addEventListener('visibilitychange',()=>{if(document.visibilityState!=='hidden')appFacts()})}
`

type appActionBody struct {
	Confirm bool   `json:"confirm"`
	Shown   string `json:"shown"`
}

type appActionReply struct {
	Outcome string `json:"outcome"`
	Message string `json:"message"`
}

func decodeAppAction(w http.ResponseWriter, r *http.Request) (appActionBody, bool) {
	var body appActionBody
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return body, false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	var extra any
	if decoder.Decode(&body) != nil || decoder.Decode(&extra) != io.EOF || !body.Confirm {
		http.Error(w, "one confirmed request is required", http.StatusBadRequest)
		return body, false
	}
	return body, true
}

// appStart starts a registered app through the registry's own activation
// recipe after the person's click. The runtime owns the launch and its
// permission; the Panel adds nothing to the recipe.
func (p *servicePanel) appStart(w http.ResponseWriter, r *http.Request, name string) {
	body, ok := decodeAppAction(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	entry, _ := appPageEntry(ctx, apps, name)
	if entry == nil || applicationDigest(&entry.Descriptor) != body.Shown {
		http.Error(w, "the registered entry changed since this page was shown; reload and review it again", http.StatusConflict)
		return
	}
	p.logAction(ctx, "application.activate", map[string]string{"application.name": name})
	result, err := apps.Activate(ctx, name)
	if err != nil {
		panelError(w, err)
		return
	}
	title := appEntryTitle(*entry)
	reply := appActionReply{Outcome: result.Outcome.String()}
	switch result.Outcome {
	case fwire.ApplicationActivationOutcomeReady:
		reply.Message = title + " is running."
	case fwire.ApplicationActivationOutcomeNotReady:
		reply.Message = title + " is starting."
	case fwire.ApplicationActivationOutcomeDisabled:
		reply.Message = "OpenAbstractions cannot start " + title + "; start it from its own shortcut."
	case fwire.ApplicationActivationOutcomeForbidden:
		reply.Message = "The runtime did not permit this Panel to start " + title + "."
	default:
		reply.Message = title + " did not start."
	}
	panelJSON(w, reply)
}

// confirmingApplications is the Applications client's AskConfirmation, which
// a test directory may not have.
type confirmingApplications interface {
	AskConfirmation(context.Context, string) (fwire.ApplicationChange, error)
}

// appConfirm has the runtime ask the person again, in its own native prompt,
// whether to keep an entry its program registered itself (facade
// AskConfirmation). The Panel answers nothing itself: the prompt's Keep
// confirms the entry and its Remove removes and blocks it.
func (p *servicePanel) appConfirm(w http.ResponseWriter, r *http.Request, name string) {
	body, ok := decodeAppAction(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	entry, _ := appPageEntry(ctx, apps, name)
	if entry == nil || applicationDigest(&entry.Descriptor) != body.Shown {
		http.Error(w, "the registered entry changed since this page was shown; reload and review it again", http.StatusConflict)
		return
	}
	asking, ok := apps.(confirmingApplications)
	if !ok {
		http.Error(w, "this runtime cannot ask about an app that added itself", http.StatusConflict)
		return
	}
	p.logAction(ctx, "application.ask_confirmation", map[string]string{"application.name": name})
	change, err := asking.AskConfirmation(ctx, name)
	if err != nil {
		panelError(w, err)
		return
	}
	title := appPageTitle(entry.Descriptor)
	switch change.Outcome {
	case fwire.ApplicationOutcomeApplied:
		message := "OpenAbstractions asks you in its own window whether to keep " + title + "."
		if !entry.SelfRegistered {
			message = title + " is already confirmed."
		}
		panelJSON(w, appActionReply{Outcome: "applied", Message: message})
	case fwire.ApplicationOutcomeUnknown:
		panelJSON(w, appActionReply{Outcome: "unknown", Message: title + " was already removed."})
	default:
		http.Error(w, "OpenAbstractions could not ask about "+title+" ("+change.Outcome.String()+")", http.StatusConflict)
	}
}

// appRemove removes a registered app's entry after the person's second
// click. Removing the entry changes no file and grants nothing.
func (p *servicePanel) appRemove(w http.ResponseWriter, r *http.Request, name string) {
	body, ok := decodeAppAction(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	entry, _ := appPageEntry(ctx, apps, name)
	if entry == nil || applicationDigest(&entry.Descriptor) != body.Shown {
		http.Error(w, "the registered entry changed since this page was shown; reload and review it again", http.StatusConflict)
		return
	}
	p.logAction(ctx, "application.remove", map[string]string{"application.name": name})
	change, err := apps.Remove(ctx, name)
	if err != nil {
		panelError(w, err)
		return
	}
	title := appEntryTitle(*entry)
	switch change.Outcome {
	case fwire.ApplicationOutcomeApplied:
		panelJSON(w, appActionReply{Outcome: "applied", Message: "Removed " + title + " from OpenAbstractions."})
	case fwire.ApplicationOutcomeUnknown:
		panelJSON(w, appActionReply{Outcome: "unknown", Message: title + " was already removed."})
	default:
		http.Error(w, "the application was not removed ("+change.Outcome.String()+")", http.StatusConflict)
	}
}
