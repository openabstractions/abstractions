// Compact tool text for a model on a local engine, where every prompt token is
// prefilled on this machine (research/opencode-first-request-2026-10-02.md).
// OpenCode's `tool.definition` hook passes only the tool's ID, never the
// session's agent, so the plugin applies this when its `compactTools` option
// is set, which configure.js writes when the default
// model runs on this machine. Each text keeps its tool's usage rules from
// OpenCode 1.18.31 (packages/opencode/src/tool/*.txt and tool/shell/prompt.ts,
// MIT, Copyright (c) 2025 opencode). The JSON schemas keep every property,
// type, enum and required list; they drop `$schema` and the ±2^53 integer
// bounds. OpenCode still validates every call with its own schema.

import {createHash} from 'node:crypto';

const SAFE = 9007199254740991;

/** The OpenCode version the compact text was condensed from. */
export const condensedFrom = '1.18.31';

// SHA-256 of each condensed core tool's trimmed 1.18.31 text
// (test/fixtures/opencode-1.18.31-tool/<tool>.txt), which OpenCode passes to
// the tool.definition hook unchanged. A different text means another OpenCode
// version whose rules the compact text may not carry.
const PINNED = Object.freeze({
  read: '97355e60233eee447b1e412c687afabf00c16de01ee18abdc370c74b57fe4f98',
  edit: '2261417339b08e2275551aa843f8c10ebf8ccc0dfa19ceefc81fd706176cfbd4',
  write: 'f51167792ab0da7431db45f0b31f725b7060ae8fb1fdb1aaa986152af39122e3',
  glob: '111f64438da43fa70c12cf44628b95e73b2e9532f79af5448cf23782c0dc7eae',
  grep: 'ca1c54463646519870b33a0fb5b25ce0e537085d0226b407770ee328145365b6',
  webfetch: 'e002acfb8419580a05c4cb732add13d897345339f384cb01b19656b242ed410c',
  todowrite: '702d619d07842a00b0a4e2029af5a83b6a73c563369fd8a3550572b897688be7',
  question: '3133ed46b195823f1ef7cd15c2aea439136a6e3a7222986bf6f31ba908b7b7ca',
});

/** The core tools whose text is pinned. */
export const pinnedTools = Object.freeze(Object.keys(PINNED));

/** Whether toolID's text is the text the compact version was condensed from.
 * A tool with no pinned text (the shell, OA's own tools) answers true. */
export function condensedText(toolID, description) {
  const digest = PINNED[toolID];
  if (!digest) return true;
  return typeof description === 'string' && createHash('sha256').update(description.trim()).digest('hex') === digest;
}

const CORE = {
  read: [
    'Read a file or list a directory. filePath must be absolute.',
    'Returns up to 2000 lines as `N: text`; offset is the 1-based first line, limit the line count. Lines over 2000 characters are cut.',
    'Read a larger window rather than tiny slices; read several files in parallel. Reads images and PDFs as attachments.',
  ].join('\n'),
  edit: [
    'Replace exact text in a file. You must read the file first in this conversation, or the edit fails.',
    'oldString must match exactly: keep the exact indentation after the `N: ` line prefix and never include the prefix. It must be unique unless replaceAll is true; add context to make it unique.',
    'Prefer editing existing files; never write new files unless required. No emojis unless asked.',
  ].join('\n'),
  write: [
    'Write a file, replacing any existing content. filePath must be absolute.',
    'You must read an existing file first, or the write fails. Prefer editing existing files; never write new files unless required, and never create documentation files unless asked. No emojis unless asked.',
  ].join('\n'),
  glob: 'Find files by glob pattern (e.g. "src/**/*.ts"). Omit path for the working directory. Batch several searches in one response.',
  grep: 'Search file contents with a regular expression; filter files with include (e.g. "*.{ts,tsx}"). Returns paths, line numbers and lines. Omit path for the working directory. To count matches, run rg in the shell, never grep.',
  webfetch: 'Fetch a URL (it must be full and valid) and return its content as markdown (default), text or html; HTTP is upgraded to HTTPS. Read-only; large results may be summarised.',
  todowrite: [
    'Keep a task list for work with 3 or more steps or several requests; skip it for single or informational tasks.',
    'States: pending, in_progress (exactly one at a time), completed (only when done and verified, never on intent), cancelled. Update as you go; keep user commands verbatim.',
  ].join('\n'),
  question: 'Ask the user questions while working, to settle preferences, ambiguity or choices. A free-text answer is added automatically; put a recommended option first, marked "(Recommended)". Answers come back as label arrays; set multiple to allow several.',
};

const string = (description) => ({type: 'string', description});
const object = (properties, required) => ({type: 'object', properties, required});

