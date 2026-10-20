// Run with node monitor/panel_collections_page_test.js; no browser or service is started.
//
// A browser's element.children is an HTMLCollection, and childNodes and
// querySelectorAll answer a NodeList: neither has map, filter, some or find.
// The Models page called .some on a row's children and drew no model row in
// any browser while its test, whose fake children was an Array, passed
// (2026-11-01). This test holds every Panel page script to Array.from (or
// another copy) before an Array method, and holds the shared fake DOM to the
// browser's collection shapes so a page test fails the same way a browser does.
'use strict';
const fs = require('fs'), path = require('path'), assert = require('assert');
const { fakeDocument, FakeHTMLCollection, FakeNodeList } = require('./panel_fake_dom_test_helper.js');

// Array methods neither collection has. NodeList's own forEach, entries,
// keys and values are allowed on childNodes and querySelectorAll's answer.
const arrayOnly = ['some', 'map', 'filter', 'find', 'findIndex', 'findLast', 'every', 'reduce', 'includes', 'indexOf', 'flatMap', 'flat', 'at', 'slice', 'concat', 'join', 'sort', 'reverse'];
const collectionOnly = ['forEach'];

// The page scripts: the Go sources that carry them and the static scripts.
function pageSources() {
  const out = [];
  for (const name of fs.readdirSync(__dirname).sort()) {
    if (name.endsWith('.go') && !name.endsWith('_test.go')) out.push(name);
  }
  for (const name of fs.readdirSync(path.join(__dirname, 'static')).sort()) {
    if (name.endsWith('.js')) out.push(path.join('static', name));
  }
  return out;
}

// copied reports whether the parenthesised expression closing at close is
// the argument of Array.from or Array.prototype.<m>.call, which copy a
// collection into an Array.
function copied(text, close) {
  let depth = 0;
  for (let i = close; i >= 0; i--) {
    if (text[i] === ')') depth++;
    else if (text[i] === '(') {
      depth--;
      if (depth === 0) return /(Array\.from|\.call)\s*$/.test(text.slice(Math.max(0, i - 40), i));
    }
  }
  return false;
}

// findings lists every Array method called straight on a DOM collection in
// text: x.children.some(...), (x.children||[]).map(...) and
// document.querySelectorAll(...).filter(...).
function findings(text) {
  const out = [];
  const methods = (list) => '(' + list.join('|') + ')';
  const property = new RegExp('\\.(children|childNodes|selectedOptions)(\\|\\|\\[\\])?(\\))?\\.' + methods([...arrayOnly, ...collectionOnly]) + '\\(', 'g');
  let m;
  while ((m = property.exec(text))) {
    const method = m[4];
    if (m[1] === 'childNodes' && collectionOnly.includes(method)) continue;
    if (m[3] && copied(text, m.index + m[0].indexOf(')', m[1].length))) continue;
    out.push(m[0]);
  }
  const query = /(querySelectorAll|getElementsBy[A-Za-z]+)\(/g;
  while ((m = query.exec(text))) {
    let depth = 0, i = m.index + m[0].length - 1;
    for (; i < text.length; i++) {
      if (text[i] === '(') depth++;
      else if (text[i] === ')' && --depth === 0) break;
    }
    const after = /^\.([A-Za-z]+)\(/.exec(text.slice(i + 1));
    if (!after) continue;
    const allowed = m[1] === 'querySelectorAll' ? collectionOnly : [];
    if (arrayOnly.includes(after[1]) || (collectionOnly.includes(after[1]) && !allowed.includes(after[1]))) out.push(m[0] + '...).' + after[1] + '(');
  }
  return out;
}

// The scanner finds the shapes that broke the Models page and passes the
// copied forms.
assert.deepStrictEqual(findings("armed=(row.parts.control.children||[]).some(b=>b)"), ['.children||[]).some(']);
assert.deepStrictEqual(findings('box.children.map(r=>r)'), ['.children.map(']);
assert.deepStrictEqual(findings("document.querySelectorAll('a').filter(x=>x)"), ['querySelectorAll(...).filter(']);
assert.deepStrictEqual(findings("Array.from(row.parts.control.children||[]).some(b=>b)"), []);
assert.deepStrictEqual(findings('Array.prototype.slice.call(list.children||[]).map(r=>r)'), []);
assert.deepStrictEqual(findings("Array.from(document.querySelectorAll('a')).filter(x=>x)"), []);
assert.deepStrictEqual(findings("document.querySelectorAll('a').forEach(x=>x)"), []);
assert.deepStrictEqual(findings('node.childNodes.forEach(x=>x)'), []);

const found = [];
for (const name of pageSources()) {
  for (const hit of findings(fs.readFileSync(path.join(__dirname, name), 'utf8'))) found.push(name + ': ' + hit);
}
assert.deepStrictEqual(found, [], 'a Panel page script calls an Array method on a DOM collection; copy it with Array.from first');

// The shared fake DOM answers the browser's shapes.
const doc = fakeDocument();
const list = doc.element('list');
list.append(doc.createElement('div'), 'text', doc.createElement('span'));
assert(list.children instanceof FakeHTMLCollection && !Array.isArray(list.children), 'children is an HTMLCollection stand-in');
assert(list.childNodes instanceof FakeNodeList && !Array.isArray(list.childNodes), 'childNodes is a NodeList stand-in');
assert(doc.querySelectorAll('div') instanceof FakeNodeList, "querySelectorAll answers a NodeList stand-in");
for (const method of arrayOnly) {
  assert.strictEqual(list.children[method], undefined, 'children has no ' + method);
  assert.strictEqual(list.childNodes[method], undefined, 'childNodes has no ' + method);
}
assert.strictEqual(list.children.forEach, undefined, 'an HTMLCollection has no forEach');
assert.strictEqual(typeof list.childNodes.forEach, 'function', 'a NodeList has forEach');
assert.strictEqual(list.children.length, 2);
assert.strictEqual(list.children[1].tagName, 'SPAN');
assert.strictEqual(list.children.item(0).tagName, 'DIV');
assert.strictEqual(list.children.item(2), null);
assert.deepStrictEqual(Array.from(list.children).map((n) => n.tagName), ['DIV', 'SPAN']);
assert.deepStrictEqual([...list.childNodes].length, 3);
assert.throws(() => list.children.some(() => true), TypeError, 'a page script calling some on children fails as in a browser');
console.log('PASS panel collections');
