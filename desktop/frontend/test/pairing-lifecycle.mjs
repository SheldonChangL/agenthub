// Loads the whole module — wiring, polls and handlers included — and drives the
// pairing panel through the sequences a render-only check cannot reach.
//
// The other render checks slice the source at the wiring marker, so loadPairing,
// the two intervals and the button handlers were never executed by anything.
// Three defects lived in exactly that gap: a stale poll overwriting a fresher
// one, an expired window counting 0:00 until the next read, and the countdown
// rebuilding the candidate rows every second.
//
//   node frontend/test/pairing-lifecycle.mjs [path-to-main.js]

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { document } from "./dom-shim.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const target = process.argv[2] ?? path.join(here, "..", "src", "main.js");

let source = fs.readFileSync(target, "utf8");
source = source
  .replace(/^import[\s\S]*?;\s*$/m, "")
  .replace(/^import\s+\{[\s\S]*?\}\s+from\s+".*?";\s*$/m, "");

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

const scope = new Function(
  "document", "setInterval", "Overview", "Discover", "SetAudience", "TrustNode", "RevokeNode",
  "Heartbeat", "Pairing", "OpenPairing", "ClosePairing",
  source + "\nreturn { state, loadPairing, renderPairing, tickCountdown, pairingRemaining };"
)(document, fakeSetInterval, Overview, noop, noop, noop, noop, noop, Pairing, OpenPairing, ClosePairing);

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

// 1. The wiring registered both intervals, and the fast one only ticks the
//    countdown. If it redrew the panel, the candidate rows would be replaced
//    every second — including the row under the pointer.
if (ticks.length !== 2) {
  failures.push(`the module registered ${ticks.length} intervals, want 2`);
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
pairingQueue = [{ value: openWindow(0) }, { value: closedWindow() }];
await loadPairing();
await settle();
const callsBefore = pairingCalls.length;
countdownTick.fn();
await settle();
if (pairingCalls.length === callsBefore) {
  failures.push("an expired countdown did not ask the node whether the window had closed");
}
if (el("pairing-countdown").serialize().includes("0:00")) {
  failures.push('an expired window rendered "0:00"');
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

// 7. A binding that throws is a failure to read, not a fact about the network.
pairingQueue = [];
const throwing = new Function(
  "document", "setInterval", "Overview", "Discover", "SetAudience", "TrustNode", "RevokeNode",
  "Heartbeat", "Pairing", "OpenPairing", "ClosePairing",
  source + "\nreturn { state, loadPairing };"
)(document, () => 0, Overview, noop, noop, noop, noop, noop,
  () => Promise.reject(new Error("binding exploded")), OpenPairing, ClosePairing);
throwing.state.view = "network";
await throwing.loadPairing();
if (throwing.state.pairing?.availability !== "unknown") {
  failures.push(`a thrown binding gave availability ${throwing.state.pairing?.availability}, want unknown`);
}
if (!String(throwing.state.pairing?.error).includes("binding exploded")) {
  failures.push("a thrown binding lost its reason");
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("pairing panel survives out-of-order reads, expiry and ticks");
