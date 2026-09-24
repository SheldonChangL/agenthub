// The listen address list (ADR-005 §5): a node that knows `peerListens` gets
// one row per address instead of the dropdown, and the four rules of
// docs/ui-contract.md §7.8 hold over the list, compared as sets.
//
// 1. The baseline is `saved.peerListens`; the same set is not sent.
// 2. Loopback is decided by the host, never the port, entry by entry.
// 3. Whether a save took is observed: the set the node holds after the
//    restart, and each saved address the node reports not open, said in a
//    sentence of its own.
// 4. The form makes no choice for the owner: an address ticks no switch and
//    the switch ticks no address — and rendering writes no field at all.
//
// Also: an older node (no `peerListens`) keeps the dropdown and is sent only
// `peerListen`; the six row states; a unit that pins peer-listen is asked
// about; pairing step 1 lists every open address and offers 「全部開放」; and a
// paired machine's detail shows its backup addresses.
//
//   node frontend/test/listen-addresses.mjs

import { document } from "./dom-shim.mjs";
import { answerConfirms } from "./fixtures/confirm-dialog.mjs";
import { TEXT as ZH } from "../src/i18n/zh-Hant.js";
import { TEXT as EN } from "../src/i18n/en.js";

globalThis.document = document;
globalThis.setInterval = () => 0;
const { configure, boot } = await import("../src/app.js");

const failures = [];
const el = (id) => document.getElementById(id);
const noop = async () => ({});
const fill = (text, values) => text.replace(/\{(\w+)\}/g, (_, key) => values[key]);

const A = "192.168.50.10:7463"; // en0, private
const B = "10.0.0.5:7463"; // en7, private
const C = "122.122.0.7:7463"; // en5, not private
const D = "100.64.3.4:7463"; // en6, not private
const P = "172.16.0.9:7463"; // en8, private
const G = "192.168.60.3:7463"; // saved, not on this machine now

const addresses = [
  { interface: "en0", address: "192.168.50.10", subnet: "192.168.50.0/24", private: true },
  { interface: "en7", address: "10.0.0.5", subnet: "10.0.0.0/24", private: true },
  { interface: "en8", address: "172.16.0.9", subnet: "172.16.0.0/16", private: true },
  { interface: "en5", address: "122.122.0.7", subnet: "122.122.0.0/16", private: false },
  { interface: "en6", address: "100.64.3.4", subnet: "100.64.0.0/10", private: false },
];

let settingsAnswer = async () => ({ settings: {}, sources: {}, saved: {} });
let pairingAnswer = () => ({ availability: "unknown", candidates: [] });
let saveAnswer = async () => ({ settings: {}, sources: {}, saved: {} });
let saveCalls = [];
let installCalls = [];
let serviceStatus = { supported: true, installed: true, running: true, pid: 1, unitPath: "/u", logHint: "/l", nodeAnswering: true };

configure({
  Overview: async () => ({ reachable: true, node: {}, sessions: [], nodes: [], peers: [], counts: {} }),
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop, SetNodeAddress: noop,
  Pairing: async () => pairingAnswer(), OpenPairing: noop, ClosePairing: noop,
  Inbox: noop, ClearInbox: noop, MCPConfig: noop, CopyText: noop, Outbound: noop, Wakes: noop,
  ServiceStatus: async () => serviceStatus,
  InstallService: async (form) => { installCalls.push(form); return { command: "ah service install", output: "installed" }; },
  UninstallService: noop,
  LocalAddresses: async () => addresses,
  NodeSettings: (...a) => settingsAnswer(...a),
  SaveNodeSettings: (...a) => { saveCalls.push(a[0]); return saveAnswer(...a); },
  RestartNode: async () => ({ command: "ah service restart", output: "restarted" }),
});
const app = boot({ start: false });
app.state.service = serviceStatus;
let confirmations = [];
let confirmAnswer = false;
answerConfirms(document, (question) => { confirmations.push(question); return confirmAnswer; });

const tick = () => new Promise((resolve) => setTimeout(resolve, 0));
const rows = () => app.peerListenRows();
const row = (address) => rows().find((entry) => entry.address === address);
const toggle = (address, on) => {
  const entry = row(address);
  if (!entry) {
    failures.push(`no row for ${address}`);
    return;
  }
  entry.box.checked = on;
  entry.box.onchange();
};
const combination = () => el("node-settings-combination").serialize();
const view = (saved, extra = {}) => ({
  settings: { peerListen: saved[0], peerListens: saved, allowLan: true, discover: false, treatAsPrivate: [], autoWake: false, ...(extra.settings ?? {}) },
  sources: { peerListen: "remembered", allowLan: "remembered", discover: "default", treatAsPrivate: "default", autoWake: "default" },
  saved: { peerListen: saved[0], peerListens: saved, allowLan: true, discover: false, treatAsPrivate: [], autoWake: false, ...(extra.saved ?? {}) },
  restartRequired: false,
  peerListeners: extra.peerListeners ?? saved.map((address) => ({ address, state: "bound" })),
  ...(extra.top ?? {}),
});
const load = async (answer) => {
  settingsAnswer = async () => answer;
  await app.loadNodeSettings();
  await tick();
};

