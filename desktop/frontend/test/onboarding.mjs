// The first-launch checklist: what it shows, when, and what its buttons press.
//
// This card is the first screen a stranger sees after the installer finishes,
// and every control it offers already exists somewhere else in the window. So
// what is checked here is not the controls but the joins: that a step is shown
// only when the window actually knows the thing it claims, that each button
// reaches the binding the panel's own button reaches and reaches it once, that
// the reachability button never turns on LAN access without saying so in its
// own label (docs/ui-contract.md §7.8 rule 4), and that the fifteen-second
// refresh does not replace the button an owner is halfway through clicking.
//
//   node frontend/test/onboarding.mjs

import { document } from "./dom-shim.mjs";
import { inEnglish } from "./fixtures/in-english.mjs";
import { TEXT as ZH } from "../src/i18n/zh-Hant.js";

globalThis.document = document;
globalThis.setInterval = () => 0;
globalThis.setTimeout = (fn) => { fn(); return 0; };
globalThis.clearTimeout = () => {};

// A localStorage the dismissal can really be written to and read back from.
const store = new Map();
globalThis.localStorage = {
  getItem: (key) => (store.has(key) ? store.get(key) : null),
  setItem: (key, value) => store.set(key, String(value)),
  removeItem: (key) => store.delete(key),
};

const failures = [];
const el = (id) => document.getElementById(id);
const noop = async () => ({});

let calls = { Discover: 0, InstallService: 0, SaveNodeSettings: 0, RestartNode: 0, Pairing: 0 };

const SETTINGS = {
  settings: { peerListen: "127.0.0.1:7463", allowLan: false, discover: true, treatAsPrivate: [], autoWake: false },
  saved: { peerListen: "127.0.0.1:7463", allowLan: false, discover: true, treatAsPrivate: [], autoWake: false },
  sources: { peerListen: "default", allowLan: "default", discover: "default", treatAsPrivate: "default", autoWake: "default" },
  restartRequired: false,
};
const ADDRESSES = [
  { interface: "en0", address: "192.168.50.10", subnet: "192.168.50.0/24", private: true },
];

const bindings = {
  Overview: async () => ({
    reachable: true, nodeUrl: "http://127.0.0.1:7462",
    node: { id: "node_local", displayName: "local", platform: "darwin/arm64" },
    sessions: [], nodes: [], peers: [], counts: {},
  }),
  Discover: async () => { calls.Discover += 1; return { claude: 0, codex: 0, total: 0, skipped: 0 }; },
  SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop, SetNodeAddress: noop,
  Pairing: async () => { calls.Pairing += 1; return { availability: "unknown", candidates: [] }; },
  OpenPairing: noop, ClosePairing: noop, Inbox: noop, ClearInbox: noop, MCPConfig: noop, CopyText: noop,
  Outbound: noop, Wakes: noop, PairRequests: async () => [],
  StartPairRequest: noop, ApprovePairRequest: noop, ConfirmPairRequest: noop, RejectPairRequest: noop,
  ServiceStatus: async () => ({ tool: "/usr/local/bin/ah", supported: true, installed: true, running: true, pid: 9, unitPath: "/u", logHint: "/l", dbPath: "~/agenthub.db", dbPathKnown: true }),
  InstallService: async () => { calls.InstallService += 1; return { command: "ah service install", output: "installed" }; },
  UninstallService: noop,
  NodeSettings: async () => JSON.parse(JSON.stringify(SETTINGS)),
  SaveNodeSettings: async (patch) => { calls.SaveNodeSettings += 1; SETTINGS.saved = { ...SETTINGS.saved, ...patch }; SETTINGS.settings = { ...SETTINGS.saved }; return JSON.parse(JSON.stringify(SETTINGS)); },
  RestartNode: async () => { calls.RestartNode += 1; return { command: "ah service restart", output: "restarted" }; },
  LocalAddresses: async () => ADDRESSES,
  HostPlatform: async () => "darwin",
  Version: async () => ({ release: "unreleased" }),
};

const { configure, boot } = await import("../src/app.js");
configure(bindings);
const app = boot({ start: false });

