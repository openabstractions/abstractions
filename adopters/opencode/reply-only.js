import {realpath} from 'node:fs/promises';
import {isAbsolute, resolve, sep} from 'node:path';

// S5 architect ruling 2026-11-22: a turn OA's wake pointer started in an
// OpenCode chat is reply-only until a turn the plugin did not start. It may read
// the message and reply in that thread, read, list and search files inside the
// project folder, and use OA's read-only tools. OA itself refuses OA tools that
// change anything and a reply outside the woken thread. Every other tool is
// refused through OpenCode's tool.execute.before hook with the ruling's reason.

// Marks by project folder and host chat ID. This module stays loaded across
// an instance reload of the same folder, so a woken turn keeps its mark.
export const replyOnlyMarks = new Map();

export function replyOnlyReason(sender) {
  return `This turn was started by a message from ${sender || 'another agent'}. Ask the person to allow more for this conversation.`;
}

// OA's tools a reply-only turn keeps; the helper enforces the rest of OA's rule.
export const replyOnlyOATools = Object.freeze(new Set(['oa_messages_wait', 'oa_messages_reply', 'oa_messages_watch', 'oa_messages_inbox_open',
  'oa_messages_inboxes_list', 'oa_models_list', 'oa_applications_list', 'oa_applications_describe', 'oa_applications_commands_list',
  'oa_applications_enrollment_read', 'oa_applications_command_call', 'oa_api_describe', 'oa_api_read', 'oa_inference_job_status',
  'oa_inference_job_diagnose', 'oa_jobs_follow', 'oa_asks_watch', 'oa_asks_list_answerable', 'oa_asks_list_choices']));

// A glob may not climb out of or start outside the searched folder.
function relativeGlob(value) {
  return value === undefined || typeof value === 'string' && value.length <= 4096 && !value.includes(':')
    && !/(?:^|[{,])\s*[\\/~]/.test(value) && !value.split(/[\\/{},]/).some(part => part.trim() === '..');
}

const fold = value => process.platform === 'win32' ? value.toLowerCase() : value;

// Whether path lands inside directory once links are resolved. A path that
// does not resolve, a network or device path and a drive-relative one do not.
export async function insideFolder(directory, path, resolveReal = realpath) {
  if (typeof directory !== 'string' || !directory || typeof path !== 'string' || !path || path.length > 4096
    || /^[A-Za-z]:(?![\\/])/.test(path) || /^[\\/][\\/]/.test(path) || path.startsWith('~')) return false;
  try {
    const root = await resolveReal(directory);
    const real = await resolveReal(isAbsolute(path) ? path : resolve(directory, path));
    const r = fold(root).replace(/[\\/]+$/, ''), at = fold(real);
    if (broadRoot(r)) return false;
    return at === r || at.startsWith(r + sep);
  } catch { return false; }
}

// A drive or file-system root, or a profile folder, is no project: reading
// "inside" it reaches the person's other files.
export function broadRoot(root) {
  const plain = String(root).replace(/[\\/]+$/, ''), win = plain.replaceAll('/', '\\');
  return plain === '' || /^[a-z]:\\?$/i.test(win) || /^[a-z]:\\users\\[^\\]+$/i.test(win) || /^\/(Users|home)\/[^/]+$/.test(plain);
}
const broadRootReason = ' File reads are off because this chat\'s folder is a whole drive or a home folder.';
async function broadFolder(directory, resolveReal = realpath) {
  try { return broadRoot(await resolveReal(directory)); } catch { return false; }
}
const fileTools = new Set(['read', 'list', 'glob', 'grep']);

// Whether a reply-only turn may make this call before the person's pair rule.
export async function replyOnlyAllows(tool, args, directory, resolveReal) {
  const a = args && typeof args === 'object' ? args : {};
  switch (tool) {
    case 'read': return insideFolder(directory, a.filePath, resolveReal);
    case 'list': return insideFolder(directory, a.path ?? directory, resolveReal);
    case 'glob': return relativeGlob(a.pattern) && insideFolder(directory, a.path ?? directory, resolveReal);
    case 'grep': return typeof a.pattern === 'string' && relativeGlob(a.include) && insideFolder(directory, a.path ?? directory, resolveReal);
  }
  return replyOnlyOATools.has(tool);
}

// The tool.execute.before hook: refuses by throwing, as OpenCode documents.
export function createReplyOnlyGuard({messages, directory, resolveReal}) {
  return async (input, output) => {
    const id = input?.sessionID, mark = messages.replyOnly?.(id);
    // A read may bring another sender's message, and the person may have
    // switched the pair off: full use is asked again after every read.
    if (mark && input?.tool === 'oa_messages_wait') mark.full = false;
    if (!mark || mark.full) return;
    if (await replyOnlyAllows(input?.tool, output?.args, directory, resolveReal)) return;
    if (await messages.peerFullTools?.(id) && messages.replyOnly(id) === mark) { mark.full = true; return; }
    const broad = fileTools.has(input?.tool) && await broadFolder(directory, resolveReal);
    throw new Error(replyOnlyReason(mark.sender) + (broad ? broadRootReason : ''));
  };
}