// 1. A read builds the list: every private address, the others under their
//    own heading, a saved address this machine does not have still ticked,
//    each with the node's state for it — and nothing written to any field.
//    Six states: open, opens after restart, closes after restart, and three
//    kinds of not open.
await load({
  settings: { peerListen: A, peerListens: [A, G, P, C], allowLan: true, discover: true, treatAsPrivate: ["122.122.0.0/16"], autoWake: false },
  sources: { peerListen: "remembered", allowLan: "remembered", discover: "default", treatAsPrivate: "remembered", autoWake: "default" },
  saved: { peerListen: A, peerListens: [A, B, G, P], allowLan: true, discover: false, treatAsPrivate: ["122.122.0.0/16"], autoWake: false },
  restartRequired: true,
  peerListeners: [
    { address: A, state: "bound" },
    { address: G, state: "failed", reason: "address_gone", detail: "bind: can't assign requested address", message: "gone" },
    { address: P, state: "failed", reason: "port_in_use", detail: "bind: address already in use", message: "held" },
    { address: C, state: "bound" },
  ],
});
if (!el("node-peerlisten-field").classList.contains("hidden")) failures.push("the dropdown is still on screen for a node that takes a list");
if (el("node-peerlistens-field").classList.contains("hidden")) failures.push("the list is hidden for a node that takes one");
const expectRows = [A, B, P, G, C, D];
const shown = rows().map((entry) => entry.address);
if (JSON.stringify(shown) !== JSON.stringify(expectRows)) {
  failures.push(`rows = ${JSON.stringify(shown)}, want ${JSON.stringify(expectRows)} (private first, the saved-but-gone one kept, the rest after)`);
}
for (const address of expectRows) {
  const want = [A, B, G, P].includes(address);
  if (row(address) && row(address).box.checked !== want) {
    failures.push(`${address} is ${row(address).box.checked ? "ticked" : "unticked"}; only saved addresses are ticked`);
  }
}
const states = {
  [A]: ZH["nodeSettings.rowOpen"],
  [B]: ZH["nodeSettings.rowOpensAfterRestart"],
  [C]: ZH["nodeSettings.rowClosesAfterRestart"],
  [G]: ZH["nodeSettings.rowNotOpenGone"],
  [P]: ZH["nodeSettings.rowNotOpenPort"],
  [D]: "",
};
for (const [address, want] of Object.entries(states)) {
  if (row(address) && row(address).status.textContent !== want) {
    failures.push(`${address} says "${row(address).status.textContent}", want "${want}"`);
  }
}
const listMarkup = el("node-peerlistens").serialize();
if (!listMarkup.includes(ZH["nodeSettings.rowGone"])) failures.push("the saved address this machine lacks is not marked as such");
if (!listMarkup.includes(ZH["nodeSettings.peerListensOther"])) failures.push("the non-private addresses have no heading of their own");
if (!listMarkup.includes(ZH["nodeSettings.optionNotPrivate"])) failures.push("a non-private row does not say it needs a declared range");
if (!listMarkup.includes(ZH["nodeSettings.otherNeedsRange"])) failures.push("nothing says the other machine must declare the same range");
if (listMarkup.indexOf(ZH["nodeSettings.peerListensOther"]) > listMarkup.indexOf(C)) failures.push("the heading comes after the row it heads");
if (row(A)?.mark.textContent !== ZH["nodeSettings.rowBroadcast"]) failures.push("the preferred row does not say it is broadcast from");
if (rows().some((entry) => entry.address !== A && entry.mark.textContent !== "")) failures.push("a second row claims to be broadcast from");
if (!el("node-peerlistens-none").classList.contains("hidden")) failures.push("the only-this-machine note shows over ticked network addresses");
// Only rendered: nothing to send, and the switches are the saved ones.
if (Object.keys(app.readNodeSettingsPatch()).length !== 0) {
  failures.push(`a read alone produced a patch: ${JSON.stringify(app.readNodeSettingsPatch())}`);
}
if (!el("node-allow-lan").checked) failures.push("a read changed allowLan");
if (el("node-private").value !== "122.122.0.0/16") failures.push(`a read changed the ranges to ${el("node-private").value}`);

