// Page compositions carry complete native records as strings. The generated
// decoder owns validation; the generated encoder supplies wire field spelling
// for the existing display functions. Original bytes remain available for details.
const modules = {
 resource: () => import('./sdk/abstraction-resource/javascript/js/abstraction/resource/internal.mjs'),
 logging: () => import('./sdk/abstraction-logging/javascript/js/abstraction/logging/internal.mjs'),
 facade: () => import('./sdk/abstraction-facade/javascript/js/abstraction/facade/internal.mjs'),
 inference: () => import('./sdk/abstraction-inference/javascript/js/abstraction/inference/api/internal.mjs'),
 job: () => import('./sdk/abstraction-job/javascript/js/abstraction/job/acceptance/internal.mjs'),
};
const loaded = new Map(), original = new WeakMap(), encoder = new TextEncoder(), decoder = new TextDecoder();
async function codec(family) {
 if (!modules[family]) throw Error('Unknown Panel record family');
 if (!loaded.has(family)) loaded.set(family, modules[family]());
 return loaded.get(family);
}
export async function decode(family, name, raw) {
 if (typeof raw !== 'string') throw Error('Native Panel record required');
 const owner = await codec(family), value = owner['decode'+name](encoder.encode(raw));
 const view = JSON.parse(decoder.decode(owner['encode'+name](value)));
 original.set(view, raw);
 return view;
}
export async function encode(family, name, value) {
 const owner = await codec(family), parsed = owner['decode'+name](encoder.encode(JSON.stringify(value)));
 return JSON.parse(decoder.decode(owner['encode'+name](parsed)));
}
export function details(value) { return original.get(value) || JSON.stringify(value, null, 2); }
