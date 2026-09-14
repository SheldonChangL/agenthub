// The node settings form (#116), and the three rules that bite.
//
// 1. The node's answer repaints the WHOLE form. Turning allowLan off pulls
//    peerListen back to loopback in the same write, and since #134 any write
//    does it when the stored allowLan is already false — so a form that kept
//    its own idea of the fields it did not send would show a LAN address the
//    node no longer has.
// 2. `peerListenWithdrawn` absent means no withdrawal stands. It is omitempty
//    at the node, so nothing may be said unless the node says it.
// 3. A refused write leaves the form as the owner typed it and shows the
//    node's own sentence, which names the address and what to send with it.
//
// Also: the patch carries only what changed (a full write would make an
// untouched LAN peerListen part of a write that turns allowLan off, which the
// node refuses), and saving on a node that is not a service does not pretend
// to have restarted it.
//
//   node frontend/test/node-settings.mjs

import { document } from "./dom-shim.mjs";

globalThis.document = document;
globalThis.setInterval = () => 0;
const { configure, boot } = await import("../src/app.js");

const failures = [];
const el = (id) => document.getElementById(id);
const noop = async () => ({});

let settingsAnswer = async () => ({ settings: {}, sources: {}, saved: {} });
let saveAnswer = async () => ({ settings: {}, sources: {}, saved: {} });
let saveCalls = [];
let restartCalls = 0;
let serviceStatus = { supported: true, installed: true, running: true, pid: 1, unitPath: "/u", logHint: "/l", nodeAnswering: true };

configure({
  Overview: async () => ({ reachable: true, node: {}, sessions: [], nodes: [], peers: [], counts: {} }),
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop, SetNodeAddress: noop,
  Pairing: async () => ({ availability: "unknown", candidates: [] }), OpenPairing: noop, ClosePairing: noop,
  Inbox: noop, ClearInbox: noop, MCPConfig: noop, CopyText: noop, Outbound: noop, Wakes: noop,
  ServiceStatus: async () => serviceStatus,
  InstallService: noop, UninstallService: noop,
  LocalAddresses: async () => [
    { interface: "en0", address: "192.168.50.10", subnet: "192.168.50.0/24", private: true },
    { interface: "en5", address: "122.122.0.7", subnet: "122.122.0.0/16", private: false },
  ],
  NodeSettings: (...a) => settingsAnswer(...a),
  SaveNodeSettings: (...a) => { saveCalls.push(a[0]); return saveAnswer(...a); },
  RestartService: async () => { restartCalls += 1; return { command: "ah service restart", output: "restarted" }; },
});
const app = boot({ start: false });
app.state.service = serviceStatus;

const tick = () => new Promise((resolve) => setTimeout(resolve, 0));
// The form only offers a range when the owner changes the address; the tests
// below drive that moment directly, so they clear the remembered offer first.
const state_nodePrivateReset = () => { app.state.nodePrivateSuggested = ""; };

// 1. A read fills the form, and each field says where its value came from.
settingsAnswer = async () => ({
  settings: { peerListen: "192.168.50.10:7463", allowLan: true, discover: true, treatAsPrivate: ["192.168.50.0/24"], autoWake: false },
  sources: { peerListen: "remembered", allowLan: "remembered", discover: "flag", treatAsPrivate: "remembered", autoWake: "default" },
  saved: { peerListen: "192.168.50.10:7463", allowLan: true, discover: false, treatAsPrivate: ["192.168.50.0/24"], autoWake: false },
  restartRequired: false,
});
await app.loadNodeSettings();
await tick();
if (el("node-peerlisten").value !== "192.168.50.10:7463") {
  failures.push(`the address field shows ${el("node-peerlisten").value}, want the node's`);
}
// The form shows what the NEXT start will use. This fixture has discover
// pinned true by a command-line flag over a stored false, so the box is
// unticked and the tag says what is running instead — a form that showed the
// running value could never write the stored one.
if (!el("node-allow-lan").checked) failures.push("a saved true did not reach its box");
if (el("node-discover").checked) {
  failures.push("the box shows the running value, not the one a save would write");
}
if (!el("node-discover-source").textContent.includes("目前執行中的是 開")) {
  failures.push(`the running value was not named: ${el("node-discover-source").textContent}`);
}
if (el("node-private").value !== "192.168.50.0/24") failures.push(`ranges = ${el("node-private").value}`);
if (!el("node-discover-source").textContent.includes("命令列")) {
  failures.push(`a flag-sourced value is not marked as such: ${el("node-discover-source").textContent}`);
}
if (!el("node-peerlisten-source").textContent.includes("記住")) failures.push("a remembered value is not marked as such");
if (el("node-settings-notice").serialize().includes("收回")) failures.push("a withdrawal was announced when none stands");

