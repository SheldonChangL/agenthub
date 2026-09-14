// The inbox drawer's two log tabs, and the dialog states the review found.
//
// The node narrows /v1/outbound to one session (agenthub#132), so the drawer
// has to pass the session it was opened for — and pass it again on every
// continuation, or the second page is the node-wide list appended under this
// session's name. Wake limits a node did not send must not be quoted as zeros.
// The audience dialog's mode radio, like its flags, starts at 不公開 every
// time. A clear that succeeded is reported even when the re-read that follows
// it fails.
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
  Outbound: async (session, limit, after) => {
    outboundCalls.push({ session, limit, after });
    return outboundPages[after ?? ""] ?? { messages: [] };
  },
  Wakes: (...a) => wakesAnswer(...a),
});
const app = boot({ start: false });
app.state.localNodeId = LOCAL;

const row = (id) => ({ id, to: "codex:peer", destinationNodeId: "node_peer", from: `${LOCAL}/${MINE}`, state: "delivered", attempts: 1, createdAt: "2026-09-11T00:00:00Z", updatedAt: "2026-09-11T00:00:00Z" });

// 1. The session the drawer was opened for reaches the node, and the rows it
//    answers with are shown as they came: no second filter in here.
outboundPages = { "": { messages: [row("o1"), row("o2")] } };
app.state.inboxSessionAsked = MINE;
await app.loadOutbound({ reset: true });
if (outboundCalls.length !== 1 || outboundCalls[0].session !== MINE) {
  failures.push(`the node was asked ${JSON.stringify(outboundCalls)}, want one call carrying ${MINE}`);
}
if (app.state.outbound.messages.length !== 2) {
  failures.push(`want both rows the node answered with, got ${app.state.outbound.messages.length}`);
}

// 2. A continuation repeats the session alongside the cursor.
outboundCalls = [];
outboundPages = { "": { messages: [row("a")], next: "c1" }, c1: { messages: [row("b")] } };
await app.loadOutbound({ reset: true });
await app.loadOutbound();
if (outboundCalls.length !== 2) {
  failures.push(`want two reads, got ${outboundCalls.length}`);
} else if (outboundCalls[1].session !== MINE || outboundCalls[1].after !== "c1") {
  failures.push(`the continuation asked ${JSON.stringify(outboundCalls[1])}, want session ${MINE} and after c1`);
}
if (app.state.outbound.messages.length !== 2) {
  failures.push("a continuation did not append to what was already shown");
}

// 3. An empty answer is an answer now: the node filtered, so there is nothing
//    to read on for, and nothing to promise behind a button.
outboundCalls = [];
outboundPages = { "": { messages: [] } };
await app.loadOutbound({ reset: true });
if (outboundCalls.length !== 1) failures.push(`an empty page read on ${outboundCalls.length} times, want 1`);
let shown = el("outbound-body").serialize();
if (!shown.includes("還沒有送出過訊息")) failures.push("an empty log for this session was not described as empty");
if (!el("outbound-more").classList.contains("hidden")) failures.push("the read-further button is shown with nothing to read");

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
console.log("inbox drawer: the session filter reaches the node on every page, absent limits stay absent");
