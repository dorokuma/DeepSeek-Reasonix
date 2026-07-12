// Run: tsx src/__tests__/turn-actions-rendering.test.ts

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const testDir = dirname(fileURLToPath(import.meta.url));
const styles = readFileSync(resolve(testDir, "../styles.css"), "utf8");

let passed = 0;
let failed = 0;

function ok(value: unknown, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

console.log("\nturn actions rendering");

ok(
  /\.app--creation \.transcript,[\s\S]*background-color:\s*var\(--creation-canvas-color, var\(--bg\)\);[\s\S]*scrollbar-width:\s*auto;/.test(styles),
  "creation transcript paints an opaque canvas when stale action rows are removed",
);

ok(
  styles.match(/--creation-canvas-color:\s*color-mix\(/g)?.length === 2,
  "explicit and system light themes retain a creation-specific canvas color",
);

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
