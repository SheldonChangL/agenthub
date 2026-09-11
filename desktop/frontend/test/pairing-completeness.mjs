// The three things a first real pairing needed and this window did not have.
//
// On 2026-09-10 a developer paired two machines for the first time (issue #63).
// Every one of these cost them time, and none of them was visible from inside
// the app:
//
//   1. The public key could only be read by running `ah node` in a terminal on
//      the other machine. The node has always answered with it on /v1/node and
//      main.js never read the field — while the README said to look at "the
//      desktop's node line", which showed no such thing. Carried by hand, a
//      trailing "=" went missing, and the only message for that is "public key
//      is not a valid Ed25519 key".
//   2. Trust is recorded per machine. The mac was paired; `ah peers` on the
//      Ubuntu box still said `No paired nodes`, and nothing in the app said
//      the job was half done.
//   3. With no --discover broadcast the peer's address has to be recorded by
//      hand, and there was no button and no `ah` subcommand for it. A peer
//      without one is skipped in silence while `ah send` still answers
//      `queued`.
//
// Driven through the whole module — wiring and handlers — because two of the
// three are button paths, and the third is a string that has to survive a real
// render rather than exist in a source file.
//
//   node frontend/test/pairing-completeness.mjs [path-to-main.js]

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
const settle = () => new Promise((resolve) => setTimeout(resolve, 30));

// Intervals are captured rather than run: nothing here needs a tick, and a live
// one would re-render underneath an assertion.
const fakeSetInterval = () => 0;

const PUBLIC_KEY = "cS2bH1mQ9pR4tV7xZ0aD3fG6jK9nQ2sU5wY8bE1hL4o=";

const node = (nodeId, extra = {}) => ({
  nodeId,
  displayName: nodeId,
  platform: "linux/amd64",
  publicKey: "xxxx",
  fingerprint: "2DCF 9604 DBA9 778A 6DDD 035B",
  pairedAt: new Date().toISOString(),
  ...extra,
});

let overviewAnswer = {
  node: {
    id: "node_local000000000",
    displayName: "local",
    platform: "darwin/arm64",
    fingerprint: "AAAA BBBB CCCC",
    publicKey: PUBLIC_KEY,
  },
  sessions: [],
  nodes: [node("node_with0000000000", { address: "192.168.1.20:7463" }), node("node_without000000")],
  // A peer this node trusts that has never been heard from arrives as a row
  // with no receivedAt — not as a missing row.
  peers: [
    { nodeId: "node_with0000000000", displayName: "node_with0000000000", online: false, sessions: [] },
    { nodeId: "node_without000000", displayName: "node_without000000", online: false, sessions: [] },
  ],
  counts: {},
  nodeUrl: "http://127.0.0.1:7462",
  reachable: true,
};
const Overview = async () => overviewAnswer;

const copied = [];
let copyFails = false;
const CopyText = async (text) => {
  copied.push(text);
  if (copyFails) throw new Error("no clipboard on this display");
};

const addressCalls = [];
let addressFails = null;
const SetNodeAddress = async (nodeId, address) => {
  addressCalls.push([nodeId, address]);
  if (addressFails) throw new Error(addressFails);
};

const trusted = [];
const TrustNode = async (nodeId, displayName) => {
  trusted.push(nodeId);
  return { nodeId, displayName };
};

const noop = async () => ({});
const Pairing = async () => ({ availability: "unknown", candidates: [] });

const scope = new Function(
  "document", "setInterval", "confirm", "Overview", "Discover", "SetAudience", "TrustNode",
  "RevokeNode", "SetNodeAddress", "Heartbeat", "Pairing", "OpenPairing", "ClosePairing",
  "Inbox", "ClearInbox", "MCPConfig", "CopyText", "ServiceStatus", "InstallService",
  "UninstallService", "LocalAddresses",
  source + "\nreturn { state, load, render, nodeDetail, presenceLabel, nodeSessions };"
)(document, fakeSetInterval, () => true, Overview, noop, noop, TrustNode, noop, SetNodeAddress,
  noop, Pairing, noop, noop, noop, noop, noop, CopyText, noop, noop, noop, noop);

