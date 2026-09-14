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
if (!el("node-allow-lan").checked || !el("node-discover").checked) failures.push("a true flag did not reach its box");
if (el("node-private").value !== "192.168.50.0/24") failures.push(`ranges = ${el("node-private").value}`);
if (!el("node-discover-source").textContent.includes("命令列")) {
  failures.push(`a flag-sourced value is not marked as such: ${el("node-discover-source").textContent}`);
}
if (!el("node-peerlisten-source").textContent.includes("記住")) failures.push("a remembered value is not marked as such");
if (el("node-settings-notice").serialize().includes("收回")) failures.push("a withdrawal was announced when none stands");

// 2. Only what changed is sent.
el("node-discover").checked = false;
let patch = app.readNodeSettingsPatch();
if (Object.keys(patch).length !== 1 || patch.discover !== false) {
  failures.push(`patch = ${JSON.stringify(patch)}, want only discover:false`);
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
saveAnswer = async () => ({ settings: { peerListen: "127.0.0.1:7463", allowLan: false, autoWake: true }, sources: {}, saved: {}, restartRequired: true });
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
saveAnswer = async () => ({ settings: { peerListen: "192.168.50.10:7463", allowLan: true }, sources: {}, saved: {}, restartRequired: true });
restartCalls = 0;
await app.saveNodeSettings();
await tick();
if (restartCalls !== 0) failures.push("a node that is not a service was 'restarted'");
if (!el("banner").textContent.includes("自己重新啟動")) {
  failures.push(`the owner was not told to restart it themselves: ${el("banner").textContent}`);
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("node settings: the answer repaints the whole form, withdrawals and refusals speak the node's words");
