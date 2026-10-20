import {runtimeConnector} from './runtime-connector.js';
import {spawn as nodeSpawn} from 'node:child_process';
import {stat as nodeStat} from 'node:fs/promises';
import {win32} from 'node:path';
import {fileURLToPath} from 'node:url';

const panelExecutable = 'Abstraction Panel.exe';
const pages = Object.freeze({credentials: 'credentials'});
const inheritedEnvironment = new Set([
  'APPDATA', 'COMSPEC', 'HOMEDRIVE', 'HOMEPATH', 'LOCALAPPDATA', 'PATH', 'PATHEXT',
  'PROGRAMDATA', 'SYSTEMROOT', 'TEMP', 'TMP', 'USERDOMAIN', 'USERNAME', 'USERPROFILE', 'WINDIR',
]);

export function panelEnvironment(source) {
  const result = {};
  for (const [name, value] of Object.entries(source ?? {})) {
    if (inheritedEnvironment.has(name.toUpperCase())) result[name] = value;
  }
  return result;
}

function manualSetup() {
  return `Open the Abstraction Panel from the Windows Start menu and choose Credentials, or register the credential from your own terminal with the existing \`openabstractions credentials add\` flow and \`--from-stdin\`. When setup is complete, provide OpenCode only the credential name. Never paste the secret into OpenCode.`;
}

function result(title, output) {
  return {title, output};
}

function launchError(provider, message) {
  return result('OpenAbstractions Panel needs manual launch', `${message}\n\n${manualSetup()}`);
}

async function selectInstalledRuntime() {
  return (await runtimeConnector()).selectRuntime({timeout: 5000});
}

/** Resolve the Panel beside the executable selected by the shared native runtime resolver. */
export async function resolveInstalledPanel({selectRuntime = selectInstalledRuntime, stat = nodeStat} = {}) {
  const selected = await selectRuntime();
  const program = selected?.program;
  if (typeof program !== 'string' || !win32.isAbsolute(program) ||
      win32.basename(program).toLowerCase() !== 'openabstractions.exe' ||
      win32.basename(win32.dirname(program)).toLowerCase() !== 'tools') {
    throw new Error('selected installation did not provide the expected runtime tools path');
  }
  const panel = win32.join(win32.dirname(program), panelExecutable);
  const info = await stat(panel);
  if (!info.isFile()) throw new Error('the selected installation does not contain the Panel');
  return panel;
}

/** The registration manifest OpenCode with OA's installer places in its
 * integration root, which holds this plugin at plugin/<version>/. Any other
 * layout (a registry or development install) has none: "". */
export function installedRegistrationManifest(moduleURL = import.meta.url) {
  let here;
  try { here = win32.dirname(fileURLToPath(moduleURL)); } catch { return ''; }
  const plugins = win32.dirname(here);
  // The installer writes this folder name; the Panel checks the manifest.
  if (win32.basename(plugins) !== 'plugin') return '';
  return win32.join(win32.dirname(plugins), 'openabstractions-application.json');
}

/** Ask the installed Panel to register this integration: the Panel records
 * the request, which waits in Apps until the person allows it, and posts its
 * one question unless this account already saw it for the same manifest.
 * The Panel validates the manifest; this call registers nothing and grants
 * nothing. It answers whether a Panel was started. */
export function requestApplicationRegistration(options = {}) {
  return requestInstalledPanel(options, manifest => ['--register-application', manifest]);
}

// Installed OpenCode first use opens the existing Panel connection owner.
// The host supplies no enrollment subject, permission or selected image.
export async function requestOpenCodeConnection({spawn = nodeSpawn, platform = process.platform, env = process.env,
  selectRuntime, stat = nodeStat, manifest = installedRegistrationManifest(), readOnly = false, signal} = {}) {
  if (platform !== 'win32' || typeof manifest !== 'string' || !win32.isAbsolute(manifest)) return false;
  let executable;
  try {
    if (!(await stat(manifest)).isFile()) return false;
    executable = await resolveInstalledPanel({selectRuntime, stat});
  } catch { return {outcome: 'not_connected'}; }
  return new Promise(resolve => {
    let child, bytes = '', invalid = false, failed = false;
    try {
      child = spawn(executable, [readOnly ? '--read-opencode-connection' : '--connect-opencode'], {
        stdio: ['ignore', 'pipe', 'ignore'], windowsHide: true, env: panelEnvironment(env), signal, timeout: 210000,
      });
    } catch { resolve({outcome: 'not_connected'}); return; }
    child.stdout?.setEncoding?.('utf8');
    child.stdout?.on('data', value => {
      if (invalid) return;
      bytes += value;
      if (Buffer.byteLength(bytes) > 1024) { invalid = true; bytes = ''; }
    });
    child.stdout?.on('error', () => { invalid = true; });
    child.once('error', () => { failed = true; });
    // close joins this child's stdout. A spawn, exit event, older Panel or
    // malformed result cannot provide either native identity proof.
    child.once('close', code => {
      let result;
      try { result = JSON.parse(bytes); } catch {}
      const exact = result && !Array.isArray(result) && [3,4].includes(Object.keys(result).length)
        && Object.hasOwn(result, 'outcome') && typeof result.hostRegistered === 'boolean'
        && typeof result.helperEnrolled === 'boolean' && JSON.stringify(result) === bytes.trim()
        && (Object.keys(result).length === 3 || typeof result.helperProgram === 'string'
          && win32.isAbsolute(result.helperProgram) && !/[\0\r\n]/.test(result.helperProgram));
      const typed = !invalid && !failed && code === 0 && exact;
      if (typed && result.outcome === 'connected' && result.hostRegistered && result.helperEnrolled) resolve(result);
      // Only the Panel's typed answer says a native question waits. A failed,
      // killed or older Panel shows no popup; the person connects in the Panel.
      else if (typed && result.outcome === 'connect_pending') resolve({outcome: 'connect_pending'});
      else resolve({outcome: 'not_connected'});
    });
  });
}

