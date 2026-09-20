// App-local D1 experiment. Transport, OA authorization and MCP hosting are
// separate integrations. Only a user's explicit Apply gesture calls apply().
export function describePresentation(result = {}) {
  const parameter = result.widget ? result.widget[0].toUpperCase() + result.widget.slice(1) : "Parameter";
  if (result.outcome === "proposed") return {
    headline: `Change ${result.title ?? "node"} node ${result.node}’s ${result.widget} from ${result.before} to ${result.after}?`,
    state: "Preview — nothing changed yet",
  };
  if (result.outcome === "applied") return result.evidence === "historical" ? {
    headline: `Previous result: ${parameter} changed to ${result.actual}`,
    state: "Historical — the target or workflow changed",
  } : {
    headline: `${parameter} changed to ${result.actual}`,
    state: "Applied — actual value observed",
  };
  if (result.outcome === "uncertain") return {
    headline: "The edit may have changed the workflow",
    state: "Result uncertain — inspect the parameter before making another edit",
  };
  if (result.outcome === "unknown") return {
    headline: "The result is unavailable",
    state: "Inspect the workflow; this does not establish whether the edit happened",
  };
  if (result.outcome === "stale") return { headline: "Preview expired", state: "Nothing changed — refresh the workflow context" };
  if (result.outcome === "revealed") return { headline: `Revealed node ${result.node}`, state: "Reveal — no value changed" };
  if (result.outcome === "context_changed") return { headline: "Workflow context changed", state: "Choose a parameter in the current workflow" };
  return { headline: "Choose a parameter", state: "Preview and Apply are separate" };
}

export function presentation(app, { now = () => Date.now(), id = () => crypto.randomUUID() } = {}) {
  const pending = new Map();
  const completed = new Map();
  const instance = id();
  let context = id();

  const failure = (outcome) => ({ outcome });
  const bounded = (value) => {
    const serialized = JSON.stringify(value);
    if (serialized === undefined || new TextEncoder().encode(serialized).length > 16384)
      throw new Error("unsupported: target exceeds 16384-byte bound");
    return serialized;
  };
  function target(nodeID, widgetName) {
    const graph = app.graph;
    const node = graph?.getNodeById(nodeID);
    const widgets = node?.widgets?.filter((w) => w.name === widgetName) ?? [];
    if (!node || widgets.length !== 1) return null;
    const widget = widgets[0];
    // Numeric scalar edits give this experiment an explicit, narrow effect.
    if (widget.type !== "number" || !Number.isFinite(widget.value)) return null;
    const snapshot = bounded({ context, graphVersion: graph._version, node: node.id,
      type: node.type, title: node.title, widget: widget.name, value: widget.value,
      options: { min: widget.options?.min, max: widget.options?.max } });
    return { graph, node, widget, snapshot };
  }
  function current(record) {
    const found = target(record.node.id, record.widget.name);
    return found && found.graph === record.graph && found.node === record.node &&
      found.widget === record.widget && found.snapshot === record.snapshot;
  }
  function expire() {
    for (const [token, p] of pending) if (now() >= p.expires) pending.delete(token);
  }
  function outcome(operation) {
    const record = completed.get(operation);
    if (!record) return failure("unknown");
    let live = false;
    try { live = current(record); } catch { /* Historical evidence survives. */ }
    return { ...record.result, evidence: live ? "current" : "historical" };
  }
  const binding = () => ({ instance, context, revision: String(app.graph?._version ?? "unknown") });
  return {
    context: binding,
    // ComfyUI calls this after configuring a workflow, including reuse of the
    // same graph object. Every old target loses its epoch.
    reset() { context = id(); pending.clear(); },
    preview(nodeID, widgetName, value, expected) {
      expire();
      const bound = binding();
      if (expected && (expected.instance !== bound.instance ||
          expected.context !== bound.context || expected.revision !== bound.revision))
        return { outcome: "stale", ...bound };
      if (pending.size >= 32) return failure("capacity");
      const found = target(nodeID, widgetName);
      if (!found || !Number.isFinite(value)) return failure("unsupported");
      const { min = -Infinity, max = Infinity } = found.widget.options ?? {};
      if (value < min || value > max) return failure("invalid");
      const token = id();
      const result = { outcome: "proposed", token, ...bound, node: found.node.id,
        title: found.node.title, widget: found.widget.name, before: found.widget.value, after: value };
      bounded(result);
      pending.set(token, { ...found, value, expires: now() + 60000, result });
      return result;
    },
    reveal(token) {
      expire();
      const p = pending.get(token);
      if (!p) return failure("stale");
      if (!current(p)) return failure("stale");
      if (typeof app.canvas?.centerOnNode !== "function") return failure("unsupported");
      // Explicit navigation; preserves node selection and keyboard focus.
      app.canvas.centerOnNode(p.node);
      return { outcome: "revealed", node: p.node.id };
    },
    apply(token) {
      expire();
      const p = pending.get(token);
      if (!p) return failure("stale");
      pending.delete(token); // A consumed proposal is never replayed.
      if (!current(p)) return failure("stale");
      const operation = id();
      let result;
      try {
        p.graph.beforeChange?.();
        p.widget.value = p.value;
        p.widget.callback?.(p.value, app.canvas, p.node);
        p.graph.afterChange?.();
        p.node.setDirtyCanvas?.(true, true);
        const readback = target(p.node.id, p.widget.name);
        if (!readback || readback.node !== p.node || readback.widget !== p.widget ||
            readback.widget.value !== p.value) throw new Error("readback differs");
        Object.assign(p, readback);
        result = { ...p.result, ...binding(), token: undefined, operation, outcome: "applied",
          actual: readback.widget.value };
      } catch {
        // A callback may have changed state before failing. Report uncertainty;
        // restoring one scalar cannot undo arbitrary application callbacks.
        result = { ...p.result, ...binding(), token: undefined, operation, outcome: "uncertain" };
      }
      if (completed.size >= 64) completed.delete(completed.keys().next().value);
      completed.set(operation, { ...p, result });
      return outcome(operation);
    },
    outcome,
  };
}
