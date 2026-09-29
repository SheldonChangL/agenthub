// The three layers that replaced the banner (docs/ui-contract.md §3.1): toasts
// that answer a button, the log behind the bell, and the attention strip that
// says what is waiting on the owner.
//
// The banner's two faults are what these checks are about. It let a success
// vanish after four seconds and an error be replaced by the next message, so
// something the owner missed was gone for good; and the four situations that
// need the owner to act — a node that is not answering, a service that is not
// running, a full inbox, a machine asking to pair — were each on a different
// tab, where nobody on another tab would see them.
//
// The whole module runs, wiring and intervals included, with the intervals
// captured so the test decides when the fifteen-second tick fires, and the
// six-second toast timer captured so "a success goes by itself" does not have
// to wait six seconds to be checked.
//
//   node frontend/test/notifications.mjs

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { document } from "./dom-shim.mjs";
import { answerConfirms } from "./fixtures/confirm-dialog.mjs";
import { attentionRows, latestToast, toastButtons, toastKind, toastMessage, toastNodes } from "./fixtures/toasts.mjs";
import { TEXT as ZH } from "../src/i18n/zh-Hant.js";

globalThis.document = document;
answerConfirms(document, () => true);

const failures = [];
const el = (id) => document.getElementById(id);
const fill = (template, params) => template.replace(/\{(\w+)\}/g, (_, name) => String(params[name]));

const ticks = [];
globalThis.setInterval = (fn, ms) => {
  ticks.push({ fn, ms });
  return ticks.length;
};
// The toast timer, and only that one: everything else keeps the real clock.
const realSetTimeout = globalThis.setTimeout;
let toastTimers = [];
globalThis.setTimeout = (fn, ms, ...rest) => {
  if (ms === 6000) {
    const handle = { fn, fake: true, cleared: false, unref() {} };
    toastTimers.push(handle);
    return handle;
  }
  return realSetTimeout(fn, ms, ...rest);
};
// A toast's timer is cleared when it is closed and when it is held; a cleared
// one must not fire when the check runs the clock forward.
const realClearTimeout = globalThis.clearTimeout;
globalThis.clearTimeout = (handle) => {
  if (handle?.fake) handle.cleared = true;
  else realClearTimeout(handle);
};
const runToastTimers = () => {
  const due = toastTimers;
  toastTimers = [];
  for (const handle of due) if (!handle.cleared) handle.fn();
};
const settle = () => new Promise((resolve) => realSetTimeout(resolve, 20));

const session = (id, title = "") => ({
  id, provider: id.split(":")[0], status: "idle", management: "managed", cwd: "/tmp", title,
  audience: { mode: "none" }, lastSeenAt: new Date().toISOString(),
});
const reachable = () => ({
  node: { id: "node_local", displayName: "local", platform: "test", fingerprint: "AAAA" },
  sessions: [session("claude:one", "First one"), session("codex:two", "Second one")],
  nodes: [{ nodeId: "node_alice", displayName: "alice", trusted: true }],
  peers: [], counts: { total: 2 }, nodeUrl: "http://127.0.0.1:7462", reachable: true,
});
const unreachable = () => ({
  sessions: [], nodes: [], peers: [], counts: {}, nodeUrl: "http://127.0.0.1:7462",
  reachable: false, error: "dial tcp 127.0.0.1:7462: connection refused",
});

let overviewAnswer = reachable();
// The first Overview is held until the check has looked at the strip before
// any read has answered.
let releaseFirstOverview;
const firstOverview = new Promise((resolve) => { releaseFirstOverview = resolve; });
let serviceAnswer = { supported: true, installed: true, running: true, pid: 1 };
let countsAnswer = { ok: true, counts: {} };
let pairingAnswer = { availability: "on", windowAvailable: true, state: { open: false }, candidates: [] };
let pairRequestsAnswer = [];
// When set, PairRequests answers whatever this returns (a promise the check
// resolves when it wants).
let pairRequestsHook = null;
const pairRequestsCalls = [];
const inboxReads = [];
let discoverPark = null;