// 2. Only what changed is sent, measured against what is saved. Ticking the
//    box that a flag is already forcing on is still a change to the stored
//    configuration, and has to reach the node — otherwise a value pinned by
//    this start-up can never be made permanent from this window.
el("node-discover").checked = true;
let patch = app.readNodeSettingsPatch();
if (Object.keys(patch).length !== 1 || patch.discover !== true) {
  failures.push(`patch = ${JSON.stringify(patch)}, want only discover:true`);
}
el("node-discover").checked = false;
if (Object.keys(app.readNodeSettingsPatch()).length !== 0) {
  failures.push("a box matching the saved value was still sent");
}

// 3. Clearing the ranges sends an empty array, not an omitted key: an omitted
//    key means "leave it alone", which would never withdraw anything.
el("node-private").value = "";
patch = app.readNodeSettingsPatch();
if (!Array.isArray(patch.treatAsPrivate) || patch.treatAsPrivate.length !== 0) {
  failures.push(`cleared ranges sent as ${JSON.stringify(patch.treatAsPrivate)}, want []`);
}

// 4. A write repaints the whole form, including a field nobody sent, and the
//    standing withdrawal is announced in the node's own words.
saveCalls = [];
restartCalls = 0;
el("node-private").value = "192.168.50.0/24";
el("node-allow-lan").checked = false;
saveAnswer = async () => ({
  settings: { peerListen: "127.0.0.1:7463", allowLan: false, discover: false, treatAsPrivate: ["192.168.50.0/24"], autoWake: false },
  sources: { peerListen: "default", allowLan: "remembered", discover: "remembered", treatAsPrivate: "remembered", autoWake: "default" },
  saved: { peerListen: "127.0.0.1:7463", allowLan: false, discover: false, treatAsPrivate: ["192.168.50.0/24"], autoWake: false },
  restartRequired: true,
  peerListenWithdrawn: true,
  message: "allowLan is off, so peerListen was pulled back to 127.0.0.1:7463",
});
await app.saveNodeSettings();
await tick();
if (saveCalls.length !== 1 || saveCalls[0].peerListen !== undefined) {
  failures.push(`the write sent ${JSON.stringify(saveCalls[0])}, want no peerListen (it was not touched)`);
}
if (el("node-peerlisten").value !== "") {
  failures.push(`the address field still shows ${el("node-peerlisten").value}; the node pulled it back to loopback`);
}
const notice = el("node-settings-notice").serialize();
if (!notice.includes("pulled back")) failures.push("the node's own sentence about the write was dropped");
if (!notice.includes("收回")) failures.push("a standing withdrawal was not announced");
if (!el("node-settings-hint").textContent.includes("重新啟動")) {
  failures.push("restartRequired did not reach the owner");
}
if (restartCalls !== 1) failures.push(`restarted ${restartCalls} times, want 1`);
if (!el("banner").textContent.includes("回應中")) {
  failures.push(`a restart that came back was not confirmed from the re-read status: ${el("banner").textContent}`);
}

// 4b. A node that does not come back is not announced as restarted. This is the
//     moment it is most likely to happen: the values just saved are what it
//     reads at start-up, and a service set to restart on failure crash-loops on
//     a combination it refuses.
serviceStatus = { supported: true, installed: true, running: false, pid: 0, unitPath: "/u", logHint: "/var/log/agenthub-node.log" };
app.state.service = serviceStatus;
el("node-autowake").checked = true;
saveAnswer = async () => ({ settings: { peerListen: "127.0.0.1:7463", allowLan: false, autoWake: true }, sources: {}, saved: { peerListen: "127.0.0.1:7463", allowLan: false, autoWake: true }, restartRequired: true });
await app.saveNodeSettings();
await tick();
const afterFailedRestart = el("banner").textContent;
if (afterFailedRestart.includes("回應中")) {
  failures.push("a node that never came back was announced as answering");
}
if (!afterFailedRestart.includes("沒有在執行")) {
  failures.push(`a stopped service was not reported: ${afterFailedRestart}`);
}
if (!afterFailedRestart.includes("/var/log/agenthub-node.log")) {
  failures.push("the log path was not offered when the node failed to come back");
}
if (el("banner").className.includes("ok")) {
  failures.push("a failed restart was marked successful, so it fades off screen");
}
serviceStatus = { supported: true, installed: true, running: true, pid: 1, unitPath: "/u", logHint: "/l", nodeAnswering: true };
app.state.service = serviceStatus;

