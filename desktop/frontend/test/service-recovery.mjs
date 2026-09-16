// Getting out of a broken state without leaving the window.
//
// The states asserted here are the ones a real machine reached on 2026-09-16,
// in this order: a direct cable was unplugged, the node could not bind the
// address it remembered, and every route back to a working node ran through a
// terminal. The panel showed one unchanging line while the service manager
// restarted the node 107 times, the owner pressed the button that looked like
// the answer, and the reinstall it opened offered an empty database path —
// which registered the node onto a fresh database, gave the machine a new
// identity and dropped its pairing, silently.
//
// So: a degraded node has to be visible and repairable from here, a restart has
// to report what actually came back, and the database path must never be
// proposed as blank.
//
//   node frontend/test/service-recovery.mjs

import { document } from "./dom-shim.mjs";

globalThis.document = document;
globalThis.setInterval = () => 0;
const { configure, boot } = await import("../src/app.js");

const failures = [];
const el = (id) => document.getElementById(id);
const noop = async () => ({});
const text = (id) => el(id).textContent ?? "";

let installed = [];
let saved = [];
let restarts = 0;
let confirmed = true;
let confirmations = [];
globalThis.confirm = (message) => {
  confirmations.push(message);
  return confirmed;
};

// A node that is up, answering, and serving loopback because the address it was
// told to serve is not on this machine any more.
const degraded = {
  settings: { peerListen: "127.0.0.1:7463", allowLan: true, discover: true, treatAsPrivate: [], autoWake: false },
  sources: { peerListen: "default", allowLan: "remembered", discover: "remembered", treatAsPrivate: "remembered", autoWake: "remembered" },
  saved: { peerListen: "122.122.122.1:7463", allowLan: true, discover: true, treatAsPrivate: [], autoWake: false },
  restartRequired: true,
  peerListenProblem: {
    address: "122.122.122.1:7463",
    reason: "address_gone",
    detail: "listen tcp 122.122.122.1:7463: bind: can't assign requested address",
    runningOn: "127.0.0.1:7463",
    message: "no interface on this machine holds 122.122.122.1:7463 any more",
  },
};
const addresses = {
  list: [
    { interface: "en0", address: "192.168.161.1", subnet: "192.168.161.0/24", private: true },
    { interface: "en8", address: "122.122.122.1", subnet: "122.122.122.0/24", private: false },
  ],
  failure: "",
};

configure({
  Overview: async () => ({ reachable: true, node: {}, sessions: [], nodes: [], peers: [], counts: {} }),
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop, SetNodeAddress: noop,
  Pairing: async () => ({ availability: "unknown", candidates: [] }), OpenPairing: noop, ClosePairing: noop,
  Inbox: noop, ClearInbox: noop, MCPConfig: noop, CopyText: noop, Outbound: noop, Wakes: noop,
  ServiceStatus: async () => app.state.service ?? {},
  InstallService: async (form) => { installed.push(form); return { command: "ah service install", output: "registered" }; },
  UninstallService: noop,
  NodeSettings: async () => app.state.nodeSettingsAnswer ?? degraded,
  SaveNodeSettings: async (patch) => { saved.push(patch); return { ...degraded, ...(app.state.saveAnswer ?? {}) }; },
  LocalAddresses: async () => addresses.list,
  RestartNode: async () => { restarts += 1; return { command: "ah service restart", output: "restarted" }; },
});
const app = boot({ start: false });

// 1. A degraded node says so, and says it in terms of the machine rather than
//    of the listener: the address is gone, nobody can reach this node.
app.applyNodeSettings(degraded, addresses);
const notice = text("node-settings-notice");
if (!notice.includes("122.122.122.1:7463") || !notice.includes("只在本機")) {
  failures.push(`a degraded node was not explained: ${notice}`);
}
if (!notice.includes("can't assign requested address")) {
  failures.push("the system's own words were dropped; they are what an owner searches for");
}

