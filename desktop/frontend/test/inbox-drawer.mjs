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
import { answerConfirms } from "./fixtures/confirm-dialog.mjs";

globalThis.document = document;
globalThis.setInterval = () => 0;
answerConfirms(document, () => true);
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

// 5. The share panel opens over a session nobody can see as 「不分享」, every
//    time, whatever the last use left in the radios.
app.state.sessions = [{ id: "codex:unshared", provider: "codex", audience: { mode: "none", nodes: [] } }];
app.openSharePanel(["codex:unshared"]);
if (app.readSharePanel().who !== "none") failures.push(`a fresh panel reads who ${app.readSharePanel().who}, want none`);
app.closeSharePanel();
app.state.sessions = [];

// 6. A clear that succeeded is reported even when the re-read fails.
inboxAnswer = async (sessionId) => ({ sessionId, messages: [], error: "connection refused" });
await app.openInbox(MINE, { removed: 7 });
const body = el("inbox-body").serialize();
if (!body.includes("移除 7 則")) failures.push(`the clear result vanished behind the read error: ${body}`);
if (!body.includes("connection refused")) failures.push("the read error was not shown beside the clear result");

// 7. The drawer is titled by the session's title, with the id on the meta line;
//    a session without a title is titled by its id, and the id is not repeated.
app.state.sessions = [
  { id: "codex:titled", provider: "codex", title: "Release notes", audience: { mode: "none", nodes: [] } },
  { id: "codex:untitled", provider: "codex", audience: { mode: "none", nodes: [] } },
];
inboxAnswer = async (sessionId) => ({ sessionId, messages: [], held: 4, capacity: 500 });
await app.openInbox("codex:titled");
if (el("inbox-title").textContent !== "Release notes") failures.push(`a titled session opened the drawer as ${el("inbox-title").textContent}`);
if (!el("inbox-meta").textContent.includes("codex:titled")) failures.push(`the meta line lost the id: ${el("inbox-meta").textContent}`);
if (!el("inbox-meta").textContent.includes("4 / 500")) failures.push(`the meta line lost the count: ${el("inbox-meta").textContent}`);
await app.openInbox("codex:untitled");
if (el("inbox-title").textContent !== "codex:untitled") failures.push(`an untitled session opened the drawer as ${el("inbox-title").textContent}`);
if (el("inbox-meta").textContent.includes("codex:untitled")) failures.push(`the id is repeated under its own title: ${el("inbox-meta").textContent}`);
if (!el("inbox-meta").textContent.includes("4 / 500")) failures.push(`the untitled meta line lost the count: ${el("inbox-meta").textContent}`);
app.state.sessions = [];

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("inbox drawer: the session filter reaches the node on every page, absent limits stay absent");
