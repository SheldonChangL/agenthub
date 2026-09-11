// Renders the records dialog — sent messages and the wake trail — through the
// real renderers, with hostile content in every field that came off the wire.
//
// This dialog exists for the two failures that were invisible from this window
// on 2026-09-10: a message queued for a peer with no address sits `pending`
// while `ah send` answers "queued", and a wake refused by a rate limit leaves
// nothing behind but a row only the CLI could read. Everything it shows —
// session labels, outcomes, the node's words for a failure — is a string this
// machine did not write, and a refusal must not read like a delivery.
//
//   node frontend/test/records.mjs [path-to-main.js]

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
const wiring = source.indexOf("/* ---------------- wiring ---------------- */");
if (wiring > 0) source = source.slice(0, wiring);

const noop = async () => ({});
// Both reads are recorded rather than stubbed away, so a click can be followed
// all the way to the call and the session filter is measured where it is sent.
const outboundCalls = [];
const wakeCalls = [];
let outboundAnswer = { messages: [] };
let wakeAnswer = { wakes: [] };
const outboundStub = async () => {
  outboundCalls.push(true);
  return outboundAnswer;
};
const wakeStub = async (sessionId) => {
  wakeCalls.push(sessionId);
  return wakeAnswer;
};

const scope = new Function(
  "document", "Overview", "Discover", "SetAudience", "TrustNode", "RevokeNode", "Heartbeat",
  "Pairing", "OpenPairing", "ClosePairing", "Inbox", "ClearInbox", "Outbound", "Wakes",
  source + "\nreturn { renderOutboundRecords, renderWakeRecords, openRecords, closeRecords, state };"
)(document, noop, noop, noop, noop, noop, noop, noop, noop, noop, noop, noop, outboundStub, wakeStub);

const { renderOutboundRecords, renderWakeRecords, openRecords, closeRecords, state } = scope;
const failures = [];
const el = (id) => document.getElementById(id);

// Only these class names may appear. A state or an outcome is a string from the
// node, and one interpolated into a class attribute is a sender choosing how
// the dialog looks — including how a refusal is coloured.
const ALLOWED = new Set([
  "recrow", "recline", "recstate pending", "recstate delivered", "recstate refused",
  "recstate unknown", "recerror", "muted", "stale", "empty", "claimed", "sender",
  "fingerprint", "mono",
]);

/* ---- 1. hostile rows are text, and the three states are told apart ---- */

renderOutboundRecords({
  messages: [
    {
      id: "msg_1",
      to: '<img src=x onerror="alert(1)">',
      from: "claude:mine",
      state: "pending",
      attempts: 0,
      createdAt: new Date().toISOString(),
    },
    {
      id: "msg_2",
      to: "codex:theirs",
      from: "claude:mine",
      state: "delivered",
      attempts: 1,
      createdAt: new Date().toISOString(),
    },
    {
      id: "msg_3",
      to: "codex:theirs",
      from: "claude:mine",
      state: "refused",
      attempts: 4,
      createdAt: new Date().toISOString(),
      lastError: '</div><script>window.go.main.App.RevokeNode("*")</script> nowhere to deliver to',
    },
    {
      // A state nobody has written yet, or a row from an older schema. It must
      // not choose its own class name.
      id: "msg_4",
      to: "codex:theirs",
      state: "exploded\" class=\"delivered",
      attempts: 0,
      createdAt: new Date().toISOString(),
    },
  ],
});
let rendered = el("records-body").serialize();

for (const marker of ["<script", "<img", "<iframe"]) {
  if (rendered.toLowerCase().includes(marker)) {
    failures.push(`an outbound row produced ${marker}`);
  }
}
for (const attribute of ['href="', 'src="', 'onerror="', 'onclick="']) {
  if (rendered.includes(attribute)) {
    failures.push(`the records dialog built a ${attribute} attribute, which node text could fill`);
  }
}
if (!rendered.includes("&lt;script&gt;")) {
  failures.push("a hostile last-error was not rendered as escaped text");
}
// And the failure is still readable, whole. Summarised, "nowhere to deliver to"
// and "the peer refused" become the same unhelpful sentence.
if (!rendered.includes("nowhere to deliver to")) {
  failures.push("the node's own words for the failure were dropped");
}
for (const cls of rendered.match(/class="[^"]*"/g) ?? []) {
  const value = cls.slice(7, -1);
  if (!ALLOWED.has(value)) {
    failures.push(`a node-supplied value reached a class name: ${cls}`);
  }
}
// The three states are distinguishable, and by more than their text.
for (const outboundState of ["pending", "delivered", "refused"]) {
  if (!rendered.includes(`class="recstate ${outboundState}"`)) {
    failures.push(`the ${outboundState} state carries no class of its own, so it reads like every other row`);
  }
}
if (!rendered.includes('class="recstate unknown"')) {
  failures.push("an unrecognised state was given a known state's colour");
}
if (!rendered.includes("被拒絕") || !rendered.includes("已送達") || !rendered.includes("等待送出")) {
  failures.push("a state has no words of its own; colour alone is not an answer");
}
if (!rendered.includes("4")) {
  failures.push("the attempt count is not shown, so a message failing repeatedly looks like a new one");
}

/* ---- 2. wake rows, refusals coloured and reasons kept ---- */

