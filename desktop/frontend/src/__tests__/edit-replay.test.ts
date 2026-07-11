// Run: tsx src/__tests__/edit-replay.test.ts

import { replaySubmitText } from "../lib/editReplay";
import { invocationSegmentsFromMessage, replaceInvocationTextRange, serializeInvocationSubmit, type ComposerInvocation } from "../lib/invocationDisplay";

let passed = 0;
let failed = 0;

function eq(a: unknown, b: unknown, label: string) {
  if (a === b) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}: expected ${JSON.stringify(b)}, got ${JSON.stringify(a)}\n`);
    failed += 1;
  }
}

console.log("\nedit replay");

eq(
  replaySubmitText("hidden session context\nvisible prompt", "visible prompt", "visible prompt", "visible prompt"),
  "hidden session context\nvisible prompt",
  "unchanged edits preserve the original submitted text",
);

eq(
  replaySubmitText("hidden session context\nvisible prompt @.reasonix/attachments/a.png", "visible prompt @[a.png](.reasonix/attachments/a.png)", "updated prompt @[a.png](.reasonix/attachments/a.png)", "updated prompt @.reasonix/attachments/a.png"),
  "hidden session context\nupdated prompt @.reasonix/attachments/a.png",
  "edited visible text preserves submit-only prefix and raw attachment refs",
);

eq(
  replaySubmitText(undefined, "visible prompt", "updated prompt", "updated prompt"),
  "updated prompt",
  "messages without hidden submit context use the rebuilt submit text",
);

eq(
  replaySubmitText("/reasonix-develop review this change", "review this change", "review the updated change", "review the updated change"),
  "/reasonix-develop review the updated change",
  "editing a structured skill message preserves its slash invocation",
);

const mixedInvocations: ComposerInvocation[] = [
  { id: "primary", offset: 0, command: { name: "general-purpose", description: "", kind: "subagent" } },
  { id: "skill", offset: 2, command: { name: "activity-dynamic-debug", description: "", kind: "skill" } },
  { id: "explore", offset: 3, command: { name: "explore", description: "", kind: "subagent" } },
];
const mixedSubmit = serializeInvocationSubmit("你是再做", mixedInvocations);
eq(
  mixedSubmit,
  "/general-purpose 你是 /activity-dynamic-debug 再 /explore 做",
  "multiple abilities serialize in visual order",
);
const mixedSegments = invocationSegmentsFromMessage("你是再做", mixedSubmit);
eq(
  mixedSegments.filter((segment) => segment.type === "invocation").map((segment) => segment.type === "invocation" ? segment.invocation.name : "").join(","),
  "general-purpose,activity-dynamic-debug,explore",
  "multiple abilities restore from display and submit text",
);
eq(
  mixedSegments.filter((segment) => segment.type === "text").map((segment) => segment.type === "text" ? segment.content : "").join(""),
  "你是再做",
  "restored ability segments preserve visible task text",
);

eq(
  replaySubmitText(mixedSubmit, "你是再做", "请开发并检查", "请开发并检查"),
  "/general-purpose 请开发 /activity-dynamic-debug 并检 /explore 查",
  "editing a mixed ability message preserves every invocation",
);

const boundaryInvocations: ComposerInvocation[] = [
  { id: "first", offset: 0, command: { name: "general-purpose", description: "", kind: "subagent" } },
  { id: "second", offset: 0, command: { name: "explore", description: "", kind: "subagent" } },
];
const insertedAfterFirst = replaceInvocationTextRange("", boundaryInvocations, 0, 0, "task", "first");
eq(
  insertedAfterFirst.invocations.map((invocation) => `${invocation.id}:${invocation.offset}`).join(","),
  "first:0,second:4",
  "inserting after an invocation preserves the selected entity boundary",
);
eq(
  serializeInvocationSubmit(insertedAfterFirst.text, insertedAfterFirst.invocations),
  "/general-purpose task /explore",
  "same-offset entities serialize around inserted text in visual order",
);
eq(
  invocationSegmentsFromMessage("develop x", "/my-formatter develop /explore x")
    .filter((segment) => segment.type === "invocation")
    .map((segment) => segment.type === "invocation" ? segment.invocation.name : "")
    .join(","),
  "my-formatter,explore",
  "spaced English messages restore every inline invocation",
);

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
