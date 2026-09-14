// Loads the whole module — wiring, polls and handlers included — and drives it
// through the sequences a render-only check cannot reach. Named for the pairing
// panel it was written for; it now also covers the inbox wiring, which sits
// below the same marker the render checks slice at.
//
// The other render checks slice the source at the wiring marker, so loadPairing,
// the two intervals and the button handlers were never executed by anything.
// Three defects lived in exactly that gap: a stale poll overwriting a fresher
// one, an expired window counting 0:00 until the next read, and the countdown
// rebuilding the candidate rows every second.
//
//   node frontend/test/pairing-lifecycle.mjs

import { document } from "./dom-shim.mjs";

globalThis.document = document;

const failures = [];
const el = (id) => document.getElementById(id);

// The intervals are captured rather than run, so the test decides when a tick
// happens instead of waiting for one.
const ticks = [];
const fakeSetInterval = (fn, ms) => {
  ticks.push({ fn, ms });
  return ticks.length;
};

// Pairing() answers from a queue the test controls, with a settle delay so two
// reads can be made to land out of order.
let pairingQueue = [];
const pairingCalls = [];
const Pairing = () => {
  const next = pairingQueue.shift() ?? { availability: "unknown", candidates: [] };
  pairingCalls.push(next);
  return new Promise((resolve) => setTimeout(() => resolve(next.value ?? next), next.delay ?? 0));
};
const openCalls = [];
const closeCalls = [];
const OpenPairing = async (seconds) => { openCalls.push(seconds); return { open: true }; };
const ClosePairing = async () => { closeCalls.push(true); return { open: false }; };
const Overview = async () => ({
  node: { id: "node_local", displayName: "local", platform: "test", fingerprint: "AAAA" },
  sessions: [], nodes: [], peers: [], counts: { total: 0 }, nodeUrl: "http://127.0.0.1:7462",
  reachable: true,
});
const noop = async () => ({});

// The inbox bindings, recorded so the destructive button can be followed.
const inboxReads = [];
const clearCalls = [];
// Answers after a delay the test controls, so a slow read really can land after
// a fast one. Without that the race the guard exists for never happens and
// removing the guard passes.
const inboxDelays = new Map();
const inboxRejects = new Set();
const InboxStub = async (sessionId) => {
  inboxReads.push(sessionId);
  const delay = inboxDelays.get(sessionId) ?? 0;
  if (delay > 0) await new Promise((resolve) => setTimeout(resolve, delay));
  if (inboxRejects.has(sessionId)) throw new Error("contact node: connection refused");
  return {
    sessionId, messages: [], held: 0, capacity: 500, full: false, showing: 0, more: false,
  };
};
let clearResult = { removed: 3 };
const ClearInboxStub = async (sessionId) => {
  clearCalls.push(sessionId);
  return clearResult;
};

let confirmAnswer = true;
globalThis.setInterval = fakeSetInterval;
globalThis.confirm = () => confirmAnswer;

const { configure, boot } = await import("../src/app.js");
configure({
  Overview, Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop,
  Heartbeat: noop, Pairing, OpenPairing, ClosePairing, Inbox: InboxStub, ClearInbox: ClearInboxStub,
});
const scope = boot();

const { state, loadPairing } = scope;
const settle = () => new Promise((resolve) => setTimeout(resolve, 30));

const openWindow = (seconds, extra = {}) => ({
  availability: "on",
  state: { open: true, remainingSeconds: seconds, announcing: { announceableAddresses: 1 } },
  candidates: [], ...extra,
});
const closedWindow = () => ({
  availability: "on",
  state: { open: false, announcing: { announceableAddresses: 1 } },
  candidates: [],
});

state.view = "network";

// 1. The wiring registered its intervals — the 5s pairing poll, the 1s
//    countdown and the 15s main-list refresh (list-refresh.mjs covers that
//    one) — and the fast one only ticks the countdown. If it redrew the panel,
//    the candidate rows would be replaced every second, including the row under
//    the pointer.
if (ticks.length !== 3) {
  failures.push(`the module registered ${ticks.length} intervals, want 3`);
}
const countdownTick = ticks.find((tick) => tick.ms === 1000);
if (!countdownTick) failures.push("no one-second interval was registered");

await settle();

