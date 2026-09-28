package main

import (
	"embed"
	"html"
	"net/http"
	"net/url"
	"runtime"
	"strings"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
)

// panelCSSFS carries the one stylesheet every page links to: the shared shell
// and its two skins (research/panel-native/DECISION.md item 1).
//
//go:embed static/panel.css
var panelCSSFS embed.FS

func servePanelCSS(w http.ResponseWriter, r *http.Request) {
	data, err := panelCSSFS.ReadFile("static/panel.css")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write(data)
}

// panelPlatform names the skin this document renders: the platform this
// binary actually runs on, or, only for the screenshot tool, an explicit
// override so one machine can render every skin.
func panelPlatform(r *http.Request) string {
	switch r.URL.Query().Get("platform") {
	case "windows", "macos", "linux":
		return r.URL.Query().Get("platform")
	}
	switch runtime.GOOS {
	case "windows":
		return "windows"
	case "darwin":
		return "macos"
	default:
		return "linux"
	}
}

type panelNavItem struct {
	ID    string
	Label string
	Path  string
	Icon  string
}

// panelNavItems lists every page in the order the shell's navigation pane
// shows them, reusing the routes each page already serves. Every item is its
// own page at its own path (task 2026-09-23, fourth first-time visitor: six
// items used to scroll one long Status page instead of opening a page of
// their own); no item carries a hash any more. Labels follow
// research/vocabulary/DECISION.md §4: "Providers" (D41) and "Questions and
// permissions" (D53); the ids and paths stay, and help.go anchors by id.
var panelNavItems = []panelNavItem{
	{ID: "status", Label: "Status", Path: "/", Icon: iconStatus},
	{ID: "work", Label: "Work", Path: "/work", Icon: iconWork},
	{ID: "card", Label: "Device", Path: "/card", Icon: iconCard},
	{ID: "inference", Label: "Inference", Path: "/inference", Icon: iconInference},
	{ID: "registry", Label: "Providers", Path: "/registry", Icon: iconRegistry},
	{ID: "credentials", Label: "Credentials", Path: "/credentials-page", Icon: iconCredentials},
	{ID: "rights", Label: "Questions and permissions", Path: "/rights", Icon: iconRights},
	{ID: "settings", Label: "Settings", Path: "/settings", Icon: iconSettings},
	{ID: "logging", Label: "Logging", Path: "/logging", Icon: iconLogging},
	{ID: "identity", Label: "Identity", Path: "/identity", Icon: iconIdentity},
	{ID: "explore", Label: "Explore", Path: "/explore", Icon: iconExplore},
}

// Monochrome 24x24 glyphs, one per nav item; windows renders them plain,
// macOS and linux render them on a colored tile (see static/panel.css).
const (
	iconStatus      = `<path d="M12 2a10 10 0 100 20 10 10 0 000-20zm-1.2 14.6l-4.4-4.4 1.4-1.4 3 3 6-6 1.4 1.4-7.4 7.4z"/>`
	iconWork        = `<path d="M10 4h4a2 2 0 012 2v1h3a2 2 0 012 2v9a2 2 0 01-2 2H5a2 2 0 01-2-2V9a2 2 0 012-2h3V6a2 2 0 012-2zm0 3h4V6h-4v1zM5 9v9h14V9H5z"/>`
	iconCard        = `<path d="M7 2v2H5v2H3v2h2v8H3v2h2v2h2v-2h10v2h2v-2h2v-2h-2V8h2V6h-2V4h-2V2h-2v2H9V2H7zm0 6h10v8H7V8z"/>`
	iconInference   = `<path d="M4 4h16a1 1 0 011 1v11a1 1 0 01-1 1H9l-5 4V17a1 1 0 01-1-1V5a1 1 0 011-1z"/>`
	iconRegistry    = `<path d="M12 2l10 6-10 6L2 8l10-6zm-8 9.2L12 16l8-4.8V16l-8 4.8L4 16z"/>`
	iconCredentials = `<path d="M14 2a6 6 0 00-5.65 8.06L2 16.4V20a1 1 0 001 1h3.6l1.35-1.35H10v-2h2v-2h1.94A6 6 0 1014 2zm2 3a2 2 0 110 4 2 2 0 010-4z"/>`
	iconRights      = `<path d="M12 2l8 3v6c0 5-3.5 8.5-8 11-4.5-2.5-8-6-8-11V5l8-3z"/>`
	iconSettings    = `<path d="M12 8a4 4 0 100 8 4 4 0 000-8zm9 4a7.4 7.4 0 01-.14 1.4l2.1 1.6-2 3.4-2.5-1a7.6 7.6 0 01-2.4 1.4L18 22H8.4l-.46-2.8a7.6 7.6 0 01-2.4-1.4l-2.5 1-2-3.4 2.1-1.6A7.4 7.4 0 013 12c0-.5.05-1 .14-1.4L1.14 9l2-3.4 2.5 1c.7-.6 1.5-1.05 2.4-1.4L8.4 2h7.2l.46 2.8c.9.35 1.7.8 2.4 1.4l2.5-1 2 3.4-2.1 1.6c.1.45.14.95.14 1.4z"/>`
	iconLogging     = `<path d="M6 2h9l5 5v15H6V2zm8 1.5V8h4.5L14 3.5zM8 12h8v2H8v-2zm0 4h8v2H8v-2zm0-8h4v2H8V8z"/>`
	iconIdentity    = `<path d="M12 12a5 5 0 100-10 5 5 0 000 10zm0 2c-4.4 0-8 2.24-8 5v3h16v-3c0-2.76-3.6-5-8-5z"/>`
	iconExplore     = `<path d="M12 2a10 10 0 100 20 10 10 0 000-20zm3.5 5.5l-2 6-6 2 2-6z"/>`
)

