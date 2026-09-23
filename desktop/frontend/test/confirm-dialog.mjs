// The window's own confirmation dialog, and the one action the owner reported
// as doing nothing: 清空收件匣.
//
// In the shipped macOS app window.confirm never shows. Wails v2 is the
// WKWebView's WKUIDelegate and implements only runOpenPanelWithParameters from
// it, and WebKit answers a confirm() with no runJavaScriptConfirmPanel delegate
// as if Cancel had been pressed. Every confirm() in app.js therefore returned
// false and every action behind one silently stopped. So: app.js calls none of
// window.confirm / alert / prompt, and the questions it does ask go through
// askConfirm, whose answer comes from its own buttons, Esc or the backdrop.
//
//   node frontend/test/confirm-dialog.mjs

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { document } from "./dom-shim.mjs";
import { answerConfirms } from "./fixtures/confirm-dialog.mjs";

globalThis.document = document;
globalThis.setInterval = () => 0;

const failures = [];
const el = (id) => document.getElementById(id);
const settle = () => new Promise((resolve) => setTimeout(resolve, 10));
const here = path.dirname(fileURLToPath(import.meta.url));

// 1. No browser dialog anywhere in the window's code. Comments are dropped
//    first so the explanation of why is not itself a match.
const source = fs.readFileSync(path.join(here, "..", "src", "app.js"), "utf8")
  .replace(/\/\*[\s\S]*?\*\//g, "")
  .split("\n").map((line) => line.replace(/(^|[^:"'`])\/\/.*$/, "$1")).join("\n");
for (const name of ["confirm", "alert", "prompt"]) {
  const call = new RegExp(`(^|[^.\\w])(window\\.|globalThis\\.)?${name}\\s*\\(`, "m");
  const found = call.exec(source);
  if (found) failures.push(`app.js still calls ${name}(): …${source.slice(found.index, found.index + 60)}…`);
}

// 2. The dialog's markup is there, and its body keeps line breaks.
const markup = fs.readFileSync(path.join(here, "..", "index.html"), "utf8");
for (const id of ["confirm-modal", "confirm-title", "confirm-body", "confirm-ok", "confirm-cancel"]) {
  if (!markup.includes(`id="${id}"`)) failures.push(`index.html has no #${id}`);
}
const css = fs.readFileSync(path.join(here, "..", "src", "style.css"), "utf8");
if (!/#confirm-body \{[^}]*white-space: pre-line/.test(css)) {
  failures.push("#confirm-body does not keep line breaks, so a two-paragraph question reads as one");
}

let answer = true;
let asked = [];
answerConfirms(document, (question) => {
  asked.push(question);
  return answer;
});

let clearCalls = [];
let clearResult = { removed: 4 };
const { configure, boot } = await import("../src/app.js");
const noop = async () => ({});
configure({
  Overview: async () => ({ reachable: true, node: {}, sessions: [], nodes: [], peers: [], counts: {} }),
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop,
  Pairing: async () => ({ availability: "unknown", candidates: [] }), OpenPairing: noop, ClosePairing: noop,
  CopyText: noop, MCPConfig: noop, Outbound: noop, Wakes: noop,
  Inbox: async (sessionId) => ({ sessionId, messages: [], held: 0, capacity: 500 }),
  ClearInbox: async (sessionId) => { clearCalls.push(sessionId); return clearResult; },
});
const app = boot({ start: false });

// 3. 清空收件匣, answered 取消: nothing is cleared, and the dialog closes.
const SESSION = "claude:held";
await app.openInbox(SESSION);
answer = false;
await el("inbox-clear").onclick();
await settle();
if (asked.length !== 1) failures.push(`清空收件匣 asked ${asked.length} questions, want 1`);
else if (!asked[0].includes(SESSION)) failures.push(`the question does not name the session: ${asked[0]}`);
if (clearCalls.length !== 0) failures.push(`a cancelled clear called ClearInbox ${clearCalls.length} times`);
if (!el("confirm-modal").classList.contains("hidden")) failures.push("the dialog stayed open after 取消");

// 4. Answered 確定: ClearInbox runs for that session and its answer is drawn in
//    the drawer, which is where the owner is looking.
answer = true;
asked = [];
await el("inbox-clear").onclick();
await settle();
if (clearCalls.length !== 1 || clearCalls[0] !== SESSION) {
  failures.push(`a confirmed clear called ClearInbox with ${JSON.stringify(clearCalls)}, want ["${SESSION}"]`);
}
if (!el("inbox-body").serialize().includes("移除 4 則")) {
  failures.push(`the clear result is not in the drawer: ${el("inbox-body").serialize()}`);
}

// 5. askConfirm itself, with the fixture's automatic answer taken off the
//    dialog so each check presses its own button, Esc or the backdrop.
{
  const modal = el("confirm-modal");
  let className = "modal confirm hidden";
  Object.defineProperty(modal, "className", { configurable: true, get: () => className, set: (v) => { className = String(v); } });
}
const ask = async (press, options) => {
  const pending = app.askConfirm(options);
  press();
  return pending;
};
const two = { title: "要清空嗎？", body: "第一段。\n\n第二段。", confirmLabel: "清空", danger: true };
if (await ask(() => el("confirm-ok").onclick(), two) !== true) failures.push("確定 did not answer true");
if (await ask(() => el("confirm-cancel").onclick(), two) !== false) failures.push("取消 did not answer false");
if (await ask(() => app.confirmKey({ key: "Escape" }), two) !== false) failures.push("Esc did not answer false");
// The backdrop: a fresh press on it, begun once the question has been up for
// a moment, is "no".
{
  const modal = el("confirm-modal");
  const pending = app.askConfirm(two);
  await new Promise((resolve) => setTimeout(resolve, 450));
  modal.onpointerdown?.({ target: modal });
  modal.onclick({ target: modal, detail: 1 });
  if (await pending !== false) failures.push("a click on the backdrop did not answer false");
}
// A double-click on the button that asks: the dialog opens inside the first
// click and covers the window, so the second press and click of the same
// gesture land on the backdrop. That is not an answer, whichever way the
// browser reports it — as a second click, or as a plain one too soon.
{
  const modal = el("confirm-modal");
  const pending = app.askConfirm(two);
  modal.onpointerdown?.({ target: modal });
  modal.onclick({ target: modal, detail: 2 });
  if (modal.classList.contains("hidden")) failures.push("the second click of a double-click on the backdrop closed the question");
  modal.onpointerdown?.({ target: modal });
  modal.onclick({ target: modal, detail: 1 });
  if (modal.classList.contains("hidden")) failures.push("a backdrop press the moment the question opened closed it");
  // Nor is a press that began inside the card and was released outside it.
  await new Promise((resolve) => setTimeout(resolve, 450));
  modal.onpointerdown?.({ target: el("confirm-body") });
  modal.onclick({ target: modal, detail: 1 });
  if (modal.classList.contains("hidden")) failures.push("a press begun on the card and released on the backdrop closed the question");
  // And the second click of a double-click is still not one after the grace.
  modal.onpointerdown?.({ target: modal });
  modal.onclick({ target: modal, detail: 2 });
  if (modal.classList.contains("hidden")) failures.push("a double-click on the backdrop closed the question");
  el("confirm-cancel").onclick();
  await pending;
}
{
  const pending = app.askConfirm(two);
  el("confirm-modal").onclick({ target: el("confirm-body") });
  if (el("confirm-modal").classList.contains("hidden")) failures.push("a click inside the card closed the dialog");
  if (el("confirm-body").textContent !== two.body) failures.push("the body lost its line breaks");
  if (el("confirm-title").textContent !== two.title) failures.push("the title was not written");
  if (el("confirm-ok").textContent !== "清空") failures.push("the confirm button does not carry its label");
  if (!el("confirm-ok").classList.contains("danger")) failures.push("a dangerous confirm is not drawn as one");
  if (document.activeElement === el("confirm-ok")) failures.push("the keyboard starts on a dangerous confirm button");
  if (document.activeElement !== el("confirm-cancel")) failures.push("the keyboard does not start on 取消 for a dangerous question");
  el("confirm-cancel").onclick();
  await pending;
}
{
  const pending = app.askConfirm({ title: "重新登記？", body: "" });
  if (document.activeElement !== el("confirm-ok")) failures.push("a safe question does not start on its confirm button");
  if (!el("confirm-ok").classList.contains("primary")) failures.push("a safe confirm is not the primary button");
  if (!el("confirm-body").classList.contains("hidden")) failures.push("an empty body is shown as an empty paragraph");
  // A second question answers the first one "no" rather than leaving it
  // waiting for ever.
  const second = app.askConfirm({ title: "另一個？" });
  if (await pending !== false) failures.push("a question replaced by another did not answer false");
  el("confirm-ok").onclick();
  if (await second !== true) failures.push("the second question was not the one the button answered");
}
if (!el("confirm-modal").classList.contains("hidden")) failures.push("the dialog is still open after every question was answered");

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("confirm dialog: no window.confirm, questions answered by their own buttons, 清空收件匣 clears only on 確定");
