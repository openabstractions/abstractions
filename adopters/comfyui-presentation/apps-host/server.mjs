import fs from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

import { Client } from "@modelcontextprotocol/client";
import { StdioClientTransport } from "@modelcontextprotocol/client/stdio";
import { createMcpExpressApp } from "@modelcontextprotocol/express";
import { NodeStreamableHTTPServerTransport } from "@modelcontextprotocol/node";
import { McpServer } from "@modelcontextprotocol/server";
import { registerAppResource, registerAppTool, RESOURCE_MIME_TYPE, EXTENSION_ID } from "@modelcontextprotocol/ext-apps/server";
import cors from "cors";
import { z } from "zod";

export const maxRecordBytes = 16 << 10;
export const maxActiveRequests = 4;
export const maxTokenFileBytes = 4 << 10;
export const maxNegotiationEvents = 64;
export const resourceUri = "ui://openabstractions/comfy-parameter-proposal.html";

export async function readBoundedTokenFile(filename) {
  const file = await fs.open(filename, "r");
  try {
    const buffer = Buffer.alloc(maxTokenFileBytes + 1);
    const { bytesRead } = await file.read(buffer, 0, buffer.length, 0);
    if (bytesRead > maxTokenFileBytes) throw new Error("Apps adapter: ordinary token file exceeds 4096-byte bound");
    const token = buffer.subarray(0, bytesRead).toString("utf8").trim();
    if (token.length < 32) throw new Error("Apps adapter: ordinary token file is invalid");
    return token;
  } finally {
    await file.close();
  }
}

export function requireExpectedOrigin(expectedOrigin) {
  return (req, res, next) => {
    const origin = req.get("origin");
    if (origin !== undefined && origin !== expectedOrigin) {
      res.status(403).json({ error: "Apps adapter: request Origin is not allowed" });
      return;
    }
    next();
  };
}

export function appendNegotiationEvent(events, event) {
  if (events.length === maxNegotiationEvents) events.shift();
  events.push(event);
}

export function limitActiveRequests(limit = maxActiveRequests) {
  let active = 0;
  return (req, res, next) => {
    if (active >= limit) {
      res.status(429).json({ error: "Apps adapter: active request limit reached" });
      return;
    }
    active++;
    let released = false;
    const release = () => {
      if (released) return;
      released = true;
      active--;
    };
    res.once("finish", release);
    res.once("close", release);
    next();
  };
}

export function parseArgs(argv) {
  const options = { port: 3001, hostOrigin: "http://127.0.0.1:8080", ordinaryArgs: [] };
  for (let index = 0; index < argv.length; index++) {
    const value = argv[index];
    if (value === "--port") options.port = Number(argv[++index]);
    else if (value === "--host-origin") options.hostOrigin = argv[++index];
    else if (value === "--ordinary-command") options.ordinaryCommand = argv[++index];
    else if (value === "--ordinary-cwd") options.ordinaryCwd = argv[++index];
    else if (value === "--ordinary-arg") options.ordinaryArgs.push(argv[++index]);
    else if (value === "--ordinary-token-file") options.ordinaryTokenFile = argv[++index];
    else throw new Error(`unknown argument: ${value}`);
  }
  let hostOrigin;
  try { hostOrigin = new URL(options.hostOrigin); } catch { /* handled below */ }
  if (!options.ordinaryCommand || !Number.isInteger(options.port) || options.port < 1 || options.port > 65535 ||
      !hostOrigin || hostOrigin.origin !== options.hostOrigin || !["localhost", "127.0.0.1", "::1"].includes(hostOrigin.hostname)) {
    throw new Error("--ordinary-command and a valid --port are required");
  }
  return options;
}

export function boundedProposalResult(result) {
  const encoded = JSON.stringify(result);
  if (Buffer.byteLength(encoded) > maxRecordBytes) throw new Error("Apps adapter: proposal record exceeds 16384-byte bound");
  const record = result?.structuredContent;
  if (!record || typeof record !== "object" || Array.isArray(record) || typeof record.outcome !== "string") {
    throw new Error("Apps adapter: ordinary tool returned no structured proposal record");
  }
  if (record.outcome === "proposed" &&
      ((record.token !== undefined && !boundedString(record.token, 512)) ||
       !boundedString(record.instance, 128) ||
       !boundedString(record.context, 128) || !boundedString(record.revision, 256) ||
       (record.application_instance !== undefined && !boundedString(record.application_instance, 128)) ||
       !Number.isSafeInteger(record.node) || record.node < 0 ||
       !boundedString(record.title, 256) || !boundedString(record.widget, 128) ||
       !Number.isFinite(record.before) || !Number.isFinite(record.after))) {
    throw new Error("Apps adapter: proposed record has invalid target or values");
  }
  return result;
}

function boundedString(value, max) {
  return typeof value === "string" && value.length > 0 && value.length <= max;
}

export function boundedContextResult(result) {
  const encoded = JSON.stringify(result);
  if (Buffer.byteLength(encoded) > maxRecordBytes) throw new Error("Apps adapter: context record exceeds 16384-byte bound");
  const record = result?.structuredContent;
  if (!record || typeof record !== "object" || Array.isArray(record) ||
      !boundedString(record.instance, 128) || !boundedString(record.context, 128) ||
      !boundedString(record.revision, 256) ||
      (record.application_instance !== undefined && !boundedString(record.application_instance, 128))) {
    throw new Error("Apps adapter: ordinary tool returned an invalid context record");
  }
  return result;
}

