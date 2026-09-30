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
// Every question asked, and how it was asked; answered yes unless a check
// says otherwise.
const questions = [];
let confirmAnswer = () => true;
answerConfirms(document, (question) => {
  questions.push({
    question,
    danger: document.getElementById("confirm-ok").className === "danger",
    focusedCancel: document.activeElement === document.getElementById("confirm-cancel"),
  });
  return confirmAnswer(question);
});

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
// Whether the node answers Overview. A node that does not keeps the last lists
// on screen (#114), which is what the service checks below need: nothing
// running, so a one-press install is the right press.
let reachable = true;
const overview = () => (reachable ? {
  node: { id: "node_local", displayName: "local", platform: "test", fingerprint: "AAAA", autoWake: true },
  sessions, nodes, peers: [], counts: { total: sessions.length }, nodeUrl: "http://127.0.0.1:7462", reachable: true,
} : {
  sessions: [], nodes: [], peers: [], counts: {}, nodeUrl: "http://127.0.0.1:7462", reachable: false,
  error: "dial tcp 127.0.0.1:7462: connection refused",
});

// SetAudience as the node does it: the audience is copied onto each session,
// and the call itself is recorded.
const setCalls = [];
let setAudienceFails = null;
// A browser drops the keyboard from a button that is disabled under it, which
// withBusy does for the length of the write; the shim does not, so the write
// drops it here, and a check can see whether it is given back.
let blurDuringWrite = false;
const SetAudience = async (ids, audience) => {
  setCalls.push({ ids: [...ids], audience: JSON.parse(JSON.stringify(audience)) });
  if (blurDuringWrite) document.activeElement?.blur();
  if (setAudienceFails) return setAudienceFails(ids, audience);
  for (const s of sessions) if (ids.includes(s.id)) s.audience = JSON.parse(JSON.stringify(audience));
  return { changed: ids.length, failed: 0, errors: [] };
};

let serviceAnswer = { supported: true, installed: true, running: true, pid: 7 };
// Set to an error to make the next ServiceStatus read throw.
let serviceReadFails = null;
// Set to { promise, open } to hold the next ServiceStatus reads until open().
let serviceReadGate = null;
const gate = () => {
  let open;
  const promise = new Promise((resolve) => { open = resolve; });
  return { promise, open };
};
let serviceReads = 0;
const serviceCalls = [];
let installFails = false;
let overviewCalls = 0;
let pairRequestsAnswer = [];
const pairDecisions = [];