const shown = () => !el("onboarding").classList.contains("hidden");
const stepIds = () => app.onboardingSteps().map((step) => step.id);
const rows = () => el("onboarding-steps").children;
const buttonsOf = (row) => (row.children[1]?.children[2]?.children ?? []);

// Everything finished: the node answers, the service holds it, sessions were
// found, a machine is paired, this one is reachable and something is published.
function allGood() {
  app.state.nodeReachable = true;
  app.state.loadedOnce = true;
  app.state.service = { supported: true, installed: true, running: true, pid: 9 };
  app.state.sessions = [{ id: "claude:one" }];
  app.state.nodes = [{ nodeId: "node_other" }];
  app.state.counts = { total: 1, all_paired: 1, selected: 0 };
  app.state.pairing = { windowAvailable: true, state: { peerAddress: "192.168.50.10:7463", peerAddressReachable: true } };
  app.state.discoveredNothing = false;
}

// Back to hidden through the card's own path rather than by poking its class:
// the first render after everything is done shows the ticks, the next one puts
// the card away.
function settle() {
  allGood();
  app.renderOnboarding();
  app.renderOnboarding();
  if (shown()) failures.push("the card never went away on a window with nothing left to do");
}

/* ---------------- 1. nothing to say ---------------- */

allGood();
app.renderOnboarding();
if (shown()) {
  failures.push("a finished window was shown the checklist from cold");
}

/* ---------------- 2. one card per trigger ---------------- */

for (const [name, apply] of [
  ["the node is unreachable", () => { app.state.nodeReachable = false; }],
  ["the service is not installed", () => { app.state.service = { supported: true, installed: false, running: false }; }],
  ["no sessions were found", () => { app.state.sessions = []; }],
  ["no machine is paired", () => { app.state.nodes = []; }],
]) {
  settle();
  apply();
  app.renderOnboarding();
  if (!shown()) failures.push(`the checklist stayed hidden when ${name}`);
  if (rows().length !== stepIds().length) {
    failures.push(`${name}: ${rows().length} rows on screen for ${stepIds().length} steps`);
  }
}

// And a service this platform has no manager for is not a reason to nag: the
// node runs, nothing this app can ask holds it, and that is the finished state.
settle();
app.state.service = { supported: false, installed: false, running: false };
app.renderOnboarding();
if (shown()) failures.push("a platform with no service manager was told to install a service");

/* ---------------- 3. never over a read that did not land ---------------- */

// The anti-#114 assertion. An empty table on a read that never reached the node
// is not a fact about this machine, and a step telling somebody to scan for
// sessions they may well have is the card asserting one.
settle();
app.state.loadedOnce = false;
app.state.sessions = [];
app.state.nodeReachable = false;
app.renderOnboarding();
if (!shown()) failures.push("an unreachable node did not show the checklist, which is what it is for");
if (stepIds().includes("sessions")) {
  failures.push("a window that has never reached the node was told its session list is empty");
}
app.state.loadedOnce = true;
if (!stepIds().includes("sessions")) {
  failures.push("the sessions step did not come back once a read had landed");
}

/* ---------------- 4. each button presses the one binding ---------------- */

const press = (id, index = 0) => {
  const step = app.onboardingSteps().find((entry) => entry.id === id);
  if (!step) { failures.push(`no ${id} step to press`); return null; }
  const action = step.actions[index];
  if (!action) { failures.push(`the ${id} step offers no button at ${index}`); return null; }
  return action.run();
};

// The service step is a doorway, not a second installer: it opens the panel and
// the form, and the install itself stays a deliberate second press.
settle();
app.state.service = { supported: true, installed: false, running: false };
app.state.view = "local";
await press("service");
if (app.state.view !== "settings" || app.state.settingsSection !== "settings-service") {
  failures.push(`the service step left the window on ${app.state.view}/${app.state.settingsSection}`);
}
if (el("service-form").classList.contains("hidden")) {
  failures.push("the service step did not open the install form");
}
if (calls.InstallService !== 0) {
  failures.push(`the service step installed a service nobody confirmed (${calls.InstallService} calls)`);
}

