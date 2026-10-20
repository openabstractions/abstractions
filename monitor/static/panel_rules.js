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
  };
  var RULES = ['raw-value', 'empty-input', 'button-status', 'caption', 'class', 'secret-scope', 'technical-summary'];

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
  // shapeGuard drops a program match that is part of an address: followed
  // by "/" (a URL path), by ":" and a digit (a port), or right after "://"
  // (a URL's host). panel_lint_test.go's shapeMatches applies the same
  // guard; the Go and JS cross-check holds them equal.
  function shapeGuard(name, text, start, end) {
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

  function panelRuleViolations(root, options) {
    var rules = (options && options.rules) || RULES, found = [];
    function on(rule) { return rules.indexOf(rule) !== -1; }
    function add(rule, detail) { found.push({ rule: rule, detail: detail }); }
    var classes = options && options.classes !== undefined ? checkClassList(options.classes, 'options.classes') : CLASSES;
    if (on('class') && !classes) add('class', 'the class list (static/panel_classes.json) is not loaded: call setClasses, or pass options.classes');
    var known = checkKnownValues(options && options.knownValues);
    var knownSummaries = checkKnownSummaries(options && options.knownSummaries);
    function walk(node, ancestors, inTechnical) {
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
      if (on('secret-scope') && hasAttr(node, 'data-oa-secret') && !secretScopeValid(node)) {
        add('secret-scope', 'data-oa-secret on ' + describe(node) + ': valid only on an input, textarea, code or output with no child elements');
      }
      if (on('raw-value') && !inTechnical) {
        var shown = judgedShapes(secret, known);
        rawValues(ownText(node)).filter(shown).forEach(function (v) { add('raw-value', v.shape + ' ' + JSON.stringify(v.match) + ' in ' + JSON.stringify(words(ownText(node)).slice(0, 80))); });
        var placeholder = attr(node, 'placeholder');
        rawValues(placeholder).filter(shown).forEach(function (v) { add('raw-value', v.shape + ' ' + JSON.stringify(v.match) + ' in the placeholder of ' + describe(node)); });
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
        if (said !== TECHNICAL_SUMMARY) {
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
      kids(node).forEach(function (child) { walk(child, next, body ? tag(child) !== 'summary' : inTechnical); });
    }
    (Array.isArray(root) ? root : [root]).forEach(function (r) { walk(r, [], false); });
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
  exports.CLASSES = CLASSES;
  exports.setClasses = setClasses;
  exports.rawValues = rawValues;
  exports.panelRuleViolations = panelRuleViolations;
  exports.assertPanelRules = assertPanelRules;
})(typeof module !== 'undefined' && module.exports ? module.exports : (this.oaPanelRules = {}));