const { configure, boot } = await import("../src/app.js");
configure({
  Overview: async () => {
    await firstOverview;
    return overviewAnswer;
  },
  ServiceStatus: async () => serviceAnswer,
  InboxCounts: async () => countsAnswer,
  Inbox: async (sessionId) => { inboxReads.push(sessionId); return { sessionId, messages: [], held: 0, capacity: 500 }; },
  Pairing: async () => pairingAnswer,
  PairRequests: async (all) => {
    pairRequestsCalls.push(all);
    return pairRequestsHook ? pairRequestsHook(all) : pairRequestsAnswer;
  },
  OpenPairing: async () => ({ open: true }),
  ClosePairing: async () => ({ open: false }),
  NodeSettings: async () => ({ error: "not in this test" }),
  Discover: () => new Promise((resolve) => { discoverPark = resolve; }),
  SetAudience: async () => ({}), TrustNode: async () => ({}), RevokeNode: async () => ({}),
  Heartbeat: async () => "", ClearInbox: async () => ({}), CopyText: async () => ({}),
  LocalAddresses: async () => [],
});
const app = boot();
await settle();
// Nothing has answered yet: a node nobody has asked is not a node that is
// down, whatever the initial state says (state.nodeReachable starts false).
if (app.state.nodeReachable !== false) failures.push("the check meant to run before the first read ran after it");
// Anything that renders in that time — a view switch, a pairing read — paints
// the strip from the state as it is.
app.render();
if (attentionRows(document).some((entry) => entry.title === ZH["attention.nodeDown.title"])) {
  failures.push("the strip said the node is not answering before the first read answered");
}
releaseFirstOverview();
await settle();
const tick15 = ticks.find((tick) => tick.ms === 15000);
if (!tick15) failures.push(`no fifteen-second tick: ${ticks.map((tick) => tick.ms).join(", ")}`);
// The module still registers exactly the four intervals §4 allows: the
// pairing request read rides on the fifteen-second tick, not a fifth one.
if (ticks.length !== 4) failures.push(`${ticks.length} intervals registered, want the four §4 allows`);

/* ---------------- 1. toasts ---------------- */

const markup = fs.readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "index.html"), "utf8");
if (!/<div id="toasts"[^>]*aria-live="polite"/.test(markup)) failures.push("#toasts is not an aria-live=polite region");
if (/id="banner"/.test(markup)) failures.push("index.html still has the #banner the toasts replace");

const clearToasts = () => {
  for (const node of toastNodes(document)) toastButtons(node).at(-1).onclick();
};
clearToasts();
toastTimers = [];

// An error stays however long it is left; a success and an info go by
// themselves, on the six-second timer, and only then.
app.notify("error", "the write failed");
app.notify("ok", "the write worked");
app.notify("info", "for your information");
const [errorToast, okToast, infoToast] = toastNodes(document);
if (toastKind(errorToast) !== "error" || toastKind(okToast) !== "ok" || toastKind(infoToast) !== "info") {
  failures.push(`the stack is ${toastNodes(document).map(toastKind).join(",")}, want error,ok,info in the order they were said`);
}
if (latestToast(document).textContent !== "for your information") {
  failures.push(`the newest toast is not the last one said: ${latestToast(document).textContent}`);
}
if (errorToast.getAttribute("role") !== "alert") failures.push(`an error toast has role ${errorToast.getAttribute("role")}, want alert`);
if (okToast.getAttribute("role") === "alert") failures.push("a success toast interrupts a screen reader as an alert");
if (toastTimers.length !== 2) failures.push(`${toastTimers.length} toasts were given the six-second timer, want the two that are not errors`);
runToastTimers();
const afterTimers = toastNodes(document);
if (!afterTimers.includes(errorToast)) failures.push("the error toast went away by itself");
if (afterTimers.includes(okToast) || afterTimers.includes(infoToast)) failures.push("a success or info toast outlived its six seconds");

// A warning stays too.
app.notify("warn", "half done");
runToastTimers();
if (latestToast(document).kind !== "warn") failures.push("a warning toast went away by itself");

// ✕ closes one and only one.
const warnNode = latestToast(document).node;
toastButtons(warnNode).at(-1).onclick();
if (toastNodes(document).includes(warnNode)) failures.push("the close button did not close its toast");
if (!toastNodes(document).includes(errorToast)) failures.push("closing one toast took another with it");
toastButtons(errorToast).at(-1).onclick();

