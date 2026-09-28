// Run with node monitor/panel_shell_page_test.js; no browser or service is
// started. Covers grantScript (monitor/panel_shell.go), the shared "Grant"
// control every *_page.go widget calls as oaGrant(id, rerun): both the click
// flow that writes rules, and the on-load check of what this Panel's own
// program already holds (task 2026-09-23, "grant held windows").
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/panel_shell.go', 'utf8');
// grantScript is its own Go raw string, `<script>...</script>`; panel_shell.go
// also builds a one-line bootstrap "<script>oaGrant(...)</script>" elsewhere
// (grantBootstrapScript), so the constant is sliced out by name first and the
// script tag stripped from that slice, not from the whole file.
const marker = 'const grantScript = `';
const start = go.indexOf(marker) + marker.length;
assert(start > marker.length - 1, 'grantScript constant not found in panel_shell.go');
const end = go.indexOf('`', start);
const raw = go.slice(start, end);
const script = raw.split('<script>')[1].split('</script>')[0];
// plainScript (monitor/panel_shell.go) is embedded ahead of grantScript in
// the real document; gPlainPhrase's fallback for an action rightsActionPlain
// does not carry now calls oaActionFallback, defined there (task 2026-09-23,
// sixth first-time visitor, finding 2), so this isolated test loads it into
// the same vm context first, the same way monitor/card_page_test.js and its
// siblings already do.
const plainMarker = 'const plainScript = `';
const plainStart = go.indexOf(plainMarker) + plainMarker.length;
assert(plainStart > plainMarker.length - 1, 'plainScript constant not found in panel_shell.go');
const plainEnd = go.indexOf('`', plainStart);
const plainScript = go.slice(plainStart, plainEnd).split('<script>')[1].split('</script>')[0];

// A grant widget's div, as grantWidgetHTML renders it: dataset carries the
// rules and default resource, children are [button, status-pre] in that
// fixed order (grantWidgetHTML always emits button then pre). hidden starts
// undefined, the same falsy starting point a real un-hidden <button> has.
// previousElementSibling stands in for the section's own "you need the
// rule..." <p>, the paragraph injectGrantDivIntoBlock places the div right
// after; its tagName is 'P', matching a real DOM element, so gApplyHeld's
// tag check only ever hides an actual paragraph, never the heading fallback.
function grantBox(id, rules, resource) {
 const button = { disabled: false, hidden: undefined, onclick: null, className: '', textContent: 'Grant these to this Panel' };
 const status = { className: 'oa-grant-status', textContent: '' };
 const sentence = { tagName: 'P', hidden: undefined };
 return { id, dataset: { grantRules: rules, grantResource: resource }, children: [button, status], button, status, previousElementSibling: sentence, sentence };
}
function rerunButton() {
 let calls = 0;
 const b = { onclick: () => { calls++ } };
 Object.defineProperty(b, 'calls', { get: () => calls });
 return b;
}

// One PolicyRule as /rights's list page renders it (Subject.Account,
// Subject.Program, Action, Resource, Permit): the shape service_page.go's
// own ruleText already reads.
function rule(program, account, action, resource, permit) {
 return { Subject: { Account: account, Program: program }, Action: action, Resource: resource, Permit: permit !== false };
}
// The two replies gLoadHeld's one page-load listing consumes: /explore
// naming this Panel's own program and account, then one /rights?cursor=
// page carrying exactly the rules to report as held (Complete, so the loop
// takes one page).
function heldPage(program, account, rules) {
 return [
  { json: { self: { program, account } } },
  { json: { Outcome: 'page', Revision: 'list-rev', Catalog: [], Rules: rules, Next: '', Complete: true } },
 ];
}