// 5. A refusal keeps the node's words and leaves the form as typed.
el("node-allow-lan").checked = false;
el("node-peerlisten").value = "192.168.50.10:7463";
saveAnswer = async () => ({
  error: "allowLan is off, so peerListen has to be a loopback address, and it was sent as 192.168.50.10:7463; " +
    "to serve that address, send allowLan true in the same write",
});
restartCalls = 0;
await app.saveNodeSettings();
await tick();
const refused = el("node-settings-notice").serialize();
if (!refused.includes("to serve that address, send allowLan true in the same write")) {
  failures.push(`the refusal was not shown verbatim: ${refused}`);
}
if (el("node-peerlisten").value !== "192.168.50.10:7463") {
  failures.push("a refused write threw away what the owner had typed");
}
if (restartCalls !== 0) failures.push("a refused write restarted the service anyway");

// 6. Saving on a node that is not a service does not claim to have restarted it.
app.state.service = { supported: true, installed: false };
el("node-allow-lan").checked = true;
saveAnswer = async () => ({ settings: { peerListen: "192.168.50.10:7463", allowLan: true }, sources: {}, saved: { peerListen: "192.168.50.10:7463", allowLan: true }, restartRequired: true });
restartCalls = 0;
await app.saveNodeSettings();
await tick();
if (restartCalls !== 0) failures.push("a node that is not a service was 'restarted'");
if (!el("banner").textContent.includes("自己重新啟動")) {
  failures.push(`the owner was not told to restart it themselves: ${el("banner").textContent}`);
}

/* ---- what the fresh-context review of PR #144 found, each pinned here ---- */

// R1. allowLan is the owner's box. An earlier version ticked it whenever a LAN
//     address was selected, wired to the box's own onchange, so it could not be
//     unticked — which made the node's headline behaviour (turn it off and the
//     listener is withdrawn) unreachable from this window.
settingsAnswer = async () => ({
  settings: { peerListen: "192.168.50.10:7463", allowLan: true, discover: false, treatAsPrivate: [], autoWake: false },
  sources: {}, saved: { peerListen: "192.168.50.10:7463", allowLan: true, discover: false, treatAsPrivate: [], autoWake: false }, restartRequired: false,
});
app.state.nodeSettings = null;
await app.loadNodeSettings();
await tick();
el("node-allow-lan").checked = false;
el("node-allow-lan").onchange?.();
if (el("node-allow-lan").checked) {
  failures.push("unticking 允許區網連線 was undone by the form; the node's withdrawal can never be asked for");
}
let p1 = app.readNodeSettingsPatch();
if (p1.allowLan !== false) {
  failures.push(`after unticking, patch = ${JSON.stringify(p1)}, want allowLan:false`);
}
// And the form says what the node will do with it, before the owner finds out
// by losing the address.
if (!el("node-settings-combination").serialize().includes("收回本機")) {
  failures.push("turning allowLan off did not warn that the address will be withdrawn");
}

// R2. A loopback listener on a non-default port is not LAN. nodeconfig's rule
//     is the host, never the port: treating 127.0.0.1:9999 as LAN is how a form
//     opens a node to the network with a write nobody made.
for (const address of ["127.0.0.1:9999", "localhost:7463", "[::1]:7463", "127.2.3.4:1"]) {
  if (!app.isLoopbackListen(address)) failures.push(`${address} was not recognised as loopback`);
}
for (const address of ["192.168.50.10:7463", "10.0.0.5:7463", "122.122.0.7:7463"]) {
  if (app.isLoopbackListen(address)) failures.push(`${address} was treated as loopback`);
}
settingsAnswer = async () => ({
  settings: { peerListen: "127.0.0.1:9999", allowLan: false, discover: false, treatAsPrivate: [], autoWake: false },
  sources: {}, saved: { peerListen: "127.0.0.1:9999", allowLan: false, discover: false, treatAsPrivate: [], autoWake: false }, restartRequired: false,
});
app.state.nodeSettings = null;
await app.loadNodeSettings();
await tick();
if (el("node-allow-lan").checked) {
  failures.push("a loopback listener on a non-default port turned allowLan on");
}
el("node-autowake").checked = true;
app.syncNodeSettingsForm();
const p2 = app.readNodeSettingsPatch();
if (p2.allowLan !== undefined) {
  failures.push(`touching an unrelated field wrote allowLan: ${JSON.stringify(p2)}`);
}
if (el("node-peerlisten").value !== "127.0.0.1:9999") {
  failures.push(`the non-default loopback address was lost: ${el("node-peerlisten").value}`);
}

