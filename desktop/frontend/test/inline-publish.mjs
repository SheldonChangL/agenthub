// The three-presses round (docs/ui-contract.md §3.2, §3.3, §3.5): publishing a
// session from its own row, the selection bar, the background service fixed
// from the row that says it is broken, the pairing drawer's step strip, and
// the view tabs as buttons.
//
// The owner's rule was that nothing common should take more than three
// presses, and that a published session should never be doing something the
// owner did not choose. So most of what is checked here is the second half:
// which machines a press publishes to, that the working directory is never
// the menu's to change, and that an undo puts back exactly what each session
// had — including the machines it named.
//
// The whole module runs, wiring and intervals included; the intervals are
// captured so the fifteen-second tick fires when the check says, and
// waitForNode's 750ms pauses are answered at once.
//
//   node frontend/test/inline-publish.mjs

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { document } from "./dom-shim.mjs";
import { answerConfirms } from "./fixtures/confirm-dialog.mjs";
import { attentionRows, latestToast, toastButtons, toastNodes } from "./fixtures/toasts.mjs";
import { TEXT as ZH } from "../src/i18n/zh-Hant.js";
import { TEXT as EN } from "../src/i18n/en.js";

globalThis.document = document;
answerConfirms(document, () => true);

const failures = [];
const el = (id) => document.getElementById(id);
const fill = (template, params) => template.replace(/\{(\w+)\}/g, (_, name) => String(params[name]));

const ticks = [];
globalThis.setInterval = (fn, ms) => {
  ticks.push({ fn, ms });
  return ticks.length;
};
const realSetTimeout = globalThis.setTimeout;
globalThis.setTimeout = (fn, ms, ...rest) => realSetTimeout(fn, ms === 750 ? 0 : ms, ...rest);
const settle = () => new Promise((resolve) => realSetTimeout(resolve, 30));

const now = new Date().toISOString();
const session = (id, audience, provider = id.split(":")[0]) => ({
  id, provider, status: "idle", management: "managed", cwd: "/tmp", title: id,
  audience, lastSeenAt: now,
});
const flags = (on = {}) => ({
  exportCwd: Boolean(on.cwd), acceptMessages: Boolean(on.msg), allowOutbound: Boolean(on.out), autoWake: Boolean(on.wake),
});

// Five situations the menu has to tell apart.
const fresh = () => [
  session("codex:quiet", { mode: "none", nodes: [], ...flags({ cwd: true }) }),
  session("codex:chosen", { mode: "selected", nodes: ["node_a", "node_b"], ...flags({ cwd: true, msg: true }) }),
  session("codex:emptychosen", { mode: "selected", nodes: [], ...flags({ msg: true }) }),
  session("codex:allwake", { mode: "all_paired", nodes: [], ...flags({ msg: true, out: true, wake: true }) }),
  session("codex:silent", { mode: "all_paired", nodes: [], ...flags() }),
  session("claude:only", { mode: "none", nodes: [], ...flags() }),
];
let sessions = fresh();
let nodes = [{ nodeId: "node_a", displayName: "alice" }, { nodeId: "node_b", displayName: "bob" }];
const overview = () => ({
  node: { id: "node_local", displayName: "local", platform: "test", fingerprint: "AAAA", autoWake: true },
  sessions, nodes, peers: [], counts: { total: sessions.length }, nodeUrl: "http://127.0.0.1:7462", reachable: true,
});

// SetAudience as the node does it: the audience is copied onto each session,
// and the call itself is recorded.
const setCalls = [];
let setAudienceFails = null;
const SetAudience = async (ids, audience) => {
  setCalls.push({ ids: [...ids], audience: JSON.parse(JSON.stringify(audience)) });
  if (setAudienceFails) return setAudienceFails(ids, audience);
  for (const s of sessions) if (ids.includes(s.id)) s.audience = JSON.parse(JSON.stringify(audience));
  return { changed: ids.length, failed: 0, errors: [] };
};

let serviceAnswer = { supported: true, installed: true, running: true, pid: 7 };
const serviceCalls = [];
let installFails = false;
let overviewCalls = 0;
let pairRequestsAnswer = [];
const pairDecisions = [];