renderWakeRecords({
  wakes: [
    {
      id: "wake_1",
      messageId: "msg_9",
      sourceNodeId: "node_evil000000000000",
      sourceSession: 'codex:<img src=x onerror="alert(1)">',
      destinationSession: "claude:mine",
      hops: 2,
      outcome: "refused_pair_rate",
      detail: "3 in the last 10m0s, at the limit of 3",
      at: new Date().toISOString(),
    },
    {
      id: "wake_2",
      messageId: "msg_10",
      sourceNodeId: "node_evil000000000000",
      sourceSession: "codex:theirs",
      destinationSession: "claude:mine",
      hops: 1,
      outcome: "woken",
      at: new Date().toISOString(),
    },
  ],
});
rendered = el("records-body").serialize();

for (const marker of ["<script", "<img", "<iframe"]) {
  if (rendered.toLowerCase().includes(marker)) {
    failures.push(`a wake row produced ${marker}`);
  }
}
for (const cls of rendered.match(/class="[^"]*"/g) ?? []) {
  const value = cls.slice(7, -1);
  if (!ALLOWED.has(value)) {
    failures.push(`a node-supplied value reached a class name in the wake trail: ${cls}`);
  }
}
if (!rendered.includes('class="recstate refused"')) {
  failures.push("a refused wake is not coloured as a refusal");
}
if (!rendered.includes('class="recstate delivered"')) {
  failures.push("a wake that actually happened is not told apart from one that was refused");
}
if (!rendered.includes("at the limit of 3")) {
  failures.push("the reason a wake was refused was dropped, leaving a refusal with no cause");
}
// The source is split the way the inbox splits a sender: the node id was
// proven, the session label is what the sender called itself.
if (!rendered.includes('class="fingerprint">node_evil000000000000')) {
  failures.push("the wake's source node id is not marked as the proven half");
}
if (!rendered.includes("自稱")) {
  failures.push("the source session label is not marked as the sender's own");
}

/* ---- 3. the two empty lists say different things ---- */

renderOutboundRecords({ messages: [] });
rendered = el("records-body").serialize();
if (!rendered.includes("這台節點沒有送出過訊息")) {
  failures.push(`an empty outbound list says ${JSON.stringify(rendered)}`);
}

state.nodeAutoWake = false;
renderWakeRecords({ wakes: [] });
rendered = el("records-body").serialize();
if (!rendered.includes("沒有任何喚醒紀錄——代表節點很安靜，或 -auto-wake 沒開")) {
  failures.push(`an empty wake trail says ${JSON.stringify(rendered)}`);
}
// An empty trail on a node whose own switch is closed is not evidence of a
// quiet network, and this is the only place that can say so.
if (!rendered.includes("-auto-wake 是關的")) {
  failures.push("an empty trail on a node with -auto-wake off does not say the switch is off");
}
state.nodeAutoWake = true;
renderWakeRecords({ wakes: [] });
rendered = el("records-body").serialize();
if (!rendered.includes("-auto-wake 是開的")) {
  failures.push("an empty trail on a node with -auto-wake on reads as though the switch were off");
}

// A failed read is not an empty list: only one of them means there is nothing
// to come back for.
renderOutboundRecords({ error: "contact node: connection refused" });
rendered = el("records-body").serialize();
if (rendered.includes("這台節點沒有送出過訊息")) {
  failures.push("a failed read renders as an empty outbox, which is a claim the window cannot make");
}
if (!rendered.includes("connection refused")) {
  failures.push("a failed read does not say why, inside the dialog that covers the banner");
}

/* ---- 4. the session filter ---- */

outboundAnswer = {
  messages: [
    { id: "msg_a", to: "codex:theirs", from: "claude:mine", state: "pending", attempts: 0,
      createdAt: new Date().toISOString() },
    { id: "msg_b", to: "codex:theirs", from: "claude:someone-else", state: "pending", attempts: 0,
      createdAt: new Date().toISOString() },
  ],
};
await openRecords("outbound", "claude:mine");
rendered = el("records-body").serialize();
if (!rendered.includes("codex:theirs")) {
  failures.push("the scoped outbound view rendered nothing at all");
}
if (el("records-modal").classList.contains("hidden")) {
  failures.push("opening the records dialog left it hidden");
}
if (!el("records-scope").serialize().includes("claude:mine")) {
  failures.push("the dialog does not say which session it is scoped to");
}
if ((rendered.match(/recrow/g) ?? []).length !== 1) {
  failures.push("the session filter did not drop the other session's message: " +
    "a row from claude:someone-else under claude:mine's heading is the wrong answer");
}

wakeAnswer = { wakes: [] };
await openRecords("wakes", "claude:mine");
if (wakeCalls[wakeCalls.length - 1] !== "claude:mine") {
  failures.push(`the wake read was asked for ${JSON.stringify(wakeCalls[wakeCalls.length - 1])}, ` +
    "not the session the dialog is scoped to");
}
// And unscoped means unscoped, not an empty session id — which the node
// resolves as a session and refuses.
await openRecords("wakes", null);
if (wakeCalls[wakeCalls.length - 1] !== "") {
  failures.push(`an unscoped wake read asked for ${JSON.stringify(wakeCalls[wakeCalls.length - 1])}`);
}
if (el("records-scope").serialize().includes("claude:mine")) {
  failures.push("the dialog still claims a session scope after being reopened without one");
}

closeRecords();
if (!el("records-modal").classList.contains("hidden")) {
  failures.push("closing the dialog left it on screen");
}
if (state.recordsSession !== null) {
  failures.push("closing the dialog left a session scope behind for the next opening");
}

if (failures.length > 0) {
  console.error("records dialog check failed:");
  for (const failure of failures) console.error(" - " + failure);
  process.exit(1);
}
console.log("records dialog check passed");