// newPage evaluates grantScript into a fresh vm context, the same way a
// fresh page load runs it once: gLoadHeld's memoized promise (module state
// inside the script's own IIFE) must not leak between scenarios, so every
// scenario below gets its own newPage() rather than sharing one context.
function newPage() {
 const elements = new Map();
 function element(id) {
  if (!elements.has(id)) throw new Error('unexpected getElementById(' + id + ')');
  return elements.get(id);
 }
 const requests = [];
 let replies = [];
 const context = {
  URLSearchParams, console, JSON,
  document: { getElementById: element },
  location: { search: '?k=test-key' },
  fetch: async (path, opts) => {
   const body = opts && opts.body ? JSON.parse(opts.body) : undefined;
   requests.push({ path, key: opts.headers['X-Panel-Key'], method: opts.method, body });
   const reply = replies.shift();
   if (!reply) throw new Error('no reply queued for ' + path);
   if (reply.fail) return { ok: false, text: async () => reply.fail };
   return { ok: true, json: async () => reply.json };
  },
 };
 context.window = context; // grantScript assigns window.oaGrant; window is the global object, same as a browser
 vm.runInNewContext(plainScript, context);
 vm.runInNewContext(script, context);
 assert.strictEqual(typeof context.oaGrant, 'function', 'grantScript must define window.oaGrant');
 return {
  elements, requests,
  setReplies: (r) => { replies = r.slice() },
  oaGrant: context.oaGrant,
 };
}
const settle = () => new Promise(r => setTimeout(r, 0));
const wait = async () => { for (let i = 0; i < 6; i++) await settle() };

