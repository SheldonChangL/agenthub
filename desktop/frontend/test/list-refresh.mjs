// The main list keeps itself current, and a read that failed never empties it.
//
// Before issue #114 the session table and the node list loaded once at startup
// and then only when someone pressed 「重新整理」. Two things went wrong at once:
// a load that could not reach the node copied its emptiness into state, and
// nothing ever asked again. On 2026-09-10 a window sat showing 0 sessions and no
// paired node while the node was serving 1083 sessions and one peer, because the
// node happened to be rescanning at the moment the window started.
//
// Both halves are checked here by driving the real module — the wiring, its
// intervals and load() itself — rather than a renderer in isolation, since the
// defect lived in the wiring.
//
//   node frontend/test/list-refresh.mjs [path-to-main.js]

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

// Intervals are captured, not run: the test decides when a tick happens.
const ticks = [];
const fakeSetInterval = (fn, ms) => {
  ticks.push({ fn, ms });
  return ticks.length;
};

const session = (id) => ({
  id,
  provider: id.split(":")[0],
  status: "idle",
  management: "external",
  cwd: "/tmp",
  audience: { mode: "none" },
  lastSeenAt: new Date().toISOString(),
});

const reachableOverview = (ids, nodeIds) => ({
  node: { id: "node_local", displayName: "local", platform: "test", fingerprint: "AAAA" },
  sessions: ids.map(session),
  nodes: nodeIds.map((nodeId) => ({ nodeId, displayName: nodeId, trusted: true })),
  peers: [],
  counts: { total: ids.length },
  nodeUrl: "http://127.0.0.1:7462",
  reachable: true,
});

// What the node answers mid-rescan: unreachable, and empty for that reason
// rather than because there is nothing there.
const unreachableOverview = () => ({
  sessions: [],
  nodes: [],
  peers: [],
  counts: {},
  nodeUrl: "http://127.0.0.1:7462",
  reachable: false,
  error: "dial tcp 127.0.0.1:7462: connection refused",
});

let overviewAnswer = reachableOverview(["claude:one", "codex:two"], ["node_alice"]);
let overviewCalls = 0;
const Overview = async () => {
  overviewCalls++;
  return overviewAnswer;
};

const noop = async () => ({});
const Pairing = async () => ({ availability: "unknown", candidates: [] });

const scope = new Function(
  "document", "setInterval", "Overview", "Discover", "SetAudience", "TrustNode", "RevokeNode",
  "Heartbeat", "Pairing", "OpenPairing", "ClosePairing", "Inbox", "ClearInbox", "confirm",
  source + "\nreturn { state, load, render };"
)(document, fakeSetInterval, Overview, noop, noop, noop, noop, noop, Pairing, noop, noop,
  noop, noop, () => true);

const { state } = scope;
// replaceChildren takes a fragment, so the rows are one level down. Counted off
// the serialized table rather than the child list for that reason.
const drawnRows = () => (el("rows").serialize().match(/<tr/g) ?? []).length;
const settle = () => new Promise((resolve) => setTimeout(resolve, 30));

await settle();

// 1. The first load landed, so everything after this measures a change rather
//    than a module that never loaded anything.
if (state.sessions.length !== 2) {
  failures.push(`the first load left ${state.sessions.length} sessions, want 2`);
}
if (state.nodes.length !== 1) {
  failures.push(`the first load left ${state.nodes.length} nodes, want 1`);
}
if (drawnRows() !== 2) {
  failures.push(`the table drew ${drawnRows()} rows after the first load, want 2`);
}

// 2. A periodic refresh of the main list exists at all. Without it the window
//    is only ever as current as the last time someone pressed refresh.
const refresh = ticks.find((tick) => tick.ms === 15000);
if (!refresh) {
  failures.push(`no 15s main-list refresh was registered; intervals: ${ticks.map((t) => t.ms).join(", ")}`);
}