// "Broadcast from here" is the node's fact, as pairing.ListenerEndpoint makes
// it: the first network address the running node reports bound, and only on a
// node running with -discover. Not the first ticked one.
await load(view([A, B], {
  settings: { discover: true },
  peerListeners: [
    { address: A, state: "failed", reason: "address_gone", message: "gone" },
    { address: B, state: "bound" },
  ],
}));
if (row(B)?.mark.textContent !== ZH["nodeSettings.rowBroadcast"] || row(A)?.mark.textContent !== "") {
  failures.push(`with the first saved address failed, the mark reads A="${row(A)?.mark.textContent}" B="${row(B)?.mark.textContent}", want it on B, the first bound`);
}
await load(view([A, B], { settings: { discover: true }, peerListeners: [
  { address: A, state: "pending" }, { address: B, state: "failed", reason: "port_in_use", message: "held" },
] }));
if (rows().some((entry) => entry.mark.textContent !== "")) failures.push("a row claims to be broadcast from while nothing is bound");
await load(view([A, B], { settings: { discover: false }, saved: { discover: true } }));
if (rows().some((entry) => entry.mark.textContent !== "")) failures.push("a row claims to be broadcast from on a node running without -discover");

// The third kind of not open carries the node's own sentence.
await load(view([A, B], { peerListeners: [
  { address: A, state: "bound" },
  { address: B, state: "failed", reason: "unusable", detail: "bind: permission denied", message: "this machine will not serve 10.0.0.5:7463" },
] }));
if (row(B)?.status.textContent !== fill(ZH["nodeSettings.rowNotOpenOther"], { message: "this machine will not serve 10.0.0.5:7463" })) {
  failures.push(`an unusable address says "${row(B)?.status.textContent}", want the node's message`);
}
if (!String(row(B)?.status.className).includes("warn")) failures.push("a failed row is not styled as a warning");

// 2. Rule 1 — the set, against saved. Unticking and re-ticking the preferred
//    one is the same set and the same order.
await load(view([A, B]));
toggle(A, false);
toggle(A, true);
if (Object.keys(app.readNodeSettingsPatch()).length !== 0) {
  failures.push(`the same set was sent: ${JSON.stringify(app.readNodeSettingsPatch())}`);
}
if (JSON.stringify(app.checkedPeerListens()) !== JSON.stringify([A, B])) {
  failures.push(`re-ticking moved the preferred address: ${JSON.stringify(app.checkedPeerListens())}`);
}
if (!app.samePeerListens([B, A], [A, B]) || app.samePeerListens([A], [A, B]) || !app.samePeerListens([], ["127.0.0.1:7463"])) {
  failures.push("samePeerListens is not a set comparison with an empty list meaning the default");
}

// Two ticked is sent as the list, never as the scalar.
await load(view([A]));
toggle(B, true);
let patch = app.readNodeSettingsPatch();
if (JSON.stringify(patch) !== JSON.stringify({ peerListens: [A, B] })) {
  failures.push(`ticking a second address sent ${JSON.stringify(patch)}, want peerListens [A, B] alone`);
}
saveCalls = [];
saveAnswer = async () => view([A, B], { top: { restartRequired: true } });
settingsAnswer = async () => view([A, B]);
await app.saveNodeSettings();
await tick();
if (saveCalls.length !== 1 || JSON.stringify(saveCalls[0]) !== JSON.stringify({ peerListens: [A, B] })) {
  failures.push(`the save sent ${JSON.stringify(saveCalls)}, want one write of peerListens [A, B]`);
}

// Down to one is still the list: the scalar would be read as a whole
// replacement by a new node too, but a window that sends it is one that has
// lost track of which node it is talking to.
await load(view([A, B]));
toggle(B, false);
patch = app.readNodeSettingsPatch();
if (JSON.stringify(patch) !== JSON.stringify({ peerListens: [A] })) {
  failures.push(`one address left sent ${JSON.stringify(patch)}, want peerListens [A]`);
}
// Nothing ticked is loopback only, and the form says what that means.
toggle(A, false);
patch = app.readNodeSettingsPatch();
if (JSON.stringify(patch) !== JSON.stringify({ peerListens: ["127.0.0.1:7463"] })) {
  failures.push(`nothing ticked sent ${JSON.stringify(patch)}, want peerListens [127.0.0.1:7463]`);
}
if (el("node-peerlistens-none").classList.contains("hidden")) failures.push("nothing ticked does not say only this machine can connect");
if (el("node-peerlistens-none").textContent !== ZH["nodeSettings.peerListensNoneDraft"]) {
  failures.push(`nothing ticked over a saved network address reads "${el("node-peerlistens-none").textContent}", want what saving would do`);
}
if (rows().some((entry) => entry.mark.textContent !== "")) failures.push("a row claims to be broadcast from with nothing ticked");

