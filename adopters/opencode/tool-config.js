// The plugin's tool choices, kept out of plugin.js: OpenCode 1.18.31 loads
// every export of a server plugin module as a plugin and refuses a module
// with any export that is not one (packages/opencode/src/plugin/index.ts
// getLegacyPlugins), so plugin.js exports only its default.
import {compactSchema} from './compact-tools.js';
import {messageToolNames, messageTools} from './messages.js';
import {modelsConnectMetadata} from './models-connect.js';

/** The plugin's tools, by the name the `tools` option uses; each registers
 * with canonical assistant names. Every definition is sent with every model
 * request, so a tool registers only when it is wanted. */
export const toolNames = Object.freeze(['generate_image', 'speak', 'selection_policy', 'watch_logs',
  'open_credentials_panel', 'failure_logs', ...messageToolNames, 'oa_models_connect']);

// These private host operations retain their existing schemas and authority.
// Configuration keys stay stable; the host declares one canonical tool name.
export const toolNameFor = name => ({generate_image: 'oa_image_generate', speak: 'oa_speech_speak',
  selection_policy: 'oa_selection_manage', watch_logs: 'oa_logs_watch',
  open_credentials_panel: 'oa_credentials_open', failure_logs: 'oa_logs_read_failure',
  project_access: 'oa_project_request_access'})[name] ?? name;

/** Tools a capability brings: each registers by default when the runtime
 * serves its profile (router ROUTE-D9) at OpenCode's startup. The helper's
 * oa_video_generate registers while video is served, so a chat without
 * video carries no video definition (architect, 2026-11-12).
 * The others open the Panel or edit preferences, and register only when the
 * `tools` option enables them. */
export const capabilityTools = Object.freeze({generate_image: 'image', speak: 'speech', oa_video_generate: 'video'});

/** Tools that register with a capability tool unless the `tools` option turns
 * them off: failure_logs reads what OA recorded about a failed OA operation
 * the companion tool reported (A-F19). */
export const companionTools = Object.freeze({generate_image: Object.freeze(['failure_logs'])});

/** One line per OA tool for compact mode; oa_tools_list returns the
 * full description and parameters on request. Each keeps its safety rule. */
export const shortToolText = Object.freeze({
  oa_models_connect: modelsConnectMetadata?.description??'',
  ...Object.fromEntries(messageTools.map(tool => [tool.name, tool.description])),
  oa_image_generate: 'Make one picture; only prompt is needed. It is saved in the person\'s Pictures/OpenAbstractions folder and the result names the file.',
  oa_speech_speak: 'Read a chosen text aloud through OpenAbstractions speech when asked.',
  oa_logs_watch: 'After explicit approval, watch one OpenAbstractions log source; details from oa_tools_list.',
  oa_logs_read_failure: 'Read what OA recorded about a failure an OA tool reported by failureId; OA asks first; sharing contents needs a second approval; never repairs.',
  oa_selection_manage: 'Read or, when the user asks, change an OpenAbstractions model preference; details from oa_tools_list.',
  oa_credentials_open: 'When the user asks to set up a credential, open its Panel page; never ask for or pass a secret.',
});

const discoveryDescription = 'List and describe the OpenAbstractions capabilities available on this machine: call with no name for registered tool names, '
  + 'or supply an exact registered name such as oa_image_generate for its full description and parameters.';

/** The discovery tool: the registered OA tools in one line each, and one
 * tool's full description and JSON parameters on request. */
export function createToolsTool({tool, registered, full}) {
  return tool({
    description: discoveryDescription,
    args: {name: tool.schema.string().describe('Exact registered tool name, for example oa_image_generate; omit to list names.').optional()},
    async execute(args) {
      const names = Object.keys(registered()).filter((name) => name !== 'oa_tools_list').sort();
      if (!args?.name) {
        const unregistered = toolNames
          .map(toolNameFor).filter(name => !names.includes(name));
        return JSON.stringify({kind: 'openabstractions_tools', tools: names.map((name) => ({name, summary: shortToolText[name] ?? ''})),
          notRegistered: unregistered,
          enable: 'A tool not registered here is enabled in the OpenAbstractions plugin entry\'s "tools" option, or arrives with its capability at the next OpenCode start.'});
      }
      const definition = full(args.name);
      if (!names.includes(args.name) || !definition) {
        return JSON.stringify({kind: 'openabstractions_tool', name: args.name, outcome: 'not_registered'});
      }
      return JSON.stringify({kind: 'openabstractions_tool', name: args.name, description: definition.description,
        parameters: definition.parameters});
    },
  });
}

/** A plugin tool's parameters as JSON schema, when OpenCode's zod can say. */
export function parametersOf(tool, definition) {
  try {
    const z = tool.schema;
    if (typeof z?.toJSONSchema === 'function' && typeof z.object === 'function') {
      return compactSchema(z.toJSONSchema(z.object(definition.args ?? {})));
    }
  } catch {
    // Fall back to the argument names.
  }
  return {type: 'object', properties: Object.fromEntries(Object.keys(definition.args ?? {}).map((name) => [name, {}]))};
}

/** Helper tools that register only when the `tools` option enables them.
 * A chat rarely needs them, and every registered definition is prefilled with
 * every model request (owner bug R27: a 32k local context overflowed). The
 * plugin itself keeps using oa_messages_watch on its helper connection. */
export const optionalTools = Object.freeze(['oa_api_describe', 'oa_api_read',
  'oa_applications_enrollment_read', 'oa_applications_list', 'oa_asks_watch', 'oa_messages_watch',
  'oa_inference_job_submit', 'oa_inference_job_status', 'oa_inference_job_diagnose', 'oa_inference_job_cancel',
  'oa_jobs_follow'].filter(name => toolNames.includes(name)));

/** The `tools` plugin option: an object of tool name to true (register) or
 * false (never register). An absent name keeps the default; the default of
 * an optional tool is off. */
export function toolChoices(option) {
  const enabled = new Set();
  const disabled = new Set();
  const unknown = [];
  if (option !== undefined && (option === null || typeof option !== 'object' || Array.isArray(option))) {
    return {enabled, disabled: new Set(optionalTools), unknown: ['tools']};
  }
  for (const [name, value] of Object.entries(option ?? {})) {
    if (!toolNames.includes(name) || typeof value !== 'boolean') {
      unknown.push(name);
      continue;
    }
    (value ? enabled : disabled).add(name);
  }
  for (const name of optionalTools) if (!enabled.has(name)) disabled.add(name);
  return {enabled, disabled, unknown};
}

/** The OA tool names registered by default once OA answers: the generated
 * helper tools less the optional and capability ones, and Models Connect.
 * Capability tools (image, speech, video) add to these when their profile is
 * served. */
export const defaultHelperTools = Object.freeze([...messageToolNames, 'oa_models_connect']
  .filter(name => !optionalTools.includes(name) && !Object.hasOwn(capabilityTools, name)));

