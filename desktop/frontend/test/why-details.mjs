// An opened 「說明」 survives the refreshes that run underneath it.
//
// #194 folded the explanations that used to be tooltips into <details>, and
// two of the places they landed were rebuilt on a timer: the pairing drawer's
// step 1 (the network view reloads the pairing state every five seconds) and
// the node detail pane (every render of the network view, the fifteen-second
// tick included). Rebuilt, a details the owner had opened folded shut and the
// keyboard on its summary fell to the page. What is checked here is the
// element itself: after a background redraw it is the same node, still open,
// with the keyboard still on its summary.
//
//   node frontend/test/why-details.mjs

import { document } from "./dom-shim.mjs";

globalThis.document = document;
globalThis.setInterval = () => 0;
globalThis.localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };

const failures = [];
const el = (id) => document.getElementById(id);
const noop = async () => ({});
const find = (node, tag, found = []) => {
  if (!node || typeof node !== "object") return found;
  if (node.tagName === tag) found.push(node);
  for (const child of node.children ?? []) find(child, tag, found);
  return found;
};

const NODE = {
  nodeId: "node_peer000000000000", displayName: "peer", platform: "linux/amd64",
  fingerprint: "AAAA-BBBB", pairedAt: "2026-09-01T00:00:00Z", lastSeenAt: "2026-09-23T00:00:00Z", address: "",
};
let displayName = NODE.displayName;
const SETTINGS = {
  settings: { peerListen: "127.0.0.1:7463", allowLan: false, discover: true, treatAsPrivate: [], autoWake: false },
  saved: { peerListen: "127.0.0.1:7463", allowLan: false, discover: true, treatAsPrivate: [], autoWake: false },
  sources: { peerListen: "default", allowLan: "default", discover: "default", treatAsPrivate: "default", autoWake: "default" },
  restartRequired: false,
};
const PAIRING = {
  availability: "available", windowAvailable: true, candidates: [],
  state: { open: false, peerAddress: "", peerAddressReachable: false },
};

const { configure, boot } = await import("../src/app.js");
configure({
  Overview: async () => ({
    reachable: true, nodeUrl: "http://127.0.0.1:7462",
    node: { id: "node_local", displayName: "local", platform: "darwin/arm64" },
    sessions: [], nodes: [{ ...NODE, displayName }], peers: [], counts: {},
  }),
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop, SetNodeAddress: noop,
  Pairing: async () => JSON.parse(JSON.stringify(PAIRING)),
  OpenPairing: noop, ClosePairing: noop, Inbox: noop, ClearInbox: noop, MCPConfig: noop, CopyText: noop,
  Outbound: noop, Wakes: noop, PairRequests: async () => [],
  NodeSettings: async () => JSON.parse(JSON.stringify(SETTINGS)),
  LocalAddresses: async () => [{ interface: "en0", address: "192.168.50.10", subnet: "192.168.50.0/24", private: true }],
  ServiceStatus: async () => ({ supported: true, installed: true, running: true }),
  HostPlatform: async () => "darwin",
  Version: async () => ({ release: "unreleased" }),
});
const app = boot({ start: false });
app.state.view = "network";
await app.load();
await app.loadNodeSettings();

// Open one, put the keyboard on its summary, run the redraw, and ask whether
// the owner's place survived it.
const survives = async (where, container, redraw, index = 0) => {
  const before = find(container(), "details");
  if (before.length <= index) {
    failures.push(`${where}: no 「說明」 to open`);
    return;
  }
  const details = before[index];
  const summary = find(details, "summary")[0];
  details.open = true;
  summary.focus();
  await redraw();
  const after = find(container(), "details");
  if (!after.includes(details)) failures.push(`${where}: the 「說明」 was replaced by a new element on a background redraw`);
  if (details.open !== true) failures.push(`${where}: the opened 「說明」 was folded by a background redraw`);
  if (document.activeElement !== summary) failures.push(`${where}: the keyboard left the 「說明」 summary on a background redraw`);
  details.open = false;
  summary.blur();
};

// 1. The pairing drawer's step 1, on the five-second pairing reload.
await app.loadPairing();
if (!el("pair-here-note").textContent.includes(app.PAIR_TEXT.hereNoAddressWhy)) {
  failures.push(`step 1 is not showing the unreachable remedy: ${el("pair-here-note").textContent}`);
}
await survives("pairing step 1", () => el("pair-here-note"), () => app.loadPairing());
// A redraw that has something new to say still says it.
PAIRING.state.peerAddress = "127.0.0.1:7463";
await app.loadPairing();
if (!el("pair-here-note").textContent.includes(app.PAIR_TEXT.hereUnreachable)) {
  failures.push(`step 1 kept the old sentence after the node's answer changed: ${el("pair-here-note").textContent}`);
}

// 2. The node detail pane, both of its whys, on the fifteen-second tick.
app.state.selectedNode = NODE.nodeId;
app.render();
const whys = find(el("node-detail-body"), "details");
if (whys.length !== 2) failures.push(`the node detail shows ${whys.length} 「說明」, want 2`);
for (const index of [0, 1]) {
  await survives(`node detail 「說明」 ${index + 1}`, () => el("node-detail-body"), async () => {
    displayName = `peer-${index}`;
    await app.load({ background: true });
  }, index);
  // The tick did land: the pane carries the name it brought.
  if (!find(el("node-detail-body"), "h2")[0]?.textContent.includes(`peer-${index}`)) {
    failures.push(`the background read did not reach the node detail: ${el("node-detail-body").textContent}`);
  }
}
// No node selected: the whys go with the pane.
app.state.selectedNode = null;
app.render();
if (find(el("node-detail-body"), "details").length !== 0) failures.push("with no node selected the detail still shows its whys");

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("why details: an opened 「說明」 keeps its element, its open state and the keyboard across background redraws");