// 3. Rule 2 — loopback by the host. A saved 127.0.0.1:9999 with allowLan off
//    is off the network: its row is kept and ticked, and nothing warns about a
//    withdrawal or a refusal.
await load(view(["127.0.0.1:9999"], { saved: { allowLan: false }, settings: { allowLan: false } }));
if (!row("127.0.0.1:9999")?.box.checked) failures.push("a non-default loopback address lost its row or its tick");
if (!el("node-peerlistens").serialize().includes(ZH["nodeSettings.optionLoopbackOtherPort"])) {
  failures.push("the loopback row is not marked as this machine only");
}
if (combination().includes(ZH["nodeSettings.warnWithdraw"].slice(0, 6)) || combination().includes("127.0.0.1:9999")) {
  failures.push(`a loopback address was judged by its port: ${combination()}`);
}
if (el("node-peerlistens-none").classList.contains("hidden")) failures.push("a loopback-only list does not say only this machine can connect");
if (el("node-peerlistens-none").textContent !== ZH["nodeSettings.peerListensNone"]) {
  failures.push(`a saved loopback-only list reads "${el("node-peerlistens-none").textContent}", want the present tense`);
}
if (Object.keys(app.readNodeSettingsPatch()).length !== 0) failures.push("a loopback list with nothing changed was sent");
// Beside a network address the node refuses it, and the form says so first.
toggle(A, true);
if (!combination().includes(fill(ZH["nodeSettings.warnMixLoopback"], { address: "127.0.0.1:9999" }))) {
  failures.push(`loopback beside a network address was not warned about: ${combination()}`);
}

// 4. Rule 4 — no choice made for the owner. Ticking an address with the
//    switch off leaves it off and says the node will refuse; ticking the
//    switch ticks nothing; unticking it unticks nothing, and says what the
//    node will withdraw.
await load(view([A], { saved: { allowLan: false }, settings: { allowLan: false } }));
toggle(B, true);
if (el("node-allow-lan").checked) failures.push("ticking an address turned allowLan on");
if (!combination().includes(fill(ZH["nodeSettings.warnLanOff"], { address: `${A}, ${B}` }))) {
  failures.push(`a named address with allowLan off was not warned about: ${combination()}`);
}
await load(view([A]));
el("node-allow-lan").checked = false;
el("node-allow-lan").onchange?.();
app.syncNodeSettingsForm();
if (!row(A)?.box.checked) failures.push("unticking allowLan unticked an address");
if (!combination().includes(fill(ZH["nodeSettings.warnWithdraw"], { stored: A }))) {
  failures.push(`turning allowLan off did not warn about the withdrawal: ${combination()}`);
}
await load(view(["127.0.0.1:7463"], { saved: { allowLan: false }, settings: { allowLan: false } }));
el("node-allow-lan").checked = true;
app.syncNodeSettingsForm();
if (rows().some((entry) => entry.box.checked)) failures.push("ticking allowLan ticked an address");
if (JSON.stringify(app.readNodeSettingsPatch()) !== JSON.stringify({ allowLan: true })) {
  failures.push(`ticking allowLan alone sent ${JSON.stringify(app.readNodeSettingsPatch())}`);
}

// A non-private address: its own subnet is offered when ticked, the most
// recently ticked one wins, and unticking it takes its suggestion with it.
await load(view([A]));
app.state.nodePrivateSuggested = "";
toggle(C, true);
if (el("node-private").value !== "122.122.0.0/16") failures.push(`ticking ${C} suggested ${el("node-private").value}`);
if (!el("node-private-note").textContent.includes(ZH["nodeSettings.otherNeedsRange"])) {
  failures.push("the suggestion does not say the other machine needs the same range");
}
toggle(D, true);
if (el("node-private").value !== "100.64.0.0/10") failures.push(`the most recent non-private tick did not lead: ${el("node-private").value}`);
toggle(D, false);
if (el("node-private").value !== "122.122.0.0/16") failures.push(`unticking ${D} left ${el("node-private").value}`);
toggle(C, false);
if (el("node-private").value !== "") failures.push(`unticking every non-private address left the suggestion ${el("node-private").value}`);

