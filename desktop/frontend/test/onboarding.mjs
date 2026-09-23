// The first launch, from the checklist to a paired node.
//
// The card is the first screen a stranger sees after the installer finishes,
// and every control it offers already exists somewhere else in the window. So
// what is checked here is not the controls but the joins: that a step is shown
// only when the window actually knows the thing it claims, that each button
// reaches the binding the panel's own button reaches and reaches it once, and
// that the fifteen-second refresh does not replace the button an owner is
// halfway through clicking.
//
// Two of the five steps are no longer steps. Scanning for sessions is done by
// the window on the first read that reaches the node, and 「can this machine be
// reached」 is the pairing drawer's own first step — so the §7.8 rule 4
// assertions about naming 「allow LAN connections」 before setting it follow the
// repair buttons there, and are made against the drawer below.
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
}

// Back to hidden through the card's own path rather than by poking its class:
// the first render after everything is done shows the ticks, the next one puts
// the card away.
function settle() {
  allGood();
  app.renderOnboarding();
  // The farewell is spent by the next fifteen-second tick, not by the next
  // render — test 12 drives that through load() itself. Here the tick is only
  // scenery, so it is stepped by hand to get the card back to hidden.
  app.spendOnboardingFarewell();
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

/* ---------------- 3. three steps, and the scan is not one -------------- */

settle();
app.state.nodeReachable = false;
app.renderOnboarding();
if (!shown()) failures.push("an unreachable node did not show the checklist, which is what it is for");
if (stepIds().join(",") !== "service,pair,publish") {
  failures.push(`the checklist offers ${stepIds().join(",")}, want service,pair,publish`);
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

/* -------- 5. the switch is named, or it is not flipped (drawer step 1) ---- */

// docs/ui-contract.md §7.8 rule 4. The label says every flag the click sets,
// and nothing but a pressed button sets them. These buttons were the
// checklist's; they are the pairing drawer's first step now, offered in place
// of a button that sent the owner two views away to find them.
settle();
app.state.pairing = { windowAvailable: true, state: { peerAddress: "", peerAddressReachable: false } };
await app.loadNodeSettings();

const lanClause = ZH["nodeSettings.repairAddressAndLan"]
  .replace("{interface}", "en0").replace("{address}", "192.168.50.10");
const noLanClause = ZH["nodeSettings.repairAddress"]
  .replace("{interface}", "en0").replace("{address}", "192.168.50.10");

// The buttons as the drawer actually renders them, not as the option list
// describes them: what an owner presses is the rendered node.
const repairButtons = () => {
  app.renderPairHere();
  const actions = [...el("pair-here-note").children].find((child) => child.className === "repairactions");
  return [...(actions?.children ?? [])];
};

el("node-allow-lan").checked = false;
if (app.pairHereState(app.state.pairing.state).reachable) {
  failures.push("a node with no announceable address was called reachable");
}
let buttons = repairButtons();
if (!el("pair-here-note").textContent.includes(ZH["pair.hereNoAddressWhy"])) {
  failures.push(`step 1 did not say why nobody can get in: ${el("pair-here-note").textContent}`);
}
if (buttons[0]?.textContent !== lanClause) {
  failures.push(`with LAN access off the button does not say it turns it on: ${buttons[0]?.textContent}`);
}
// 「stay on this machine only」 is peerListenRepairs' last option and the
// checklist's skip. It is not offered here: this block exists because the other
// machine cannot reach this one, so an option that changes nothing is an answer
// to a different question. The way through to the form is what is last.
if (buttons.some((button) => button.textContent === ZH["nodeSettings.repairLoopback"])) {
  failures.push("the drawer offered 「stay local」 under the heading that says nothing can reach this machine");
}
if (buttons.at(-1)?.textContent !== ZH["pair.hereFix"]) {
  failures.push(`no way through to the settings form: ${buttons.map((b) => b.textContent).join(" | ")}`);
}

// The clause follows what the NODE has stored, not what the settings form is
// showing. That form is on another view and live-editable, and a tick nobody
// saved used to drop "and allow LAN connections" from this label while the
// click still turned it on — the silent tick §7.8 rule 4 forbids, arrived at
// from the other direction.
el("node-allow-lan").checked = true;
if (repairButtons()[0]?.textContent !== lanClause) {
  failures.push(`an unsaved tick in the settings form took the LAN clause off the label: ${repairButtons()[0]?.textContent}`);
}
el("node-allow-lan").checked = false;
app.state.nodeSettings.saved = { ...app.state.nodeSettings.saved, allowLan: true };
if (repairButtons()[0]?.textContent !== noLanClause) {
  failures.push(`with LAN access already saved on, the button still promises to turn it on: ${repairButtons()[0]?.textContent}`);
}
app.state.nodeSettings.saved = { ...app.state.nodeSettings.saved, allowLan: false };

// And the button never carries an unrelated unsaved edit into the save.
// applyPeerListenRepair presses save on the form as it stands, which is right
// for the button inside that form and wrong for one in a drawer: a private
// range somebody was still typing would be committed by a click about a
// listening address.
el("node-private").value = "10.9.0.0/16";
calls.SaveNodeSettings = 0;
app.state.view = "local";
await repairButtons()[0].onclick();
if (calls.SaveNodeSettings !== 0) {
  failures.push("the drawer saved the settings form while it held an edit nobody asked to save");
}
if (app.state.view !== "settings" || app.state.settingsSection !== "settings-node") {
  failures.push(`the refusal did not take the owner to the form it is about: ${app.state.view}/${app.state.settingsSection}`);
}
if (!el("banner").textContent.includes(ZH["pair.formDirty"])) {
  failures.push(`the refusal was silent: ${el("banner").textContent}`);
}
el("node-private").value = (SETTINGS.saved.treatAsPrivate ?? []).join(", ");
app.state.view = "local";

// A machine with nothing private to offer gets no one-click button at all: a
// repair that lands on the node's refusal is worse than no button, so what is
// left is the way through to the form, where the decision is visible.
app.state.nodeAddresses = { list: [{ interface: "en5", address: "122.122.0.7", subnet: "122.122.0.0/16", private: false }], failure: "" };
buttons = repairButtons();
if (buttons.length !== 1 || buttons[0].textContent !== ZH["pair.hereFix"]) {
  failures.push(`a machine with no private address was still offered one: ${buttons.map((b) => b.textContent).join(" | ")}`);
}
buttons[0].onclick();
if (app.state.view !== "settings" || app.state.settingsSection !== "settings-node") {
  failures.push(`the fallback left the window on ${app.state.view}/${app.state.settingsSection}`);
}
app.state.nodeAddresses = { list: ADDRESSES, failure: "" };

// Rendering the drawer is not a decision. Building these buttons reads the
// checkbox; nothing here may write it.
for (const before of [false, true]) {
  el("node-allow-lan").checked = before;
  app.renderPairHere();
  app.pairHereRepairs();
  if (el("node-allow-lan").checked !== before) {
    failures.push(`merely rendering step 1 changed “allow LAN connections” to ${el("node-allow-lan").checked}`);
  }
}

// Pressing it goes through the form, so there is one validation, one restart
// and one check that what was asked for is what the node now holds.
el("node-allow-lan").checked = false;
calls.SaveNodeSettings = 0;
calls.RestartNode = 0;
await repairButtons()[0].onclick();
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

/* ---------------- 9b. the scan the window runs for itself ---------------- */

// The anti-#114 assertion, now about the scan the window runs itself. An empty
// table on a read that never reached the node is not a fact about this machine,
// and scanning on the strength of one would be the window asserting it.
{
  const unreachable = { ...bindings, Overview: async () => ({ reachable: false, error: "connection refused", node: {} }) };
  calls.Discover = 0;
  configure(unreachable);
  const down = boot({ start: false });
  await down.load();
  if (calls.Discover !== 0) {
    failures.push("a read that never reached the node still triggered a scan of this machine");
  }
  configure(bindings);
}

// And on the first read that does land with an empty table it scans once, by
// itself, because "press this button once" was never a step.
{
  calls.Discover = 0;
  const fresh = boot({ start: false });
  await fresh.load();
  if (calls.Discover !== 1) {
    failures.push(`a first reachable read with no sessions scanned ${calls.Discover} times, want 1`);
  }
  await fresh.load();
  if (calls.Discover !== 1) {
    failures.push(`the scan ran again on a later read (${calls.Discover} in all); it is once per window`);
  }
  // A scan that found nothing says where AgentHub looks, in the banner, which
  // is where the answer is. That sentence used to be the step's own body.
  if (!el("banner").textContent.includes(ZH["app.rescanNothingFound"].trim())) {
    failures.push(`an empty scan did not explain where AgentHub looks: ${el("banner").textContent}`);
  }
}

// A load made while something else holds the window — installService calls
// load() in the middle of its own busy stretch, on exactly a first launch —
// must not spend the once-per-window scan: discoverSessions goes through
// withBusy, which drops the call, and the flag would be gone with no scan run.
{
  calls.Discover = 0;
  const installing = boot({ start: false });
  installing.state.busy = true;
  await installing.load();
  if (calls.Discover !== 0) {
    failures.push(`a load made while busy asked for ${calls.Discover} scans; withBusy would drop them`);
  }
  installing.state.busy = false;
  await installing.load();
  if (calls.Discover !== 1) {
    failures.push(`the first load after a busy one scanned ${calls.Discover} times, want 1: the busy load spent the only scan`);
  }
}

// With no session found, the publish step says why there is nothing to tick
// instead of pointing at an empty table — and says nothing of the kind once a
// session exists, or before a read has reached the node (#114).
{
  const empty = boot({ start: false });
  await empty.load();
  const publish = () => empty.onboardingSteps().find((step) => step.id === "publish")?.body ?? "";
  if (!publish().includes(ZH["onboarding.publish.noSessions"])) {
    failures.push(`with no sessions the publish step does not say to start one: ${publish()}`);
  }
  empty.state.sessions = [{ id: "claude:one" }];
  if (publish().includes(ZH["onboarding.publish.noSessions"])) {
    failures.push("with a session found the publish step still says none was found");
  }
  empty.state.sessions = [];
  empty.state.nodeReachable = false;
  if (publish().includes(ZH["onboarding.publish.noSessions"])) {
    failures.push("a read that never reached the node was reported as a machine with no sessions");
  }
}

/* ---------------- 10. a node that is not answering ---------------- */

// The one situation this card exists for. Step 1 used to be derived from the
// service manager's answer alone, so it ticked over a window showing "cannot
// reach http://127.0.0.1:7462", and the card had nothing to press.
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

/* -------- 11. a settings read that failed does not wedge the drawer ------- */

// The latch is set before the await. Left unreleased on failure, one unlucky
// read cost the pairing drawer its repair buttons for the life of the window —
// with nothing on screen to retry and nothing saying why. The drawer asks
// again the next time it is opened.
store.clear();
bindings.Overview = async () => ({
  reachable: true, nodeUrl: "http://127.0.0.1:7462",
  node: { id: "node_local", displayName: "local", platform: "darwin/arm64" },
  sessions: [], nodes: [], peers: [], counts: {},
});
// Back on loopback: section 5 saved a LAN address, and a node already serving
// one has nothing here to repair.
SETTINGS.saved = { peerListen: "127.0.0.1:7463", allowLan: false, discover: true, treatAsPrivate: [], autoWake: false };
SETTINGS.settings = { ...SETTINGS.saved };
let settingsReads = 0;
bindings.NodeSettings = async () => { settingsReads += 1; throw new Error("dial tcp: connection refused"); };
configure(bindings);
const wedged = boot({ start: false });
wedged.state.pairing = { windowAvailable: true, state: { peerAddress: "", peerAddressReachable: false } };
await wedged.load();

const settleReads = async () => { for (let turn = 0; turn < 12; turn++) await Promise.resolve(); };

await wedged.openPairingDrawer();
await settleReads();
if (settingsReads === 0) failures.push("the drawer never asked for the node settings at all");
if (wedged.pairHereRepairs().length !== 0) {
  failures.push("a failed read still produced repair options, which can only have been invented");
}

// Asked once per opening, not once per render: a read on every repaint would
// re-fire on every tick behind the drawer.
const afterFirst = settingsReads;
wedged.renderPairHere();
wedged.renderPairHere();
await settleReads();
if (settingsReads !== afterFirst) {
  failures.push(`rendering the drawer read the settings ${settingsReads - afterFirst} more times`);
}

// And the next opening asks again, which is the whole point of releasing the
// latch on a read that produced no baseline.
bindings.NodeSettings = async () => { settingsReads += 1; return JSON.parse(JSON.stringify(SETTINGS)); };
configure(bindings);
wedged.closePairingDrawer();
await wedged.openPairingDrawer();
await settleReads();
if (settingsReads === afterFirst) {
  failures.push("reopening the drawer did not ask the node again");
}
if (wedged.pairHereRepairs().length === 0) {
  failures.push("the drawer offered no repair after a read that succeeded");
}
wedged.closePairingDrawer();

/* ---------------- 12. the card says it is going, and stays up to be read ---------------- */

// The auto-hide shows every tick once and then puts the card away. Two defects
// lived here. onboarding.allDone was written in both tables and rendered
// nowhere, so the last render showed ticks and no reason. And then the farewell
// was spent by the next RENDER rather than the next tick: load() fires
// loadService() and renders, the status lands about fifty milliseconds later
// and loadService re-renders this card, so the goodbye was on screen for those
// fifty milliseconds and nobody ever read it.
//
// So this drives the real sequence rather than calling renderOnboarding() by
// hand: load(), the status landing after it, and the next fifteen-second tick.
store.clear();
let farewellPaired = [];
let releaseFarewell = () => {};
let heldFarewell = Promise.resolve();
const holdStatus = () => {
  heldFarewell = new Promise((resolve) => { releaseFarewell = resolve; });
};
// Held on every read, so the status always lands AFTER the render load() does,
// which is the order the app really runs in.
bindings.ServiceStatus = async () => { await heldFarewell; return RUNNING; };
bindings.Overview = async () => ({
  reachable: true, nodeUrl: "http://127.0.0.1:7462",
  node: { id: "node_local", displayName: "local", platform: "darwin/arm64" },
  sessions: [{ id: "claude:one" }],
  nodes: farewellPaired,
  peers: [],
  counts: { total: 1, all_paired: 1, selected: 0 },
});
configure(bindings);
const finishing = boot({ start: false });
finishing.state.pairing = { windowAvailable: true, state: { peerAddress: "192.168.50.10:7463", peerAddressReachable: true } };

const settleStatus = async () => {
  releaseFarewell();
  for (let turn = 0; turn < 12; turn++) await Promise.resolve();
};

// Tick one: nothing is paired yet, so the card is open on a real step. The
// finish has to be something that happens to the card, not a state it booted
// into.
holdStatus();
await finishing.load();
await settleStatus();
if (!shown()) failures.push("the card was not on screen before the step that finishes it");
if (!el("onboarding-alldone").classList.contains("hidden")) {
  failures.push("the card said goodbye while a step was still open");
}

// Tick two: the last step completes. load() renders the farewell, and then the
// status lands and re-renders. The card has to survive that.
farewellPaired = [{ nodeId: "node_other" }];
holdStatus();
await finishing.load();
if (!shown()) failures.push("the card vanished under the click that completed it");
if (el("onboarding-alldone").classList.contains("hidden")) {
  failures.push("the last render showed three ticks and never said the card was going");
}
await settleStatus();
if (!shown()) {
  failures.push("the service status landing fifty milliseconds later took the farewell off screen");
}
if (el("onboarding-alldone").classList.contains("hidden")) {
  failures.push("the farewell was rendered away by a read that answered inside the same tick");
}

// Tick three, fifteen seconds later: read by now, so it goes.
holdStatus();
await finishing.load();
if (shown()) failures.push("the card went before the tick its farewell was put up for was over");
await settleStatus();
if (shown()) failures.push("the card stayed after the tick that spent its farewell");

bindings.ServiceStatus = async () => RUNNING;
configure(bindings);

/* ---------------- 14. a suggestion this window filled in is not an edit ---------------- */

// suggestPrivateRange writes the chosen interface's own subnet into the
// private-range box the moment the owner picks a non-private address in the
// settings form, with nobody typing anything. That counted as an unsaved
// change, so step 3 refused to do anything for an owner who had merely looked
// at the address list, and the refusal named no field, so there was nothing to
// go and undo.
store.clear();
const MIXED = [
  { interface: "en0", address: "192.168.50.10", subnet: "192.168.50.0/24", private: true },
  { interface: "en5", address: "122.122.0.7", subnet: "122.122.0.0/16", private: false },
];
bindings.Overview = async () => ({
  reachable: true, nodeUrl: "http://127.0.0.1:7462",
  node: { id: "node_local", displayName: "local", platform: "darwin/arm64" },
  sessions: [], nodes: [], peers: [], counts: {},
});
bindings.LocalAddresses = async () => MIXED;
configure(bindings);
const suggesting = boot({ start: false });
suggesting.state.nodeReachable = true;
suggesting.state.loadedOnce = true;
suggesting.state.service = { supported: true, installed: true, running: true, pid: 9 };
suggesting.state.pairing = { windowAvailable: true, state: { peerAddress: "", peerAddressReachable: false } };
SETTINGS.saved = { peerListen: "127.0.0.1:7463", allowLan: false, discover: true, treatAsPrivate: [], autoWake: false };
SETTINGS.settings = { ...SETTINGS.saved };
await suggesting.loadNodeSettings();

// The owner picks the non-private address in the form. Nothing else.
el("node-peerlisten").value = "122.122.0.7:7463";
suggesting.suggestPrivateRange();
suggesting.syncNodeSettingsForm();
if (el("node-private").value.trim() !== "122.122.0.0/16") {
  failures.push(`the form did not offer the subnet, so this test proves nothing: ${el("node-private").value}`);
}
if (suggesting.state.nodePrivateSuggested !== "122.122.0.0/16") {
  failures.push("the form filled the range in without recording that it was its own suggestion");
}

// In the drawer, the repair button now has to work: the range on screen is the
// window's own suggestion, not an edit belonging to the owner.
suggesting.state.view = "network";
calls.SaveNodeSettings = 0;
const repair = suggesting.pairHereRepairs()[0];
await suggesting.applyPeerListenRepairFromCard(repair);
if (calls.SaveNodeSettings !== 1) {
  failures.push("the drawer refused over a private range the window itself had filled in");
}
if ((SETTINGS.saved.treatAsPrivate ?? []).length !== 0) {
  failures.push(`the repair declared a private range nobody asked for: ${JSON.stringify(SETTINGS.saved.treatAsPrivate)}`);
}
if (el("node-private").value.trim() !== "") {
  failures.push(`the withdrawn suggestion was left in the form: ${el("node-private").value}`);
}

// And an edit that really is the owner's is still refused, by name now.
SETTINGS.saved = { peerListen: "127.0.0.1:7463", allowLan: false, discover: true, treatAsPrivate: [], autoWake: false };
SETTINGS.settings = { ...SETTINGS.saved };
suggesting.state.nodeSettings = null;
await suggesting.loadNodeSettings();
el("node-autowake").checked = true;
suggesting.state.view = "network";
calls.SaveNodeSettings = 0;
const refused = suggesting.pairHereRepairs()[0];
await suggesting.applyPeerListenRepairFromCard(refused);
if (calls.SaveNodeSettings !== 0) {
  failures.push("the drawer carried an unsaved auto-wake tick into a save about a listening address");
}
if (!el("banner").textContent.includes(ZH["nodeSettings.autoWake"])) {
  failures.push(`the refusal did not name the dirty field: ${el("banner").textContent}`);
}
if (!el("banner").textContent.includes(ZH["pair.formDirty"])) {
  failures.push(`the refusal did not say which two buttons undo it: ${el("banner").textContent}`);
}
el("node-autowake").checked = false;
bindings.LocalAddresses = async () => ADDRESSES;
configure(bindings);

/* ---------------- 15. no ah AND no node is still a start button ---------------- */

// status.toolError is tested before the unreachable branch, so a down node on a
// machine without ah got a step 1 that explained and offered nothing, on the
// one situation this card exists for. RestartNode does not need ah there:
// desktop/nodeprocess.go sends anything that is not "supported and installed"
// to restartNodeProcess, which stops whatever agenthub-node is running and
// starts the binary beside this app.
store.clear();
const noAh = boot({ start: false });
const AH_ERROR = "AGENTHUB_AH points at /nope: no such file";
noAh.state.loadedOnce = true;
noAh.state.nodeReachable = false;
noAh.state.service = { toolError: AH_ERROR };
const noAhStep = noAh.onboardingSteps().find((step) => step.id === "service");
if (noAhStep.actions.length === 0) {
  failures.push("a dead node on a machine without ah was offered nothing to press");
}
if (noAhStep.actions[0]?.label !== ZH["onboarding.service.actionStart"]) {
  failures.push(`the step offers ${noAhStep.actions[0]?.label}, not the start`);
}
if (noAhStep.body !== ZH["onboarding.service.bodyNoAhNodeDown"].replace("{error}", AH_ERROR)) {
  failures.push(`a dead node on a machine without ah read: ${noAhStep.body}`);
}
calls.RestartNode = 0;
await noAhStep.actions[0]?.run();
if (calls.RestartNode !== 1) {
  failures.push(`the start button called RestartNode ${calls.RestartNode} times, want 1`);
}
// A node that IS answering keeps the explanation and no button: without ah
// there is nothing here worth pressing while the node is up.
noAh.state.nodeReachable = true;
// restartNode() ran a load() of its own, which read the status again — and the
// fake answers a healthy one. The situation under test is the machine without
// ah, so it is put back.
noAh.state.service = { toolError: AH_ERROR };
const noAhUp = noAh.onboardingSteps().find((step) => step.id === "service");
if (noAhUp.actions.length !== 0) {
  failures.push(`a live node without ah was offered ${noAhUp.actions.length} buttons about a process it cannot see`);
}
if (noAhUp.body !== ZH["onboarding.service.bodyNoAh"].replace("{error}", AH_ERROR)) {
  failures.push(`a live node without ah read: ${noAhUp.body}`);
}

/* ---------------- 13. the English half ---------------- */

// Every other assertion above reads the Chinese table, because that is what the
// shim boots in. This card is the first thing an English-speaking stranger
// reads, so it is rendered in English too and checked for anything left behind.
inEnglish(next, failures, "the checklist", ["onboarding-steps"], () => next.renderOnboarding());

if (failures.length > 0) {
  for (const failure of failures) console.error(` - ${failure}`);
  process.exit(1);
}
console.log("onboarding: three steps shown for a reason, the scan runs itself, "
  + "and the drawer names the LAN switch before it sets it");
