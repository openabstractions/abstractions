import {randomBytes} from 'node:crypto';
import {mkdir, realpath, rename, rm, stat, writeFile} from 'node:fs/promises';
import {dirname, extname, isAbsolute, relative, resolve, sep, win32} from 'node:path';
import {within} from '@openabstractions/ipc/path';
import {newImageRequest, newImageRequestV2} from '@openabstractions/inference/protocol';
import {mediaMatchers, readDelivery} from './content-delivery.js';

const digestPattern = /^sha256:[0-9a-f]{64}$/;
const mediaTypes = new Set(['image/png', 'image/jpeg', 'image/webp']);
const maxImageBytes = 8 * 1024 * 1024;
const policyName = /^[a-z0-9][a-z0-9_-]{0,63}$/;
// The file extensions save_to accepts for each media type the tool returns.
const extensions = {'.png': 'image/png', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.webp': 'image/webp'};

function sniffMediaType(bytes) {
  for (const mediaType of mediaTypes) if (mediaMatchers[mediaType](bytes)) return mediaType;
  return '';
}

function validRequest(args) {
  const explicit = typeof args?.model === 'string' && args.model.trim().length > 0;
  const policy = typeof args?.selectionPolicy === 'string' && policyName.test(args.selectionPolicy);
  if (!args || Object.keys(args).some((key) => !['model', 'selectionPolicy', 'prompt', 'size', 'inputImage', 'mask', 'save_to', 'overwrite'].includes(key))
      || explicit === policy || (args.selectionPolicy !== undefined && !policy)
      || (args.model !== undefined && !explicit)
      || typeof args.prompt !== 'string' || !args.prompt.trim()
      || Buffer.byteLength(args.prompt) > 32768
      || typeof args.size !== 'string' || !/^(auto|[1-9][0-9]*x[1-9][0-9]*)$/.test(args.size)
      || (args.inputImage !== undefined && !digestPattern.test(args.inputImage))
      || (args.mask !== undefined && (args.inputImage === undefined || !digestPattern.test(args.mask)))
      || (args.save_to !== undefined && typeof args.save_to !== 'string')
      || (args.overwrite !== undefined && (typeof args.overwrite !== 'boolean' || args.save_to === undefined))) {
    throw new TypeError('Provide exactly one model or saved selectionPolicy, a nonempty prompt, size auto or WIDTHxHEIGHT, '
      + 'and to edit, an inputImage OA content digest with an optional PNG mask digest; save_to is a path inside the project, '
      + 'and overwrite goes only with save_to.');
  }
}

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
    return `Saved to ${target.shown}.`;
  }
  const temporary = `${target.path}.${randomBytes(6).toString('hex')}.oa-tmp`;
  try {
    await writeFile(temporary, bytes, {flag: 'wx'});
    await rename(temporary, target.path);
  } catch (error) {
    await rm(temporary, {force: true}).catch(() => {});
    throw error;
  }
  return `Saved to ${target.shown}.`;
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

export function createImageTool({tool, resolveImage, resolveReader}) {
  if (typeof tool !== 'function' || typeof resolveImage !== 'function' || typeof resolveReader !== 'function') {
    throw new TypeError('Image tool requires generated image and content-reader resolvers.');
  }
  return tool({
    description: 'Generate one image through an explicit OpenAbstractions model or saved selection policy, or edit one: '
      + 'inputImage is the SHA-256 digest (sha256:...) of an OA image, such as an earlier result, and mask an optional digest of a PNG '
      + "of the input's pixel size whose fully transparent pixels mark the area to change; other pixels are kept. "
      + 'Returns the verified image as an OpenCode attachment. save_to, a path relative to the project such as '
      + 'assets/fox.png, also writes it there; the extension must match the image type, and an existing file is kept '
      + 'unless overwrite is true. Only prompt, target, size and these inputs are supported.',
    args: {model: tool.schema.string().optional(), selectionPolicy: tool.schema.string().optional(),
      prompt: tool.schema.string(), size: tool.schema.string(),
      inputImage: tool.schema.string().optional(), mask: tool.schema.string().optional(),
      save_to: tool.schema.string().optional(), overwrite: tool.schema.boolean().optional()},
    async execute(args, context) {
      validRequest(args);
      const signal = context?.abort ?? context?.signal;
      if (signal?.aborted) return 'Image generation cancelled.';
      if (typeof context?.ask !== 'function') return 'Image generation needs an available approval prompt.';
      const target = args.selectionPolicy ? `saved policy ${JSON.stringify(args.selectionPolicy)}`
        : `model ${JSON.stringify(args.model)}`;
      const edit = args.inputImage !== undefined;
      // The save path is checked before anything is generated or asked.
      let save = null;
      if (args.save_to !== undefined) {
        save = await savePath(context?.directory, args.save_to, args.overwrite === true);
        if (save.refused) return `Image not generated: ${save.refused}.`;
      }
      const saying = save ? ` and ${args.overwrite ? 'save or replace' : 'save'} it as ${save.shown}` : '';
      await context.ask({permission: 'openabstractions_image_generate',
        patterns: [edit ? `Edit one ${args.size} image ${args.inputImage.slice(0, 19)}... with OA ${target}${saying}.`
          : `Generate one ${args.size} image with OA ${target}${saying}.`],
        always: [], metadata: {model: args.model ?? '', selectionPolicy: args.selectionPolicy ?? '', size: args.size, count: 1,
          mode: edit ? 'edit' : 'generate', inputImage: args.inputImage ?? '', mask: args.mask ?? '',
          saveTo: save?.shown ?? '', overwrite: args.overwrite === true}});
      if (signal?.aborted) return 'Image generation cancelled.';
      const inputs = {};
      if (edit) {
        const reader = await resolveReader({signal});
        const image = await inputMediaType(reader, args.inputImage);
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
        const admission = await image.start(request);
        if (admission?.outcome !== 'accepted' || !admission.operation) {
          const refused = `Image generation refused: ${admission?.outcome ?? 'unavailable'} (${admission?.reason ?? ''}).`;
          // A not_granted refusal leaves a first-use question pending (asks
          // ASK-R2); the sentence names it as the chat refusal does.
          const reason = admission?.outcome === 'not_permitted' ? admission?.reason ?? '' : '';
          if (/^rights:not_granted(?:\s|$)/.test(reason)) {
            return `${refused} A question is waiting in the OpenAbstractions Panel; allow it there and retry.`;
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
              if (end.outcome !== 'completed') return `Image generation ${end.outcome}: ${end.reason}.`;
              if (end.deliveries?.length !== 1 || !result || result.index !== 0n
                  || result.delivery?.digest !== end.deliveries[0].digest) throw new Error('Image result and terminal delivery disagree.');
              const reader = await resolveReader({signal});
              const bytes = await readDelivery(reader, end.deliveries[0], {mediaTypes, maxBytes: maxImageBytes, what: 'Image'});
              const saved = save ? ` ${await saveImage(save, bytes, end.deliveries[0].mediaType, args.overwrite === true)}` : '';
              return {title: 'OpenAbstractions image',
                output: `Generated one ${end.deliveries[0].mediaType} image with ${end.host}/${end.model}; SHA-256 ${end.deliveries[0].digest}.${saved}`,
                metadata: {operation, host: end.host, model: end.model, digest: end.deliveries[0].digest,
                  mediaType: end.deliveries[0].mediaType, bytes: Number(end.deliveries[0].size), savedTo: saved.startsWith(' Saved') ? save.shown : ''},
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
