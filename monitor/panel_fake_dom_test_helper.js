// A small fake DOM for the Panel page tests (node, no browser). Its nodes
// are browser-shaped, so static/panel_rules.js reads them as it reads a real
// page, and a page script can update rows in place:
//   nodeType, tagName (upper case), id, className, hidden, value, type,
//   getAttribute / setAttribute / hasAttribute / removeAttribute, dataset
//   (kept in step with data-* attributes), children, childNodes,
//   focus / blur with document.activeElement (removing a subtree that holds
//   it moves it to body),
//   firstElementChild, parentNode, appendChild, append, insertBefore, removeChild,
//   replaceChild, replaceChildren, remove, textContent (set replaces the
//   children with one text node), and querySelector / querySelectorAll for
//   tag, #id, .class and [attr] / [attr="value"] parts, compounded and
//   joined by spaces (descendant).
//
// Usage:
//   const { fakeDocument } = require('./panel_fake_dom_test_helper.js');
//   const doc = fakeDocument();
//   const list = doc.element('apps', 'div');   // the markup's <div id="apps">
//   context.document = doc;                     // getElementById, createElement
//   ... run the page script ...
//   assertPanelRules(doc.body, 'Apps');
'use strict';

function dataKey(name) { return name.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase()); }
function dataName(key) { return 'data-' + key.replace(/[A-Z]/g, (c) => '-' + c.toLowerCase()); }

class FakeText {
 constructor(data) { this.nodeType = 3; this.data = String(data); this.parentNode = null; }
 get textContent() { return this.data; }
}

