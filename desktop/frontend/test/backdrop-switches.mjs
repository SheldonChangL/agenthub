// The backdrop is two switches, and the expensive half starts off (#156).
//
// Measured on an Intel HD 520 (WebKitGTK, the desktop app's own build): the
// window sat at 101.7% of a core with the rain running and 2.4% with it off,
// the difference being 56 separately animated, glowing columns. An earlier
// version of this file tested an automatic degrade that measured frame pacing
// and dropped the rain by itself. That is gone: it changed the background out
// from under the owner, and once it had degraded it remembered the decision,
// so the machine never drew what was asked for again.
//
// What has to hold instead:
//
// 1. The rain is off until it is asked for — including on a stored preferences
//    file that predates the switch, where a missing key must not read as on.
// 2. Turning it on turns it on, and it is remembered.
// 3. The photo is independent of it, and turning the backdrop off takes both.
// 4. The columns are not built while the rain is off: an owner who never asks
//    for it should not carry 56 columns of DOM.
// 5. The settings page says what the rain costs, so leaving it off is a
//    decision and turning it on is not a surprise.
//
//   node frontend/test/backdrop-switches.mjs

import { document } from "./dom-shim.mjs";

globalThis.document = document;
globalThis.setInterval = () => 0;

const store = new Map();
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => store.set(k, String(v)),
};

const { configure, boot } = await import("../src/app.js");

const failures = [];
const el = (id) => document.getElementById(id);
const noop = async () => ({});
configure({
  Overview: async () => ({ reachable: true, node: {}, sessions: [], nodes: [], peers: [], counts: {} }),
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop, SetNodeAddress: noop,
  Pairing: async () => ({ availability: "unknown", candidates: [] }), OpenPairing: noop, ClosePairing: noop,
  Inbox: noop, ClearInbox: noop, MCPConfig: noop, CopyText: noop, Outbound: noop, Wakes: noop,
  ServiceStatus: async () => ({ supported: true, installed: false }), InstallService: noop, UninstallService: noop,
  LocalAddresses: async () => [], NodeSettings: async () => ({ error: "not needed" }), SaveNodeSettings: noop,
  RestartService: noop, HostPlatform: async () => "linux",
});
const app = boot({ start: false });
const UI_KEY = "agenthub.desktop.ui.v1";

// 1. Off out of the box.
if (app.state.ui.motion !== false) failures.push("the rain is on before anyone asked for it");
if (app.backdropPlan().rain) failures.push("the plan draws the rain with the switch off");
if (!app.backdropPlan().photo) failures.push("the photo is off, and the photo is the cheap half");

// 1b. A stored file written before the switch existed has no motion key. The
//     rain must stay off there: `!== false` would read a missing key as on,
//     which is exactly how every existing install would get the expensive
//     background back.
store.set(UI_KEY, JSON.stringify({ backdrop: true }));
app.state.ui.motion = true;
app.loadPrefs();
if (app.state.ui.motion !== false) {
  failures.push("a stored file with no motion key turned the rain on");
}

// 1c. And a file that remembers it degraded does not resurrect the tier.
store.set(UI_KEY, JSON.stringify({ backdrop: true, motion: true, autoTier: "plain" }));
app.loadPrefs();
if (app.state.ui.motion !== true) failures.push("an explicit motion:true was not honoured");
if (!app.backdropPlan().photo) {
  failures.push("a remembered autoTier still suppresses the photo; the tier is supposed to be gone");
}

// 2. The switch works, and is written down.
store.clear();
el("toggle-motion").onchange({ target: { checked: true } });
if (!app.backdropPlan().rain) failures.push("turning the rain on did not turn it on");
if (!JSON.parse(store.get(UI_KEY) ?? "{}").motion) failures.push("the rain being on was not remembered");
if (String(store.get(UI_KEY)).includes("autoTier")) {
  failures.push("autoTier is still being written to storage");
}

// 3. The photo switch takes both halves with it.
el("toggle-backdrop").onchange({ target: { checked: false } });
const off = app.backdropPlan();
if (off.photo || off.rain) failures.push("turning the backdrop off left something being drawn");
if (!app.describeBackdropState().includes("已關閉")) {
  failures.push("the settings page does not say the owner turned it off");
}
el("toggle-backdrop").onchange({ target: { checked: true } });
if (!app.backdropPlan().rain) failures.push("the rain did not come back with the backdrop it belongs to");

// 4. No columns while it is off. The rain element is shared, so this checks the
//    build is what is being deferred, not merely that it is hidden.
const rain = el("rain");
rain.children.splice(0, rain.children.length);
app.state.ui.motion = false;
app.state.ui.backdrop = true;
app.applyBackdrop();
if (rain.children.length !== 0) failures.push("columns were built for a rain nobody asked for");
app.state.ui.motion = true;
app.applyBackdrop();
if (rain.children.length === 0) failures.push("the columns were never built once the rain was on");
const built = rain.children.length;
app.applyBackdrop();
if (rain.children.length !== built) failures.push("the columns were built twice");

// 5. The cost is stated, both ways round, so the switch is a decision.
const on = app.describeBackdropState();
if (!on.includes("CPU")) failures.push(`the settings page does not say what the rain costs: ${on}`);
app.state.ui.motion = false;
const said = app.describeBackdropState();
if (!said.includes("CPU") || !said.includes("預設關閉")) {
  failures.push(`the settings page does not explain why the rain is off: ${said}`);
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("backdrop: the rain is opt-in, remembered, built only when on, and its cost is stated");