// 5. Rule 3 — observed. The set the node holds after the restart decides,
//    order aside; and a saved address the node reports not open is named in
//    a sentence of its own, with the banner no longer marked a success.
if (app.didNotStick({ peerListens: [A, B] }, { peerListens: [B, A] }).length !== 0) {
  failures.push("the same set in another order was reported as not kept");
}
const lost = app.didNotStick({ peerListens: [A, B] }, { peerListens: [A] });
if (lost.length !== 1 || lost[0] !== ZH["nodeSettings.peerListensLabel"]) {
  failures.push(`a dropped address was reported as ${JSON.stringify(lost)}, want the list's own label`);
}
await load(view([A]));
toggle(B, true);
saveAnswer = async () => view([A, B], { top: { restartRequired: true } });
settingsAnswer = async () => view([A]);
await app.saveNodeSettings();
await tick();
if (!el("banner").textContent.includes(fill(ZH["nodeSettings.savedDidNotStick"], { lost: ZH["nodeSettings.peerListensLabel"] }))) {
  failures.push(`a list the restart undid was not reported: ${el("banner").textContent}`);
}
await load(view([A]));
toggle(B, true);
saveAnswer = async () => view([A, B], { top: { restartRequired: true } });
settingsAnswer = async () => view([A, B], { peerListeners: [
  { address: A, state: "bound" },
  { address: B, state: "failed", reason: "address_gone", message: "gone" },
] });
await app.saveNodeSettings();
await tick();
const said = el("banner").textContent;
if (!said.includes(ZH["nodeSettings.savedServiceAnswering"]) ||
    !said.includes(fill(ZH["nodeSettings.savedNotOpen"], { addresses: B }))) {
  failures.push(`a saved address that did not open was not said on its own: ${said}`);
}
if (String(el("banner").className).includes("ok")) failures.push("a save with an address not open was styled as a plain success");
settingsAnswer = async () => view([A, B]);
await load(view([A]));
toggle(B, true);
await app.saveNodeSettings();
await tick();
if (el("banner").textContent.includes(fill(ZH["nodeSettings.savedNotOpen"], { addresses: B }).slice(0, 8))) {
  failures.push(`every address open still produced the not-open sentence: ${el("banner").textContent}`);
}

// Every failed is still the node's own problem banner, with its repairs.
await load(view([A, B], {
  settings: { peerListen: "127.0.0.1:7463" },
  peerListeners: [
    { address: A, state: "failed", reason: "address_gone", message: "gone" },
    { address: B, state: "failed", reason: "address_gone", message: "gone" },
  ],
  top: { peerListenProblem: { address: A, reason: "address_gone", detail: "bind: can't assign requested address", runningOn: "127.0.0.1:7463", message: "gone" } },
}));
const notice = el("node-settings-notice").serialize();
if (!notice.includes(fill(ZH["nodeSettings.problemAddressGone"], { address: A, runningOn: "127.0.0.1:7463" }))) {
  failures.push("every address failing no longer shows the node's problem banner");
}
if (!notice.includes(ZH["nodeSettings.repairLoopback"])) failures.push("the problem banner lost its repairs");
if (row(A)?.status.textContent !== ZH["nodeSettings.rowNotOpenGone"] || row(B)?.status.textContent !== ZH["nodeSettings.rowNotOpenGone"]) {
  failures.push("the rows under the banner do not say each address is not open");
}

// A repair that moves every entry to the next port keeps the saved order: the
// preferred address is the owner's, not the interface list's.
await load(view([B, A], {
  settings: { peerListen: "127.0.0.1:7463" },
  peerListeners: [
    { address: B, state: "failed", reason: "port_in_use", message: "held" },
    { address: A, state: "failed", reason: "port_in_use", message: "held" },
  ],
  top: { peerListenProblem: { address: B, reason: "port_in_use", detail: "bind: address already in use", runningOn: "127.0.0.1:7463", message: "held" } },
}));
const portRepair = app.peerListenRepairs(app.state.nodeSettings.peerListenProblem, { list: addresses }, true)[0];
if (JSON.stringify(portRepair?.peerListens) !== JSON.stringify(["10.0.0.5:7464", "192.168.50.10:7464"])) {
  failures.push(`the next-port repair carries ${JSON.stringify(portRepair)}`);
}
saveCalls = [];
saveAnswer = async () => view(["10.0.0.5:7464", "192.168.50.10:7464"], { top: { restartRequired: true } });
settingsAnswer = async () => view(["10.0.0.5:7464", "192.168.50.10:7464"]);
await app.applyPeerListenRepair(portRepair);
await tick();
if (JSON.stringify(saveCalls[0]?.peerListens) !== JSON.stringify(["10.0.0.5:7464", "192.168.50.10:7464"])) {
  failures.push(`the next-port repair sent ${JSON.stringify(saveCalls[0])}, want the saved order at the new port`);
}

