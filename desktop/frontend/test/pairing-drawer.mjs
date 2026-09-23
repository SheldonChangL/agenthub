// The pairing drawer opens and closes the window on its own, so the two races
// around that are checked here, against the drawer's own handlers (#193
// review, P2-3 and P2-4):
//
//   1. Dismissed while the first read is still out, the drawer must not go on
//      to open a window nobody is looking at — nothing on screen would close it.
//   2. Dismissing it closes the window only when nobody is waiting, and "nobody
//      is waiting" is read from the node at that moment, not from the last
//      two-second poll: a request that arrived since would be expired by the
//      node's ExpirePending when the window closed (internal/api/pair.go).
//
//   node frontend/test/pairing-drawer.mjs

import { document } from "./dom-shim.mjs";
import { TEXT as ZH } from "../src/i18n/zh-Hant.js";

globalThis.document = document;
globalThis.setInterval = () => 0;

const failures = [];
const el = (id) => document.getElementById(id);
const noop = async () => ({});
const settle = () => new Promise((resolve) => setTimeout(resolve, 30));

let pairingOpen = false;
let pairingDelay = 0;
let openDelay = 0;
const openCalls = [];
const closeCalls = [];
const Pairing = () => new Promise((resolve) => setTimeout(() => resolve({
  availability: "on", windowAvailable: true,
  state: { open: pairingOpen, remainingSeconds: 300, announcing: { announceableAddresses: 1 } },
  candidates: [],
}), pairingDelay));

let requests = [];
let requestsReads = 0;
let requestsFail = false;
const PairRequests = async () => {
  requestsReads += 1;
  if (requestsFail) throw new Error("contact node: connection refused");
  return requests;
};

const { configure, boot } = await import("../src/app.js");
configure({
  Overview: async () => ({
    reachable: true, nodeUrl: "http://127.0.0.1:7462",
    node: { id: "node_local", displayName: "local", platform: "test" },
    sessions: [], nodes: [], peers: [], counts: {},
  }),
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop,
  SetNodeAddress: noop, Pairing, PairRequests,
  OpenPairing: async (seconds) => {
    openCalls.push(seconds);
    if (openDelay > 0) await new Promise((resolve) => setTimeout(resolve, openDelay));
    pairingOpen = true;
    return { open: true };
  },
  ClosePairing: async () => { closeCalls.push(true); pairingOpen = false; return { open: false }; },
  Inbox: noop, ClearInbox: noop, Outbound: noop, Wakes: noop,
  StartPairRequest: noop, ApprovePairRequest: noop, ConfirmPairRequest: noop, RejectPairRequest: noop,
  NodeSettings: async () => { throw new Error("not in this test"); },
});
const scope = boot();
// The drawer is opened from the network view, and the panel renders only there.
scope.state.view = "network";
await settle();

/* ---------------- 1. dismissed before the first read lands ---------------- */

pairingOpen = false;
pairingDelay = 60;
const opening = scope.openPairingDrawer();
await settle();                     // the drawer is up, Pairing() is still out
await el("pairing-close").onclick();
await opening;
await settle();
if (openCalls.length !== 0) {
  failures.push(`a drawer dismissed before the node answered still opened a window (OpenPairing ${JSON.stringify(openCalls)})`);
}
pairingDelay = 0;

// And the ordinary case still opens one, so the check above is not passing
// because nothing ever does.
await scope.openPairingDrawer();
await settle();
if (openCalls.length !== 1 || openCalls[0] !== 0) {
  failures.push(`opening the drawer did not open a window with the node's default: ${JSON.stringify(openCalls)}`);
}
if (!el("pairing-detail").serialize().includes(ZH["pair.closeEnds"])) {
  failures.push("the drawer's status does not say that closing it ends pairing");
}

/* ---------------- 2. a request that arrived since the last poll ---------------- */

// The poll last saw nobody; the node has one waiting now.
scope.state.pairRequests = [];
requests = [{ id: "req_1", state: "pending", direction: "incoming" }];
const readsBefore = requestsReads;
await el("pairing-close").onclick();
await settle();
if (requestsReads === readsBefore) {
  failures.push("dismissing the drawer decided from the last poll without asking the node for its requests");
}
if (closeCalls.length !== 0) {
  failures.push("dismissing the drawer closed the window on a request that arrived after the last poll");
}

// A read that fails cannot say nobody is waiting, so the window is left to its
// own timeout.
await scope.openPairingDrawer();
await settle();
requests = [];
requestsFail = true;
await el("pairing-close").onclick();
await settle();
if (closeCalls.length !== 0) {
  failures.push("dismissing the drawer closed the window although the requests could not be read");
}
requestsFail = false;

// Nobody waiting: the window closes, which is what dismissing is for.
await scope.openPairingDrawer();
await settle();
scope.state.pairRequests = [{ id: "req_old", state: "pending" }]; // stale: resolved since
requests = [{ id: "req_old", state: "approved" }];
await el("pairing-close").onclick();
await settle();
if (closeCalls.length !== 1) {
  failures.push(`dismissing the drawer with nobody waiting called ClosePairing ${closeCalls.length} times, want 1`);
}

/* ---------------- 3. dismissed while OpenPairing is out (#194) ---------------- */

// The first read has landed and the window is being opened when the owner
// closes the drawer. dismissPairingDrawer runs then, sees no open window yet,
// and leaves; the window the call opens a moment later would stay open for the
// node's whole default duration with nothing on screen to close it.
pairingOpen = false;
openDelay = 60;
requests = [];
openCalls.length = 0;
closeCalls.length = 0;
{
  const opening = scope.openPairingDrawer();
  await settle();                   // Pairing() answered, OpenPairing is out
  if (openCalls.length !== 1) failures.push(`the drawer did not start opening a window: ${JSON.stringify(openCalls)}`);
  await el("pairing-close").onclick();
  if (closeCalls.length !== 0) failures.push("the dismissal closed a window that was not open yet");
  await opening;
  await settle();
  if (closeCalls.length !== 1) {
    failures.push(`a window opened after the drawer was dismissed was left open (ClosePairing ${closeCalls.length} times, want 1)`);
  }
  if (pairingOpen) failures.push("the node still holds a pairing window nobody is looking at");
}

// The dismissal's own rule still holds: somebody mid-exchange keeps it open.
pairingOpen = false;
closeCalls.length = 0;
requests = [{ id: "req_2", state: "awaiting-confirm", direction: "outgoing" }];
{
  const opening = scope.openPairingDrawer();
  await settle();
  await el("pairing-close").onclick();
  await opening;
  await settle();
  if (closeCalls.length !== 0) failures.push("the late dismissal closed the window on a request mid-exchange");
}

// And a drawer that is still open keeps the window it opened.
pairingOpen = false;
closeCalls.length = 0;
requests = [];
await scope.openPairingDrawer();
await settle();
if (closeCalls.length !== 0 || !pairingOpen) failures.push("an open drawer's window was closed by the in-flight check");
openDelay = 0;

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("pairing drawer: opens a window only while it is open, and closes one only when the node says nobody is waiting");
