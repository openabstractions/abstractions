import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { spawn, execFileSync } from "node:child_process";
import { fileURLToPath, pathToFileURL } from "node:url";

import { start } from "../server.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const appRoot = path.dirname(here);
const repository = path.resolve(appRoot, "../../..");
const extApps = path.resolve(process.env.OA_EXT_APPS_CHECKOUT || path.join(repository, ".forks", "ext-apps-v2.0.0"));
const hostRoot = path.join(extApps, "examples", "basic-host");
const expectedCommit = "352f6ced4d80772e92b4e7a311854481a8d65b04";
const edge = process.env.OA_BROWSER_EXECUTABLE || "C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe";

async function waitForURL(url, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url);
      if (response.ok) return;
    } catch { /* server is still starting */ }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`timed out waiting for ${url}`);
}

async function waitForRenderedRecord(page, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    for (const frame of page.frames()) {
      const body = await frame.locator("body").textContent({ timeout: 250 }).catch(() => "");
      if (body?.includes("Demonstration: preview a ComfyUI sampling-steps edit") && body.includes("Change KSampler node 70’s steps from 8 to 12?") && body.includes("Preview — nothing changed yet") && body.includes("workflow-fixture")) return { body, frame };
    }
    await page.waitForTimeout(50);
  }
  const bodies = [];
  for (const frame of page.frames()) bodies.push((await frame.locator("body").textContent({ timeout: 250 }).catch(() => ""))?.replace(/\s+/g, " ").slice(0, 600));
  throw new Error(`official host did not render the deterministic proposal record: ${JSON.stringify(bodies)}`);
}