async function requestInstalledPanel({spawn = nodeSpawn, platform = process.platform, env = process.env,
  selectRuntime, stat = nodeStat, manifest = installedRegistrationManifest()} = {}, argumentsFor) {
  if (platform !== 'win32' || typeof manifest !== 'string' || !win32.isAbsolute(manifest)) return false;
  let executable;
  try {
    if (!(await stat(manifest)).isFile()) return false;
    executable = await resolveInstalledPanel({selectRuntime, stat});
  } catch {
    return false;
  }
  return new Promise((resolve) => {
    let child;
    try {
      child = spawn(executable, argumentsFor(manifest), {detached: true, stdio: 'ignore', windowsHide: true, env: panelEnvironment(env)});
    } catch {
      resolve(false);
      return;
    }
    child.once('error', () => resolve(false));
    child.once('spawn', () => { child.unref(); resolve(true); });
  });
}

function requestedProvider(args) {
  if (!args || typeof args !== 'object' || Array.isArray(args) ||
      Object.keys(args).some((key) => key !== 'provider')) {
    throw new Error('Choose a provider only; credential values are never accepted.');
  }
  const provider = args.provider ?? 'credentials';
  if (!Object.hasOwn(pages, provider)) throw new Error('Choose credentials.');
  return provider;
}

export async function launchCredentialsPanel({provider = 'credentials', spawn = nodeSpawn,
  platform = process.platform, env = process.env, selectRuntime, stat} = {}) {
  if (typeof provider !== 'string' || !Object.hasOwn(pages, provider)) throw new TypeError('unsupported credential provider');
  if (platform !== 'win32') {
    return launchError(provider, 'This helper launches the installed Windows Panel only.');
  }

  let executable;
  try {
    executable = await resolveInstalledPanel({selectRuntime, stat});
  } catch {
    return launchError(provider, 'The installed OpenAbstractions Panel could not be resolved from the selected runtime installation.');
  }

  return new Promise((resolve) => {
    let child;
    try {
      child = spawn(executable, ['-page', pages[provider], '-quiet'], {
        detached: true,
        stdio: 'ignore',
        windowsHide: true,
        env: panelEnvironment(env),
      });
    } catch {
      resolve(launchError(provider, 'The installed Panel could not be started.'));
      return;
    }

    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      resolve(value);
    };
    child.once('error', () => {
      finish(launchError(provider, 'The installed Panel could not be started from its selected runtime installation.'));
    });
    child.once('spawn', () => {
      child.unref();
      finish(result(
        'OpenAbstractions Panel started',
        `The Panel process started with its Credentials page selected. Enter the secret in the Panel, then provide OpenCode only the credential name. This launch did not store a credential or grant app access.`,
      ));
    });
  });
}

/** Build an OpenCode tool. The provider selector is a closed enum; there is no secret or path input. */
export function createOpenAbstractionsPanelTool({tool, ...options} = {}) {
  if (typeof tool !== 'function' || typeof tool.schema?.enum !== 'function') {
    throw new TypeError('The OpenCode tool helper is required.');
  }
  const providerSchema = tool.schema.enum(['credentials']).optional();
  return tool({
    description: 'After the user requests credential setup, open the Credentials page in the Panel selected beside the trusted installed runtime. The only input is a provider choice. Never ask for, accept or pass a credential value, URL or executable path in OpenCode. The person enters the secret in the Panel and separately grants app access.',
    args: {provider: providerSchema},
    async execute(args, context) {
      const provider = requestedProvider(args);
      if (typeof context?.ask !== 'function') {
        return launchError(provider, 'OpenCode did not provide its user approval prompt, so the Panel was not started.');
      }
      try {
        await context.ask({
          permission: 'oa_panel_launch',
          patterns: [provider],
          always: [],
          metadata: {
            title: 'OpenAbstractions Credentials',
            description: 'Start the Panel at its Credentials page. The credential value stays in the Panel.',
          },
        });
      } catch {
        return result('Panel not started', 'The OpenAbstractions Panel was not started because the approval step did not complete.');
      }
      return launchCredentialsPanel({...options, provider});
    },
  });
}
