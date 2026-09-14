// The backdrop degrades itself on a machine that cannot draw it (#156).
//
// Four things have to hold, and each of them is a way this could be worse than
// no degrade at all:
//
// 1. The decision comes from measured frames, not from a platform string. The
//    same GPU is fast with one window open and slow with twenty.
// 2. It never climbs back on its own. A machine that manages 60fps for two
//    quiet seconds would otherwise turn the rain back on, fail again, and leave
//    the background flickering.
// 3. It never rewrites the owner's switches. Their switch says what they asked
//    for; a separate sentence says what is being drawn and why it differs. A
//    degrade that moved the switch would read as a broken switch.
// 4. A hidden window is not evidence. requestAnimationFrame is throttled or
//    stopped there, so measuring then would judge every machine unable to draw
//    its own background — and the window already pauses the rain when hidden,
//    so there would be nothing to measure anyway.
//
//   node frontend/test/backdrop-degrade.mjs

import { document } from "./dom-shim.mjs";

globalThis.document = document;
globalThis.setInterval = () => 0;

// A frame clock the test drives: `frameMs` is what the machine is pretending to
// manage, and `hidden` is the window going away mid-measurement.
let frameMs = 16;
// A queue lets one calibrate() see a slow measurement and then a fast one,
// which is the only way to land on the middle tier.
let framePlan = null;
const nextFrameMs = () => {
  if (Array.isArray(framePlan) && framePlan.length > 0) return framePlan[0].shift?.() ?? framePlan.shift();
  return frameMs;
};
let hidden = false;
Object.defineProperty(document, "hidden", { get: () => hidden, configurable: true });
let now = 0;
globalThis.requestAnimationFrame = (fn) => {
  now += nextFrameMs();
  setTimeout(() => fn(now), 0);
  return 1;
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

const reset = () => {
  app.state.ui.backdrop = true;
  app.state.ui.motion = true;
  app.state.autoTier = null;
  app.state.autoReason = "";
};

// 1. A machine that keeps up is left alone.
reset();
frameMs = 16;
await app.calibrateBackdrop();
if (app.state.autoTier !== null) {
  failures.push(`a machine drawing at ${frameMs}ms a frame was degraded to ${app.state.autoTier}`);
}
if (!app.backdropPlan().rain) failures.push("the rain was dropped on a machine that keeps up");

// 2. A machine that cannot keep up loses the rain first, then the photo.
reset();
frameMs = 45;
await app.calibrateBackdrop();
if (app.state.autoTier !== "plain") {
  failures.push(`a machine at 45ms a frame ended at ${app.state.autoTier}, want plain after two steps`);
}
const plan = app.backdropPlan();
if (plan.rain || plan.photo) failures.push("the backdrop is still being drawn on a machine that cannot draw it");

// 3. The owner's switches are untouched; a sentence explains the difference.
if (app.state.ui.backdrop !== true || app.state.ui.motion !== true) {
  failures.push("the degrade rewrote the owner's switches, which would read as a broken switch");
}
const said = app.describeBackdropState();
if (!said.includes("跟不上") || !said.includes("fps")) {
  failures.push(`the settings page does not say what happened or why: ${said}`);
}
if (!said.includes("重新打開")) failures.push("the owner was not told how to ask for it again");

// 4. It does not climb back on its own, however fast the machine becomes.
frameMs = 8;
await app.calibrateBackdrop();
if (app.state.autoTier !== "plain") {
  failures.push("the backdrop climbed back on its own, which is how the flicker starts");
}

// 4b. The same at the middle tier, which is where a climb-back would actually
//     be reachable: the photo is still being drawn, so there is something to
//     measure, and a quiet couple of seconds would otherwise turn the rain back
//     on and start the flicker.
reset();
// Slow enough to drop the rain, then fast enough to keep the photo.
framePlan = [[...Array(80).fill(45), ...Array(200).fill(10)]];
await app.calibrateBackdrop();
framePlan = null;
if (app.state.autoTier !== "photo") {
  failures.push(`want the middle tier after slow-then-fast, got ${app.state.autoTier}`);
}
if (!app.backdropPlan().photo || app.backdropPlan().rain) {
  failures.push("the middle tier is not photo-without-rain");
}
frameMs = 8;
await app.calibrateBackdrop();
if (app.state.autoTier !== "photo") {
  failures.push(`a fast measurement at the middle tier climbed back to ${app.state.autoTier}`);
}
if (app.backdropPlan().rain) failures.push("the rain came back on its own");

// 5. Only the owner raises it, and that measures again rather than assuming.
frameMs = 16;
el("toggle-backdrop").checked = true;
await el("toggle-backdrop").onchange({ target: { checked: true } });
await new Promise((resolve) => setTimeout(resolve, 20));
if (app.state.autoTier !== null) {
  failures.push("asking for it again did not clear what was measured last time");
}

// 6. A hidden window is not evidence. This is the one that would degrade every
//    machine: rAF stops when hidden, so the intervals look enormous.
reset();
hidden = true;
frameMs = 500;
await app.calibrateBackdrop();
if (app.state.autoTier !== null) {
  failures.push("a hidden window was read as a machine that cannot draw");
}
if (await app.measureFramePacing() !== null) {
  failures.push("measuring a hidden window returned a number instead of refusing");
}
hidden = false;

// 7. Nothing to draw, nothing to measure.
reset();
app.state.ui.backdrop = false;
frameMs = 45;
await app.calibrateBackdrop();
if (app.state.autoTier !== null) {
  failures.push("a backdrop the owner had already turned off was still degraded");
}
if (!app.describeBackdropState().includes("已關閉")) {
  failures.push("the settings page does not say the owner turned it off");
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("backdrop: degrades on measured frames, never climbs back, never rewrites the owner's switch");