// R3. A failed read must not become the diff baseline. The form still shows the
//     last good values, so adopting an empty answer would make every field look
//     changed: the next save would write fields nobody touched and drop the
//     ones they did.
settingsAnswer = async () => ({
  settings: { peerListen: "127.0.0.1:7463", allowLan: false, discover: true, treatAsPrivate: [], autoWake: true },
  sources: {}, saved: { peerListen: "127.0.0.1:7463", allowLan: false, discover: true, treatAsPrivate: [], autoWake: true }, restartRequired: false,
});
app.state.nodeSettings = null;
await app.loadNodeSettings();
await tick();
const goodBaseline = app.state.nodeSettings;
settingsAnswer = async () => ({ error: "connection refused" });
await app.loadNodeSettings();
await tick();
if (app.state.nodeSettings !== goodBaseline) {
  failures.push("a failed read replaced the baseline the next save diffs against");
}
el("node-discover").checked = false;
el("node-autowake").checked = false;
const p3 = app.readNodeSettingsPatch();
if (p3.discover !== false || p3.autoWake !== false || Object.keys(p3).length !== 2) {
  failures.push(`after a failed read the patch is ${JSON.stringify(p3)}, want exactly the two boxes that were unticked`);
}
if (!el("node-settings-notice").serialize().includes("上次成功讀到")) {
  failures.push("a failed read did not say the values on screen may be stale");
}

// R4. A write that landed is restarted even if a newer read came back while it
//     was out. The node is holding settings it has not read; skipping the
//     restart leaves that true with nothing on screen saying so.
saveCalls = [];
restartCalls = 0;
serviceStatus = { supported: true, installed: true, running: true, pid: 1, unitPath: "/u", logHint: "/l", nodeAnswering: true };
app.state.service = serviceStatus;
app.state.nodeSettings = goodBaseline;
await app.applyNodeSettings(goodBaseline);
await tick();
el("node-discover").checked = false;
saveAnswer = async () => {
  // A reload lands while the write is in flight.
  await app.loadNodeSettings();
  return { settings: { peerListen: "127.0.0.1:7463", allowLan: false, discover: false, treatAsPrivate: [], autoWake: true },
    sources: {}, saved: { peerListen: "127.0.0.1:7463", allowLan: false, discover: false, treatAsPrivate: [], autoWake: true }, restartRequired: true };
};
settingsAnswer = async () => ({
  settings: { peerListen: "127.0.0.1:7463", allowLan: false, discover: true, treatAsPrivate: [], autoWake: true },
  sources: {}, saved: { peerListen: "127.0.0.1:7463", allowLan: false, discover: true, treatAsPrivate: [], autoWake: true }, restartRequired: false,
});
await app.saveNodeSettings();
await tick();
if (saveCalls.length !== 1) failures.push(`wrote ${saveCalls.length} times, want 1`);
if (restartCalls !== 1) {
  failures.push(`a write that landed was restarted ${restartCalls} times, want 1 — the node is holding unread settings`);
}
saveAnswer = async () => ({ settings: {}, sources: {}, saved: {}, restartRequired: true });

// R5. A value pinned by this start-up's command line names what the database
//     still holds, so an owner does not save an unrelated field and find the
//     address changed after a restart they thought was unrelated.
settingsAnswer = async () => ({
  settings: { peerListen: "192.168.1.10:7463", allowLan: true, discover: false, treatAsPrivate: [], autoWake: false },
  sources: { peerListen: "flag" },
  saved: { peerListen: "127.0.0.1:7463", allowLan: true, discover: false, treatAsPrivate: [], autoWake: false },
  restartRequired: false,
});
app.state.nodeSettings = null;
await app.loadNodeSettings();
await tick();
const tag = el("node-peerlisten-source").textContent;
if (!tag.includes("命令列")) failures.push(`a flag-sourced value is not marked: ${tag}`);
if (!tag.includes("192.168.1.10:7463")) {
  failures.push(`the address actually running was not named: ${tag}`);
}
// And the field itself holds the stored address, because that is what a save
// writes and what the next start uses.
if (el("node-peerlisten").value !== "") {
  failures.push(`the field shows ${el("node-peerlisten").value}, want the stored loopback`);
}