const { configure, boot } = await import("../src/app.js");
configure({
  Overview: async () => { overviewCalls++; return overview(); },
  ServiceStatus: async () => serviceAnswer,
  InboxCounts: async () => ({ ok: true, counts: {} }),
  Inbox: async (sessionId) => ({ sessionId, messages: [], held: 0, capacity: 500 }),
  Pairing: async () => ({ availability: "on", windowAvailable: true, state: { open: true, remainingSeconds: 200 }, candidates: [] }),
  PairRequests: async () => pairRequestsAnswer,
  OpenPairing: async () => ({ open: true }),
  ClosePairing: async () => ({ open: false }),
  NodeSettings: async () => ({ settings: {}, saved: {} }),
  Discover: async () => ({ claude: 0, codex: 0, total: 0, skipped: 0 }),
  SetAudience,
  InstallService: async (form) => {
    serviceCalls.push(["InstallService", form]);
    if (installFails) throw new Error("ah service install: exit status 1: launchctl refused");
    serviceAnswer = { supported: true, installed: true, running: true, pid: 9 };
    return { command: "ah service install", output: "installed" };
  },
  RestartService: async () => {
    serviceCalls.push(["RestartService"]);
    serviceAnswer = { supported: true, installed: true, running: true, pid: 11 };
    return { command: "ah service restart", output: "restarted" };
  },
  RestartNode: async () => {
    serviceCalls.push(["RestartNode"]);
    return { command: "ah service restart", output: "restarted" };
  },
  ApprovePairRequest: async (id) => { pairDecisions.push(["approve", id]); return { id, direction: "incoming", state: "approved", nextStep: "" }; },
  ConfirmPairRequest: async (id) => { pairDecisions.push(["confirm", id]); return { id, direction: "outgoing", state: "approved", nextStep: "" }; },
  RejectPairRequest: async (id) => { pairDecisions.push(["reject", id]); return { id, state: "rejected", nextStep: "" }; },
  TrustNode: async () => ({}), RevokeNode: async () => ({}), Heartbeat: async () => "",
  ClearInbox: async () => ({}), CopyText: async () => ({}), LocalAddresses: async () => [],
});
const app = boot();
await settle();
if (ticks.length !== 4) failures.push(`${ticks.length} intervals registered, want the four §4 allows`);

const clearToasts = () => {
  for (const node of toastNodes(document)) toastButtons(node).at(-1).onclick();
};
const reload = async () => {
  await app.load();
  await app.loadService();
  await settle();
};

const rowFor = (id) => el("rows").children.find((tr) => tr.session?.id === id);
const audienceButton = (id) => rowFor(id)?.sessionParts?.audiencePill;
const walk = (node, visit) => {
  if (!node || typeof node !== "object") return;
  visit(node);
  for (const child of node.children ?? []) walk(child, visit);
};
const menuButtons = () => {
  const found = [];
  walk(el("audience-popover"), (node) => { if (node.tagName === "button") found.push(node); });
  return found;
};
const menuItem = (choice) => menuButtons().find((button) => button.dataset.choice === choice);
const checkedChoices = () => menuButtons()
  .filter((button) => button.getAttribute("aria-checked") === "true").map((button) => button.dataset.choice);
const menuText = () => el("audience-popover").textContent;
const menuOpen = () => app.audiencePopoverOpen();
const press = async (button) => {
  button.onclick();
  await settle();
};
const openRow = (id) => {
  if (menuOpen()) app.closeAudiencePopover();
  audienceButton(id).onclick();
};
const pick = async (id, choice) => {
  openRow(id);
  const item = menuItem(choice);
  if (!item) {
    failures.push(`the menu for ${id} has no ${choice} entry: ${menuText()}`);
    return;
  }
  await press(item);
};
const lastSet = () => setCalls.at(-1);
const audienceOf = (id) => sessions.find((s) => s.id === id).audience;
app.state.view = "local";
app.render();

/* ---------------- 1. which machines, which flags ---------------- */

// An unpublished session goes to every paired machine; the working directory
// it had stays.
setCalls.length = 0;
await pick("codex:quiet", "messages");
{
  const call = lastSet();
  const want = { mode: "all_paired", nodes: [], exportCwd: true, acceptMessages: true, allowOutbound: false, autoWake: false };
  if (!call || JSON.stringify(call.audience) !== JSON.stringify(want) || JSON.stringify(call.ids) !== JSON.stringify(["codex:quiet"])) {
    failures.push(`能留訊息 on an unpublished session wrote ${JSON.stringify(call)}, want ${JSON.stringify(want)} for it alone`);
  }
}
// A session published to chosen machines keeps them, and its mode.
await pick("codex:chosen", "wake");
{
  const call = lastSet();
  const want = { mode: "selected", nodes: ["node_a", "node_b"], exportCwd: true, acceptMessages: true, allowOutbound: true, autoWake: true };
  if (JSON.stringify(call?.audience) !== JSON.stringify(want)) {
    failures.push(`留訊息並喚醒 on a session with chosen machines wrote ${JSON.stringify(call?.audience)}, want ${JSON.stringify(want)}`);
  }
}
// 「指定：無」 is nobody, so publishing it is publishing to every paired machine.
await pick("codex:emptychosen", "messages");
if (lastSet()?.audience.mode !== "all_paired" || lastSet()?.audience.nodes.length !== 0) {
  failures.push(`publishing a 「指定：無」 session kept it on no machines: ${JSON.stringify(lastSet()?.audience)}`);
}
// Published to all keeps all, and 不公開 keeps the working directory too.
await pick("codex:allwake", "messages");
if (lastSet()?.audience.mode !== "all_paired" || lastSet()?.audience.autoWake) {
  failures.push(`能留訊息 on an all-paired waking session wrote ${JSON.stringify(lastSet()?.audience)}`);
}
await pick("codex:chosen", "none");
{
  const want = { mode: "none", nodes: [], exportCwd: true, acceptMessages: false, allowOutbound: false, autoWake: false };
  if (JSON.stringify(lastSet()?.audience) !== JSON.stringify(want)) {
    failures.push(`不公開 wrote ${JSON.stringify(lastSet()?.audience)}, want ${JSON.stringify(want)}`);
  }
}
clearToasts();