// More than the node serves is said before the save.
await load(view([A]));
for (const address of [B, P, C, D]) toggle(address, true);
if (!combination().includes(fill(ZH["nodeSettings.warnTooMany"], { count: 5 }))) {
  failures.push(`five ticked addresses were not warned about: ${combination()}`);
}
// A saved address this window cannot list (IPv6) that the node reports bound
// is not also called missing.
await load(view(["[fd12::5]:7463"]));
if (el("node-peerlistens").serialize().includes(`<span class="why">${ZH["nodeSettings.rowGone"]}</span>`) ||
    row("[fd12::5]:7463")?.status.textContent !== ZH["nodeSettings.rowOpen"]) {
  failures.push(`a bound IPv6 address reads ${el("node-peerlistens").serialize()}`);
}

// 6. A unit that pins peer-listen: saving the list asks first, naming the
//    list by its own label, and a "no" leaves it out.
await load(view([A]));
app.state.service = { ...serviceStatus, installed: true, pinnedSettings: ["peer-listen"], dbPathKnown: true, dbPath: "/db" };
toggle(B, true);
confirmations = [];
confirmAnswer = false;
saveCalls = [];
installCalls = [];
await app.saveNodeSettings();
await tick();
if (confirmations.length !== 1) {
  failures.push(`saving the list under a unit that pins peer-listen asked ${confirmations.length} times, want once`);
} else if (!confirmations[0].includes(fill(ZH["service.pinnedChanging"], { label: ZH["nodeSettings.peerListensLabel"] }))) {
  failures.push(`the question does not name the list as the field this save changes: ${confirmations[0]}`);
}
if (saveCalls.length !== 0 || installCalls.length !== 0) {
  failures.push(`a refused question still wrote ${JSON.stringify(saveCalls)} / re-registered ${installCalls.length}`);
}
// Unticking everything and refusing the question: nothing was sent, every
// row still says the node serves it, so the note says what saving would do —
// in both languages — rather than that only this machine can connect now.
await load(view([A]));
toggle(A, false);
confirmations = [];
saveCalls = [];
await app.saveNodeSettings();
await tick();
if (confirmations.length !== 1 || saveCalls.length !== 0) {
  failures.push(`unticking everything under the pinned unit asked ${confirmations.length} / wrote ${saveCalls.length}`);
}
if (row(A)?.status.textContent !== ZH["nodeSettings.rowOpen"]) failures.push(`after the refused save A reads "${row(A)?.status.textContent}"`);
if (el("node-peerlistens-none").classList.contains("hidden") ||
    el("node-peerlistens-none").textContent !== ZH["nodeSettings.peerListensNoneDraft"]) {
  failures.push(`after the refused save the note reads "${el("node-peerlistens-none").textContent}", want what saving would do`);
}
app.setUILanguage("en");
if (el("node-peerlistens-none").textContent !== EN["nodeSettings.peerListensNoneDraft"]) {
  failures.push(`in English after the refused save the note reads "${el("node-peerlistens-none").textContent}"`);
}
app.setUILanguage("zh-Hant");
app.state.service = serviceStatus;

// 7. An older node: no `peerListens`, so the dropdown, and only the scalar.
await load({
  settings: { peerListen: A, allowLan: true, discover: false, treatAsPrivate: [], autoWake: false },
  sources: { peerListen: "remembered" },
  saved: { peerListen: A, allowLan: true, discover: false, treatAsPrivate: [], autoWake: false },
  restartRequired: false,
});
if (el("node-peerlisten-field").classList.contains("hidden") || !el("node-peerlistens-field").classList.contains("hidden")) {
  failures.push("an older node was given the list instead of the dropdown");
}
if (rows().length !== 0) failures.push("an older node still has list rows behind the dropdown");
el("node-peerlisten").value = B;
patch = app.readNodeSettingsPatch();
if (JSON.stringify(patch) !== JSON.stringify({ peerListen: B })) {
  failures.push(`an older node was sent ${JSON.stringify(patch)}, want peerListen alone`);
}