// 2. A stale read must not overwrite a fresher one.
//
// The client's timeout is 15s and the poll runs every 5, so several can be in
// flight. Here a slow read describing an open window is started first, then a
// fast read describing a closed one — the shape of clicking Stop while a poll
// is outstanding. The panel must end up closed.
pairingQueue = [
  { value: openWindow(150), delay: 40 },
  { value: closedWindow(), delay: 0 },
];
const slow = loadPairing();
const fast = loadPairing();
await Promise.all([slow, fast]);
await settle();
if (state.pairing?.state?.open) {
  failures.push("a stale read put the window back to open after a fresher one closed it");
}
if (el("pairing-headline").serialize().includes("開啟中")) {
  failures.push("the panel still claims an open window after a fresher read closed it");
}
if (!el("btn-pairing-off").disabled) {
  failures.push("the Stop button is live on a closed window");
}

// 3. And the same in the other direction: an old closed read must not close a
//    window a newer read reported open.
pairingQueue = [
  { value: closedWindow(), delay: 40 },
  { value: openWindow(150), delay: 0 },
];
await Promise.all([loadPairing(), loadPairing()]);
await settle();
if (!state.pairing?.state?.open) {
  failures.push("a stale read closed a window a fresher one reported open");
}

// 4. Ticking the countdown must not rebuild the rows.
pairingQueue = [{ value: openWindow(120, {
  candidates: [{
    nodeId: "node_alice", address: "192.168.1.5:7463", displayName: "alice",
    platform: "darwin/arm64", fingerprint: "AAAA BBBB CCCC DDDD EEEE FFFF",
    firstSeen: new Date().toISOString(), lastSeen: new Date().toISOString(),
  }],
}) }];
await loadPairing();
await settle();
const rowsBefore = el("candidate-rows").children.slice();
const buttonBefore = rowsBefore[0]?.children?.find?.((child) => child.tagName === "button");
countdownTick.fn();
const rowsAfter = el("candidate-rows").children.slice();
const buttonAfter = rowsAfter[0]?.children?.find?.((child) => child.tagName === "button");
if (rowsBefore[0] !== rowsAfter[0]) {
  failures.push("a countdown tick replaced the candidate row element");
}
if (buttonBefore && buttonBefore !== buttonAfter) {
  failures.push("a countdown tick replaced the row's own button, so a click in flight lands elsewhere");
}
// It did update the countdown, so the tick is not simply doing nothing.
if (!el("pairing-countdown").serialize().includes(":")) {
  failures.push("the tick did not write a countdown");
}

// 5. A window whose count reaches zero asks the node instead of counting 0:00.
//
// Started from a window with time left, so the headline has to change on the
// tick rather than having arrived that way: with remainingSeconds already 0 the
// panel rendered "已到期" on load, and removing the tick's re-render passed.
pairingQueue = [{ value: openWindow(1) }, { value: closedWindow() }];
await loadPairing();
await settle();
if (el("pairing-headline").serialize().includes("已到期")) {
  failures.push("the window read as expired before the countdown reached zero");
}
// Let the countdown run out, so the tick is what discovers it.
state.pairingReadAt = performance.now() - 2000;
const callsBefore = pairingCalls.length;
countdownTick.fn();
// Read before settling. Once the reload lands the panel is closed and the
// countdown is empty for that reason, which would make this assertion pass
// whatever the tick had written.
if (el("pairing-countdown").serialize().includes("0:00")) {
  failures.push('an expired window rendered "0:00"');
}
if (!el("pairing-headline").serialize().includes("已到期")) {
  failures.push("an expired countdown did not say the window had expired");
}
await settle();
if (pairingCalls.length === callsBefore) {
  failures.push("an expired countdown did not ask the node whether the window had closed");
}

// 6. The open button asks for the node's own default rather than a duration of
//    its own, so the desktop and `ah` cannot disagree about how long a window
//    lasts.
pairingQueue = [{ value: openWindow(300) }];
await scope.state && null;
const onClick = el("btn-pairing-on").onclick;
if (typeof onClick !== "function") {
  failures.push("the open button has no handler");
} else {
  await onClick();
  await settle();
  if (openCalls.length !== 1 || openCalls[0] !== 0) {
    failures.push(`OpenPairing was called with ${JSON.stringify(openCalls)}, want [0]`);
  }
}
pairingQueue = [{ value: closedWindow() }];
const offClick = el("btn-pairing-off").onclick;
if (typeof offClick !== "function") {
  failures.push("the stop button has no handler");
} else {
  await offClick();
  await settle();
  if (closeCalls.length !== 1) {
    failures.push("ClosePairing was not called");
  }
}

const closeInboxHandler = () => el("inbox-close").onclick();