// Three at most, the oldest goes first — and is still in the log.
for (const n of [1, 2, 3, 4]) app.notify("error", `failure ${n}`);
const stack = toastNodes(document).map(toastMessage);
if (stack.join("|") !== "failure 2|failure 3|failure 4") failures.push(`four toasts left ${JSON.stringify(stack)}, want the newest three`);
if (!app.state.notices.some((notice) => notice.title === "failure 1")) failures.push("the toast pushed off the stack is not in the log");
clearToasts();
// But what would have gone by itself goes before what would not: an error
// or a warning is pushed off only when there is nothing else to push.
app.notify("error", "kept error");
app.notify("ok", "passing ok");
app.notify("warn", "kept warning");
app.notify("info", "passing info");
{
  const order = toastNodes(document).map(toastMessage).join("|");
  if (order !== "kept error|kept warning|passing info") failures.push(`a fourth toast pushed off the wrong one: ${order}`);
  app.notify("ok", "newest ok");
  const next = toastNodes(document).map(toastMessage).join("|");
  if (next !== "kept error|kept warning|newest ok") failures.push(`a fifth toast pushed off the wrong one: ${next}`);
}
clearToasts();
toastTimers = [];
// Behind three that stay, the new one that would go by itself is still the
// one just shown: the oldest of the three goes, not it and its 復原.
{
  app.notify("error", "stays 1");
  app.notify("warn", "stays 2");
  app.notify("error", "stays 3");
  let undid = 0;
  const record = app.notify("ok", "done, undo?", { actions: [{ label: "復原", run() { undid += 1; } }] });
  const order = toastNodes(document).map(toastMessage).join("|");
  if (order !== "stays 2|stays 3|done, undo?") failures.push(`a success behind three errors pushed off the wrong one: ${order}`);
  if (!toastNodes(document).includes(record.node)) failures.push("a success with 復原 behind three errors pushed itself off");
  else {
    toastButtons(record.node)[0].onclick();
    if (undid !== 1) failures.push("the 復原 on a success behind three errors did not run");
  }
}
clearToasts();
toastTimers = [];

// A toast that goes by itself holds while the pointer is on it or the
// keyboard is in it, and gets its whole six seconds again once both leave.
{
  const record = app.notify("ok", "hold me", { actions: [{ label: "復原", run() {} }] });
  const node = record.node;
  const bar = () => node.children.find((child) => child.className === "timer");
  const firstBar = bar();
  node.dispatchEvent({ type: "mouseenter" });
  runToastTimers();
  if (!toastNodes(document).includes(node)) failures.push("a toast under the pointer went away by itself");
  if (!node.classList.contains("held")) failures.push("a held toast's countdown bar is not paused");
  node.dispatchEvent({ type: "focusin" });
  node.dispatchEvent({ type: "mouseleave" });
  runToastTimers();
  if (!toastNodes(document).includes(node)) failures.push("a toast with the keyboard in it went away when the pointer left");
  // Tab from its 復原 to its ✕ is still inside it.
  const [undoButton, closeButton] = toastButtons(node);
  node.dispatchEvent({ type: "focusout", relatedTarget: closeButton });
  runToastTimers();
  if (!toastNodes(document).includes(node)) failures.push("moving between a toast's own buttons let it go");
  if (toastTimers.length !== 0) failures.push("moving between a toast's own buttons restarted its countdown");
  node.dispatchEvent({ type: "focusout", relatedTarget: null });
  if (node.classList.contains("held")) failures.push("a toast left by pointer and keyboard still shows as held");
  if (toastTimers.length !== 1) failures.push(`leaving a held toast started ${toastTimers.length} countdowns, want one`);
  if (bar() === firstBar || !bar()) failures.push("leaving a held toast did not restart its countdown bar from full");
  runToastTimers();
  if (toastNodes(document).includes(node)) failures.push("a toast let go did not go away when its new six seconds ran out");
  void undoButton;
  // An error has no countdown to hold.
  const lasting = app.notify("error", "stays").node;
  lasting.dispatchEvent({ type: "mouseenter" });
  lasting.dispatchEvent({ type: "mouseleave" });
  if (toastTimers.length !== 0) failures.push("leaving an error toast gave it a countdown");
}
clearToasts();
toastTimers = [];

