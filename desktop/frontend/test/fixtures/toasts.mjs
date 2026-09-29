// The window's notices as the checks read them: the toast stack, and the
// attention strip under the title bar.
//
// There used to be one #banner, and each check read "what the banner says".
// Toasts stack, so the same question is now "what does the newest toast say":
// the stack is oldest first, newest at the bottom, and a write's answer is the
// toast it added last. What a toast says is its title and its optional second
// line — not the ✕ on its close button or the labels on its action buttons,
// which are chrome rather than the message.

function walk(node, visit) {
  if (!node || typeof node !== "object") return;
  visit(node);
  for (const child of node.children ?? []) walk(child, visit);
}

function hasClass(node, name) {
  return String(node?.className ?? "").split(/\s+/).includes(name);
}

// textOf joins the text of every descendant carrying one of these classes, in
// document order.
function textOf(node, classes) {
  const parts = [];
  walk(node, (child) => {
    if (child !== node && classes.some((name) => hasClass(child, name))) parts.push(child.textContent);
  });
  return parts.join(" ");
}

export function toastNodes(document) {
  return [...(document.getElementById("toasts").children ?? [])];
}

export function toastMessage(node) {
  return node ? textOf(node, ["toasttitle", "toastbody"]) : "";
}

// The kind a toast was drawn as: ok, info, warn or error.
export function toastKind(node) {
  return node ? String(node.className).split(/\s+/).find((name) => name !== "toast") ?? "" : "";
}

// latestToast is the newest toast, or a stand-in with nothing in it when the
// stack is empty. textContent and className read like the banner's did, so a
// check that asked "does the banner say X" and "is it drawn as a success"
// asks the same two things of the toast.
export function latestToast(document) {
  const nodes = toastNodes(document);
  const node = nodes[nodes.length - 1] ?? null;
  return {
    node,
    shown: node !== null,
    textContent: toastMessage(node),
    className: node ? String(node.className) : "",
    kind: toastKind(node),
  };
}

// The buttons on a toast: its actions first, then the close button.
export function toastButtons(node) {
  const buttons = [];
  walk(node, (child) => {
    if (child !== node && child.tagName === "button") buttons.push(child);
  });
  return buttons;
}

// attentionRows is the strip's rows as data: severity, title, body, and the
// two buttons, so a check can press them.
export function attentionRows(document) {
  const strip = document.getElementById("attention");
  if (hasClass(strip, "hidden")) return [];
  return [...(strip.children ?? [])].map((row) => {
    const find = (name) => {
      let found = null;
      walk(row, (child) => {
        if (!found && child !== row && hasClass(child, name)) found = child;
      });
      return found;
    };
    return {
      row,
      sev: String(row.className).split(/\s+/).find((name) => name !== "attnrow") ?? "",
      title: find("attntitle")?.textContent ?? "",
      body: find("attnbody")?.textContent ?? "",
      action: find("attnaction"),
      later: find("attnlater"),
    };
  });
}

export function attentionText(document) {
  return attentionRows(document).map((row) => `${row.title} ${row.body}`).join("\n");
}
