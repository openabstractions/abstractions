/**
 * Development diagnostic for the separate protected CLI-session entrypoint.
 * The Windows application installer configures ordinary OpenCode Desktop
 * without requiring a user-installed Node or this manual command.
 * Finds the official OpenCode CLI, this plugin as OpenCode installed it and
 * OpenCode's global config, and names them to OA's sandboxed start:
 *
 *   openabstractions opencode open --project <folder> --program <opencode.exe>
 *     --plugin <plugin folder> --config <opencode.json>
 *
 * OA's runtime keeps no list of where OpenCode's installers put their files
 * (VISION 2026-10-06: adopter code stays in adopters/); this module is that
 * list. It reads only; it downloads, installs and changes nothing.
 *
 * Run it with Node, never through OpenCode's own program (that would make
 * OpenCode's identity a script host):
 *
 *   node <plugin folder>/official-install.js --project <folder>
 *
 * It prints the command; with --run it starts it.
 */
import {spawnSync} from 'node:child_process';
import {statSync, lstatSync, readdirSync} from 'node:fs';
import {dirname, basename, isAbsolute, win32} from 'node:path';
import {fileURLToPath} from 'node:url';

const pluginName = ['@openabstractions', 'opencode'];

const regular = (stat, path) => {
  try { return stat(path).isFile(); } catch { return false; }
};

function home(env) {
  return env.USERPROFILE || env.HOME || '';
}

/**
 * Where the official installers put the Windows CLI, in lookup order
 * (opencode.ai/docs, Install):
 * - the install script: $OPENCODE_INSTALL_DIR, else ~/.opencode/bin;
 * - npm i -g opencode-ai: the opencode-windows-x64 package's bin, nested or hoisted;
 * - bun add -g opencode-ai: the same package under ~/.bun/install/global;
 * - scoop install opencode: $SCOOP (default ~/scoop) apps/opencode/current;
 * - choco install opencode: $ChocolateyInstall lib/opencode/tools.
 * The release zip (opencode-windows-x64.zip) holds opencode.exe at its root
 * and has no fixed place: name it with --program. OpenCode Desktop runs its
 * server inside Electron and carries no CLI.
 */
export function officialInstalls(env = process.env, path = win32) {
  const user = home(env);
  const installs = [];
  const add = (method, first, ...rest) => { if (first) installs.push({method, program: path.join(first, ...rest)}); };
  if (env.OPENCODE_INSTALL_DIR) add('install script', env.OPENCODE_INSTALL_DIR, 'opencode.exe');
  add('install script', user, '.opencode', 'bin', 'opencode.exe');
  // OpenCode V2: npm install -g @opencode/cli (its bin is bin/opencode.exe).
  add('npm', env.APPDATA, 'npm', 'node_modules', '@opencode', 'cli', 'bin', 'opencode.exe');
  for (const platform of ['opencode-windows-x64', 'opencode-windows-x64-baseline']) {
    add('npm', env.APPDATA, 'npm', 'node_modules', 'opencode-ai', 'node_modules', platform, 'bin', 'opencode.exe');
    add('npm', env.APPDATA, 'npm', 'node_modules', platform, 'bin', 'opencode.exe');
    add('bun', user, '.bun', 'install', 'global', 'node_modules', platform, 'bin', 'opencode.exe');
  }
  add('scoop', env.SCOOP || (user && path.join(user, 'scoop')), 'apps', 'opencode', 'current', 'opencode.exe');
  add('chocolatey', env.ChocolateyInstall, 'lib', 'opencode', 'tools', 'opencode.exe');
  return installs;
}

/** The named program, else the first official install present. */
export function findProgram({given, env = process.env, stat = statSync, path = win32} = {}) {
  if (given) {
    if (!path.isAbsolute(given) || !regular(stat, given)) {
      throw new Error(`--program must name an existing absolute opencode.exe: ${given}`);
    }
    return {method: 'given', program: given};
  }
  const installs = officialInstalls(env, path);
  const found = installs.find((install) => regular(stat, install.program));
  if (!found) {
    throw new Error('No official OpenCode CLI found. Install it with OpenCode\'s own installer, or name opencode.exe with --program. Looked in: '
      + installs.map((install) => install.program).join('; '));
  }
  return found;
}

/**
 * This plugin where `opencode plugin @openabstractions/opencode` puts it:
 * $XDG_CACHE_HOME (default ~/.cache) opencode/packages/<spec>/node_modules.
 * Several installed versions need --plugin.
 */