// The 1.18.31 schemas of the core tools whose schemas OpenCode builds itself
// (no jsonSchema reaches the hook), compacted.
const SCHEMAS = {
  bash: object({command: string('The command to execute'), timeout: {type: 'integer', exclusiveMinimum: 0, description: 'Timeout in milliseconds'},
    workdir: string('Directory to run in; default is the working directory')}, ['command']),
  read: object({filePath: string('Absolute path of the file or directory'), offset: {type: 'integer', minimum: 0, description: 'First line, 1-based'},
    limit: {type: 'integer', minimum: 0, description: 'Maximum lines (default 2000)'}}, ['filePath']),
  edit: object({filePath: string('Absolute path'), oldString: string('Exact text to replace'), newString: string('Replacement, different from oldString'),
    replaceAll: {type: 'boolean', description: 'Replace every occurrence (default false)'}}, ['filePath', 'oldString', 'newString']),
  write: object({content: string('File content'), filePath: string('Absolute path')}, ['content', 'filePath']),
  glob: object({pattern: string('Glob pattern'), path: string('Directory to search; omit for the working directory')}, ['pattern']),
  grep: object({pattern: string('Regular expression'), path: string('Directory to search; default is the working directory'),
    include: string('File filter, e.g. "*.js"')}, ['pattern']),
  webfetch: object({url: string('URL to fetch'), format: {type: 'string', enum: ['text', 'markdown', 'html'], default: 'markdown', description: 'Result format'},
    timeout: {type: 'number', description: 'Timeout in seconds (max 120)'}}, ['url']),
  todowrite: object({todos: {type: 'array', description: 'The updated todo list', items: object({content: string('Brief task description'),
    status: string('pending, in_progress, completed or cancelled'), priority: string('high, medium or low')}, ['content', 'status', 'priority'])}}, ['todos']),
  question: object({questions: {type: 'array', description: 'Questions to ask', items: object({question: string('Complete question'),
    header: string('Very short label (max 30 chars)'), options: {type: 'array', description: 'Available choices', items: object({
      label: string('Display text (1-5 words)'), description: string('Explanation of the choice')}, ['label', 'description'])},
    multiple: {type: 'boolean', description: 'Allow several choices'}}, ['question', 'header', 'options'])}}, ['questions']),
};

/** The shell essentials for the shell OpenCode chose, read from its own
 * description ("Shell: powershell") with the temporary-work path it names. */
export function compactShell(original = '') {
  const shell = /Shell: ([\w.-]+)/.exec(original)?.[1] ?? 'bash';
  const tmp = /Use `([^`]+)` for temporary work/.exec(original)?.[1];
  const lines = [`Run a ${shell} command in the working directory and return its output; use workdir instead of cd.`];
  if (shell === 'powershell') {
    lines.push('Windows PowerShell 5.1: no && or ||; chain dependent commands with `cmd1; if ($?) { cmd2 }`. Use `$env:NAME` for variables, `& "path\\to\\exe" args` for a path with spaces, full cmdlet names, and the backtick to escape.');
  } else if (shell === 'pwsh') {
    lines.push('PowerShell 7: chain dependent commands with &&. Use `& "path/to/exe" args` for a path with spaces and full cmdlet names.');
  } else if (shell === 'cmd') {
    lines.push('cmd.exe: chain dependent commands with &&; quote paths with spaces.');
  } else {
    lines.push('Chain dependent commands with &&; quote paths with spaces.');
  }
  lines.push('Use the read, edit, write, glob and grep tools for files, not shell commands. Run independent commands as parallel calls; do not separate commands with newlines.');
  lines.push('Default timeout 120000 ms. Output over 2000 lines or 51200 bytes is cut and saved to a file you can read; do not trim it with head, tail or more.');
  if (tmp) lines.push(`Use ${tmp} for temporary work.`);
  // The git safety rules of 1.18.31's shell.txt, kept whole: compact text
  // applies to Build too.
  lines.push('Commit, amend, push or open PRs only when asked; inspect status and diff first and never commit secrets.');
  lines.push('Do not update git config, skip hooks, use interactive -i, force-push or create empty commits unless asked. If a commit fails or a hook rejects it, fix the cause and make a new commit; do not amend the failed one.');
  return lines.join('\n');
}

/** A JSON schema without `$schema` and without the ±2^53 integer bounds. */
export function compactSchema(schema) {
  if (Array.isArray(schema)) return schema.map(compactSchema);
  if (!schema || typeof schema !== 'object') return schema;
  const out = {};
  for (const [key, value] of Object.entries(schema)) {
    if (key === '$schema') continue;
    if ((key === 'minimum' || key === 'maximum') && Math.abs(value) === SAFE) continue;
    out[key] = compactSchema(value);
  }
  return out;
}

/** Applies the compact text and schema to one tool definition in place.
 * short maps an OA tool ID to its one-line description; its full text stays
 * available through openabstractions_tools. */
export function compactDefinition(toolID, output, short = {}) {
  if (toolID === 'bash') output.description = compactShell(output.description);
  else if (CORE[toolID]) output.description = CORE[toolID];
  else if (short[toolID]) output.description = short[toolID];
  if (SCHEMAS[toolID]) output.jsonSchema = SCHEMAS[toolID];
  else if (output.jsonSchema) output.jsonSchema = compactSchema(output.jsonSchema);
}
