// Run with node monitor/setup_ticks_page_test.js. Setup is one page of
// ticks (architect ruling 2026-11-09): each row arrives showing what is
// installed, an installed app without a model arrives with its recommended
// model ticked, engines are chosen for the computer and ticked by what needs
// them, and the summary row says in one sentence what the one button does
// or the one reason it cannot.
const assert = require('assert');
const h = require('./testdata/setup_page_test_helper.js');
const {GB} = h;

const routes = (state, over, plan, catalog = h.catalog) => (path, body) => {
  if (path === '/setup/catalog') return catalog;
  if (path === '/setup/state') return typeof state === 'function' ? state() : state;
  if (path === '/setup/overview') return over;
  if (path === '/setup/other-copies') return {copies: []};
  if (path.startsWith('/setup/feature')) return {installed: true, available: true};
  if (path === '/setup/modelhost/coding') return {status: {plan: over.coding_model, assets: []}};
  if (path === '/setup/modelhost/chat') return {available: true, feature: {installed: true}, engine: {staged: true}, pin: {}};
  if (path === '/setup/install') return {current_catalog: true, approved: false, install: {}};
  if (path === '/setup/plan') return plan ? plan(body) : h.planFor(body, ['pytorch-engine', 'thenoise-engine']);
  return undefined;
};