/* ---------------- 2. undo puts back each session's own audience ---------- */

sessions = fresh();
await reload();
{
  const before = JSON.parse(JSON.stringify(audienceOf("codex:chosen")));
  await pick("codex:chosen", "none");
  const toast = latestToast(document);
  if (toast.kind !== "ok") failures.push(`a menu choice that went through left a ${toast.kind} toast: ${toast.textContent}`);
  const undo = toastButtons(toast.node).find((button) => button.textContent === ZH["popover.undo"]);
  if (!undo) {
    failures.push(`the success toast has no 復原: ${toast.textContent}`);
  } else {
    setCalls.length = 0;
    await press(undo);
    const call = lastSet();
    if (JSON.stringify(call?.audience) !== JSON.stringify(before)) {
      failures.push(`復原 wrote ${JSON.stringify(call?.audience)}, want the session's own ${JSON.stringify(before)}`);
    }
    if (JSON.stringify(audienceOf("codex:chosen")) !== JSON.stringify(before)) {
      failures.push(`after 復原 the session holds ${JSON.stringify(audienceOf("codex:chosen"))}`);
    }
    if (latestToast(document).textContent !== ZH["popover.undone"]) {
      failures.push(`復原 answered ${latestToast(document).textContent}`);
    }
  }
  // Picking an entry gives the keyboard back to the row's button afterwards.
  if (document.activeElement !== audienceButton("codex:chosen")) failures.push("after a menu choice the keyboard is not on the row's button");
  // An undo pressed while something else is being written says it was not done.
  await pick("codex:chosen", "messages");
  const again = toastButtons(latestToast(document).node).find((button) => button.textContent === ZH["popover.undo"]);
  app.state.busy = true;
  setCalls.length = 0;
  again.onclick();
  await settle();
  app.state.busy = false;
  if (setCalls.length !== 0) failures.push("an undo pressed while busy wrote anyway");
  if (latestToast(document).textContent !== ZH["popover.undoBusy"]) failures.push(`an undo dropped while busy said ${latestToast(document).textContent}`);
}
clearToasts();

// Several sessions with different audiences go back to different audiences:
// one call per distinct audience, each with its own sessions.
sessions = fresh();
await reload();
{
  const ids = ["codex:quiet", "codex:chosen", "codex:allwake"];
  const before = Object.fromEntries(ids.map((id) => [id, JSON.parse(JSON.stringify(audienceOf(id)))]));
  for (const id of ids) app.state.selected.add(id);
  app.render();
  el("btn-audience").onclick();
  if (!menuOpen()) failures.push("「公開 ▾」 on the selection bar did not open the menu");
  setCalls.length = 0;
  await press(menuItem("messages"));
  // Three audiences, because the menu keeps each session's own working
  // directory as well as its machines: quiet (cwd on, every paired machine),
  // chosen (its two machines) and allwake (cwd off, every paired machine).
  if (setCalls.length !== 3 || setCalls.some((call) => call.ids.length !== 1)) {
    failures.push(`a batch of three different outcomes made ${JSON.stringify(setCalls)}, want one call each`);
  }
  // And one call when the outcome is the same.
  for (const id of ["codex:silent", "claude:only"]) app.state.selected.add(id);
  sessions.find((s) => s.id === "claude:only").audience = { mode: "all_paired", nodes: [], ...flags() };
  app.render();
  el("btn-audience").onclick();
  const quietCalls = setCalls.length;
  await press(menuItem("messages"));
  if (setCalls.length !== quietCalls + 1 || setCalls.at(-1).ids.length !== 2) {
    failures.push(`two sessions with the same outcome made ${JSON.stringify(setCalls.slice(quietCalls))}, want one call for both`);
  }
  sessions.find((s) => s.id === "claude:only").audience = { mode: "none", nodes: [], ...flags() };
  sessions.find((s) => s.id === "codex:silent").audience = { mode: "all_paired", nodes: [], ...flags() };
  await reload();
  const undoToast = toastNodes(document).find((node) => toastButtons(node).length > 1
    && node.textContent.includes(fill(ZH["popover.applied.messages.other"], { n: 3 })));
  if (app.state.selected.size !== 0) failures.push("a batch that went through left the selection");
  const undo = toastButtons(undoToast).find((button) => button.textContent === ZH["popover.undo"]);
  setCalls.length = 0;
  await press(undo);
  for (const id of ids) {
    if (JSON.stringify(audienceOf(id)) !== JSON.stringify(before[id])) {
      failures.push(`復原 left ${id} at ${JSON.stringify(audienceOf(id))}, want ${JSON.stringify(before[id])}`);
    }
  }
  if (setCalls.length !== 3) failures.push(`復原 of three different audiences made ${setCalls.length} calls, want 3`);
}
clearToasts();