const { state, nodeDetail, presenceLabel, nodeSessions } = scope;

await settle();

if (state.nodes.length !== 2) {
  failures.push(`the first load left ${state.nodes.length} nodes, want 2; nothing below would mean anything`);
}

/* ---------------- 1. this machine's own public key ---------------- */

// The dialog is opened the way an owner opens it, so a handler that was never
// wired fails here rather than passing because the test called the function.
el("btn-pair").onclick();

if (el("pair-modal").classList.contains("hidden")) {
  failures.push("clicking 配對新節點… did not open the dialog");
}
if (el("local-public-key").textContent !== PUBLIC_KEY) {
  failures.push(`the dialog shows ${JSON.stringify(el("local-public-key").textContent)} as the local public key, want the node's`);
}
// The whole string, to the last character. A key shown truncated is a key
// retyped wrong, which is the failure this replaces.
if (!el("local-public-key").textContent.endsWith("=")) {
  failures.push("the local public key was rendered without its trailing '='");
}
if (el("local-fingerprint").textContent !== "AAAA BBBB CCCC") {
  failures.push(`the local fingerprint is ${JSON.stringify(el("local-fingerprint").textContent)}`);
}
// The two are different things and the dialog has to say which is which: the
// key is carried across and typed in, the fingerprint is compared on screens.
const dialog = fs.readFileSync(path.join(here, "..", "index.html"), "utf8");
if (!dialog.includes("對方在配對對話框裡要填的就是這串；指紋則要在兩台螢幕上逐組比對。")) {
  failures.push("the dialog does not say which of the two strings is typed in and which is compared");
}

// The copy button copies that key, and only that key.
await el("copy-local-public-key").onclick();
if (copied.length !== 1 || copied[0] !== PUBLIC_KEY) {
  failures.push(`the copy button wrote ${JSON.stringify(copied)} to the clipboard`);
}
if (!el("copy-public-key-status").textContent.includes("已複製")) {
  failures.push(`a successful copy was not reported: ${el("copy-public-key-status").textContent}`);
}

// A clipboard that refuses must say so. Reporting success over a failed copy
// sends the owner to the other machine to paste nothing.
copyFails = true;
await el("copy-local-public-key").onclick();
if (el("copy-public-key-status").textContent.includes("已複製")) {
  failures.push(`a failed copy was reported as a success: ${el("copy-public-key-status").textContent}`);
}
if (!el("copy-public-key-status").textContent.includes("手動")) {
  failures.push(`a failed copy did not tell the owner what to do instead: ${el("copy-public-key-status").textContent}`);
}
copyFails = false;

// Before any read reaches the node there is no key, and the dialog must not
// present an empty string as one.
const savedKey = state.localPublicKey;
state.localPublicKey = "";
el("btn-pair").onclick();
if (el("local-public-key").textContent === "") {
  failures.push("with no key read yet the dialog rendered an empty value rather than a placeholder");
}
await el("copy-local-public-key").onclick();
if (copied.length !== 2) {
  failures.push("the copy button wrote an empty string to the clipboard");
}
state.localPublicKey = savedKey;

/* ---------------- 2. pairing is recorded on each machine ---------------- */

el("pair-node-id").value = "node_remote00000000";
el("pair-display-name").value = "the ubuntu box";
el("pair-platform").value = "linux/amd64";
el("pair-public-key").value = "yyyy";
el("pair-fingerprint").value = "2DCF 9604";
await el("pair-submit").onclick();
await settle();