// R6/R7. The address list is filled before the form is explained, and a failure
//     to read this machine's addresses is said out loud rather than leaving the
//     owner with only 「只在本機」 and no reason.
let addressesThrow = false;
const realAddresses = [
  { interface: "en5", address: "122.122.0.7", subnet: "122.122.0.0/16", private: false },
];
configure({
  ...{
    Overview: async () => ({ reachable: true, node: {}, sessions: [], nodes: [], peers: [], counts: {} }),
    Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop, SetNodeAddress: noop,
    Pairing: async () => ({ availability: "unknown", candidates: [] }), OpenPairing: noop, ClosePairing: noop,
    Inbox: noop, ClearInbox: noop, MCPConfig: noop, CopyText: noop, Outbound: noop, Wakes: noop,
    ServiceStatus: async () => serviceStatus, InstallService: noop, UninstallService: noop,
    NodeSettings: (...a) => settingsAnswer(...a),
    SaveNodeSettings: (...a) => { saveCalls.push(a[0]); return saveAnswer(...a); },
    RestartService: async () => { restartCalls += 1; return { command: "ah service restart", output: "restarted" }; },
  },
  LocalAddresses: async () => {
    if (addressesThrow) throw new Error("no route to host");
    return realAddresses;
  },
});
settingsAnswer = async () => ({
  settings: { peerListen: "122.122.0.7:7463", allowLan: true, discover: false, treatAsPrivate: [], autoWake: false },
  sources: {}, saved: { peerListen: "122.122.0.7:7463", allowLan: true, discover: false, treatAsPrivate: [], autoWake: false }, restartRequired: false,
});
app.state.nodeSettings = null;
await app.loadNodeSettings();
await tick();
// A repaint never edits the ranges the node holds — they are the owner's. What
// the form owes them is the warning that this address will be refused without
// one, and the subnet to use.
if (el("node-private").value.trim() !== "") {
  failures.push(`a repaint wrote into the ranges field: ${el("node-private").value}`);
}
const publicWarn = el("node-settings-combination").serialize();
if (!publicWarn.includes("不在私有網段") || !publicWarn.includes("122.122.0.0/16")) {
  failures.push(`a non-private address was not flagged with its subnet: ${publicWarn}`);
}
// Changing the address is the one moment a suggestion is offered, and only
// into a field the owner has not filled.
el("node-private").value = "";
state_nodePrivateReset();
app.suggestPrivateRange();
app.syncNodeSettingsForm();
if (el("node-private").value.trim() !== "122.122.0.0/16") {
  failures.push(`picking the address did not offer its subnet: ${el("node-private").value}`);
}
if (el("node-private-note").classList.contains("hidden")) {
  failures.push("the offered range was not explained");
}
// Clearing it stays cleared: that is how a declared range is withdrawn.
el("node-private").value = "";
el("node-private").oninput?.();
if (el("node-private").value.trim() !== "") {
  failures.push(`clearing the ranges field was undone: ${el("node-private").value}`);
}
// And a range offered for one address does not outlive it.
el("node-private").value = "";
state_nodePrivateReset();
app.suggestPrivateRange();
const offered = el("node-private").value.trim();
el("node-peerlisten").value = "";
app.suggestPrivateRange();
app.syncNodeSettingsForm();
if (offered !== "122.122.0.0/16") failures.push("the suggestion was not offered before switching away");
if (el("node-private").value.trim() !== "") {
  failures.push(`a public range outlived the address that justified it: ${el("node-private").value}`);
}
addressesThrow = true;
app.state.nodeSettings = null;
await app.loadNodeSettings();
await tick();
const failedList = el("node-settings-notice").serialize();
if (!failedList.includes("no route to host")) {
  failures.push(`a failure to read local addresses was swallowed: ${failedList}`);
}

// R8. A save is refused outright while no good read stands: without a baseline
//     there is no honest answer to "what changed".
app.state.nodeSettings = null;
saveCalls = [];
await app.saveNodeSettings();
if (saveCalls.length !== 0) failures.push("a save went out with no baseline to diff against");
if (!el("banner").textContent.includes("重新讀取")) {
  failures.push(`the owner was not told to reload first: ${el("banner").textContent}`);
}