func panelNav(key, active string) string {
	// Every WriteString below targets a strings.Builder, which never errors.
	var b strings.Builder
	//unchecked: strings.Builder never errors
	b.WriteString(`<nav class="oa-nav" aria-label="Panel pages">`)
	for _, item := range panelNavItems {
		href := item.Path + "?k=" + url.QueryEscape(key)
		class := "oa-nav-item"
		if item.ID == active {
			class += " active"
		}
		//unchecked: strings.Builder never errors
		b.WriteString(`<a class="` + class + `" href="` + html.EscapeString(href) + `">`)
		//unchecked: strings.Builder never errors
		b.WriteString(`<span class="oa-nav-icon"><svg viewBox="0 0 24 24" aria-hidden="true">` + item.Icon + `</svg></span>`)
		//unchecked: strings.Builder never errors
		b.WriteString(`<span class="oa-nav-label">` + html.EscapeString(item.Label) + `</span></a>`)
	}
	//unchecked: strings.Builder never errors
	b.WriteString(`</nav>`)
	return b.String()
}

// panelPage assembles one full document around a page's own fragment (its
// existing markup and unchanged <script>): doctype, the platform attribute,
// the shared stylesheet, and the shell's navigation pane. It also links every
// section to its reference anchor (help.go): a mark inside each section
// heading, a Learn-more link after each section's first paragraph, and one
// page-level floating mark, shown per platform by static/panel.css.
func panelPage(r *http.Request, key, active, title, fragment string) string {
	fragment = injectSectionHelp(active, fragment)
	fragment = injectGrantControls(active, fragment)
	// The viewport meta tag was missing (task 2026-09-23, fifth first-time
	// visitor, finding 8): with none, a narrow render of this page falls back
	// to the 980px layout a touch browser assumes for a page never designed
	// for a small screen, then shrinks that to fit — the two-column layout
	// stays two columns, shrunk down and unreadable, well past where
	// static/panel.css's own max-width: 860px stacking rule would otherwise
	// take over. width=device-width makes the layout viewport track the
	// window's real width, so that rule engages at the width it names.
	return `<!doctype html><html lang="en" data-platform="` + panelPlatform(r) + `"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>` + html.EscapeString(title) + `</title><link rel="stylesheet" href="/static/panel.css"></head><body><div class="oa-shell">` + panelNav(key, active) + navScript + plainScript + grantScript + `<main class="oa-content">` + fragment + pageHelpFloat(active) + `</main></div></body></html>`
}

// grantEntry names one section that already tells the person which rules it
// needs (task 2026-09-23, "Work started by apps" and its siblings): the exact
// rules this Panel's own program must hold as permit, the resource a rule
// carries none of its own, and the buttons to click again once every rule
// lands, so the section's own read runs with the new rule in effect. Page and
// ID locate the block the same way help.go's sectionBlock does: an existing
// "<section id=...>" that help.go already anchors, so attaching a control
// here adds no new id for TestHelpAnchorsCoverEverySection to track.
//
// The Inference and Registry pages name their rules in an un-sectioned intro
// with no id of its own; rather than mint one (which would need a matching
// help.go entry and a matching anchor on the published site), those two
// pages carry their "Grant" widget inline in inference_page.go and
// registry_page.go, wired to the same shared grantScript below by id.
type grantEntry struct {
	Page     string
	ID       string
	Rules    []string // "<action>" (uses Resource) or "<action>|<resource>" naming its own
	Resource string
	Rerun    []string // button ids this Panel already renders for that section's read
}