// An action runs and closes its toast.
let undone = 0;
app.notify("ok", "published", { actions: [{ label: "復原", run: () => { undone++; } }] });
const withAction = latestToast(document).node;
const [undo] = toastButtons(withAction);
if (undo?.textContent !== "復原") failures.push(`the action button reads ${undo?.textContent}`);
undo?.onclick();
if (undone !== 1) failures.push(`the action ran ${undone} times, want 1`);
if (toastNodes(document).includes(withAction)) failures.push("pressing an action left its toast up");

// The old call sites speak through the same thing: ok true is a success,
// anything else an error.
app.banner("old style success", true);
if (latestToast(document).kind !== "ok") failures.push("banner(message, true) was not drawn as a success");
app.banner("old style failure");
if (latestToast(document).kind !== "error") failures.push("banner(message) was not drawn as an error");
clearToasts();

/* ---------------- 2. the bell ---------------- */

app.state.notices.length = 0;
app.notify("ok", "fine");
app.notify("info", "noted");
if (!el("bell-n").classList.contains("hidden")) failures.push(`the bell shows ${el("bell-n").textContent} with nothing but a success and an info unread`);
app.notify("error", "broken");
app.notify("warn", "careful");
if (el("bell-n").textContent !== "2" || el("bell-n").classList.contains("hidden")) {
  failures.push(`the bell shows ${JSON.stringify(el("bell-n").textContent)}, want 2 (one error, one warning)`);
}
el("btn-bell").onclick();
if (el("notify-modal").classList.contains("hidden")) failures.push("the bell did not open the log");
if (!el("bell-n").classList.contains("hidden")) failures.push("opening the log did not mark everything read");
const logged = el("notify-list").textContent;
for (const said of ["fine", "noted", "broken", "careful"]) {
  if (!logged.includes(said)) failures.push(`the log is missing ${said}: ${logged}`);
}
if (logged.indexOf("careful") > logged.indexOf("fine")) failures.push("the log is not newest first");
if (!/\d\d:\d\d/.test(logged)) failures.push(`the log gives no HH:MM time: ${logged}`);
if (!logged.includes(ZH["notify.kind.error"])) failures.push("the log does not say an error is an error in words");
// Something said while the log is open is read as it arrives.
app.notify("error", "while open");
if (!el("bell-n").classList.contains("hidden")) failures.push("a notice that arrived with the log open was counted unread");
if (!el("notify-list").textContent.includes("while open")) failures.push("the open log did not show a notice as it arrived");
el("notify-close").onclick();
if (!el("notify-modal").classList.contains("hidden")) failures.push("the close button left the log open");
// Empty says so.
app.state.notices.length = 0;
app.openNotices();
if (!el("notify-list").textContent.includes(ZH["notify.empty"])) failures.push(`an empty log says ${el("notify-list").textContent}`);
app.closeNotices();
// A hundred at most, the oldest dropped.
for (let n = 0; n < 130; n++) app.notify("info", `n${n}`);
if (app.state.notices.length !== 100) failures.push(`the log holds ${app.state.notices.length}, want 100`);
if (app.state.notices.at(-1).title !== "n30") failures.push(`the oldest kept is ${app.state.notices.at(-1).title}, want n30`);
app.state.notices.length = 0;
clearToasts();

/* ---------------- 3. the attention strip ---------------- */

const byTitle = (text) => attentionRows(document).find((entry) => entry.title.includes(text));
const loadWith = async (overview) => {
  overviewAnswer = overview;
  await app.load();
  await app.loadService();
  await settle();
};

await loadWith(reachable());
if (attentionRows(document).length !== 0) {
  failures.push(`a healthy machine has attention rows: ${attentionRows(document).map((entry) => entry.title).join(" | ")}`);
}
if (!el("attention").classList.contains("hidden")) failures.push("an empty attention strip is not hidden");

// 3a. The node not answering: alert, with a retry that asks again.
await loadWith(unreachable());
let node = byTitle(ZH["attention.nodeDown.title"]);
if (!node) {
  failures.push("an unreachable node put no row on the strip");
} else {
  if (node.sev !== "alert") failures.push(`the unreachable row is ${node.sev}, want alert`);
  if (!node.body.includes("connection refused")) failures.push(`the unreachable row lost the reason: ${node.body}`);
  if (node.action.textContent !== ZH["attention.nodeDown.action"]) failures.push(`its button reads ${node.action.textContent}`);
  overviewAnswer = reachable();
  node.action.onclick();
  await settle();
  if (byTitle(ZH["attention.nodeDown.title"])) failures.push("重試 reached the node and the row stayed");
}
// Once per appearance in the log, not once per render.
const nodeLogged = () => app.state.notices.filter((notice) => notice.title === ZH["attention.nodeDown.title"]).length;
if (nodeLogged() !== 1) failures.push(`the unreachable row was logged ${nodeLogged()} times, want 1`);
if (app.state.notices.find((notice) => notice.title === ZH["attention.nodeDown.title"])?.kind !== "error") {
  failures.push("an alert row was not logged as an error");
}