// The one shape SetAudience refuses — selected with no nodes — goes back as
// none with the same flags: nobody could see it before, nobody can after.
sessions = fresh();
await reload();
{
  await pick("codex:emptychosen", "wake");
  const undo = toastButtons(latestToast(document).node).find((button) => button.textContent === ZH["popover.undo"]);
  setCalls.length = 0;
  await press(undo);
  const want = { mode: "none", nodes: [], exportCwd: false, acceptMessages: true, allowOutbound: false, autoWake: false };
  if (JSON.stringify(lastSet()?.audience) !== JSON.stringify(want)) {
    failures.push(`復原 of a 「指定：無」 session wrote ${JSON.stringify(lastSet()?.audience)}, want ${JSON.stringify(want)}`);
  }
}
clearToasts();

// A batch that partly fails keeps the selection and says the first error.
sessions = fresh();
await reload();
{
  setAudienceFails = (ids) => ({ changed: ids.length - 1, failed: 1, errors: [`${ids[0]}: refused by the node`] });
  app.state.selected.add("codex:quiet");
  app.state.selected.add("codex:silent");
  app.render();
  el("btn-audience").onclick();
  await press(menuItem("messages"));
  if (app.state.selected.size !== 2) failures.push(`a batch that partly failed left ${app.state.selected.size} selected, want the 2 it had`);
  const toast = latestToast(document);
  if (toast.kind !== "error" || !toast.textContent.includes("refused by the node")) {
    failures.push(`a partial failure said ${toast.kind}: ${toast.textContent}`);
  }
  if (toastButtons(toast.node).some((button) => button.textContent === ZH["popover.undo"])) {
    failures.push("a partial failure offered 復原 as if it had worked");
  }
  setAudienceFails = null;
  app.state.selected.clear();
  app.render();
}
clearToasts();

/* ---------------- 3. what the menu says ---------------- */

sessions = fresh();
await reload();
// One row: its own situation ticked, the others not.
openRow("codex:allwake");
if (JSON.stringify(checkedChoices()) !== JSON.stringify(["wake"])) failures.push(`a waking session's menu ticks ${JSON.stringify(checkedChoices())}`);
if (!menuText().includes(ZH["popover.targetAll"])) failures.push(`an all-paired session's menu does not say who: ${menuText()}`);
openRow("codex:chosen");
if (!menuText().includes(fill(ZH["popover.targetSelected.other"], { n: 2 }))) {
  failures.push(`a session with two chosen machines is not told it keeps them: ${menuText()}`);
}
openRow("codex:quiet");
if (JSON.stringify(checkedChoices()) !== JSON.stringify(["none"])) failures.push(`an unpublished session's menu ticks ${JSON.stringify(checkedChoices())}`);
// A published session with every flag off is none of the three.
openRow("codex:silent");
if (checkedChoices().length !== 0) failures.push(`a custom session's menu ticks ${JSON.stringify(checkedChoices())}`);
if (!menuText().includes(ZH["popover.custom"])) failures.push(`a custom session's menu does not say so: ${menuText()}`);
app.closeAudiencePopover();

