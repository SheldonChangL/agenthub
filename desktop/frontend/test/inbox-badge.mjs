// How many messages each session is still holding, on the row and on the tab.
//
// The redesign shipped without this badge because the only way to draw it was
// one GET /v1/inbox/{id} per row, and the list has held a thousand rows (issue
// #146). The node now answers all of them in one read, and what this check
// pins is the part that is easy to get wrong afterwards:
//
//   - a badge is a thing that ARRIVED. Zero is no badge, because a "0" on every
//     row hides the rows carrying something;
//   - and UNKNOWN is also no badge — but it must not be turned into zero. A
//     read that failed says nothing about any inbox, and the numbers it had
//     stay in state rather than being wiped;
//   - a full inbox is marked apart, because it is refusing new mail right now;
//   - the rows are updated, never rebuilt, so the fifteen-second tick cannot
//     swap out the button under a press (docs/ui-contract.md §3.1);
//   - and a session id written by nobody-knows-who never becomes markup.
//
//   node frontend/test/inbox-badge.mjs

import { document } from "./dom-shim.mjs";

globalThis.document = document;

const failures = [];
const el = (id) => document.getElementById(id);

const ticks = [];
globalThis.setInterval = (fn, ms) => {
  ticks.push({ fn, ms });
  return ticks.length;
};

const HOSTILE = 'claude:<img src=x onerror="alert(1)">';

// Fixed, distinct timestamps. The table sorts by last activity, so sessions
// stamped "now" can land in a different order on the next tick purely because
// the clock moved between two of them — and this check compares rows by
// identity, which such a reshuffle would break for a reason it is not about.
const session = (id, index) => ({
  id,
  provider: id.split(":")[0],
  status: "idle",
  management: "unmanaged",
  cwd: "/tmp",
  audience: { mode: "none" },
  lastSeenAt: new Date(Date.UTC(2026, 8, 18, 10, 0, index)).toISOString(),
});

const SESSION_IDS = ["claude:quiet", "claude:three", "codex:full", HOSTILE];

const overview = () => ({
  node: { id: "node_local", displayName: "local", platform: "test", fingerprint: "AAAA" },
  sessions: SESSION_IDS.map(session),
  nodes: [],
  peers: [],
  counts: { total: SESSION_IDS.length },
  nodeUrl: "http://127.0.0.1:7462",
  reachable: true,
});

// What the node answers: only the sessions holding something. claude:quiet is
// absent, which is how "nothing waiting" is said.
const goodCounts = {
  ok: true,
  counts: {
    "claude:three": { held: 3, capacity: 500, full: false },
    "codex:full": { held: 500, capacity: 500, full: true },
    [HOSTILE]: { held: 7, capacity: 500, full: false },
  },
};

let countsAnswer = () => goodCounts;
let countsCalls = 0;
const InboxCounts = async () => {
  countsCalls++;
  return countsAnswer();
};

const noop = async () => ({});
const { configure, boot } = await import("../src/app.js");
configure({
  Overview: async () => overview(),
  InboxCounts,
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop,
  Heartbeat: noop, Pairing: async () => ({ availability: "unknown", candidates: [] }),
  OpenPairing: noop, ClosePairing: noop, Inbox: noop, ClearInbox: noop,
});
const scope = boot();
const settle = () => new Promise((resolve) => setTimeout(resolve, 30));

// The table has no per-row id attribute, so a row is found by the session id
// its SESSION cell carries as a tooltip — never by position, because the list
// is sorted and this check is about which row a number landed on.
const rowNodes = () => el("rows").children;
const find = (node, predicate, found = []) => {
  if (!node || typeof node !== "object") return found;
  if (predicate(node)) found.push(node);
  for (const child of node.children ?? []) find(child, predicate, found);
  return found;
};
const hasClass = (name) => (node) => String(node.className ?? "").split(" ").includes(name);
const badgeIn = (row) => find(row, hasClass("inboxbadge"))[0];
const rowFor = (id) => {
  const row = [...rowNodes()].find((node) => find(node, (n) => n.title === id).length > 0);
  if (!row) throw new Error(`no row for ${id}; the table drew ${rowNodes().length}`);
  return row;
};

await settle();

if (rowNodes().length !== SESSION_IDS.length) {
  failures.push(`the table drew ${rowNodes().length} rows, want ${SESSION_IDS.length}; nothing below measures anything`);
}
if (countsCalls === 0) {
  failures.push("the first load never read the counts");
}

