import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { decode, encode, Refusal } from "./rec.mjs";

const dir = process.argv[2];
const corpus = process.argv[3];
const lines = [];
for (const f of readdirSync(corpus).filter((n) => n.endsWith(".json")).sort()) {
  const stem = f.slice(0, -5);
  try {
    const v = decode(readFileSync(`${corpus}/${f}`));
    lines.push(`${stem}\tok\n`);
    writeFileSync(`${dir}/js-rt-${stem}.json`, encode(v));
  } catch (e) {
    if (!(e instanceof Refusal)) throw e;
    lines.push(`${stem}\t${e.word}\t${e.offset}\n`);
  }
}
writeFileSync(`${dir}/js-corpus.txt`, lines.join(""));