// The sessions step is the rescan button, once.
settle();
app.state.sessions = [];
app.state.view = "local";
calls.Discover = 0;
await press("sessions");
if (calls.Discover !== 1) failures.push(`the sessions step called Discover ${calls.Discover} times, want 1`);
// A scan that came back empty turns the step into the explanation. Pressing
// again would find the same nothing.
if (!app.state.discoveredNothing) failures.push("an empty rescan was not remembered");
const empty = app.onboardingSteps().find((step) => step.id === "sessions");
if (empty.body !== ZH["onboarding.sessions.bodyNoneFound"]) {
  failures.push(`a scan that found nothing did not explain where AgentHub looks: ${empty.body}`);
}

// The pairing step is a doorway to the drawer, on the view the drawer polls from.
settle();
app.state.nodes = [];
app.state.view = "local";
press("pair");
if (el("pairing-modal").classList.contains("hidden")) failures.push("the pairing step did not open the drawer");
if (app.state.view !== "network") failures.push(`the drawer was opened from ${app.state.view}, where it is never polled`);
app.closePairingDrawer();

// And the last step points rather than acts: the audience dialog resets every
// flag each time it opens, so opening it with nothing selected is a dead dialog.
settle();
app.state.counts = { total: 1, all_paired: 0, selected: 0 };
press("publish");
if (document.activeElement !== el("select-all")) {
  failures.push("the publish step did not put the keyboard on the checkbox that starts a selection");
}

// Without ah this window cannot find out what is holding the node, and
// restarting the process behind a launchd job is how one node becomes two. So
// that step explains and offers nothing, exactly as the service panel does.
settle();
app.state.service = { toolError: "ah: command not found", supported: false, installed: false, running: false };
const blocked = app.onboardingSteps().find((step) => step.id === "service");
if (blocked.actions.length !== 0) {
  failures.push(`a window that cannot see the service offered ${blocked.actions.length} buttons anyway`);
}
if (!blocked.body.includes("ah: command not found")) {
  failures.push(`the step did not say what went wrong: ${blocked.body}`);
}

// A platform with no service manager gets the start the window performs itself,
// which is the only way a setting saved here takes effect there.
settle();
app.state.service = { supported: false, installed: false, running: false };
app.state.nodeReachable = false;
calls.RestartNode = 0;
await press("service");
if (calls.RestartNode !== 1) {
  failures.push(`the start step called RestartNode ${calls.RestartNode} times, want 1`);
}

/* ---------------- 5. the switch is named, or it is not flipped ---------------- */

// docs/ui-contract.md §7.8 rule 4. The label says every flag the click sets,
// and nothing but a pressed button sets them.
settle();
app.state.pairing = { windowAvailable: true, state: { peerAddress: "", peerAddressReachable: false } };
await app.loadNodeSettings();

const lanClause = ZH["nodeSettings.repairAddressAndLan"]
  .replace("{interface}", "en0").replace("{address}", "192.168.50.10");
const noLanClause = ZH["nodeSettings.repairAddress"]
  .replace("{interface}", "en0").replace("{address}", "192.168.50.10");

el("node-allow-lan").checked = false;
let reach = app.onboardingSteps().find((step) => step.id === "reachable");
if (reach.done) failures.push("a node on loopback was called reachable");
if (reach.actions[0]?.label !== lanClause) {
  failures.push(`with LAN access off the button does not say it turns it on: ${reach.actions[0]?.label}`);
}
// The skip is always last, and always there.
if (reach.actions.at(-1)?.label !== ZH["nodeSettings.repairLoopback"]) {
  failures.push(`the step offers no way to stay local: ${reach.actions.map((a) => a.label).join(" | ")}`);
}

