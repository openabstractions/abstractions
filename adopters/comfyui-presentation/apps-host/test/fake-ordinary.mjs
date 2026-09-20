import { McpServer } from "@modelcontextprotocol/server";
import { StdioServerTransport } from "@modelcontextprotocol/server/stdio";
import { z } from "zod";

const server = new McpServer({ name: "deterministic-comfy-ordinary-fixture", version: "1" });
server.registerTool("comfy_read_context", {
  description: "Deterministic Apps context fixture",
  inputSchema: z.object({}),
}, async () => {
  const record = { instance: "instance-fixture", context: "workflow-fixture", revision: "r7" };
  return { content: [{ type: "text", text: JSON.stringify(record) }], structuredContent: record };
});
server.registerTool("comfy_preview_parameter", {
  description: "Deterministic Apps adapter fixture",
  inputSchema: z.object({ instance: z.string(), context: z.string(), revision: z.string(), node_id: z.number(), widget: z.string(), value: z.number() }),
}, async ({ instance, context, revision, node_id, widget, value }) => {
  const record = { outcome: "proposed", token: "fixture-proposal", instance, context, revision, node: node_id, title: "KSampler", widget, before: 8, after: value };
  return { content: [{ type: "text", text: JSON.stringify(record) }], structuredContent: record };
});
await server.connect(new StdioServerTransport());