// Several: a tick only when every one of them is in the same situation.
const bulkMenu = (ids) => {
  app.state.selected.clear();
  for (const id of ids) app.state.selected.add(id);
  app.render();
  if (menuOpen()) app.closeAudiencePopover();
  el("btn-audience").onclick();
};
bulkMenu(["codex:quiet", "claude:only"]);
if (JSON.stringify(checkedChoices()) !== JSON.stringify(["none"])) failures.push(`two unpublished sessions tick ${JSON.stringify(checkedChoices())}`);
if (!menuText().includes(fill(ZH["popover.count.other"], { n: 2 }))) failures.push(`the batch menu does not say how many: ${menuText()}`);
bulkMenu(["codex:quiet", "codex:allwake"]);
if (checkedChoices().length !== 0) failures.push(`two sessions in different situations tick ${JSON.stringify(checkedChoices())}`);
if (!menuText().includes(ZH["popover.mixed"])) failures.push(`a mixed batch is not told it is mixed: ${menuText()}`);
bulkMenu(["codex:quiet", "codex:chosen"]);
if (!menuText().includes(fill(ZH["popover.targetMixed"], { all: 1, kept: 1 }))) {
  failures.push(`a batch of one unpublished and one chosen session is not told where each goes: ${menuText()}`);
}
app.closeAudiencePopover();
app.state.selected.clear();
app.render();

// Claude Code alone cannot be woken, so the entry is there and cannot be picked.
openRow("claude:only");
if (!menuItem("wake")?.disabled) failures.push("a Claude-only menu lets waking be picked");
openRow("codex:quiet");
if (menuItem("wake")?.disabled) failures.push("a Codex session's menu disabled waking");

// The wake entry carries what waking cannot promise.
if (!menuItem("wake")?.textContent.includes(ZH["wake.caveat"])) failures.push(`the wake entry has no caveat: ${menuItem("wake")?.textContent}`);
app.closeAudiencePopover();

// Nothing paired yet: still usable, and it says who will see it, with the way
// to pair.
nodes = [];
await reload();
openRow("codex:quiet");
if (!menuText().includes(ZH["popover.noNodes"])) failures.push(`with nothing paired the menu does not say so: ${menuText()}`);
{
  const pair = menuButtons().find((button) => button.textContent === ZH["popover.pairAction"]);
  if (!pair) failures.push("with nothing paired the menu offers no way to pair");
  else {
    pair.onclick();
    await settle();
    if (app.state.view !== "network") failures.push(`配對另一台機器 left the view at ${app.state.view}`);
    if (menuOpen()) failures.push("配對另一台機器 left the menu open");
    app.closePairingDrawer();
    app.state.view = "local";
    app.render();
  }
}
if (menuItem("messages")?.disabled) failures.push("with nothing paired the menu refused to publish");
// A failed read of the pairing list is not "nothing paired".
app.state.nodesError = "trusted nodes: 500";
openRow("codex:quiet");
if (menuText().includes(ZH["popover.noNodes"])) failures.push("a failed pairing-list read was described as nothing paired");
app.closeAudiencePopover();
app.state.nodesError = "";
nodes = [{ nodeId: "node_a", displayName: "alice" }, { nodeId: "node_b", displayName: "bob" }];
await reload();

/* ---------------- 4. the menu holds still, and answers the keyboard ------- */

openRow("codex:quiet");
if (!app.interactionInProgress()) failures.push("an open menu is not an interaction in progress");
{
  const tick15 = ticks.find((tick) => tick.ms === 15000);
  const before = overviewCalls;
  const anchor = audienceButton("codex:quiet");
  tick15.fn();
  await settle();
  if (overviewCalls !== before) failures.push("the fifteen-second tick read the list with the menu open");
  if (!menuOpen()) failures.push("the fifteen-second tick closed the menu");
  if (audienceButton("codex:quiet") !== anchor) failures.push("the row the menu hangs from was replaced");
}
{
  const key = (name) => {
    let prevented = false;
    app.popoverKey({ key: name, preventDefault: () => { prevented = true; }, stopPropagation() {} });
    return prevented;
  };
  const live = menuButtons().filter((button) => !button.disabled);
  if (document.activeElement !== menuItem("none")) failures.push("the menu opened without the keyboard on the ticked entry");
  key("ArrowDown");
  if (document.activeElement !== menuItem("messages")) failures.push("ArrowDown did not move to the next entry");
  key("ArrowUp");
  key("ArrowUp");
  if (document.activeElement !== live.at(-1)) failures.push("ArrowUp from the first entry did not wrap to the last");
  key("Home");
  if (document.activeElement !== live[0]) failures.push("Home did not go to the first entry");
  const anchor = audienceButton("codex:quiet");
  key("Tab");
  if (menuOpen()) failures.push("Tab left the menu open");
  if (document.activeElement !== anchor) failures.push("Tab out of the menu did not go back to its button");
  openRow("codex:quiet");
  if (!key("Escape")) failures.push("Esc was not taken by the menu");
  if (menuOpen()) failures.push("Esc did not close the menu");
  if (document.activeElement !== anchor) failures.push("Esc did not give the keyboard back to the button");
  if (anchor.getAttribute("aria-expanded") !== "false") failures.push("the closed menu's button still says expanded");
}
// A press outside closes it; a press on the menu or on its own button does not.
openRow("codex:quiet");
app.popoverOutsidePress({ target: menuItem("messages") });
if (!menuOpen()) failures.push("a press inside the menu closed it");
app.popoverOutsidePress({ target: audienceButton("codex:quiet") });
if (!menuOpen()) failures.push("a press on the menu's own button closed it before its click");
app.popoverOutsidePress({ target: el("search") });
if (menuOpen()) failures.push("a press outside the menu left it open");
// A second press on the same button closes it.
openRow("codex:quiet");
audienceButton("codex:quiet").onclick();
if (menuOpen()) failures.push("a second press on the button did not close its menu");

