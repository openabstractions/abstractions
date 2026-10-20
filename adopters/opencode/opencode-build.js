/**
 * The plugin's start notices and the once helper for feature checks.
 */

/** The one toast the plugin shows, once per start, when OpenCode runs
 * without OA's native session (`openabstractions opencode open`). */
export const outsideSandboxMessage = 'OpenCode is running outside the OA sandbox. Folder permission requests are available only in a protected OA session.';

/** The one notice the plugin shows, once per start, inside OA's sandbox:
 * OpenCode's grep and glob have no search tool there (TODO.md). */
export const searchToolMissingMessage = 'Search tools aren\'t available inside the OA sandbox yet.';

/** Remembers one check's first answer; a check that throws answers false. */
export function once(check) {
  let answer;
  return () => {
    answer ??= Promise.resolve().then(check).then(Boolean, () => false);
    return answer;
  };
}