// 3b. The service: supported, not running.
serviceAnswer = { supported: true, installed: true, running: false, pid: 0, logHint: "~/agenthub.log" };
await loadWith(reachable());
// The first-launch checklist is up with 「把節點跑成背景服務」 not done, and
// that step is the same fix: on the local view, where the card is, the strip
// leaves it to the card. Logged all the same, and on another view it is back.
if (el("onboarding").classList.contains("hidden")) failures.push("the checklist did not come up for a stopped service");
if (byTitle(ZH["attention.service.stoppedTitle"])) failures.push("the strip repeats the checklist's service step beside it");
if (!app.state.notices.some((notice) => notice.title === ZH["attention.service.stoppedTitle"])) {
  failures.push("the service row the card stands in for was not logged");
}
app.state.view = "settings";
app.render();
if (!byTitle(ZH["attention.service.stoppedTitle"])) failures.push("away from the checklist the strip does not say the service is stopped");
app.state.view = "local";
app.state.ui.onboardingDismissed = true;
app.render();
let service = byTitle(ZH["attention.service.stoppedTitle"]);
if (!service) {
  failures.push("a stopped service put no row on the strip");
} else {
  if (service.sev !== "alert") failures.push(`the service row is ${service.sev}, want alert`);
  if (!service.body.includes("~/agenthub.log")) failures.push(`the service row does not say where the log is: ${service.body}`);
  // The button does the fix itself now (inline-publish.mjs §service presses
  // it and follows each branch); here, that it says which fix it is.
  if (service.action.textContent !== ZH["attention.service.startAction"]) {
    failures.push(`a stopped service's row offers ${service.action.textContent}, want ${ZH["attention.service.startAction"]}`);
  }
}
// A node that answers and is not a service was started by hand: its button
// goes to the form rather than installing (inline-publish.mjs §6 presses it).
serviceAnswer = { supported: true, installed: false, running: false, pid: 0 };
await loadWith(reachable());
if (!byTitle(ZH["attention.service.noneTitle"])) failures.push("a node that is not a service put no row on the strip");
else if (byTitle(ZH["attention.service.noneTitle"]).action.textContent !== ZH["attention.service.formAction"]) {
  failures.push(`a node that is not a service is offered ${byTitle(ZH["attention.service.noneTitle"]).action.textContent}`);
}
// Nothing running at all: installing is the fix, and the button says so.
await loadWith(unreachable());
if (byTitle(ZH["attention.service.noneTitle"])?.action.textContent !== ZH["attention.service.installAction"]) {
  failures.push(`with nothing running the service row offers ${byTitle(ZH["attention.service.noneTitle"])?.action.textContent}`);
}
// Not a service manager this machine has, and not a status this window could
// read: nothing to do from here, so nothing on the strip.
serviceAnswer = { supported: false };
await loadWith(reachable());
if (byTitle(ZH["attention.service.noneTitle"]) || byTitle(ZH["attention.service.stoppedTitle"])) {
  failures.push("a platform without a service manager was told to go and install one");
}
serviceAnswer = { supported: true, installed: true, running: true, pid: 1 };
await loadWith(reachable());
if (byTitle(ZH["attention.service.stoppedTitle"]) || byTitle(ZH["attention.service.noneTitle"])) {
  failures.push("the service row outlived the service starting");
}