// The clause follows what the NODE has stored, not what the settings form is
// showing. The card is on another view; that checkbox is live-editable over
// there, and a tick nobody saved used to drop "and allow LAN connections" from
// this label while the click still turned it on — the silent tick §7.8 rule 4
// forbids, arrived at from the other direction.
el("node-allow-lan").checked = true;
reach = app.onboardingSteps().find((step) => step.id === "reachable");
if (reach.actions[0]?.label !== lanClause) {
  failures.push(`an unsaved tick in the settings form took the LAN clause off the card's label: ${reach.actions[0]?.label}`);
}
el("node-allow-lan").checked = false;
app.state.nodeSettings.saved = { ...app.state.nodeSettings.saved, allowLan: true };
reach = app.onboardingSteps().find((step) => step.id === "reachable");
if (reach.actions[0]?.label !== noLanClause) {
  failures.push(`with LAN access already saved on, the button still promises to turn it on: ${reach.actions[0]?.label}`);
}
app.state.nodeSettings.saved = { ...app.state.nodeSettings.saved, allowLan: false };

// And the card's button never carries an unrelated unsaved edit into the save.
// applyPeerListenRepair presses save on the form as it stands, which is right
// for the button inside that form and wrong for one two views away: a private
// range somebody was still typing would be committed by a click about a
// listening address.
el("node-private").value = "10.9.0.0/16";
calls.SaveNodeSettings = 0;
app.state.view = "local";
await press("reachable");
if (calls.SaveNodeSettings !== 0) {
  failures.push("the card saved the settings form while it held an edit nobody asked to save");
}
if (app.state.view !== "settings" || app.state.settingsSection !== "settings-node") {
  failures.push(`the refusal did not take the owner to the form it is about: ${app.state.view}/${app.state.settingsSection}`);
}
if (!el("banner").textContent.includes(ZH["onboarding.reachable.formDirty"])) {
  failures.push(`the refusal was silent: ${el("banner").textContent}`);
}
el("node-private").value = (SETTINGS.saved.treatAsPrivate ?? []).join(", ");
app.state.view = "local";

// A machine with nothing private to offer gets no one-click button at all: a
// repair that lands on the node's refusal is worse than no button, so the step
// says so and points at the form where the decision is visible.
app.state.nodeAddresses = { list: [{ interface: "en5", address: "122.122.0.7", subnet: "122.122.0.0/16", private: false }], failure: "" };
const nowhere = app.onboardingSteps().find((step) => step.id === "reachable");
if (nowhere.body !== ZH["onboarding.reachable.bodyNoAddress"]) {
  failures.push(`a machine with no private address was still told to pick one: ${nowhere.body}`);
}
if (nowhere.actions[0]?.label !== ZH["onboarding.reachable.openSettings"]) {
  failures.push(`no way through to the form: ${nowhere.actions.map((a) => a.label).join(" | ")}`);
}
nowhere.actions[0].run();
if (app.state.view !== "settings" || app.state.settingsSection !== "settings-node") {
  failures.push(`the fallback left the window on ${app.state.view}/${app.state.settingsSection}`);
}
app.state.nodeAddresses = { list: ADDRESSES, failure: "" };

// Rendering the card is not a decision. Building these buttons reads the
// checkbox; nothing here may write it.
for (const before of [false, true]) {
  el("node-allow-lan").checked = before;
  app.renderOnboarding();
  app.onboardingSteps();
  if (el("node-allow-lan").checked !== before) {
    failures.push(`merely rendering the checklist changed “allow LAN connections” to ${el("node-allow-lan").checked}`);
  }
}

// Pressing it goes through the form, so there is one validation, one restart
// and one check that what was asked for is what the node now holds.
el("node-allow-lan").checked = false;
calls.SaveNodeSettings = 0;
calls.RestartNode = 0;
await press("reachable");
if (calls.SaveNodeSettings !== 1) failures.push(`SaveNodeSettings called ${calls.SaveNodeSettings} times, want 1`);
if (calls.RestartNode !== 1) failures.push(`RestartNode called ${calls.RestartNode} times, want 1`);
if (SETTINGS.saved.peerListen !== "192.168.50.10:7463" || SETTINGS.saved.allowLan !== true) {
  failures.push(`the node was sent ${JSON.stringify(SETTINGS.saved)}, not the address and the flag the label named`);
}

/* ---------------- 6. a write in flight disables every button ---------------- */