// 1. The badge appears only where something is held, and carries the number.
{
  const quiet = badgeIn(rowFor("claude:quiet"));
  if (!quiet) {
    failures.push("a row has no badge element at all, so it can never gain one");
  } else if (!quiet.classList.contains("hidden")) {
    failures.push(`a session holding nothing shows a badge reading ${JSON.stringify(quiet.textContent)}`);
  }

  // Neither zero nor unknown wears a badge, so the button's own title is the
  // only place the two can be told apart on screen — and they have to be.
  const quietButton = find(rowFor("claude:quiet"), hasClass("inbox"))[0];
  if (!quietButton.title.includes("沒有訊息在等")) {
    failures.push(`a session known to be holding nothing says ${JSON.stringify(quietButton.title)}`);
  }

  const three = badgeIn(rowFor("claude:three"));
  if (three.classList.contains("hidden")) {
    failures.push("a session holding three shows no badge");
  }
  if (three.textContent !== "3") {
    failures.push(`the badge reads ${JSON.stringify(three.textContent)}, want "3"`);
  }
  // A number, not a word. The owner asked for a count.
  if (!/^\d+$/.test(three.textContent)) {
    failures.push(`the badge is not a number: ${JSON.stringify(three.textContent)}`);
  }
}

// 2. A full inbox is marked apart. It is turning new messages away right now,
//    which is a different thing from merely holding a lot.
{
  const full = badgeIn(rowFor("codex:full"));
  if (full.classList.contains("hidden") || full.textContent !== "500") {
    failures.push(`the full session's badge is ${JSON.stringify(full.textContent)} (class ${full.className})`);
  }
  if (!full.classList.contains("full")) {
    failures.push(`a full inbox's badge carries no "full" class: ${full.className}`);
  }
  const notFull = badgeIn(rowFor("claude:three"));
  if (notFull.classList.contains("full")) {
    failures.push(`a session well under the bound is marked full: ${notFull.className}`);
  }
}

// 3. The tab and the window title carry the total, so something that arrived
//    while the owner was in another view — or another application — is visible
//    without going looking for it.
{
  const total = 3 + 500 + 7;
  const pill = el("tab-local-inbox");
  if (pill.classList.contains("hidden")) {
    failures.push("the 本機 session tab shows no total while three sessions are holding messages");
  }
  if (pill.textContent !== String(total)) {
    failures.push(`the tab total reads ${JSON.stringify(pill.textContent)}, want ${total}`);
  }
  if (document.title !== `(${total}) AgentHub`) {
    failures.push(`the window title is ${JSON.stringify(document.title)}, want "(${total}) AgentHub"`);
  }
}

// 4. A hostile session id never becomes markup. The id is the node's own, but
//    the provider wrote the part after the colon and this window renders it.
{
  const serialized = el("rows").serialize();
  if (serialized.includes("<img")) {
    failures.push("a session id reached the table as markup");
  }
  if (!serialized.includes("&lt;img")) {
    failures.push("the hostile id is not in the table at all, so the escaping above proves nothing");
  }
  const hostileBadge = badgeIn(rowFor(HOSTILE));
  if (hostileBadge.textContent !== "7") {
    failures.push(`the hostile row's badge reads ${JSON.stringify(hostileBadge.textContent)}, want "7"`);
  }
}

// 5. The tick updates the rows it already drew. A rebuilt row replaces the
//    button an owner is halfway through pressing, which is the rule the
//    candidate list was fixed for and this table is the busiest one.
const refresh = ticks.find((tick) => tick.ms === 15000);
if (!refresh) {
  failures.push(`no 15s refresh was registered; intervals: ${ticks.map((tick) => tick.ms).join(", ")}`);
} else {
  const before = [...rowNodes()];
  const badgesBefore = before.map(badgeIn);
  const buttonsBefore = before.map((row) => find(row, hasClass("inbox"))[0]);
  refresh.fn();
  await settle();
  const after = [...rowNodes()];
  if (after.length !== before.length || after.some((row, i) => row !== before[i])) {
    failures.push("a tick with unchanged counts rebuilt the table rows instead of updating them");
  }
  if (after.some((row, i) => badgeIn(row) !== badgesBefore[i])) {
    failures.push("a tick replaced the badge elements");
  }
  if (after.some((row, i) => find(row, hasClass("inbox"))[0] !== buttonsBefore[i])) {
    failures.push("a tick replaced the inbox buttons, so a press in flight is lost");
  }
  // And the numbers are still right after that update, or 5 would be passing
  // over a renderer that stopped writing anything.
  if (badgeIn(rowFor("claude:three")).textContent !== "3") {
    failures.push("after a tick the badge no longer carries the count");
  }
}

// 6. A count that changed is written into the row that is already there.
if (refresh) {
  const row = rowFor("claude:three");
  const badge = badgeIn(row);
  countsAnswer = () => ({
    ok: true,
    counts: { ...goodCounts.counts, "claude:three": { held: 9, capacity: 500, full: false } },
  });
  refresh.fn();
  await settle();
  if (rowFor("claude:three") !== row || badgeIn(rowFor("claude:three")) !== badge) {
    failures.push("a changed count replaced the row rather than writing into it");
  }
  if (badge.textContent !== "9") {
    failures.push(`a changed count left the badge reading ${JSON.stringify(badge.textContent)}, want "9"`);
  }
}

