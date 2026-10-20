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
	identity "github.com/openabstractions/abstraction-identity"
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
	if err != nil || page.Outcome != fwire.ApplicationOutcomePage {
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

// appPageNative is the native applications@3 owner the app page asks first;
// a test replaces it.
var appPageNative = func(p *servicePanel, ctx context.Context, name string) (setupApplications, error) {
	return p.nativeApplicationsForPanel(ctx, name)
}

// appPageApplications finds the entry named name and the client that acts on
// it. An app registered through its signed manifest is read, started and
// removed through the native applications@3 owner. Every other entry comes
// from the directory the Apps list reads (applications@2), so each row of
// Apps opens its own page. Absence is readable only after both owners answered
// a current page; a stale or unavailable owner cannot prove removal. err is
// set when the applications@2 directory cannot be resolved.
func (p *servicePanel) appPageApplications(ctx context.Context, name string) (apps setupApplications, entry *fwire.ApplicationEntry, readable bool, err error) {
	nativeReadable := false
	if native, nativeErr := appPageNative(p, ctx, name); nativeErr == nil {
		found, observed := appPageEntry(ctx, native, name)
		nativeReadable = observed
		if found != nil {
			return native, found, true, nil
		}
	}
	directory, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		return nil, nil, false, err
	}
	entry, readable = appPageEntry(ctx, directory, name)
	if entry == nil && !nativeReadable {
		readable = false
	}
	return directory, entry, readable, nil
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
		case "stop":
			guard(key, func(w http.ResponseWriter, r *http.Request) { p.appStop(w, r, name) })(w, r)
		case "agent":
			p.appAgentEnrollment(key, name)(w, r)
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
		_, entry, readable, _ := p.appPageApplications(ctx, name)
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
	// Open is the app's primary action, at the head's end like an app's
	// page in Windows Settings (coordinator, 2026-11-09): Open for an app
	// with a declared open verb, Running while it runs, nothing otherwise.
	// The person's press is their consent; the runtime's owned launch owns
	// the start.
	action := ""
	if entry != nil && entry.Descriptor.Activation != nil {
		if len(entry.Instances) > 0 {
			action = `<div class="oa-pagehead-action"><button id="app-open" data-status="app-open-status" disabled>Running</button></div>`
		} else {
			action = `<div class="oa-pagehead-action"><button id="app-open" class="oa-primary" data-status="app-open-status">Open</button></div>`
		}
	}
	// The page calls oaReady once its first reads finish (data-ready-owner).
	head := `<header class="oa-pagehead" data-ready-owner="page"><span class="oa-pagehead-tile oa-icon-status"><svg viewBox="0 0 24 24" aria-hidden="true">` + appsItem.Icon +
		`</svg></span><div class="oa-pagehead-text"><h1 class="oa-pagehead-title"><a href="` + e(keyed("/", nil)) + `">Apps</a> › ` + e(title) + `</h1></div>` + action + `</header>`
	setTitle := `<script>document.title=` + jsString(documentTitle) + `;</script>`
	back := oaRowNav("All apps", "", keyed("/", nil))
	if entry == nil {
		missing := oaEmpty("This app is no longer registered.")
		if !readable {
			// While OpenAbstractions is down the banner says so once.
			missing = runtimeEcho(oaEmpty("The list of apps is unavailable right now."))
		}
		return setTitle + strings.Replace(head, ` data-ready-owner="page"`, "", 1) + oaGroup("app-missing", "This app", "", missing, back)
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
	// Models it uses stays hidden until the facts show an inference need
	// (P-J1a): a model call, or a permission or default for an inference
	// action. An app without one shows no model line.
	models := strings.Replace(oaGroup("app-models", "Models it uses", "Models this app called recently.",
		oaStatus("app-models-status"), `<div id="app-models-list"></div>`,
		oaRowNav("Choose models", "", keyed("/inference", nil))), `<section id="app-models" class="oa-group">`, `<section id="app-models" class="oa-group" hidden>`, 1)
	// The declared picture package offers a review for its workflow.
	pictures := ""
	registration, _ := picturePackageIdentity()
	if d.Name == registration {
		pictures = oaRowNav("Review picture workflow permissions", "Choose whether this app may make and save pictures for one hour.", keyed("/permission-review", url.Values{"app": {d.Name}, "feature": {"picture-workflow"}}))
	}
	permissions := oaGroup("app-permissions", "Permissions", "What this app may do.",
		oaStatus("app-permissions-status"), `<div id="app-permissions-list"></div>`,
		pictures,
		oaRowNav("All permissions for this app", "", keyed("/rights", permissionsQuery)+"#rights-section"))
	answering := strings.Replace(oaGroup("app-answering", "Answering for you", "This app answers routine questions from your apps.",
		oaRow("Answers for you", "", `<button id="app-answering-revoke">Revoke answering access</button>`, "app-answering-status"),
		`<div id="app-answered-list"></div>`), `class="oa-group"`, `class="oa-group" hidden`, 1)
	activity := oaGroup("app-activity", "Recent activity", "What this app did recently.",
		oaStatus("app-activity-status"), `<div id="app-activity-list"></div>`,
		`<button id="app-activity-more" hidden>Show more</button>`)
	// An app OpenAbstractions opens has Open in the head and its sentence
	// under it; any other app keeps its own start guidance.
	opened, start := "", ""
	switch {
	case d.Activation != nil:
		opened = oaStatusLine("app-open-status", false)
	case strings.TrimSpace(d.StartGuidance) != "":
		start = oaGroup("app-start-section", "Start", "", oaRow("Start "+title, d.StartGuidance, "", ""))
	default:
		start = oaGroup("app-start-section", "Start", "", oaRow("Start "+title, "Start it from its own shortcut or menu.", "", ""))
	}
	// A server OpenAbstractions started for an app shows while it runs, with
	// Stop (app_started.go).
	running := strings.Replace(oaGroup("app-started", "Running", "",
		strings.Replace(oaRow("", "", `<button id="app-started-stop">Stop</button>`, "app-started-status"), `<span class="oa-row-label">`, `<span class="oa-row-label" id="app-started-line">`, 1)),
		`class="oa-group"`, `class="oa-group" hidden`, 1)
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
	return setTitle + head + opened + data + confirm + running + models + permissions + answering + appAgentEnrollmentLink(key, name) + activity +
		start +
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
let appCalls=[],appAnswers=[],appRules=[],appDefaults=[],appRuleSubject=null,appScriptHost=false,appSharesCard=false,appRevision='',appPlain={},appRulesAvailable=false,appAssistant=null,appAssistantRevision='',appAnsweringRevoked=false,appActivityAll=false;
const appFailed={models:true,permissions:true};
// Models it uses: one row per model, most recently used first.
// appNeedsInference is true when this app used inference or holds a rule
// or default in the rights action family abstraction.inference/. Rules carry
// Action (policyRuleView), defaults carry action.
const appInferenceAction=a=>String(a||'').startsWith('abstraction.inference/');
function appNeedsInference(){return appCalls.length>0||appRules.some(r=>appInferenceAction(r&&r.Action))||appDefaults.some(d=>appInferenceAction(d&&d.action))}
// A failed read supplies no inference evidence. Apps already known to use
// inference keep their model group and its failure sentence.
function appShowModels(){const group=document.getElementById('app-models');if(group)group.hidden=!appNeedsInference()}
function renderAppModels(){
 appShowModels();
 const models=[],seen=new Set();for(const c of appCalls){const name=oaModelName(c.model);if(!seen.has(name)){seen.add(name);models.push(c)}}
 appList('app-models-list',appModelRows,models,c=>oaModelName(c.model),()=>appRow([],false),(row,c)=>appFill(row,oaModelName(c.model),'Last used '+appWhen(c.unixMs)+' on '+(c.server||'Model server')),'This app has not used a model yet.');
}
// Recent activity: model calls and the answers it gave, newest first, a
// page at a time.
function appActivityItems(){const items=appCalls.map(c=>({key:'call:'+c.unixMs+':'+c.model,ms:c.unixMs,label:'Used '+oaModelName(c.model),caption:appWhen(c.unixMs)+' · '+appOutcomeWord(c.outcome)}));for(const a of appAnswers){const ms=a.answered?Date.parse(a.answered):0;items.push({key:'answer:'+a.id,ms,label:String(a.sentence||'').replace(/[.]$/,''),caption:(a.kind==='choice'?'Chose '+(a.optionLabel||a.option)+' for you':a.option==='allow'?'Allowed for you':'Not now for you')+(ms?' · '+appWhen(ms):'')})}return items.sort((a,b)=>b.ms-a.ms)}
function renderAppActivity(){const items=appActivityItems();appList('app-activity-list',appActivityRows,appActivityAll?items:items.slice(0,appPageSize),i=>i.key,()=>appRow([],false),(row,i)=>appFill(row,i.label,i.caption),'Nothing recent from this app.');el('app-activity-more').hidden=appActivityAll||items.length<=appPageSize}
el('app-activity-more').onclick=()=>{appActivityAll=true;renderAppActivity();oaSetStatus('app-activity-status','ok','Showing all '+appActivityItems().length+' recent events.')};
// Permissions: what this app may do today. Its own rules, each with a switch
// (off blocks it) and Remove; then each default for all apps that applies to
// it (rights RIGHTS-R22), with Block, which saves this app's own block, and a
// link to the default's switch. A script host receives no default; one row
// says so.
const appRightsText={forbidden:'OpenAbstractions did not permit this Panel to change permissions; nothing was changed.',conflict:'Permissions changed meanwhile; nothing was changed. Try again.',unavailable:'The permission service is unavailable; nothing was changed.',invalid:'This permission is changed on Permissions.'};
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
function appPermissionItems(){const items=appSharesCard?[{kind:'note',key:'note:card'}]:[];items.push(...appRules.map(r=>({kind:'rule',key:appRuleKey(r),rule:r})));for(const d of appDefaults)items.push({kind:'default',key:'default:'+d.name,d});if(appScriptHost)items.push({kind:'note',key:'note:script-host'});return items}
function renderAppPermissions(){appList('app-permissions-list',appRuleRows,appPermissionItems(),i=>i.key,
 i=>i.kind==='rule'?appPermissionRow():i.kind==='default'?appDefaultRow():appRow([],false),
 (row,i)=>i.kind==='rule'?appFillPermission(row,i.rule):i.kind==='default'?appFillDefault(row,i.d):i.key==='note:card'?appFill(row,'Shares the graphics card with OpenAbstractions.',''):appFill(row,'Defaults for all apps','They never apply to this app: it runs scripts.'),
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
 appList('app-answered-list',appAnswerRows,appAnswers.slice(0,appPageSize),a=>a.id,()=>appRow([],false),(row,a)=>appFill(row,String(a.sentence||'').replace(/\.$/,''),(a.kind==='choice'?'Chose '+(a.optionLabel||a.option):a.option==='allow'?'Allowed':'Not now')+(a.answered?' · '+appWhen(Date.parse(a.answered)):'')),'It has not answered a question yet.');
}
el('app-answering-revoke').onclick=async()=>{if(!appAssistant){oaSetStatus('app-answering-status','idle','This app does not answer for you.');return}
 try{const r=await call('/rights/assistant',{action:'stop',program:appAssistant.program,revision:appAssistantRevision,alsoRevokeGrants:false});if(r.outcome==='applied')appAnsweringRevoked=true;oaSetStatus('app-answering-status',r.outcome==='applied'?'ok':'error',r.message);await appFacts()}catch(e){oaShowError('app-answering-status',e)}};
// One facts pass at a time. A part's status line is written when its read
// fails and cleared only when it recovers.
let appFactsChain=Promise.resolve();
let appPermissionsGeneration=0;
function appFacts(){const run=appFactsChain.then(appFactsPass);appFactsChain=run.catch(()=>{});return run}
function appPartFailed(part,id,sentence){appFailed[part]=true;oaSetStatus(id,'error',sentence)}
function appPartRecovered(part,ids){if(!appFailed[part])return;appFailed[part]=false;for(const id of ids)oaSetStatus(id,'idle','')}
function invalidateAppPermissions(){appPermissionsGeneration++;appRulesAvailable=false;appRevision='';appRules=[];appDefaults=[];appPlain={};appRuleSubject=null;appScriptHost=false;appSharesCard=false;appAssistant=null;appAssistantRevision='';appRuleRows.rows.clear();appRuleRows.order=[];appRuleRows.drawn=false;el('app-permissions-list').replaceChildren();appShowModels();renderAppAnswering()}
async function appFactsPass(){
 if(appRun&&appRun.signal.aborted)return;
 const readMark=appPermissionsGeneration;
 let f;
 try{f=await call(appPath+'/facts',undefined,appRun&&appRun.signal)}catch(e){if(appRun&&appRun.signal.aborted)return;invalidateAppPermissions();appPartFailed('models','app-models-status',oaPlain(e&&e.message||e).line);appPartFailed('permissions','app-permissions-status',oaPlain(e&&e.message||e).line);return}
 if((appRun&&appRun.signal.aborted)||readMark!==appPermissionsGeneration)return;
 if(f.outcome==='missing'){invalidateAppPermissions();appPartFailed('permissions','app-permissions-status','This app is no longer registered.');return}
 appShowRunning(f.running);
 appShowStarted(f.started);
 if(f.rulesOutcome==='page'){appRevision=f.revision;appPlain=f.actionPlain||{};appRules=f.rules||[];appDefaults=f.defaults||[];appRuleSubject=f.subject||null;appScriptHost=!!f.scriptHost;appSharesCard=!!f.sharesCard;appRulesAvailable=true;appPartRecovered('permissions',['app-permissions-status']);renderAppPermissions()}
 else{invalidateAppPermissions();appPartFailed('permissions','app-permissions-status',f.rulesOutcome==='forbidden'?'This Panel may not read permissions.':'Permissions are unavailable right now.')}
 if(f.callsOutcome==='page'){appCalls=f.calls||[];appPartRecovered('models',['app-models-status','app-activity-status'])}
 else{appPartFailed('models','app-models-status',f.callsOutcome==='forbidden'?'This Panel may not read model calls.':'Model calls are unavailable right now.')}
 appAssistant=appRulesAvailable?f.assistant||null:null;appAssistantRevision=appRulesAvailable?f.assistantRevision||'':'';
 if(appAssistant||appAnswers.length){try{const p=await call('/rights/answered?app='+encodeURIComponent(appSubject.name));if(p.outcome==='page')appAnswers=p.answers||[]}catch(e){}}
 renderAppModels();renderAppActivity();renderAppAnswering();
}
// Saved permissions follow the rights observer; the facts are read again on
// a change, and every ten seconds while the page is visible.
function appPause(signal,ms){return new Promise(resolve=>{if(signal.aborted){resolve();return}const done=()=>{clearTimeout(timer);signal.removeEventListener('abort',done);resolve()},timer=setTimeout(done,ms);signal.addEventListener('abort',done,{once:true})})}
async function appFollowRights(run){let after='';while(!run.signal.aborted){try{if(!appRulesAvailable){await appPause(run.signal,5000);if(run.signal.aborted)return;await appFacts();continue}const o=await call('/rights/observe?'+new URLSearchParams({after}),undefined,run.signal);if(run.signal.aborted)return;if(o.Outcome==='changed'||o.Outcome==='unchanged'){after=o.Cursor;if(o.Outcome==='changed')await appFacts()}else{invalidateAppPermissions();appPartFailed('permissions','app-permissions-status',o.Outcome==='forbidden'?'This Panel may not read permissions.':'Permissions are unavailable right now.')}}catch(e){if(run.signal.aborted)return;invalidateAppPermissions();appPartFailed('permissions','app-permissions-status','Permissions are unavailable right now.')}}}
// Start and Remove act on the registry entry this page showed.
async function appAction(action,fields={}){return call(appPath+'/'+action,{confirm:true,shown:appSubject.shown,...fields})}
// Open starts the app through the runtime's owned launch; once it runs the
// button reads Running.
const appOpen=el('app-open');
let appOpening=false,appRemoved=false;
// The head's button follows the app on every facts read: Running while it
// runs, whoever opened it, and Open again once it stops.
function appShowRunning(running){if(!appOpen||typeof running!=='boolean'||appOpening||appRemoved)return;appOpen.textContent=running?'Running':'Open';appOpen.disabled=running;appOpen.className=running?'':'oa-primary'}
if(appOpen)appOpen.onclick=async()=>{appOpening=true;appOpen.disabled=true;oaSetStatus('app-open-status','loading','Opening '+appSubject.title+'…');let running=false;try{const r=await appAction('start');running=r.outcome==='ready';oaSetStatus('app-open-status',running||r.outcome==='not_ready'?'ok':'error',r.message)}catch(e){oaShowError('app-open-status',e)}appOpening=false;if(running){appOpen.textContent='Running';appOpen.className=''}else appOpen.disabled=false};
// A server OpenAbstractions started shows its line and Stop while it runs.
const appStartedGroup=el('app-started'),appStartedLine=el('app-started-line'),appStartedStop=el('app-started-stop');
let appStopping=false,appStartedOccurrence='';
function appShowStarted(started){
 const occurrence=started&&typeof started.occurrence==='string'?started.occurrence:'';
 if(occurrence!==appStartedOccurrence)appStopping=false;
 appStartedOccurrence=occurrence;if(!appStartedGroup)return;
 appStartedGroup.hidden=!occurrence;
 if(occurrence&&appStartedLine&&appStartedLine.textContent!==started.line)appStartedLine.textContent=started.line;
 if(appStartedStop)appStartedStop.disabled=appStopping||!occurrence;
}
if(appStartedStop)appStartedStop.onclick=async()=>{
 const occurrence=appStartedOccurrence;if(!occurrence)return;
 appStopping=true;appStartedStop.disabled=true;
 try{
  const r=await appAction('stop',{start_occurrence:occurrence});
  if(occurrence!==appStartedOccurrence)return;
  oaSetStatus('app-started-status',r.outcome==='applied'?'ok':'error',r.message);
  if(r.outcome!=='applied'){appStopping=false;appStartedStop.disabled=false}
 }catch(e){if(occurrence===appStartedOccurrence){appStopping=false;appStartedStop.disabled=false;oaShowError('app-started-status',e)}}
};
// Open on Setup and in the Apps list arrives here with open=1: that press
// was the person's, so the page opens the app once and drops the flag.
var appOpenedOnArrival=Promise.resolve();
if(appOpen&&new URLSearchParams(location.search).get('open')==='1'){try{const q=new URLSearchParams(location.search);q.delete('open');if(typeof history!=='undefined'&&history.replaceState)history.replaceState(null,'',location.pathname+'?'+q)}catch(e){}if(!appOpen.disabled)appOpenedOnArrival=appOpen.onclick()}
// A step that changed nothing reads as an error with its own sentence.
const appDone=r=>r&&(r.outcome==='applied'||r.outcome==='unknown');
const appConfirm=el('app-confirm');
if(appConfirm)appConfirm.onclick=async()=>{appConfirm.disabled=true;try{const r=await appAction('confirm');oaSetStatus('app-confirm-status',appDone(r)?'ok':'error',r.message)}catch(e){oaShowError('app-confirm-status',e)}appConfirm.disabled=false};
let appRemoveArmed=false;
el('app-remove').onclick=async()=>{const button=el('app-remove');
 if(!appRemoveArmed){appRemoveArmed=true;button.textContent='Remove '+appSubject.title;oaSetStatus('app-remove-status','idle','Select Remove '+appSubject.title+' again to remove it.');return}
 button.disabled=true;oaSetStatus('app-remove-status','loading','Removing '+appSubject.title+'…');
 try{const r=await appAction('remove');if(!appDone(r)){appRemoveArmed=false;button.disabled=false;button.textContent='Remove…';oaSetStatus('app-remove-status','error',r.message);return}oaSetStatus('app-remove-status','ok',r.message);appRemoved=true;if(appOpen)appOpen.disabled=true}catch(e){appRemoveArmed=false;button.disabled=false;button.textContent='Remove…';oaShowError('app-remove-status',e)}};
var appFactsReady=appFacts();
if(typeof oaOnRuntimeBack==='function')oaOnRuntimeBack(appFacts);
// The screenshot tool waits for data-ready once the first reads finished.
var appArrived=appFactsReady.then(()=>{if(typeof oaReady==='function')oaReady()});
const appRun=typeof AbortController==='undefined'?null:new AbortController();
var appWatchReady=appRun?appFactsReady.then(()=>appFollowRights(appRun)):Promise.resolve();
if(typeof window!=='undefined'&&typeof window.addEventListener==='function')window.addEventListener('pagehide',()=>{if(appRun)appRun.abort();invalidateAppPermissions()});
if(typeof document.addEventListener==='function'){const tick=()=>setTimeout(async()=>{if(appRun&&appRun.signal.aborted)return;if(document.visibilityState!=='hidden')await appFacts();if(!appRun||!appRun.signal.aborted)tick()},10000);tick();document.addEventListener('visibilitychange',()=>{if((!appRun||!appRun.signal.aborted)&&document.visibilityState!=='hidden')appFacts()})}
`

type appActionBody struct {
	Confirm         bool   `json:"confirm"`
	Shown           string `json:"shown"`
	StartOccurrence string `json:"start_occurrence,omitempty"`
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
// permission; the Panel adds nothing to the recipe. An entry its own program
// registered is kept first: the click raises its Keep question
// (keepBeforeOpen), and the app opens after the person keeps it.
func (p *servicePanel) appStart(w http.ResponseWriter, r *http.Request, name string) {
	body, ok := decodeAppAction(w, r)
	if !ok {
		return
	}
	read, cancelRead := context.WithTimeout(r.Context(), appOpenBudget)
	defer cancelRead()
	apps, entry, _, err := p.appPageApplications(read, name)
	if err != nil {
		panelError(w, err)
		return
	}
	if entry == nil || applicationDigest(&entry.Descriptor) != body.Shown {
		http.Error(w, "the registered entry changed since this page was shown; reload and review it again", http.StatusConflict)
		return
	}
	title := appEntryTitle(*entry)
	if directory, pending := p.appKeepPending(read, name, *entry); pending {
		kept, reply := p.keepBeforeOpen(r.Context(), directory, name, *entry)
		if !kept {
			panelJSON(w, reply)
			return
		}
		title = appPageTitle(entry.Descriptor)
	}
	ctx, cancel := context.WithTimeout(r.Context(), appOpenBudget)
	defer cancel()
	p.logAction(ctx, "application.activate", map[string]string{"application.name": name})
	result, err := apps.Activate(ctx, name)
	if err != nil {
		if ctx.Err() != nil && r.Context().Err() == nil {
			// The Panel's own wait ran out: the person's answer or the
			// app's start is still pending, and the next Open finds it.
			panelJSON(w, appActionReply{Outcome: fwire.ApplicationActivationOutcomeNotReady.String(), Message: title + " is still opening. If OpenAbstractions asked you to allow it, answer, then select Open again."})
			return
		}
		panelError(w, err)
		return
	}
	panelJSON(w, appActionReply{Outcome: result.Outcome.String(), Message: appOpenSentence(title, result)})
}

// appKeepWait is how long Open waits for the person's answer to the Keep
// question it raised: the wait policy's 40 s for an answer
// (backlog/RIGHTS-FLOW.md "Wait policy"). appKeepPoll is the interval between
// reads of the entry. Tests shorten both.
var (
	appKeepWait = 40 * time.Second
	appKeepPoll = 200 * time.Millisecond
)

// appKeepPending reports whether the entry waits for the person's Keep, and
// the directory that asks about it. The directory Apps lists
// (applications@2) carries the kept state of both registration owners; an
// applications@3 page does not.
func (p *servicePanel) appKeepPending(ctx context.Context, name string, entry fwire.ApplicationEntry) (setupApplications, bool) {
	directory, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		return nil, entry.SelfRegistered
	}
	listed, readable := appPageEntry(ctx, directory, name)
	if !readable || listed == nil || !identity.SameSubjectProgram(listed.Descriptor.Program, entry.Descriptor.Program) {
		return directory, entry.SelfRegistered
	}
	return directory, listed.SelfRegistered
}

// keepBeforeOpen raises the Keep question of an entry its own program
// registered when the person presses Open on it, and waits for the answer.
// Registration asks nothing; the Keep question is raised when the person acts
// on the app in Apps (VISION 2026-10-06, amended 2026-11-10), and Open is that
// action. apps is the directory that asks and lists the kept state. It
// reports whether the entry now reads kept, and otherwise the one reply Open
// answers.
func (p *servicePanel) keepBeforeOpen(parent context.Context, apps setupApplications, name string, entry fwire.ApplicationEntry) (bool, appActionReply) {
	title := appPageTitle(entry.Descriptor)
	asking, ok := apps.(confirmingApplications)
	if !ok {
		return false, appActionReply{Outcome: fwire.ApplicationActivationOutcomeForbidden.String(), Message: "OpenAbstractions cannot ask whether to keep " + title + "."}
	}
	ctx, cancel := context.WithTimeout(parent, appKeepWait)
	defer cancel()
	p.logAction(ctx, "application.ask_confirmation", map[string]string{"application.name": name})
	change, err := asking.AskConfirmation(ctx, name)
	if err != nil || change.Outcome != fwire.ApplicationOutcomeApplied {
		outcome := fwire.ApplicationActivationOutcomeUnavailable.String()
		if err == nil && change.Outcome == fwire.ApplicationOutcomeUnknown {
			return false, appActionReply{Outcome: "unknown", Message: title + " was already removed."}
		}
		return false, appActionReply{Outcome: outcome, Message: "OpenAbstractions could not ask whether to keep " + title + ". Try again."}
	}
	for {
		current, readable := appPageEntry(ctx, apps, name)
		if readable {
			if current == nil || !identity.SameSubjectProgram(current.Descriptor.Program, entry.Descriptor.Program) {
				return false, appActionReply{Outcome: "unknown", Message: title + " was removed."}
			}
			if !current.SelfRegistered {
				return true, appActionReply{}
			}
		}
		select {
		case <-ctx.Done():
			return false, appActionReply{Outcome: fwire.ApplicationActivationOutcomeNotReady.String(), Message: "OpenAbstractions is waiting for your answer to keep " + title + ". Answer its question, then select Open again."}
		case <-time.After(appKeepPoll):
		}
	}
}

// appOpenBudget is how long the app page's Open waits. The Panel holds
// application.activate, so its Open is decided by that rule alone
// (backlog/APP-LAUNCH-JOB.md ruling 2) and waits for an Allow only for a verb
// only the person may answer: the wait policy's 40 s for that Allow
// (backlog/RIGHTS-FLOW.md "Wait policy"), then the app's start, which its
// manifest bounds at 30 s, and a short margin. Tests shorten it.
var appOpenBudget = 40*time.Second + 30*time.Second + 5*time.Second

// appOpenSentence is the app page's one sentence for an Open's outcome.
func appOpenSentence(title string, result fwire.ApplicationActivationResult) string {
	switch result.Outcome {
	case fwire.ApplicationActivationOutcomeReady:
		return title + " is open."
	case fwire.ApplicationActivationOutcomeNotReady:
		if result.Reason == "consent" {
			return "OpenAbstractions is waiting for your answer to open " + title + ". Answer its question, then select Open again."
		}
		return title + " is opening."
	case fwire.ApplicationActivationOutcomeDisabled:
		return "Open " + title + " from its own shortcut."
	case fwire.ApplicationActivationOutcomeForbidden:
		if result.Reason == "consent" {
			return "You answered Not now. " + title + " stays closed."
		}
		return "OpenAbstractions did not let the Panel open " + title + "."
	case fwire.ApplicationActivationOutcomeIdentityRefused:
		return "OpenAbstractions could not start " + title + "'s program."
	}
	return title + " did not open."
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
	apps, entry, _, err := p.appPageApplications(ctx, name)
	if err != nil {
		panelError(w, err)
		return
	}
	if entry == nil || applicationDigest(&entry.Descriptor) != body.Shown {
		http.Error(w, "the registered entry changed since this page was shown; reload and review it again", http.StatusConflict)
		return
	}
	asking, ok := apps.(confirmingApplications)
	if !ok {
		http.Error(w, "this OpenAbstractions cannot ask about an app that added itself", http.StatusConflict)
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
		// The outcome word stays in the reply's outcome; the sentence is the
		// person's.
		panelJSON(w, appActionReply{Outcome: change.Outcome.String(), Message: "OpenAbstractions could not ask about " + title + ". Try again."})
	}
}

// appRemoveFailure is the log record of a Remove the service refused: its
// outcome, its reason and the Applications contract the Panel used, "native"
// for applications@3 or "legacy" for the @2 listing it falls back to. It is
// nil for an applied Remove and for an entry already gone.
func appRemoveFailure(apps setupApplications, name string, change fwire.ApplicationChange) map[string]string {
	if change.Outcome == fwire.ApplicationOutcomeApplied || change.Outcome == fwire.ApplicationOutcomeUnknown {
		return nil
	}
	client := "legacy"
	if isNativeApplications(apps) {
		client = "native"
	}
	return map[string]string{"application.name": name, "application.outcome": change.Outcome.String(),
		"application.reason": change.Reason, "application.client": client}
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
	apps, entry, _, err := p.appPageApplications(ctx, name)
	if err != nil {
		panelError(w, err)
		return
	}
	if entry == nil || applicationDigest(&entry.Descriptor) != body.Shown {
		http.Error(w, "the registered entry changed since this page was shown; reload and review it again", http.StatusConflict)
		return
	}
	p.logAction(ctx, "application.remove", map[string]string{"application.name": name})
	change, err := apps.Remove(ctx, name)
	if err != nil {
		p.logAction(ctx, "application.remove.failed", map[string]string{"application.name": name, "application.error": err.Error()})
		panelError(w, err)
		return
	}
	if failure := appRemoveFailure(apps, name, change); failure != nil {
		p.logAction(ctx, "application.remove.failed", failure)
	}
	title := appEntryTitle(*entry)
	switch change.Outcome {
	case fwire.ApplicationOutcomeApplied:
		panelJSON(w, appActionReply{Outcome: "applied", Message: "Removed " + title + " from OpenAbstractions."})
	case fwire.ApplicationOutcomeUnknown:
		panelJSON(w, appActionReply{Outcome: "unknown", Message: title + " was already removed."})
	default:
		panelJSON(w, appActionReply{Outcome: change.Outcome.String(), Message: title + " was not removed. Try again."})
	}
}
