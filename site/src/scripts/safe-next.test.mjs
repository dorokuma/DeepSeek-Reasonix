import { test } from "node:test";
import assert from "node:assert/strict";
import { safeNext } from "./safe-next.js";

const ORIGIN = "https://reasonix.io";

test("same-origin path passes through", () => {
  assert.equal(safeNext("/account/settings?x=1#y", ORIGIN), "/account/settings?x=1#y");
});

test("allowed reasonix.io subdomain passes through", () => {
  assert.equal(safeNext("https://crash.reasonix.io/x", ORIGIN), "https://crash.reasonix.io/x");
});

test("empty/missing next", () => {
  assert.equal(safeNext("", ORIGIN), null);
  assert.equal(safeNext(null, ORIGIN), null);
});

test("rejects a plain protocol-relative redirect", () => {
  assert.equal(safeNext("//evil.example", ORIGIN), null);
});

test("rejects a backslash-prefixed redirect", () => {
  assert.equal(safeNext("/\\evil.example", ORIGIN), null);
  assert.equal(safeNext("/\\/evil.example", ORIGIN), null);
});

test("rejects control characters (as decoded by URLSearchParams) smuggling a protocol-relative redirect", () => {
  // URLSearchParams.get() decodes %09/%0A/%0D before this function ever sees
  // the value, e.g. "?next=/%09/evil.example" arrives as "/\t/evil.example".
  for (const c of ["\t", "\n", "\r"]) {
    assert.equal(safeNext(`/${c}/evil.example`, ORIGIN), null, JSON.stringify(c));
  }
});

test("rejects an unrelated https host and non-https schemes", () => {
  assert.equal(safeNext("https://evil.example/", ORIGIN), null);
  assert.equal(safeNext("javascript:alert(1)", ORIGIN), null);
});

test("rejects a reasonix.io-lookalike host", () => {
  assert.equal(safeNext("https://reasonix.io.evil.example/", ORIGIN), null);
});