settle();
app.state.nodes = [];
app.state.sessions = [];
app.state.busy = true;
app.renderOnboarding();
let seen = 0;
for (const row of rows()) {
  for (const button of buttonsOf(row)) {
    seen += 1;
    if (!button.disabled) failures.push(`a step button stayed live during a write: ${button.textContent}`);
  }
}
if (seen === 0) failures.push("no step buttons were on screen, so the busy assertion covers nothing");
app.state.busy = false;
app.renderOnboarding();
for (const row of rows()) {
  for (const button of buttonsOf(row)) {
    if (button.disabled) failures.push(`a step button stayed disabled after the write: ${button.textContent}`);
  }
}

/* ---------------- 7. the fifteen-second tick keeps the click target ---------------- */

// The local view is repainted by load(); rebuilding these rows every tick
// replaces the button an owner is halfway through clicking, which is the defect
// updateCandidateRow exists for.
const firstRows = [...rows()];
const firstButtons = firstRows.map((row) => [...buttonsOf(row)]);
app.renderOnboarding();
app.renderOnboarding();
const againRows = [...rows()];
if (againRows.length !== firstRows.length) {
  failures.push(`the step set changed under an unchanged state: ${firstRows.length} then ${againRows.length}`);
}
for (let index = 0; index < firstRows.length; index++) {
  if (firstRows[index] !== againRows[index]) {
    failures.push(`step row ${index} was replaced on a repaint with nothing changed`);
  }
  const now = [...buttonsOf(againRows[index])];
  if (now.length !== firstButtons[index].length) {
    failures.push(`step row ${index} lost or gained a button on an unchanged repaint`);
  }
  for (let button = 0; button < firstButtons[index].length; button++) {
    if (firstButtons[index][button] !== now[button]) {
      failures.push(`the button an owner is about to click was replaced (step ${index}, button ${button})`);
    }
  }
}

/* ---------------- 8. the dismissal is remembered ---------------- */

el("onboarding-dismiss").onclick();
if (shown()) failures.push("the card stayed on screen after being dismissed");
if (!app.state.ui.onboardingDismissed) failures.push("the dismissal was not recorded in state");
const written = JSON.parse(store.get("agenthub.desktop.ui.v1") ?? "{}");
if (written.onboardingDismissed !== true) {
  failures.push(`the dismissal did not reach the preferences: ${store.get("agenthub.desktop.ui.v1")}`);
}

// A fresh window with that preference does not render it, even with every
// trigger holding.
const next = boot({ start: false });
if (!next.state.ui.onboardingDismissed) failures.push("a fresh boot did not read the dismissal back");
next.state.nodeReachable = false;
next.state.loadedOnce = true;
next.state.sessions = [];
next.state.nodes = [];
next.state.counts = {};
next.renderOnboarding();
if (shown()) failures.push("a dismissed checklist came back on the next launch");

// And it is reversible, because this card is the only screen that says what the
// app needs before it can do anything.
el("settings-show-onboarding").onclick();
if (next.state.ui.onboardingDismissed) failures.push("the settings link did not clear the dismissal");
if (!shown()) failures.push("the settings link did not bring the checklist back");
if (next.state.view !== "local") failures.push("the checklist was brought back on a view it is not on");

/* ---------------- 9. the service status lands after the first render ---------------- */

// load() renders the card and only then fires loadService(), so for the first
// seconds of every launch the card is built with state.service === null. That
// used to fall through to "Install the service" — on a machine whose service
// was installed and running, beside a title-bar pill that said so. Fourteen
// seconds of a flat contradiction on the first screen a stranger sees.
const RUNNING = { tool: "/usr/local/bin/ah", supported: true, installed: true, running: true, pid: 9, unitPath: "/u", logHint: "/l", dbPath: "~/agenthub.db", dbPathKnown: true };
let releaseStatus = () => {};
const heldStatus = new Promise((resolve) => { releaseStatus = resolve; });
bindings.ServiceStatus = async () => { await heldStatus; return RUNNING; };
// configure() copies the object, so a fake swapped in afterwards only takes
// effect once it is handed over again.
configure(bindings);

next.state.ui.onboardingDismissed = false;
next.state.service = null;
next.state.nodeReachable = true;
next.state.loadedOnce = true;
next.state.sessions = [];
next.state.nodes = [];
next.state.counts = {};
next.state.pairing = null;
next.renderOnboarding();