// The last entry is the full dialog, for this row only — and the owner's own
// selection comes back when it closes.
app.state.selected.add("codex:silent");
app.render();
openRow("codex:chosen");
await press(menuButtons().at(-1));
if (el("audience-modal").classList.contains("hidden")) failures.push("指定機器、進階旗標… did not open the dialog");
if (app.state.selected.size !== 1 || !app.state.selected.has("codex:chosen")) {
  failures.push(`the dialog from a row's menu is about ${JSON.stringify([...app.state.selected])}, want that row alone`);
}
if (app.readAudienceForm().mode !== "selected") failures.push("the dialog from a row's menu did not open as that one session");
el("audience-close").onclick();
if (JSON.stringify([...app.state.selected]) !== JSON.stringify(["codex:silent"])) {
  failures.push(`closing the dialog left the selection at ${JSON.stringify([...app.state.selected])}, want the owner's own`);
}
app.state.selected.clear();
app.render();

/* ---------------- 5. the wake caveat ---------------- */

{
  const waking = rowFor("codex:allwake").sessionParts;
  if (waking.wakeCaveat.classList.contains("hidden")) failures.push("a waking row shows no ⚠ in FLAGS");
  if (waking.wakeCaveat.title !== ZH["wake.caveat"]) failures.push(`the ⚠'s title is ${waking.wakeCaveat.title}`);
  if (!rowFor("codex:chosen").sessionParts.wakeCaveat.classList.contains("hidden")) failures.push("a row that does not wake shows the ⚠");
}
await pick("codex:quiet", "wake");
if (!latestToast(document).textContent.includes(ZH["wake.caveat"])) {
  failures.push(`the toast after turning waking on does not carry the caveat: ${latestToast(document).textContent}`);
}
clearToasts();
{
  const markup = fs.readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "index.html"), "utf8");
  const settings = markup.slice(markup.indexOf('id="node-autowake"'), markup.indexOf("</label>", markup.indexOf('id="node-autowake"')));
  if (!settings.includes('data-t="wake.caveat"')) failures.push("the node settings' auto-wake switch does not carry the caveat");
  if (EN["wake.caveat"] === ZH["wake.caveat"] || !/not guaranteed/i.test(EN["wake.caveat"])) {
    failures.push(`the English caveat does not say waking is not guaranteed: ${EN["wake.caveat"]}`);
  }
}

/* ---------------- 6. the background service in one press ---------------- */

const serviceRow = () => attentionRows(document).find((row) => row.row.className.includes("alert")
  && (row.title === ZH["attention.service.stoppedTitle"] || row.title === ZH["attention.service.noneTitle"]));

// Supported, nothing installed: install, through the settings page's own path,
// with the database path blank (the node's default).
serviceAnswer = { supported: true, installed: false, running: false, pid: 0 };
await reload();
if (app.serviceQuickAction() !== "install") failures.push(`nothing installed answers ${app.serviceQuickAction()}, want install`);
serviceCalls.length = 0;
await press(serviceRow().action);
await settle();
if (JSON.stringify(serviceCalls.map((call) => call[0])) !== JSON.stringify(["InstallService"])) {
  failures.push(`the install row called ${JSON.stringify(serviceCalls)}, want InstallService alone`);
} else if (serviceCalls[0][1].dbPath !== "") {
  failures.push(`the one-press install sent the database path ${JSON.stringify(serviceCalls[0][1].dbPath)}, want blank`);
}
await reload();
if (serviceRow()) failures.push("the service row outlived the install");
if (latestToast(document).textContent !== ZH["service.installed"]) failures.push(`the install answered ${latestToast(document).textContent}`);
clearToasts();

// Installed, not running: RestartService, not RestartNode.
serviceAnswer = { supported: true, installed: true, running: false, pid: 0, logHint: "~/node.log" };
await reload();
if (app.serviceQuickAction() !== "restart") failures.push(`installed and stopped answers ${app.serviceQuickAction()}, want restart`);
serviceCalls.length = 0;
// From the title bar's pill this time: the same press.
await press(el("service-pill"));
await settle();
if (JSON.stringify(serviceCalls.map((call) => call[0])) !== JSON.stringify(["RestartService"])) {
  failures.push(`the stopped service's pill called ${JSON.stringify(serviceCalls)}, want RestartService alone`);
}
await reload();
if (serviceRow()) failures.push("the service row outlived the restart");
clearToasts();