// 2. And it is repairable from here. The repair offered is an address this
//    machine actually holds — not a diagnosis, not a command to go and type.
const repairs = app.peerListenRepairs(degraded.peerListenProblem, addresses, true);
const primary = repairs.find((repair) => repair.primary);
if (!primary || primary.peerListen !== "192.168.161.1:7463") {
  failures.push(`the repair offered is ${primary?.peerListen}, want the address this machine has`);
}
if (!primary.label.includes("en0") || !primary.label.includes("192.168.161.1")) {
  failures.push(`the button does not name the address it will use: ${primary?.label}`);
}
// A label promises only what clicking it changes. 允許區網連線 is already on
// here, so saying it would be a promise about nothing — and the owner notices
// that one, because nothing visibly happens.
if (primary.label.includes("允許區網連線")) {
  failures.push(`the button promises to turn on a switch that is already on: ${primary.label}`);
}
const offLabel = app.peerListenRepairs(degraded.peerListenProblem, addresses, false)
  .find((repair) => repair.primary).label;
if (!offLabel.includes("允許區網連線")) {
  failures.push(`a click that will turn the switch on did not say so: ${offLabel}`);
}
// The non-private address is not offered: taking it would need 視為私有網段
// filled in too, and a one-click repair that lands on a refusal is worse than
// no button at all.
if (repairs.some((repair) => repair.peerListen.startsWith("122.122.122.1"))) {
  failures.push("an address that needs a second decision was offered as one click");
}
// Staying off the network is a decision too, and without a way to say it the
// banner comes back every start until it is background noise.
if (!repairs.some((repair) => repair.peerListen === "")) {
  failures.push("no way to accept being local-only");
}

// 3. Pressing it goes through the ordinary save: same validation, same restart,
//    same re-read. A second path would be a second set of bugs.
saved = [];
app.state.nodeSettingsAnswer = { ...degraded, peerListenProblem: undefined, settings: { ...degraded.settings, peerListen: "192.168.161.1:7463" }, saved: { ...degraded.saved, peerListen: "192.168.161.1:7463" }, restartRequired: false };
await app.applyPeerListenRepair(primary);
if (saved.length !== 1 || saved[0].peerListen !== "192.168.161.1:7463") {
  failures.push(`the repair saved ${JSON.stringify(saved)}`);
}
// The patch carries only what changed: allowLan was already on, so sending it
// again would be this window writing a value nobody touched.
if ("allowLan" in saved[0]) {
  failures.push(`the repair re-sent a setting it did not change: ${JSON.stringify(saved[0])}`);
}

// 4. A restart reports what came back, not that it was asked for. `ah service
//    restart` answers when the service manager accepts the job; a node that
//    exits a second later used to leave "節點已重新啟動。" on screen as the only
//    thing anyone said about it.
app.state.service = { supported: true, installed: true, running: false, pid: 0, unitPath: "/u", logHint: "/log" };
app.state.nodeSettingsAnswer = degraded;
await app.restartNode();
if (!text("banner").includes("對外位址沒有綁起來")) {
  failures.push(`a node that came back degraded was reported as an ordinary restart: ${text("banner")}`);
}

// 5. A unit that pins node settings overrides this window on every start. The
//    panel has to say so — the node's log was the only place that did — and
//    offer the repair.
app.state.service = {
  supported: true, installed: true, running: true, pid: 7, unitPath: "/u", logHint: "/log",
  dbPath: "/Users/me/agenthub/data/agenthub.db", dbPathKnown: true,
  pinnedSettings: ["peer-listen", "allow-lan"],
};
app.renderService();
if (!text("service-repair").includes("peer-listen")) {
  failures.push(`a unit that pins settings was not reported: ${text("service-repair")}`);
}