class FakeElement {
 constructor(tag, ownerDocument) {
  this.nodeType = 1;
  this.tagName = String(tag).toUpperCase();
  this.ownerDocument = ownerDocument;
  this.childNodes = [];
  this.parentNode = null;
  this.attributes = new Map();
  this.value = '';
  this.listeners = {};
  const element = this;
  this.dataset = new Proxy({}, {
   get: (_, key) => (typeof key === 'string' ? element.attributes.get(dataName(key)) : undefined),
   set: (_, key, value) => { element.attributes.set(dataName(key), String(value)); return true; },
   deleteProperty: (_, key) => { element.attributes.delete(dataName(key)); return true; },
   has: (_, key) => element.attributes.has(dataName(key)),
   ownKeys: () => [...element.attributes.keys()].filter((k) => k.startsWith('data-')).map(dataKey),
   getOwnPropertyDescriptor: (_, key) => (element.attributes.has(dataName(key)) ? { enumerable: true, configurable: true, value: element.attributes.get(dataName(key)) } : undefined),
  });
 }
 getAttribute(name) { return this.attributes.has(name) ? this.attributes.get(name) : null; }
 setAttribute(name, value) { this.attributes.set(name, String(value)); }
 hasAttribute(name) { return this.attributes.has(name); }
 removeAttribute(name) { this.attributes.delete(name); }
 get id() { return this.getAttribute('id') || ''; }
 set id(v) { this.setAttribute('id', v); }
 get className() { return this.getAttribute('class') || ''; }
 set className(v) { this.setAttribute('class', v); }
 get classList() {
  const element = this;
  const list = () => element.className.split(/\s+/).filter(Boolean);
  const classes = {
   contains: (c) => list().includes(c),
   add: (...c) => { element.className = [...new Set([...list(), ...c])].join(' '); },
   remove: (...c) => { element.className = list().filter((x) => !c.includes(x)).join(' '); },
   toggle: (c, on) => { const want = on === undefined ? !list().includes(c) : !!on; if (want) classes.add(c); else classes.remove(c); return want; },
  };
  return classes;
 }
 get hidden() { return this.hasAttribute('hidden'); }
 set hidden(v) { if (v) this.setAttribute('hidden', ''); else this.removeAttribute('hidden'); }
 get type() { return this.getAttribute('type') || ''; }
 set type(v) { this.setAttribute('type', v); }
 get title() { return this.getAttribute('title') || ''; }
 set title(v) { this.setAttribute('title', v); }
 get children() { return this.childNodes.filter((n) => n.nodeType === 1); }
 get firstElementChild() { return this.children[0] || null; }
 get textContent() { return this.childNodes.map((n) => n.textContent).join(''); }
 set textContent(text) { this.replaceChildren(); if (text !== '' && text !== null && text !== undefined) this.appendChild(new FakeText(text)); }
 adopt(node) {
  const child = typeof node === 'string' ? new FakeText(node) : node;
  if (child.parentNode) child.parentNode.removeChild(child);
  child.parentNode = this;
  return child;
 }
 appendChild(node) { const child = this.adopt(node); this.childNodes.push(child); return child; }
 append(...nodes) { for (const n of nodes) this.appendChild(n); }
 removeChild(node) {
  const i = this.childNodes.indexOf(node);
  if (i === -1) throw new Error('removeChild: not a child');
  blurInside(node);
  this.childNodes.splice(i, 1); node.parentNode = null; return node;
 }
 // focus makes this the document's activeElement, as a browser does for a
 // connected element, and records the options it was given.
 focus(options) {
  const doc = this.ownerDocument;
  if (!doc || !isConnected(this)) return;
  doc.activeElement = this;
  this.focusOptions = options || {};
  this.focusCount = (this.focusCount || 0) + 1;
 }
 blur() { const doc = this.ownerDocument; if (doc && doc.activeElement === this) doc.activeElement = doc.body; }
 replaceChild(next, old) {
  const i = this.childNodes.indexOf(old);
  if (i === -1) throw new Error('replaceChild: not a child');
  const child = this.adopt(next);
  this.childNodes.splice(this.childNodes.indexOf(old), 1, child); old.parentNode = null; return old;
 }
 insertBefore(node, ref) {
  if (ref === null || ref === undefined) return this.appendChild(node);
  const child = this.adopt(node);
  const i = this.childNodes.indexOf(ref);
  if (i === -1) throw new Error('insertBefore: the reference is not a child');
  this.childNodes.splice(i, 0, child);
  return child;
 }
 replaceChildren(...nodes) { for (const n of this.childNodes) { blurInside(n); n.parentNode = null; } this.childNodes = []; this.append(...nodes); }
 remove() { if (this.parentNode) this.parentNode.removeChild(this); }
 addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); }
 dispatch(type, event = {}) { for (const fn of this.listeners[type] || []) fn(event); if (typeof this['on' + type] === 'function') this['on' + type](event); }
 click() { this.dispatch('click'); }
 descendants() { const out = []; const walk = (n) => { for (const c of n.children) { out.push(c); walk(c); } }; walk(this); return out; }
 matches(selector) { return matchChain(this, parseSelector(selector)); }
 querySelectorAll(selector) {
  const groups = selector.split(',').map((s) => parseSelector(s.trim()));
  return this.descendants().filter((n) => groups.some((g) => matchChain(n, g)));
 }
 querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
}

// The focus model: removing a subtree that holds document.activeElement
// (removeChild, a move by appendChild or insertBefore, replaceChildren)
// moves focus to body, as a browser does.
function blurInside(node) {
 const doc = node && node.ownerDocument;
 if (!doc || !doc.activeElement) return;
 for (let p = doc.activeElement; p; p = p.parentNode) if (p === node) { doc.activeElement = doc.body; return; }
}
function isConnected(node) {
 const doc = node.ownerDocument;
 for (let p = node; p; p = p.parentNode) if (p === doc.documentElement) return true;
 return false;
}