// 8. Pairing step 1. Unreachable, two private addresses, a node that takes a
//    list: 「全部開放」 first, naming every address and the switch exactly when
//    it is off; then the single ones, at most three.
app.state.pairing = { windowAvailable: true, state: { open: true, peerAddress: "127.0.0.1:7463", peerAddressReachable: false } };
await load(view(["127.0.0.1:7463"], { saved: { allowLan: false }, settings: { allowLan: false } }));
let options = app.pairHereRepairs();
const all = [A, B, P].join(", ");
if (options[0]?.label !== fill(ZH["nodeSettings.repairAllAndLan"], { list: all })) {
  failures.push(`the first repair is "${options[0]?.label}", want 全部開放 naming ${all} and the switch`);
}
if (JSON.stringify(options[0]?.peerListens) !== JSON.stringify([A, B, P]) || !options[0]?.primary) {
  failures.push(`全部開放 carries ${JSON.stringify(options[0])}`);
}
const singles = options.slice(1).filter((option) => !option.peerListens);
if (singles.length !== 3 || singles.some((option) => option.primary)) {
  failures.push(`after 全部開放 come ${singles.length} single-address buttons (want 3, none primary)`);
}
app.setLanguage("en");
options = app.pairHereRepairs();
if (options[0]?.label !== fill(EN["nodeSettings.repairAllAndLan"], { list: all })) {
  failures.push(`in English the first repair is "${options[0]?.label}"`);
}
app.setLanguage("zh-Hant");
app.renderPairHere();
const hereNote = el("pair-here-note").serialize();
if (!hereNote.includes(fill(ZH["nodeSettings.repairAllAndLan"], { list: all }))) {
  failures.push("the drawer does not show 全部開放");
}
// Pressed: the whole list and the switch, through the form's own save, and
// rendering the drawer before that wrote neither.
if (el("node-allow-lan").checked || rows().some((entry) => entry.box.checked)) {
  failures.push("rendering the drawer wrote the settings form");
}
saveCalls = [];
saveAnswer = async () => view([A, B, P], { top: { restartRequired: true } });
settingsAnswer = async () => view([A, B, P]);
await app.applyPeerListenRepairFromCard(options[0]);
await tick();
if (saveCalls.length !== 1 || JSON.stringify(saveCalls[0]) !== JSON.stringify({ peerListens: [A, B, P], allowLan: true })) {
  failures.push(`全部開放 sent ${JSON.stringify(saveCalls)}, want peerListens [A, B, P] and allowLan true`);
}
// With the switch already on, the label does not promise to turn it on.
await load(view(["127.0.0.1:7463"], { saved: { allowLan: true }, settings: { allowLan: true } }));
if (app.pairHereRepairs()[0]?.label !== fill(ZH["nodeSettings.repairAll"], { list: all })) {
  failures.push(`with allowLan on the first repair is "${app.pairHereRepairs()[0]?.label}"`);
}
// The set already saved, the switch on, and every address failing with the
// port held: 全部開放 would save nothing and answer "no change". What is
// offered is the node's own reason's repair, the next port up for them all.
await load(view([A, B, P], {
  settings: { peerListen: "127.0.0.1:7463" },
  peerListeners: [A, B, P].map((address) => ({ address, state: "failed", reason: "port_in_use", message: "held" })),
  top: { peerListenProblem: { address: A, reason: "port_in_use", detail: "bind: address already in use", runningOn: "127.0.0.1:7463", message: "held" } },
}));
options = app.pairHereRepairs();
const moved = ["192.168.50.10:7464", "10.0.0.5:7464", "172.16.0.9:7464"];
if (options.some((option) => option.label === fill(ZH["nodeSettings.repairAll"], { list: all }))) {
  failures.push("step 1 offered 全部開放 for the very set already saved with the switch on");
}
if (options[0]?.label !== fill(ZH["nodeSettings.repairPort"], { address: moved.join(", ") }) ||
    JSON.stringify(options[0]?.peerListens) !== JSON.stringify(moved) || !options[0]?.primary) {
  failures.push(`with the port held step 1 first offers ${JSON.stringify(options[0])}, want the next port for every address`);
}
// The same set with the switch off is still a change: it turns the switch on.
await load(view([A, B, P], {
  saved: { allowLan: false }, settings: { peerListen: "127.0.0.1:7463", allowLan: false },
  peerListeners: [A, B, P].map((address) => ({ address, state: "failed", reason: "port_in_use", message: "held" })),
}));
if (app.pairHereRepairs()[0]?.label !== fill(ZH["nodeSettings.repairAllAndLan"], { list: all })) {
  failures.push(`with the switch off the saved set is not offered with it: "${app.pairHereRepairs()[0]?.label}"`);
}
// An older node gets no 全部開放: it cannot be sent a list.
await load({
  settings: { peerListen: "127.0.0.1:7463", allowLan: false }, sources: {},
  saved: { peerListen: "127.0.0.1:7463", allowLan: false, treatAsPrivate: [] }, restartRequired: false,
});
if (app.pairHereRepairs().some((option) => option.peerListens)) failures.push("an older node was offered 全部開放");

