import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const source = () => readFile(new URL("../pages/skills.astro", import.meta.url), "utf8");

test("the registry publish CTA does not inherit the publish panel layout", async () => {
  const page = await source();

  assert.match(page, /class="btn btn-dark reg-publish-cta" id="publish-open"/);
  assert.match(page, /<section class="reg-publish" id="publish" hidden>/);
  assert.doesNotMatch(page, /class="btn btn-dark reg-publish" id="publish-open"/);
});

test("the registry exposes plugins as a first-class package kind", async () => {
  const page = await source();

  assert.match(page, /<button data-kind="plugin" role="tab">Plugins<\/button>/);
  assert.match(page, /<option value="plugin">plugin<\/option>/);
  assert.match(page, /installKind: f\.get\('kind'\)/);
  assert.match(page, /Plugins must point to a GitHub repository or path containing/);
  assert.match(page, /reasonix-plugin\.json/);
  assert.match(page, /\.codex-plugin\/plugin\.json/);
  assert.match(page, /\.claude-plugin\/plugin\.json/);
  assert.match(page, /\.claude-plugin\/marketplace\.json/);
  assert.match(page, /pattern="\(https\?:\/\/\[\^ \]\+\|git:github\[\.\]com\/\[\^ \]\+/);
});

test("registry copy requests preserve the reviewed package kind", async () => {
  const page = await source();

  assert.match(page, /Install this Reasonix \$\{p\.kind\} package from \$\{p\.source\}/);
  assert.match(page, /Use install_source with kind="\$\{p\.kind\}"/);
  assert.match(page, /data-copy="\$\{esc\(installRequest\(p\)\)\}"/);
  assert.doesNotMatch(page, /data-copy="\$\{esc\(p\.source\)\}"/);
});
