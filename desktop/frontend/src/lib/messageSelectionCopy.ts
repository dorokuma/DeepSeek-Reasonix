const MESSAGE_SELECTION_SELECTOR = ".msg__body, .reasoning__body";

export interface MessageSelectionCopyState {
  text: string;
  isCollapsed: boolean;
  targetIsEditable: boolean;
  intersectsMessage: boolean;
  canWriteClipboard: boolean;
}

export function messageSelectionCopyText(state: MessageSelectionCopyState): string | null {
  if (state.isCollapsed) return null;
  if (state.targetIsEditable) return null;
  if (!state.intersectsMessage) return null;
  if (!state.canWriteClipboard) return null;
  if (state.text.trim() === "") return null;
  return state.text;
}

// Gates the transcript right-click menu the same way the copy interceptor
// gates ⌘C — a non-collapsed, non-editable selection touching a message or
// reasoning body — plus one menu-only rule: the right-click itself must land
// inside a message body. A selection outlives clicks elsewhere, and surfaces
// like the project tree and tab bar own their right-click menus; without the
// target gate this menu would open on top of theirs. Returns the text the
// menu would copy, or null when the menu should not open. canWriteClipboard
// is true because the menu writes through writeClipboardText rather than a
// ClipboardEvent's clipboardData.
export function messageSelectionContextText(doc: Document, target: EventTarget | null): string | null {
  if (!targetWithinMessage(target)) return null;
  const selection = doc.getSelection();
  return messageSelectionCopyText({
    text: selection?.toString() ?? "",
    isCollapsed: selection == null || selection.isCollapsed,
    targetIsEditable: isEditableTarget(target),
    intersectsMessage: selectionIntersectsMessage(selection, doc),
    canWriteClipboard: true,
  });
}

function targetWithinMessage(target: EventTarget | null): boolean {
  const el = elementFromEventTarget(target);
  return el?.closest(MESSAGE_SELECTION_SELECTOR) != null;
}

export function installMessageSelectionCopy(doc: Document = document): () => void {
  const onCopy = (event: ClipboardEvent) => {
    const selection = doc.getSelection();
    const text = messageSelectionCopyText({
      text: selection?.toString() ?? "",
      isCollapsed: selection == null || selection.isCollapsed,
      targetIsEditable: isEditableTarget(event.target),
      intersectsMessage: selectionIntersectsMessage(selection, doc),
      canWriteClipboard: event.clipboardData != null,
    });
    if (text == null || event.clipboardData == null) return;

    event.clipboardData.setData("text/plain", text);
    event.preventDefault();
  };

  doc.addEventListener("copy", onCopy);
  return () => doc.removeEventListener("copy", onCopy);
}

function selectionIntersectsMessage(selection: Selection | null, root: ParentNode): boolean {
  if (selection == null || selection.rangeCount === 0 || selection.isCollapsed) return false;
  for (let i = 0; i < selection.rangeCount; i += 1) {
    if (rangeIntersectsMessage(selection.getRangeAt(i), root)) return true;
  }
  return false;
}

function rangeIntersectsMessage(range: Range, root: ParentNode): boolean {
  const common = elementFromNode(range.commonAncestorContainer);
  const directMessage = common?.closest(MESSAGE_SELECTION_SELECTOR);
  if (directMessage) return true;

  const scope = common ?? root;
  const candidates = scope instanceof Element && scope.matches(MESSAGE_SELECTION_SELECTOR)
    ? [scope, ...Array.from(scope.querySelectorAll(MESSAGE_SELECTION_SELECTOR))]
    : Array.from(scope.querySelectorAll(MESSAGE_SELECTION_SELECTOR));

  return candidates.some((node) => {
    try {
      return range.intersectsNode(node);
    } catch {
      return false;
    }
  });
}

function isEditableTarget(target: EventTarget | null): boolean {
  const el = elementFromEventTarget(target);
  if (!el) return false;
  if (el.closest("input, textarea, select")) return true;
  for (let node: Element | null = el; node; node = node.parentElement) {
    if (node instanceof HTMLElement && node.isContentEditable) return true;
  }
  return false;
}

function elementFromEventTarget(target: EventTarget | null): Element | null {
  return target instanceof Element ? target : null;
}

function elementFromNode(node: Node | null): Element | null {
  if (!node) return null;
  return node.nodeType === Node.ELEMENT_NODE ? node as Element : node.parentElement;
}