export function createServer(ordinary, appHtml) {
  const server = new McpServer({ name: "OA ComfyUI MCP Apps comparison", version: "0.1.0-dev" });
  server.registerTool("comfy_read_context", {
    title: "Read ComfyUI context",
    description: "Read the exact browser instance, workflow context and revision required by preview.",
    inputSchema: z.object({}),
    annotations: { readOnlyHint: true, idempotentHint: true, openWorldHint: false },
  }, async (_input, extra) => boundedContextResult(await ordinary.callTool({
    name: "comfy_read_context",
    arguments: {},
  }, undefined, { signal: extra.signal })));
  registerAppTool(server, "comfy_preview_parameter", {
    title: "Preview ComfyUI parameter",
    description: "Create a read-only preview record for one exact ComfyUI numeric parameter. Apply remains in the visible ComfyUI panel.",
    inputSchema: z.object({
      instance: z.string().min(1).max(128),
      application_instance: z.string().min(1).max(128).optional(),
      context: z.string().min(1).max(128),
      revision: z.string().min(1).max(256),
      node_id: z.number().int().nonnegative(),
      widget: z.string().min(1).max(128),
      value: z.number().finite(),
    }),
    annotations: { readOnlyHint: true, idempotentHint: false, openWorldHint: false },
    _meta: { ui: { resourceUri } },
  }, async (input, extra) => boundedProposalResult(await ordinary.callTool({
    name: "comfy_preview_parameter",
    arguments: input,
  }, undefined, { signal: extra.signal })));
  registerAppResource(server, resourceUri, resourceUri, { mimeType: RESOURCE_MIME_TYPE }, async () => ({
    contents: [{ uri: resourceUri, mimeType: RESOURCE_MIME_TYPE, text: appHtml }],
  }));
  return server;
}

export async function start(argv = process.argv.slice(2)) {
  const options = parseArgs(argv);
  const ordinaryEnv = { ...process.env };
  if (options.ordinaryTokenFile) {
    ordinaryEnv.OA_COMFY_BRIDGE_TOKEN = await readBoundedTokenFile(options.ordinaryTokenFile);
  }
  const ordinary = new Client({ name: "oa-comfy-apps-adapter", version: "0.1.0-dev" });
  const transport = new StdioClientTransport({
    command: options.ordinaryCommand,
    args: options.ordinaryArgs,
    cwd: options.ordinaryCwd,
    env: ordinaryEnv,
    stderr: "inherit",
    maxBufferSize: 32 << 10,
  });
  let httpServer;
  try {
    await ordinary.connect(transport);
    const tools = await ordinary.listTools();
    const requiredTools = ["comfy_read_context", "comfy_preview_parameter"];
    if (!requiredTools.every((name) => tools.tools.some((tool) => tool.name === name))) {
      throw new Error("Apps adapter: ordinary server does not expose the required context and preview tools");
    }

    const here = path.dirname(fileURLToPath(import.meta.url));
    const appHtml = await fs.readFile(path.join(here, "dist", "app.html"), "utf8");
    const negotiationEvents = [];
    const app = createMcpExpressApp({ host: "127.0.0.1", jsonLimit: maxRecordBytes });
    app.use(requireExpectedOrigin(options.hostOrigin));
    app.use(cors({ origin: options.hostOrigin, methods: ["POST", "GET", "DELETE"], allowedHeaders: ["content-type", "mcp-protocol-version", "mcp-session-id", "last-event-id"] }));
    app.use(limitActiveRequests());
    app.all("/mcp", async (req, res) => {
      if (req.body?.method === "initialize") {
        const ui = req.body?.params?.capabilities?.extensions?.[EXTENSION_ID];
        const event = { event: "initialize", extension: EXTENSION_ID, negotiated: Boolean(ui), mimeTypes: ui?.mimeTypes ?? [] };
        appendNegotiationEvent(negotiationEvents, event);
        console.log(JSON.stringify(event));
      }
      const server = createServer(ordinary, appHtml);
      const httpTransport = new NodeStreamableHTTPServerTransport({ sessionIdGenerator: undefined });
      res.on("close", () => { void httpTransport.close(); void server.close(); });
      try {
        await server.connect(httpTransport);
        await httpTransport.handleRequest(req, res, req.body);
      } catch (error) {
        if (!res.headersSent) res.status(500).json({ jsonrpc: "2.0", error: { code: -32603, message: String(error) }, id: null });
      }
    });
    app.use((error, _req, res, next) => {
      if (error?.type === "entity.too.large") {
        res.status(413).json({ error: "Apps adapter: request body exceeds 16384-byte bound" });
        return;
      }
      next(error);
    });
    httpServer = app.listen(options.port, "127.0.0.1", () => {
      console.log(`Apps comparison server listening on http://127.0.0.1:${options.port}/mcp`);
    });
    await new Promise((resolve, reject) => {
      if (httpServer.listening) resolve();
      else {
        httpServer.once("listening", resolve);
        httpServer.once("error", reject);
      }
    });
    let closed = false;
    const close = async () => {
      if (closed) return;
      closed = true;
      if (httpServer.listening) await new Promise((resolve) => httpServer.close(resolve));
      await ordinary.close();
    };
    process.once("SIGINT", () => { void close().then(() => process.exit(0)); });
    process.once("SIGTERM", () => { void close().then(() => process.exit(0)); });
    return { httpServer, ordinary, negotiationEvents, close };
  } catch (error) {
    if (httpServer?.listening) await new Promise((resolve) => httpServer.close(resolve));
    try { await ordinary.close(); } catch { /* retain the setup error */ }
    throw error;
  }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  start().catch((error) => { console.error(error); process.exitCode = 1; });
}
