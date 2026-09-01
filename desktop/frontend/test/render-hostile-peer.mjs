// Renders hostile peer metadata through the real network-view renderer and
// asserts that none of it becomes markup.
//
// Peer session metadata is strictly less trustworthy than local provider
// metadata: it arrives from another machine over the network. It is
// authenticated — the sender's signature is verified and the envelope must name
// this node — but authenticated is not the same as benign. A paired peer that
// has itself been compromised, or whose own provider files were tampered with,
// sends signed hostile strings.
//
//   node frontend/test/render-hostile-peer.mjs [path-to-main.js]

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
  source + "\nreturn { nodeSessions, presenceLabel, state };"
)(document, noop, noop, noop, noop, noop, noop);

const { nodeSessions, presenceLabel, state } = scope;

const hostileSession = {
  id: 'node_evil/claude:<img src=x onerror="alert(1)">',
  provider: '"><script>steal()</script>',
  status: "</td></tr><script>window.go.main.App.RevokeNode('*')</script>",
  visibility: "public",
  lastSeenAt: new Date().toISOString(),
};

const failures = [];

// 1. An online peer's sessions must render as inert text.
state.peers = [{
  nodeId: "node_evil000000000000",
  displayName: '<script>alert("name")</script>',
  online: true,
  receivedAt: new Date().toISOString(),
  sessions: [hostileSession],
}];
const container = document.getElementById("probe-online");
container.replaceChildren(...nodeSessions({
  nodeId: "node_evil000000000000",
  displayName: '<script>alert("name")</script>',
}));
const html = container.serialize();

// No element may be constructed from peer data. The table's own markup is
// legitimate, and literal characters such as `onerror=` surviving inside
// escaped text cannot execute, so neither is checked — the same reasoning as
// render-untrusted.mjs.
for (const marker of ["<script", "<img", "<iframe", "javascript:"]) {
  if (html.toLowerCase().includes(marker)) {
    failures.push(`peer metadata produced ${marker} in the network view`);
  }
}
// The values must still be visible to the owner, as escaped text. Without this
// the check would pass by rendering nothing at all.
if (!html.includes("&lt;img src=x onerror=&quot;alert(1)&quot;&gt;")) {
  failures.push("a hostile peer session id was not rendered as escaped text");
}
if (!html.includes("&lt;script&gt;steal()&lt;/script&gt;")) {
  failures.push("a hostile peer provider was not rendered as escaped text");
}
// An unrecognised status from a peer must not reach a class name.
const pillClass = /class="pill[^"]*"/.exec(html)?.[0] ?? "";
if (pillClass !== 'class="pill"') {
  failures.push(`a peer's status leaked into a class name: ${pillClass}`);
}
// A peer's display name is chosen by the peer, so it is untrusted too.
if (!html.includes("&lt;script&gt;alert(&quot;name&quot;)&lt;/script&gt;")) {
  failures.push("a hostile peer display name was not rendered as escaped text");
}

// 2. An offline peer must not render its last snapshot.
state.peers = [{
  nodeId: "node_quiet0000000000",
  displayName: "quiet",
  online: false,
  receivedAt: new Date(Date.now() - 3600_000).toISOString(),
  sessions: [hostileSession],
}];
const offline = document.getElementById("probe-offline");
offline.replaceChildren(...nodeSessions({ nodeId: "node_quiet0000000000", displayName: "quiet" }));
const offlineHTML = offline.serialize();
if (offlineHTML.includes("claude:")) {
  failures.push("an offline peer's stale sessions were rendered as current");
}
if (!offlineHTML.includes("離線")) {
  failures.push("an offline peer was not marked offline");
}

// 3. A paired peer that has never been heard from shows no sessions.
state.peers = [];
const never = document.getElementById("probe-never");
never.replaceChildren(...nodeSessions({ nodeId: "node_silent000000000", displayName: "silent" }));
const neverHTML = never.serialize();
if (neverHTML.includes("claude:")) {
  failures.push("a peer with no heartbeat showed sessions");
}

// 4. The three states must be distinguishable from each other.
if (offlineHTML === neverHTML) {
  failures.push('"offline" and "never heard from" render identically');
}

// 5. presenceLabel must never claim an offline peer is current.
const offlineLabel = presenceLabel({ online: false, receivedAt: new Date().toISOString() });
if (offlineLabel.className !== "offline") {
  failures.push(`offline peer labelled ${offlineLabel.className}`);
}
const neverLabel = presenceLabel(null);
if (neverLabel.className !== "never") {
  failures.push(`unheard peer labelled ${neverLabel.className}`);
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  console.error("\n--- online ---\n" + html);
  console.error("\n--- offline ---\n" + offlineHTML);
  process.exit(1);
}
console.log("network view renders hostile peer metadata inertly");