// 7. THE RULE. A read that failed is not a count of zero.
//
//    The node omits a session holding nothing, so an empty map is what "every
//    inbox is empty" looks like — and it is also what a rejected read leaves
//    behind. Treating the second as the first paints a zero this window
//    invented, over inboxes it could not see. Badges go away; the numbers stay.
if (refresh) {
  const beforeRows = [...rowNodes()];
  const knownBefore = { ...scope.state.inboxCounts.counts };

  countsAnswer = () => { throw new Error("dial tcp 127.0.0.1:7462: connection refused"); };
  refresh.fn();
  await settle();

  if (scope.state.inboxCounts.ok !== false) {
    failures.push("a failed counts read still reports the counts as good");
  }
  for (const id of ["claude:three", "codex:full", HOSTILE]) {
    const badge = badgeIn(rowFor(id));
    if (!badge.classList.contains("hidden")) {
      failures.push(`a failed counts read left ${id} wearing a badge of ${JSON.stringify(badge.textContent)}`);
    }
    if (badge.textContent === "0") {
      failures.push(`a failed counts read painted ${id} as zero; unknown and zero are different facts`);
    }
    // And the button does not claim the inbox is empty, which is the sentence
    // a known zero earns. This is where "unknown became zero" shows up: both
    // states are badgeless, so without this the two are the same on screen.
    const button = find(rowFor(id), hasClass("inbox"))[0];
    if (button.title.includes("沒有訊息在等")) {
      failures.push(`a failed counts read made ${id} say its inbox is empty: ${JSON.stringify(button.title)}`);
    }
  }
  if (!el("tab-local-inbox").classList.contains("hidden")) {
    failures.push(`a failed counts read left the tab total showing ${JSON.stringify(el("tab-local-inbox").textContent)}`);
  }
  if (document.title !== "AgentHub") {
    failures.push(`a failed counts read left the window title as ${JSON.stringify(document.title)}`);
  }
  // The numbers themselves are not thrown away — clearing them would be the
  // same invention in the state rather than on the screen.
  if (JSON.stringify(scope.state.inboxCounts.counts) !== JSON.stringify(knownBefore)) {
    failures.push("a failed counts read zeroed the numbers it had: "
      + JSON.stringify(scope.state.inboxCounts.counts));
  }
  // And it did not take the table apart while doing it.
  const afterRows = [...rowNodes()];
  if (afterRows.some((row, i) => row !== beforeRows[i])) {
    failures.push("a failed counts read rebuilt the rows");
  }

  // Once the read works again the badges come back, or everything above would
  // pass over a window that had simply stopped drawing them.
  countsAnswer = () => goodCounts;
  refresh.fn();
  await settle();
  if (badgeIn(rowFor("claude:three")).classList.contains("hidden")) {
    failures.push("the badges never came back after a successful read");
  }
  if (el("tab-local-inbox").classList.contains("hidden")) {
    failures.push("the tab total never came back after a successful read");
  }
}

// 8. A node that cannot be reached says nothing about any inbox either — the
//    session list is deliberately kept from the last good read (#114), and a
//    badge drawn beside a stale row would be this window asserting a number
//    nobody confirmed.
if (refresh) {
  const beforeCounts = { ...scope.state.inboxCounts.counts };
  configure({
    Overview: async () => ({
      sessions: [], nodes: [], peers: [], counts: {},
      nodeUrl: "http://127.0.0.1:7462", reachable: false,
      error: "dial tcp 127.0.0.1:7462: connection refused",
    }),
    InboxCounts, Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop,
    Heartbeat: noop, Pairing: async () => ({ availability: "unknown", candidates: [] }),
    OpenPairing: noop, ClosePairing: noop, Inbox: noop, ClearInbox: noop,
  });
  const asked = countsCalls;
  refresh.fn();
  await settle();
  if (countsCalls !== asked) {
    failures.push("an unreachable node was still asked for inbox counts");
  }
  if (scope.state.inboxCounts.ok !== false) {
    failures.push("an unreachable node left the counts reported as good");
  }
  if (!badgeIn(rowFor("claude:three")).classList.contains("hidden")) {
    failures.push("an unreachable node left the badges on the retained rows");
  }
  if (JSON.stringify(scope.state.inboxCounts.counts) !== JSON.stringify(beforeCounts)) {
    failures.push("an unreachable node zeroed the numbers the window had");
  }
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("inbox badges count what is held, mark a full inbox, survive a tick in place, and never turn unknown into zero");