// No ah, no service manager, a status not read yet, or nothing to fix: the
// settings page, and nothing run.
for (const [status, label] of [
  [{ toolError: "ah not found", supported: true }, "no ah"],
  [{ supported: false }, "unsupported"],
  [null, "unknown"],
  [{ supported: true, installed: true, running: true, pid: 3 }, "running"],
]) {
  app.state.service = status;
  app.state.view = "local";
  serviceCalls.length = 0;
  await press(el("service-pill"));
  if (serviceCalls.length !== 0) failures.push(`${label}: the pill ran ${JSON.stringify(serviceCalls)}`);
  if (app.state.view !== "settings" || app.state.settingsSection !== "settings-service") {
    failures.push(`${label}: the pill went to ${app.state.view} / ${app.state.settingsSection}`);
  }
}
app.state.view = "local";

// A failed install says so with a way to the settings page.
serviceAnswer = { supported: true, installed: false, running: false, pid: 0 };
installFails = true;
await reload();
clearToasts();
await press(serviceRow().action);
await settle();
{
  const toast = latestToast(document);
  const open = toastButtons(toast.node).find((button) => button.textContent === ZH["service.openSettings"]);
  if (toast.kind !== "error" || !toast.textContent.includes("launchctl refused")) failures.push(`a failed install said ${toast.kind}: ${toast.textContent}`);
  if (!open) failures.push(`a failed install's toast offers no 開啟設定: ${toast.textContent}`);
  else {
    await press(open);
    if (app.state.view !== "settings" || app.state.settingsSection !== "settings-service") {
      failures.push(`開啟設定 went to ${app.state.view} / ${app.state.settingsSection}`);
    }
    // Where ah's own words went, still showing: the settings page's form is
    // not opened again over them.
    if (el("service-output").classList.contains("hidden") || !el("service-output").textContent.includes("launchctl refused")) {
      failures.push(`開啟設定 hid why the install failed: ${el("service-output").textContent}`);
    }
  }
}
// A database path typed into the open form is not reset by a press elsewhere:
// blank is the node's default database, a new identity.
installFails = false;
app.state.view = "local";
await app.openServiceForm();
el("service-db").value = "/data/custom.db";
serviceCalls.length = 0;
await press(el("service-pill"));
if (serviceCalls.length !== 0) failures.push(`with a path typed into the form the pill ran ${JSON.stringify(serviceCalls)}`);
if (el("service-db").value !== "/data/custom.db") failures.push(`the pill reset a typed database path to ${JSON.stringify(el("service-db").value)}`);
if (app.state.view !== "settings") failures.push(`with a path typed the pill went to ${app.state.view}, want the form`);
el("service-db").value = "";
el("service-form").classList.add("hidden");
// And nothing happens while something else is being written.
app.state.busy = true;
serviceCalls.length = 0;
await press(el("service-pill"));
if (serviceCalls.length !== 0) failures.push(`the pill ran ${JSON.stringify(serviceCalls)} while another write was out`);
app.state.busy = false;
// The pill keeps its spinner through a status read that lands mid-press.
el("service-pill").classList.add("busy");
await app.loadService();
if (!el("service-pill").classList.contains("busy")) failures.push("a status read took the pill's spinner away");
el("service-pill").classList.remove("busy");
installFails = false;
serviceAnswer = { supported: true, installed: true, running: true, pid: 7 };
app.state.view = "local";
await reload();
clearToasts();

/* ---------------- 7. the pairing drawer's steps ---------------- */