export function findPlugin({given, env = process.env, stat = statSync, list = readdirSync, path = win32} = {}) {
  if (given) {
    if (!path.isAbsolute(given) || !regular(stat, path.join(given, 'package.json'))) {
      throw new Error(`--plugin must name the installed plugin folder: ${given}`);
    }
    return given;
  }
  const base = env.XDG_CACHE_HOME || (home(env) && path.join(home(env), '.cache'));
  const scope = path.join(base, 'opencode', 'packages', pluginName[0]);
  let entries = [];
  try { entries = list(scope); } catch { entries = []; }
  const found = entries.filter((entry) => entry === 'opencode' || entry.startsWith('opencode@')).sort()
    .map((entry) => path.join(scope, entry, 'node_modules', ...pluginName))
    .filter((dir) => regular(stat, path.join(dir, 'package.json')));
  if (found.length === 1) return found[0];
  if (!found.length) throw new Error(`The OA plugin is not installed for OpenCode. Run: opencode plugin @openabstractions/opencode (looked in ${scope})`);
  throw new Error(`${found.length} installed versions of the OA plugin; name one with --plugin`);
}

/** OpenCode's global config: $OPENCODE_CONFIG, else $XDG_CONFIG_HOME (default
 * ~/.config) opencode/opencode.json or opencode.jsonc. */
export function findConfig({given, env = process.env, stat = statSync, path = win32} = {}) {
  const named = given || env.OPENCODE_CONFIG;
  if (named) {
    if (!path.isAbsolute(named) || !regular(stat, named)) throw new Error(`--config must name an existing absolute file: ${named}`);
    return named;
  }
  const base = env.XDG_CONFIG_HOME || (home(env) && path.join(home(env), '.config'));
  for (const name of ['opencode.json', 'opencode.jsonc']) {
    const file = path.join(base, 'opencode', name);
    if (regular(stat, file)) return file;
  }
  throw new Error('OpenCode\'s global config is missing; run the plugin\'s configure step first');
}

/** The arguments of `openabstractions opencode open` for this computer. */
export function sandboxArguments({project, program, plugin, config, env = process.env, stat = statSync, list = readdirSync, path = win32} = {}) {
  if (!project || !path.isAbsolute(project)) throw new Error('--project must name an absolute project folder');
  const found = findProgram({given: program, env, stat, path});
  return {method: found.method, args: ['opencode', 'open', '--project', project, '--program', found.program,
    '--plugin', findPlugin({given: plugin, env, stat, list, path}), '--config', findConfig({given: config, env, stat, path})]};
}

function parse(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 1) {
    const flag = argv[index];
    if (flag === '--run' || flag === '--help') { options[flag.slice(2)] = true; continue; }
    const name = {'--project': 'project', '--program': 'program', '--plugin': 'plugin', '--config': 'config'}[flag];
    if (!name || index + 1 >= argv.length) throw new Error(`unknown or incomplete option: ${flag}`);
    options[name] = argv[++index];
  }
  return options;
}

// These are observations for the person running the diagnostic launcher. No
// ancestor configuration is granted, followed, disabled or copied into a guest.
function startupWarnings(project, path = win32) {
  const warnings = [];
  const inspect = (candidate) => {
    try { return lstatSync(candidate); }
    catch (error) {
      if (error.code !== 'ENOENT' && error.code !== 'ENOTDIR') {
        warnings.push(`Cannot inspect ${candidate}: ${error.message}`);
      }
      return undefined;
    }
  };
  const own = path.join(project, '.opencode');
  if (inspect(own)) {
    warnings.push(`Project configuration ${own} remains enabled. The OA sandbox has no network; OpenCode starts there without that folder's missing dependencies. Prepare project dependencies with OpenCode outside the sandbox before starting a protected session.`);
  }
  for (let current = path.dirname(project); current !== project; current = path.dirname(current)) {
    const candidate = path.join(current, '.opencode');
    if (inspect(candidate)) {
      warnings.push(`Ancestor configuration ${candidate} is outside the project folder grant. An OpenCode build that fails on unreadable ancestor configuration may stop at start. OA does not grant this folder or disable project configuration.`);
    }
    if (path.dirname(current) === current) break;
  }
  return warnings;
}

const usage = `Usage: node official-install.js --project <folder> [--program EXE] [--plugin DIR] [--config FILE] [--run]

Finds the official OpenCode CLI, the installed OA plugin and OpenCode's global
config, and prints the OA sandboxed start for them; --run starts it.
`;

if (process.argv[1] && isAbsolute(process.argv[1]) && basename(process.argv[1]) === 'official-install.js'
    && dirname(process.argv[1]) === dirname(fileURLToPath(import.meta.url))) {
  try {
    const options = parse(process.argv.slice(2));
    if (options.help || !options.project) {
      process.stdout.write(usage);
    } else {
      const {args} = sandboxArguments(options);
      for (const warning of startupWarnings(options.project)) process.stderr.write(`${warning}\n`);
      process.stdout.write(`openabstractions ${args.map((arg) => /[\s"']/.test(arg) ? JSON.stringify(arg) : arg).join(' ')}\n`);
      if (options.run) process.exitCode = spawnSync('openabstractions', args, {stdio: 'inherit'}).status ?? 1;
    }
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 2;
  }
}