// 3c. A full inbox: warn, names the session, opens that inbox.
countsAnswer = { ok: true, counts: { "codex:two": { held: 500, capacity: 500, full: true }, "claude:one": { held: 3, capacity: 500, full: false } } };
await loadWith(reachable());
let inbox = byTitle(fill(ZH["attention.inboxFull.titleOne"], { session: "Second one" }));
if (!inbox) {
  failures.push(`one full inbox put no row naming it on the strip: ${attentionRows(document).map((entry) => entry.title).join(" | ")}`);
} else {
  if (inbox.sev !== "warn") failures.push(`the full-inbox row is ${inbox.sev}, want warn`);
  if (!inbox.body.includes("500")) failures.push(`the full-inbox row does not say how full: ${inbox.body}`);
  inbox.action.onclick();
  await settle();
  if (inboxReads.at(-1) !== "codex:two") failures.push(`開啟收件匣 opened ${inboxReads.at(-1)}, want codex:two`);
  el("inbox-close").onclick();
}
// Two full: the title counts them, the button opens the first in table order.
countsAnswer = { ok: true, counts: { "codex:two": { held: 9, capacity: 9, full: true }, "claude:one": { held: 5, capacity: 5, full: true } } };
await loadWith(reachable());
inbox = byTitle(fill(ZH["attention.inboxFull.titleMany.other"], { n: 2 }));
if (!inbox) {
  failures.push(`two full inboxes did not give a counted title: ${attentionRows(document).map((entry) => entry.title).join(" | ")}`);
} else {
  inbox.action.onclick();
  await settle();
  if (inboxReads.at(-1) !== "claude:one") failures.push(`with two full the button opened ${inboxReads.at(-1)}, want the first row, claude:one`);
  el("inbox-close").onclick();
}
// Counts nobody could read say nothing, full or not.
countsAnswer = { ok: false, counts: {} };
await loadWith(reachable());
if (attentionRows(document).some((entry) => entry.sev === "warn")) failures.push("an unread count still claimed an inbox is full");
countsAnswer = { ok: true, counts: {} };
await loadWith(reachable());
if (attentionRows(document).some((entry) => entry.sev === "warn")) failures.push("the full-inbox row outlived the inbox emptying");