const serviceIndex = next.onboardingSteps().findIndex((step) => step.id === "service");
const tickAt = (index) => rows()[index]?.children[0]?.textContent;
const bodyAt = (index) => rows()[index]?.children[1]?.children[1]?.textContent;
const waiting = next.onboardingSteps()[serviceIndex];
if (waiting.done) failures.push("the service step was ticked before any status had been read");
if (waiting.actions.length !== 0) {
  failures.push(`a window that does not yet know whether a service exists offered ${waiting.actions.length} buttons about it`);
}
if (waiting.body !== ZH["onboarding.service.bodyChecking"]) {
  failures.push(`before the status landed the step said: ${waiting.body}`);
}
if (bodyAt(serviceIndex) === ZH["onboarding.service.body"] || tickAt(serviceIndex) === "✓") {
  failures.push(`the rendered row does not match the step: ${tickAt(serviceIndex)} / ${bodyAt(serviceIndex)}`);
}

// And when it lands, the card is repainted by loadService itself — no second
// render from the test, because there is no second render in the app either:
// the next one is fifteen seconds away.
const inFlight = next.loadService();
releaseStatus();
await inFlight;
if (tickAt(serviceIndex) !== "✓") {
  failures.push(`the status landed and the card still read ${tickAt(serviceIndex)} / ${bodyAt(serviceIndex)}`);
}
bindings.ServiceStatus = async () => RUNNING;
configure(bindings);

/* ---------------- 10. a node that is not answering ---------------- */

// The one situation this card exists for. Step 1 used to be derived from the
// service manager's answer alone, so it ticked over a window showing "cannot
// reach http://127.0.0.1:7462", and step 3 sat on "waiting for the node to
// say…" forever because the read that fills it is never even attempted while
// the node is down. Between them the card had nothing to press.
store.clear();
bindings.Overview = async () => ({
  // Shaped as App.Overview shapes it in Go: it never rejects, it answers
  // unreachable with the dial error and empty lists.
  reachable: false, nodeUrl: "http://127.0.0.1:7462",
  error: "dial tcp 127.0.0.1:7462: connect: connection refused",
  sessions: [], nodes: [], peers: [], counts: {},
});
configure(bindings);
const dead = boot({ start: false });
await dead.load();

if (!shown()) failures.push("a node that never answered did not show the checklist, which is what it is for");
const deadService = dead.onboardingSteps().find((step) => step.id === "service");
if (deadService.done) failures.push("a node that is not answering was called a finished step");
if (deadService.actions.length === 0) {
  failures.push("a node that is not answering was offered no way to start it");
}
if (deadService.actions[0]?.label !== ZH["onboarding.service.actionStart"]) {
  failures.push(`the step offers ${deadService.actions[0]?.label}, not the start`);
}
const deadReach = dead.onboardingSteps().find((step) => step.id === "reachable");
if (deadReach.body !== ZH["onboarding.reachable.bodyNodeDown"]) {
  failures.push(`step 3 on a dead node said: ${deadReach.body}`);
}
if (deadReach.done) failures.push("a dead node was called reachable from another machine");
const pressable = dead.onboardingSteps().reduce((total, step) => total + step.actions.length, 0);
if (pressable === 0) {
  failures.push("the checklist offered nothing to press on the one situation it exists for");
}
// And the button is the node restart, not the service installer: whatever the
// service manager says it is holding, what is wrong is that nothing answers.
calls.RestartNode = 0;
await deadService.actions[0].run();
if (calls.RestartNode !== 1) {
  failures.push(`the start button called RestartNode ${calls.RestartNode} times, want 1`);
}
// Even where the service is installed and running: that is exactly the state
// the live run found, and the one that used to put a tick over a dead node.
dead.state.nodeReachable = false;
dead.state.service = { supported: true, installed: true, running: true, pid: 9 };
const stillDown = dead.onboardingSteps().find((step) => step.id === "service");
if (stillDown.done || stillDown.actions.length === 0) {
  failures.push("an installed, running service ticked the step over a node that answers nothing");
}

/* ---------------- 11. a settings read that failed does not wedge step 3 ---------------- */