// R9. Two overlapping reads must not interleave. Painting used to await in the
//     middle of itself, so the baseline came from one read and the form from
//     the other, and the next save wrote fields nobody touched.
let slowFirst = true;
configure({
  Overview: async () => ({ reachable: true, node: {}, sessions: [], nodes: [], peers: [], counts: {} }),
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop, SetNodeAddress: noop,
  Pairing: async () => ({ availability: "unknown", candidates: [] }), OpenPairing: noop, ClosePairing: noop,
  Inbox: noop, ClearInbox: noop, MCPConfig: noop, CopyText: noop, Outbound: noop, Wakes: noop,
  ServiceStatus: async () => serviceStatus, InstallService: noop, UninstallService: noop,
  NodeSettings: (...a) => settingsAnswer(...a),
  SaveNodeSettings: (...a) => { saveCalls.push(a[0]); return saveAnswer(...a); },
  RestartService: async () => { restartCalls += 1; return { command: "ah service restart", output: "restarted" }; },
  LocalAddresses: async () => {
    // The first read's address lookup is the slow one, so its paint would land
    // last if anything were painted after an await.
    if (slowFirst) {
      slowFirst = false;
      await new Promise((resolve) => setTimeout(resolve, 30));
    }
    return [];
  },
});
let answers = [
  { settings: { peerListen: "127.0.0.1:7463", allowLan: false, discover: true, treatAsPrivate: [], autoWake: true }, sources: {}, saved: { peerListen: "127.0.0.1:7463", allowLan: false, discover: true, treatAsPrivate: [], autoWake: true }, restartRequired: false },
  { settings: { peerListen: "127.0.0.1:7463", allowLan: false, discover: false, treatAsPrivate: [], autoWake: false }, sources: {}, saved: { peerListen: "127.0.0.1:7463", allowLan: false, discover: false, treatAsPrivate: [], autoWake: false }, restartRequired: false },
];
settingsAnswer = async () => answers.shift() ?? answers[0];
app.state.nodeSettings = null;
const first = app.loadNodeSettings();
const second = app.loadNodeSettings();
await Promise.all([first, second]);
await tick();
// Whichever read won, the baseline and the form must be the same read.
const baseline = app.state.nodeSettings?.settings ?? {};
if (el("node-discover").checked !== Boolean(baseline.discover) ||
    el("node-autowake").checked !== Boolean(baseline.autoWake)) {
  failures.push("the form and the baseline came from different reads");
}
const idle = app.readNodeSettingsPatch();
if (Object.keys(idle).length !== 0) {
  failures.push(`with nothing touched the patch is ${JSON.stringify(idle)}, want empty`);
}

// R10. A successful save repaints from the node AND keeps the address list: a
//      repaint without it would leave the owner with only 「只在本機」.
configure({
  Overview: async () => ({ reachable: true, node: {}, sessions: [], nodes: [], peers: [], counts: {} }),
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop, SetNodeAddress: noop,
  Pairing: async () => ({ availability: "unknown", candidates: [] }), OpenPairing: noop, ClosePairing: noop,
  Inbox: noop, ClearInbox: noop, MCPConfig: noop, CopyText: noop, Outbound: noop, Wakes: noop,
  ServiceStatus: async () => serviceStatus, InstallService: noop, UninstallService: noop,
  NodeSettings: (...a) => settingsAnswer(...a),
  SaveNodeSettings: (...a) => { saveCalls.push(a[0]); return saveAnswer(...a); },
  RestartService: async () => { restartCalls += 1; return { command: "ah service restart", output: "restarted" }; },
  LocalAddresses: async () => [{ interface: "en0", address: "192.168.50.10", subnet: "192.168.50.0/24", private: true }],
});
settingsAnswer = async () => ({
  settings: { peerListen: "192.168.50.10:7463", allowLan: true, discover: false, treatAsPrivate: [], autoWake: false },
  sources: {}, saved: { peerListen: "192.168.50.10:7463", allowLan: true, discover: false, treatAsPrivate: [], autoWake: false }, restartRequired: false,
});
app.state.nodeSettings = null;
await app.loadNodeSettings();
await tick();
el("node-autowake").checked = true;
saveAnswer = async () => ({
  settings: { peerListen: "192.168.50.10:7463", allowLan: true, discover: false, treatAsPrivate: [], autoWake: true },
  sources: {}, saved: { peerListen: "192.168.50.10:7463", allowLan: true, discover: false, treatAsPrivate: [], autoWake: true }, restartRequired: true,
});
await app.saveNodeSettings();
await tick();
if (el("node-peerlisten").options.length < 2) {
  failures.push("a successful save emptied the address list, leaving only 只在本機");
}
if (el("node-peerlisten").value !== "192.168.50.10:7463") {
  failures.push(`a successful save lost the selected address: ${el("node-peerlisten").value}`);
}