// Reachable on two: one row each, with its interface and its own copy
// button, and the sentence that says either works. One configured and not
// open is named, with the way to the rows.
app.state.pairing = { windowAvailable: true, state: { open: true, peerAddress: A, peerAddressReachable: true, notice: "" } };
await load(view([A, B, G], { peerListeners: [
  { address: A, state: "bound" }, { address: B, state: "bound" },
  { address: G, state: "failed", reason: "address_gone", message: "gone" },
] }));
app.renderPairHere();
const value = el("pair-local-address");
const lines = value.children.filter((child) => child && child.className === "pairaddr");
if (lines.length !== 2 || !lines[0].textContent.includes(A) || !lines[0].textContent.includes("en0") ||
    !lines[1].textContent.includes(B) || !lines[1].textContent.includes("en7")) {
  failures.push(`the open addresses read ${value.serialize()}`);
}
if (lines.some((line) => !line.children.some((child) => child.tagName === "button"))) failures.push("an open address has no copy button");
if (!el("copy-pair-address").classList.contains("hidden")) failures.push("the single copy button is still shown over the list");
const reachableNote = el("pair-here-note").serialize();
if (!reachableNote.includes(ZH["pair.hereEither"])) failures.push("nothing says either address works");
if (!reachableNote.includes(fill(ZH["pair.hereNotOpen"], { addresses: G })) || !reachableNote.includes(ZH["pair.hereGoSettings"])) {
  failures.push(`the address that is not open is not named with the way to settings: ${reachableNote}`);
}
// One open: the single address as before, and its one button.
await load(view([A]));
app.renderPairHere();
if (value.textContent !== A || el("copy-pair-address").classList.contains("hidden")) {
  failures.push(`one open address is no longer shown as the address: ${value.serialize()}`);
}

// The drawer reads the node again for step 1 alone: an address that bound
// after the form was read (Wi-Fi joining after login) is listed without
// repainting the settings form over the owner's unsaved ticks.
await load(view([A, B], { peerListeners: [
  { address: A, state: "bound" }, { address: B, state: "failed", reason: "address_gone", message: "gone" },
] }));
toggle(C, true);
const reachablePairing = { availability: "on", windowAvailable: true, candidates: [],
  state: { open: true, peerAddress: A, peerAddressReachable: true, notice: "" } };
pairingAnswer = () => reachablePairing;
app.state.pairing = reachablePairing;
settingsAnswer = async () => view([A, B]);
el("pairing-modal").classList.remove("hidden");
await app.loadPairing();
await tick();
await tick();
app.renderPairHere();
if (el("pair-local-address").children.filter((child) => child && child.className === "pairaddr").length !== 2) {
  failures.push(`step 1 still lists the node's old answer after it bound a second address: ${el("pair-local-address").serialize()}`);
}
if (el("pair-here-note").serialize().includes(fill(ZH["pair.hereNotOpen"], { addresses: B }))) {
  failures.push("step 1 still says an address the node now serves is not open");
}
if (!row(C)?.box.checked) failures.push("refreshing step 1 repainted the settings form over an unsaved tick");
el("pairing-modal").classList.add("hidden");
pairingAnswer = () => ({ availability: "unknown", candidates: [] });

// 9. A paired machine's detail names the backup addresses after the recorded one.
const detail = app.nodeDetail({
  nodeId: "node_peer", displayName: "peer", platform: "linux", fingerprint: "AAAA",
  address: "192.168.50.22:7463", alternateAddresses: ["10.0.0.22:7463", "172.16.0.22:7463"],
});
const detailText = detail.map((part) => part.serialize()).join("");
if (!detailText.includes(ZH["network.detailAlternates"]) || !detailText.includes("10.0.0.22:7463, 172.16.0.22:7463")) {
  failures.push("the node detail does not show the backup addresses");
}
if (!detailText.includes(fill(ZH["network.addressAlternates"], { list: "10.0.0.22:7463, 172.16.0.22:7463" }))) {
  failures.push("the address section does not say the backups are tried next");
}
const bare = app.nodeDetail({ nodeId: "node_peer", displayName: "peer", platform: "linux", fingerprint: "AAAA", address: "192.168.50.22:7463" })
  .map((part) => part.serialize()).join("");
if (bare.includes(ZH["network.detailAlternates"])) failures.push("a machine with no backups shows a backup row");

if (failures.length > 0) {
  console.error("listen addresses:\n  " + failures.join("\n  "));
  process.exit(1);
}
console.log("listen addresses: the list is the saved set, observed after a restart, and chooses nothing for the owner");