const { configure, boot } = await import("../src/app.js");
configure({
  Overview: async () => { overviewCalls++; return overview(); },
  ServiceStatus: async () => {
    serviceReads++;
    if (serviceReadGate) await serviceReadGate.promise;
    if (serviceReadFails) throw serviceReadFails;
    return serviceAnswer;
  },
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
// Published to all keeps all.
await pick("codex:allwake", "messages");
if (lastSet()?.audience.mode !== "all_paired" || lastSet()?.audience.autoWake) {
  failures.push(`能留訊息 on an all-paired waking session wrote ${JSON.stringify(lastSet()?.audience)}`);
}
// 不公開 clears the working directory. A flag left on an unpublished session
// is one its row does not show, and the next 能留訊息 would publish the
// directory without the owner seeing it was on.
await pick("codex:chosen", "none");
{
  const want = { mode: "none", nodes: [], exportCwd: false, acceptMessages: false, allowOutbound: false, autoWake: false };
  if (JSON.stringify(lastSet()?.audience) !== JSON.stringify(want)) {
    failures.push(`不公開 wrote ${JSON.stringify(lastSet()?.audience)}, want ${JSON.stringify(want)}`);
  }
  // So publishing it again does not bring the directory back with it.
  openRow("codex:chosen");
  if (menuText().includes(ZH["popover.withCwd"])) failures.push(`a session 不公開 just cleared still says it publishes its directory: ${menuText()}`);
  await press(menuItem("messages"));
  if (lastSet()?.audience.exportCwd !== false) failures.push(`publishing after 不公開 wrote exportCwd ${lastSet()?.audience.exportCwd}`);
}
// 收回公開 on the selection bar is the same 不公開.
app.state.selected.add("codex:silent");
app.state.selected.add("codex:chosen");
sessions.find((s) => s.id === "codex:chosen").audience.exportCwd = true;
await reload();
setCalls.length = 0;
await press(el("btn-unpublish"));
if (setCalls.length !== 1 || setCalls[0].audience.exportCwd !== false || setCalls[0].audience.mode !== "none") {
  failures.push(`收回公開 wrote ${JSON.stringify(setCalls)}, want one none with exportCwd false`);
}
app.state.selected.clear();
app.render();
clearToasts();

// A session nobody can see that still holds exportCwd — older data, or the
// full dialog — is told before the press that publishing takes the directory
// with it; one that does not hold it is told nothing.
sessions = fresh();
await reload();
openRow("codex:quiet");
if (!menuText().includes(ZH["popover.withCwd"])) failures.push(`an unpublished session holding exportCwd is not told: ${menuText()}`);
openRow("claude:only");
if (menuText().includes(ZH["popover.withCwd"]) || menuText().includes("工作目錄")) {
  failures.push(`a session without exportCwd is told it publishes a directory: ${menuText()}`);
}
app.closeAudiencePopover();
// Several, some with it: how many.
for (const id of ["codex:quiet", "claude:only"]) app.state.selected.add(id);
app.render();
el("btn-audience").onclick();
if (!menuText().includes(fill(ZH["popover.withCwdSome.one"], { n: 1 }))) {
  failures.push(`a batch with one directory to publish does not say how many: ${menuText()}`);
}
app.closeAudiencePopover();
app.state.selected.clear();
app.render();

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
  blurDuringWrite = true;
  await pick("codex:chosen", "none");
  blurDuringWrite = false;
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
  // The two get different audiences, so two calls: the node refuses the one
  // for codex:quiet and applies the other.
  setAudienceFails = (ids, audience) => {
    if (ids.includes("codex:quiet")) return { changed: 0, failed: 1, errors: ["codex:quiet: refused by the node"] };
    for (const s of sessions) if (ids.includes(s.id)) s.audience = JSON.parse(JSON.stringify(audience));
    return { changed: ids.length, failed: 0, errors: [] };
  };
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
  // What did go through is undoable: 復原 writes every session's own
  // previous audience, the failed one included (for it, the same value again).
  const undo = toastButtons(toast.node).find((button) => button.textContent === ZH["popover.undo"]);
  setAudienceFails = null;
  if (!undo) {
    failures.push(`a partial failure offers no 復原 for the part that went through: ${toast.textContent}`);
  } else {
    if (audienceOf("codex:silent").acceptMessages !== true) failures.push("the half of the batch that went through was not written");
    const want = fresh().filter((s) => ["codex:quiet", "codex:silent"].includes(s.id)).map((s) => [s.id, s.audience]);
    setCalls.length = 0;
    await press(undo);
    for (const [id, audience] of want) {
      const call = setCalls.find((c) => c.ids.includes(id));
      if (JSON.stringify(call?.audience) !== JSON.stringify(audience)) {
        failures.push(`復原 after a partial failure wrote ${JSON.stringify(call?.audience)} for ${id}, want ${JSON.stringify(audience)}`);
      }
      if (JSON.stringify(audienceOf(id)) !== JSON.stringify(audience)) failures.push(`after 復原 ${id} holds ${JSON.stringify(audienceOf(id))}`);
    }
  }
  app.state.selected.clear();
  app.render();
}
clearToasts();
// A SetAudience call that throws outright counts every session it carried as
// failed (§2), and the calls for the other audiences are still made: the batch
// does not stop half written, and the count is not one per call.
sessions = fresh();
await reload();
{
  // codex:silent and claude:only get the same audience (every paired machine,
  // no directory), so they share one call; codex:quiet has its own.
  setAudienceFails = (ids, audience) => {
    if (ids.length === 2) throw new Error("SetAudience: node went away");
    for (const s of sessions) if (ids.includes(s.id)) s.audience = JSON.parse(JSON.stringify(audience));
    return { changed: ids.length, failed: 0, errors: [] };
  };
  for (const id of ["codex:silent", "claude:only", "codex:quiet"]) app.state.selected.add(id);
  app.render();
  el("btn-audience").onclick();
  setCalls.length = 0;
  await press(menuItem("messages"));
  setAudienceFails = null;
  const toast = latestToast(document);
  const want = fill(ZH["audience.partlyApplied"], {
    action: fill(ZH["popover.verb"], { preset: ZH["popover.messages"] }),
    changed: 1,
    failed: 2,
    error: "Error: SetAudience: node went away",
  });
  if (toast.textContent !== want) failures.push(`a call that threw was reported as ${toast.kind}: ${toast.textContent}, want ${want}`);
  if (setCalls.length !== 2) failures.push(`a call that threw stopped the batch after ${setCalls.length} of 2 calls`);
  if (audienceOf("codex:quiet").acceptMessages !== true) failures.push("the audience after the one that threw was not written");
  if (app.state.selected.size !== 3) failures.push(`a batch with a call that threw left ${app.state.selected.size} selected, want 3`);
  app.state.selected.clear();
  app.render();
}
clearToasts();

// A batch where nothing went through has nothing to undo.
sessions = fresh();
await reload();
{
  setAudienceFails = (ids) => ({ changed: 0, failed: ids.length, errors: ["refused by the node"] });
  await pick("codex:quiet", "messages");
  setAudienceFails = null;
  if (toastButtons(latestToast(document).node).some((button) => button.textContent === ZH["popover.undo"])) {
    failures.push("a batch that changed nothing offered 復原");
  }
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

// The dialog never keeps or publishes a working directory out of sight. It
// opens as the session is (§4), so a directory held by a session nobody can
// see is loaded with it — and the box is in the section a preset's session
// opens folded.
const setDialogMode = (mode) => {
  const radios = document.querySelectorAll('input[name="audience-mode"]');
  for (const radio of radios) radio.checked = radio.value === mode;
  radios.find((radio) => radio.value === mode).onchange();
};
const dialogFor = async (id) => {
  openRow(id);
  await press(menuButtons().at(-1));
};
sessions = fresh();
await reload();
clearToasts();
// (a) Unpublished, holding exportCwd, published from the dialog: the line
// under the presets says the directory goes with it, in the menu's words,
// before 套用 — and what 套用 writes is what it said.
await dialogFor("codex:quiet");
if (el("audience-advanced").open) failures.push("an unpublished session matching no preset opened the advanced section");
if (!el("audience-preset-note").textContent.includes(ZH["audience.noneClearsFlags"])) {
  failures.push(`不公開 with the directory box ticked does not say the flags go off: ${el("audience-preset-note").textContent}`);
}
setDialogMode("all_paired");
app.applyAudiencePreset("messages");
if (!el("audience-preset-note").textContent.includes(ZH["popover.withCwd"])) {
  failures.push(`publishing from the dialog with the directory box folded away says nothing: ${el("audience-preset-note").textContent}`);
}
// Unfolded, the box says it itself; folded again, the line comes back.
el("audience-advanced").open = true;
el("audience-advanced").dispatchEvent({ type: "toggle" });
if (el("audience-preset-note").textContent.includes(ZH["popover.withCwd"])) {
  failures.push("the directory line stays with the box it describes in view");
}
el("audience-advanced").open = false;
el("audience-advanced").dispatchEvent({ type: "toggle" });
if (!el("audience-preset-note").textContent.includes(ZH["popover.withCwd"])) {
  failures.push("folding the section again did not bring the directory line back");
}
setCalls.length = 0;
await press(el("audience-apply"));
{
  const want = { mode: "all_paired", nodes: [], exportCwd: true, acceptMessages: true, allowOutbound: false, autoWake: false };
  if (JSON.stringify(lastSet()?.audience) !== JSON.stringify(want)) {
    failures.push(`the dialog published codex:quiet as ${JSON.stringify(lastSet()?.audience)}, want ${JSON.stringify(want)}`);
  }
}
clearToasts();
// (b) Published with the directory, turned to 不公開 in the dialog: every flag
// off, the directory included, as the menu's 不公開.
await dialogFor("codex:chosen");
if (!el("audience-cwd").checked) failures.push("codex:chosen opened without its own working-directory flag");
setDialogMode("none");
if (!el("audience-preset-note").textContent.includes(ZH["audience.noneClearsFlags"])) {
  failures.push(`turning to 不公開 does not say the flags go off: ${el("audience-preset-note").textContent}`);
}
if (el("audience-preset-note").textContent.includes(ZH["popover.withCwd"])) {
  failures.push("不公開 still says the working directory is published");
}
setCalls.length = 0;
await press(el("audience-apply"));
{
  const want = { mode: "none", nodes: [], exportCwd: false, acceptMessages: false, allowOutbound: false, autoWake: false };
  if (JSON.stringify(lastSet()?.audience) !== JSON.stringify(want)) {
    failures.push(`不公開 from the dialog wrote ${JSON.stringify(lastSet()?.audience)}, want ${JSON.stringify(want)}`);
  }
}
clearToasts();
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

// Supported, nothing installed, nothing running: install, through the
// settings page's own path, with the database path blank (the node's default).
serviceAnswer = { supported: true, installed: false, running: false, pid: 0 };
reachable = false;
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
reachable = true;
await reload();
if (serviceRow()) failures.push("the service row outlived the install");
if (latestToast(document).textContent !== ZH["service.installed"]) failures.push(`the install answered ${latestToast(document).textContent}`);
clearToasts();

// The status on screen is stale — the tick stays away while rows are selected
// or a dialog is open — and `ah service install` replaces a registration. So
// the press reads it again and acts on what it reads: here the service was
// installed and started from a terminal since, and the press installs nothing.
app.state.service = { supported: true, installed: false, running: false, pid: 0 };
serviceAnswer = { supported: true, installed: true, running: true, pid: 21 };
reachable = false;
app.state.nodeReachable = false;
app.state.view = "local";
serviceCalls.length = 0;
{
  const reads = serviceReads;
  await press(el("service-pill"));
  if (serviceReads === reads) failures.push("the one-press fix acted without reading the service status again");
}
if (serviceCalls.length !== 0) failures.push(`a stale "nothing installed" was acted on: ${JSON.stringify(serviceCalls)}`);
if (app.state.view !== "settings") failures.push(`a service found running on the re-read left the view at ${app.state.view}`);
// Found installed and stopped instead: started, not installed over.
app.state.service = { supported: true, installed: false, running: false, pid: 0 };
serviceAnswer = { supported: true, installed: true, running: false, pid: 0 };
app.state.view = "local";
serviceCalls.length = 0;
await press(el("service-pill"));
await settle();
if (JSON.stringify(serviceCalls.map((call) => call[0])) !== JSON.stringify(["RestartService"])) {
  failures.push(`a stale "nothing installed" over a stopped service called ${JSON.stringify(serviceCalls)}, want RestartService`);
}
reachable = true;
await reload();
clearToasts();

// A node running but not as a service was started by hand, on a database this
// window cannot see: installing the default one over it would be a new
// identity. The press goes to the form, which asks.
serviceAnswer = { supported: true, installed: false, running: false, pid: 0 };
await reload();
el("service-form").classList.add("hidden");
app.state.view = "local";
// With the first-run wizard up the strip is hidden whole: the wizard's first
// step is this same fix (first-run.mjs covers the wizard's own path to it).
app.openFirstRun();
if (serviceRow()) failures.push("the first-run wizard is up and the strip repeats its fix beside it");
app.state.firstRun.engaged = false;
app.state.firstRun.forced = false;
app.state.ui.onboardingDismissed = true;
app.render();
{
  const row = serviceRow();
  if (!row) failures.push("a node that is not a service put no row on the strip");
  else {
    if (row.action.textContent !== ZH["attention.service.formAction"]) {
      failures.push(`a running node that is not a service is offered ${row.action.textContent}, want ${ZH["attention.service.formAction"]}`);
    }
    serviceCalls.length = 0;
    await press(row.action);
    await settle();
    if (serviceCalls.length !== 0) failures.push(`a running node that is not a service was installed over: ${JSON.stringify(serviceCalls)}`);
    if (app.state.view !== "settings" || app.state.settingsSection !== "settings-service") {
      failures.push(`a running node that is not a service went to ${app.state.view} / ${app.state.settingsSection}`);
    }
    if (el("service-form").classList.contains("hidden")) failures.push("a running node that is not a service did not open the form");
    // The form says what the field is for here: the --db the node was started
    // with, and what a blank costs. Not 「第一次安裝：留空就用預設位置」, which
    // walked the owner into a new identity.
    if (el("service-db-note").textContent !== ZH["service.dbNoteRunningNotService"]) {
      failures.push(`the form for a running node that is not a service says: ${el("service-db-note").textContent}`);
    }
  }
}
// Installing from that form with the field blank is asked first — red, with
// the keyboard on 取消 — and a 取消 installs nothing.
questions.length = 0;
confirmAnswer = () => false;
serviceCalls.length = 0;
el("service-db").value = "";
await app.installService();
await settle();
if (questions.length !== 1 || !questions[0].question.includes(ZH["service.runningNotServiceConfirmTitle"])) {
  failures.push(`a blank install over a running node asked ${JSON.stringify(questions)}`);
} else if (!questions[0].danger || !questions[0].focusedCancel) {
  failures.push(`a blank install over a running node was not asked as a danger with 取消 focused: ${JSON.stringify(questions[0])}`);
}
if (serviceCalls.length !== 0) failures.push(`取消 on a blank install over a running node still ran ${JSON.stringify(serviceCalls)}`);
// A path typed in is the owner's answer already: no question, and that path.
questions.length = 0;
confirmAnswer = () => true;
el("service-db").value = "/data/by-hand.db";
await app.installService();
await settle();
if (questions.length !== 0) failures.push(`an install over a running node with its path typed asked ${JSON.stringify(questions)}`);
if (serviceCalls.length !== 1 || serviceCalls[0][0] !== "InstallService" || serviceCalls[0][1].dbPath !== "/data/by-hand.db") {
  failures.push(`an install over a running node with its path typed ran ${JSON.stringify(serviceCalls)}`);
}
// And a yes to the question installs, blank.
serviceAnswer = { supported: true, installed: false, running: false, pid: 0 };
await reload();
await app.openServiceForm();
questions.length = 0;
serviceCalls.length = 0;
el("service-db").value = "";
await app.installService();
await settle();
if (questions.length !== 1) failures.push(`a confirmed blank install over a running node asked ${questions.length} questions`);
if (serviceCalls.length !== 1 || serviceCalls[0][0] !== "InstallService" || serviceCalls[0][1].dbPath !== "") {
  failures.push(`a confirmed blank install over a running node ran ${JSON.stringify(serviceCalls)}`);
}
serviceAnswer = { supported: true, installed: false, running: false, pid: 0 };
await reload();
clearToasts();
// The status command's own probe says the same when the last Overview did not.
reachable = false;
await reload();
serviceAnswer = { supported: true, installed: false, running: false, pid: 0, nodeAnswering: true };
el("service-form").classList.add("hidden");
app.state.view = "local";
serviceCalls.length = 0;
await press(el("service-pill"));
await settle();
if (serviceCalls.length !== 0) failures.push(`a node the status says is answering was installed over: ${JSON.stringify(serviceCalls)}`);
if (app.state.view !== "settings") failures.push(`a node the status says is answering went to ${app.state.view}`);
// A re-read that fails decides nothing: the settings page, and a toast that
// says why.
serviceAnswer = { supported: true, installed: false, running: false, pid: 0 };
await reload();
serviceReadFails = new Error("ah service status: exit status 2");
app.state.view = "local";
serviceCalls.length = 0;
clearToasts();
await press(el("service-pill"));
await settle();
serviceReadFails = null;
if (serviceCalls.length !== 0) failures.push(`a failed status read was acted on: ${JSON.stringify(serviceCalls)}`);
if (app.state.view !== "settings") failures.push(`a failed status read left the view at ${app.state.view}`);
{
  const toast = latestToast(document);
  if (toast.kind !== "error" || !toast.textContent.includes("exit status 2")
    || !toast.textContent.startsWith(ZH["service.quickReadFailed"].split("{error}")[0])) {
    failures.push(`a failed status read said ${toast.kind}: ${toast.textContent}`);
  }
}
clearToasts();
reachable = true;
app.state.ui.onboardingDismissed = false;
el("service-form").classList.add("hidden");

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
  serviceAnswer = status;
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
reachable = false;
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
// And nothing happens while something else is being written — not even the
// form being reset under that write, which is what opening it does.
app.state.service = { supported: true, installed: false, running: false, pid: 0 };
el("service-output").textContent = "the write in flight";
el("service-output").classList.remove("hidden");
app.state.view = "local";
app.state.busy = true;
serviceCalls.length = 0;
await press(el("service-pill"));
if (serviceCalls.length !== 0) failures.push(`the pill ran ${JSON.stringify(serviceCalls)} while another write was out`);
if (el("service-output").classList.contains("hidden") || app.state.view !== "local") {
  failures.push("a press while another write was out still opened the service form");
}
app.state.busy = false;
// The re-read is the press's first wait, and can be the status command's
// whole timeout: the button spins and cannot be pressed from the moment it is
// pressed, through a strip render landing mid-read, and is let go once the
// read has answered.
el("service-output").classList.add("hidden");
for (const [name, button] of [["the pill", () => el("service-pill")], ["the strip's row", () => serviceRow()?.action]]) {
  serviceAnswer = { supported: true, installed: true, running: true, pid: 7 };
  app.state.service = { supported: true, installed: true, running: false, pid: 0 };
  app.state.ui.onboardingDismissed = true;
  app.state.view = "local";
  app.render();
  const pressed = button();
  if (!pressed) {
    failures.push(`${name}: no button to press`);
    continue;
  }
  serviceReadGate = gate();
  const done = app.runServiceQuickAction({ button: pressed });
  await settle();
  if (!pressed.classList.contains("busy") || !pressed.disabled) {
    failures.push(`${name} shows nothing while the status is read again (busy ${pressed.classList.contains("busy")}, disabled ${pressed.disabled})`);
  }
  app.renderAttention();
  if (!pressed.disabled) failures.push(`${name}: a render during the re-read made the button pressable again`);
  serviceReadGate.open();
  serviceReadGate = null;
  await done;
  await settle();
  if (pressed.classList.contains("busy") || pressed.disabled) failures.push(`${name} still spins after the re-read found nothing to do`);
}
app.state.ui.onboardingDismissed = false;
// The pill and the strip's row pressed together are one press: the second
// finds the first still reading and does nothing — no second read, no second
// start. Each button's spinner stops only itself, which is how both used to run.
{
  serviceAnswer = { supported: true, installed: true, running: false, pid: 0 };
  app.state.service = { supported: true, installed: true, running: false, pid: 0 };
  app.state.ui.onboardingDismissed = true;
  app.state.view = "local";
  app.render();
  const pill = el("service-pill");
  const rowButton = serviceRow()?.action;
  if (!rowButton) failures.push("no strip row to press alongside the pill");
  serviceCalls.length = 0;
  const reads = serviceReads;
  serviceReadGate = gate();
  const first = app.runServiceQuickAction({ button: pill });
  await settle();
  const second = app.runServiceQuickAction({ button: rowButton });
  const third = app.runServiceQuickAction({ button: pill });
  await settle();
  serviceReadGate.open();
  serviceReadGate = null;
  await Promise.all([first, second, third]);
  await settle();
  if (serviceReads - reads !== 1 + 1) {
    // One re-read by the press, one by the load() after its restart.
    failures.push(`three presses at once read the status ${serviceReads - reads} times, want the one press's re-read and its reload`);
  }
  if (JSON.stringify(serviceCalls.map((call) => call[0])) !== JSON.stringify(["RestartService"])) {
    failures.push(`three presses at once ran ${JSON.stringify(serviceCalls)}, want one RestartService`);
  }
  // Let go once it is done: a later press is a press.
  serviceAnswer = { supported: true, installed: true, running: false, pid: 0 };
  app.state.service = { supported: true, installed: true, running: false, pid: 0 };
  serviceCalls.length = 0;
  await app.runServiceQuickAction({ button: pill });
  await settle();
  if (serviceCalls.length !== 1) failures.push(`a press after the first had finished ran ${JSON.stringify(serviceCalls)}`);
  app.state.ui.onboardingDismissed = false;
  clearToasts();
}
// And a write that starts while the status is being read wins: the press does
// nothing with what it read — not even open the form under that write.
serviceAnswer = { supported: true, installed: false, running: false, pid: 0 };
app.state.service = { supported: true, installed: false, running: false, pid: 0 };
reachable = false;
app.state.nodeReachable = false;
app.state.view = "local";
el("service-form").classList.add("hidden");
el("service-output").textContent = "the write in flight";
el("service-output").classList.remove("hidden");
serviceCalls.length = 0;
serviceReadGate = gate();
{
  const done = app.runServiceQuickAction({ button: el("service-pill") });
  await settle();
  app.state.busy = true;
  serviceReadGate.open();
  serviceReadGate = null;
  await done;
  await settle();
  app.state.busy = false;
}
if (serviceCalls.length !== 0) failures.push(`a write started during the re-read, and the press still ran ${JSON.stringify(serviceCalls)}`);
if (el("service-output").classList.contains("hidden") || !el("service-form").classList.contains("hidden") || app.state.view !== "local") {
  failures.push("a write started during the re-read, and the press still opened the service form");
}
el("service-output").classList.add("hidden");
// The pill keeps its spinner through a status read that lands mid-press.
el("service-pill").classList.add("busy");
await app.loadService();
if (!el("service-pill").classList.contains("busy")) failures.push("a status read took the pill's spinner away");
el("service-pill").classList.remove("busy");
installFails = false;
serviceAnswer = { supported: true, installed: true, running: true, pid: 7 };
reachable = true;
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