/* ---- round three ---- */

// R11. The form edits the saved configuration, so a flag that pins something
//      over a different stored value cannot silently destroy it.
//
//      The node merges a write onto `saved` and judges it there. Started with
//      -allow-lan over a stored allowLan:false and a remembered LAN address,
//      a form painted from the running values would show the box ticked, send
//      an unrelated field, and the node would withdraw the address — with the
//      screen having promised nothing of the sort.
configure({
  Overview: async () => ({ reachable: true, node: {}, sessions: [], nodes: [], peers: [], counts: {} }),
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop, SetNodeAddress: noop,
  Pairing: async () => ({ availability: "unknown", candidates: [] }), OpenPairing: noop, ClosePairing: noop,
  Inbox: noop, ClearInbox: noop, MCPConfig: noop, CopyText: noop, Outbound: noop, Wakes: noop,
  ServiceStatus: async () => serviceStatus, InstallService: noop, UninstallService: noop,
  NodeSettings: (...a) => settingsAnswer(...a),
  SaveNodeSettings: (...a) => { saveCalls.push(a[0]); return saveAnswer(...a); },
  RestartService: async () => { restartCalls += 1; return { command: "ah service restart", output: "restarted" }; },
  LocalAddresses: async () => [{ interface: "en0", address: "192.168.1.10", subnet: "192.168.1.0/24", private: true }],
});
settingsAnswer = async () => ({
  // running: the flag opened it
  settings: { peerListen: "192.168.1.10:7463", allowLan: true, discover: false, treatAsPrivate: [], autoWake: false },
  sources: { allowLan: "flag", peerListen: "remembered" },
  // stored: allowLan is off, and the LAN address is remembered beside it
  saved: { peerListen: "192.168.1.10:7463", allowLan: false, discover: false, treatAsPrivate: [], autoWake: false },
  restartRequired: true,
});
app.state.nodeSettings = null;
await app.loadNodeSettings();
await tick();
if (el("node-allow-lan").checked) {
  failures.push("the box shows the flag's value, so a save would write a configuration nobody sees");
}
// The form must warn that saving anything withdraws the remembered address.
if (!el("node-settings-combination").serialize().includes("收回本機")) {
  failures.push("an unrelated save would withdraw the remembered address with no warning");
}
// And ticking the box has to reach the node: it differs from what is stored.
el("node-allow-lan").checked = true;
const r11 = app.readNodeSettingsPatch();
if (r11.allowLan !== true) {
  failures.push(`ticking allowLan over a stored false sent ${JSON.stringify(r11)}; a LAN listener is unreachable`);
}

// R12. The refusal prediction, which had no assertion at all.
//      Naming a non-loopback address while allowLan is off is refused by the
//      node; leaving the address alone is a withdrawal. The form must say which.
el("node-allow-lan").checked = false;
el("node-peerlisten").value = "192.168.1.10:7463";
app.syncNodeSettingsForm();
const withdrawal = el("node-settings-combination").serialize();
if (!withdrawal.includes("收回本機") || withdrawal.includes("會拒絕")) {
  failures.push(`leaving the address alone was not described as a withdrawal: ${withdrawal}`);
}
// Now name a different LAN address with allowLan still off.
el("node-peerlisten").value = "";
app.syncNodeSettingsForm();
const backToLoopback = el("node-settings-combination").serialize();
if (backToLoopback.includes("會拒絕")) {
  failures.push("moving to loopback with allowLan off was described as a refusal");
}