// onboardingSettingsAsked is set before the await and was never cleared, so one
// failed read left "waiting for the node to say…" on screen for the life of the
// window — with no button and no timeout, which reads as a hang.
store.clear();
bindings.Overview = async () => ({
  reachable: true, nodeUrl: "http://127.0.0.1:7462",
  node: { id: "node_local", displayName: "local", platform: "darwin/arm64" },
  sessions: [], nodes: [], peers: [], counts: {},
});
configure(bindings);
let settingsReads = 0;
bindings.NodeSettings = async () => { settingsReads += 1; throw new Error("dial tcp: connection refused"); };
configure(bindings);
const wedged = boot({ start: false });
await wedged.load();
wedged.renderOnboarding();
// The read is fired from inside the render and resolves on its own; let it.
for (let turn = 0; turn < 8; turn++) await Promise.resolve();

if (settingsReads === 0) failures.push("the checklist never asked for the node settings at all");
const failed = wedged.onboardingSteps().find((step) => step.id === "reachable");
if (failed.body === ZH["onboarding.reachable.bodyLoading"]) {
  failures.push("a failed read left the step waiting for an answer that is never coming");
}
if (failed.body !== ZH["onboarding.reachable.bodyUnreadable"]) {
  failures.push(`a failed read said: ${failed.body}`);
}
if (failed.actions[0]?.label !== ZH["onboarding.reachable.retry"]) {
  failures.push(`a failed read offered no retry: ${failed.actions.map((a) => a.label).join(" | ")}`);
}
// And the retry really asks again, which is the whole point of clearing the latch.
bindings.NodeSettings = async () => { settingsReads += 1; return JSON.parse(JSON.stringify(SETTINGS)); };
configure(bindings);
const readsBefore = settingsReads;
failed.actions[0].run();
for (let turn = 0; turn < 8; turn++) await Promise.resolve();
if (settingsReads === readsBefore) {
  failures.push("the retry button did not ask the node again");
}
const recovered = wedged.onboardingSteps().find((step) => step.id === "reachable");
if (recovered.body === ZH["onboarding.reachable.bodyUnreadable"]) {
  failures.push("the step stayed on the failure after a read that succeeded");
}

/* ---------------- 12. the card says it is going ---------------- */

// The auto-hide shows every tick once and then puts the card away. That last
// render used to show ticks and nothing else; onboarding.allDone was written in
// both tables and rendered nowhere.
store.clear();
const finishing = boot({ start: false });
// Open first, so the finish is something that happens to the card rather than
// a state it booted into.
finishing.state.nodeReachable = true;
finishing.state.loadedOnce = true;
finishing.state.service = { supported: true, installed: true, running: true, pid: 9 };
finishing.state.sessions = [{ id: "claude:one" }];
finishing.state.counts = { total: 1, all_paired: 1, selected: 0 };
finishing.state.pairing = { windowAvailable: true, state: { peerAddress: "192.168.50.10:7463", peerAddressReachable: true } };
finishing.state.nodes = [];
finishing.renderOnboarding();
if (!shown()) failures.push("the card was not on screen before the step that finishes it");
if (!el("onboarding-alldone").classList.contains("hidden")) {
  failures.push("the card said goodbye while a step was still open");
}
finishing.state.nodes = [{ nodeId: "node_other" }];
finishing.renderOnboarding();
if (!shown()) failures.push("the card vanished under the click that completed it");
if (el("onboarding-alldone").classList.contains("hidden")) {
  failures.push("the last render showed five ticks and never said the card was going");
}
finishing.renderOnboarding();
if (shown()) failures.push("the card stayed after its farewell render");

/* ---------------- 13. the English half ---------------- */

// Every other assertion above reads the Chinese table, because that is what the
// shim boots in. This card is the first thing an English-speaking stranger
// reads, so it is rendered in English too and checked for anything left behind.
inEnglish(next, failures, "the checklist", ["onboarding-steps"], () => next.renderOnboarding());

if (failures.length > 0) {
  for (const failure of failures) console.error(` - ${failure}`);
  process.exit(1);
}
console.log("onboarding: shown for a reason, each button its own binding, the LAN switch named before it is set");
