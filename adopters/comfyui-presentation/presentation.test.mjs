import test from "node:test";
import assert from "node:assert/strict";
import { describePresentation, presentation } from "./web/presentation.mjs";

function fixture() {
  let serial = 0, clock = 0, edits = 0, revealed = 0;
  const widget = { name: "steps", type: "number", value: 20, options: { min: 1, max: 100 } };
  const node = { id: 7, title: "Sampler", type: "KSampler", widgets: [widget] };
  const graph = { _version: 1, getNodeById: (id) => id === 7 ? node : null,
    afterChange() { this._version++; edits++; } };
  const app = { graph, canvas: { centerOnNode() { revealed++; } } };
  const demo = presentation(app, { now: () => clock, id: () => String(++serial) });
  return { demo, app, graph, node, widget, tick() { clock += 60000; },
    edits: () => edits, revealed: () => revealed };
}

test("proposal and reveal preserve values; apply records actual current result", () => {
  const f = fixture();
  const p = f.demo.preview(7, "steps", 24);
  assert.equal(p.outcome, "proposed");
  assert.equal(p.revision, "1");
  assert.equal(f.widget.value, 20);
  assert.equal(f.demo.reveal(p.token).outcome, "revealed");
  assert.equal(f.revealed(), 1);
  assert.equal(f.edits(), 0);
  const r = f.demo.apply(p.token);
  assert.equal(r.outcome, "applied");
  assert.equal(r.actual, 24);
  assert.equal(r.evidence, "current");
  assert.deepEqual({ instance: r.instance, context: r.context, revision: r.revision },
    f.demo.context());
  assert.equal(r.revision, "2");
  assert.equal(f.demo.apply(p.token).outcome, "stale");
  assert.equal(f.edits(), 1);
  f.widget.value = 25;
  assert.equal(f.demo.outcome(r.operation).evidence, "historical");
});

test("concurrent edit, reopened context and same-ID replacement refuse stale action", () => {
  for (const change of [f => f.widget.value++, f => f.graph._version++,
    f => f.demo.reset(), f => f.app.graph = { ...f.graph },
    f => f.graph.getNodeById = () => ({ ...f.node }),
    f => f.node.widgets = [{ ...f.widget }]]) {
    const f = fixture(), p = f.demo.preview(7, "steps", 24);
    change(f);
    assert.equal(f.demo.apply(p.token).outcome, "stale");
    assert.equal(f.edits(), 0);
  }
});

test("target and value validation never selects by duplicate title or widget name", () => {
  const f = fixture();
  assert.equal(f.demo.preview(8, "steps", 24).outcome, "unsupported");
  assert.equal(f.demo.preview(7, "steps", 101).outcome, "invalid");
  assert.equal(f.demo.preview(7, "steps", NaN).outcome, "unsupported");
  f.node.widgets.push({ ...f.widget });
  assert.equal(f.demo.preview(7, "steps", 24).outcome, "unsupported");
});

test("callback failure or altered readback retains uncertainty without replay", () => {
  for (const callback of [() => { throw Error("callback"); }, function() { this.value = 30; }]) {
    const f = fixture(); f.widget.callback = callback;
    const p = f.demo.preview(7, "steps", 24);
    assert.equal(f.demo.apply(p.token).outcome, "uncertain");
    assert.equal(f.demo.apply(p.token).outcome, "stale");
  }
});

test("pending proposals expire and stay bounded", () => {
  const f = fixture();
  for (let i = 0; i < 32; i++) assert.equal(f.demo.preview(7, "steps", 24).outcome, "proposed");
  assert.equal(f.demo.preview(7, "steps", 24).outcome, "capacity");
  f.tick();
  assert.equal(f.demo.preview(7, "steps", 24).outcome, "proposed");
});

test("expected instance context and revision prevent same-ID retargeting", () => {
  const f = fixture();
  const expected = f.demo.context();
  f.demo.reset();
  // The reopened graph deliberately reuses node 7 and widget "steps".
  const result = f.demo.preview(7, "steps", 24, expected);
  assert.equal(result.outcome, "stale");
  assert.notEqual(result.context, expected.context);
  assert.equal(f.widget.value, 20);
  assert.equal(f.edits(), 0);
});

test("workflow reset makes a completed result historical", () => {
  const f = fixture();
  const proposal = f.demo.preview(7, "steps", 24);
  const result = f.demo.apply(proposal.token);
  assert.equal(result.evidence, "current");
  assert.equal(result.revision, "2");
  f.demo.reset();
  const historical = f.demo.outcome(result.operation);
  assert.equal(historical.evidence, "historical");
  assert.notEqual(historical.context, f.demo.context().context);
});

test("uncertain callback result carries the binding after its attempted change", () => {
  const f = fixture();
  f.graph.afterChange = function() { this._version++; throw Error("afterChange"); };
  const proposal = f.demo.preview(7, "steps", 24);
  const result = f.demo.apply(proposal.token);
  assert.equal(result.outcome, "uncertain");
  assert.equal(result.revision, "2");
  assert.deepEqual({ instance: result.instance, context: result.context, revision: result.revision },
    f.demo.context());
});

test("plain presentation labels distinguish preview current and historical result", () => {
  assert.deepEqual(describePresentation({ outcome: "proposed", title: "KSampler", node: 1,
    widget: "steps", before: 20, after: 24 }), {
    headline: "Change KSampler node 1’s steps from 20 to 24?",
    state: "Preview — nothing changed yet",
  });
  assert.equal(describePresentation({ outcome: "applied", widget: "steps", actual: 24,
    evidence: "current" }).headline, "Steps changed to 24");
  assert.equal(describePresentation({ outcome: "applied", widget: "steps", actual: 24,
    evidence: "historical" }).state, "Historical — the target or workflow changed");
});

test("callback uncertainty remains visible with technical details collapsed", () => {
  const f = fixture();
  f.widget.callback = () => { throw Error("failed after mutation"); };
  const proposal = f.demo.preview(7, "steps", 24);
  const result = f.demo.apply(proposal.token);
  assert.equal(f.widget.value, 24);
  assert.equal(result.outcome, "uncertain");
  assert.match(describePresentation(result).state, /Result uncertain/);
  assert.match(describePresentation(result).headline, /may have changed/);
});