const row = (direction, state) => ({ id: `pair_${direction}_${state}`, direction, state, nodeId: "node_x", displayName: "x" });
for (const [rows, want] of [
  [[], "find"],
  [[row("outgoing", "rejected"), row("incoming", "expired")], "find"],
  [[row("outgoing", "pending")], "waiting"],
  [[row("outgoing", "awaiting-confirm")], "compare"],
  [[row("incoming", "pending")], "compare"],
  [[row("outgoing", "pending"), row("incoming", "pending")], "compare"],
]) {
  const got = app.pairStepperPhase(rows);
  if (got !== want) failures.push(`the steps read ${got} for ${JSON.stringify(rows.map((r) => `${r.direction}/${r.state}`))}, want ${want}`);
}
{
  const classes = () => (el("pair-stepper").children ?? []).map((item) => item.className);
  app.state.pairRequests = [];
  app.renderPairStepper();
  const items = [...el("pair-stepper").children];
  if (items.length !== 3) failures.push(`the step strip has ${items.length} steps`);
  if (JSON.stringify(classes()) !== JSON.stringify(["pairstepitem now", "pairstepitem", "pairstepitem"])) {
    failures.push(`with nothing sent the steps are ${JSON.stringify(classes())}`);
  }
  app.state.pairRequests = [row("outgoing", "pending")];
  app.renderPairStepper();
  if (JSON.stringify(classes()) !== JSON.stringify(["pairstepitem done", "pairstepitem done", "pairstepitem waiting"])) {
    failures.push(`with a request out the steps are ${JSON.stringify(classes())}`);
  }
  if (!el("pair-stepper").textContent.includes(ZH["pair.stepper.waiting"])) failures.push("a request out does not say it is waiting");
  app.state.pairRequests = [row("incoming", "pending")];
  app.renderPairStepper();
  if (JSON.stringify(classes()) !== JSON.stringify(["pairstepitem done", "pairstepitem done", "pairstepitem now"])) {
    failures.push(`with a request to compare the steps are ${JSON.stringify(classes())}`);
  }
  if ([...el("pair-stepper").children].some((item, index) => item !== items[index])) failures.push("a render replaced the step elements");
  let pressable = false;
  walk(el("pair-stepper"), (node) => { if (node.tagName === "button" || node.onclick) pressable = true; });
  if (pressable) failures.push("the step strip has something to press");
}

// Approving is half of pairing; the toast carries the way to the other half.
{
  pairRequestsAnswer = [row("incoming", "pending")];
  app.state.view = "network";
  app.render();
  await app.decidePairRequest("pair_incoming_pending", "approve");
  await settle();
  const go = toastButtons(latestToast(document).node).find((button) => button.textContent === ZH["pair.goPublish"]);
  if (!go) failures.push(`an approval's toast offers no 去公開 session: ${latestToast(document).textContent}`);
  else {
    await press(go);
    if (app.state.view !== "local") failures.push(`去公開 session left the view at ${app.state.view}`);
  }
  clearToasts();
  await app.decidePairRequest("pair_incoming_pending", "reject");
  await settle();
  if (toastButtons(latestToast(document).node).some((button) => button.textContent === ZH["pair.goPublish"])) {
    failures.push("a rejection offers 去公開 session");
  }
  clearToasts();
  pairRequestsAnswer = [];
}

/* ---------------- 8. the view tabs are buttons ---------------- */

{
  const markup = fs.readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "index.html"), "utf8");
  const nav = /<nav[^>]*id="view-switch"[^>]*>([\s\S]*?)<\/nav>/.exec(markup);
  if (!nav || !/role="tablist"/.test(nav[0].slice(0, nav[0].indexOf(">")))) failures.push("#view-switch is not a tablist");
  const tabs = [...(nav?.[1] ?? "").matchAll(/<([a-z]+)[^>]*\sdata-view="([^"]+)"[^>]*>/g)];
  if (tabs.length !== 3) failures.push(`the view switch has ${tabs.length} tabs`);
  for (const [tag, name, view] of tabs) {
    if (name !== "button" || !/role="tab"/.test(tag)) failures.push(`the ${view} tab is a <${name}> without role=tab`);
  }
  const source = fs.readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "src", "app.js"), "utf8");
  if (source.includes("span[data-view]")) failures.push("app.js still looks for the tabs as spans");

  const shimTabs = document.querySelectorAll("#view-switch [data-view]");
  const byView = (view) => shimTabs.find((tab) => tab.dataset.view === view);
  byView("settings").onclick();
  if (app.state.view !== "settings") failures.push("pressing the settings tab did not open settings");
  if (byView("settings").getAttribute("aria-selected") !== "true" || byView("local").getAttribute("aria-selected") !== "false") {
    failures.push(`aria-selected is ${shimTabs.map((tab) => tab.getAttribute("aria-selected")).join(",")} on settings`);
  }
  if (byView("settings").getAttribute("tabindex") !== "0" || byView("network").getAttribute("tabindex") !== "-1") {
    failures.push("the selected tab is not the strip's one stop for Tab");
  }
  app.viewSwitchKey({ key: "ArrowLeft", preventDefault() {} });
  if (app.state.view !== "network") failures.push(`ArrowLeft from settings went to ${app.state.view}`);
  app.viewSwitchKey({ key: "Home", preventDefault() {} });
  if (app.state.view !== "local") failures.push(`Home went to ${app.state.view}`);
  if (document.activeElement !== byView("local")) failures.push("the arrow keys did not move the keyboard with the tab");
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("inline publish: the menu publishes to the right machines and undoes to each session's own, "
  + "the service is fixed in one press, the pairing steps follow the rows, and the tabs are buttons");
