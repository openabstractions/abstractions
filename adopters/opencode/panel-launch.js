import {spawn as nodeSpawn} from 'node:child_process';
import {stat as nodeStat} from 'node:fs/promises';
import {win32} from 'node:path';

const panelExecutable = 'Abstraction Panel.exe';
const pages = Object.freeze({credentials: 'credentials'});
const inheritedEnvironment = new Set([
  'APPDATA', 'COMSPEC', 'HOMEDRIVE', 'HOMEPATH', 'LOCALAPPDATA', 'PATH', 'PATHEXT',
  'PROGRAMDATA', 'SYSTEMROOT', 'TEMP', 'TMP', 'USERDOMAIN', 'USERNAME', 'USERPROFILE', 'WINDIR',
]);

function panelEnvironment(source) {
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
  const {BunNativeConnector} = await import('@openabstractions/ipc/bun');
  return new BunNativeConnector().selectRuntime({timeout: 5000});
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