var grantEntries = []grantEntry{
	// Work, "Work started by apps": JOB-A13 decides both on the one job
	// acceptance resource (account_work_panel.go).
	{Page: "work", ID: "account-work",
		Rules:    []string{host.JobInventoryAction, host.JobCancelAction},
		Resource: host.JobResource,
		Rerun:    []string{"account-start"}},
	// Graphics card: the resource table read, on "account".
	{Page: "card", ID: "holders",
		Rules:    []string{resourceservice.ActionTableRead},
		Resource: resourceservice.ResourceAccount,
		Rerun:    []string{"refresh"}},
	// Credentials, "App access": the page's own listing and the allow/deny
	// editor both need this Panel to hold the credentials holder rules, not
	// only the dynamic per-name apply rule the section's form itself edits.
	{Page: "credentials", ID: "access",
		Rules:    []string{credentials.ActionManage, credentials.ActionRead},
		Resource: credentials.ResourceAccount,
		Rerun:    []string{"allow-list"}},
}

// injectGrantControls rewrites one page's fragment to carry a "Grant" control
// after the first paragraph of every listed section: a button and a status
// line, wired by the shared grantScript to write the section's exact rules
// for this Panel's own program at the policy revision the control reads
// itself, then click the section's own read again. It leaves a section
// untouched when the map has no entry for this page, and leaves the fragment
// untouched when a listed section id cannot be found in it.
//
// It places the div inline, right after each section's own
// paragraph, and collects every widget's one-line bootstrap call to append
// once at the very end of the fragment, after the page's own <script>...
// </script> block closes: monitor/*_page_test.js extracts each page's script
// as the text between the first "<script>" and the first "</script>" it
// finds, so a bootstrap tag placed any earlier would be mistaken for that
// script and break every one of those tests.
func injectGrantControls(page, fragment string) string {
	var trailer strings.Builder
	for _, g := range grantEntries {
		if g.Page != page {
			continue
		}
		start, end, ok := sectionBlock(fragment, g.ID)
		if !ok {
			continue
		}
		block := fragment[start:end]
		block = injectGrantDivIntoBlock(block, g)
		fragment = fragment[:start] + block + fragment[end:]
		//unchecked: strings.Builder never errors
		trailer.WriteString(grantBootstrapScript(g))
	}
	return fragment + trailer.String()
}

// injectGrantDivIntoBlock places the widget's div right after the block's
// first paragraph (the sentence that already names the rules), or after its
// first heading when the block carries no paragraph of its own.
func injectGrantDivIntoBlock(block string, g grantEntry) string {
	widget := grantWidgetHTML(g)
	if pm := firstPara.FindStringIndex(block); pm != nil {
		return block[:pm[1]] + widget + block[pm[1]:]
	}
	if hm := firstHeading.FindStringSubmatchIndex(block); hm != nil {
		return block[:hm[7]] + widget + block[hm[7]:]
	}
	return block + widget
}

// grantWidgetHTML renders one section's control: a labelled div carrying the
// rules and default resource as data attributes for inspection, and a button
// and a status line the shared grantScript's bootstrap call wires up. The
// status line is a plain sentence ("Nothing to grant here; you can use this
// section."), never a JSON dump or a hash; it used to sit in a <pre>, so
// every such sentence rendered monospace like a data block instead of body
// text (task 2026-09-23, fourteenth first-time visitor, finding 1). <pre>
// is reserved for Technical details now; a status sentence is a <p>.
func grantWidgetHTML(g grantEntry) string {
	id := g.ID + "-grant"
	rules := html.EscapeString(strings.Join(g.Rules, ","))
	resource := html.EscapeString(g.Resource)
	return `<div class="oa-grant" id="` + id + `" data-grant-rules="` + rules + `" data-grant-resource="` + resource + `"><button type="button">Grant these to this Panel</button><p class="oa-grant-status" aria-live="polite"></p></div>`
}

// grantBootstrapScript is the one-line call that hands one widget to the
// shared grantScript once its div exists in the document.
func grantBootstrapScript(g grantEntry) string {
	id := g.ID + "-grant"
	rerun := html.EscapeString(strings.Join(g.Rerun, ","))
	return `<script>oaGrant('` + id + `','` + rerun + `')</script>`
}