// A selector is compound parts joined by spaces; each part is an optional
// tag, then #id, .class and [attr] or [attr="value"] pieces.
function parseSelector(selector) {
 return selector.trim().split(/\s+/).filter(Boolean).map((part) => {
  const m = /^([A-Za-z][A-Za-z0-9-]*|\*)?/.exec(part);
  const compound = { tag: m[1] && m[1] !== '*' ? m[1].toUpperCase() : '', ids: [], classes: [], attrs: [] };
  const piece = /#([\w-]+)|\.([\w-]+)|\[\s*([\w:-]+)\s*(?:=\s*(?:"([^"]*)"|'([^']*)'|([^\]\s]+)))?\s*\]/g;
  let p;
  piece.lastIndex = m[0].length;
  while ((p = piece.exec(part))) {
   if (p[1]) compound.ids.push(p[1]);
   else if (p[2]) compound.classes.push(p[2]);
   else compound.attrs.push({ name: p[3], value: p[4] !== undefined ? p[4] : p[5] !== undefined ? p[5] : p[6] });
  }
  return compound;
 });
}
function matchCompound(node, c) {
 if (!node || node.nodeType !== 1) return false;
 if (c.tag && node.tagName !== c.tag) return false;
 if (c.ids.some((id) => node.id !== id)) return false;
 if (c.classes.some((cls) => !node.classList.contains(cls))) return false;
 return c.attrs.every((a) => node.hasAttribute(a.name) && (a.value === undefined || node.getAttribute(a.name) === a.value));
}
function matchChain(node, chain) {
 if (!chain.length || !matchCompound(node, chain[chain.length - 1])) return false;
 let i = chain.length - 2, at = node.parentNode;
 while (i >= 0 && at) { if (matchCompound(at, chain[i])) i--; at = at.parentNode; }
 return i < 0;
}

function fakeDocument() {
 const doc = { listeners: {}, visibilityState: 'visible' };
 doc.createElement = (tag) => new FakeElement(tag, doc);
 doc.createTextNode = (text) => new FakeText(text);
 doc.documentElement = doc.createElement('html');
 doc.body = doc.createElement('body');
 doc.documentElement.appendChild(doc.body);
 doc.activeElement = doc.body;
 doc.getElementById = (id) => (doc.documentElement.id === id ? doc.documentElement : doc.documentElement.querySelector('#' + id));
 doc.querySelector = (s) => doc.documentElement.querySelector(s);
 doc.querySelectorAll = (s) => doc.documentElement.querySelectorAll(s);
 doc.addEventListener = (type, fn) => { (doc.listeners[type] = doc.listeners[type] || []).push(fn); };
 // element is the markup's own element by id: created under body (or under
 // parent) the first time a test names it.
 doc.element = (id, tag = 'div', parent = doc.body) => {
  let found = doc.getElementById(id);
  if (!found) { found = doc.createElement(tag); found.id = id; parent.appendChild(found); }
  return found;
 };
 return doc;
}

// assertLiveUpdateKeepsRow is the live-update check every page with live
// rows runs (SPEC §4: pages update live while visible; every button writes
// one sentence into its row's status line). It clicks, lets the fake server's
// state change, runs one live tick, and asserts the row is the same element
// and its status line still reads the click's sentence. data returns the
// fake server data the row's list is read from; the change must alter it,
// or the check fails, since an unchanged list proves nothing:
//   await assertLiveUpdateKeepsRow({
//     click: () => row.querySelector('button').onclick(),
//     change: () => { serverState.jobs[0].state = 'cancelling'; },
//     data: () => serverState.jobs,
//     tick: async () => { await interval(); await settle(); },
//     row: () => doc.getElementById('jobs').children[0],
//     status: (row) => row.querySelector('.oa-status'),
//     label: 'Activity, background work',
//   });
// row and status are functions, read before and after, so a page that
// rebuilds the row fails. It returns { row, sentence } from the click.
async function assertLiveUpdateKeepsRow({ click, change, data, tick, row, status, label = 'page' }) {
 const assert = require('assert');
 assert.strictEqual(typeof change, 'function', label + ': pass change, the step that moves the fake server on');
 assert.strictEqual(typeof data, 'function', label + ': pass data, a function returning the fake server data the row\'s list is read from');
 if (click) await click();
 const rowBefore = row();
 assert(rowBefore, label + ': no row before the live update');
 const statusBefore = status(rowBefore);
 assert(statusBefore, label + ': the row has no status line');
 const sentence = statusBefore.textContent;
 assert(String(sentence).trim() !== '', label + ': the click wrote no sentence into the row status line');
 const dataBefore = JSON.stringify(data());
 await change();
 assert.notStrictEqual(JSON.stringify(data()), dataBefore, label + ': the change left the list\'s data as it was, so the live update proves nothing; change what the list under test reads');
 await tick();
 const rowAfter = row();
 assert.strictEqual(rowAfter, rowBefore, label + ': the live update replaced the row element');
 assert.strictEqual(status(rowAfter), statusBefore, label + ': the live update replaced the row status line');
 assert.strictEqual(statusBefore.textContent, sentence, label + ': the live update rewrote the row sentence');
 return { row: rowBefore, sentence };
}

