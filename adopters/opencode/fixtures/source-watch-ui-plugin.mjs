import OpenAbstractionsPlugin from '../plugin.js';

const sourceIDs = ['fixture/system', 'fixture/gap', 'fixture/unavailable'];

function syntheticReader() {
  return {
    async sources() {
      return {outcome: 'catalog', sources: sourceIDs.map((id) => ({id, availability: 'available', limitations: ['retention']}))};
    },
    async startAtEnd(sourceId) {
      if (!sourceIDs.includes(sourceId)) return {outcome: 'unsupported', records: [], next: '', atEnd: false};
      return {outcome: 'page', records: [], next: `synthetic:baseline:${sourceId}`, atEnd: true};
    },
    async read(sourceId, cursor) {
      if (!sourceIDs.includes(sourceId)) return {outcome: 'unsupported', records: [], next: cursor, atEnd: false};
      if (sourceId === 'fixture/gap') return {outcome: 'gap', records: [], next: cursor, atEnd: false};
      if (sourceId === 'fixture/unavailable') return {outcome: 'unavailable', records: [], next: cursor, atEnd: false};
      return {outcome: 'page', records: [{sourceId, record: {
        schema: 1, time: '2026-09-29T10:11:12.000000Z', level: 4,
        msg: 'Synthetic source-watch event', attrs: {fixture: 'owned-test-data'}, identity: [],
      }, producer: {provider: 'test-fixture', program: 'synthetic-writer', pid: 42}}],
      next: `synthetic:after:${sourceId}`, atEnd: true};
    },
  };
}

export default async function sourceWatchUIPlugin(context, options = {}) {
  const modulePath = process.env.OA_OPENCODE_PLUGIN_TOOL_MODULE;
  if (typeof modulePath !== 'string' || modulePath.length === 0) {
    throw new Error('OA_OPENCODE_PLUGIN_TOOL_MODULE must name the pinned OpenCode tool helper.');
  }
  const {tool} = await import(modulePath);
  return OpenAbstractionsPlugin(context, options, {
    tool,
    readInventory: async () => ({offers: []}),
    resolveSourceReader: async () => syntheticReader(),
  });
}