// grantScript is the one control panelPage embeds before every page's
// fragment (task 2026-09-23): oaGrant(id, rerun) wires the button inside
// #id, reading its own configuration back from that div's data attributes
// rather than from its arguments, so the rendered HTML stays the single
// source of truth. A rule token of "<action>|<resource>" names its own
// resource; a bare action uses the div's data-grant-resource. On success it
// clicks each button id in the comma-separated rerun list, the section's own
// read. A refusal is shown inline, never assumed to have changed nothing more
// than the rule it names; forbidden also names the programs that may
// administer policy instead of asking to elevate this Panel, since no rule
// this control writes can grant that authority to itself.
//
// Every widget's bootstrap call also feeds gLoadHeld (task 2026-09-23,
// "grant held windows"): an installed Panel already holds its rules from
// installation (serve/runtime_rights.go, installationRules), so the button
// and its sentence are wrong on first sight unless the control checks what
// this Panel's own program already holds. The listing runs once per page,
// shared by every widget through the memoized gHeldPromise, and never
// invents a state when that listing is refused or unavailable. A rule
// granted through the button is known to be held the moment every one of
// its own tokens lands, so the widget settles into the holds state directly
// rather than listing the policy again.
// navScript keeps the navigation pane's highlight on the item the person
// chose. panelNav marks one item from the page id on the server, and six
// items (Work, Questions and rights, Settings, Logging, Identity, Explore) are
// sections of the Status page reached by a hash, so before this script every
// one of them left "Status" lit after the click (owner, 2026-09-23: "they
// don't stick selected"). The item whose path and hash match the location is
// current; with no hash, the item on this path that carries no hash is. It
// runs at load and on every hashchange, and never touches items on another
// path, which the server already marked from the page id.
const navScript = `<script>
(function () {
  function current() {
    var items = document.querySelectorAll('.oa-nav .oa-nav-item');
    var here = location.pathname, hash = location.hash;
    var chosen = null, plain = null;
    for (var i = 0; i < items.length; i++) {
      var a = items[i];
      if (a.pathname !== here) continue;
      if (hash && a.hash === hash) chosen = a;
      if (!a.hash) plain = a;
    }
    var pick = chosen || plain;
    if (!pick) return;
    for (var j = 0; j < items.length; j++) {
      var b = items[j];
      if (b.pathname !== here) continue;
      if (b === pick) { b.classList.add('active'); b.setAttribute('aria-current', 'page'); }
      else { b.classList.remove('active'); b.removeAttribute('aria-current'); }
    }
  }
  window.addEventListener('hashchange', current);
  current();
})();
</script>`

