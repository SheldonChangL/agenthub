// Renders a hostile pairing candidate through the real panel renderer and
// asserts that none of it becomes markup, that a claim is never shown as a
// fact, and that clicking a row cannot pre-confirm a fingerprint nobody
// compared.
//
// Candidate metadata is the least trustworthy input in the whole app. Peer
// session metadata at least arrives authenticated — a verified signature, an
// envelope naming this node. A candidate arrives on a multicast group anyone on
// the segment can write to, with no signature and no prior relationship: every
// field is whatever the sender typed, from whoever happens to be on the wifi.
//
//   node frontend/test/render-hostile-candidate.mjs [path-to-main.js]

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
const scope = new Function(
  "document", "Overview", "Discover", "SetAudience", "TrustNode", "RevokeNode", "Heartbeat",
  "Pairing", "OpenPairing", "ClosePairing",
  source + "\nreturn { renderPairing, candidateRow, prefillPairFrom, pairingRemaining, state };"
)(document, noop, noop, noop, noop, noop, noop, noop, noop, noop);

const { renderPairing, candidateRow, prefillPairFrom, state } = scope;
const failures = [];
const el = (id) => document.getElementById(id);

const hostile = {
  nodeId: 'node_evil<img src=x onerror="alert(1)">',
  address: '"><script>steal()</script>:7463',
  displayName: '<script>alert("name")</script>',
  platform: "</div><iframe src=javascript:alert(1)>",
  fingerprint: "AAAA BBBB CCCC DDDD EEEE FFFF",
  firstSeen: new Date().toISOString(),
  lastSeen: new Date().toISOString(),
  contested: true,
  duplicate: true,
};

// 1. None of it becomes markup, and all of it stays visible as escaped text.
const row = candidateRow(hostile).serialize();
// An unescaped tag is the failure. A literal such as `javascript:` surviving
// inside escaped text cannot execute — the same reasoning as
// render-hostile-peer.mjs — so what is checked is that no element was
// constructed from candidate data, and separately that no attribute was.
for (const marker of ["<script", "<img", "<iframe"]) {
  if (row.toLowerCase().includes(marker)) {
    failures.push(`candidate metadata produced ${marker} in the panel`);
  }
}
// Nothing in this panel builds an href, src or handler attribute, so any
// appearance of one is a new sink and candidate data is what would fill it.
//
// The trailing quote is what makes this precise rather than a substring match
// on the whole document: the serializer escapes every `"` inside text to
// `&quot;`, so a literal quote can only come from an attribute it actually
// emitted. Without it, hostile text reading `<iframe src=javascript:...>`
// matches its own escaped self.
for (const attribute of ['href="', 'src="', 'onerror="', 'onclick="']) {
  if (row.includes(attribute)) {
    failures.push(`the panel built a ${attribute} attribute, which candidate data could fill`);
  }
}
if (!row.includes("&lt;script&gt;alert(&quot;name&quot;)&lt;/script&gt;")) {
  failures.push("a hostile candidate display name was not rendered as escaped text");
}
if (!row.includes("&lt;/div&gt;&lt;iframe")) {
  failures.push("a hostile candidate platform was not rendered as escaped text");
}
if (!row.includes("&lt;script&gt;steal()&lt;/script&gt;")) {
  failures.push("a hostile candidate address was not rendered as escaped text");
}
// The full fingerprint has to be readable: comparing a prefix is what a forger
// can defeat, so a truncated one would be worse than none.
if (!row.includes("AAAA BBBB CCCC DDDD EEEE FFFF")) {
  failures.push("the candidate's full fingerprint is not shown");
}
// Nothing a sender chose may decide a class name.
for (const cls of row.match(/class="[^"]*"/g) ?? []) {
  if (!/^class="(candidaterow|line|name|meta|fingerprint|muted|pill bad|ghost)"$/.test(cls)) {
    failures.push(`a candidate-supplied value reached a class name: ${cls}`);
  }
}
// A contested or duplicated row is the shape an impersonation attempt has from
// this side, so it has to be flagged on the row itself.
if (!row.includes("身分有爭用") || !row.includes("名稱或指紋重複")) {
  failures.push("a contested or duplicate candidate was not flagged");
}

// `ah candidates` prints every field the node returns, and #61 asks the desktop
// list and `ah` to show the same thing. A field rendered in one and not the
// other is a difference the owner cannot see but would act on — the node id
// most of all, since that is what actually gets trusted.
const shown = candidateRow({ ...hostile, displayName: "laptop", platform: "darwin/arm64",
  address: "192.168.1.5:7463", nodeId: "node_1234567890abcdef1234567890abcdef" }).serialize();
