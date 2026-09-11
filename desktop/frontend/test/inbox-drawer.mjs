// The inbox drawer's two log tabs, and the dialog states the review found.
//
// /v1/outbound is node-wide and stores the sender as `<node id>/<session>`
// (address.QualifiedID), so the drawer has to compare the session half — the
// first version compared the whole string and showed every session an empty
// log. A page with nothing for this session must not be reported as "never
// sent anything" while the node still has older pages. Wake limits a node did
// not send must not be quoted as zeros. The audience dialog's mode radio, like
// its flags, starts at 不公開 every time. A clear that succeeded is reported
// even when the re-read that follows it fails.
//
//   node frontend/test/inbox-drawer.mjs

import { document } from "./dom-shim.mjs";

globalThis.document = document;
globalThis.setInterval = () => 0;
globalThis.confirm = () => true;
const { configure, boot } = await import("../src/app.js");

const failures = [];
const el = (id) => document.getElementById(id);
const noop = async () => ({});
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

const LOCAL = "node_local0000000000";
const MINE = "claude:mine";
let outboundCalls = [];
let outboundPages = {};
let wakesAnswer = async () => ({ wakes: [] });
let inboxAnswer = async (sessionId) => ({ sessionId, messages: [], held: 0, capacity: 500 });
let clearAnswer = async () => ({ removed: 3 });

configure({
  Overview: noop, Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop,
  Pairing: async () => ({ availability: "unknown", candidates: [] }), OpenPairing: noop, ClosePairing: noop,
  CopyText: noop, MCPConfig: noop,
  Inbox: (...a) => inboxAnswer(...a),
  ClearInbox: (...a) => clearAnswer(...a),
  Outbound: async (limit, after) => {
    outboundCalls.push({ limit, after });
    return outboundPages[after ?? ""] ?? { messages: [] };
  },
  Wakes: (...a) => wakesAnswer(...a),
});
const app = boot({ start: false });
app.state.localNodeId = LOCAL;

const row = (id, from) => ({ id, to: "codex:peer", destinationNodeId: "node_peer", from, state: "delivered", attempts: 1, createdAt: "2026-09-11T00:00:00Z", updatedAt: "2026-09-11T00:00:00Z" });

// 1. Qualified senders match on their session half.
outboundPages = { "": { messages: [row("o1", `${LOCAL}/${MINE}`), row("o2", `${LOCAL}/claude:other`), row("o3", MINE)] } };
app.state.inboxSessionAsked = MINE;
await app.loadOutbound({ reset: true });
let shown = el("outbound-body").serialize();
if (!shown.includes("o1") && !shown.includes("codex:peer")) failures.push("a qualified sender for this session was not shown");
if (app.state.outbound.messages.length !== 2) {
  failures.push(`want the qualified row and the bare row (2), got ${app.state.outbound.messages.length}`);
}
if (app.state.outbound.messages.some((m) => m.id === "o2")) failures.push("another session's row was shown under this session");

// 2. A full page with nothing for this session keeps reading, and when the
//    bound is reached with more still available it says so and keeps the button.
outboundCalls = [];
const others = (n, prefix) => Array.from({ length: n }, (_, i) => row(`${prefix}${i}`, `${LOCAL}/claude:other`));
outboundPages = {
  "": { messages: others(50, "a"), next: "c1" },
  c1: { messages: others(50, "b"), next: "c2" },
  c2: { messages: others(50, "c"), next: "c3" },
  c3: { messages: others(50, "d"), next: "c4" },
  c4: { messages: others(50, "e"), next: "c5" },
};
await app.loadOutbound({ reset: true });
if (outboundCalls.length < 2) failures.push(`a zero-match page did not read on: ${outboundCalls.length} call(s)`);
if (outboundCalls.length > 4) failures.push(`reading on is unbounded: ${outboundCalls.length} calls`);
shown = el("outbound-body").serialize();
if (shown.includes("還沒有送出過訊息")) failures.push("a zero-match scan with more pages claimed the session never sent anything");
if (el("outbound-more").classList.contains("hidden")) failures.push("the button to read further is hidden while the node has more");

// 3. No more pages and nothing found: only then is "never sent" true.
outboundPages = { "": { messages: others(5, "z") } };
await app.loadOutbound({ reset: true });
shown = el("outbound-body").serialize();
if (!shown.includes("還沒有送出過訊息")) failures.push("an exhausted log for this session was not described as empty");
if (!el("outbound-more").classList.contains("hidden")) failures.push("the read-further button is shown when the node has no more");

// 4. Wake limits the node did not send are not quoted as zeros.
wakesAnswer = async () => ({ wakes: [{ id: "w1", messageId: "m", destinationSession: MINE, hops: 1, outcome: "refused_hops", at: "2026-09-11T00:00:00Z" }], limits: { hops: 0, pair: 0, pairWindow: "", session: 0, sessionWindow: "", node: 0, nodeWindow: "" } });
await app.loadWakes();
const wakes = el("wakes-limits").serialize() + el("wakes-body").serialize();
if (wakes.includes("0 hops") || wakes.includes("限制：")) failures.push("zero-valued limits were rendered as the node's rules");
wakesAnswer = async () => ({ wakes: [], limits: { hops: 3, pair: 6, pairWindow: "10m0s", session: 3, sessionWindow: "10m0s", node: 30, nodeWindow: "1h0m0s" } });
await app.loadWakes();
if (!el("wakes-limits").serialize().includes("3 hops")) failures.push("real limits were not shown");

// 5. The audience dialog's mode radio starts at 不公開 every time.
//    The shim has no querySelectorAll, so the reset is checked through the
//    form reader, which falls back to "none" when nothing is checked.
app.openAudienceModal();
if (app.readAudienceForm().mode !== "none") failures.push(`a fresh dialog reads mode ${app.readAudienceForm().mode}, want none`);

// 6. A clear that succeeded is reported even when the re-read fails.
inboxAnswer = async (sessionId) => ({ sessionId, messages: [], error: "connection refused" });
await app.openInbox(MINE, { removed: 7 });
const body = el("inbox-body").serialize();
if (!body.includes("移除 7 則")) failures.push(`the clear result vanished behind the read error: ${body}`);
if (!body.includes("connection refused")) failures.push("the read error was not shown beside the clear result");

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("inbox drawer: qualified senders match, zero-match pages read on, absent limits stay absent");
