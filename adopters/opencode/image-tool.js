import {randomBytes} from 'node:crypto';
import {mkdir, readFile, realpath, rename, rm, stat, writeFile} from 'node:fs/promises';
import {dirname, extname, isAbsolute, relative, resolve, sep, win32} from 'node:path';
import {within} from '@openabstractions/ipc/path';
import {newImageRequest, newImageRequestV2} from '@openabstractions/inference/protocol';
import {mediaMatchers, readDelivery} from './content-delivery.js';
import {failureNote} from './failure-logs.js';
import {invokeWithPermission} from './permission-wait.js';

const digestPattern = /^sha256:[0-9a-f]{64}$/;
const mediaTypes = new Set(['image/png', 'image/jpeg', 'image/webp']);
const maxImageBytes = 8 * 1024 * 1024;
const policyName = /^[a-z0-9][a-z0-9_-]{0,63}$/;
// The file extensions save_to accepts for each media type the tool returns.
const extensions = {'.png': 'image/png', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.webp': 'image/webp'};
// What the person reads when no provider serves image@1: the router refuses
// no_host with reason router:no-host. OA ships no picture engine of its own;
// registered providers such as Lemonade and TheNoise serve image@1
// (architect ruling, 2026-11-09).
export const noPictureEngine = 'Pictures are not set up yet. Open Catalog to add them.';
// What the person reads while this chat's OA connection, which makes the
// pictures, is not ready.
export const pictureConnectionPending = 'Pictures need this chat\'s OpenAbstractions connection, which is not ready yet. Try again in a moment.';

function sniffMediaType(bytes) {
  for (const mediaType of mediaTypes) if (mediaMatchers[mediaType](bytes)) return mediaType;
  return '';
}

// The account's picture policy Setup writes when it installs the starter
// picture model (monitor setup_step.go imagePolicy); a call that names no
// model or policy uses it through image@2.
export const defaultPicturePolicy = 'oa-image-default';
const argumentNames = ['prompt', 'size', 'model', 'selectionPolicy', 'save_to', 'overwrite', 'inputImage', 'mask', 'handle', 'wait_ms'];
const sizePattern = /^(auto|[1-9][0-9]{0,4}x[1-9][0-9]{0,4})$/;
const handlePattern = /^[A-Za-z0-9_-]{32}$/;
// The folder of the person's Pictures folder that pictures saved with no
// save_to go into (backlog/PICTURES-STORAGE.md ruling 2). The gateway's
// oa_image_generate resolves Pictures and writes the file.
export const defaultPictureFolder = 'OpenAbstractions';

// One short sentence per wrong argument, naming the values it takes. The
// returned copy carries the defaults: size auto, and the account's picture
// policy when neither model nor selectionPolicy is given.
function validRequest(args) {
  const refuse = (text) => { throw new TypeError(text); };
  if (!args || typeof args !== 'object') refuse('prompt is required: describe the picture.');
  const unknown = Object.keys(args).find((key) => !argumentNames.includes(key));
  if (unknown) refuse(`Unknown argument ${unknown}; allowed: ${argumentNames.join(', ')}.`);
  if (args.wait_ms !== undefined && (!Number.isInteger(args.wait_ms) || args.wait_ms < 0)) refuse('wait_ms must be a whole number of milliseconds; leave it out for 20000.');
  if (args.wait_ms !== undefined && args.save_to !== undefined) refuse('wait_ms goes only without save_to.');
  if (args.handle !== undefined) {
    // An argument left null, empty or at its default (size auto) is one a
    // client filled in; any other describes a new picture (R35).
    const conflict = Object.keys(args).sort().find((key) => key !== 'handle' && key !== 'wait_ms'
      && args[key] !== null && args[key] !== undefined && args[key] !== '' && !(key === 'size' && args[key] === 'auto'));
    if (conflict) refuse(`${conflict} describes a new picture, and handle continues one already started: to wait for it, call oa_image_generate with the handle and optionally wait_ms; to start another, leave out handle.`);
    if (typeof args.handle !== 'string' || !handlePattern.test(args.handle)) refuse('handle must be the handle an earlier oa_image_generate call answered with.');
    return args.wait_ms === undefined ? {handle: args.handle} : {handle: args.handle, wait_ms: args.wait_ms};
  }
  if (typeof args.prompt !== 'string' || !args.prompt.trim()) refuse('prompt is required: describe the picture.');
  if (Buffer.byteLength(args.prompt) > 32768) refuse('prompt is too long: at most 32768 bytes.');
  if (args.size !== undefined && (typeof args.size !== 'string' || !sizePattern.test(args.size))) {
    refuse('size must be auto or WIDTHxHEIGHT such as 512x512; leave it out for auto.');
  }
  if (args.model !== undefined && (typeof args.model !== 'string' || !args.model.trim())) refuse('model must be a model name; leave it out for the picture default.');
  if (args.selectionPolicy !== undefined && (typeof args.selectionPolicy !== 'string' || !policyName.test(args.selectionPolicy))) {
    refuse('selectionPolicy must be a saved policy name; leave it out for the picture default.');
  }
  if (args.model !== undefined && args.selectionPolicy !== undefined) refuse('Give model or selectionPolicy, not both; leave both out for the picture default.');
  if (args.inputImage !== undefined && !digestPattern.test(args.inputImage)) refuse('inputImage must be an OA image digest sha256:<64 hex>.');
  if (args.mask !== undefined && (args.inputImage === undefined || !digestPattern.test(args.mask))) refuse('mask must be a sha256 digest and goes only with inputImage.');
  if (args.save_to !== undefined && typeof args.save_to !== 'string') refuse('save_to must be a project path ending .png, .jpg, .jpeg or .webp.');
  if (args.overwrite !== undefined && (typeof args.overwrite !== 'boolean' || args.save_to === undefined)) refuse('overwrite is true or false and goes only with save_to.');
  const request = {...args, size: args.size ?? 'auto'};
  if (request.model === undefined && request.selectionPolicy === undefined) request.selectionPolicy = defaultPicturePolicy;
  return request;
}

// delegates says whether the call goes to the gateway's oa_image_generate:
// a continuation, or a picture for the person's Pictures folder, an edit and a
// saved policy included. A call naming save_to stays in this plugin, which
// saves into the project.
function delegates(input) {
  return input.handle !== undefined || input.save_to === undefined;
}

// The gateway's arguments a delegated call carries, as the person gave them.
const delegatedArguments = ['prompt', 'size', 'model', 'selectionPolicy', 'inputImage', 'mask', 'wait_ms'];

// sizeRefusal is the tool's answer when the engine does not make the
// requested size: the sizes it names (a reason ending size:WxH,WxH), else auto.
function sizeRefusal(size, reason) {
  const listed = /(?:^|:)size:([0-9x,]+)$/.exec(String(reason ?? ''))?.[1];
  const allowed = ['auto', ...(listed ? listed.split(',').filter(Boolean) : [])];
  return `This picture model does not make ${size}. Allowed sizes: ${allowed.join(', ')}. Leave size out for auto.`;
}
const sizeReason = /(?:^|:)size(?::[0-9x,]+)?$/;

/**
 * The file save_to names inside the project directory, checked before the
 * image is generated: a relative path with an image extension that stays in
 * the project after symbolic links, and no existing file unless overwrite.
 * Returns {path, shown} or {refused}.
 */
export async function savePath(directory, saveTo, overwrite = false) {
  if (typeof directory !== 'string' || !isAbsolute(directory)) return {refused: 'the project directory is unknown'};
  const wanted = saveTo.trim();
  if (!wanted || isAbsolute(wanted) || win32.isAbsolute(wanted) || wanted.includes('\0')) {
    return {refused: 'save_to must be a path relative to the project'};
  }
  // A colon names a drive or, on NTFS, a hidden alternate data stream.
  if (wanted.includes(':')) return {refused: 'save_to must not contain a colon'};
  if (!extensions[extname(wanted).toLowerCase()]) return {refused: 'save_to must end in .png, .jpg, .jpeg or .webp'};
  const root = await realpath(directory);
  const path = resolve(root, wanted);
  const shown = relative(root, path);
  const display = shown.split(sep).join('/');
  if (!await within(path, root)) return {refused: 'save_to must stay inside the project'};
  try {
    const found = await stat(path);
    if (found.isDirectory()) return {refused: `${display} is a folder`};
    if (!overwrite) return {refused: `${display} exists; set overwrite to true to replace it`};
  } catch (error) {
    if (error?.code !== 'ENOENT') throw error;
  }
  return {root, path, shown: display};
}

/**
 * Write the image to the checked path. The approval can take minutes, so the
 * folder is checked before and after creating its parent: a link or junction
 * found at either check is refused. A change racing a check and the following
 * filesystem operation remains possible. A new file is created with wx; a
 * replacement is a new file renamed onto the name, which never writes into a
 * hard-linked file outside the project.
 */
export async function saveImage(target, bytes, mediaType, overwrite) {
  if (extensions[extname(target.path).toLowerCase()] !== mediaType) {
    return `Not saved: the image is ${mediaType}, and ${target.shown} names another type.`;
  }
  const moved = `Not saved: a folder on the way to ${target.shown} now leads outside the project.`;
  if (!await within(target.path, target.root)) return moved;
  await mkdir(dirname(target.path), {recursive: true});
  if (!await within(target.path, target.root)) return moved;
  if (!overwrite) {
    try {
      await writeFile(target.path, bytes, {flag: 'wx'});
    } catch (error) {
      if (error?.code === 'EEXIST') return `Not saved: ${target.shown} appeared meanwhile; set overwrite to true to replace it.`;
      throw error;
    }
    return `Saved to ${target.path}.`;
  }
  const temporary = `${target.path}.${randomBytes(6).toString('hex')}.oa-tmp`;
  try {
    await writeFile(temporary, bytes, {flag: 'wx'});
    await rename(temporary, target.path);
  } catch (error) {
    await rm(temporary, {force: true}).catch(() => {});
    throw error;
  }
  return `Saved to ${target.path}.`;
}

// inputMediaType opens one OA content digest the caller may read and names
// its media type from its first bytes; the runtime verifies the digest and
// the type again before the engine receives the image.
async function inputMediaType(reader, digest) {
  const opened = await reader.open(digest);
  if (opened?.outcome !== 'opened' || opened.resource?.digest !== digest) return {error: opened?.outcome ?? 'invalid'};
  try {
    const response = await reader.read(opened.resource.handle, 0n, 16n);
    const data = response?.chunk?.data;
    if (response?.outcome !== 'data' || !(data instanceof Uint8Array)) return {error: response?.outcome ?? 'invalid'};
    return {mediaType: sniffMediaType(Buffer.from(data))};
  } finally {
    await reader.close(opened.resource.handle);
  }
}

// described adds a parameter description where the schema builder has one.
function described(schema, text) {
  return typeof schema?.describe === 'function' ? schema.describe(text) : schema;
}

// A picture belongs to the program that saved it: calls without save_to run
// as the gateway helper, calls with save_to as OpenCode, and a content read
// admits only the same program's pictures.
const savedInProject = 'That picture was saved in the project; pass save_to to edit it.';
const savedInPictures = 'That picture was saved in Pictures; leave out save_to to edit it.';

// crossedIdentity says whether a delegated edit's input image belongs to the
// other program identity.
function crossedIdentity(answer) {
  return (answer?.outcome === 'forbidden' || answer?.outcome === 'unknown')
    && String(answer?.message ?? '').startsWith('Input image unavailable');
}

// The gateway outcomes a person decides on, passed on whole as before.
const permissionOutcomes = new Set(['awaiting_permission', 'permission_refused', 'permission_blocked', 'unknown']);
// Gateway outcomes that are an answer, not a failed OA operation (A-F19).
const answeredOutcomes = new Set(['saved', 'working', 'invalid', 'not_set_up', 'hosted', 'no_pictures_folder', 'cancelled']);
const savedTypes = {'.png': 'image/png', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.webp': 'image/webp'};

/**
 * The tool's result for the gateway's oa_image_generate answer: its sentence
 * unchanged, with the saved picture attached so the model can look at it.
 */
async function delegatedAnswer(answer, failures) {
  if (!answer || typeof answer !== 'object' || typeof answer.outcome !== 'string') return pictureConnectionPending;
  if (answer.outcome === 'unavailable' && answer.reason === 'native_helper_unavailable') return pictureConnectionPending;
  if (permissionOutcomes.has(answer.outcome)) return JSON.stringify(answer);
  const message = typeof answer.message === 'string' && answer.message ? answer.message : `Image generation refused: ${answer.outcome}.`;
  if (answer.outcome !== 'saved') {
    if (answeredOutcomes.has(answer.outcome)) return message;
    return message + failureNote(failures?.record({operation: 'image', outcome: answer.outcome}));
  }
  const result = {title: 'OpenAbstractions image', output: message,
    metadata: {digest: answer.digest ?? '', path: answer.path ?? '', savedTo: ''}};
  const mime = savedTypes[extname(String(answer.path ?? '')).toLowerCase()];
  try {
    const bytes = mime && isAbsolute(answer.path) ? await readFile(answer.path) : null;
    if (bytes && bytes.length <= maxImageBytes && mediaMatchers[mime](bytes)) {
      Object.assign(result.metadata, {mediaType: mime, bytes: bytes.length});
      result.attachments = [{type: 'file', mime, url: `data:${mime};base64,${bytes.toString('base64')}`}];
    }
  } catch {
    // The saved file stays the answer; the attachment is a convenience.
  }
  return result;
}

/**
 * OpenCode's oa_image_generate. A picture for the person's Pictures folder,
 * an edit and a saved policy included, and a continuation by handle go to the
 * gateway's oa_image_generate through delegate(args, context), this chat's OA
 * connection: one implementation resolves Pictures, runs the durable job and
 * saves the file. A call naming save_to runs here through image@1/image@2 and
 * saves into the project.
 */
export function createImageTool({tool, resolveImage, resolveReader, resolvePermission, failures, delegate}) {
  if (typeof tool !== 'function' || typeof resolveImage !== 'function' || typeof resolveReader !== 'function') {
    throw new TypeError('Image tool requires generated image and content-reader resolvers.');
  }
  return tool({
    description: 'Make one picture and save it in the person\'s Pictures folder; the result names the file. '
      + 'Only prompt is needed; leave out model and size. Waits wait_ms (default 20000); if the result gives progress and a handle, call again with that handle. '
      + 'save_to: a project path ending .png, .jpg or .webp instead. '
      + 'To edit, give inputImage, an OA image digest (sha256:...); '
      + "mask is a PNG digest of the input's pixel size whose fully transparent pixels mark the area to change. "
      + 'For a workflow in an app such as ComfyUI, use oa_applications_command_call.',
    args: {prompt: described(tool.schema.string(), 'What the picture shows.'),
      size: described(tool.schema.string(), 'auto (default): the model\'s own size; or WIDTHxHEIGHT such as 512x512.').optional(),
      model: described(tool.schema.string(), 'Leave out for the person\'s picture model.').optional(),
      selectionPolicy: described(tool.schema.string(), 'A saved policy name; leave out for the picture default.').optional(),
      save_to: tool.schema.string().optional(), overwrite: tool.schema.boolean().optional(),
      inputImage: tool.schema.string().optional(), mask: tool.schema.string().optional(),
      handle: described(tool.schema.string(), 'Only to wait for a picture still being made: the handle an earlier call gave, alone or with wait_ms.').optional(),
      wait_ms: described(tool.schema.number(), 'Milliseconds to wait before answering with a handle; default 20000.').optional()},
    async execute(input, context) {
      const args = validRequest(input);
      const signal = context?.abort ?? context?.signal;
      if (signal?.aborted) return 'Image generation cancelled.';
      if (delegates(input ?? {})) {
        if (typeof delegate !== 'function') return pictureConnectionPending;
        let forward = args.wait_ms === undefined ? {handle: args.handle} : {handle: args.handle, wait_ms: args.wait_ms};
        if (args.handle === undefined) {
          if (typeof context?.ask !== 'function') return 'Image generation needs an available approval prompt.';
          const sized = args.size === 'auto' ? '' : ` ${args.size}`;
          const named = input.selectionPolicy !== undefined;
          const target = args.model !== undefined ? `model ${JSON.stringify(args.model)}`
            : named ? `saved policy ${JSON.stringify(args.selectionPolicy)}` : 'your picture model';
          const edit = args.inputImage !== undefined;
          const saying = ` and save it in Pictures/${defaultPictureFolder}`;
          await context.ask({permission: 'openabstractions_image_generate',
            patterns: [edit ? `Edit one${sized} picture ${args.inputImage.slice(0, 19)}... with ${target}${saying}.`
              : `Make one${sized} picture with ${target}${saying}.`],
            always: [], metadata: {model: args.model ?? '', selectionPolicy: named ? args.selectionPolicy : '', size: args.size, count: 1,
              mode: edit ? 'edit' : 'generate', inputImage: args.inputImage ?? '', mask: args.mask ?? '',
              saveTo: `Pictures/${defaultPictureFolder}`, overwrite: false}});
          if (signal?.aborted) return 'Image generation cancelled.';
          forward = {size: args.size};
          for (const name of delegatedArguments) if (input[name] !== undefined) forward[name] = input[name];
        }
        const answer = await delegate(forward, context);
        if (forward.inputImage !== undefined && crossedIdentity(answer)) return savedInProject;
        return delegatedAnswer(answer, failures);
      }
      if (typeof context?.ask !== 'function') return 'Image generation needs an available approval prompt.';
      const target = args.model !== undefined ? `model ${JSON.stringify(args.model)}`
        : args.selectionPolicy === defaultPicturePolicy ? 'your picture model' : `saved policy ${JSON.stringify(args.selectionPolicy)}`;
      const edit = args.inputImage !== undefined;
      // The save_to path is checked before anything is generated or asked.
      let save = await savePath(context?.directory, args.save_to, args.overwrite === true);
      if (save.refused) return `Image not generated: ${save.refused}.`;
      const saying = ` and ${args.overwrite ? 'save or replace' : 'save'} it as ${save.shown}`;
      const sized = args.size === 'auto' ? '' : ` ${args.size}`;
      await context.ask({permission: 'openabstractions_image_generate',
        patterns: [edit ? `Edit one${sized} picture ${args.inputImage.slice(0, 19)}... with ${target}${saying}.`
          : `Make one${sized} picture with ${target}${saying}.`],
        always: [], metadata: {model: args.model ?? '', selectionPolicy: args.selectionPolicy ?? '', size: args.size, count: 1,
          mode: edit ? 'edit' : 'generate', inputImage: args.inputImage ?? '', mask: args.mask ?? '',
          saveTo: save.shown, overwrite: args.overwrite === true}});
      if (signal?.aborted) return 'Image generation cancelled.';
      const inputs = {};
      if (edit) {
        const reader = await resolveReader({signal});
        const image = await inputMediaType(reader, args.inputImage);
        if (image.error === 'forbidden' || image.error === 'unknown') return savedInPictures;
        if (image.error) return `Input image unavailable: ${image.error}.`;
        if (!image.mediaType) return 'The input image must be a PNG, JPEG or WebP image.';
        Object.assign(inputs, {imageDigest: args.inputImage, imageMediaType: image.mediaType});
        if (args.mask !== undefined) {
          const mask = await inputMediaType(reader, args.mask);
          if (mask.error) return `Mask unavailable: ${mask.error}.`;
          if (mask.mediaType !== 'image/png') return 'The mask must be a PNG image.';
          Object.assign(inputs, {maskDigest: args.mask, maskMediaType: 'image/png'});
        }
        if (signal?.aborted) return 'Image generation cancelled.';
      }
      const contract = args.selectionPolicy ? 'abstraction.inference/image@2' : 'abstraction.inference/image@1';
      const image = await resolveImage({signal, contract});
      if (signal?.aborted) return 'Image generation cancelled.';
      let operation = '';
      let complete = false;
      try {
        const content = {mode: edit ? 'edit' : 'generate', prompt: args.prompt, size: args.size, count: 1n, guarantees: [], ...inputs};
        const request = args.selectionPolicy ? newImageRequestV2() : newImageRequest();
        if (args.selectionPolicy) {
          request.target.selectionPolicy = args.selectionPolicy;
          Object.assign(request.content, content);
        } else {
          Object.assign(request, {model: args.model, ...content});
        }
        const admission = await invokeWithPermission(() => image.start(request), {resolvePermission, signal});
        if (['awaiting_permission', 'permission_refused', 'permission_blocked'].includes(admission?.outcome)) return JSON.stringify(admission);
        if (admission?.outcome === 'no_host' && /^router:no-host(?:\s|$)/.test(String(admission.reason ?? ''))) return noPictureEngine;
        if (admission?.outcome === 'no_host' && args.selectionPolicy === defaultPicturePolicy
            && /^selection:(?:unknown|no_route)(?:\s|$)/.test(String(admission.reason ?? ''))) return noPictureEngine;
        if (admission?.outcome === 'unsupported_feature' && sizeReason.test(String(admission.reason ?? ''))) return sizeRefusal(args.size, admission.reason);
        if (admission?.outcome !== 'accepted' || !admission.operation) {
          // An OA refusal is a failure the person may ask about (A-F19).
          const note = failureNote(failures?.record({operation: 'image', outcome: admission?.outcome ?? 'unavailable'}));
          // The person reads the grant words; a question's id is never page text.
          const shown = String(admission?.reason ?? '').split(/\s+/).filter((word) => word && !word.startsWith('rights:question_pending:')).join(' ');
          const refused = `Image generation refused: ${admission?.outcome ?? 'unavailable'} (${shown}).${note}`;
          const reason = admission?.outcome === 'not_permitted' ? admission?.reason ?? '' : '';
          if (/^rights:not_granted(?:\s|$)/.test(reason)) {
            return `${refused} No question is waiting; allow it on the Permissions page of the OpenAbstractions Panel.`;
          }
          if (/^rights:denied(?:\s|$)/.test(reason)) {
            return `${refused} This program is blocked from doing that; to change that, allow it on the Permissions page of the OpenAbstractions Panel.`;
          }
          return refused;
        }
        operation = admission.operation;
        let cursor = 0n;
        let result = null;
        for (;;) {
          if (signal?.aborted) return 'Image generation cancelled.';
          const page = await image.observe(operation, cursor, 64n, 65536n, 25000n);
          if (page?.outcome !== 'page' || !Array.isArray(page.deltas) || BigInt(page.next) < cursor) {
            throw new Error(`Image observation stopped: ${page?.outcome ?? 'invalid'}`);
          }
          for (const delta of page.deltas) {
            if (delta.kind === 'image_result') result = delta.imageResult;
            if (delta.imageEnd) {
              const end = delta.imageEnd;
              complete = true;
              if (end.outcome === 'unsupported_feature' && sizeReason.test(String(end.reason ?? ''))) return sizeRefusal(args.size, end.reason);
              if (end.outcome !== 'completed') {
                const note = end.outcome === 'cancelled' ? '' : failureNote(failures?.record({operation: 'image', host: end.host ?? '', outcome: end.outcome}));
                return `Image generation ${end.outcome}: ${end.reason}.${note}`;
              }
              if (end.deliveries?.length !== 1 || !result || result.index !== 0n
                  || result.delivery?.digest !== end.deliveries[0].digest) throw new Error('Image result and terminal delivery disagree.');
              const reader = await resolveReader({signal});
              const bytes = await readDelivery(reader, end.deliveries[0], {mediaTypes, maxBytes: maxImageBytes, what: 'Image'});
              const mediaType = end.deliveries[0].mediaType;
              const saved = await saveImage(save, bytes, mediaType, args.overwrite === true);
              if (!saved.startsWith('Saved')) save = null;
              const digest = end.deliveries[0].digest;
              return {title: 'OpenAbstractions image',
                output: `${saved} To edit it, pass inputImage ${digest} with save_to.`,
                metadata: {operation, host: end.host, model: end.model, digest,
                  mediaType, bytes: Number(end.deliveries[0].size), savedTo: save?.shown ?? '', path: save?.path ?? ''},
                attachments: [{type: 'file', mime: end.deliveries[0].mediaType,
                  url: `data:${end.deliveries[0].mediaType};base64,${bytes.toString('base64')}`}]};
            }
          }
          if (page.atEnd || BigInt(page.next) === cursor && page.deltas.length) throw new Error('Image operation ended without a terminal result.');
          cursor = BigInt(page.next);
        }
      } finally {
        if (operation && !complete) {
          try { await (await resolveImage({signal: null, contract})).cancel(operation); } catch { /* original result remains authoritative */ }
        }
      }
    },
  });
}