let adapter;
let host;
let browser;
const hostStderr = [];
const hostStdout = [];
try {
  assert.equal(execFileSync("git", ["-C", extApps, "rev-parse", "HEAD"], { encoding: "utf8" }).trim(), expectedCommit);
  const implementation = await fs.readFile(path.join(hostRoot, "src", "implementation.ts"), "utf8");
  assert.match(implementation, /io\.modelcontextprotocol\/ui/);
  assert.match(implementation, /mimeTypes:\s*\[RESOURCE_MIME_TYPE\]/);
  await fs.access(edge);

  adapter = await start([
    "--ordinary-command", process.execPath,
    "--ordinary-arg", path.join(here, "fake-ordinary.mjs"),
    "--ordinary-cwd", appRoot,
    "--host-origin", "http://127.0.0.1:8080",
    "--port", "3001",
  ]);
  const oversized = await fetch("http://127.0.0.1:3001/mcp", {
    method: "POST",
    headers: { "content-type": "application/json", origin: "http://127.0.0.1:8080" },
    body: JSON.stringify({ padding: "x".repeat(17 << 10) }),
  });
  assert.equal(oversized.status, 413);
  host = spawn(process.execPath, ["--experimental-strip-types", "serve.ts"], {
    cwd: hostRoot,
    env: { ...process.env, SERVERS: '["http://127.0.0.1:3001/mcp"]', HOST_PORT: "8080", SANDBOX_PORT: "8081" },
    stdio: ["ignore", "pipe", "pipe"],
    windowsHide: true,
  });
  host.stderr.on("data", (chunk) => hostStderr.push(String(chunk)));
  host.stdout.on("data", (chunk) => hostStdout.push(String(chunk)));
  await waitForURL("http://127.0.0.1:8080/api/servers", 8_000);

  const playwright = await import(pathToFileURL(path.join(extApps, "node_modules", "playwright", "index.mjs")));
  browser = await playwright.chromium.launch({ headless: true, executablePath: edge });
  const page = await browser.newPage();
  await page.goto("http://127.0.0.1:8080", { waitUntil: "domcontentloaded", timeout: 8_000 });
  await page.waitForFunction(() => Array.from(document.querySelectorAll("select")[1]?.options ?? []).some((option) => option.value === "comfy_read_context"), undefined, { timeout: 8_000 });
  const tool = page.locator("select").nth(1);
  await tool.selectOption("comfy_read_context");
  await page.getByRole("button", { name: "Call Tool" }).click();
  await page.waitForFunction(() => !document.body.innerText.includes("Loading result..."), undefined, { timeout: 8_000 });
  const contextResult = await page.locator("body").innerText();
  assert.match(contextResult, /instance-fixture/);
  await tool.selectOption("comfy_preview_parameter");
  const input = page.locator("textarea");
  await input.fill(JSON.stringify({ instance: "instance-fixture", context: "workflow-fixture", revision: "r7", node_id: 70, widget: "steps", value: 12 }));
  const renderStarted = performance.now();
  await page.getByRole("button", { name: "Call Tool" }).click();
  const renderedView = await waitForRenderedRecord(page, 8_000);
  const rendered = renderedView.body;
  const renderMs = Math.round((performance.now() - renderStarted) * 10) / 10;
  assert.match(rendered, /Node\s*70/);
  assert.match(rendered, /Parameter\s*steps/);
  assert.match(rendered, /Change KSampler node 70’s steps from 8 to 12\?/);
  assert.match(rendered, /Preview — nothing changed yet/);
  assert.match(rendered, /Demonstration: preview a ComfyUI sampling-steps edit/);
  assert.match(rendered, /Instance\s*instance-fixture/);
  assert.match(rendered, /Context\s*workflow-fixture/);
  assert.match(rendered, /Revision\s*r7/);
  assert.match(rendered, /fixture-proposal/);
  assert.equal(await renderedView.frame.locator("details").getAttribute("open"), null);
  assert(adapter.negotiationEvents.some((event) => event.negotiated && event.mimeTypes.includes("text/html;profile=mcp-app")));
  let resultSettled = true;
  try {
    await page.waitForFunction(() => !document.body.innerText.includes("Loading result..."), undefined, { timeout: 3_000 });
  } catch { resultSettled = false; }
  const settledMs = Math.round((performance.now() - renderStarted) * 10) / 10;
  const screenshot = process.env.OA_APPS_SCREENSHOT;
  if (screenshot) {
    await fs.mkdir(path.dirname(path.resolve(screenshot)), { recursive: true });
    await page.screenshot({ path: path.resolve(screenshot), fullPage: true });
  }
  const cardScreenshot = process.env.OA_APPS_CARD_SCREENSHOT;
  if (cardScreenshot) {
    await fs.mkdir(path.dirname(path.resolve(cardScreenshot)), { recursive: true });
    await renderedView.frame.locator("body").screenshot({ path: path.resolve(cardScreenshot) });
  }
  const record = { outcome: "proposed", token: "fixture-proposal", instance: "instance-fixture", context: "workflow-fixture", revision: "r7", node: 70, title: "KSampler", widget: "steps", before: 8, after: 12 };
  const callToolResult = { content: [{ type: "text", text: JSON.stringify(record) }], structuredContent: record };
  const resourceBytes = (await fs.stat(path.join(appRoot, "dist", "app.html"))).size;
  console.log(JSON.stringify({
    outcome: "rendered", extension: "io.modelcontextprotocol/ui", mimeType: "text/html;profile=mcp-app",
    target: { instance: "instance-fixture", context: "workflow-fixture", revision: "r7", node: 70, widget: "steps", before: 8, after: 12 },
    bytes: { proposal: Buffer.byteLength(JSON.stringify(record)), modelVisible: Buffer.byteLength(JSON.stringify(callToolResult)), appResource: resourceBytes },
    renderMs, resultSettled, settledMs, screenshot: screenshot ? path.resolve(screenshot) : "", cardScreenshot: cardScreenshot ? path.resolve(cardScreenshot) : "",
  }));
} catch (error) {
  if (hostStderr.length) process.stderr.write(hostStderr.join(""));
  if (hostStdout.length) process.stderr.write(hostStdout.join(""));
  throw error;
} finally {
  if (browser) await browser.close().catch(() => {});
  if (host && host.exitCode == null) {
    host.kill();
    await new Promise((resolve) => { host.once("exit", resolve); setTimeout(resolve, 2_000); });
  }
  if (adapter) await adapter.close().catch(() => {});
}
