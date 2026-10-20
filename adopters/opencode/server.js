// The package's `./server` entry for both OpenCode plugin APIs: OpenCode 1.18
// (1.18.29 and later read an object default) calls `server`; OpenCode V2
// (@opencode/plugin 2.x) reads `id` and calls `setup`. This module exports
// only its default, which both loaders require.
import OpenAbstractionsPlugin from './plugin.js';
import {pluginID, setupV2} from './v2.js';

export default {id: pluginID, setup: (ctx) => setupV2(ctx), server: OpenAbstractionsPlugin};
