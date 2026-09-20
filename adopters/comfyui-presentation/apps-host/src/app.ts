import { App } from "@modelcontextprotocol/ext-apps";
import type { CallToolResult } from "@modelcontextprotocol/client";

type Proposal = {
  outcome?: string;
  token?: string;
  node?: number;
  title?: string;
  widget?: string;
  instance?: string;
  application_instance?: string;
  context?: string;
  revision?: string;
  before?: number;
  after?: number;
};

function text(id: string, value: unknown) {
  const element = document.getElementById(id);
  if (element) element.textContent = value == null ? "" : String(value);
}

export function renderResult(result: CallToolResult) {
  const record = (result.structuredContent ?? {}) as Proposal;
  document.getElementById("loading")!.hidden = true;
  if (record.outcome === "proposed" && Number.isFinite(record.node) && record.widget) {
    text("target", `Change ${record.title || "the"} node ${record.node}’s ${record.widget} from ${record.before} to ${record.after}?`);
    text("node", record.node);
    text("widget", record.widget);
    text("instance", record.instance);
    text("application-instance", record.application_instance);
    document.getElementById("application-instance-label")!.hidden = !record.application_instance;
    document.getElementById("application-instance")!.hidden = !record.application_instance;
    text("context", record.context);
    text("revision", record.revision);
    text("token", record.token);
    document.getElementById("token-label")!.hidden = !record.token;
    document.getElementById("token")!.hidden = !record.token;
    document.getElementById("proposal")!.hidden = false;
    document.getElementById("refusal")!.hidden = true;
    return;
  }
  text("outcome", record.outcome || (result.isError ? "error" : "unavailable"));
  document.getElementById("proposal")!.hidden = true;
  document.getElementById("refusal")!.hidden = false;
}

const app = new App({ name: "OA ComfyUI proposal view", version: "0.1.0-dev" });
app.ontoolresult = renderResult;
app.ontoolcancelled = ({ reason }) => renderResult({ isError: true, content: [], structuredContent: { outcome: reason || "cancelled" } });
app.onerror = console.error;
void app.connect();