// allowlistedClasses names the classes testdata/panel_lint.allowlist still
// allows one page (its class:<name> rows), for assertPanelRules's
// allowClasses option: a page test passes them until its slice removes the
// rows, and the rendered-DOM class rule then checks every class.
//   assertPanelRules(root, 'Device', { allowClasses: allowlistedClasses('card') });
function allowlistedClasses(page) {
 const fs = require('fs'), path = require('path');
 const text = fs.readFileSync(path.join(__dirname, 'testdata', 'panel_lint.allowlist'), 'utf8');
 const out = [];
 for (const line of text.split('\n')) {
  const fields = line.trim().split('\t');
  if (fields.length === 3 && fields[0] === page && fields[1].startsWith('class:')) out.push(fields[1].slice('class:'.length));
 }
 return out;
}

// knownValues is one page's known-value rows of testdata/panel_lint.allowlist
// ("<page>\tknown-value:program:<file name>\t<count>"), for assertPanelRules's
// knownValues option: a program file name the page still shows, owned by
// the fix named in the row's comment, allowed exactly that many times.
//   assertPanelRules(root, 'Activity', { knownValues: knownValues('work'), knownExact: true });
function knownValues(page) {
 const fs = require('fs'), path = require('path');
 const text = fs.readFileSync(path.join(__dirname, 'testdata', 'panel_lint.allowlist'), 'utf8');
 const out = {};
 for (const line of text.split('\n')) {
  const fields = line.trim().split('\t');
  if (fields.length !== 3 || fields[0] !== page || !fields[1].startsWith('known-value:')) continue;
  const [, shape, ...rest] = fields[1].split(':');
  if (shape !== 'program') throw new Error('known-value rows take the program shape only: ' + line.trim());
  out[rest.join(':')] = Number(fields[2]);
 }
 return out;
}

// knownSummaries is one page's technical-summary rows of
// testdata/panel_lint.allowlist ("<page>\ttechnical-summary:<summary>\t<count>"),
// for assertPanelRules's knownSummaries option: an expander marked
// data-oa-technical under another summary, owned by the fix named in the
// row's comment.
function knownSummaries(page) {
 const fs = require('fs'), path = require('path');
 const text = fs.readFileSync(path.join(__dirname, 'testdata', 'panel_lint.allowlist'), 'utf8');
 const out = {};
 for (const line of text.split('\n')) {
  const fields = line.trim().split('\t');
  if (fields.length === 3 && fields[0] === page && fields[1].startsWith('technical-summary:')) out[fields[1].slice('technical-summary:'.length)] = Number(fields[2]);
 }
 return out;
}

module.exports = { fakeDocument, FakeElement, FakeText, parseSelector, assertLiveUpdateKeepsRow, allowlistedClasses, knownValues, knownSummaries };
