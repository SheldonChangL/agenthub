// The first launch, around the wizard: the pairing drawer's own first step,
// the scan the window runs for itself, and the settings read behind both.
//
// The checklist this file used to cover is gone; the first-run wizard that
// replaced it is covered by first-run.mjs. What is left here are the joins the
// checklist used to lean on and the wizard leans on now: 「can this machine be
// reached」 is the pairing drawer's first step, so the §7.8 rule 4 assertions
// about naming 「allow LAN connections」 before setting it follow the repair
// buttons there; and scanning for sessions is done by the window on the first
// read that reaches the node.
//
//   node frontend/test/onboarding.mjs

import { document } from "./dom-shim.mjs";
import { latestToast, toastNodes } from "./fixtures/toasts.mjs";
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
// The newest toast, which is where a write says what it did.
const toast = () => latestToast(document);
const noop = async () => ({});

let calls = { Discover: 0, InstallService: 0, SaveNodeSettings: 0, RestartNode: 0, Pairing: 0 };
// What Pairing() answers, and every OpenPairing the window made. Unknown by
// default, which asks for no window; section 5 swaps in a node that has one.
let pairingAnswer = { availability: "unknown", candidates: [] };
const openPairingCalls = [];
// Set to make the next OpenPairing refuse, as a node does when it cannot open one.
let openPairingRefusal = null;

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
  Pairing: async () => { calls.Pairing += 1; return pairingAnswer; },
  OpenPairing: async (seconds) => {
    openPairingCalls.push(seconds);
    if (openPairingRefusal) throw new Error(openPairingRefusal);
    return { open: true };
  },
  ClosePairing: noop, Inbox: noop, ClearInbox: noop, MCPConfig: noop, CopyText: noop,
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
const settle = allGood;

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
if (!toast().textContent.includes(ZH["pair.formDirty"])) {
  failures.push(`the refusal was silent: ${toast().textContent}`);
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
// Pressed from inside the open drawer, on a node that can hold a window. The
// save restarts the node and the restart ends its pairing window (#194): the
// drawer has to read that and open one again, or it stands open over a window
// that is gone.
const pairingBefore = app.state.pairing;
el("pairing-modal").classList.remove("hidden");
pairingAnswer = { availability: "on", windowAvailable: true, state: { open: false, peerAddress: "", peerAddressReachable: false }, candidates: [] };
openPairingCalls.length = 0;
const lanRepair = app.pairHereRepairs()[0];
await repairButtons()[0].onclick();
if (calls.SaveNodeSettings !== 1) failures.push(`SaveNodeSettings called ${calls.SaveNodeSettings} times, want 1`);
if (calls.RestartNode !== 1) failures.push(`RestartNode called ${calls.RestartNode} times, want 1`);
if (SETTINGS.saved.peerListen !== "192.168.50.10:7463" || SETTINGS.saved.allowLan !== true) {
  failures.push(`the node was sent ${JSON.stringify(SETTINGS.saved)}, not the address and the flag the label named`);
}
if (openPairingCalls.length !== 1 || openPairingCalls[0] !== 0) {
  failures.push(`after the repair restarted the node the open drawer called OpenPairing ${JSON.stringify(openPairingCalls)}, want [0]`);
}
// And not behind a closed drawer: a window nobody is looking at is one
// nothing on screen would close.
el("pairing-modal").classList.add("hidden");
openPairingCalls.length = 0;
await app.applyPeerListenRepairFromCard(lanRepair);
if (openPairingCalls.length !== 0) {
  failures.push(`a repair pressed with the drawer closed opened a pairing window: ${JSON.stringify(openPairingCalls)}`);
}
// A window the node refuses to reopen after the repair is said after what the
// repair did, not instead of it: the toast that says the save went through is
// still on screen when the refusal arrives as a toast of its own.
el("pairing-modal").classList.remove("hidden");
openPairingCalls.length = 0;
const savedToast = toast();
if (!savedToast.shown || savedToast.textContent === "") {
  failures.push("the repair left nothing on screen to keep");
}
openPairingRefusal = "pairing window: node refused";
await app.applyPeerListenRepairFromCard(lanRepair);
openPairingRefusal = null;
const afterRefusal = toast().textContent;
if (openPairingCalls.length !== 1) failures.push(`the refused reopen was tried ${openPairingCalls.length} times, want 1`);
if (savedToast.node && !toastNodes(document).includes(savedToast.node)) {
  failures.push(`a refused reopen took away what the save said: ${savedToast.textContent}`);
}
if (!afterRefusal.includes("node refused")) {
  failures.push(`a refused reopen was not said: ${afterRefusal}`);
}
if (toast().className.includes("ok")) failures.push("a toast carrying a failure is drawn as a success");
el("pairing-modal").classList.add("hidden");
pairingAnswer = { availability: "unknown", candidates: [] };
app.state.pairing = pairingBefore;

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
  if (!toast().textContent.includes(ZH["app.rescanNothingFound"].trim())) {
    failures.push(`an empty scan did not explain where AgentHub looks: ${toast().textContent}`);
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
if (!toast().textContent.includes(ZH["nodeSettings.autoWake"])) {
  failures.push(`the refusal did not name the dirty field: ${toast().textContent}`);
}
if (!toast().textContent.includes(ZH["pair.formDirty"])) {
  failures.push(`the refusal did not say which two buttons undo it: ${toast().textContent}`);
}
el("node-autowake").checked = false;
bindings.LocalAddresses = async () => ADDRESSES;
configure(bindings);

if (failures.length > 0) {
  for (const failure of failures) console.error(` - ${failure}`);
  process.exit(1);
}
console.log("onboarding: the scan runs itself, and the drawer names the LAN switch before it sets it");