for (const [field, value] of Object.entries({
  nodeId: "node_1234567890abcdef1234567890abcdef",
  address: "192.168.1.5:7463",
  displayName: "laptop",
  platform: "darwin/arm64",
  fingerprint: "AAAA BBBB CCCC DDDD EEEE FFFF",
})) {
  if (!shown.includes(value)) {
    failures.push(`the row does not show ${field}, which ah candidates prints`);
  }
}
// Both timestamps: "first seen" says how long it has been advertising and
// "last seen" whether it still is.
if (!/首次看到.*最後/.test(shown)) {
  failures.push("the row does not show both first and last seen");
}

// 2. An unnamed candidate is described as unnamed, not rendered as a blank row
//    that reads as a machine with no name.
const unnamed = candidateRow({ ...hostile, displayName: "   ", contested: false, duplicate: false })
  .serialize();
if (!unnamed.includes("（未提供名稱）")) {
  failures.push("a candidate that announced no name rendered as a blank row");
}

// 3. "Not looking", "could not read" and "nobody is advertising" are three
//    different facts. Collapsing any two tells the owner to keep waiting for
//    something that is not coming, or to change a setting that is not the
//    problem.
state.busy = false;
state.pairing = { availability: "off", candidates: [], error: "DISCOVERY_DISABLED: not listening" };
renderPairing();
const off = el("pairing-state").serialize() + el("candidate-rows").serialize();
if (off.includes("目前沒有看到任何機器在廣播")) {
  failures.push('a node that is not looking was rendered as "nobody is advertising"');
}
if (!off.includes("-discover")) {
  failures.push("a node started without -discover did not say so");
}
if (!el("btn-pairing-on").disabled) {
  failures.push("the open button is live on a node that cannot advertise");
}

state.pairing = { availability: "unknown", candidates: [], error: "connection refused" };
renderPairing();
const unknown = el("pairing-state").serialize() + el("pairing-note").serialize();
if (unknown.includes("-discover")) {
  failures.push("an unreachable node was blamed on a missing flag");
}
if (unknown.includes("目前沒有看到任何機器在廣播")) {
  failures.push("a failed read was rendered as a fact about the network");
}
if (!unknown.includes("connection refused")) {
  failures.push("the reason the read failed was not shown");
}

// 4. An open window on a machine with nothing to announce must say so. This is
//    the one failure an owner cannot see from the other machine: the panel says
//    open, and the other machine waits for a candidate that never arrives.
state.pairing = {
  availability: "on",
  state: {
    open: true, remainingSeconds: 240,
    announcing: { announceableAddresses: 0, lastError: "this node has no address a peer could reach" },
  },
  candidates: [],
};
state.pairingReadAt = Date.now();
renderPairing();
const silent = el("pairing-state").serialize();
if (!silent.includes("實際上什麼都沒有送出")) {
  failures.push("an open window that announces nothing was rendered as advertising");
}
if (!silent.includes("4:00")) {
  failures.push(`the countdown is missing or wrong: ${silent}`);
}

// 5. A full list is a condition an attacker can hold this node in, so the owner
//    has to learn their machine may be missing for that reason.
state.pairing = {
  availability: "on",
  state: { open: false, announcing: { announceableAddresses: 1 } },
  candidates: [hostile],
  full: true,
  notice: "nothing here has been verified",
};
renderPairing();
const full = el("candidate-rows").serialize() + el("candidate-notice").serialize();
if (!full.includes("候選清單已滿")) {
  failures.push("a full candidate list was not reported");
}
if (!full.includes("nothing here has been verified")) {
  failures.push("the node's own notice about the list was not shown");
}

// 6. Clicking a row must not pre-confirm anything.
//
// The pairing form's fingerprint field is the owner's statement that they
// compared the fingerprint on the other machine. Filling it from the
// announcement would turn an unverified claim into that statement without
// anybody having compared anything — and the public key is not announced at
// all, by design.
prefillPairFrom(hostile);
if (el("pair-fingerprint").value !== "") {
  failures.push("the announced fingerprint was written into the field that means 'I compared this'");
}
if (el("pair-public-key").value !== "") {
  failures.push("the pairing form's public key was filled from an announcement");
}
if (el("pair-node-id").value !== hostile.nodeId) {
  failures.push("the node id was not carried into the form");
}
const note = el("pair-prefill-note").serialize();
if (!note.includes("沒有經過任何驗證")) {
  failures.push("the prefill note does not say the values are unverified");
}
if (!note.includes("ah node")) {
  failures.push("the prefill note does not say where the public key has to come from");
}
if (el("pair-prefill-note").classList.contains("hidden")) {
  failures.push("the prefill note was left hidden");
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  console.error("\n--- row ---\n" + row);
  process.exit(1);
}
console.log("pairing panel renders hostile candidate metadata inertly, and grants nothing");