(async () => {
 // 1. Every rule held: the button hides, the status line replaces itself
 //    with the one fixed sentence, and the section's own "you need the
 //    rule..." paragraph (the widget's previousElementSibling) hides too,
 //    since the restriction it names no longer applies (task 2026-09-23,
 //    finding 2: the page must not threaten a restriction it does not
 //    enforce).
 {
  const page = newPage();
  const box = grantBox('acct-grant', 'abstraction.job/inventory.read,abstraction.job/acceptance.cancel', 'abstraction.job/acceptance@1');
  const rerun = rerunButton();
  page.elements.set('acct-grant', box);
  page.elements.set('account-start', rerun);
  page.setReplies(heldPage('C:\\OA\\tools\\Abstraction Panel.exe', 'S-1-5-21-1', [
   rule('C:\\OA\\tools\\Abstraction Panel.exe', 'S-1-5-21-1', 'abstraction.job/inventory.read', 'abstraction.job/acceptance@1'),
   rule('C:\\OA\\tools\\Abstraction Panel.exe', 'S-1-5-21-1', 'abstraction.job/acceptance.cancel', 'abstraction.job/acceptance@1'),
  ]));
  page.oaGrant('acct-grant', 'account-start');
  await wait();

  assert.deepStrictEqual(page.requests.map(r => r.path), ['/explore', '/rights?cursor='], 'the on-load check lists policy once');
  assert.strictEqual(box.button.hidden, true, 'every rule held hides the button');
  assert.strictEqual(box.status.className, 'oa-grant-status');
  assert.strictEqual(box.status.textContent, 'Nothing to grant here; you can use this section.');
  assert.strictEqual(box.sentence.hidden, true, 'the section\'s own rule sentence hides once nothing is left to grant');
  assert.strictEqual(rerun.calls, 0, 'holding the rules already is not a grant; the section\'s read is not re-run');
 }

 // 2. Some rules held, some missing: the button stays, its label narrows to
 //    the missing count, and the status line names the ones already held.
 //    A rule not on the account/program this Panel runs as does not count
 //    as held even when the action and resource match.
 {
  const page = newPage();
  const box = grantBox('cred-grant', 'abstraction.credentials/holder.manage,abstraction.credentials/holder.read', 'account');
  page.elements.set('cred-grant', box);
  page.elements.set('allow-list', rerunButton());
  page.setReplies(heldPage('C:\\OA\\tools\\Abstraction Panel.exe', 'S-1-5-21-1', [
   rule('C:\\OA\\tools\\Abstraction Panel.exe', 'S-1-5-21-1', 'abstraction.credentials/holder.read', 'account'),
   rule('C:\\OA\\tools\\Other.exe', 'S-1-5-21-1', 'abstraction.credentials/holder.manage', 'account'),
  ]));
  page.oaGrant('cred-grant', 'allow-list');
  await wait();

  assert.strictEqual(box.button.hidden, false);
  assert.strictEqual(box.button.textContent, 'Grant the missing rule to this Panel');
  assert.strictEqual(box.status.textContent, 'This Panel holds abstraction.credentials/holder.read.');
  assert.strictEqual(box.sentence.hidden, undefined, 'the partly-held state keeps the section\'s own rule sentence');
 }

 // 3. Two widgets on one page share the one listing (one /explore, one
 //    /rights?cursor= total, not one per widget), and a widget with more
 //    than one rule still missing gets the plural label.
 {
  const page = newPage();
  const acct = grantBox('acct-grant', 'abstraction.job/inventory.read,abstraction.job/acceptance.cancel', 'abstraction.job/acceptance@1');
  const card = grantBox('holders', 'abstraction.resource/table.read,abstraction.resource/table.write', 'account');
  page.elements.set('acct-grant', acct);
  page.elements.set('account-start', rerunButton());
  page.elements.set('holders', card);
  page.elements.set('refresh', rerunButton());
  page.setReplies(heldPage('C:\\OA\\tools\\Abstraction Panel.exe', 'S-1-5-21-1', [
   rule('C:\\OA\\tools\\Abstraction Panel.exe', 'S-1-5-21-1', 'abstraction.job/inventory.read', 'abstraction.job/acceptance@1'),
   rule('C:\\OA\\tools\\Abstraction Panel.exe', 'S-1-5-21-1', 'abstraction.job/acceptance.cancel', 'abstraction.job/acceptance@1'),
  ]));
  page.oaGrant('acct-grant', 'account-start');
  page.oaGrant('holders', 'refresh');
  await wait();

  assert.deepStrictEqual(page.requests.map(r => r.path), ['/explore', '/rights?cursor='], 'one listing serves every widget on the page');
  assert.strictEqual(acct.button.hidden, true, 'the first widget holds every rule it names');
  assert.strictEqual(acct.sentence.hidden, true, 'the first widget\'s rule sentence hides along with its button');
  assert.strictEqual(card.button.hidden, undefined, 'the second widget holds none of the rules it names, so it is left untouched');
  assert.strictEqual(card.button.textContent, 'Grant these to this Panel', 'none held: today\'s behaviour, label untouched');
  assert.strictEqual(card.status.textContent, '', 'none held: today\'s behaviour, no status line');
  assert.strictEqual(card.sentence.hidden, undefined, 'none held: the second widget\'s rule sentence is left untouched too');
 }

 // 4. A widget with three rules, one held: the label counts the two still
 //    missing.
 {
  const page = newPage();
  const box = grantBox('reg-grant', 'abstraction.inference/host.manage,abstraction.router/inventory.read|abstraction.router/inventory,abstraction.router/inventory.write|abstraction.router/inventory', 'account');
  page.elements.set('reg-grant', box);
  page.elements.set('registry-read', rerunButton());
  page.setReplies(heldPage('C:\\OA\\tools\\Abstraction Panel.exe', 'S-1-5-21-1', [
   rule('C:\\OA\\tools\\Abstraction Panel.exe', 'S-1-5-21-1', 'abstraction.inference/host.manage', 'account'),
  ]));
  page.oaGrant('reg-grant', 'registry-read');
  await wait();

  assert.strictEqual(box.button.hidden, false);
  assert.strictEqual(box.button.textContent, 'Grant the 2 missing rules to this Panel');
  assert.strictEqual(box.status.textContent, 'This Panel holds abstraction.inference/host.manage.');
  assert.strictEqual(box.sentence.hidden, undefined, 'the partly-held state keeps the section\'s own rule sentence');
 }

 // 5. The listing itself refused or unavailable: nothing about the widget
 //    changes, not even the status line, rather than invent a state from a
 //    partial answer.
 {
  const page = newPage();
  const box = grantBox('card-grant', 'abstraction.resource/table.read', 'account');
  page.elements.set('card-grant', box);
  page.elements.set('refresh', rerunButton());
  page.setReplies([
   { json: { self: { program: 'C:\\OA\\tools\\Abstraction Panel.exe', account: 'S-1-5-21-1' } } },
   { json: { Outcome: 'unavailable' } },
  ]);
  page.oaGrant('card-grant', 'refresh');
  await wait();

  assert.strictEqual(box.button.hidden, undefined, 'an unavailable listing leaves the button exactly as rendered');
  assert.strictEqual(box.button.textContent, 'Grant these to this Panel');
  assert.strictEqual(box.status.textContent, '', 'an unavailable listing writes no status line');
 }

 // 6. gLoadHeld follows Next across pages until Complete, so a rule held on
 //    a later page still counts.
 {
  const page = newPage();
  const box = grantBox('acct-grant', 'abstraction.job/inventory.read', 'abstraction.job/acceptance@1');
  page.elements.set('acct-grant', box);
  page.elements.set('account-start', rerunButton());
  page.setReplies([
   { json: { self: { program: 'C:\\OA\\tools\\Abstraction Panel.exe', account: 'S-1-5-21-1' } } },
   { json: { Outcome: 'page', Revision: 'list-rev', Catalog: [], Rules: [rule('C:\\OA\\tools\\Other.exe', 'S-1-5-21-1', 'abstraction.job/inventory.read', 'abstraction.job/acceptance@1')], Next: 'cursor-2', Complete: false } },
   { json: { Outcome: 'page', Revision: 'list-rev', Catalog: [], Rules: [rule('C:\\OA\\tools\\Abstraction Panel.exe', 'S-1-5-21-1', 'abstraction.job/inventory.read', 'abstraction.job/acceptance@1')], Next: '', Complete: true } },
  ]);
  page.oaGrant('acct-grant', 'account-start');
  await wait();

  assert.deepStrictEqual(page.requests.map(r => r.path), ['/explore', '/rights?cursor=', '/rights?cursor=cursor-2']);
  assert.strictEqual(box.button.hidden, true, 'the rule held on the second page still counts');
  assert.strictEqual(box.sentence.hidden, true);
 }

 // 7. The click flow: two rules sharing the box's default resource. Posts
 //    the exact body at the listed revision, chains to each edit's own
 //    returned revision, re-reads (clicks the section's own Show button)
 //    once every rule lands, and settles into the holds state without a
 //    reload or another listing call, since every rule it asked for just
 //    landed.
 {
  const page = newPage();
  const box = grantBox('acct-grant', 'abstraction.job/inventory.read,abstraction.job/acceptance.cancel', 'abstraction.job/acceptance@1');
  const rerun = rerunButton();
  page.elements.set('acct-grant', box);
  page.elements.set('account-start', rerun);
  page.setReplies([
   ...heldPage('C:\\OA\\tools\\Abstraction Panel.exe', 'S-1-5-21-1', []), // on load: nothing held yet
   { json: { self: { program: 'C:\\OA\\tools\\Abstraction Panel.exe', account: 'S-1-5-21-1' } } },
   { json: { Outcome: 'page', Revision: 'rev-1' } },
   { json: { Outcome: 'applied', Revision: 'rev-2' } },
   { json: { Outcome: 'applied', Revision: 'rev-3' } },
  ]);
  page.oaGrant('acct-grant', 'account-start');
  await wait(); // let the on-load check finish before the click, so the two flows' requests don't interleave
  assert.strictEqual(box.button.hidden, undefined, 'nothing held yet: the button stays');
  assert.strictEqual(typeof box.button.onclick, 'function');
  box.button.onclick();
  await wait();

  assert.deepStrictEqual(page.requests.slice(2).map(r => r.path), ['/explore', '/rights?cursor=', '/rights', '/rights']);
  for (const r of page.requests) assert.strictEqual(r.key, 'test-key');
  assert.deepStrictEqual(page.requests[4].body, { edit: 'set', revision: 'rev-1', account: 'S-1-5-21-1', program: 'C:\\OA\\tools\\Abstraction Panel.exe', action: 'abstraction.job/inventory.read', resource: 'abstraction.job/acceptance@1', permit: true });
  assert.deepStrictEqual(page.requests[5].body, { edit: 'set', revision: 'rev-2', account: 'S-1-5-21-1', program: 'C:\\OA\\tools\\Abstraction Panel.exe', action: 'abstraction.job/acceptance.cancel', resource: 'abstraction.job/acceptance@1', permit: true });
  assert.strictEqual(box.button.hidden, true, 'a full grant re-evaluates and lands in the holds state without a reload');
  assert.strictEqual(box.status.className, 'oa-grant-status');
  assert.strictEqual(box.status.textContent, 'Nothing to grant here; you can use this section.');
  assert.strictEqual(box.sentence.hidden, true, 'the rule sentence hides once the grant lands in the holds state');
  assert.strictEqual(box.button.disabled, false);
  assert.strictEqual(rerun.calls, 1, 'the section\'s own read button is clicked again on success');
 }

 // 8. A rule token naming its own resource ("<action>|<resource>") overrides
 //    the box's default resource for that one rule only.
 {
  const page = newPage();
  const box = grantBox('reg-grant', 'abstraction.inference/host.manage,abstraction.router/inventory.read|abstraction.router/inventory', 'account');
  const rerun = rerunButton();
  page.elements.set('reg-grant', box);
  page.elements.set('registry-read', rerun);
  page.setReplies([
   ...heldPage('C:\\OA\\tools\\Abstraction Panel.exe', 'S-1-5-21-1', []),
   { json: { self: { program: 'C:\\OA\\tools\\Abstraction Panel.exe', account: 'S-1-5-21-1' } } },
   { json: { Outcome: 'page', Revision: 'rev-1' } },
   { json: { Outcome: 'applied', Revision: 'rev-2' } },
   { json: { Outcome: 'applied', Revision: 'rev-3' } },
  ]);
  page.oaGrant('reg-grant', 'registry-read');
  await wait();
  box.button.onclick();
  await wait();

  assert.strictEqual(page.requests[4].body.resource, 'account', 'a bare action uses the box\'s default resource');
  assert.strictEqual(page.requests[5].body.resource, 'abstraction.router/inventory', 'an action|resource token names its own resource');
  assert.strictEqual(rerun.calls, 1);
  assert.strictEqual(box.button.hidden, true);
  assert.strictEqual(box.sentence.hidden, true);
 }

 // 9. Listing the policy itself refused, from the click flow: the typed
 //    reason is shown inline, a "forbidden" outcome also names the programs
 //    that may administer policy instead of asking to elevate this Panel,
 //    no edit is attempted, and the section's read is not re-run.
 {
  const page = newPage();
  const box = grantBox('card-grant', 'abstraction.resource/table.read', 'account');
  const rerun = rerunButton();
  page.elements.set('card-grant', box);
  page.elements.set('refresh', rerun);
  page.setReplies([
   ...heldPage('C:\\OA\\tools\\Abstraction Panel.exe', 'S-1-5-21-1', []),
   { json: { self: { program: 'C:\\OA\\tools\\Abstraction Panel.exe', account: 'S-1-5-21-1' } } },
   { json: { Outcome: 'forbidden' } },
  ]);
  page.oaGrant('card-grant', 'refresh');
  await wait();
  box.button.onclick();
  await wait();

  assert.deepStrictEqual(page.requests.slice(2).map(r => r.path), ['/explore', '/rights?cursor=']);
  assert.strictEqual(box.status.textContent, 'The rights service refused: this Panel may not administer policy. The operator programs this installation grants are the runtime itself and the programs installed beside it: openabstractions, openabstractionsw and Abstraction Panel.');
  assert.strictEqual(box.status.className, 'oa-grant-status error');
  assert.strictEqual(box.button.disabled, false, 'the button re-enables after a refusal');
  assert.strictEqual(box.button.hidden, undefined, 'a refusal never claims the rules are held');
  assert.strictEqual(rerun.calls, 0, 'a refusal never re-runs the section\'s own read');
 }

 // 10. A rule refused partway through the bundle: the rules that already
 //     landed are not claimed granted, the typed reason names the outcome,
 //     and no later rule in the list is sent.
 {
  const page = newPage();
  const box = grantBox('cred-grant', 'abstraction.credentials/holder.manage,abstraction.credentials/holder.read', 'account');
  const rerun = rerunButton();
  page.elements.set('cred-grant', box);
  page.elements.set('allow-list', rerun);
  page.setReplies([
   ...heldPage('C:\\OA\\tools\\Abstraction Panel.exe', 'S-1-5-21-1', []),
   { json: { self: { program: 'C:\\OA\\tools\\Abstraction Panel.exe', account: 'S-1-5-21-1' } } },
   { json: { Outcome: 'page', Revision: 'rev-1' } },
   { json: { Outcome: 'applied', Revision: 'rev-2' } },
   { json: { Outcome: 'conflict', Current: { Permit: false } } },
  ]);
  page.oaGrant('cred-grant', 'allow-list');
  await wait();
  box.button.onclick();
  await wait();

  assert.strictEqual(page.requests.length, 6, 'the rule after the refused one is never sent');
  assert.strictEqual(box.status.textContent, 'The rights service refused: the policy changed since it was listed; list again before another edit.');
  assert.strictEqual(box.status.className, 'oa-grant-status error');
  assert.strictEqual(box.button.hidden, undefined);
  assert.strictEqual(rerun.calls, 0);
 }

 // 11. A transport failure (the fetch itself rejects, not a typed outcome)
 //     shows the error and still re-enables the button.
 {
  const page = newPage();
  const box = grantBox('acct-grant2', 'abstraction.job/inventory.read', 'abstraction.job/acceptance@1');
  page.elements.set('acct-grant2', box);
  page.elements.set('account-start', rerunButton());
  page.setReplies([
   ...heldPage('C:\\OA\\tools\\Abstraction Panel.exe', 'S-1-5-21-1', []),
   { fail: 'panel key required' },
  ]);
  page.oaGrant('acct-grant2', 'account-start');
  await wait();
  box.button.onclick();
  await wait();

  assert.strictEqual(box.status.textContent, 'panel key required');
  assert.strictEqual(box.status.className, 'oa-grant-status error');
  assert.strictEqual(box.button.disabled, false);
  assert.strictEqual(box.button.hidden, undefined);
 }

 // 12. The section's own "you need the rule <raw action>..." sentence is
 //     rewritten in plain words, from the same ActionPlain map the one
 //     /rights?cursor= listing already carries (task 2026-09-23, finding 3):
 //     "You need permission to <plain phrase>", one clause per rule the
 //     widget names, joined with "and". An action this runtime does not
 //     name in that map falls back to oaActionFallback's generic phrase
 //     (task 2026-09-23, sixth first-time visitor, finding 2). Neither ever
 //     names the raw action in the sentence itself (task 2026-09-23, seventh
 //     first-time visitor, finding 2: an id is never visible page text); the
 //     sentence's own title attribute carries every id instead.
 {
  const page = newPage();
  const box = grantBox('acct-grant', 'abstraction.job/inventory.read,abstraction.job/acceptance.cancel', 'abstraction.job/acceptance@1');
  page.elements.set('acct-grant', box);
  page.elements.set('account-start', rerunButton());
  page.setReplies([
   { json: { self: { program: 'C:\\OA\\tools\\Abstraction Panel.exe', account: 'S-1-5-21-1' } } },
   { json: { Outcome: 'page', Revision: 'list-rev', Catalog: [], ActionPlain: { 'abstraction.job/inventory.read': 'jobs all' }, Rules: [], Next: '', Complete: true } },
  ]);
  page.oaGrant('acct-grant', 'account-start');
  await wait();

  assert.strictEqual(box.sentence.textContent, 'You need permission to Jobs all and permission to Job: acceptance cancel to use this section.', 'a mapped action reads in plain words, capitalized; an unmapped one falls back to oaActionFallback\'s generic phrase; neither names the raw id in the sentence');
  assert.strictEqual(box.sentence.title, 'abstraction.job/inventory.read, abstraction.job/acceptance.cancel', 'every resolved id sits in the sentence\'s own title instead');
  assert.strictEqual(box.sentence.hidden, undefined, 'none held: the rewrite alone does not hide the sentence');
 }

 console.log('PASS panel shell grant control');
})().catch(e => { console.error(e); process.exit(1); });
