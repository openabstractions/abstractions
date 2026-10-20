import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import readline from 'node:readline';

const nativeBridge = process.argv[2] === '--native-bridge';
const faultOnly = process.argv[2] === '--fault-only';
const pendingBridge = new Map();
let bridgeSerial = 0;
if (nativeBridge) readline.createInterface({input: process.stdin}).on('line', line => {
  const reply = JSON.parse(line), pending = pendingBridge.get(reply.id);
  assert.ok(pending, 'unexpected host response');
  pendingBridge.delete(reply.id); pending(reply);
});

async function hostFetch(url, options) {
  const id = ++bridgeSerial;
  const answer = new Promise(resolve => pendingBridge.set(id, resolve));
  process.stdout.write('panel-fixture:' + JSON.stringify({id, url: url.href, method: options.method || 'GET',
    headers: options.headers, body: options.body ? Buffer.from(options.body).toString('base64') : ''}) + '\n');
  const result = await answer, bytes = Buffer.from(result.body, 'base64');
  return {status: result.status, ok: result.status >= 200 && result.status < 300,
    json: async () => JSON.parse(bytes.toString()), arrayBuffer: async () => Uint8Array.from(bytes).buffer};
}

const root = path.resolve('static'), encoder = new TextEncoder();
const manifest = fs.readFileSync('panel-application.json', 'utf8');
for (const origin of nativeBridge ? ['https://panel.invalid'] : ['https://panel.invalid', 'oa-panel://panel']) {
for (const mode of nativeBridge ? [process.argv[3]] : faultOnly ? ['callback-throw'] : ['ready', 'hidden', 'unready', 'closed', 'wrong-page', 'wrong-origin']) {
  let bootstrapCalls = 0, reads = 0, writes = 0, faults = 0, elapsed = 0, finishRead;
  const answers = [], originalDocument = {documentElement: {dataset: {ready: mode === 'unready' ? '' : '1'}}};
  const frame = {contentDocument: originalDocument, contentWindow: mode === 'closed' ? null : {
    location: new URL((mode === 'wrong-origin' ? origin.replace('://panel', '://foreign') : origin) +
      (mode === 'wrong-page' ? '/work' : '/credentials-page')),
  }};
  Object.defineProperty(frame, 'src', {set() { /* navigation failure remains observable */ }});
  const response = (status, body) => ({status, ok: status >= 200 && status < 300,
    json: async () => JSON.parse(body), arrayBuffer: async () => encoder.encode(body).buffer});
  const fetch = async (url, options) => {
    assert.equal(options.headers['X-Panel-Key'], 'fixture-key');
    assert.equal(url.protocol, new URL(origin).protocol);
    assert.equal(url.host, new URL(origin).host);
    if (url.pathname === '/native-host/visible' && mode === 'callback-throw') throw new Error('controlled callback failure');
    if (nativeBridge) {
      if (url.pathname === '/native-host/bootstrap') bootstrapCalls++;
      if (url.pathname === '/native-host/read') reads++;
      if (url.pathname === '/native-host/write') {
        if (url.searchParams.get('fault') === '1') { faults++; assert.equal(options.body.length, 0); }
        else { writes++; answers.push(JSON.parse(new TextDecoder().decode(options.body))); }
      }
      return hostFetch(url, options);
    }
    switch (url.pathname) {
      case '/native-host/bootstrap':
        bootstrapCalls++;
        return response(200, JSON.stringify({application: 'openabstractions-panel', instance: 'service-issued', manifest}));
      case '/native-host/read':
        if (reads++ === 0) return response(200, JSON.stringify({id: '1', request: JSON.stringify({
          application: 'openabstractions-panel', verb: 'open', payload: {page: 'credentials'},
        })}));
        return new Promise(resolve => { finishRead = () => resolve(response(410, '')); });
      case '/native-host/write': {
        assert.equal(url.searchParams.get('call'), '1');
        if (url.searchParams.get('fault') === '1') { faults++; assert.equal(options.body.length, 0); }
        else { writes++; answers.push(JSON.parse(new TextDecoder().decode(options.body))); }
        // Wake the pending read after this response is accepted.
        setTimeout(() => finishRead?.(), 0);
        return response(204, '');
      }
      case '/native-host/visible': return response(mode === 'hidden' ? 503 : 204, '');
      default: throw new Error(`unexpected route ${url.pathname}`);
    }
  };
  const context = vm.createContext({URL, Uint8Array, TextEncoder, TextDecoder, AbortController,
    location: new URL(origin + '/native-host/shell?k=fixture-key'),
    document: {getElementById(id) { assert.equal(id, 'panel-content'); return frame; }},
    fetch, performance: {now() { elapsed += 1000; return elapsed; }},
    setTimeout(callback) { return setTimeout(callback, 0); }, clearTimeout,
  });
  const modules = new Map();
  function load(filename) {
    if (modules.has(filename)) return modules.get(filename);
    const module = new vm.SourceTextModule(fs.readFileSync(filename, 'utf8'), {context, identifier: filename});
    modules.set(filename, module);
    return module;
  }
  // The host's ./sdk URL maps to the generated native-sdk resource directory.
  const hostFile = path.join(root, 'native-host.mjs');
  const host = new vm.SourceTextModule(fs.readFileSync(hostFile, 'utf8'), {context, identifier: hostFile});
  await host.link((specifier, owner) => {
    const target = owner.identifier === hostFile
      ? path.join(root, 'native-sdk', specifier.slice('./sdk/'.length))
      : specifier === '@openabstractions/download-request/internal'
        ? path.join(root, 'native-sdk/abstraction-download/javascript/js/abstraction/download/request/internal.mjs')
        : specifier === '@openabstractions/rights/internal'
          ? path.join(root, 'native-sdk/abstraction-rights/javascript/js/abstraction/rights/api/internal.mjs')
        : path.resolve(path.dirname(owner.identifier), specifier);
    assert.ok(target.startsWith(root + path.sep), `module escaped resource closure: ${specifier}`);
    return load(target);
  });
  await host.evaluate({timeout: 10000});
  assert.equal(bootstrapCalls, 1, mode);
  assert.equal(writes, mode === 'callback-throw' ? 0 : 1, mode);
  assert.equal(faults, mode === 'callback-throw' ? 1 : 0, mode);
  assert.equal(frame.contentDocument, originalDocument, 'EOF must preserve the content GUI');
  if (mode !== 'callback-throw') {
    const answer = answers[0];
    assert.equal(answer.outcome, mode === 'ready' ? 'applied' : 'unavailable', mode);
    assert.equal(answer.reason === '', mode === 'ready', mode);
  }
}
}
console.log(nativeBridge ? 'PASS actual native host browser callback' : faultOnly ? 'two callback faults forwarded without fabricated result' : 'twelve retained UI/generated-result receiver controls passed');
if (nativeBridge) process.stdin.destroy();