if (trusted.length !== 1) {
  failures.push(`pair-submit called TrustNode ${trusted.length} times, want 1`);
}
const bannerText = el("banner").textContent;
if (!bannerText.includes("對方那台也要")) {
  failures.push(`the success banner does not say the other machine must pair too: ${bannerText}`);
}
if (!bannerText.includes("心跳")) {
  failures.push(`the success banner does not say what happens until then: ${bannerText}`);
}
// Not the four-second self-dismissing kind: "you are half done" that vanishes
// is "you are half done" that was never read.
if (el("banner").className.includes("ok")) {
  failures.push("the pairing banner is the self-dismissing success kind, so the warning disappears on its own");
}

// The node's own page says it as well, since the banner is gone by the time
// anyone looks at the row.
const detail = document.createElement("div");
detail.replaceChildren(...nodeDetail(node("node_without000000")));
const detailHTML = detail.serialize();
if (!detailHTML.includes("對方那台也要對這台做一次配對")) {
  failures.push("the node detail page does not say the other machine must pair too");
}
if (!detailHTML.includes("ah nodes")) {
  failures.push("the node detail page does not point at where the other half is visible");
}

// A row that has never been heard from must not be presented as one fact when
// there are two possible causes and this side cannot choose between them.
const silent = presenceLabel({ online: false, sessions: [] });
if (silent.className !== "never") {
  failures.push(`a peer with no receivedAt was labelled ${silent.className}`);
}
if (silent.text === "尚未收到心跳") {
  failures.push("the row still says only 尚未收到心跳, which hides that the peer may not have paired back");
}
if (!silent.text.includes("配對")) {
  failures.push(`the row does not raise the not-paired-back possibility: ${silent.text}`);
}
// And it must stay a possibility, not a claim: this node genuinely cannot tell.
if (!silent.text.includes("可能")) {
  failures.push(`the row states a cause it cannot know: ${silent.text}`);
}

const silentPanel = document.createElement("div");
state.presenceError = "";
state.peers = [{ nodeId: "node_without000000", displayName: "x", online: false, sessions: [] }];
silentPanel.replaceChildren(...nodeSessions(node("node_without000000")));
const silentHTML = silentPanel.serialize();
for (const required of ["對方還沒對這台做配對", "還沒送出心跳", "ah nodes"]) {
  if (!silentHTML.includes(required)) {
    failures.push(`the silent-peer panel omits ${required}: ${silentHTML}`);
  }
}

/* ---------------- 3. the peer's address ---------------- */

function find(node, className, found = []) {
  if (!node || typeof node !== "object") return found;
  if ((node.className ?? "").split(" ").includes(className)) found.push(node);
  for (const child of node.children ?? []) find(child, className, found);
  return found;
}

// A node with no address gets the red warning, naming the silent skip and the
// `queued` that hides it.
const without = document.createElement("div");
without.replaceChildren(...nodeDetail(node("node_without000000")));
const withoutHTML = without.serialize();
if (find(without, "noaddress").length !== 1) {
  failures.push("a node with no recorded address carries no warning styled as one");
}
for (const required of ["靜默跳過", "queued", "--discover"]) {
  if (!withoutHTML.includes(required)) {
    failures.push(`the no-address warning omits ${required}`);
  }
}

// A node that has one shows it rather than warning.
const withAddress = document.createElement("div");
withAddress.replaceChildren(...nodeDetail(node("node_with0000000000", { address: "192.168.1.20:7463" })));
const withHTML = withAddress.serialize();
if (find(withAddress, "noaddress").length !== 0) {
  failures.push("a node with a recorded address was warned about not having one");
}
if (!withHTML.includes("192.168.1.20:7463")) {
  failures.push(`the recorded address is not shown: ${withHTML}`);
}

// The field is prefilled with what is recorded, so an edit is an edit rather
// than a retype.
const prefilled = find(withAddress, "addressinput")[0];
if (!prefilled || prefilled.value !== "192.168.1.20:7463") {
  failures.push(`the address field holds ${JSON.stringify(prefilled?.value)}, want the recorded address`);
}