// 3d. A machine asking to pair: read by the fifteen-second tick, and only
//     while the node last said the pairing window is open.
const incoming = { id: "pair_in1", direction: "incoming", state: "pending", displayName: "ubuntu-lab", nodeId: "node_ub" };
const outgoing = { id: "pair_out1", direction: "outgoing", state: "pending", displayName: "win", nodeId: "node_win" };
pairRequestsAnswer = [incoming, outgoing];
pairRequestsCalls.length = 0;
pairingAnswer = { availability: "on", windowAvailable: true, state: { open: false }, candidates: [] };
await app.loadPairing();
tick15.fn();
await settle();
if (pairRequestsCalls.length !== 0) failures.push(`the tick read the exchange ${pairRequestsCalls.length} times with the pairing window closed`);
pairingAnswer = { availability: "on", windowAvailable: true, state: { open: true, remainingSeconds: 300 }, candidates: [] };
await app.loadPairing();
tick15.fn();
await settle();
if (pairRequestsCalls.length !== 1 || pairRequestsCalls[0] !== false) {
  failures.push(`one tick with the window open read the exchange as ${JSON.stringify(pairRequestsCalls)}, want [false]`);
}
let pair = byTitle(fill(ZH["attention.pair.titleOne"], { name: "ubuntu-lab" }));
if (!pair) {
  failures.push(`an incoming request put no row on the strip: ${attentionRows(document).map((entry) => entry.title).join(" | ")}`);
} else {
  if (pair.sev !== "info") failures.push(`the pairing row is ${pair.sev}, want info`);
  // The name is the other machine's own word for itself, marked as that the
  // way an inbox sender's chosen half is: a claimed span, 「自稱」 in front.
  const claimed = [];
  const walk = (node) => {
    if (!node || typeof node !== "object") return;
    if (String(node.className).split(/\s+/).includes("claimed")) claimed.push(node.textContent);
    for (const child of node.children ?? []) walk(child);
  };
  walk(pair.row);
  if (JSON.stringify(claimed) !== JSON.stringify(["ubuntu-lab"])) failures.push(`the pairing row marks ${JSON.stringify(claimed)} as claimed, want the name alone`);
  if (!pair.title.includes("自稱")) failures.push(`the pairing row does not say the name is claimed: ${pair.title}`);
}
// The owner is busy selecting rows: the tick stays away, as it does for the list.
app.state.selected.add("claude:one");
tick15.fn();
await settle();
if (pairRequestsCalls.length !== 1) failures.push("the tick read the exchange while rows were selected");
app.state.selected.clear();
// With the drawer open the drawer's own poll reads these rows; the tick does
// not. The drawer is a modal, so the tick is held off whole — and the read
// refuses on its own as well, for a drawer opened while the tick's overview
// read was still on its way.
el("pairing-modal").classList.remove("hidden");
tick15.fn();
await settle();
await app.refreshIncomingPairRequests();
if (pairRequestsCalls.length !== 1) failures.push("the tick read the exchange while the pairing drawer was open");
el("pairing-modal").classList.add("hidden");
// 比對並核准 goes to the view the drawer belongs to and opens it.
if (pair) {
  pair.action.onclick();
  await settle();
  if (app.state.view !== "network") failures.push(`比對並核准 left the view at ${app.state.view}`);
  if (el("pairing-modal").classList.contains("hidden")) failures.push("比對並核准 did not open the pairing drawer");
  await app.dismissPairingDrawer();
  app.state.view = "local";
}
// A machine that gave no name is said to have given none — its node id is
// not a name it chose, and 「自稱 node_ub」 would put one in its mouth.
{
  pairRequestsAnswer = [{ ...incoming, displayName: "" }];
  tick15.fn();
  await settle();
  const titles = attentionRows(document).map((entry) => entry.title);
  if (!titles.includes(ZH["attention.pair.titleOneNoName"])) failures.push(`a nameless request's row reads ${JSON.stringify(titles)}`);
  if (titles.some((title) => title.includes("node_ub"))) failures.push(`a nameless request is called by its node id: ${JSON.stringify(titles)}`);
  pairRequestsAnswer = [incoming, outgoing];
  tick15.fn();
  await settle();
}
// Decided elsewhere: the next read has nothing waiting, and the row goes.
pairRequestsAnswer = [outgoing];
el("pairing-modal").classList.add("hidden");
tick15.fn();
await settle();
if (attentionRows(document).some((entry) => entry.sev === "info")) failures.push("the pairing row outlived the request");
// Reads are numbered: a slow answer that lands after a newer one is dropped.
// Here the older read would put the request back; the newer one says there
// is none.
{
  let resolveSlow;
  const slow = new Promise((resolve) => { resolveSlow = resolve; });
  let calls = 0;
  pairRequestsHook = () => (calls++ === 0 ? slow : Promise.resolve([outgoing]));
  const older = app.refreshIncomingPairRequests();
  await app.refreshIncomingPairRequests();
  resolveSlow([incoming, outgoing]);
  await older;
  await settle();
  pairRequestsHook = null;
  if (calls !== 2) failures.push(`the ordering check made ${calls} reads, want 2`);
  if (attentionRows(document).some((entry) => entry.sev === "info")) failures.push("a slow read of the exchange overwrote a newer one");
}
// A window that closed takes its requests with it, without another read.
pairRequestsAnswer = [incoming];
tick15.fn();
await settle();
const callsBeforeClose = pairRequestsCalls.length;
pairingAnswer = { availability: "on", windowAvailable: true, state: { open: false }, candidates: [] };
await app.loadPairing();
app.render();
if (attentionRows(document).some((entry) => entry.sev === "info")) failures.push("the pairing row outlived the pairing window");
tick15.fn();
await settle();
if (pairRequestsCalls.length !== callsBeforeClose) failures.push("a closed window was read again");

// 3e. 稍後: put away while the thing lasts, back when it comes back.
await loadWith(reachable());
const outagesBefore = nodeLogged();
await loadWith(unreachable());
node = byTitle(ZH["attention.nodeDown.title"]);
node?.later.onclick();
if (byTitle(ZH["attention.nodeDown.title"])) failures.push("稍後 did not put the row away");
await loadWith(unreachable());
if (byTitle(ZH["attention.nodeDown.title"])) failures.push("a row put away came back while the thing was still going on");
if (nodeLogged() !== outagesBefore + 1) failures.push(`an outage was logged ${nodeLogged() - outagesBefore} times, want once per appearance`);
await loadWith(reachable());
await loadWith(unreachable());
if (!byTitle(ZH["attention.nodeDown.title"])) failures.push("a row put away did not come back after the thing cleared and happened again");
// One row put away leaves the others.
serviceAnswer = { supported: true, installed: false, running: false, pid: 0 };
await loadWith(unreachable());
byTitle(ZH["attention.nodeDown.title"])?.later.onclick();
if (!byTitle(ZH["attention.service.noneTitle"])) failures.push("putting one row away took another with it");
// And the row element is kept across renders, like every other row a tick
// repaints: a new element would be a button pulled out from under the pointer.
const kept = byTitle(ZH["attention.service.noneTitle"])?.row;
app.render();
if (byTitle(ZH["attention.service.noneTitle"])?.row !== kept) failures.push("a render replaced the attention row element");
serviceAnswer = { supported: true, installed: true, running: true, pid: 1 };
await loadWith(reachable());

