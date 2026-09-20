import { app } from "../../scripts/app.js";
import { describePresentation, presentation } from "./presentation.mjs";

const demo = presentation(app);
let renderContextChange = () => {};
app.registerExtension({
  name: "openabstractions.presentation.experiment",
  afterConfigureGraph() { demo.reset(); renderContextChange(); },
  setup() {
    // Exposed only in this opt-in local experiment for an integration harness.
    // No network listener or automatic agent action is registered.
    app.oaPresentationExperiment = demo;
    renderContextChange = mountPanel();
  },
});


// Visible, opt-in controls let the same bounded proposal be inspected and
// approved by a person. Rendering never changes the workflow or moves focus.
function mountPanel() {
  const panel = document.createElement("details");
  panel.style.cssText = "position:fixed;right:16px;bottom:80px;z-index:10000;background:#20242a;color:#fff;padding:12px;width:320px;border:1px solid #888;border-radius:8px;font:14px sans-serif";
  const title = document.createElement("summary");
  title.textContent = "Demonstration: preview a sampling-steps edit";
  panel.append(title);
  const taskContext = document.createElement("p");
  taskContext.textContent = "This ComfyUI demonstration lets you review a proposed change to a node’s sampling steps. Apply change updates the displayed target in this workflow.";
  const headline = document.createElement("strong");
  headline.style.cssText = "display:block;font-size:16px;margin:8px 0 4px";
  const state = document.createElement("p");
  state.style.cssText = "margin:0 0 8px";
  panel.append(taskContext, headline, state);
  const target = document.createElement("select");
  target.setAttribute("aria-label", "Node parameter");
  const value = document.createElement("input");
  value.type = "number";
  value.setAttribute("aria-label", "Proposed value");
  const output = document.createElement("pre");
  output.setAttribute("role", "status");
  output.style.cssText = "white-space:pre-wrap;max-height:240px;overflow:auto";
  let targets = [], proposal, operation, bridgeSession, bridgeCredential, bridgeStop = false;
  const bridgeContext = () => {
    const active = app.workflowManager?.activeWorkflow;
    const rawTitle = [active?.filename, active?.name, active?.path]
      .find(value => typeof value === "string" && value.trim()) ?? "Untitled workflow";
    let title = "";
    for (const character of rawTitle) {
      if (new TextEncoder().encode(title + character).length > 256) break;
      title += character;
    }
    return { ...demo.context(), title };
  };
  const technical = document.createElement("details");
  const technicalTitle = document.createElement("summary");
  technicalTitle.textContent = "Technical details";
  technical.append(technicalTitle, output);
  const show = result => {
    const description = describePresentation(result);
    headline.textContent = description.headline;
    state.textContent = description.state;
    output.textContent = JSON.stringify(result, null, 2);
  };
  const button = (label, action) => {
    const element = document.createElement("button");
    element.textContent = label;
    element.style.cssText = "margin:6px 4px 0 0;padding:6px";
    element.onclick = () => {
      try {
        const pending = action();
        if (pending?.catch) pending.catch(error => show({ outcome: "error", message: String(error) }));
      } catch (error) { show({ outcome: "error", message: String(error) }); }
    };
    panel.append(element);
    return element;
  };
  panel.append(target, value);
  const refresh = (announce = true) => {
    targets = [];
    target.replaceChildren();
    for (const node of app.graph?._nodes ?? []) {
      for (const widget of node.widgets ?? []) {
        if (widget.type !== "number" || !Number.isFinite(widget.value)) continue;
        const option = document.createElement("option");
        option.textContent = `${node.id}: ${node.title} / ${widget.name}`;
        option.value = String(targets.length);
        targets.push({ node: node.id, name: widget.name, value: widget.value });
        target.append(option);
      }
    }
    value.value = String(targets[0]?.value ?? 0);
    proposal = undefined;
    if (announce) show({ outcome: "choose_parameter", parameters: targets.length });
  };
  target.onchange = () => { value.value = String(targets[Number(target.value)]?.value ?? 0); proposal = undefined; };
  value.oninput = () => { proposal = undefined; };
  button("Refresh parameters", refresh);
  button("Preview change", () => {
    const selected = targets[Number(target.value)];
    proposal = selected ? demo.preview(selected.node, selected.name, value.valueAsNumber) : { outcome: "unsupported" };
    show(proposal);
  });
  button("Reveal node", () => show(demo.reveal(proposal?.token)));
  button("Apply proposed change", () => {
    const result = demo.apply(proposal?.token);
    if (result.operation) operation = result.operation;
    proposal = undefined;
    show(result);
  });
  button("Check observed result", () => show(demo.outcome(operation)));
  const bridgeToken = document.createElement("input");
  bridgeToken.type = "password";
  bridgeToken.autocomplete = "off";
  bridgeToken.placeholder = "Per-run bridge token";
  bridgeToken.setAttribute("aria-label", "Per-run bridge token");
  const bridgeTokenFile = document.createElement("input");
  bridgeTokenFile.type = "file";
  bridgeTokenFile.accept = "text/plain";
  bridgeTokenFile.setAttribute("aria-label", "Per-run bridge credential file");
  panel.append(bridgeToken, bridgeTokenFile);
  const bridgeFetch = async (path, token, body) => {
    const response = await fetch(`/oa/presentation/v1/${path}`, {
      method: "POST",
      headers: { "Authorization": `Bearer ${token}`, "Content-Type": "application/json" },
      body: JSON.stringify(body ?? {}),
    });
    if (!response.ok) throw new Error(`bridge ${path} refused (${response.status})`);
    return response.json();
  };
  const pollBridge = async (token, session) => {
    while (!bridgeStop && bridgeSession === session) {
      const { command } = await bridgeFetch("browser/next", token, {
        session, context: bridgeContext(),
      });
      if (!command) continue;
      let result;
      try {
        if (command.kind === "context") {
          result = bridgeContext();
        } else if (command.kind === "preview") {
          const args = command.arguments ?? {};
          proposal = demo.preview(args.node_id, args.widget, args.value, {
            instance: args.instance, context: args.context, revision: args.revision,
          });
          if (proposal.outcome === "proposed") {
            const index = targets.findIndex(item => item.node === proposal.node && item.name === proposal.widget);
            if (index >= 0) {
              target.value = String(index);
              value.value = String(proposal.after);
            }
          }
          result = proposal;
        } else if (command.kind === "read") {
          result = demo.outcome(command.arguments?.operation);
        } else {
          result = { outcome: "unsupported" };
        }
      } catch (error) {
        result = { outcome: "error", message: String(error) };
      }
      show(result);
      await bridgeFetch("browser/result", token, { session, id: command.id, result });
    }
  };
  button("Connect local MCP fixture", async () => {
    if (bridgeSession) throw new Error("bridge already connected in this browser");
    const token = bridgeTokenFile.files?.[0]
      ? (await bridgeTokenFile.files[0].text()).trim()
      : bridgeToken.value;
    if (token.length < 32) throw new Error("enter the per-run bridge token");
    const registered = await bridgeFetch("browser/register", token, {
      context: bridgeContext(),
    });
    bridgeToken.value = "";
    bridgeTokenFile.value = "";
    bridgeSession = registered.session;
    bridgeCredential = token;
    bridgeStop = false;
    show({ outcome: "bridge_connected" });
    pollBridge(token, bridgeSession).catch(async error => {
      if (bridgeStop) return;
      const failedSession = bridgeSession;
      const failedCredential = bridgeCredential;
      try {
        await bridgeFetch("browser/unregister", failedCredential, { session: failedSession });
      } catch { /* The lease remains the fallback for a lost connection. */ }
      bridgeCredential = undefined;
      bridgeSession = undefined;
      show({ outcome: "bridge_disconnected", message: String(error) });
    });
  });
  button("Disconnect local MCP fixture", async () => {
    if (!bridgeSession) return show({ outcome: "bridge_disconnected" });
    bridgeStop = true;
    const session = bridgeSession;
    bridgeSession = undefined;
    const token = bridgeCredential;
    bridgeCredential = undefined;
    await bridgeFetch("browser/unregister", token, { session });
    show({ outcome: "bridge_disconnected" });
  });
  panel.append(technical);
  document.body.append(panel);
  refresh();
  return () => {
    refresh(false);
    proposal = undefined;
    show(operation ? demo.outcome(operation) : { outcome: "context_changed", ...demo.context() });
    if (bridgeSession) {
      bridgeFetch("browser/context", bridgeCredential, {
        session: bridgeSession, context: bridgeContext(),
      }).catch(error => show({ outcome: "bridge_disconnected", message: String(error) }));
    }
  };
}
