import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { decode, encode, Refusal } from "./rec.mjs";

const TS = "2026-09-08T05:07:14.951609Z";
const cp = (...p) => p.map((c) => String.fromCodePoint(c)).join("");

function awkward() {
  const by = "Ada L" + cp(0x016b) + "velace <ada@" + cp(0x4f8b, 0x3048) +
    ".jp> & co " + cp(0x2702, 0xfe0f) + " " + cp(0x1f9ff);
  const err = "line1" + cp(0x0a) + "line2" + cp(0x09) + "tabbed" + cp(0x01) +
    "ctrl " + cp(0x22) + "quoted" + cp(0x22) + " back" + cp(0x5c) + "slash";
  return {
    content: ["abstraction.job/base@1", "abstraction.job/intent@1",
      "abstraction.job/envelope@1", "abstraction.job/step@1"],
    critical: ["abstraction.job/base@1"],
    id: "1787202430967-a752f9a9c2c77b123ffd",
    kind: "download",
    envelope: {
      schema: "nas.example/transfer@2",
      actions: ["cancel", "nas.example/transfer@2#re-mirror"],
    },
    state: "pending",
    spec: '{"artifact":{"bytes":9223372036854775807,"empty_obj":{},"empty_arr":[]},' +
      '"note":"a<b&c>d","nested":{"deep":{"x":1.50,"neg":-0.0}}}',
    checkpoint: "",
    progress: {
      done: 0n,
      total: 9223372036854775807n,
      updated_at: TS,
      step: { name: "", ordinal: 1, of: 0, done: 0n, total: -9223372036854775808n },
    },
    lease: { owner: "", epoch: 0n, expires_at: TS, recall: null },
    delegation: null,
    requires: [],
    error: err,
    intent: { want: "cancel", by, at: TS },
    extensions: {
      "zz.example/v1": '{"k":"v"}',
      "aa.example/v1": "[1,2,3]",
      [cp(0x00e9) + ".example"]: "null",
      [cp(0xfffd) + ".example"]: "true",
      [cp(0x1d11e) + ".example"]: "{}",
    },
    created_at: TS,
    updated_at: TS,
  };
}

function ranges() {
  return {
    content: ["abstraction.job/base@1", "abstraction.download/ranges@1"],
    critical: ["abstraction.job/base@1"],
    id: "1787202430967-a752f9a9c2c77b123ffd",
    kind: "download",
    state: "running",
    spec: '{"artifact":{"bytes":23068672}}',
    checkpoint: '{"verified_prefix":4194304,"verified":' +
      "[[0,4194304],[8388608,12582912],[20971520,23068672]]}",
    progress: {
      done: 10485760n,
      total: 23068672n,
      updated_at: "2026-08-20T05:07:14.951609Z",
      step: null,
    },
    lease: {
      owner: "go-worker",
      epoch: 2n,
      expires_at: "2026-08-20T05:08:14.635068Z",
      recall: null,
    },
    delegation: null,
    requires: [],
    error: "",
    intent: null,
    extensions: {},
    created_at: "2026-08-20T05:07:10.967343Z",
    updated_at: "2026-08-20T05:07:15.134811Z",
  };
}

function terminal() {
  return {
    content: ["abstraction.job/base@1", "abstraction.job/step@1",
      "abstraction.job/terminal@1", "abstraction.job/recall@1"],
    critical: ["abstraction.job/base@1", "abstraction.job/terminal@1",
      "abstraction.job/recall@1"],
    id: "1787202430967-a752f9a9c2c77b123ffd",
    kind: "download",
    state: "failed",
    spec: '{"artifact":{"bytes":64}}',
    checkpoint: '{"verified_prefix":8}',
    progress: {
      done: 8n,
      total: 64n,
      updated_at: "2026-09-09T17:21:08.958178Z",
      step: { name: "fetch", ordinal: 1, of: 2, done: 8n, total: 64n },
    },
    lease: {
      owner: "alpha",
      epoch: 3n,
      expires_at: "2026-09-09T17:21:09.958178Z",
      recall: {
        reason: "yield",
        by: "broker",
        at: "2026-09-09T17:21:08.958178Z",
        until: "2026-09-09T17:21:09.958178Z",
      },
    },
    delegation: null,
    requires: [],
    error: "source closed the connection",
    intent: null,
    extensions: {},
    created_at: "2026-09-09T17:21:06.998457Z",
    updated_at: "2026-09-09T17:21:08.967883Z",
  };
}

function verdict(data) {
  try {
    return "ok\t" + new TextDecoder().decode(decode(data).spec);
  } catch (e) {
    if (e instanceof Refusal) return e.word + "\t" + e.offset;
    throw e;
  }
}

const dir = process.argv[2];
for (const [name, r] of [["awkward", awkward()], ["ranges", ranges()], ["terminal", terminal()]]) {
  const encoded = encode(r);
  writeFileSync(`${dir}/js-${name}.json`, encoded);
  writeFileSync(`${dir}/js-rt-${name}.json`, encode(decode(encoded)));
}

const corpus = process.argv[3];
const lines = readdirSync(corpus).filter((f) => f.endsWith(".json")).sort()
  .map((f) => `${f.slice(0, -5)}\t${verdict(readFileSync(`${corpus}/${f}`))}\n`);
writeFileSync(`${dir}/js-corpus.txt`, lines.join(""));