// The button sends its own row's node id and the typed address.
const field = find(without, "addressinput")[0];
const button = find(without, "setaddress")[0];
if (!field || !button) {
  failures.push("the node detail page has no address field and button");
} else {
  field.value = "  10.0.0.7:7463  ";
  await button.onclick();
  await settle();
  if (addressCalls.length !== 1) {
    failures.push(`the button called SetNodeAddress ${addressCalls.length} times, want 1`);
  } else {
    const [calledNode, calledAddress] = addressCalls[0];
    if (calledNode !== "node_without000000") {
      failures.push(`the button recorded an address against ${calledNode}, not its own row`);
    }
    // Trimmed, because a trailing space typed into a text field is not part of
    // an address — and untrimmed it reaches the node as a refusal.
    if (calledAddress !== "10.0.0.7:7463") {
      failures.push(`the address sent was ${JSON.stringify(calledAddress)}`);
    }
  }

  // A half-typed address survives a re-render. This page is rebuilt from
  // scratch every fifteen seconds by the background refresh, which does not
  // count typing as an interaction in progress — so without this the address
  // being entered is deleted under the owner's hands mid-word.
  const typing = find(without, "addressinput")[0];
  typing.value = "192.168.1.2";
  typing.oninput({ target: typing });
  const redrawn = document.createElement("div");
  redrawn.replaceChildren(...nodeDetail(node("node_without000000")));
  const survived = find(redrawn, "addressinput")[0];
  if (!survived || survived.value !== "192.168.1.2") {
    failures.push(`a re-render replaced a half-typed address with ${JSON.stringify(survived?.value)}`);
  }
  // And it belongs to the node it was typed on, not to whichever page renders
  // next. Recording one machine's address against another is silent and wrong.
  const other = document.createElement("div");
  other.replaceChildren(...nodeDetail(node("node_with0000000000", { address: "192.168.1.20:7463" })));
  if (find(other, "addressinput")[0].value !== "192.168.1.20:7463") {
    failures.push("a draft typed on one node leaked into another node's field");
  }
  scope.state.addressDraft = null;

  // An empty field must not reach the node: the API reads an empty address as
  // "forget the one you had", which is the opposite of what the button says.
  field.value = "   ";
  await button.onclick();
  await settle();
  if (addressCalls.length !== 1) {
    failures.push(`an empty field was sent to the node as ${JSON.stringify(addressCalls[1])}`);
  }
  if (!el("banner").textContent.includes("host:port")) {
    failures.push(`an empty field gave no usable message: ${el("banner").textContent}`);
  }

  // What is wrong with an address is something only the node knows — it holds
  // the ranges this build will deliver to — so its words are what is shown.
  addressFails = "INVALID_REQUEST: address 8.8.8.8:7463 is outside the ranges this node delivers to";
  field.value = "8.8.8.8:7463";
  await button.onclick();
  await settle();
  if (!el("banner").textContent.includes("outside the ranges this node delivers to")) {
    failures.push(`the node's refusal did not reach the banner: ${el("banner").textContent}`);
  }
  addressFails = null;
}

// A peer's display name is chosen by the peer, so the detail page is untrusted
// output like every other renderer here.
const hostile = document.createElement("div");
hostile.replaceChildren(...nodeDetail(node("node_evil0000000000", {
  displayName: '<script>alert("name")</script>',
  address: '<img src=x onerror="alert(1)">',
})));
const hostileHTML = hostile.serialize();
for (const marker of ["<script", "<img", "<iframe", "javascript:"]) {
  if (hostileHTML.toLowerCase().includes(marker)) {
    failures.push(`node detail produced ${marker} from peer-chosen metadata`);
  }
}
if (!hostileHTML.includes("&lt;script&gt;alert(&quot;name&quot;)&lt;/script&gt;")) {
  failures.push("a hostile display name was not rendered as escaped text");
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("the pairing dialog shows this node's key, says pairing is two-sided, and records a peer address");
