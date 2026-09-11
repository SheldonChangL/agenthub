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
// When set, the next call parks instead of answering, so a read can be left in
// flight while a later one overtakes it.
let overviewPark = null;
const Overview = async () => {
  overviewCalls++;
  if (overviewPark) {
    const park = overviewPark;
    overviewPark = null;
    return new Promise((resolve) => park(resolve));
  }
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

// 7. A read that started earlier but answers later does not overwrite the newer
//    one. state.busy is only checked before a periodic read starts, so a read
//    already awaiting Overview() when the owner changes an audience or revokes
//    a node still comes back — after the mutation's own load() has landed, with
//    the node list and sessions from before the change.
{
  let release;
  overviewPark = (resolve) => { release = resolve; };
  const slow = scope.load();
  await settle();

  overviewAnswer = reachableOverview(["claude:newest"], ["node_alice", "node_bob"]);
  await scope.load();
  await settle();

  // The older read finally answers, describing the moment before the change.
  release(reachableOverview(["claude:stale"], ["node_alice"]));
  await slow;
  await settle();

  if (state.sessions.length !== 1 || state.sessions[0].id !== "claude:newest") {
    failures.push(`a late read overwrote the newer session list: ${JSON.stringify(state.sessions.map((s) => s.id))}`);
  }
  if (state.nodes.length !== 2) {
    failures.push(`a late read overwrote the newer node list (${state.nodes.length} nodes, want 2)`);
  }
  if (drawnRows() !== 1) {
    failures.push(`the table shows ${drawnRows()} rows after a late read, want the newer read's 1`);
  }
}

// And the same for the banner and the connection dot: a late read that could
// not reach the node must not report the window as disconnected when the newer
// read reached it.
{
  let release;
  overviewPark = (resolve) => { release = resolve; };
  const slow = scope.load();
  await settle();

  overviewAnswer = reachableOverview(["claude:newest"], ["node_alice", "node_bob"]);
  await scope.load();
  await settle();

  release(unreachableOverview());
  await slow;
  await settle();

  if (el("conn-dot").className !== "dot ok") {
    failures.push(`a late failed read set the connection dot to ${el("conn-dot").className} over a newer read that reached the node`);
  }
  if (!el("banner").classList.contains("hidden")) {
    failures.push(`a late failed read raised the unreachable banner over a newer successful read: ${el("banner").textContent}`);
  }
  if (state.sessions.length !== 1 || state.sessions[0].id !== "claude:newest") {
    failures.push(`a late failed read disturbed the newer session list: ${JSON.stringify(state.sessions.map((s) => s.id))}`);
  }
}

// 8. A background refresh that was already in flight when the owner started
//    interacting is thrown away rather than applied. The tick's guards only
//    speak for the moment the read was sent; by the time it answers the owner
//    may have selected rows or opened a dialog, and applying it then is the
//    same repaint-under-the-click the guards exist to prevent.
const droppedMidInteraction = async (label, arrange, restore) => {
  if (!refresh) return;

  // A known-good starting point, so what follows measures the background read
  // and not whatever the previous section left behind.
  state.selected.clear();
  overviewAnswer = reachableOverview(["claude:three"], ["node_alice"]);
  await scope.load();
  await settle();

  const sessionsBefore = state.sessions.map((s) => s.id).join(",");
  const nodesBefore = state.nodes.map((n) => n.nodeId).join(",");
  const rowsBefore = drawnRows();
  const bannerHiddenBefore = el("banner").classList.contains("hidden");
  const bannerTextBefore = el("banner").textContent;
  const dotBefore = el("conn-dot").className;

  // The tick fires with nothing in the way, and the read parks in flight.
  let release;
  overviewPark = (resolve) => { release = resolve; };
  const asked = overviewCalls;
  refresh.fn();
  await settle();
  if (overviewCalls === asked) {
    failures.push(`the background tick never asked the node before ${label}`);
  }

  // Only now does the owner start interacting.
  arrange();
  const selectedBefore = [...state.selected].join(",");

  // And only then does the read answer, describing a different world.
  release(reachableOverview(["claude:other"], ["node_alice", "node_bob"]));
  await settle();

  if (state.sessions.map((s) => s.id).join(",") !== sessionsBefore) {
    failures.push(`a background read landing while ${label} replaced the session list: ${JSON.stringify(state.sessions.map((s) => s.id))}`);
  }
  if (state.nodes.map((n) => n.nodeId).join(",") !== nodesBefore) {
    failures.push(`a background read landing while ${label} replaced the node list: ${JSON.stringify(state.nodes.map((n) => n.nodeId))}`);
  }
  if ([...state.selected].join(",") !== selectedBefore) {
    failures.push(`a background read landing while ${label} changed the selection: ${JSON.stringify([...state.selected])}`);
  }
  if (drawnRows() !== rowsBefore) {
    failures.push(`a background read landing while ${label} redrew the table (${rowsBefore} rows -> ${drawnRows()})`);
  }
  if (el("banner").classList.contains("hidden") !== bannerHiddenBefore
    || el("banner").textContent !== bannerTextBefore) {
    failures.push(`a background read landing while ${label} moved the banner`);
  }
  if (el("conn-dot").className !== dotBefore) {
    failures.push(`a background read landing while ${label} changed the connection dot to ${el("conn-dot").className}`);
  }

  restore();
  state.selected.clear();

  // And the abandoned read did not poison the sequence. The next foreground
  // load started before that read was thrown away is not what matters — this
  // one starts after, carries a higher number, and must still apply; a read
  // that was dropped whole must not count as the newest one on screen.
  overviewAnswer = reachableOverview(["claude:after"], ["node_alice"]);
  await scope.load();
  await settle();
  if (state.sessions.length !== 1 || state.sessions[0].id !== "claude:after") {
    failures.push(`after a background read was dropped while ${label}, a foreground load no longer applies: ${JSON.stringify(state.sessions.map((s) => s.id))}`);
  }
  if (drawnRows() !== 1) {
    failures.push(`after a background read was dropped while ${label}, the table shows ${drawnRows()} rows, want 1`);
  }
};

await droppedMidInteraction("rows were selected",
  () => state.selected.add("claude:three"),
  () => state.selected.clear());
await droppedMidInteraction("the audience dialog was open",
  () => el("audience-modal").classList.remove("hidden"),
  () => el("audience-modal").classList.add("hidden"));

// 9. Dropping that read leaves the numbering alone, so a foreground read that
//    started before it still lands. The owner presses 「重新整理」, the tick
//    fires while that read is still in the air, the owner selects a row, and
//    the tick's answer is thrown away. If throwing it away had counted as
//    "applied", the refresh the owner actually asked for would come back
//    carrying a lower number and be discarded as stale — the window would sit
//    on the old list with no way to tell.
if (refresh) {
  state.selected.clear();
  overviewAnswer = reachableOverview(["claude:three"], ["node_alice"]);
  await scope.load();
  await settle();

  // The owner's own refresh, still in flight.
  let releaseManual;
  overviewPark = (resolve) => { releaseManual = resolve; };
  const manual = scope.load();
  await settle();

  // The tick fires behind it and parks too.
  let releaseTick;
  overviewPark = (resolve) => { releaseTick = resolve; };
  refresh.fn();
  await settle();

  // The owner selects a row, and the tick's read answers into that.
  state.selected.add("claude:three");
  releaseTick(reachableOverview(["claude:fromTick"], ["node_alice", "node_bob"]));
  await settle();
  if (state.sessions.some((s) => s.id === "claude:fromTick")) {
    failures.push("a background read landing while a row was selected was applied");
  }

  // The owner clicks away, and their own refresh finally answers.
  state.selected.clear();
  releaseManual(reachableOverview(["claude:fromManual"], ["node_alice"]));
  await manual;
  await settle();
  if (state.sessions.length !== 1 || state.sessions[0].id !== "claude:fromManual") {
    failures.push(`the owner's refresh was discarded after a later background read was dropped: ${JSON.stringify(state.sessions.map((s) => s.id))}`);
  }
  if (drawnRows() !== 1) {
    failures.push(`the table shows ${drawnRows()} rows after the owner's refresh landed, want 1`);
  }
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("the main list refreshes itself and survives a failed read");