// 3f. Past two rows, the rest fold behind one button, most severe first; the
//     button unfolds them for this window only.
serviceAnswer = { supported: true, installed: true, running: false, pid: 0, logHint: "~/agenthub.log" };
countsAnswer = { ok: true, counts: { "codex:two": { held: 9, capacity: 9, full: true } } };
pairRequestsAnswer = [incoming];
pairingAnswer = { availability: "on", windowAvailable: true, state: { open: true, remainingSeconds: 300 }, candidates: [] };
await app.loadPairing();
await loadWith(reachable());
tick15.fn();
await settle();
{
  const strip = () => el("attention").children;
  const titles = () => attentionRows(document).filter((entry) => entry.title).map((entry) => entry.sev);
  const more = () => strip().at(-1);
  if (JSON.stringify(titles()) !== JSON.stringify(["alert", "warn"])) failures.push(`three things show ${JSON.stringify(titles())}, want the alert and the warning`);
  const button = more()?.children?.[0];
  if (strip().length !== 3 || button?.textContent !== fill(ZH["attention.more.one"], { n: 1 })) {
    failures.push(`the third row is ${JSON.stringify(button?.textContent)}, want ${fill(ZH["attention.more.one"], { n: 1 })}`);
  }
  button?.onclick();
  if (JSON.stringify(titles()) !== JSON.stringify(["alert", "warn", "info"])) failures.push(`unfolded the strip shows ${JSON.stringify(titles())}`);
  if (more()?.children?.[0] !== button || button?.textContent !== ZH["attention.less"]) failures.push(`unfolded, the button reads ${button?.textContent}`);
  // It stays unfolded across the tick's renders.
  app.render();
  if (titles().length !== 3) failures.push("a render folded the strip back up");
  button?.onclick();
  if (titles().length !== 2) failures.push("收起 did not fold the strip");
  // Two or fewer: no button at all — and unfolded is forgotten, so a third
  // thing arriving later starts folded again.
  button?.onclick();
  pairRequestsAnswer = [];
  tick15.fn();
  await settle();
  if (attentionRows(document).some((entry) => !entry.title)) failures.push("two rows still carry the fold button");
  pairRequestsAnswer = [incoming];
  tick15.fn();
  await settle();
  if (titles().length !== 2 || more()?.children?.[0]?.textContent !== fill(ZH["attention.more.one"], { n: 1 })) {
    failures.push(`a strip that went down to two and back to three came back unfolded: ${JSON.stringify(titles())}`);
  }
  pairRequestsAnswer = [];
  tick15.fn();
  await settle();
}
serviceAnswer = { supported: true, installed: true, running: true, pid: 1 };
countsAnswer = { ok: true, counts: {} };
pairingAnswer = { availability: "on", windowAvailable: true, state: { open: false }, candidates: [] };
await app.loadPairing();
await loadWith(reachable());

/* ---------------- 4. the pressed button spins ---------------- */

clearToasts();
const rescan = el("btn-discover");
rescan.onclick();
await settle();
if (!rescan.classList.contains("busy") || !rescan.disabled) failures.push("重新掃描 did not spin and disable itself while the scan ran");
discoverPark?.({ claude: 1, codex: 1, total: 2, skipped: 0 });
await settle();
if (rescan.classList.contains("busy") || rescan.disabled) failures.push("重新掃描 was left spinning after the scan answered");
if (!latestToast(document).textContent.includes("2")) failures.push(`the scan's answer is not on a toast: ${latestToast(document).textContent}`);

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("notifications: errors stay, successes go, the bell counts what was missed, the strip says what is waiting and 稍後 holds until it clears");
