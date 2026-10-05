// Esc on the window's overlays. Before this, only the confirm question, the
// audience menu and the copy fallback listened for it; none of the three
// drawers (inbox, notification log, pairing) nor the four dialogs (heartbeat
// preview, MCP, manual pairing, audience) did, and a click on the backdrop was
// the only way out. overlayKey closes the topmost one per press.
//
// dom-shim's document has no addEventListener, so the exported overlayKey is
// called directly, as confirm-dialog.mjs does with confirmKey.
//
//   node frontend/test/escape-closes.mjs

import { document } from "./dom-shim.mjs";

globalThis.document = document;
globalThis.setInterval = () => 0;

const failures = [];
const el = (id) => document.getElementById(id);
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

const noop = async () => ({});
const { configure, boot } = await import("../src/app.js");
configure({
  Overview: async () => ({ reachable: true, node: {}, sessions: [], nodes: [], peers: [], counts: {} }),
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop,
  Pairing: async () => ({ availability: "unknown", candidates: [] }), OpenPairing: noop, ClosePairing: noop,
  PairRequests: async () => [], CopyText: noop, MCPConfig: noop,
  Inbox: async (sessionId) => ({ sessionId, messages: [] }),
});
const internals = boot({ start: false });
const { overlayKey } = internals;

function press(key = "Escape", defaultPrevented = false) {
  const event = {
    key,
    defaultPrevented,
    prevented: false,
    preventDefault() { this.prevented = true; },
  };
  event.returned = overlayKey(event);
  return event;
}
const isHidden = (id) => el(id).classList.contains("hidden");

const OVERLAYS = ["modal", "mcp-modal", "pair-modal", "audience-modal", "notify-modal", "pairing-modal", "inbox-modal"];

// Nothing is open at the start.
for (const id of OVERLAYS) {
  if (!isHidden(id)) failures.push(`#${id} is open before the test opens anything`);
}

// 1. Each overlay, alone, is closed by Esc.
for (const id of OVERLAYS) {
  el(id).classList.remove("hidden");
  const event = press();
  await settle();
  if (event.returned !== true) failures.push(`Esc with #${id} open did not report handling it`);
  if (!event.prevented) failures.push(`Esc with #${id} open was not preventDefault-ed`);
  if (!isHidden(id)) failures.push(`Esc did not close #${id}`);
}

// 2. Two open: the dialog, which is on top, goes first; the drawer under it
//    needs a second press.
el("inbox-modal").classList.remove("hidden");
el("modal").classList.remove("hidden");
press();
await settle();
if (!isHidden("modal")) failures.push("the first Esc left the dialog open");
if (isHidden("inbox-modal")) failures.push("the first Esc closed the drawer under the dialog as well");
press();
await settle();
if (!isHidden("inbox-modal")) failures.push("the second Esc left the drawer open");

// 3. Nothing open: Esc is not ours. Other keys and an Esc somebody already
//    took (the audience menu, the copy fallback) close nothing.
{
  const event = press();
  if (event.returned !== false || event.prevented) failures.push("Esc with nothing open was handled");
}
el("notify-modal").classList.remove("hidden");
{
  const event = press("Enter");
  if (event.returned !== false || isHidden("notify-modal")) failures.push("Enter closed the notification log");
}
{
  const event = press("Escape", true);
  if (event.returned !== false) failures.push("an Esc already marked handled was handled again");
  if (isHidden("notify-modal")) failures.push("an Esc already marked handled still closed the notification log");
}
el("notify-modal").classList.add("hidden");

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("escape: Esc closes the topmost drawer or dialog, one per press, and leaves an Esc the menu took alone");