// 8. The clear button empties the session the dialog is showing, and only
//    after the owner says yes.
//
//    It reads state.inboxSession, which an out-of-order read could have left
//    pointing elsewhere — so a stale answer must not repaint the dialog. That
//    guard is what this checks, on the one irreversible action here.
await scope.openInbox("claude:shown");
clearCalls.length = 0;
confirmAnswer = false;
await el("inbox-clear").onclick();
await settle();
if (clearCalls.length !== 0) {
  failures.push("the inbox was cleared after the owner declined");
}
confirmAnswer = true;
await el("inbox-clear").onclick();
await settle();
if (clearCalls.length !== 1 || clearCalls[0] !== "claude:shown") {
  failures.push(`clear emptied ${JSON.stringify(clearCalls)}, want ["claude:shown"] once`);
}
// What it did, where the person who pressed it is looking. A banner is behind
// the modal, so the count — the only sign that something arrived between the
// read and the confirm — would be invisible there.
if (!el("inbox-body").serialize().includes("移除 3 則")) {
  failures.push(`the clear result is not in the dialog: ${el("inbox-body").serialize()}`);
}

// A clear that fails has to say so there too, or the owner is left with an
// unchanged list and no sign the destructive action did not happen.
clearResult = { removed: 0, error: "the inbox could not be emptied" };
await el("inbox-clear").onclick();
await settle();
const afterFailure = el("inbox-body").serialize();
if (!afterFailure.includes("could not be emptied")) {
  failures.push(`a failed clear said nothing in the dialog: ${afterFailure}`);
}
if (!afterFailure.includes("沒有變動")) {
  failures.push("a failed clear did not say the inbox is unchanged");
}
clearResult = { removed: 3 };

// 9. A slow read landing after a newer one must not repaint the dialog, because
//    the clear button aims at whatever the dialog says it is showing.
inboxDelays.set("claude:slow", 60);
const slowInbox = scope.openInbox("claude:slow");
const fastInbox = scope.openInbox("claude:fast");
await Promise.all([slowInbox, fastInbox]);
await settle();
inboxDelays.clear();
if (scope.state.inboxSession !== "claude:fast") {
  failures.push(`a stale read left the dialog aimed at ${scope.state.inboxSession}`);
}
if (!el("inbox-meta").serialize().includes("claude:fast")) {
  failures.push(`the dialog shows ${el("inbox-meta").serialize()} while clear targets claude:fast`);
}
if (el("inbox-meta").serialize().includes("claude:slow")) {
  failures.push("the slow read repainted the dialog after the fast one landed");
}
// And the button follows the dialog, not the last read to finish. This is the
// irreversible one.
clearCalls.length = 0;
await el("inbox-clear").onclick();
await settle();
if (clearCalls[0] !== "claude:fast") {
  failures.push(`clear aimed at ${clearCalls[0]}, not the session the dialog is showing`);
}

// 9b. While a read is in flight the button empties nothing. It aims at whatever
//     the dialog is showing, and during a load that is not yet a session.
inboxDelays.set("claude:pending", 60);
clearCalls.length = 0;
const pending = scope.openInbox("claude:pending");
await el("inbox-clear").onclick();
if (clearCalls.length !== 0) {
  failures.push(`clear emptied ${clearCalls[0]} while its content was still loading`);
}
await pending;
await settle();
inboxDelays.clear();

// 9c. A binding that rejects shows its reason in the dialog. It used to reach a
//     banner the modal covers, so the owner saw an open dialog saying nothing
//     while the error sat behind it.
inboxRejects.add("claude:broken");
await scope.openInbox("claude:broken");
await settle();
inboxRejects.clear();
const brokenBody = el("inbox-body").serialize();
if (!brokenBody.includes("connection refused")) {
  failures.push(`a rejected read did not show its reason in the dialog: ${brokenBody}`);
}
if (brokenBody.includes("還沒有任何訊息")) {
  failures.push("a rejected read was rendered as an empty inbox");
}
if (el("inbox-modal").classList.contains("hidden")) {
  failures.push("a rejected read closed the dialog");
}
// And clear stays available, deliberately: a read that fails because the
// inbox is too large to decode is exactly when emptying it is the way out, and
// the truncation message says so. It must still aim at the session that was
// asked for.
clearCalls.length = 0;
await el("inbox-clear").onclick();
await settle();
if (clearCalls[0] !== "claude:broken") {
  failures.push(`after a failed read clear aimed at ${clearCalls[0]}, want claude:broken`);
}