// 6. And the repair keeps the database it is already using. A reinstall that
//    dropped it is what gave this machine a new identity.
installed = [];
confirmed = true;
await app.reinstallWithoutPinnedSettings(app.state.service);
if (installed.length !== 1 || installed[0].dbPath !== "/Users/me/agenthub/data/agenthub.db") {
  failures.push(`re-registering sent ${JSON.stringify(installed)}, want the database already in use`);
}

// 7. The install form never proposes a blank database path for an installed
//    service. Blank means the node's default, which is a different database and
//    therefore a different node.
await app.openServiceForm();
if (el("service-db").value !== "/Users/me/agenthub/data/agenthub.db") {
  failures.push(`the form offered "${el("service-db").value}" instead of the path in use`);
}
if (!text("service-db-note").includes("換一個節點身分")) {
  failures.push(`the form does not say what changing the path costs: ${text("service-db-note")}`);
}

// 8. Changing it is confirmed, in those terms, and a refusal installs nothing.
installed = [];
confirmations = [];
confirmed = false;
el("service-db").value = "/somewhere/else.db";
await app.installService();
if (installed.length !== 0) {
  failures.push("a refused confirmation installed anyway");
}
if (confirmations.length !== 1 || !confirmations[0].includes("重新配對")) {
  failures.push(`the confirmation does not name the cost: ${confirmations[0]}`);
}

// 9. Leaving it alone is not a change, so it is not confirmed: a dialog on every
//    reinstall is one nobody reads by the third time.
installed = [];
confirmations = [];
confirmed = true;
el("service-db").value = "/Users/me/agenthub/data/agenthub.db";
await app.installService();
if (confirmations.length !== 0) {
  failures.push("reinstalling with the same database asked for confirmation");
}
if (installed.length !== 1) failures.push("an unchanged reinstall did not install");

// 10. A first install, with no unit to read, still works: blank is the node's
//     default and the form says so rather than pretending to know.
app.state.service = { supported: true, installed: false, running: false, pid: 0, unitPath: "", logHint: "/log" };
app.renderService();
await app.openServiceForm();
if (el("service-db").value !== "") {
  failures.push(`a first install prefilled "${el("service-db").value}"`);
}
if (!text("service-db-note").includes("預設位置")) {
  failures.push(`a first install was not told what blank means: ${text("service-db-note")}`);
}
if (text("service-repair") !== "") {
  failures.push("a service that pins nothing was offered a repair");
}

// 11. The same panel, against what a real node actually answered.
//
// The fixture beside this file is the body of GET /v1/node/settings, captured
// from an agenthub-node started against an address this machine does not hold —
// not written by hand. Everything above uses a stub, and a stub is a copy of
// what this test's author believed the node sends: a field renamed on the Go
// side would leave all ten checks above green and the window blank.
const real = JSON.parse(
  await (await import("node:fs/promises")).readFile(new URL("./fixtures/degraded-node-settings.json", import.meta.url), "utf8"),
);
el("node-settings-notice").replaceChildren();
app.applyNodeSettings(real, addresses);
const realNotice = text("node-settings-notice");
if (!realNotice.includes(real.peerListenProblem.address)) {
  failures.push(`the real payload rendered no address: ${realNotice}`);
}
if (!realNotice.includes(real.peerListenProblem.runningOn)) {
  failures.push(`the real payload did not say where the node actually is: ${realNotice}`);
}
// And the form still shows the address the owner configured, not the loopback
// one the node fell back to: nothing was written, and offering the fallback as
// their setting would turn a cable being unplugged into a lost configuration.
if (el("node-peerlisten").value !== real.saved.peerListen) {
  failures.push(`the form shows ${el("node-peerlisten").value}, want the saved ${real.saved.peerListen}`);
}
if (app.peerListenRepairs(real.peerListenProblem, addresses, true).length < 2) {
  failures.push("the real payload produced no way out");
}

if (failures.length > 0) {
  for (const failure of failures) console.error(failure);
  process.exit(1);
}
console.log("service recovery: a degraded node is visible, repairable, and cannot lose its identity by accident");
