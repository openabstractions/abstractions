import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";

import {
  appendNegotiationEvent,
  boundedContextResult,
  boundedProposalResult,
  limitActiveRequests,
  maxActiveRequests,
  maxNegotiationEvents,
  maxRecordBytes,
  maxTokenFileBytes,
  parseArgs,
  readBoundedTokenFile,
  requireExpectedOrigin,
} from "../server.mjs";

test("passes the ordinary structured proposal record through unchanged", () => {
  const result = {
    content: [{ type: "text", text: "proposal" }],
    structuredContent: { outcome: "proposed", token: "opaque", instance: "instance-1", application_instance: "oa-instance-1", context: "workflow-1", revision: "r7", node: 70, title: "Sampler", widget: "steps", before: 8, after: 12 },
  };
  assert.equal(boundedProposalResult(result), result);
});

test("accepts an OA proposal without its application-local effect handle", () => {
  const result = {
    structuredContent: { outcome: "proposed", instance: "instance-1", application_instance: "oa-instance-1", context: "workflow-1", revision: "r7", node: 70, title: "Sampler", widget: "steps", before: 8, after: 12 },
  };
  assert.equal(boundedProposalResult(result), result);
  const invalid = structuredClone(result);
  invalid.structuredContent.token = "";
  assert.throws(() => boundedProposalResult(invalid), /invalid target/);
});

test("passes a bounded context record through unchanged", () => {
  const result = {
    content: [{ type: "text", text: "context" }],
    structuredContent: { instance: "instance-1", application_instance: "oa-instance-1", context: "workflow-1", revision: "r7" },
  };
  assert.equal(boundedContextResult(result), result);
  assert.throws(() => boundedContextResult({ structuredContent: { instance: "instance-1", application_instance: "", context: "workflow-1", revision: "r7" } }), /invalid context/);
  assert.throws(() => boundedContextResult({ structuredContent: { instance: "instance-1", context: "workflow-1" } }), /invalid context/);
});

test("requires one exact loopback host origin", () => {
  assert.equal(parseArgs(["--ordinary-command", "fixture", "--host-origin", "http://127.0.0.1:8080"]).hostOrigin, "http://127.0.0.1:8080");
  assert.throws(() => parseArgs(["--ordinary-command", "fixture", "--host-origin", "https://example.com"]), /valid --port/);
  assert.throws(() => parseArgs(["--ordinary-command", "fixture", "--host-origin", "http://127.0.0.1:8080/path"]), /valid --port/);
});

test("rejects a mismatched present Origin before MCP handling", () => {
  const middleware = requireExpectedOrigin("http://127.0.0.1:8080");
  let nextCalls = 0;
  const response = {
    statusCode: 0,
    body: undefined,
    status(code) { this.statusCode = code; return this; },
    json(body) { this.body = body; },
  };
  middleware({ get: () => "http://127.0.0.1:9090" }, response, () => { nextCalls++; });
  assert.equal(response.statusCode, 403);
  assert.equal(nextCalls, 0);

  middleware({ get: () => undefined }, response, () => { nextCalls++; });
  middleware({ get: () => "http://127.0.0.1:8080" }, response, () => { nextCalls++; });
  assert.equal(nextCalls, 2);
});

test("bounds token reads before accepting file contents", async () => {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), "oa-apps-token-"));
  try {
    const valid = path.join(directory, "valid.txt");
    const oversized = path.join(directory, "oversized.txt");
    await fs.writeFile(valid, `${"a".repeat(32)}\n`);
    await fs.writeFile(oversized, "a".repeat(maxTokenFileBytes + 1));
    assert.equal(await readBoundedTokenFile(valid), "a".repeat(32));
    await assert.rejects(readBoundedTokenFile(oversized), /4096-byte bound/);
  } finally {
    await fs.rm(directory, { recursive: true, force: true });
  }
});

test("retains only the latest bounded negotiation events", () => {
  const events = [];
  for (let index = 0; index < maxNegotiationEvents + 3; index++) appendNegotiationEvent(events, { index });
  assert.equal(events.length, maxNegotiationEvents);
  assert.equal(events[0].index, 3);
});

test("bounds active HTTP requests and releases each response once", () => {
  const middleware = limitActiveRequests(maxActiveRequests);
  const listeners = [];
  const responses = Array.from({ length: maxActiveRequests }, () => ({
    once(event, callback) { listeners.push({ event, callback }); },
  }));
  let nextCalls = 0;
  for (const response of responses) middleware({}, response, () => { nextCalls++; });
  const refused = {
    statusCode: 0,
    status(code) { this.statusCode = code; return this; },
    json() {},
  };
  middleware({}, refused, () => { nextCalls++; });
  assert.equal(nextCalls, maxActiveRequests);
  assert.equal(refused.statusCode, 429);
  listeners.find(({ event }) => event === "finish").callback();
  listeners.find(({ event }) => event === "close").callback();
  const admitted = { once() {} };
  middleware({}, admitted, () => { nextCalls++; });
  assert.equal(nextCalls, maxActiveRequests + 1);
});

test("refuses absent and oversized proposal records", () => {
  assert.throws(() => boundedProposalResult({ content: [] }), /no structured proposal/);
  assert.throws(() => boundedProposalResult({ structuredContent: { outcome: "proposed", padding: "x".repeat(maxRecordBytes) } }), /16384-byte bound/);
  assert.throws(() => boundedProposalResult({ structuredContent: { outcome: "proposed", token: "token" } }), /invalid target/);
  const stale = { structuredContent: { outcome: "stale", instance: "current", context: "new", revision: "8" } };
  assert.equal(boundedProposalResult(stale), stale);
});
