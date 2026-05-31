import { useEffect, useRef } from "react";
import type { Item } from "../lib/useController";
import { AssistantMessage, UserMessage } from "./Message";
import { ToolCard } from "./ToolCard";
import { Welcome } from "./Welcome";

type ToolItem = Extract<Item, { kind: "tool" }>;

export function Transcript({
  items,
  onPrompt,
  onRewind,
}: {
  items: Item[];
  onPrompt: (text: string) => void;
  onRewind?: (turn: number, scope: string) => void;
}) {
  const scrollRef = useRef<HTMLDivElement>(null);
  // stick tracks whether the view is pinned to the bottom; once the user scrolls
  // up to read, we stop yanking them back down.
  const stick = useRef(true);

  const onScroll = () => {
    const el = scrollRef.current;
    if (el) stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
  };

  // Follow new content by setting scrollTop directly (no scrollIntoView fighting
  // the browser's scroll anchoring), and inside rAF so layout has settled first —
  // together with plain-text streaming this keeps the view from jittering.
  useEffect(() => {
    if (!stick.current) return;
    const el = scrollRef.current;
    if (!el) return;
    const id = requestAnimationFrame(() => {
      el.scrollTop = el.scrollHeight;
    });
    return () => cancelAnimationFrame(id);
  }, [items]);

  // Sub-agent calls carry a parentId; collect them under their parent `task`
  // call so the parent card can render them nested, and skip them at top level.
  const subcallsByParent = new Map<string, ToolItem[]>();
  for (const it of items) {
    if (it.kind === "tool" && it.parentId) {
      const arr = subcallsByParent.get(it.parentId) ?? [];
      arr.push(it);
      subcallsByParent.set(it.parentId, arr);
    }
  }

  // Each user message's turn = its ordinal among user messages, so a rewind
  // targets the matching checkpoint.
  const userTurn = new Map<string, number>();
  let nt = 0;
  for (const it of items) {
    if (it.kind === "user") userTurn.set(it.id, nt++);
  }

  return (
    <div className="transcript" ref={scrollRef} onScroll={onScroll}>
      {items.length === 0 && <Welcome onPrompt={onPrompt} />}

      {items.map((it) => {
        switch (it.kind) {
          case "user":
            return (
              <UserMessage
                key={it.id}
                text={it.text}
                turn={userTurn.get(it.id)}
                onRewind={onRewind}
              />
            );
          case "assistant":
            return <AssistantMessage key={it.id} item={it} />;
          case "tool":
            if (it.parentId) return null; // rendered nested under its parent
            if (it.name === "todo_write") return null; // shown live in the pinned TodoPanel
            if (it.name === "exit_plan_mode") return null; // the plan was shown in the approval card
            return <ToolCard key={it.id} item={it} subcalls={subcallsByParent.get(it.id)} />;
          case "phase":
            return (
              <div key={it.id} className="phase">
                {it.text}
              </div>
            );
          case "notice":
            return (
              <div key={it.id} className={`notice notice--${it.level}`}>
                {it.text}
              </div>
            );
        }
      })}
    </div>
  );
}