// 3. A refresh that cannot reach the node keeps the last lists it had.
//
//    This is the shape of the reported bug: the node is rescanning, the read
//    comes back unreachable and empty, and the window must not adopt that
//    emptiness as the truth.
if (refresh) {
  overviewAnswer = unreachableOverview();
  const before = overviewCalls;
  refresh.fn();
  await settle();
  if (overviewCalls === before) {
    failures.push("an idle refresh tick never asked the node");
  }
  if (state.sessions.length !== 2) {
    failures.push(`a failed read left ${state.sessions.length} sessions, want the previous 2`);
  }
  if (state.nodes.length !== 1) {
    failures.push(`a failed read left ${state.nodes.length} nodes, want the previous 1`);
  }
  if (drawnRows() !== 2) {
    failures.push(`a failed read blanked the table to ${drawnRows()} rows`);
  }
  if (!el("empty").classList.contains("hidden")) {
    failures.push('a failed read showed the "no sessions" placeholder over rows that are still there');
  }
  // Shown as unreachable, and said to be stale — a retained list presented as
  // current is its own bug.
  if (el("conn-dot").className !== "dot bad") {
    failures.push(`a failed read left the connection dot as ${el("conn-dot").className}`);
  }
  const bannerText = el("banner").textContent;
  if (el("banner").classList.contains("hidden")) {
    failures.push("a failed read showed no banner");
  }
  if (!bannerText.includes("節點未連線")) {
    failures.push(`the banner does not say the node is unreachable: ${bannerText}`);
  }
  if (!bannerText.includes("上次")) {
    failures.push(`the banner does not say the lists are the previous read's: ${bannerText}`);
  }
  if (!bannerText.includes("connection refused")) {
    failures.push(`the banner dropped the reason: ${bannerText}`);
  }
}

// 4. And a refresh that does reach the node replaces them — otherwise 3 would
//    pass over a window that has simply stopped updating.
if (refresh) {
  overviewAnswer = reachableOverview(["claude:three"], ["node_alice", "node_bob"]);
  refresh.fn();
  await settle();
  if (state.sessions.length !== 1 || state.sessions[0].id !== "claude:three") {
    failures.push(`a successful refresh did not adopt the new list: ${JSON.stringify(state.sessions.map((s) => s.id))}`);
  }
  if (state.nodes.length !== 2) {
    failures.push(`a successful refresh did not adopt the new node list (${state.nodes.length})`);
  }
  if (!el("banner").classList.contains("hidden")) {
    failures.push("the unreachable banner survived a successful refresh");
  }
}

// 5. The refresh stays out of the way of whatever the owner is doing. Each of
//    these would otherwise repaint the table under a click already in flight.
const skipped = (label, arrange, restore) => {
  if (!refresh) return;
  arrange();
  const before = overviewCalls;
  refresh.fn();
  restore();
  if (overviewCalls !== before) {
    failures.push(`the periodic refresh asked the node while ${label}`);
  }
};

skipped("a write was in flight", () => { state.busy = true; }, () => { state.busy = false; });
skipped("rows were selected",
  () => { state.selected.add("claude:three"); },
  () => { state.selected.clear(); });
for (const id of ["audience-modal", "pair-modal", "inbox-modal", "modal"]) {
  skipped(`#${id} was open`,
    () => el(id).classList.remove("hidden"),
    () => el(id).classList.add("hidden"));
}

// And with none of those true it does ask, so 5 is measuring the guards rather
// than a tick that never fires.
if (refresh) {
  const before = overviewCalls;
  overviewAnswer = reachableOverview(["claude:three"], ["node_alice"]);
  refresh.fn();
  await settle();
  if (overviewCalls === before) {
    failures.push("the periodic refresh never asks the node even when nothing is in the way");
  }
}

// 6. The selection cleanup a rescan needs still runs: a manual load() drops
//    selections for sessions that are gone, or the audience buttons stay armed
//    at IDs the node no longer has.
state.selected.add("claude:gone");
state.selected.add("claude:three");
overviewAnswer = reachableOverview(["claude:three"], ["node_alice"]);
await scope.load();
await settle();
if (state.selected.has("claude:gone")) {
  failures.push("a load kept a selection for a session that no longer exists");
}
if (!state.selected.has("claude:three")) {
  failures.push("a load dropped a selection for a session that is still there");
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("the main list refreshes itself and survives a failed read");
