// Run with node monitor/setup_sac_page_test.js. SAC-OPENCODE-INSTALL: when
// Windows Smart App Control blocked OpenCode's own installer, the OpenCode
// row says so in one plain sentence, offers Retry, and keeps the Windows
// records under Technical details. Retry reads the Catalog again.
const assert = require('assert');
const h = require('./testdata/setup_page_test_helper.js');

const sentence = "Windows Smart App Control blocked OpenCode's installer. Turn off Smart App Control in Windows Security, install OpenCode again, then select Retry.";
const detail = 'Smart App Control: on. The OpenCode installer registered version 1.18.34, and OpenCode.exe is missing. Code Integrity 3077 at 2026-11-22T10:56:54Z blocked nsis7z.dll under VerifiedAndReputableDesktop (3089 signatures: 0).';

const routes = world => path => {
  if (path === '/setup/catalog') return h.catalog;
  if (path === '/setup/state') return h.freshState();
  if (path === '/setup/overview') { world.reads++; return world.overview(); }
  if (path === '/setup/other-copies') return {copies: []};
  if (path.startsWith('/setup/feature')) return {installed: true, available: true};
  if (path === '/setup/modelhost/coding') return {status: {plan: h.overview().coding_model, assets: []}};
  if (path === '/setup/modelhost/chat') return {available: true, feature: {installed: true}, engine: {staged: true}, pin: {}};
  if (path === '/setup/install') return {current_catalog: true, approved: false, install: {}};
  return undefined;
};
const button = p => p.parts('opencode').details.children[0].children.find(c => c.tagName === 'BUTTON' && c.id === 'setup-action-opencode');

(async () => {
  const blocked = {app: 'opencode', coding: true, installed: false, blocked: true, reason: sentence, detail};
  const world = {reads: 0, overview: () => h.overview({desktop_integration: blocked})};
  const p = await h.page(routes(world));
  const row = p.parts('opencode');
  assert.strictEqual(row.line, sentence);
  assert.strictEqual(row.lineState, 'error');
  assert(!/3077|nsis7z|VerifiedAndReputable/.test(row.line), 'no identifiers in the visible sentence');
  const retry = button(p);
  assert(retry && !retry.hidden && !retry.disabled, 'Retry is offered');
  assert.strictEqual(retry.textContent, 'Retry');
  const others = p.parts('opencode').details.children[0].children.filter(c => c.tagName === 'BUTTON' && c !== retry && !c.hidden);
  assert.strictEqual(others.length, 0, 'Retry is the only button: ' + others.map(c => c.textContent).join(', '));
  assert(h.allText(row.body).includes('Technical details'), h.allText(row.body));
  assert(h.allText(row.body).includes(detail), h.allText(row.body));

  // The person installs OpenCode again; Retry reads the Catalog and the row
  // offers the integration. It starts no installer itself.
  world.overview = () => h.overview({desktop_integration: {app: 'opencode', coding: true, installed: true, action: 'install'}});
  const before = world.reads;
  await retry.onclick({preventDefault() {}});
  await h.settled();
  assert(world.reads > before, 'Retry read the Catalog again');
  assert.strictEqual(p.posts('/setup/desktop-integration').length, 0, 'Retry starts no installer');
  assert(!/Smart App Control/.test(p.parts('opencode').line), p.parts('opencode').line);

  // An OpenCode that is simply not installed keeps its ordinary row.
  const plain = await h.page(routes({reads: 0, overview: () => h.overview()}));
  assert.strictEqual(plain.parts('opencode').line, 'Get opens opencode.ai, where you download and install OpenCode yourself. Then Connect links it to OpenAbstractions.');
  assert(!button(plain) || button(plain).hidden, 'no Retry without a block');
  console.log('setup_sac_page_test: ok');
})().catch(e => { console.error(e); process.exit(1); });
