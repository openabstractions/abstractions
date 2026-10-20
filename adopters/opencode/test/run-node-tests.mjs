import { spawnSync } from "node:child_process";
import { readdirSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../", import.meta.url));
const testName = /\.test\.(?:cjs|js|mjs|cts|ts|mts)$/;

function testFiles(directory, recurse = false) {
  const files = [];
  for (const entry of readdirSync(join(root, directory), { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isFile() && testName.test(entry.name)) {
      files.push(path);
    } else if (recurse && entry.isDirectory() && entry.name !== "fixtures") {
      files.push(...testFiles(path, true));
    }
  }
  return files;
}

// Explicit files keep Node from executing upstream source snapshots in test/fixtures.
// Keep npm's local-package links visible while resolving their sibling dependencies.
const files = [...testFiles("."), ...testFiles("test", true)].sort();
if (files.length === 0) throw new Error("No OpenCode Node test files found");
const result = spawnSync(process.execPath, [
  "--preserve-symlinks", "--import", "./runtime-isolation.mjs", "--test", ...process.argv.slice(2), ...files,
], { cwd: root, stdio: "inherit" });
if (result.error) throw result.error;
process.exitCode = result.status ?? 1;