const grantScript = `<script>
(function(){
const gKey=new URLSearchParams(location.search).get('k');
async function gcall(path,body){const r=await fetch(path,{method:body?'POST':'GET',headers:{'X-Panel-Key':gKey,...(body?{'Content-Type':'application/json'}:{})},body:body?JSON.stringify(body):undefined});if(!r.ok)throw Error(await r.text());return r.json()}
const gOperators='the runtime itself and the programs installed beside it: openabstractions, openabstractionsw and Abstraction Panel';
function gReason(o){switch(o){case 'forbidden':return 'this Panel may not administer policy. The operator programs this installation grants are '+gOperators+'.';case 'unavailable':return 'the rights service could not complete this; list again before another edit.';case 'gap':return 'the policy changed while listing; start again.';case 'invalid':return 'this rule was rejected as invalid.';case 'conflict':return 'the policy changed since it was listed; list again before another edit.';default:return 'outcome '+o+'.'}}
function gRefused(o){return 'The rights service refused: '+gReason(o)}
function gRuleKey(action,resource){return action+'|'+resource}
function gResolveRules(box){
 const defaultResource=box.dataset.grantResource;
 return box.dataset.grantRules.split(',').map(s=>s.trim()).filter(Boolean).map(token=>{
  const parts=token.split('|');
  return{action:parts[0],resource:parts[1]||defaultResource}
 })
}
// gApplyHeld reflects what this Panel's own program already holds onto one
// widget: every rule held hides the button and also hides the section's own
// "you need the rule..." sentence (the <p> the widget's div follows, placed
// there by injectGrantDivIntoBlock), since a restriction the Panel already
// clears is not worth threatening the reader with; the status line then
// carries the whole explanation on its own. Some held keeps the button and
// the sentence, and narrows the button's label to the rules still missing
// while the status line names the ones already held. None held is left
// exactly as rendered (today's behaviour), so this function makes no change
// at all in that case.
function gApplyHeld(button,status,resolved,held,box){
 const missing=[],present=[];
 for(const r of resolved)(held.has(gRuleKey(r.action,r.resource))?present:missing).push(r.action);
 if(missing.length===0){
  button.hidden=true;
  const sentence=box&&box.previousElementSibling;
  if(sentence&&sentence.tagName==='P')sentence.hidden=true;
  status.className='oa-grant-status';
  status.textContent='Nothing to grant here; you can use this section.';
  return
 }
 if(present.length===0)return;
 button.hidden=false;
 button.textContent=missing.length===1?'Grant the missing rule to this Panel':'Grant the '+missing.length+' missing rules to this Panel';
 status.className='oa-grant-status';
 status.textContent='This Panel holds '+present.join(', ')+'.';
}
// gPlainPhrase and gRewriteSentence replace a section's own "you need the
// rule <raw action>..." sentence with plain wording (task 2026-09-23,
// finding 3: the sentence still named the raw action alone, e.g. "You need
// the rule abstraction.job/inventory.read to see it"). The plain phrase for
// each action comes from the same ActionPlain map /rights already sends
// alongside the one listing gLoadHeld makes for every widget on the page, so
// this costs no extra call; a rule this runtime does not name in that map
// falls back to oaActionFallback's generic phrase. Neither ever shows the
// raw action id in the sentence itself; every id resolved names, however
// many, sits in the sentence's own title attribute instead (task 2026-09-23,
// seventh first-time visitor, finding 2: an id is never visible page text).
function gPlainPhrase(action,plain){const p=plain&&plain[action];return p?p.charAt(0).toUpperCase()+p.slice(1):oaActionFallback(action)}
function gRewriteSentence(box,resolved,plain){
 const sentence=box&&box.previousElementSibling;
 if(!sentence||sentence.tagName!=='P')return;
 const parts=resolved.map(r=>'permission to '+gPlainPhrase(r.action,plain));
 sentence.textContent='You need '+parts.join(' and ')+' to use this section.';
 sentence.title=resolved.map(r=>r.action).join(', ');
}
// gLoadHeld lists the policy once per page load (memoized here, so every
// widget's bootstrap call shares the one listing) and returns the rules this
// Panel's own program holds as permit, plus the plain-phrase map that same
// listing carries. A page that comes back refused or unavailable, at any
// point in the listing, resolves the whole thing to null: every widget then
// leaves itself exactly as rendered rather than invent a state from a
// partial answer.
// gLoadHeld's own listing used to be memoized for the whole page's life
// with no way to ask it again: a widget that read "Grant these to this
// Panel" because its very first check (racing the page's own other reads)
// came back before this Panel actually held the rule stayed wrong until a
// full reload, "flipping" only when some other, unrelated action happened
// to reload the page along with it (task 2026-09-23, fifteenth first-time
// visitor, finding 1). gLoadHeld(true) now forces a fresh listing; every
// section's own Refresh button calls oaGrantRefresh (below) alongside its
// own read, the same fetch this held-check itself already makes.
let gHeldPromise=null;
function gLoadHeld(force){
 if(force)gHeldPromise=null;
 if(!gHeldPromise)gHeldPromise=(async()=>{
  const self=(await gcall('/explore')).self;
  const held=new Set();
  let cursor='',plain=null;
  for(;;){
   const page=await gcall('/rights?cursor='+encodeURIComponent(cursor));
   if(page.Outcome!=='page')return null;
   if(!cursor)plain=page.ActionPlain;
   for(const r of page.Rules)if(r.Permit&&r.Subject&&r.Subject.Program===self.program&&r.Subject.Account===self.account)held.add(gRuleKey(r.Action,r.Resource));
   if(page.Complete||!page.Next)break;
   cursor=page.Next
  }
  return{self,held,plain}
 })().catch(()=>null);
 return gHeldPromise
}
// gWidgets lists every grant widget oaGrant has bootstrapped on this page,
// so oaGrantRefresh (called from a section's own Refresh handler) can
// re-check and re-render every one of them from the one fresh listing,
// without each page needing to know its own widget's id.
const gWidgets=[];
window.oaGrantRefresh=function(){
 gLoadHeld(true).then(info=>{if(!info)return;for(const w of gWidgets){gRewriteSentence(w.box,w.resolved,info.plain);gApplyHeld(w.button,w.status,w.resolved,info.held,w.box)}});
};
window.oaGrant=function(id,rerun){
 const box=document.getElementById(id);
 const button=box.children[0],status=box.children[1];
 const rerunIds=(rerun||'').split(',').map(s=>s.trim()).filter(Boolean);
 const resolved=gResolveRules(box);
 gWidgets.push({box,button,status,resolved});
 gLoadHeld().then(info=>{if(info){gRewriteSentence(box,resolved,info.plain);gApplyHeld(button,status,resolved,info.held,box)}});
 button.onclick=async function(){
  button.disabled=true;status.className='oa-grant-status';status.textContent='Granting…';
  try{
   const self=(await gcall('/explore')).self;
   const page=await gcall('/rights?cursor=');
   if(page.Outcome!=='page'){status.className='oa-grant-status error';status.textContent=gRefused(page.Outcome);button.disabled=false;return}
   let revision=page.Revision;const landed=[];
   for(const r of resolved){
    const reply=await gcall('/rights',{edit:'set',revision,account:self.account,program:self.program,action:r.action,resource:r.resource,permit:true});
    if(reply.Outcome!=='applied'){status.className='oa-grant-status error';status.textContent=gRefused(reply.Outcome);button.disabled=false;return}
    revision=reply.Revision;landed.push(r.action)
   }
   status.textContent='Granted '+landed.join(', ')+' to this Panel.';
   gApplyHeld(button,status,resolved,new Set(resolved.map(r=>gRuleKey(r.action,r.resource))),box);
   for(const rid of rerunIds){const b=document.getElementById(rid);if(b&&b.onclick)b.onclick()}
  }catch(e){status.className='oa-grant-status error';status.textContent=e.message}
  button.disabled=false
 }
};
})();
</script>`

