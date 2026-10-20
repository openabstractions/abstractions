/**
 * Feature checks for what the draft patches in `patches/` add over the official
 * OpenCode. OA's own patched build is retired; a build carrying the patches is
 * one someone made from them. The plugin runs on both; each check answers
 * false on the official build or when OpenCode cannot answer, and the caller
 * then leaves the feature out without a message.
 */

/** The one toast the plugin shows, once per start, when OpenCode runs
 * without OA's native session (`openabstractions opencode open`). */
export const outsideSandboxMessage = 'OpenCode is running outside the OA sandbox. To start it protected, run: node <plugin folder>/official-install.js --project <folder> --run';

/** The one notice the plugin shows, once per start, inside OA's sandbox:
 * OpenCode's grep and glob have no search tool there (TODO.md). */
export const searchToolMissingMessage = 'Search tools aren\'t available inside the OA sandbox yet.';

// OpenCode's OpenAPI document names the provider refresh patch's route
// `/provider/{providerID}/models/refresh`.
const refreshRoute = /^\/provider\/(?:\{[^/}]+\}|:[^/]+)\/models\/refresh$/;

/** Whether OpenCode serves the provider refresh route. It reads OpenCode's own
 * OpenAPI document (`GET /doc`) through the SDK's HTTP client, which sends
 * nothing to a provider. */
export async function servesProviderRefresh(client) {
  try {
    const http = client?.client ?? client;
    if (typeof http?.get !== 'function') return false;
    const response = await http.get({url: '/doc'});
    if (response?.error || (response?.response && !response.response.ok)) return false;
    const paths = response?.data?.paths;
    return Boolean(paths && typeof paths === 'object' && Object.keys(paths).some((path) => refreshRoute.test(path)));
  } catch {
    return false;
  }
}

/** Remembers one check's first answer; a check that throws answers false. */
export function once(check) {
  let answer;
  return () => {
    answer ??= Promise.resolve().then(check).then(Boolean, () => false);
    return answer;
  };
}
