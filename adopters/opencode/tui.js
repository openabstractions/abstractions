/**
 * Each TUI feature module, imported when the TUI starts: dictation
 * (listen.js, Ctrl+Alt+M and /listen), Speak replies with the playback of
 * `openabstractions_speak` (speak.js, /speak), where each reply ran
 * (where.js: this computer or a hosted server), and /oa, OA's models in one
 * list (oa-models.js). A feature imports inside its own attempt, so a module
 * that fails to load leaves the others running.
 */
export const tuiFeatures = {
  listen: async () => (await import('./listen.js')).registerListen,
  speak: async () => (await import('./speak.js')).installSpeak,
  where: async () => (await import('./where.js')).installWhere,
  models: async () => (await import('./oa-models.js')).installModels,
};

/**
 * Installs each feature in turn. One feature that cannot load or start
 * leaves the others running and says why in a toast.
 */
export async function installTui(api, options, features = tuiFeatures) {
  for (const [name, load] of Object.entries(features)) {
    try {
      const install = await load();
      await install(api, options ?? {});
    } catch (error) {
      api.ui?.toast?.({variant: 'error', message: `OpenAbstractions ${name} is unavailable: ${error?.message ?? error}`});
    }
  }
}

/** OpenCode's TUI entry for this package (`exports["./tui"]`), listed in
 * tui.json's `plugin`. OpenCode 1.18 calls `tui`. OpenCode V2 calls `setup`
 * with its own TUI API (@opencode/plugin/tui), which these features do not
 * use yet: there the entry says once that they are V1-only and the server
 * plugin's features (v2.js) run without them. */
export default {
  id: 'openabstractions',
  async tui(api, options) {
    await installTui(api, options);
  },
  async setup(context) {
    try {
      context?.ui?.toast?.show?.({variant: 'info', title: 'OpenAbstractions',
        message: 'OA models and tools work here; dictation, /oa and the reply place need OpenCode 1.18 for now.'});
    } catch {
      // A toast must not stop the TUI.
    }
  },
};
