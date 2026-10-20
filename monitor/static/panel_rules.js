// The Panel rules (research/panel-redesign/SPEC.md §4 and §6; the owner's
// Panel rules of 2026-10-07) as a small module page tests apply to the DOM
// their page script rendered. monitor/panel_lint_test.go applies the same
// rules to every page's served HTML and reads SHAPES from this file, so the
// raw-value shapes have one source.
//
// It walks the root it is given and nothing else: a browser DOM node, or a
// page test's fake node with children, textContent, tagName, className,
// hidden, value, type, placeholder and role or getAttribute. A node without
// a tagName is a plain container. Rules:
//   raw-value      visible text or a placeholder shaped like a path, a SID, a
//                  hash, a raw action id (abstraction.<layer>/<action>) or a
//                  program file name (a word ending in .exe, .app or .dll);
//                  a title or hidden text is not visible; a Technical
//                  details body (details marked data-oa-technical, or
//                  .oa-error-details) is exempt from this rule only, and
//                  every other expander's body, open or closed, is judged;
//                  a secret field's own text and placeholder are exempt
//                  from the hash shape only.
//   empty-input    a visible empty text input or textarea; a secret field,
//                  search and data-oa-other (an "Other…" input) are exempt.
//   secret-scope   data-oa-secret anywhere but an input, textarea, code or
//                  output with no child elements.
//   technical-summary  data-oa-technical on an expander whose summary does
//                  not read "Technical details"; options.knownSummaries
//                  takes a page's technical-summary:<summary> rows.
//   A secret field is a valid data-oa-secret, a password input, or an input
//   or textarea with a password or one-time-code autocomplete. An exemption
//   applies to its own element and only to the shape it exists for.
//   button-status  a button with no status line (role=status, aria-live,
//                  class oa-status, <output>) in its oa-row, else in its
//                  section.
//   caption        a second caption (a paragraph with its own text that is
//                  no status line and no empty state) directly in a section.
//   class          a class on an element, built in markup or by a function,
//                  that static/panel_classes.json (SPEC §6) does not list;
//                  a listed prefix (oa-icon-) completed by a name passes.
//                  options.allowClasses names a page's allowlisted classes
//                  (testdata/panel_lint.allowlist) until its slice removes
//                  them.
//   raw-value also takes a visible number of seven or more digits (shape
//                  number): a size or duration reads in human units.
//   raw-name       a row label, an h2 or h3, or one " · " part of a row
//                  caption that is a single lowercase token, the shape of a
//                  registered name or resource ("image-server",
//                  "store:pictures", "awake"), also before "(not
//                  registered)" ("inventoryd (not registered)"); an
//                  upstream lowercase project name ("llama.cpp") passes.
//   label-period   a row label ending in a period ("…" and "..." pass).
//   caption-repeat a row caption equal to its label, to its label behind
//                  "No" or a count ("No questions waiting" under
//                  "Questions waiting"), or to a value the row's control
//                  shows ("Default" beside a chooser reading "Default").
//   runtime-once   "OpenAbstractions is not running" in more than one
//                  visible element, or, with the runtime down
//                  (data-runtime="down" on the root, or
//                  options.runtimeDown), in none; and with the runtime
//                  down, a visible section sentence that only repeats it
//                  (RUNTIME_ECHO: "The list of apps is unavailable right
//                  now.", "Not running", "No runtime is reachable." …).
//   empty-row      a shown row (.oa-row, a navigation row excepted) with a
//                  label and nothing else shown: no caption, value, status
//                  sentence or control (owner, 2026-09-23: "no empty
//                  fields"). A disabled control counts.
//   empty-select   a shown chooser with no option that has words.
//   empty-section  a shown section whose h2 has nothing shown under it but
//                  its caption: no row, sentence or control.
//   The empty-* rules judge what a person sees: an element hidden, styled
//   display:none (a browser node) or inside either is not shown.
//   Every allowance is a row of testdata/panel_lint.allowlist: exact, one
//   rule, counted, shrink-only. options.knownValues takes a page's
//   known-value rows for the program shape ({"<file name>": count}); with
//   options.knownExact a value seen fewer times than its count fails too.
//
// Usage in a node page test:
//   const { assertPanelRules } = require('./static/panel_rules.js');
//   assertPanelRules(root, 'Device');            // throws on a violation
//   panelRuleViolations(root, { rules: ['raw-value'] });
(function (exports) {
  'use strict';

  // One shape per line; panel_lint_test.go parses these lines.
  var SHAPES = {
    path: /\b[a-z]:\\|\\\\[a-z0-9]|%[a-z_]+%\\|(?:^|[\s("'])(?:~|\/Users|\/home|\/Applications|\/Library|\/opt|\/usr|\/var|\/etc|\/tmp)\//gi,
    SID: /\bS-1-\d+(?:-\d+)+/g,
    hash: /\b[0-9a-fA-F]{16,}\b|\bsha256:/g,
    action: /\babstraction\.[a-z][a-z0-9_-]*\/[a-z](?:[A-Za-z0-9_.@-]*[A-Za-z0-9_@-])?/g,
    program: /\b[A-Za-z0-9_][A-Za-z0-9_.-]*\.(?:exe|app|dll)\b/gi,
    number: /\b\d{7,}\b/g,
  };
  // The naming rules judge a page once it adopts them (options.naming, or
  // naming in options.rules): Apps, Permissions and Settings since the
  // ruling of 2026-11-09; Activity, Device, Models and the app page since
  // its second block.
  var NAMING_RULES = ['raw-name', 'label-period', 'caption-repeat'];
  var EMPTY_RULES = ['empty-row', 'empty-select', 'empty-section'];
  var RULES = ['raw-value', 'empty-input', 'button-status', 'caption', 'class', 'secret-scope', 'technical-summary', 'runtime-once'].concat(EMPTY_RULES, NAMING_RULES);
  // runtime-once: the runtime-down sentence is the page's banner (owner,
  // 2026-11-09: one runtime-down sentence per Panel page). At most one
  // visible element outside Technical details says it; while the runtime is
  // down (data-runtime="down" on the document root, or options.runtimeDown)
  // exactly one does.
  var RUNTIME_DOWN = /OpenAbstractions is not running/;
  // RUNTIME_ECHO are the sections' own sentences for a stopped runtime
  // (R19 render check, 2026-11-09). While the banner shows, a page hides
  // them (runtimeEcho in panel_shell.go).
  var RUNTIME_ECHO = /^(The list of [a-z -]+ is unavailable right now\.|The model folders could not be read right now\.|Which models are loaded is unknown right now\.|Model uses are unavailable right now\.|OpenAbstractions is not reachable\.|Settings could not be refreshed\.|Not running)$/;

  // The shell's class names, from the one source panel_shell.go embeds
  // (static/panel_classes.json). Under node the module reads it; a browser
  // has no require, so whoever runs the rules there (the screenshot tool)
  // hands the list in with setClasses, or per call with options.classes.
  // With the class rule on and no list, the rule reports that instead of
  // passing silently.
  var CLASSES = null;
  if (typeof module !== 'undefined' && module && module.exports && typeof require === 'function') {
    try { CLASSES = require('./panel_classes.json'); } catch (e) { CLASSES = null; }
  }
  // setClasses takes the panel_classes.json object and refuses anything
  // else: classes a non-empty array of non-empty strings, prefixes an array
  // (possibly empty) of strings each ending in "-".
  function checkClassList(list, who) {
    var strings = function (a) { return Array.isArray(a) && a.every(function (s) { return typeof s === 'string' && s !== ''; }); };
    if (!list || typeof list !== 'object') throw new Error(who + ' needs the panel_classes.json object: {classes: [...], prefixes: [...]}');
    if (!strings(list.classes) || list.classes.length === 0) throw new Error(who + ': classes must be a non-empty array of class names');
    if (!strings(list.prefixes) || !list.prefixes.every(function (p) { return p.length > 1 && p.charAt(p.length - 1) === '-'; })) {
      throw new Error(who + ': prefixes must be an array of strings each ending in "-" after at least one character');
    }
    return list;
  }
  function setClasses(list) {
    CLASSES = checkClassList(list, 'setClasses');
    exports.CLASSES = list;
  }
  // allowedClass: a listed class, or a completed listed prefix (oa-icon-card).
  function allowedClass(list, c) {
    if ((list.classes || []).indexOf(c) !== -1) return true;
    var prefixes = list.prefixes || [];
    for (var i = 0; i < prefixes.length; i++) if (c.length > prefixes[i].length && c.indexOf(prefixes[i]) === 0) return true;
    return false;
  }

  function tag(node) { return String(node && (node.tagName || node.localName) || '').toLowerCase(); }
  function attr(node, name) {
    if (node && typeof node.getAttribute === 'function') {
      var v = node.getAttribute(name);
      if (v !== null && v !== undefined) return String(v);
    }
    var prop = { 'class': 'className', 'aria-live': 'ariaLive' }[name] || name;
    return node && node[prop] !== undefined && node[prop] !== null && typeof node[prop] !== 'object' && typeof node[prop] !== 'function' ? String(node[prop]) : '';
  }
  function hasAttr(node, name) {
    if (node && typeof node.hasAttribute === 'function') return node.hasAttribute(name);
    // A fake node with getAttribute only: a set attribute may be empty, as a
    // boolean attribute is ("data-oa-technical").
    if (node && typeof node.getAttribute === 'function') {
      var v = node.getAttribute(name);
      if (v !== null && v !== undefined) return true;
    }
    return attr(node, name) !== '';
  }
  function hasClass(node, name) { return (' ' + attr(node, 'class') + ' ').indexOf(' ' + name + ' ') !== -1; }
  function kids(node) {
    var list = node && (node.children || []);
    return Array.prototype.slice.call(list);
  }
  // A browser node keeps its text in text child nodes; a fake node keeps
  // its own text in textContent beside its children.
  function ownText(node) {
    if (node && node.childNodes && typeof node.nodeType === 'number') {
      var text = '';
      for (var i = 0; i < node.childNodes.length; i++) if (node.childNodes[i].nodeType === 3) text += node.childNodes[i].data;
      return text;
    }
    return node && typeof node.textContent === 'string' ? node.textContent : '';
  }
  function words(text) { return String(text || '').replace(/\s+/g, ' ').trim(); }
  function hidden(node) { return node.hidden === true || (typeof node.hasAttribute === 'function' && node.hasAttribute('hidden')); }
  function silent(node) {
    var t = tag(node);
    return t === 'script' || t === 'style' || t === 'template' || t === 'title';
  }
  // A Technical details disclosure is a <details> marked data-oa-technical,
  // or oaShowError's details.oa-error-details (SPEC §6). Its body, the raw
  // values a page keeps out of its sentences, is exempt from raw-value only;
  // its summary, and every other expander's body, open or closed, is
  // checked like the rest of the page.
  // (A test fake built without a tag name counts by its marker alone.)
  function technical(node) {
    var t = tag(node);
    return (t === 'details' || t === '') && (hasAttr(node, 'data-oa-technical') || hasClass(node, 'oa-error-details'));
  }
  function isStatus(node) {
    var live = attr(node, 'aria-live');
    return attr(node, 'role') === 'status' || (live !== '' && live !== 'off') || hasClass(node, 'oa-status') || tag(node) === 'output';
  }
  function containsStatus(node) {
    if (isStatus(node)) return true;
    var list = kids(node);
    for (var i = 0; i < list.length; i++) if (containsStatus(list[i])) return true;
    return false;
  }
  function describe(node) {
    var names = ['id', 'name', 'placeholder', 'aria-label'];
    for (var i = 0; i < names.length; i++) { var v = attr(node, names[i]); if (v) return tag(node) + ' ' + names[i] + '=' + JSON.stringify(v); }
    var text = words(node.textContent);
    return tag(node) + (text ? ' ' + JSON.stringify(text.slice(0, 60)) : '');
  }
  // shapeGuard drops a number match that is a development version's date,
  // and a program match that is part of an address: followed
  // by "/" (a URL path), by ":" and a digit (a port), or right after "://"
  // (a URL's host). panel_lint_test.go's shapeMatches applies the same
  // guard; the Go and JS cross-check holds them equal.
  function shapeGuard(name, text, start, end) {
    // A date-stamped development version ("0.8.0-dev.20261109") is a
    // version, not a raw number.
    if (name === 'number') return text.slice(Math.max(0, start - 4), start) !== 'dev.';
    if (name !== 'program') return true;
    var next = text.charAt(end);
    if (next === '/' || (next === ':' && /[0-9]/.test(text.charAt(end + 1)))) return false;
    return text.slice(Math.max(0, start - 3), start) !== '://';
  }
  function rawValues(text) {
    var found = [], s = String(text || '');
    for (var name in SHAPES) {
      var re = new RegExp(SHAPES[name].source, SHAPES[name].flags), m;
      while ((m = re.exec(s))) {
        if (shapeGuard(name, s, m.index, m.index + m[0].length)) found.push({ shape: name, match: m[0].trim() });
        if (!m[0]) re.lastIndex++;
      }
    }
    return found;
  }
  // An exemption attribute applies to its own element and only to the shape
  // it exists for (SPEC §6). data-oa-secret is valid only on an input,
  // textarea, code or output with no child elements; anywhere else it is a
  // secret-scope violation and exempts nothing. A secret field (a valid
  // data-oa-secret, a password input, or an input or textarea with a
  // password or one-time-code autocomplete) is exempt from empty-input,
  // since a secret is typed, never chosen; and its own text and placeholder
  // are exempt from the hash shape only, since a key shaped like a hash is
  // the secret itself. A path, SID or action id in it is still a violation.
  var SECRET_TAGS = ['input', 'textarea', 'code', 'output'];
  function secretScopeValid(node) {
    return SECRET_TAGS.indexOf(tag(node)) !== -1 && kids(node).length === 0;
  }
  // judgedShapes is the raw-value filter for one element: a secret field's
  // own text and placeholder are not judged for the hash shape, and a
  // known value (below) is spent from its count.
  function judgedShapes(secret, known) {
    return function (v) {
      if (secret && v.shape === 'hash') return false;
      if (v.shape === 'program' && known && Object.prototype.hasOwnProperty.call(known.left, v.match)) {
        known.seen[v.match] = (known.seen[v.match] || 0) + 1;
        if (known.left[v.match] > 0) { known.left[v.match]--; return false; }
      }
      return true;
    };
  }
  // checkKnownValues takes options.knownValues, the known-value rows of
  // testdata/panel_lint.allowlist for one page ({"<file name>": count}): a
  // program file name a page still shows, owned by a named fix. Only the
  // program shape takes known values, and each entry is a whole file name
  // the shape matches (no bare extension, no prefix) with a positive count.
  function checkKnownValues(known) {
    if (known === undefined || known === null) return null;
    if (typeof known !== 'object' || Array.isArray(known)) throw new Error('options.knownValues must be {"<program file name>": count}');
    var left = {};
    Object.keys(known).forEach(function (name) {
      var whole = rawValues(name).filter(function (v) { return v.shape === 'program'; });
      if (whole.length !== 1 || whole[0].match !== name) throw new Error('options.knownValues: ' + JSON.stringify(name) + ' is no whole program file name');
      if (typeof known[name] !== 'number' || known[name] < 1 || known[name] % 1 !== 0) throw new Error('options.knownValues: ' + JSON.stringify(name) + ' needs a positive whole count');
      left[name] = known[name];
    });
    return { counts: known, left: left, seen: {} };
  }
  function isSecret(node) {
    var t = tag(node), auto = attr(node, 'autocomplete');
    if (hasAttr(node, 'data-oa-secret')) return secretScopeValid(node);
    if (t === 'input' && attr(node, 'type') === 'password') return true;
    return (t === 'input' || t === 'textarea') && (auto === 'current-password' || auto === 'new-password' || auto === 'one-time-code');
  }
  // A row's parts (architect ruling 2026-11-09): its first label, its first
  // shown caption and the values its control shows, never a nested row's.
  function findIn(node, pred) {
    var list = kids(node);
    for (var i = 0; i < list.length; i++) {
      if (hidden(list[i]) || silent(list[i])) continue;
      if (pred(list[i])) return list[i];
      var hit = hasClass(list[i], 'oa-row') ? null : findIn(list[i], pred);
      if (hit) return hit;
    }
    return null;
  }
  function plain(text) { return words(text).toLowerCase().replace(/[.]$/, ''); }
  function chosenText(select) {
    var options = kids(select).filter(function (o) { return tag(o) === 'option'; });
    var value = select.value === undefined || select.value === null ? '' : String(select.value);
    var hit = options.filter(function (o) { return String(o.value !== undefined ? o.value : attr(o, 'value')) === value; })[0] || options[0];
    return hit ? words(hit.textContent) : '';
  }
  function shownValues(node, out) {
    kids(node).forEach(function (k) {
      if (hidden(k) || silent(k)) return;
      var t = tag(k);
      if (t === 'select') out.push(plain(chosenText(k)));
      else if (t === 'span' && kids(k).length === 0 && words(k.textContent)) out.push(plain(k.textContent));
      else if (t !== 'button') shownValues(k, out);
    });
    return out;
  }
  // rawName: one lowercase token with a letter and no space, the shape of
  // a registered name, a resource or a program stem ("image-server",
  // "store:pictures", "awake").
  // A "(not registered)" suffix leaves the stem judged ("inventoryd").
  // A project name its upstream writes in lowercase is a name (architect
  // ruling 2026-11-09, second block).
  var UPSTREAM_NAMES = ['llama.cpp', 'whisper.cpp', 'stable-diffusion.cpp'];
  function rawName(text) { text = text.replace(/ [(]not registered[)]$/, ''); return text.length > 1 && UPSTREAM_NAMES.indexOf(text) === -1 && /^[a-z0-9][a-z0-9_.:-]*$/.test(text) && /[a-z]/.test(text); }
  function isCaption(node) {
    return tag(node) === 'p' && !isStatus(node) && !hasClass(node, 'oa-empty') && !hidden(node) && words(ownText(node)) !== '';
  }
  // technicalSummary is the summary text of a details marked
  // data-oa-technical. Only an expander whose summary reads "Technical
  // details" may carry the marker (SPEC §6): the exemption follows the
  // words a person sees.
  var TECHNICAL_SUMMARY = 'Technical details';
  function technicalSummary(node) {
    var s = kids(node).filter(function (k) { return tag(k) === 'summary'; })[0];
    return s ? words(ownText(s)) : '';
  }
  // checkKnownSummaries takes options.knownSummaries, a page's
  // technical-summary:<summary> rows ({"<summary>": count}): a marked
  // expander under another summary, counted, owned by a named fix.
  function checkKnownSummaries(known) {
    if (known === undefined || known === null) return null;
    if (typeof known !== 'object' || Array.isArray(known)) throw new Error('options.knownSummaries must be {"<summary>": count}');
    var left = {};
    Object.keys(known).forEach(function (text) {
      if (words(text) !== text || text === '' || text === TECHNICAL_SUMMARY) throw new Error('options.knownSummaries: ' + JSON.stringify(text) + ' is no other summary');
      if (typeof known[text] !== 'number' || known[text] < 1 || known[text] % 1 !== 0) throw new Error('options.knownSummaries: ' + JSON.stringify(text) + ' needs a positive whole count');
      left[text] = known[text];
    });
    return { counts: known, left: left, seen: {} };
  }

  // A browser element styled display:none (panel.css hides a section's error
  // line while the runtime is down) is not visible; a fake node always is.
  function displayed(node) {
    if (!node || node.nodeType !== 1 || typeof getComputedStyle !== 'function') return true;
    try { return getComputedStyle(node).display !== 'none'; } catch (e) { return true; }
  }
  // holdsSomething: node, or a shown descendant not skipped, has words or
  // is a control a person can see (a disabled one included).
  var CONTROL_TAGS = ['select', 'textarea', 'button', 'a', 'img', 'svg', 'progress', 'meter'];
  function shownNode(node) { return !hidden(node) && !silent(node) && displayed(node); }
  function holdsSomething(node, skip) {
    if (words(ownText(node)) !== '') return true;
    var t = tag(node);
    if (CONTROL_TAGS.indexOf(t) !== -1 || (t === 'input' && attr(node, 'type').toLowerCase() !== 'hidden')) return true;
    var list = kids(node);
    for (var i = 0; i < list.length; i++) {
      if (!shownNode(list[i]) || (skip && skip(list[i]))) continue;
      if (holdsSomething(list[i], skip)) return true;
    }
    return false;
  }
  function wordedOptions(select) {
    var n = 0;
    (function count(node) { kids(node).forEach(function (k) { if (tag(k) === 'option') { if (words(k.textContent) !== '') n++; } else count(k); }); })(select);
    return n;
  }
  function runtimeSentences(root, pattern) {
    var out = [];
    function walk(node, inTechnical) {
      if (!node || typeof node !== 'object' || hidden(node) || silent(node) || !displayed(node)) return;
      if (!inTechnical && (pattern || RUNTIME_DOWN).test(words(ownText(node)))) out.push(node);
      var body = technical(node);
      kids(node).forEach(function (child) { walk(child, body ? tag(child) !== 'summary' : inTechnical); });
    }
    (Array.isArray(root) ? root : [root]).forEach(function (r) { walk(r, false); });
    return out;
  }
  function runtimeDown(root, options) {
    if (options && typeof options.runtimeDown === 'boolean') return options.runtimeDown;
    var r = Array.isArray(root) ? root[0] : root, doc = r && r.ownerDocument, top = doc && doc.documentElement;
    return !!(top && typeof top.getAttribute === 'function' && top.getAttribute('data-runtime') === 'down');
  }

  function panelRuleViolations(root, options) {
    var rules = (options && options.rules) || RULES.filter(function (r) { return (options && options.naming) || NAMING_RULES.indexOf(r) === -1; }), found = [];
    function on(rule) { return rules.indexOf(rule) !== -1; }
    function add(rule, detail) { found.push({ rule: rule, detail: detail }); }
    var classes = options && options.classes !== undefined ? checkClassList(options.classes, 'options.classes') : CLASSES;
    if (on('class') && !classes) add('class', 'the class list (static/panel_classes.json) is not loaded: call setClasses, or pass options.classes');
    var known = checkKnownValues(options && options.knownValues);
    var knownSummaries = checkKnownSummaries(options && options.knownSummaries);
    function walk(node, ancestors, inTechnical, shownAbove) {
      if (!node || typeof node !== 'object') return;
      if (on('class') && classes) {
        // Hidden or not, an element's classes are what the skins style.
        attr(node, 'class').split(/\s+/).filter(Boolean).forEach(function (c) {
          if (!allowedClass(classes, c) && !(options && options.allowClasses && options.allowClasses.indexOf(c) !== -1)) add('class', JSON.stringify(c) + ' on ' + describe(node));
        });
      }
      if (hidden(node) || silent(node)) return;
      var t = tag(node);
      var secret = isSecret(node);
      // Shown: no display:none on the node or above it (the empty-* rules).
      var shownHere = shownAbove !== false && displayed(node);
      if (shownHere && on('empty-row') && hasClass(node, 'oa-row') && t !== 'a' && !hasClass(node, 'oa-row-nav')) {
        var emptyLabel = findIn(node, function (n) { return hasClass(n, 'oa-row-label'); });
        if (emptyLabel && words(emptyLabel.textContent) !== '' && !holdsSomething(node, function (n) { return n === emptyLabel; })) {
          add('empty-row', JSON.stringify(words(emptyLabel.textContent)) + ' has no caption, value or control');
        }
      }
      if (shownHere && on('empty-select') && t === 'select' && wordedOptions(node) === 0) add('empty-select', describe(node));
      if (shownHere && on('empty-section') && t === 'section') {
        var heading = kids(node).filter(function (k) { return tag(k) === 'h2' && shownNode(k); })[0];
        if (heading && !holdsSomething(node, function (n) { return n === heading || (hasClass(n, 'oa-caption') && isCaption(n)); })) {
          add('empty-section', JSON.stringify(words(ownText(heading)) || words(heading.textContent)) + ' has nothing under it');
        }
      }
      if (on('secret-scope') && hasAttr(node, 'data-oa-secret') && !secretScopeValid(node)) {
        add('secret-scope', 'data-oa-secret on ' + describe(node) + ': valid only on an input, textarea, code or output with no child elements');
      }
      if (on('raw-value') && !inTechnical) {
        var shown = judgedShapes(secret, known);
        rawValues(ownText(node)).filter(shown).forEach(function (v) { add('raw-value', v.shape + ' ' + JSON.stringify(v.match) + ' in ' + JSON.stringify(words(ownText(node)).slice(0, 80))); });
        var placeholder = attr(node, 'placeholder');
        rawValues(placeholder).filter(shown).forEach(function (v) { add('raw-value', v.shape + ' ' + JSON.stringify(v.match) + ' in the placeholder of ' + describe(node)); });
      }
      if (on('label-period') && hasClass(node, 'oa-row-label')) {
        var label = words(node.textContent);
        if (/[^.]\.$/.test(label)) add('label-period', JSON.stringify(label));
      }
      if (on('raw-name')) {
        if (hasClass(node, 'oa-row-label') || t === 'h2' || t === 'h3') {
          if (rawName(words(node.textContent))) add('raw-name', t + ' ' + JSON.stringify(words(node.textContent)));
        }
        if (hasClass(node, 'oa-row-caption')) {
          words(node.textContent).split(' · ').forEach(function (part) { if (rawName(part)) add('raw-name', 'caption part ' + JSON.stringify(part) + ' in ' + JSON.stringify(words(node.textContent))); });
        }
      }
      if (on('caption-repeat') && hasClass(node, 'oa-row')) {
        var rowLabel = findIn(node, function (n) { return hasClass(n, 'oa-row-label'); });
        var rowCaption = findIn(node, function (n) { return hasClass(n, 'oa-row-caption'); });
        var rowControl = findIn(node, function (n) { return hasClass(n, 'oa-row-control'); });
        var l = rowLabel ? plain(rowLabel.textContent) : '', c = rowCaption ? plain(rowCaption.textContent) : '';
        if (c && (c === l || (l && c.replace(/^(?:no|\d+) /, '') === l) || (rowControl && shownValues(rowControl, []).indexOf(c) !== -1))) {
          add('caption-repeat', JSON.stringify(words(rowLabel ? rowLabel.textContent : '')) + ' / ' + JSON.stringify(words(rowCaption.textContent)));
        }
      }
      if (on('empty-input') && (t === 'input' || t === 'textarea')) {
        var type = attr(node, 'type').toLowerCase();
        var textual = t === 'textarea' || ['', 'text', 'url', 'email', 'tel', 'number'].indexOf(type) !== -1;
        if (textual && words(node.value) === '' && !secret && !hasAttr(node, 'data-oa-other')) add('empty-input', describe(node));
      }
      if (on('button-status') && t === 'button') {
        var container = null, i;
        for (i = ancestors.length - 1; i >= 0 && !container; i--) if (hasClass(ancestors[i], 'oa-row')) container = ancestors[i];
        for (i = ancestors.length - 1; i >= 0 && !container; i--) if (tag(ancestors[i]) === 'section') container = ancestors[i];
        var target = attr(node, 'data-status');
        if (!(target && options && options.statusIds && options.statusIds.indexOf(target) !== -1) && !(container && containsStatus(container))) add('button-status', describe(node));
      }
      if (on('technical-summary') && hasAttr(node, 'data-oa-technical')) {
        var said = technicalSummary(node);
        if (t !== 'details') {
          add('technical-summary', 'data-oa-technical on ' + describe(node) + ': only a "Technical details" expander may carry it');
        } else if (said !== TECHNICAL_SUMMARY) {
          var spent = false;
          if (knownSummaries && Object.prototype.hasOwnProperty.call(knownSummaries.left, said)) {
            knownSummaries.seen[said] = (knownSummaries.seen[said] || 0) + 1;
            if (knownSummaries.left[said] > 0) { knownSummaries.left[said]--; spent = true; }
          }
          if (!spent) add('technical-summary', 'data-oa-technical on an expander whose summary reads ' + JSON.stringify(said) + ': only a "Technical details" expander may carry it');
        }
      }
      if (on('caption') && t === 'section') {
        var captions = kids(node).filter(isCaption);
        captions.slice(1).forEach(function (c) { add('caption', 'section ' + JSON.stringify(attr(node, 'id')) + ': ' + JSON.stringify(words(ownText(c)))); });
      }
      var next = ancestors.concat([node]), body = technical(node);
      kids(node).forEach(function (child) { walk(child, next, body ? tag(child) !== 'summary' : inTechnical, shownHere); });
    }
    (Array.isArray(root) ? root : [root]).forEach(function (r) { walk(r, [], false, true); });
    if (on('runtime-once')) {
      var said = runtimeSentences(root), down = runtimeDown(root, options);
      if (said.length > 1) add('runtime-once', said.length + ' visible elements say the runtime is not running: ' + said.map(describe).join(', '));
      else if (down && said.length !== 1) add('runtime-once', 'the runtime is down and no visible element says so');
      if (down) runtimeSentences(root, RUNTIME_ECHO).forEach(function (n) { add('runtime-once', 'the runtime is down and a section repeats it: ' + describe(n)); });
    }
    // With knownExact (a page test over its whole fixture), a known value
    // seen fewer times than its row counts means the row must shrink.
    if (known && on('raw-value') && options.knownExact) {
      Object.keys(known.counts).forEach(function (name) {
        var seen = known.seen[name] || 0;
        if (seen < known.counts[name]) add('raw-value', 'known value ' + JSON.stringify(name) + ' allowlisted ' + known.counts[name] + ', seen ' + seen + ': lower or drop its row in testdata/panel_lint.allowlist');
      });
    }
    if (knownSummaries && on('technical-summary') && options.knownExact) {
      Object.keys(knownSummaries.counts).forEach(function (text) {
        var seen = knownSummaries.seen[text] || 0;
        if (seen < knownSummaries.counts[text]) add('technical-summary', 'known summary ' + JSON.stringify(text) + ' allowlisted ' + knownSummaries.counts[text] + ', seen ' + seen + ': lower or drop its row in testdata/panel_lint.allowlist');
      });
    }
    return found;
  }

  function assertPanelRules(root, label, options) {
    var found = panelRuleViolations(root, options);
    if (found.length) {
      throw new Error((label || 'page') + ' breaks the Panel rules:\n  ' + found.map(function (v) { return v.rule + ': ' + v.detail; }).join('\n  '));
    }
  }

  exports.SHAPES = SHAPES;
  exports.RULES = RULES;
  exports.NAMING_RULES = NAMING_RULES;
  exports.EMPTY_RULES = EMPTY_RULES;
  exports.CLASSES = CLASSES;
  exports.setClasses = setClasses;
  exports.rawValues = rawValues;
  exports.panelRuleViolations = panelRuleViolations;
  exports.assertPanelRules = assertPanelRules;
})(typeof module !== 'undefined' && module.exports ? module.exports : (this.oaPanelRules = {}));