(async () => {
  // The owner's computer: every row says what it is. ComfyUI is no package
  // (owner, 2026-11-11): its row has no tick and offers Get ComfyUI. With no
  // picture app installed, no picture model arrives ticked.
  {
    const p = await h.page(routes(h.ownerState(), h.ownerOverview()));
    assert.deepStrictEqual(p.rowIDs('setup-app-rows'), ['setup-row-comfyui', 'setup-row-opencode'], 'Lemonade, not in this release, has no row');
    assert.deepStrictEqual(p.rowIDs('setup-engine-rows'), ['setup-row-modelhost', 'setup-row-pytorch', 'setup-row-thenoise'], 'engines, no app and no runtime');
    const line = id => p.parts(id).line;
    assert.strictEqual(p.parts('comfyui').box, null, 'ComfyUI has no tick');
    assert.strictEqual(line('comfyui'), 'Not installed');
    assert.strictEqual(line('opencode'), 'Installed · uses Qwen3.6 35B · remove in Windows Settings');
    assert.strictEqual(line('coding'), 'Installed · needed by OpenCode · 22.9 GB');
    // One tick rule: an installed row something needs, or that another page
    // removes, is locked and its state line says why; an installed row this
    // page can remove stays changeable.
    assert.strictEqual(p.parts('opencode').box, null);
    for (const id of ['coding', 'pytorch', 'thenoise']) {
      const row = p.parts(id);
      assert.strictEqual(row.box.checked, true, id + ' arrives ticked');
      if (row.box.disabled) assert(/ · (needed by|remove in) /.test(row.line), id + ' is locked without a reason: ' + row.line);
      else assert(!/needed by|remove in/.test(row.line), id + ' is changeable but reads locked: ' + row.line);
    }
    assert.deepStrictEqual(['coding', 'pytorch', 'thenoise'].filter(id => !p.parts(id).box.disabled), ['pytorch', 'thenoise'], 'only rows Setup can remove are changeable');
    // OpenCode's details link straight to Windows Settings at Apps.
    assert(h.facts(p.parts('opencode').body).includes('Remove: Open Windows Settings'), h.facts(p.parts('opencode').body).join(' | '));
    const remove = p.parts('opencode').body.children.find(c => c.className === 'oa-row' && c.children[0].children[0].textContent === 'Remove');
    const link = remove.children[1].children.find(c => c.tagName === 'A');
    assert.strictEqual(link.href, 'ms-settings:appsfeatures');
    assert.strictEqual(link.target, '_blank', 'the link leaves the Panel through its external opener');
    assert.strictEqual(line('model:klein-compact'), '8.27 GB download');
    assert.strictEqual(p.parts('model:klein-compact').caption, 'Recommended for this computer');
    assert.strictEqual(p.parts('model:klein-compact').box.checked, false, 'no installed picture app lacks it');
    assert.strictEqual(line('pytorch'), 'Installed · not used by any app · 1.67 GB');
    assert.strictEqual(p.parts('pytorch').title, 'Diffusers');
    assert.strictEqual(p.parts('pytorch').caption, 'Pictures and video on AMD graphics');
    assert.strictEqual(p.parts('modelhost').title, 'llama.cpp');
    assert.strictEqual(p.parts('modelhost').caption, 'Chat and embeddings');
    assert.strictEqual(p.parts('thenoise').caption, 'Pictures on the processor');
    assert.strictEqual(line('thenoise'), 'Installed · not used by any app · 4.82 GB');
    assert.strictEqual(p.parts('modelhost').line, 'Included with OpenAbstractions · Ready');
    assert.strictEqual(p.parts('modelhost').box, null, 'the model host has no tick');
    assert.strictEqual(p.get('setup-engine-choice').textContent, 'Chosen for this computer: Diffusers for pictures, llama.cpp for chat.');
    // No person-facing text names the runtime or a retired name.
    const words = h.lists.map(id => h.allText(p.get(id))).join(' ') + p.get('setup-engine-choice').textContent + p.summary().sentence;
    assert(!/PyTorch|ROCm|OpenAbstractions picture engine|Model host/.test(words), words.match(/.*(PyTorch|ROCm|OpenAbstractions picture engine|Model host).*/));
    assert.strictEqual(p.parts('oa').caption, 'Version 0.8.0');
    assert.deepStrictEqual(p.summary(), {sentence: 'Tick to install. Clear a tick to remove.', facts: '', button: 'Install', disabled: true});
    assert.strictEqual(p.context.document.documentElement.dataset.ready, '1', 'the page marks itself ready once its first reads answer');
    // A picture model ticks the engine it runs in; the rows' sizes and the
    // total come from the one plan.
    await p.tick('model:klein-compact');
    assert.strictEqual(line('pytorch'), 'Installed · needed by FLUX.2 Klein 4B compact · 1.67 GB');
    assert.strictEqual(p.parts('pytorch').box.disabled, true, 'an engine a model needs cannot be cleared');
    assert.deepStrictEqual(p.posts('/setup/plan').at(-1).body, {selected: ['pytorch', 'thenoise'], coding_model_plan_id: '', picture_model_plan_ids: ['klein-compact']},
      'the plan keeps every installed package and prepares the ticked model');
    assert.deepStrictEqual(p.summary(), {sentence: 'Adds FLUX.2 Klein 4B compact.', facts: '8.27 GB download · needs 10.3 GB · 400 GB free on C:', button: 'Install', disabled: false});
    await p.tick('model:klein-full');
    assert.strictEqual(p.summary().sentence, 'Adds FLUX.2 Klein 4B compact and FLUX.2 Klein 4B full precision.');
    assert.strictEqual(p.summary().facts, '24.4 GB download · needs 28.4 GB · 400 GB free on C:', 'one total: 8.27 + 16.1 GB');
    await p.tick('model:z-image');
    assert.deepStrictEqual(p.posts('/setup/plan').at(-1).body.picture_model_plan_ids, ['klein-compact', 'klein-full', 'z-image']);
    // Raw values sit in Technical details; publishers are names.
    const detail = h.facts(p.parts('thenoise').body);
    assert(detail.includes('Publisher: Lemonade SDK'), detail.join('\n'));
    assert(detail.includes('Licence: Apache-2.0 and bundled dependency licences Read it'), 'one spelling, as the Licence label: ' + detail.join('\n'));
    assert(detail.includes('Size on disk: 4.82 GB'), detail.join('\n'));
    // ComfyUI's details name its maker and offer a portable folder.
    const comfy = h.facts(p.parts('comfyui').body);
    for (const fact of ['Publisher: Comfy Org', 'Licence: GPL-3.0 Read it', 'Source: View the source code', 'Portable ComfyUI: Choose folder…']) assert(comfy.includes(fact), fact + ' missing: ' + comfy.join(' | '));
    // Diffusers shows its own facts, not those of the runtime it runs on.
    const diffusers = h.facts(p.parts('pytorch').body);
    for (const fact of ['Publisher: Hugging Face', 'Licence: Apache-2.0 Read it', 'Source: View the source code', 'Size on disk: 1.67 GB']) assert(diffusers.includes(fact), fact + ' missing: ' + diffusers.join(' | '));
    assert(!diffusers.some(f => /^Version|PSF|2\.11/.test(f)), 'runtime facts on Diffusers: ' + diffusers.join(' | '));
  }

  // A saved plan naming an item this release does not carry (Lemonade) is
  // dropped silently: no row, never in a request, and Install stays enabled.
  {
    const state = h.ownerState({plan: {id: 'plan-old', selected: ['lemonade', 'pytorch', 'thenoise'], components: []}});
    const p = await h.page(routes(state, h.ownerOverview()));
    assert.strictEqual(p.row('lemonade'), undefined);
    await p.tick('model:klein-compact');
    for (const r of p.posts('/setup/plan')) assert(!r.body.selected.includes('lemonade'), 'a request named Lemonade');
    assert.strictEqual(p.summary().disabled, false, 'Install is enabled for the rest: ' + p.summary().sentence);
  }

  {
    const id = 'declared-renderer';
    const component = {id: id + '-engine', artifact: {version: '1.0', sha256: 'c'.repeat(64), bytes: GB}};
    const catalog = {
      choices: [...h.catalog.choices, {id, title: 'Declared Renderer', components: [component.id]}],
      components: [...h.catalog.components, component],
    };
    const over = h.ownerOverview({
      offered: ['pytorch', 'thenoise', id, 'overview-only'],
      engines: [...h.engines(),
        {id, title: 'Declared Renderer', kind: 'package'},
        {id: 'overview-only', title: 'Unknown Engine', kind: 'package'}],
    });
    const p = await h.page(routes(h.ownerState({catalog}), over, undefined, catalog));
    assert.strictEqual(p.parts(id).box.disabled, false);
    assert.strictEqual(p.row('overview-only'), undefined);
    await p.tick(id);
    assert.deepStrictEqual(p.posts('/setup/plan').at(-1).body.selected, ['pytorch', 'thenoise', id]);
    assert.strictEqual(p.summary().sentence, 'Adds Declared Renderer.');
  }

  {
    const engine = {id: 'lemonade', title: 'Lemonade', kind: 'package'};
    const base = h.ownerState();
    const state = {...base, components: [...base.components, h.installedRow('lemonade-engine')]};
    const over = h.ownerOverview({offered: ['pytorch', 'thenoise', engine.id], engines: [...h.engines(), engine]});
    const p = await h.page(routes(state, over));
    assert.strictEqual(p.parts(engine.id).box.checked, true);
    assert.strictEqual(p.parts(engine.id).box.disabled, true);
    assert.strictEqual(p.parts(engine.id).line, 'Installed · managed by OpenAbstractions');
  }

  // Not enough space: the reason names the free space and the one untick
  // that fits.
  {
    const p = await h.page(routes(h.ownerState(), h.ownerOverview(), body => h.planFor(body, ['pytorch-engine', 'thenoise-engine'], {disk: {volume: 'C:', free_bytes: 20 * GB}})));
    await p.tick('model:klein-compact');
    assert.strictEqual(p.summary().sentence, 'Adds FLUX.2 Klein 4B compact.', 'the compact model fits in 20 GB');
    await p.tick('model:z-image');
    assert.deepStrictEqual(p.summary(), {sentence: 'Not enough space on C:. Clear Z-Image-Turbo or free 13 GB.', facts: '29 GB download · needs 33 GB · 20 GB free on C:', button: 'Install', disabled: true});
    assert.strictEqual(p.notices.filter(n => n.kind === 'blocked' && n.sentence.startsWith('Not enough space')).length, 1, 'the reason is logged once');
  }

  // Clearing an installed engine nothing uses removes it with the one
  // button, with no plan and no installer step.
  {
    let state = h.ownerState();
    const p = await h.page(routes(() => state, h.ownerOverview({picture_models: [{...h.compact(), prepared: true}, h.full(), h.zimage()]}), body => h.planFor(body, ['pytorch-engine', 'thenoise-engine'])));
    assert.deepStrictEqual(p.summary(), {sentence: 'Tick to install. Clear a tick to remove.', facts: '', button: 'Install', disabled: true}, 'nothing to change');
    assert.strictEqual(p.parts('model:klein-compact').line, 'Installed · remove it in Models · 8.27 GB');
    assert.strictEqual(p.parts('model:klein-compact').box.disabled, true, 'Models removes models');
    await p.tick('thenoise', false);
    assert.strictEqual(p.parts('thenoise').line, 'Will be removed · frees 4.82 GB');
    assert.deepStrictEqual(p.summary(), {sentence: 'Removes TheNoise.', facts: 'frees 4.82 GB', button: 'Apply changes', disabled: false});
    const removed = [];
    const handle = routes(() => state, h.ownerOverview());
    p.context.fetch = (orig => async (path, options) => { if (path === '/setup/engine/remove') { removed.push(JSON.parse(options.body)); state = h.ownerState({components: [h.installedRow('pytorch-engine'), {id: 'thenoise-engine', effect: {phase: 'pending'}}]}); return {ok: true, json: async () => state}; } return orig(path, options); })(p.context.fetch);
    await p.click();
    assert.deepStrictEqual(removed, [{engine: 'thenoise', plan_id: 'plan-owner', confirm: true}]);
    assert.strictEqual(p.requests.filter(r => r.path === '/setup/install' && r.body).length, 0, 'a removal approves no install');
    assert.strictEqual(p.summary().sentence, 'Changes applied.');
    void handle;
  }

  // A fresh computer: nothing ticked; ComfyUI offers Get ComfyUI, and each
  // engine ticks on its own.
  {
    const p = await h.page(routes(h.freshState(), h.overview(), body => h.planFor(body, [])));
    assert.deepStrictEqual(p.summary(), {sentence: 'Tick to install. Clear a tick to remove.', facts: '', button: 'Install', disabled: true});
    assert.strictEqual(p.parts('comfyui').line, 'Not installed');
    assert.strictEqual(p.parts('opencode').line, 'This build can integrate an existing OpenCode Desktop installation.');
    // Every engine this computer is offered can be ticked on its own (the
    // R17 engine chooser could not select PyTorch at all).
    for (const engine of ['pytorch', 'thenoise']) {
      assert.strictEqual(p.parts(engine).box.disabled, false, engine + ' cannot be ticked');
      await p.tick(engine);
      assert.deepStrictEqual(p.posts('/setup/plan').at(-1).body.selected, [engine], engine + ' is not what the plan installs');
      await p.tick(engine, false);
    }
    // A picture model ticks the engine it runs in; the installer-only app
    // with no installer cannot be ticked and says why.
    await p.tick('model:klein-full');
    assert.strictEqual(p.parts('pytorch').box.checked, true, 'the engine is ticked for the model');
    assert.strictEqual(p.parts('pytorch').box.disabled, true);
    assert.deepStrictEqual(p.posts('/setup/plan').at(-1).body.selected, ['pytorch']);
    assert.strictEqual(p.parts('pytorch').line, 'Needed by FLUX.2 Klein 4B full precision · 1.2 GB download');
  }
  {
    const p = await h.page(routes(h.freshState(), h.overview(), body => h.planFor(body, [])));
    assert.strictEqual(p.parts('opencode').box, null);
    assert(!h.facts(p.parts('opencode').body).some(text => text.includes('OpenAbstractions installer')));
  }

  // Engines are the catalogue's and the core's: Lemonade, installed by the
  // core, shows its real state without a tick; on a computer without AMD
  // graphics Diffusers has no row.
  {
    const p = await h.page(routes(h.ownerState(), h.ownerOverview({engines: h.engines({ready: false})})));
    assert.deepStrictEqual(p.rowIDs('setup-engine-rows'), ['setup-row-modelhost', 'setup-row-lemonade', 'setup-row-pytorch', 'setup-row-thenoise']);
    assert.deepStrictEqual([p.parts('lemonade').title, p.parts('lemonade').caption, p.parts('lemonade').line], ['Lemonade', 'Pictures, speech and transcription', 'Included with OpenAbstractions · No picture model yet']);
    assert.strictEqual(p.parts('lemonade').box, null);
    const other = await h.page(routes(h.freshState(), h.overview({offered: ['thenoise'], picture_engine: '', picture_models: [], engines: h.engines({ready: true}).filter(e => e.id !== 'pytorch')}), body => h.planFor(body, [])));
    assert.deepStrictEqual(other.rowIDs('setup-engine-rows'), ['setup-row-modelhost', 'setup-row-lemonade', 'setup-row-thenoise'], 'three engines without AMD graphics');
    assert.strictEqual(other.parts('lemonade').line, 'Included with OpenAbstractions · Model in place');
    assert.strictEqual(other.get('setup-engine-choice').textContent, 'Chosen for this computer: llama.cpp for chat.');
  }

  // The release pins Lemonade's engine package on Windows x64: the owner's
  // computer, without it, shows Lemonade offered with an empty tick.
  {
    const pinned = {...h.catalog, components: h.catalog.components.map(c => c.id !== 'lemonade-engine' ? c
      : {id: 'lemonade-engine', title: 'Lemonade (OA build)', artifact: {version: 'v2026.39.1', sha256: 'd'.repeat(64), bytes: 0.005 * GB, publisher: 'lemonade-sdk', license: 'Apache-2.0'}})};
    const lemonade = {id: 'lemonade', title: 'Lemonade', caption: 'Pictures, speech and transcription', kind: 'package'};
    const over = h.ownerOverview({offered: ['lemonade', 'thenoise', 'pytorch'], engines: [h.engines()[0], lemonade, ...h.engines().slice(1)]});
    const base = routes(h.ownerState({catalog: pinned}), over);
    const p = await h.page((path, body) => path === '/setup/catalog' ? pinned : base(path, body));
    assert.deepStrictEqual(p.rowIDs('setup-engine-rows'), ['setup-row-modelhost', 'setup-row-lemonade', 'setup-row-pytorch', 'setup-row-thenoise']);
    assert.strictEqual(p.parts('lemonade').title, 'Lemonade');
    assert(p.parts('lemonade').box, 'the pinned Lemonade engine has a tick');
    assert.strictEqual(p.parts('lemonade').box.checked, false, 'Lemonade is not installed on this computer');
    assert.strictEqual(p.parts('lemonade').box.disabled, false, 'Lemonade can be ticked to install it');
    assert(!/Installed/.test(p.parts('lemonade').line), p.parts('lemonade').line);
  }

  // A failed state read shows one sentence and Try again; every row stays.
  {
    let fail = true;
    const p = await h.page(routes(() => fail ? new Error('Failed to fetch') : h.ownerState(), h.ownerOverview()));
    assert.deepStrictEqual(p.summary(), {sentence: 'Setup could not read what is installed.', facts: '', button: 'Try again', disabled: false});
    assert(p.row('comfyui') && p.row('model:klein-compact'), 'the rows stay on the page');
    assert.strictEqual(p.parts('pytorch').box.disabled, true, 'no tick changes before Setup knows what is installed');
    assert(!JSON.stringify(p.summary()).includes('Failed to fetch'), 'no raw error on the page');
    fail = false;
    await p.click();
    assert.strictEqual(p.summary().sentence, 'Tick to install. Clear a tick to remove.');
    assert.strictEqual(p.parts('pytorch').box.disabled, false);
  }
  // A picture model installs per user, even where the
  // engine's adapter reads missing (an isolated runtime).
  {
    const missing = (state, over) => { const base = routes(state, over); return (path, body) => path.startsWith('/setup/feature') ? {installed: false, available: true} : base(path, body); };
    const p = await h.page(missing(h.ownerState(), h.ownerOverview()));
    await p.tick('model:klein-compact');
    assert.strictEqual(p.summary().sentence, 'Adds FLUX.2 Klein 4B compact.', 'a single model asks Windows for nothing');
    const fresh = await h.page(missing(h.freshState(), h.overview()));
    assert.strictEqual(fresh.parts('opencode').box, null);
    await fresh.tick('thenoise');
    assert(!fresh.summary().sentence.includes('Windows'), fresh.summary().sentence);
  }

  // A preview build's version reads without its build suffix; an update
  // between two builds of one version names both in full.
  {
    const preview = JSON.parse(JSON.stringify(h.catalog));
    preview.components.find(c => c.id === 'thenoise-engine').artifact.version = '0.9.1-r17';
    const p = await h.page(routes(h.ownerState({catalog: preview}), h.ownerOverview()));
    assert(h.facts(p.parts('thenoise').body).includes('Version: 0.9.1'), h.facts(p.parts('thenoise').body).join(' | '));
    const earlier = h.ownerState({catalog: preview, current_catalog: false, plan: {id: 'plan-r16', selected: ['pytorch', 'thenoise'], components: preview.components.filter(c => ['pytorch-engine', 'thenoise-engine'].includes(c.id)).map(c => c.id === 'thenoise-engine' ? {...c, artifact: {...c.artifact, version: '0.9.1-r16', sha256: 'd'.repeat(64)}} : c)}});
    const update = await h.page(routes(earlier, h.ownerOverview()));
    assert(h.facts(update.parts('thenoise').body).includes('Version: 0.9.1-r17 (installed: 0.9.1-r16)'), h.facts(update.parts('thenoise').body).join(' | '));
  }
  console.log('setup ticks page: ok');
})().catch(error => { console.error(error); process.exit(1); });