// 9d. Closing retires a read in flight, so its answer cannot repaint a hidden
//     dialog and re-arm the button. Today that is safe only because the button
//     is unclickable while hidden — a guard leaning on a CSS rule.
inboxDelays.set("claude:abandoned", 60);
const abandoned = scope.openInbox("claude:abandoned");
closeInboxHandler();
await abandoned;
await settle();
inboxDelays.clear();
if (scope.state.inboxSession !== null) {
  failures.push(`a read that landed after closing re-armed the button at ${scope.state.inboxSession}`);
}

// 10. Closing forgets which session it was, so a later clear cannot fire at it.
el("inbox-close").onclick();
if (scope.state.inboxSession !== null) {
  failures.push("closing the dialog left it aimed at a session");
}
clearCalls.length = 0;
await el("inbox-clear").onclick();
if (clearCalls.length !== 0) {
  failures.push("clear fired with no session open");
}

// The inbox sections run before section 7, which instantiates the module a
// second time. The shim's element cache is module-global, so that second
// instance's wiring replaces the first's handlers — and a handler closing over
// the other instance's state reads an empty inboxSession and returns early,
// which looks exactly like the button not working.
// 7. A binding that throws is a failure to read, not a fact about the network.
pairingQueue = [];
globalThis.setInterval = () => 0;
configure({
  Overview, Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop,
  Heartbeat: noop, Pairing: () => Promise.reject(new Error("binding exploded")),
  OpenPairing, ClosePairing,
});
const throwing = boot();
throwing.state.view = "network";
await throwing.loadPairing();
if (throwing.state.pairing?.availability !== "unknown") {
  failures.push(`a thrown binding gave availability ${throwing.state.pairing?.availability}, want unknown`);
}
if (!String(throwing.state.pairing?.error).includes("binding exploded")) {
  failures.push("a thrown binding lost its reason");
}

// 11. The broadcast name is re-read on every poll, not once at startup.
//
//     The warning tells an owner to restart the node with -display-name. Doing
//     what it says used to leave the panel naming the old string forever: the
//     name was read in load(), and the poller only calls loadPairing(). An
//     owner then reads a warning about a name their machine no longer sends.
//
//     The queue also covers the other direction: a poll that could not reach
//     the node must keep the last name it knew, rather than blanking the
//     warning to （未知） because one read failed.
// A single mutable answer rather than a queue: constructing the module fires a
// read of its own, which would consume a queued one before the first assertion.
const announcing = (displayName, nameIsChosen = false) => ({
  availability: "on",
  candidates: [],
  state: { open: false, displayName, nameIsChosen, announcing: { announceableAddresses: 1 } },
});
let answer = announcing("sheldon.chang mac");
globalThis.setInterval = () => 0;
configure({
  Overview, Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop,
  Heartbeat: noop, Pairing: () => Promise.resolve(answer), OpenPairing, ClosePairing,
});
const named = boot();
named.state.view = "network";

await named.loadPairing();
if (named.state.localName !== "sheldon.chang mac") {
  failures.push(`a poll left the broadcast name as ${JSON.stringify(named.state.localName)}`);
}

// A poll that could not reach the node keeps the last name it knew. Blanking
// the warning to （未知） because one read failed drops the one string it is for.
answer = { availability: "unknown", candidates: [], error: "node down" };
await named.loadPairing();
if (named.state.localName !== "sheldon.chang mac") {
  failures.push(
    `an unreachable node dropped the last known broadcast name: ${JSON.stringify(named.state.localName)}`);
}

// Restarted under -display-name: the panel follows, which is the whole point —
// and it learns that the name was picked, not read off the machine.
//
// Both halves, because only one of them was held down. Deleting the line that
// carries nameIsChosen left every test green, and the panel then told an owner
// who had just run -display-name that their name came from the machine — the
// exact sentence the previous round removed.
answer = announcing("Sheldon 的 MacBook", true);
await named.loadPairing();
if (named.state.localName !== "Sheldon 的 MacBook") {
  failures.push(
    `a restart under a new name still shows ${JSON.stringify(named.state.localName)}`);
}
if (named.state.localNameIsChosen !== true) {
  failures.push("a chosen name arrived without its provenance, so the warning names the wrong remedy");
}
answer = announcing("sheldon.chang mac", false);
await named.loadPairing();
if (named.state.localNameIsChosen !== false) {
  failures.push("a name read off the machine still reads as chosen");
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("pairing panel survives out-of-order reads, expiry and ticks");