// R13. The address select's real handler offers the range; R6/R7 called the
//      function directly, so the wiring itself was untested and could be
//      removed without a failure.
configure({
  Overview: async () => ({ reachable: true, node: {}, sessions: [], nodes: [], peers: [], counts: {} }),
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop, SetNodeAddress: noop,
  Pairing: async () => ({ availability: "unknown", candidates: [] }), OpenPairing: noop, ClosePairing: noop,
  Inbox: noop, ClearInbox: noop, MCPConfig: noop, CopyText: noop, Outbound: noop, Wakes: noop,
  ServiceStatus: async () => serviceStatus, InstallService: noop, UninstallService: noop,
  NodeSettings: (...a) => settingsAnswer(...a),
  SaveNodeSettings: (...a) => { saveCalls.push(a[0]); return saveAnswer(...a); },
  RestartService: async () => { restartCalls += 1; return { command: "ah service restart", output: "restarted" }; },
  LocalAddresses: async () => [{ interface: "en5", address: "122.122.0.7", subnet: "122.122.0.0/16", private: false }],
});
settingsAnswer = async () => ({
  settings: { peerListen: "127.0.0.1:7463", allowLan: true, discover: false, treatAsPrivate: [], autoWake: false },
  sources: {},
  saved: { peerListen: "127.0.0.1:7463", allowLan: true, discover: false, treatAsPrivate: [], autoWake: false },
  restartRequired: false,
});
app.state.nodeSettings = null;
await app.loadNodeSettings();
await tick();
el("node-peerlisten").value = "122.122.0.7:7463";
el("node-peerlisten").onchange();
if (el("node-private").value.trim() !== "122.122.0.0/16") {
  failures.push(`the select's own handler did not offer the range: ${el("node-private").value}`);
}

// R14. A stored non-private address this machine no longer offers is still
//      judged: the interface list is a convenience, not the rule.
settingsAnswer = async () => ({
  settings: { peerListen: "203.0.113.9:7463", allowLan: true, discover: false, treatAsPrivate: [], autoWake: false },
  sources: {},
  saved: { peerListen: "203.0.113.9:7463", allowLan: true, discover: false, treatAsPrivate: [], autoWake: false },
  restartRequired: false,
});
configure({
  Overview: async () => ({ reachable: true, node: {}, sessions: [], nodes: [], peers: [], counts: {} }),
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop, SetNodeAddress: noop,
  Pairing: async () => ({ availability: "unknown", candidates: [] }), OpenPairing: noop, ClosePairing: noop,
  Inbox: noop, ClearInbox: noop, MCPConfig: noop, CopyText: noop, Outbound: noop, Wakes: noop,
  ServiceStatus: async () => serviceStatus, InstallService: noop, UninstallService: noop,
  NodeSettings: (...a) => settingsAnswer(...a),
  SaveNodeSettings: (...a) => { saveCalls.push(a[0]); return saveAnswer(...a); },
  RestartService: async () => { restartCalls += 1; return { command: "ah service restart", output: "restarted" }; },
  LocalAddresses: async () => [],
});
app.state.nodeSettings = null;
await app.loadNodeSettings();
await tick();
if (!el("node-settings-combination").serialize().includes("不在私有網段")) {
  failures.push("a stored public address with no interface entry was not flagged");
}
// Declaring a range that covers it clears the warning; one that does not, does not.
el("node-private").value = "203.0.113.0/24";
app.syncNodeSettingsForm();
if (el("node-settings-combination").serialize().includes("不在私有網段")) {
  failures.push("a declared range covering the address did not clear the warning");
}
el("node-private").value = "198.51.100.0/24";
app.syncNodeSettingsForm();
if (!el("node-settings-combination").serialize().includes("不在私有網段")) {
  failures.push("an unrelated declared range was accepted as covering the address");
}

// R15. Private-by-definition and the loopback spellings, against the node's rule.
for (const address of ["10.0.0.5:7463", "172.16.0.1:7463", "172.31.255.254:7463", "192.168.1.1:7463", "169.254.1.1:7463"]) {
  if (!app.isPrivateByDefinition(address)) failures.push(`${address} is private by definition and was not`);
}
for (const address of ["172.15.0.1:7463", "172.32.0.1:7463", "203.0.113.9:7463", "122.122.0.7:7463"]) {
  if (app.isPrivateByDefinition(address)) failures.push(`${address} is not private by definition and was`);
}
for (const address of ["[::ffff:7f00:1]:7463", "[::ffff:127.0.0.1]:7463", "[::1]:7463", "localhost:1", "127.5.5.5:9"]) {
  if (!app.isLoopbackListen(address)) failures.push(`${address} is loopback to the node and was not here`);
}
if (app.coversAddress("10.0.0.0/8", "10.1.2.3:7463") !== true) failures.push("a covering range was not recognised");
if (app.coversAddress("10.0.0.0/8", "11.1.2.3:7463") !== false) failures.push("a range that does not cover was accepted");
if (app.coversAddress("not-a-range", "10.1.2.3:7463") !== false) failures.push("a malformed range was accepted");

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("node settings: the answer repaints the whole form, withdrawals and refusals speak the node's words");