// plainScript is the one control every page's fragment can call to keep a raw
// backend error off the screen (task 2026-09-23, first-time-user walkthrough:
// a service-resolution failure naming a pipe path and a security identifier
// shown as the Graphics card page's first line before any click; "open
// C:\Users\<name>\...\admin.secret: ..." shown after a click; the same
// generic denial worded three different ways on the Registry page). A page's
// own script calls oaShowError(id, err) wherever it used to write a caught
// error or a raw outcome string straight into an element; oaPlain(text) is
// exposed separately for a page that needs to fold several such texts into
// one sentence of its own (registry_page.go, unifying three denials from one
// click into one line).
//
// oaPlain never repeats the input text in the sentence it returns: no file
// path, pipe name, security identifier, or contract name (an
// "abstraction.<x>/<y>@<n>" carries a runtime version and is never a rule to
// ask for) can survive into it. The one exception is a rule name of the
// shape "abstraction.<x>/<y>" or "<x>.<y>" found in a permission denial,
// named so the reader knows what to ask this Panel's operator for; the
// original text always remains available as the "details" this function also
// returns, for oaShowError's disclosure.
const plainScript = `<script>
function oaRuleName(text){
 var m=/abstraction\.[A-Za-z0-9_-]+\/[A-Za-z0-9_.-]+/.exec(text);
 if(m&&text.charAt(m.index+m[0].length)!=='@')return m[0].replace(/\.+$/,'');
 // A bare "<x>.<y>" token immediately followed by "/" is the head of an
 // "abstraction.<x>/<y>" token (a rule already handled above, or a
 // "abstraction.<x>/<y>@<n>" contract, never a rule on its own): skip it
 // rather than report half of it. A denylisted extension (a file, not a
 // rule) is skipped the same way; the search keeps going past either.
 var skip={secret:1,exe:1,dll:1,json:1,txt:1,log:1,py:1,go:1,js:1,md:1,yml:1,yaml:1,ini:1,cfg:1,dat:1,db:1,pipe:1};
 var re=/\b[a-z][a-z0-9_-]*\.[a-z][a-z0-9_-]*\b/g,b;
 while((b=re.exec(text))){
  if(text.charAt(b.index+b[0].length)==='/')continue;
  var ext=b[0].slice(b[0].lastIndexOf('.')+1);
  if(!skip[ext])return b[0].replace(/\.+$/,'');
 }
 return null;
}
// oaContractName finds the one "abstraction.<layer>/<name>@<n>" contract a
// service-resolution failure names, if any: the runtime version this Panel
// asked for and the layer/name it belongs to, never a rule to ask for (see
// oaRuleName above, which already skips this same shape).
function oaContractName(text){
 var m=/abstraction\.([a-z][a-z0-9_-]*)\/([a-z][a-z0-9_.-]*)@\d+/.exec(text);
 return m?{layer:m[1],name:m[2]}:null;
}
// oaServiceNames maps a contract's "<layer>/<name>" (or, failing that, its
// bare layer) to the plain-language service name a settings-app sentence
// says instead of the contract itself. Two resource-layer contracts name
// different things to the person (the awake-lease service, the graphics-card
// table), so those two are listed by their full "layer/name"; every other
// layer here means the same thing across its contracts, so it is listed by
// layer alone. An unlisted layer falls back to "that service" rather than
// invent a name.
var oaServiceNames=[
 ['resource/leases','the awake-lease service'],
 ['resource/table','the graphics-card table'],
 ['config/observer','settings watching'],
 ['job','background work'],
 ['download','downloads'],
 ['logging','the log'],
 ['storage','storage'],
 ['inference','inference'],
 ['router','the model router'],
 ['credentials','credentials'],
 ['asks','questions'],
 ['model','model lookup'],
];
function oaServiceName(contract){
 if(!contract)return'that service';
 var full=contract.layer+'/'+contract.name;
 for(var i=0;i<oaServiceNames.length;i++)if(oaServiceNames[i][0]===full)return oaServiceNames[i][1];
 for(var j=0;j<oaServiceNames.length;j++)if(oaServiceNames[j][0]===contract.layer)return oaServiceNames[j][1];
 return'that service';
}
// oaActionFallback names a rights action this Panel's rightsActionPlain map
// (rights_panel.go) does not carry yet: "abstraction.<layer>/<rest>" reads
// as "<Layer>: <rest, dots turned to spaces>" (task 2026-09-23, sixth
// first-time visitor, finding 2: a dropdown and a list mixed plain phrases
// with bare ids like "abstraction.storage/content.observe" for the one
// action this runtime knows that the map did not yet name). It is never
// shown alone: every caller appends the raw id after it in parentheses, the
// same shape a known action's "phrase (id)" already uses elsewhere.
function oaActionFallback(action){
 var m=/^abstraction\.([a-z][a-z0-9_-]*)\/([a-z][a-z0-9_.-]*)$/.exec(action);
 if(!m)return action;
 return m[1].charAt(0).toUpperCase()+m[1].slice(1)+': '+m[2].replace(/\./g,' ');
}
// oaResourceLabel names a graphics-card resource ("card:<n>", the resource
// table's own index for that card) "Graphics card" for the first one and
// "Graphics card <n+1>" for each after it, instead of the raw id (task
// 2026-09-23, eighth first-time visitor, finding 5: "card:0" shown as a
// label on Device and in rule rows, e.g. "Resource hold on card:0"). Any
// other resource (a credential name, a host, "awake") is not a card and
// keeps its own name unchanged.
function oaResourceLabel(resource){
 var m=/^card:(\d+)$/.exec(resource);
 if(m){var n=Number(m[1]);return 'Graphics card'+(n>0?' '+(n+1):'')}
 // A resource shaped like a contract id ("abstraction.job/acceptance@1")
 // names the whole surface a job-shaped rule is scoped to, never a value
 // typed or chosen; the same generic word Explore's own argumentPhrase
 // already reads it as (task 2026-09-23, eleventh first-time visitor,
 // finding 4: a rights confirmation read the raw contract id as its own
 // resource). "service" is the vocabulary decision's own word for this
 // string shape (research/vocabulary/DECISION.md D4), replacing "this
 // capability's own contract" (task 2026-09-23, twelfth first-time visitor,
 // finding 4).
 if(/^abstraction\.[a-z]+\//.test(resource))return "this service";
 return resource;
}
// oaHostState reads a host's Up flag as one plain word, "Up" or "Not
// reachable"; the raw connection error (Go's own "Get \"http://...\":
// dial tcp ...: connectex: ..." text) never appears as visible text,
// shared by Inference and Registry's own host tables, each of which keeps
// it as that cell's own title (task 2026-09-23, eleventh first-time
// visitor, finding 5).
function oaHostState(up){return up?'Up':'Not reachable'}
// oaLocalTime reads a raw ISO 8601 timestamp (or an epoch millisecond
// number) as a local, readable time: "today 6:14 PM" for a moment on
// today's date, a locale date ("9/20/2026") for an older one. The raw ISO
// value is never the return value; callers keep it as the element's own
// title, the same convention oaResourceLabel and oaPlain follow for the
// identifiers they read (task 2026-09-23, tenth first-time visitor, finding
// 2: "Issued 2026-09-23T15:09:12.464Z", "Checked at 2026-09-23T18:14:50+03:00"
// and "at 2026-09-23T15:09:12.462974Z" were all a raw timestamp read aloud).
// oaClockText is the one clock format every page shares (task 2026-09-23,
// twelfth first-time visitor, minor: Logging read "20:32:56", a 24-hour
// format built by hand, while Settings and everywhere else read oaLocalTime's
// 12-hour "8:42 PM" through this same function): a caller that needs
// second-level precision (Logging's own rows, several a minute) passes
// seconds=true and gets the same hour:minute shape with :SS appended.
function oaClockText(d,seconds){
 return d.toLocaleTimeString([],seconds?{hour:'numeric',minute:'2-digit',second:'2-digit'}:{hour:'numeric',minute:'2-digit'});
}
function oaLocalTime(value){
 if(value===null||value===undefined||value==='')return '';
 var d=new Date(value);
 if(isNaN(d.getTime()))return String(value);
 var now=new Date();
 var time=oaClockText(d,false);
 if(d.toDateString()===now.toDateString())return 'today '+time;
 return d.toLocaleDateString();
}
function oaShortName(path){var parts=String(path||'').split(/[\\/]/).filter(function(x){return x});return parts.length?parts[parts.length-1]:path}
// oaProgramChooser builds and keeps in sync a program-path chooser: a
// <select> of known programs, each by file name with the full path on its
// own title, plus "Other…" revealing a paired hidden <input> so a page's
// existing code reading that input's own .value keeps working unchanged.
// Rights' own Program field built this exact pair on its own; Inference's
// Issue key and Credentials' App access each left their own program field
// a bare text box instead (task 2026-09-23, fourteenth first-time visitor,
// finding 8). One shared implementation now backs all three: knownPrograms
// is a function returning the current list (a page's own set may still be
// growing when this chooser first builds), called fresh on every rebuild.
function oaProgramChooser(selectId,inputId,knownPrograms){
 var select=document.getElementById(selectId),input=document.getElementById(inputId);
 function sync(){
  var v=select.value;
  if(v==='__other__'){input.hidden=false;if(knownPrograms().indexOf(input.value)!==-1)input.value=''}
  else{input.hidden=true;input.value=v}
 }
 select.onchange=sync;
 function rebuild(){
  var current=select.value;
  var known=[];for(var i=0,list=knownPrograms();i<list.length;i++)if(known.indexOf(list[i])===-1)known.push(list[i]);
  known.sort();
  select.replaceChildren();
  for(var j=0;j<known.length;j++){var o=document.createElement('option');o.value=known[j];o.textContent=oaShortName(known[j]);o.title=known[j];select.append(o)}
  var other=document.createElement('option');other.value='__other__';other.textContent='Other…';select.append(other);
  select.value=current&&known.indexOf(current)!==-1?current:'__other__';
  sync();
 }
 return {rebuild:rebuild};
}
// oaAccountChooser is oaProgramChooser's own twin for an account field: it
// defaults to this Panel's own account, "this account, <name>", the
// moment ensureSelf resolves it, with "Other…" for a different one. Rights'
// own Account field built this pattern first (task 2026-09-23, twelfth
// first-time visitor, finding 6); Credentials' App access left its own
// account field a bare required text box (task 2026-09-23, fourteenth
// first-time visitor, finding 8). selfAccount and selfAccountName are
// functions so the chooser always reads the page's own current values,
// which usually resolve after this chooser first builds.
function oaAccountChooser(selectId,inputId,selfAccount,selfAccountName){
 var select=document.getElementById(selectId),input=document.getElementById(inputId);
 var touched=false;
 function sync(){
  var v=select.value;
  if(v==='__other__'){input.hidden=false;if(input.value===selfAccount())input.value=''}
  else{input.hidden=true;input.value=v}
 }
 select.onchange=function(){touched=true;sync()};
 function rebuild(){
  var current=select.value,self=selfAccount();
  select.replaceChildren();
  if(self){var o=document.createElement('option');o.value=self;o.textContent='this account'+(selfAccountName()?', '+selfAccountName():'');o.title=self;select.append(o)}
  var other=document.createElement('option');other.value='__other__';other.textContent='Other…';select.append(other);
  var fallback=self||'__other__';
  select.value=touched&&current?current:fallback;
  sync();
 }
 return {rebuild:rebuild};
}
function oaPlain(text){
 var t=text==null?'':String(text);var lower=t.toLowerCase();var line;
 if(lower.indexOf('forbidden')!==-1||lower.indexOf('not permitted')!==-1||lower.indexOf('no rule')!==-1){
  line='The runtime did not permit this Panel to do this; nothing was changed.';
  var rule=oaRuleName(t);if(rule)line+=' It needs the rule '+rule+'.';
 }else if(lower.indexOf('incompatible')!==-1){
  var ic=oaContractName(t);
  if(ic)line="The runtime provides "+oaServiceName(ic)+" at a version this Panel doesn't understand; update the runtime or the Panel.";
  else line="This Panel and the runtime don't speak the same version of this service; update one of them.";
 }else if(lower.indexOf('unavailable')!==-1||lower.indexOf('service resolution')!==-1){
  var uc=oaContractName(t);
  if(uc)line="The runtime is running but doesn't provide "+oaServiceName(uc)+" on this account.";
  else line="That service isn't running on this account right now. Start the runtime, or check the service on the Status page.";
 }else if(lower.indexOf('gap')!==-1){
  line='The list changed while reading; start again.';
 }else if(lower.indexOf('invalid')!==-1){
  line='The runtime rejected this request.';
 // task 2026-09-23, ninth first-time visitor, finding 2: Continue saved
 // download and Save download record, called with nothing saved, both threw
 // the one technical sentence "No saved request to <continue|save>. ..."
 // straight to the status line. Each reads its own plain line now.
 }else if(lower.indexOf('no saved request to continue')!==-1){
  line='Choose a saved download first.';
 }else if(lower.indexOf('no saved request to save')!==-1){
  line='Start a download first, then save its record.';
 }else{
  line="This didn't work.";
 }
 return{line:line,details:t};
}
function oaShowError(id,err){
 var el=document.getElementById(id);
 if(!el)return;
 var text=err&&typeof err==='object'&&'message'in err?err.message:err;
 var p=oaPlain(text);
 if(el.replaceChildren)el.replaceChildren();
 el.textContent=p.line;
 if(p.details&&p.details!==p.line){
  var details=document.createElement('details');details.className='oa-error-details';
  var summary=document.createElement('summary');summary.textContent='Technical details';
  var pre=document.createElement('pre');pre.textContent=p.details;
  details.append(summary,pre);
  el.append(details);
 }
}
// oaShowResult is oaShowError's own shape for a call that succeeded: one
// plain line, with any raw value the call returned (a revision, a receipt,
// a full response) kept off that line, on the element's own title and
// behind a Technical details disclosure instead (task 2026-09-23, tenth
// first-time visitor, finding 1: a raw {"Outcome":...} object, and lines
// like "Stored test-credential at revision 2-3a9e13ed", were visible text).
function oaShowResult(id,line,raw,title){
 var el=document.getElementById(id);
 if(!el)return;
 if(el.replaceChildren)el.replaceChildren();
 el.textContent=line;
 el.title=title||'';
 if(raw!==undefined&&raw!==null){
  var details=document.createElement('details');details.className='oa-error-details';
  var summary=document.createElement('summary');summary.textContent='Technical details';
  var pre=document.createElement('pre');pre.textContent=typeof raw==='string'?raw:JSON.stringify(raw,null,2);
  details.append(summary,pre);
  el.append(details);
 }
}
</script>`
